package uddi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testKey = "test-api-key"

type capturedRequest struct {
	Method string
	Path   string
	Query  map[string]string
	Auth   string
	Client string
	Body   map[string]any
}

// portal is a minimal fake of the Infoblox Portal DDI API.
type portal struct {
	mu       sync.Mutex
	views    []map[string]any
	zones    []map[string]any
	records  []map[string]any
	requests []capturedRequest
	nextID   int
	// failures maps "METHOD path-prefix" to a status to return.
	failures map[string]int
	// dropConn closes the connection without responding when set.
	dropConn bool
}

func newPortal() *portal {
	return &portal{failures: map[string]int{}}
}

var quotedFilter = regexp.MustCompile(`(\w+)==["']([^"']*)["']`)

func filterValues(filter string) map[string][]string {
	out := map[string][]string{}
	for _, m := range quotedFilter.FindAllStringSubmatch(filter, -1) {
		out[m[1]] = append(out[m[1]], m[2])
	}
	return out
}

func (p *portal) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()

		if p.dropConn {
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
					return
				}
			}
			panic("cannot hijack")
		}

		req := capturedRequest{Method: r.Method, Path: r.URL.Path, Query: map[string]string{}, Auth: r.Header.Get("Authorization"), Client: r.Header.Get("x-infoblox-client")}
		for k := range r.URL.Query() {
			req.Query[k] = r.URL.Query().Get(k)
		}
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &req.Body)
			}
		}
		p.requests = append(p.requests, req)

		if req.Auth != "Token "+testKey {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		for key, status := range p.failures {
			method, prefix, _ := strings.Cut(key, " ")
			if r.Method == method && strings.HasPrefix(r.URL.Path, prefix) {
				writeJSON(w, status, map[string]any{"error": "injected"})
				return
			}
		}

		const base = "/api/ddi/v1"
		switch {
		case r.Method == http.MethodGet && r.URL.Path == base+"/dns/view":
			writeJSON(w, 200, map[string]any{"results": page(p.views, filterValues(req.Query["_filter"]), req.Query)})
		case r.Method == http.MethodGet && r.URL.Path == base+"/dns/auth_zone":
			writeJSON(w, 200, map[string]any{"results": page(p.zones, filterValues(req.Query["_filter"]), req.Query)})
		case r.Method == http.MethodGet && r.URL.Path == base+"/dns/record":
			writeJSON(w, 200, map[string]any{"results": page(p.records, filterValues(req.Query["_filter"]), req.Query)})
		case r.Method == http.MethodPost && r.URL.Path == base+"/dns/record":
			p.nextID++
			rec := req.Body
			rec["id"] = fmt.Sprintf("dns/record/%d", p.nextID)
			if v, ok := rec["view"]; ok {
				rec["view_name"] = v
			}
			p.records = append(p.records, rec)
			writeJSON(w, 201, map[string]any{"result": rec})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, base+"/dns/record/"):
			id := strings.TrimPrefix(r.URL.Path, base+"/dns/record/")
			for _, rec := range p.records {
				if strings.HasSuffix(fmt.Sprint(rec["id"]), "/"+id) {
					for k, v := range req.Body {
						rec[k] = v
					}
					writeJSON(w, 200, map[string]any{"result": rec})
					return
				}
			}
			writeJSON(w, 404, map[string]any{"error": "no such record"})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, base+"/dns/record/"):
			id := strings.TrimPrefix(r.URL.Path, base+"/dns/record/")
			for i, rec := range p.records {
				if strings.HasSuffix(fmt.Sprint(rec["id"]), "/"+id) {
					p.records = append(p.records[:i], p.records[i+1:]...)
					w.WriteHeader(http.StatusOK)
					return
				}
			}
			writeJSON(w, 404, map[string]any{"error": "no such record"})
		default:
			writeJSON(w, 404, map[string]any{"error": "unknown route " + r.Method + " " + r.URL.Path})
		}
	})
}

