// Package provider implements the external-dns provider.Provider interface on
// top of Infoblox Universal DDI.
package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider"

	"github.com/wayvz-io/external-dns-uddi-webhook/internal/uddi"
)

// Config configures a Provider.
type Config struct {
	Client uddi.Client
	// View is the DNS view name to manage.
	View         string
	DomainFilter *endpoint.DomainFilter
	DryRun       bool
	// DefaultTTL is applied in AdjustEndpoints to endpoints without a TTL;
	// 0 leaves the TTL unset so UDDI uses the zone default.
	DefaultTTL    int64
	RecordComment string
	Tags          map[string]string
	ZoneCacheTTL  time.Duration
	// ZoneFilter optionally restricts the managed zones by FQDN; empty means no
	// restriction beyond the domain filter.
	ZoneFilter []string
	Logger     *slog.Logger
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Provider is the Universal DDI external-dns provider.
type Provider struct {
	provider.BaseProvider

	client  uddi.Client
	viewID  string
	filter  *endpoint.DomainFilter
	dryRun  bool
	ttl     int64
	comment string
	tags    map[string]string
	log     *slog.Logger
	zones   *zoneCache

	mu    sync.Mutex
	index map[recordKey]indexEntry
}

type recordKey struct {
	name, typ, target string
}

type indexEntry struct {
	id     string
	zoneID string
}

// New resolves the view and returns a ready Provider.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.Client == nil {
		return nil, errors.New("provider: client is required")
	}
	if cfg.View == "" {
		cfg.View = "default"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.DomainFilter == nil {
		cfg.DomainFilter = endpoint.NewDomainFilter(nil)
	}
	viewID, err := cfg.Client.ResolveView(ctx, cfg.View)
	if err != nil {
		return nil, fmt.Errorf("resolve view %q: %w", cfg.View, err)
	}
	p := &Provider{
		client:  cfg.Client,
		viewID:  viewID,
		filter:  cfg.DomainFilter,
		dryRun:  cfg.DryRun,
		ttl:     cfg.DefaultTTL,
		comment: cfg.RecordComment,
		tags:    cfg.Tags,
		log:     cfg.Logger.With("component", "provider", "view", cfg.View),
		zones: &zoneCache{
			client: cfg.Client,
			viewID: viewID,
			filter: cfg.DomainFilter,
			only:   zoneSet(cfg.ZoneFilter),
			ttl:    cfg.ZoneCacheTTL,
			now:    cfg.Now,
		},
		index: map[recordKey]indexEntry{},
	}
	p.log.Info("resolved view", "view_id", viewID)
	return p, nil
}

// ViewID returns the resolved view resource id.
func (p *Provider) ViewID() string { return p.viewID }

// GetDomainFilter implements provider.Provider.
func (p *Provider) GetDomainFilter() endpoint.DomainFilterInterface {
	return p.filter
}

// Records implements provider.Provider.
func (p *Provider) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	zones, err := p.zones.get(ctx)
	if err != nil {
		return nil, p.apiErr(err)
	}
	zonesGauge.Set(float64(len(zones)))

	b := &recordBuilder{groups: map[recordKey]*endpoint.Endpoint{}, index: map[recordKey]indexEntry{}}
	for _, zone := range zones {
		recs, err := p.client.ListRecords(ctx, zone.ID)
		if err != nil {
			return nil, p.apiErr(fmt.Errorf("zone %s: %w", zone.FQDN, err))
		}
		for _, rec := range recs {
			b.add(zone.ID, rec, p.filter, p.log)
		}
	}

	p.mu.Lock()
	p.index = b.index
	p.mu.Unlock()
	recordsGauge.Set(float64(len(b.index)))
	p.log.Debug("listed records", "zones", len(zones), "records", len(b.index), "endpoints", len(b.order))
	return b.order, nil
}

// recordBuilder folds UDDI records into external-dns endpoints and the
// (name,type,target) -> record-id index Records() populates.
type recordBuilder struct {
	order  []*endpoint.Endpoint
	groups map[recordKey]*endpoint.Endpoint
	index  map[recordKey]indexEntry
}

