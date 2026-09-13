package uddi

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"sync"
)

// Call records one invocation on the Fake.
type Call struct {
	Method string
	// Arg is the view name, view id, zone id or record id depending on Method.
	Arg string
	// Record is set for CreateRecord/UpdateRecord.
	Record Record
}

// Fake is an in-memory Client for tests. All methods are safe for
// concurrent use.
type Fake struct {
	mu sync.Mutex

	// Views maps view name to view id.
	Views map[string]string
	// Zones are the zones returned by ListZones (filtered by view id).
	Zones []Zone
	// Errors injects a failure for the named method ("ListZones", ...).
	Errors map[string]error

	records map[string]Record
	calls   []Call
	nextID  int
}

var _ Client = (*Fake)(nil)

// NewFake returns an empty Fake with a single view named "default".
func NewFake() *Fake {
	return &Fake{
		Views:   map[string]string{"default": "dns/view/default"},
		Errors:  map[string]error{},
		records: map[string]Record{},
	}
}

// FailWith injects err for the given method (pass nil to clear).
func (f *Fake) FailWith(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.Errors, method)
		return
	}
	f.Errors[method] = err
}

// AddZone registers a zone.
func (f *Fake) AddZone(z Zone) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Zones = append(f.Zones, z)
}

// AddRecord stores a record, assigning an id when missing, and returns it.
func (f *Fake) AddRecord(rec Record) Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.put(rec)
}

func (f *Fake) put(rec Record) Record {
	if rec.ID == "" {
		f.nextID++
		rec.ID = fmt.Sprintf("dns/record/%04d", f.nextID)
	}
	f.records[rec.ID] = rec
	return rec
}

// Records returns a snapshot of stored records sorted by id.
func (f *Fake) Records() []Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Record, 0, len(f.records))
	for _, r := range f.records {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Calls returns a snapshot of recorded calls.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.calls))
	copy(out, f.calls)
	return out
}

// CallsTo returns the recorded calls for one method.
func (f *Fake) CallsTo(method string) []Call {
	var out []Call
	for _, c := range f.Calls() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// ResetCalls clears the call log.
func (f *Fake) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *Fake) record(c Call) error {
	f.calls = append(f.calls, c)
	return f.Errors[c.Method]
}

// ResolveView implements Client.
func (f *Fake) ResolveView(ctx context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(Call{Method: "ResolveView", Arg: name}); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	id, ok := f.Views[name]
	if !ok {
		return "", &Error{Op: "ResolveView", StatusCode: 404, Err: fmt.Errorf("view %q: %w", name, ErrNotFound)}
	}
	return id, nil
}

// ListZones implements Client.
func (f *Fake) ListZones(ctx context.Context, viewID string) ([]Zone, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(Call{Method: "ListZones", Arg: viewID}); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []Zone
	for _, z := range f.Zones {
		if z.ViewID == viewID || z.ViewID == "" {
			out = append(out, z)
		}
	}
	return out, nil
}

// ListRecords implements Client.
func (f *Fake) ListRecords(ctx context.Context, zoneID string) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(Call{Method: "ListRecords", Arg: zoneID}); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []Record
	for _, r := range f.records {
		if r.ZoneID == zoneID {
			out = append(out, cloneRecord(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// CreateRecord implements Client. The zone is inferred from the longest
// registered zone FQDN that is a suffix of the record name.
func (f *Fake) CreateRecord(ctx context.Context, rec Record) (Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(Call{Method: "CreateRecord", Record: cloneRecord(rec)}); err != nil {
		return Record{}, err
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	rec.ID = ""
	if rec.ZoneID == "" {
		rec.ZoneID = f.zoneFor(rec.Name)
	}
	return f.put(cloneRecord(rec)), nil
}

func (f *Fake) zoneFor(name string) string {
	best := ""
	bestLen := -1
	for _, z := range f.Zones {
		if (name == z.FQDN || len(name) > len(z.FQDN) && name[len(name)-len(z.FQDN)-1:] == "."+z.FQDN) && len(z.FQDN) > bestLen {
			best, bestLen = z.ID, len(z.FQDN)
		}
	}
	return best
}

// UpdateRecord implements Client.
func (f *Fake) UpdateRecord(ctx context.Context, rec Record) (Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(Call{Method: "UpdateRecord", Arg: rec.ID, Record: cloneRecord(rec)}); err != nil {
		return Record{}, err
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	cur, ok := f.records[rec.ID]
	if !ok {
		return Record{}, &Error{Op: "UpdateRecord", StatusCode: 404, Err: fmt.Errorf("record %q: %w", rec.ID, ErrNotFound)}
	}
	if rec.Rdata != nil {
		cur.Rdata = maps.Clone(rec.Rdata)
	}
	cur.TTL = rec.TTL
	if rec.Comment != "" {
		cur.Comment = rec.Comment
	}
	if rec.Tags != nil {
		cur.Tags = maps.Clone(rec.Tags)
	}
	f.records[rec.ID] = cur
	return cloneRecord(cur), nil
}

// DeleteRecord implements Client.
func (f *Fake) DeleteRecord(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(Call{Method: "DeleteRecord", Arg: id}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := f.records[id]; !ok {
		return &Error{Op: "DeleteRecord", StatusCode: 404, Err: fmt.Errorf("record %q: %w", id, ErrNotFound)}
	}
	delete(f.records, id)
	return nil
}

func cloneRecord(r Record) Record {
	r.Rdata = maps.Clone(r.Rdata)
	r.Tags = maps.Clone(r.Tags)
	if r.TTL != nil {
		ttl := *r.TTL
		r.TTL = &ttl
	}
	return r
}