// page applies equality filters and _offset/_limit.
func page(items []map[string]any, filters map[string][]string, q map[string]string) []map[string]any {
	var matched []map[string]any
	for _, it := range items {
		ok := true
		for field, values := range filters {
			found := false
			for _, v := range values {
				if fmt.Sprint(it[field]) == v {
					found = true
				}
			}
			if !found {
				ok = false
			}
		}
		if ok {
			matched = append(matched, it)
		}
	}
	offset, _ := strconv.Atoi(q["_offset"])
	limit, _ := strconv.Atoi(q["_limit"])
	if offset > len(matched) {
		offset = len(matched)
	}
	matched = matched[offset:]
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	if matched == nil {
		matched = []map[string]any{}
	}
	return matched
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (p *portal) requestsTo(method, path string) []capturedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []capturedRequest
	for _, r := range p.requests {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func newTestClient(t *testing.T, p *portal, opts Options) *APIClient {
	t.Helper()
	srv := httptest.NewServer(p.handler())
	t.Cleanup(srv.Close)
	opts.APIKey = testKey
	if opts.PortalURL == "" {
		opts.PortalURL = srv.URL + "/"
	}
	return New(opts)
}

func seedPortal(p *portal) {
	p.views = []map[string]any{
		{"id": "dns/view/v1", "name": "default"},
		{"id": "dns/view/v2", "name": "lab"},
	}
	p.zones = []map[string]any{
		{"id": "dns/auth_zone/z1", "fqdn": "example.com.", "view": "dns/view/v1", "primary_type": "cloud"},
		{"id": "dns/auth_zone/z2", "fqdn": "ext.example.com.", "view": "dns/view/v1", "primary_type": "external"},
		{"id": "dns/auth_zone/z3", "fqdn": "lab.example.com.", "view": "dns/view/v2", "primary_type": "cloud"},
	}
	p.records = []map[string]any{
		{"id": "dns/record/r1", "absolute_name_spec": "www.example.com.", "type": "A", "rdata": map[string]any{"address": "10.0.0.1"}, "ttl": 300, "zone": "dns/auth_zone/z1", "view": "dns/view/v1", "comment": "hi", "tags": map[string]any{"external-dns": "true"}},
		{"id": "dns/record/r2", "absolute_name_spec": "example.com.", "type": "SOA", "rdata": map[string]any{"mname": "ns."}, "zone": "dns/auth_zone/z1", "view": "dns/view/v1"},
		{"id": "dns/record/r3", "absolute_name_spec": "cname.example.com.", "type": "CNAME", "rdata": map[string]any{"cname": "www.example.com."}, "zone": "dns/auth_zone/z1", "view": "dns/view/v1"},
	}
}

func TestClientResolveView(t *testing.T) {
	p := newPortal()
	seedPortal(p)
	c := newTestClient(t, p, Options{})
	ctx := context.Background()

	id, err := c.ResolveView(ctx, "lab")
	require.NoError(t, err)
	assert.Equal(t, "dns/view/v2", id)

	reqs := p.requestsTo(http.MethodGet, "/api/ddi/v1/dns/view")
	require.Len(t, reqs, 1)
	assert.Equal(t, `name=="lab"`, reqs[0].Query["_filter"])
	assert.Equal(t, "id,name", reqs[0].Query["_fields"])
	assert.Equal(t, "Token "+testKey, reqs[0].Auth)
	assert.Equal(t, ClientName, reqs[0].Client)

	_, err = c.ResolveView(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, 404, e.StatusCode)
	assert.False(t, e.Retryable())
}

func TestClientUnauthorizedIsNotRetryable(t *testing.T) {
	p := newPortal()
	seedPortal(p)
	srv := httptest.NewServer(p.handler())
	t.Cleanup(srv.Close)
	c := New(Options{APIKey: "wrong", PortalURL: srv.URL})

	_, err := c.ResolveView(context.Background(), "default")
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, http.StatusUnauthorized, e.StatusCode)
	assert.False(t, IsRetryable(err))
	assert.Equal(t, "ResolveView", e.Op)
}

func TestClientListZonesPagingAndFilter(t *testing.T) {
	p := newPortal()
	seedPortal(p)
	for i := 0; i < 4; i++ {
		p.zones = append(p.zones, map[string]any{"id": fmt.Sprintf("dns/auth_zone/extra%d", i), "fqdn": fmt.Sprintf("z%d.example.com.", i), "view": "dns/view/v1", "primary_type": "cloud"})
	}
	c := newTestClient(t, p, Options{PageLimit: 2})

	zones, err := c.ListZones(context.Background(), "dns/view/v1")
	require.NoError(t, err)
	// z1 + 4 extra are cloud; z2 is external and dropped; z3 is another view.
	require.Len(t, zones, 5)
	assert.Equal(t, Zone{ID: "dns/auth_zone/z1", FQDN: "example.com", ViewID: "dns/view/v1", PrimaryType: "cloud"}, zones[0])

	reqs := p.requestsTo(http.MethodGet, "/api/ddi/v1/dns/auth_zone")
	// 6 matching zones with page size 2 -> pages at 0,2,4 return 2 each, page at 6 returns 0.
	require.Len(t, reqs, 4)
	assert.Equal(t, []string{"0", "2", "4", "6"}, offsets(reqs))
	for _, r := range reqs {
		assert.Equal(t, `view=="dns/view/v1"`, r.Query["_filter"])
		assert.Equal(t, "2", r.Query["_limit"])
		assert.Equal(t, "id,fqdn,view,primary_type", r.Query["_fields"])
	}
}

func offsets(reqs []capturedRequest) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Query["_offset"])
	}
	return out
}