// add folds one record from zoneID into the builder, skipping unsupported
// types, names outside the domain filter, or records with unreadable rdata.
func (b *recordBuilder) add(zoneID string, rec uddi.Record, filter *endpoint.DomainFilter, log *slog.Logger) {
	if !supportedType(rec.Type) {
		return
	}
	name := strings.TrimSuffix(rec.Name, ".")
	if !filter.Match(name) {
		return
	}
	target, err := fromRdata(rec.Type, rec.Rdata)
	if err != nil {
		log.Warn("skipping record with unreadable rdata", "id", rec.ID, "name", name, "type", rec.Type, "error", err)
		return
	}
	b.index[keyOf(name, rec.Type, target)] = indexEntry{id: rec.ID, zoneID: zoneID}
	if rec.Type == endpoint.RecordTypeTXT {
		// The Portal stores a single character-string without its quotes;
		// hand it back in the quoted form AdjustEndpoints and the TXT
		// registry produce, or every TXT would plan as an update forever.
		target = quoteTXT(target)
	}
	gk := keyOf(name, rec.Type, "")
	ep, ok := b.groups[gk]
	if !ok {
		ep = endpoint.NewEndpoint(name, rec.Type)
		if rec.TTL != nil && *rec.TTL > 0 {
			ep.RecordTTL = endpoint.TTL(*rec.TTL)
		}
		b.groups[gk] = ep
		b.order = append(b.order, ep)
	}
	ep.Targets = append(ep.Targets, target)
}

// AdjustEndpoints implements provider.Provider: it drops unsupported record
// types, normalises TXT quoting and applies the default TTL.
func (p *Provider) AdjustEndpoints(endpoints []*endpoint.Endpoint) ([]*endpoint.Endpoint, error) {
	out := make([]*endpoint.Endpoint, 0, len(endpoints))
	for _, ep := range endpoints {
		if ep == nil {
			continue
		}
		if !supportedType(ep.RecordType) {
			p.log.Warn("dropping endpoint with unsupported record type", "name", ep.DNSName, "type", ep.RecordType)
			continue
		}
		ep.DNSName = strings.TrimSuffix(ep.DNSName, ".")
		if ep.RecordType == endpoint.RecordTypeTXT {
			for i, t := range ep.Targets {
				ep.Targets[i] = quoteTXT(t)
			}
		}
		if !ep.RecordTTL.IsConfigured() && p.ttl > 0 {
			ep.RecordTTL = endpoint.TTL(p.ttl)
		}
		out = append(out, ep)
	}
	return out, nil
}

type action string

const (
	actionCreate action = "create"
	actionUpdate action = "update"
	actionDelete action = "delete"
)

type change struct {
	action action
	ep     *endpoint.Endpoint
	target string
}

// ApplyChanges implements provider.Provider.
func (p *Provider) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	if changes == nil {
		return nil
	}
	ops := planChanges(changes)
	if len(ops) == 0 {
		return nil
	}
	zones, err := p.zones.get(ctx)
	if err != nil {
		return p.apiErr(err)
	}

	var soft []error
	for _, op := range ops {
		if err := ctx.Err(); err != nil {
			return err
		}
		abort, err := p.applyOne(ctx, zones, op)
		if abort {
			return p.apiErr(err)
		}
		if err != nil {
			soft = append(soft, err)
		}
	}
	if len(soft) > 0 {
		return provider.NewSoftError(errors.Join(soft...))
	}
	return nil
}

