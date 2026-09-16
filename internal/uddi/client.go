package uddi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	uddiclient "github.com/infobloxopen/universal-ddi-go-client/client"
	"github.com/infobloxopen/universal-ddi-go-client/dnsconfig"
	"github.com/infobloxopen/universal-ddi-go-client/dnsdata"
	"github.com/infobloxopen/universal-ddi-go-client/option"
)

// ClientName is sent in the x-infoblox-client header.
const ClientName = "external-dns-uddi-webhook"

// DefaultPageLimit is the page size used when Options.PageLimit is zero.
const DefaultPageLimit = 1000

// Options configures the real API client.
type Options struct {
	// APIKey is the required Infoblox Portal API key.
	APIKey string
	// PortalURL overrides the default Portal base URL.
	PortalURL string
	// DefaultTags are added to every record that the SDK creates or updates.
	DefaultTags map[string]string
	// PageLimit sets the _limit value for paginated list calls.
	PageLimit int
	// RecordTypes restricts ListRecords to these types. Empty means all types.
	RecordTypes []string
	// HTTPClient optionally overrides the transport.
	HTTPClient *http.Client
}

// APIClient implements Client on top of universal-ddi-go-client.
type APIClient struct {
	dns       *dnsconfig.APIClient
	data      *dnsdata.APIClient
	pageLimit int32
	typeExpr  string
}

var _ Client = (*APIClient)(nil)

// New builds an APIClient.
func New(opts Options) *APIClient {
	clientOpts := []option.ClientOption{
		option.WithAPIKey(opts.APIKey),
		option.WithClientName(ClientName),
	}
	if opts.PortalURL != "" {
		clientOpts = append(clientOpts, option.WithCSPUrl(strings.TrimRight(opts.PortalURL, "/")))
	}
	if len(opts.DefaultTags) > 0 {
		clientOpts = append(clientOpts, option.WithDefaultTags(opts.DefaultTags))
	}
	if opts.HTTPClient != nil {
		clientOpts = append(clientOpts, option.WithHTTPClient(opts.HTTPClient))
	}
	limit := opts.PageLimit
	if limit <= 0 {
		limit = DefaultPageLimit
	}
	c := uddiclient.NewAPIClient(clientOpts...)
	return &APIClient{
		dns:       c.DNSConfigurationAPI,
		data:      c.DNSDataAPI,
		pageLimit: int32(limit),
		typeExpr:  typeFilter(opts.RecordTypes),
	}
}

func typeFilter(types []string) string {
	if len(types) == 0 {
		return ""
	}
	parts := make([]string, 0, len(types))
	for _, t := range types {
		parts = append(parts, fmt.Sprintf("type=='%s'", strings.ToUpper(t)))
	}
	return "(" + strings.Join(parts, " or ") + ")"
}

// ResolveView implements Client.
func (c *APIClient) ResolveView(ctx context.Context, name string) (string, error) {
	resp, httpResp, err := c.dns.ViewAPI.List(ctx).
		Filter(fmt.Sprintf("name==%q", name)).
		Fields("id,name").
		Execute()
	if err != nil {
		return "", wrap("ResolveView", httpResp, err)
	}
	for _, v := range resp.GetResults() {
		if v.Name == name && v.Id != nil && *v.Id != "" {
			return *v.Id, nil
		}
	}
	return "", &Error{Op: "ResolveView", StatusCode: http.StatusNotFound, Err: fmt.Errorf("view %q: %w", name, ErrNotFound)}
}

// ListZones implements Client.
func (c *APIClient) ListZones(ctx context.Context, viewID string) ([]Zone, error) {
	var zones []Zone
	for offset := int32(0); ; offset += c.pageLimit {
		resp, httpResp, err := c.dns.AuthZoneAPI.List(ctx).
			Filter(fmt.Sprintf("view==%q", viewID)).
			Fields("id,fqdn,view,primary_type").
			Offset(offset).
			Limit(c.pageLimit).
			Execute()
		if err != nil {
			return nil, wrap("ListZones", httpResp, err)
		}
		results := resp.GetResults()
		for _, z := range results {
			zone := Zone{
				ID:          deref(z.Id),
				FQDN:        strings.TrimSuffix(deref(z.Fqdn), "."),
				ViewID:      deref(z.View),
				PrimaryType: deref(z.PrimaryType),
			}
			if zone.PrimaryType != "cloud" {
				continue
			}
			zones = append(zones, zone)
		}
		if int32(len(results)) < c.pageLimit {
			return zones, nil
		}
	}
}