func TestClientListRecords(t *testing.T) {
	p := newPortal()
	seedPortal(p)
	c := newTestClient(t, p, Options{RecordTypes: []string{"A", "cname"}})

	recs, err := c.ListRecords(context.Background(), "dns/auth_zone/z1")
	require.NoError(t, err)
	require.Len(t, recs, 2, "SOA filtered out by the type filter")

	ttl := int64(300)
	assert.Equal(t, Record{
		ID: "dns/record/r1", Name: "www.example.com", Type: "A",
		Rdata: map[string]any{"address": "10.0.0.1"}, TTL: &ttl,
		ZoneID: "dns/auth_zone/z1", ViewID: "dns/view/v1", Comment: "hi",
		Tags: map[string]string{"external-dns": "true"},
	}, recs[0])
	assert.Equal(t, "cname.example.com", recs[1].Name)
	assert.Nil(t, recs[1].TTL)
	assert.Nil(t, recs[1].Tags)

	reqs := p.requestsTo(http.MethodGet, "/api/ddi/v1/dns/record")
	require.Len(t, reqs, 1)
	assert.Equal(t, `zone=="dns/auth_zone/z1" and (type=='A' or type=='CNAME')`, reqs[0].Query["_filter"])
	assert.Equal(t, strconv.Itoa(DefaultPageLimit), reqs[0].Query["_limit"])
	assert.Equal(t, "0", reqs[0].Query["_offset"])
}

func TestClientListRecordsPaging(t *testing.T) {
	p := newPortal()
	seedPortal(p)
	p.records = nil
	for i := 0; i < 5; i++ {
		p.records = append(p.records, map[string]any{"id": fmt.Sprintf("dns/record/%d", i), "absolute_name_spec": fmt.Sprintf("h%d.example.com.", i), "type": "A", "rdata": map[string]any{"address": fmt.Sprintf("10.0.0.%d", i)}, "zone": "dns/auth_zone/z1"})
	}
	c := newTestClient(t, p, Options{PageLimit: 2})
	recs, err := c.ListRecords(context.Background(), "dns/auth_zone/z1")
	require.NoError(t, err)
	assert.Len(t, recs, 5)
	reqs := p.requestsTo(http.MethodGet, "/api/ddi/v1/dns/record")
	assert.Equal(t, []string{"0", "2", "4"}, offsets(reqs), "short last page stops paging")
	assert.Equal(t, `zone=="dns/auth_zone/z1"`, reqs[0].Query["_filter"], "no type filter when RecordTypes empty")
}