// applyOne applies a single change and updates metrics/logs for it. abort
// reports a retryable infrastructure failure that should stop the whole
// batch; a non-nil err with abort false is a rejected change to collect as a
// soft error and keep going.
func (p *Provider) applyOne(ctx context.Context, zones []uddi.Zone, op change) (abort bool, err error) {
	log := p.log.With("action", string(op.action), "name", op.ep.DNSName, "type", op.ep.RecordType, "target", op.target)
	if !supportedType(op.ep.RecordType) {
		log.Warn("skipping unsupported record type")
		changesTotal.WithLabelValues(string(op.action), "skipped").Inc()
		return false, nil
	}
	zone, ok := findZone(zones, op.ep.DNSName)
	if !ok {
		log.Warn("skipping endpoint: no matching zone in view")
		changesTotal.WithLabelValues(string(op.action), "skipped").Inc()
		return false, nil
	}
	if p.dryRun {
		log.Info("dry-run: would apply change", "zone", zone.FQDN, "ttl", int64(op.ep.RecordTTL))
		changesTotal.WithLabelValues(string(op.action), "dry_run").Inc()
		return false, nil
	}

	switch err := p.apply(ctx, zone, op); {
	case err == nil:
		changesTotal.WithLabelValues(string(op.action), "ok").Inc()
		return false, nil
	case !isInputError(err) && uddi.IsRetryable(err):
		changesTotal.WithLabelValues(string(op.action), "error").Inc()
		return true, err
	default:
		changesTotal.WithLabelValues(string(op.action), "error").Inc()
		apiErrorsTotal.WithLabelValues("false").Inc()
		log.Error("change rejected", "zone", zone.FQDN, "error", err)
		return false, fmt.Errorf("%s %s %s %q: %w", op.action, op.ep.RecordType, op.ep.DNSName, op.target, err)
	}
}

// epKey identifies one logical endpoint across an UpdateOld/UpdateNew pair,
// independent of target values.
type epKey struct{ name, typ, setID string }

func changeKey(ep *endpoint.Endpoint) epKey {
	return epKey{strings.ToLower(strings.TrimSuffix(ep.DNSName, ".")), ep.RecordType, ep.SetIdentifier}
}

// planChanges flattens plan.Changes into per-target operations, ordered
// deletes -> updates -> creates so CNAME/A conflicts resolve cleanly.
func planChanges(changes *plan.Changes) []change {
	old := indexByKey(changes.UpdateOld)
	creates, updates, deletes := diffUpdates(changes.UpdateNew, old)
	// Anything still in old after diffUpdates had no UpdateNew match by key,
	// so the whole endpoint is going away; iterate UpdateOld again (rather
	// than the map) to keep the output order deterministic.
	deletes = append(deletes, unmatchedDeletes(changes.UpdateOld, old)...)
	deletes = append(deletes, deletesFromAll(changes.Delete)...)
	creates = append(creates, createsFromAll(changes.Create)...)

	out := make([]change, 0, len(deletes)+len(updates)+len(creates))
	out = append(out, deletes...)
	out = append(out, updates...)
	out = append(out, creates...)
	return out
}

// diffUpdates matches each UpdateNew endpoint against old by key, removing
// matches from old as it goes, and returns the resulting creates/updates/
// deletes.
func diffUpdates(updateNew []*endpoint.Endpoint, old map[epKey]*endpoint.Endpoint) (creates, updates, deletes []change) {
	for _, ep := range updateNew {
		if ep == nil {
			continue
		}
		k := changeKey(ep)
		prev, ok := old[k]
		if !ok {
			creates = append(creates, createsFor(ep)...)
			continue
		}
		delete(old, k)
		c, u, d := diffTargets(prev, ep)
		creates = append(creates, c...)
		updates = append(updates, u...)
		deletes = append(deletes, d...)
	}
	return creates, updates, deletes
}

// unmatchedDeletes returns deletes for the UpdateOld endpoints still present
// in old (i.e. diffUpdates found no UpdateNew counterpart for them).
func unmatchedDeletes(updateOld []*endpoint.Endpoint, old map[epKey]*endpoint.Endpoint) []change {
	var out []change
	for _, ep := range updateOld {
		if ep == nil {
			continue
		}
		if _, unmatched := old[changeKey(ep)]; unmatched {
			out = append(out, deletesFor(ep)...)
		}
	}
	return out
}