// ListRecords implements Client.
func (c *APIClient) ListRecords(ctx context.Context, zoneID string) ([]Record, error) {
	filter := fmt.Sprintf("zone==%q", zoneID)
	if c.typeExpr != "" {
		filter += " and " + c.typeExpr
	}
	var records []Record
	for offset := int32(0); ; offset += c.pageLimit {
		resp, httpResp, err := c.data.RecordAPI.List(ctx).
			Filter(filter).
			Fields("id,absolute_name_spec,type,rdata,ttl,zone,view,comment,tags").
			Offset(offset).
			Limit(c.pageLimit).
			Execute()
		if err != nil {
			return nil, wrap("ListRecords", httpResp, err)
		}
		results := resp.GetResults()
		for i := range results {
			records = append(records, fromSDK(&results[i]))
		}
		if int32(len(results)) < c.pageLimit {
			return records, nil
		}
	}
}

// CreateRecord implements Client.
func (c *APIClient) CreateRecord(ctx context.Context, rec Record) (Record, error) {
	body := dnsdata.Record{
		AbsoluteNameSpec:   ptr(rec.Name + "."),
		View:               ptr(rec.ViewID),
		Type:               ptr(rec.Type),
		Rdata:              rec.Rdata,
		Ttl:                rec.TTL,
		InheritanceSources: ttlInheritance(rec.TTL),
	}
	if rec.Comment != "" {
		body.Comment = ptr(rec.Comment)
	}
	if len(rec.Tags) > 0 {
		body.Tags = toAnyMap(rec.Tags)
	}
	resp, httpResp, err := c.data.RecordAPI.Create(ctx).Body(body).Execute()
	if err != nil {
		return Record{}, wrap("CreateRecord", httpResp, err)
	}
	if resp == nil || resp.Result == nil {
		return Record{}, &Error{Op: "CreateRecord", StatusCode: statusOf(httpResp), Err: fmt.Errorf("empty response body")}
	}
	return fromSDK(resp.Result), nil
}

// UpdateRecord implements Client. It sends only rdata, TTL, and the comment.
// Zone, view, and name changes require a delete and create.
func (c *APIClient) UpdateRecord(ctx context.Context, rec Record) (Record, error) {
	if rec.ID == "" {
		return Record{}, &Error{Op: "UpdateRecord", StatusCode: http.StatusBadRequest, Err: fmt.Errorf("record id is required")}
	}
	body := dnsdata.Record{
		Rdata:              rec.Rdata,
		Ttl:                rec.TTL,
		InheritanceSources: ttlInheritance(rec.TTL),
	}
	if rec.Comment != "" {
		body.Comment = ptr(rec.Comment)
	}
	if len(rec.Tags) > 0 {
		body.Tags = toAnyMap(rec.Tags)
	}
	resp, httpResp, err := c.data.RecordAPI.Update(ctx, rec.ID).Body(body).Execute()
	if err != nil {
		return Record{}, wrap("UpdateRecord", httpResp, err)
	}
	if resp == nil || resp.Result == nil {
		return Record{}, &Error{Op: "UpdateRecord", StatusCode: statusOf(httpResp), Err: fmt.Errorf("empty response body")}
	}
	return fromSDK(resp.Result), nil
}

// DeleteRecord implements Client.
func (c *APIClient) DeleteRecord(ctx context.Context, id string) error {
	if id == "" {
		return &Error{Op: "DeleteRecord", StatusCode: http.StatusBadRequest, Err: fmt.Errorf("record id is required")}
	}
	httpResp, err := c.data.RecordAPI.Delete(ctx, id).Execute()
	return wrap("DeleteRecord", httpResp, err)
}

func fromSDK(r *dnsdata.Record) Record {
	rec := Record{
		ID:      deref(r.Id),
		Name:    strings.TrimSuffix(deref(r.AbsoluteNameSpec), "."),
		Type:    deref(r.Type),
		Rdata:   r.Rdata,
		TTL:     r.Ttl,
		ZoneID:  deref(r.Zone),
		ViewID:  deref(r.View),
		Comment: deref(r.Comment),
	}
	if len(r.Tags) > 0 {
		rec.Tags = make(map[string]string, len(r.Tags))
		for k, v := range r.Tags {
			rec.Tags[k] = fmt.Sprint(v)
		}
	}
	return rec
}

func toAnyMap(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func statusOf(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ptr[T any](v T) *T { return &v }

// ttlInheritance selects the record TTL or the inherited zone default. The
// Portal stores but does not serve a TTL without the override action. A nil TTL
// selects inheritance.
func ttlInheritance(ttl *int64) *dnsdata.RecordInheritance {
	action := "inherit"
	if ttl != nil {
		action = "override"
	}
	return &dnsdata.RecordInheritance{Ttl: &dnsdata.Inheritance2InheritedUInt32{Action: ptr(action)}}
}