func TestClientCreateUpdateDelete(t *testing.T) {
	p := newPortal()
	seedPortal(p)
	c := newTestClient(t, p, Options{DefaultTags: map[string]string{"external-dns": "true"}})
	ctx := context.Background()

	ttl := int64(120)
	created, err := c.CreateRecord(ctx, Record{
		Name: "new.example.com", Type: "CNAME", Rdata: map[string]any{"cname": "www.example.com."},
		TTL: &ttl, ViewID: "dns/view/v1", Comment: "managed", Tags: map[string]string{"team": "infra"},
	})
	require.NoError(t, err)
	assert.Equal(t, "dns/record/1", created.ID)
	assert.Equal(t, "new.example.com", created.Name)
	assert.Equal(t, "CNAME", created.Type)
	assert.Equal(t, int64(120), *created.TTL)

	posts := p.requestsTo(http.MethodPost, "/api/ddi/v1/dns/record")
	require.Len(t, posts, 1)
	body := posts[0].Body
	assert.Equal(t, "new.example.com.", body["absolute_name_spec"], "trailing dot added on the wire")
	assert.Equal(t, "dns/view/v1", body["view"])
	assert.Equal(t, "CNAME", body["type"])
	assert.Equal(t, "managed", body["comment"])
	assert.Equal(t, map[string]any{"cname": "www.example.com."}, body["rdata"])
	assert.Equal(t, map[string]any{"team": "infra", "external-dns": "true"}, body["tags"], "SDK merges default tags")
	_, hasZone := body["zone"]
	assert.False(t, hasZone, "zone is derived from absolute_name_spec + view")
	assert.Equal(t, map[string]any{"ttl": map[string]any{"action": "override"}}, body["inheritance_sources"], "an explicit TTL must override the zone default or it is stored but never served")

	ttl2 := int64(60)
	updated, err := c.UpdateRecord(ctx, Record{ID: created.ID, Rdata: map[string]any{"cname": "www2.example.com."}, TTL: &ttl2, Comment: "changed"})
	require.NoError(t, err)
	assert.Equal(t, int64(60), *updated.TTL)
	assert.Equal(t, "www2.example.com.", updated.Rdata["cname"])
	// The SDK strips the "dns/record/" prefix and sends only the bare id in the path.
	patches := p.requestsTo(http.MethodPatch, "/api/ddi/v1/dns/record/1")
	require.Len(t, patches, 1)
	_, hasName := patches[0].Body["absolute_name_spec"]
	assert.False(t, hasName, "immutable fields are not patched")
	assert.Equal(t, map[string]any{"ttl": map[string]any{"action": "override"}}, patches[0].Body["inheritance_sources"])
	_, hasView := patches[0].Body["view"]
	assert.False(t, hasView)

	_, err = c.UpdateRecord(ctx, Record{ID: "dns/record/nope", Rdata: map[string]any{}})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, 404, e.StatusCode)
	assert.False(t, e.Retryable())

	require.NoError(t, c.DeleteRecord(ctx, created.ID))
	dels := p.requestsTo(http.MethodDelete, "/api/ddi/v1/dns/record/1")
	require.Len(t, dels, 1)

	err = c.DeleteRecord(ctx, created.ID)
	require.ErrorAs(t, err, &e)
	assert.Equal(t, 404, e.StatusCode)

	_, err = c.UpdateRecord(ctx, Record{})
	require.ErrorAs(t, err, &e)
	assert.Equal(t, http.StatusBadRequest, e.StatusCode)
	err = c.DeleteRecord(ctx, "")
	require.ErrorAs(t, err, &e)
	assert.Equal(t, http.StatusBadRequest, e.StatusCode)
}