func deletesFromAll(eps []*endpoint.Endpoint) []change {
	var out []change
	for _, ep := range eps {
		if ep != nil {
			out = append(out, deletesFor(ep)...)
		}
	}
	return out
}

func createsFromAll(eps []*endpoint.Endpoint) []change {
	var out []change
	for _, ep := range eps {
		if ep != nil {
			out = append(out, createsFor(ep)...)
		}
	}
	return out
}

func indexByKey(eps []*endpoint.Endpoint) map[epKey]*endpoint.Endpoint {
	m := make(map[epKey]*endpoint.Endpoint, len(eps))
	for _, ep := range eps {
		if ep != nil {
			m[changeKey(ep)] = ep
		}
	}
	return m
}

func createsFor(ep *endpoint.Endpoint) []change {
	out := make([]change, 0, len(ep.Targets))
	for _, t := range ep.Targets {
		out = append(out, change{actionCreate, ep, t})
	}
	return out
}

func deletesFor(ep *endpoint.Endpoint) []change {
	out := make([]change, 0, len(ep.Targets))
	for _, t := range ep.Targets {
		out = append(out, change{actionDelete, ep, t})
	}
	return out
}

// diffTargets compares an endpoint's previous and new target lists and
// returns the creates/updates/deletes needed to converge prev into ep.
func diffTargets(prev, ep *endpoint.Endpoint) (creates, updates, deletes []change) {
	oldTargets := targetSet(prev.Targets)
	newTargets := targetSet(ep.Targets)
	ttlChanged := prev.RecordTTL != ep.RecordTTL
	for _, t := range ep.Targets {
		switch {
		case !oldTargets[t]:
			creates = append(creates, change{actionCreate, ep, t})
		case ttlChanged:
			updates = append(updates, change{actionUpdate, ep, t})
		}
	}
	for _, t := range prev.Targets {
		if !newTargets[t] {
			deletes = append(deletes, change{actionDelete, prev, t})
		}
	}
	return creates, updates, deletes
}

func targetSet(targets []string) map[string]bool {
	m := make(map[string]bool, len(targets))
	for _, t := range targets {
		m[t] = true
	}
	return m
}

func (p *Provider) apply(ctx context.Context, zone uddi.Zone, op change) error {
	switch op.action {
	case actionCreate:
		return p.applyCreate(ctx, zone, op)
	case actionUpdate:
		return p.applyUpdate(ctx, zone, op)
	case actionDelete:
		return p.applyDelete(ctx, zone, op)
	}
	return fmt.Errorf("unknown action %q", op.action)
}

func (p *Provider) applyCreate(ctx context.Context, zone uddi.Zone, op change) error {
	name := strings.TrimSuffix(op.ep.DNSName, ".")
	rdata, err := toRdata(op.ep.RecordType, op.target)
	if err != nil {
		return &inputError{err}
	}
	created, err := p.client.CreateRecord(ctx, uddi.Record{
		Name:    name,
		Type:    op.ep.RecordType,
		Rdata:   rdata,
		TTL:     ttlPtr(op.ep.RecordTTL),
		ZoneID:  zone.ID,
		ViewID:  p.viewID,
		Comment: p.comment,
		Tags:    p.tags,
	})
	if err != nil {
		return err
	}
	p.setIndex(keyOf(name, op.ep.RecordType, op.target), indexEntry{id: created.ID, zoneID: zone.ID})
	p.log.Info("created record", "id", created.ID, "name", name, "type", op.ep.RecordType, "target", op.target)
	return nil
}

func (p *Provider) applyUpdate(ctx context.Context, zone uddi.Zone, op change) error {
	name := strings.TrimSuffix(op.ep.DNSName, ".")
	id, err := p.lookupID(ctx, zone, name, op.ep.RecordType, op.target)
	if err != nil {
		return err
	}
	if id == "" {
		// The record we meant to retune does not exist; create it instead.
		return p.applyCreate(ctx, zone, op)
	}
	rdata, err := toRdata(op.ep.RecordType, op.target)
	if err != nil {
		return &inputError{err}
	}
	if _, err := p.client.UpdateRecord(ctx, uddi.Record{
		ID:      id,
		Name:    name,
		Type:    op.ep.RecordType,
		Rdata:   rdata,
		TTL:     ttlPtr(op.ep.RecordTTL),
		Comment: p.comment,
		Tags:    p.tags,
	}); err != nil {
		return err
	}
	p.log.Info("updated record", "id", id, "name", name, "type", op.ep.RecordType, "target", op.target, "ttl", int64(op.ep.RecordTTL))
	return nil
}

func (p *Provider) applyDelete(ctx context.Context, zone uddi.Zone, op change) error {
	name := strings.TrimSuffix(op.ep.DNSName, ".")
	id, err := p.lookupID(ctx, zone, name, op.ep.RecordType, op.target)
	if err != nil {
		return err
	}
	if id == "" {
		p.log.Warn("record to delete not found; treating as already deleted", "name", name, "type", op.ep.RecordType, "target", op.target)
		return nil
	}
	if err := p.client.DeleteRecord(ctx, id); err != nil {
		var apiErr *uddi.Error
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			p.log.Warn("record already gone", "id", id)
		} else {
			return err
		}
	}
	p.deleteIndex(keyOf(name, op.ep.RecordType, op.target))
	p.log.Info("deleted record", "id", id, "name", name, "type", op.ep.RecordType, "target", op.target)
	return nil
}

// lookupID finds the record id for (name,type,target), refreshing the index
// for the zone on a miss. Returns "" when the record does not exist.
func (p *Provider) lookupID(ctx context.Context, zone uddi.Zone, name, typ, target string) (string, error) {
	k := keyOf(name, typ, target)
	p.mu.Lock()
	e, ok := p.index[k]
	p.mu.Unlock()
	if ok {
		return e.id, nil
	}
	recs, err := p.client.ListRecords(ctx, zone.ID)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, entry := range p.index {
		if entry.zoneID == zone.ID {
			delete(p.index, key)
		}
	}
	for _, rec := range recs {
		if !supportedType(rec.Type) {
			continue
		}
		t, err := fromRdata(rec.Type, rec.Rdata)
		if err != nil {
			continue
		}
		p.index[keyOf(strings.TrimSuffix(rec.Name, "."), rec.Type, t)] = indexEntry{id: rec.ID, zoneID: zone.ID}
	}
	return p.index[k].id, nil
}

func (p *Provider) setIndex(k recordKey, e indexEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.index[k] = e
}

func (p *Provider) deleteIndex(k recordKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.index, k)
}

// InvalidateZones drops the zone cache; the next call re-lists zones.
func (p *Provider) InvalidateZones() { p.zones.invalidate() }

func (p *Provider) apiErr(err error) error {
	retryable := uddi.IsRetryable(err)
	apiErrorsTotal.WithLabelValues(strconv.FormatBool(retryable)).Inc()
	p.log.Error("universal ddi api call failed", "retryable", retryable, "error", err)
	return err
}

// keyOf identifies one (name, type, target) record. TXT targets are keyed in
// their unquoted form: the Portal returns the text without the quotes the
// registry sent, and a delete or update for that record arrives quoted.
func keyOf(name, typ, target string) recordKey {
	if typ == endpoint.RecordTypeTXT {
		target = unquoteTXT(target)
	}
	return recordKey{name: strings.ToLower(strings.TrimSuffix(name, ".")), typ: typ, target: target}
}

func ttlPtr(ttl endpoint.TTL) *int64 {
	if !ttl.IsConfigured() {
		return nil
	}
	v := int64(ttl)
	return &v
}

// inputError marks a change that can never succeed (bad target syntax) so it
// is reported as soft instead of aborting the batch.
type inputError struct{ err error }

func (e *inputError) Error() string { return "invalid input: " + e.err.Error() }
func (e *inputError) Unwrap() error { return e.err }

func isInputError(err error) bool {
	var ie *inputError
	return errors.As(err, &ie)
}