func TestClientServerErrorsAreRetryable(t *testing.T) {
	p := newPortal()
	seedPortal(p)
	p.failures["GET /api/ddi/v1/dns/auth_zone"] = 503
	p.failures["GET /api/ddi/v1/dns/record"] = 500
	p.failures["POST /api/ddi/v1/dns/record"] = 502
	p.failures["PATCH /api/ddi/v1/dns/record/"] = 504
	p.failures["DELETE /api/ddi/v1/dns/record/"] = 500
	c := newTestClient(t, p, Options{})
	ctx := context.Background()

	_, err := c.ListZones(ctx, "dns/view/v1")
	assertRetryable(t, err, 503, "ListZones")
	_, err = c.ListRecords(ctx, "dns/auth_zone/z1")
	assertRetryable(t, err, 500, "ListRecords")
	_, err = c.CreateRecord(ctx, Record{Name: "a.example.com", Type: "A", Rdata: map[string]any{"address": "10.0.0.9"}})
	assertRetryable(t, err, 502, "CreateRecord")
	_, err = c.UpdateRecord(ctx, Record{ID: "dns/record/r1", Rdata: map[string]any{}})
	assertRetryable(t, err, 504, "UpdateRecord")
	err = c.DeleteRecord(ctx, "dns/record/r1")
	assertRetryable(t, err, 500, "DeleteRecord")
}

func assertRetryable(t *testing.T, err error, status int, op string) {
	t.Helper()
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, status, e.StatusCode)
	assert.Equal(t, op, e.Op)
	assert.True(t, e.Retryable())
}

func TestClientNetworkErrorIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := New(Options{APIKey: testKey, PortalURL: url})
	_, err := c.ResolveView(context.Background(), "default")
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, 0, e.StatusCode)
	assert.True(t, e.Retryable())
}

func TestClientCancelledContext(t *testing.T) {
	p := newPortal()
	seedPortal(p)
	c := newTestClient(t, p, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.ListZones(ctx, "dns/view/v1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "got %v", err)
	assert.False(t, IsRetryable(err))
}

func TestClientEmptyResultBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{})
	}))
	t.Cleanup(srv.Close)
	c := New(Options{APIKey: testKey, PortalURL: srv.URL, HTTPClient: srv.Client()})
	ctx := context.Background()
	_, err := c.CreateRecord(ctx, Record{Name: "a.example.com", Type: "A", Rdata: map[string]any{"address": "10.0.0.9"}})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, 200, e.StatusCode)
	_, err = c.UpdateRecord(ctx, Record{ID: "dns/record/x", Rdata: map[string]any{}})
	require.ErrorAs(t, err, &e)
	assert.Equal(t, 200, e.StatusCode)
	assert.Equal(t, 0, statusOf(nil))
}

func TestTypeFilter(t *testing.T) {
	assert.Equal(t, "", typeFilter(nil))
	assert.Equal(t, "(type=='A')", typeFilter([]string{"a"}))
	assert.Equal(t, "(type=='A' or type=='TXT')", typeFilter([]string{"A", "TXT"}))
}

func TestClientTTLInheritance(t *testing.T) {
	p := newPortal()
	seedPortal(p)
	c := newTestClient(t, p, Options{})
	ctx := context.Background()

	created, err := c.CreateRecord(ctx, Record{Name: "nottl.example.com", Type: "A", Rdata: map[string]any{"address": "10.0.0.9"}, ViewID: "dns/view/v1"})
	require.NoError(t, err)
	posts := p.requestsTo(http.MethodPost, "/api/ddi/v1/dns/record")
	require.Len(t, posts, 1)
	_, hasTTL := posts[0].Body["ttl"]
	assert.False(t, hasTTL, "no ttl sent when unset")
	assert.Equal(t, map[string]any{"ttl": map[string]any{"action": "inherit"}}, posts[0].Body["inheritance_sources"])

	_, err = c.UpdateRecord(ctx, Record{ID: created.ID, Rdata: map[string]any{"address": "10.0.0.10"}})
	require.NoError(t, err)
	patches := p.requestsTo(http.MethodPatch, "/api/ddi/v1/dns/record/"+strings.TrimPrefix(created.ID, "dns/record/"))
	require.Len(t, patches, 1)
	assert.Equal(t, map[string]any{"ttl": map[string]any{"action": "inherit"}}, patches[0].Body["inheritance_sources"], "clearing the TTL hands control back to the zone default")
}
