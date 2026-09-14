package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider"

	"github.com/wayvz-io/external-dns-uddi-webhook/internal/uddi"
)

const (
	viewID = "dns/view/default"
	zone1  = "dns/auth_zone/example-com"
	zone2  = "dns/auth_zone/sub-example-com"
)

type harness struct {
	fake *uddi.Fake
	prov *Provider
	logs *bytes.Buffer
}

func newHarness(t *testing.T, mutate func(*Config)) *harness {
	t.Helper()
	fake := uddi.NewFake()
	fake.AddZone(uddi.Zone{ID: zone1, FQDN: "example.com", ViewID: viewID, PrimaryType: "cloud"})
	fake.AddZone(uddi.Zone{ID: zone2, FQDN: "sub.example.com", ViewID: viewID, PrimaryType: "cloud"})
	fake.AddZone(uddi.Zone{ID: "dns/auth_zone/other", FQDN: "other.net", ViewID: "dns/view/other", PrimaryType: "cloud"})

	logs := &bytes.Buffer{}
	cfg := Config{
		Client:        fake,
		View:          "default",
		RecordComment: "managed by external-dns",
		Tags:          map[string]string{"external-dns": "true"},
		ZoneCacheTTL:  time.Minute,
		Logger:        slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	p, err := New(context.Background(), cfg)
	require.NoError(t, err)
	fake.ResetCalls()
	return &harness{fake: fake, prov: p, logs: logs}
}

func ttlOf(v int64) *int64 { return &v }

func seedRecords(f *uddi.Fake) {
	f.AddRecord(uddi.Record{Name: "www.example.com", Type: "A", Rdata: map[string]any{"address": "10.0.0.1"}, TTL: ttlOf(300), ZoneID: zone1})
	f.AddRecord(uddi.Record{Name: "www.example.com", Type: "A", Rdata: map[string]any{"address": "10.0.0.2"}, TTL: ttlOf(300), ZoneID: zone1})
	f.AddRecord(uddi.Record{Name: "www.example.com", Type: "AAAA", Rdata: map[string]any{"address": "2001:db8::1"}, ZoneID: zone1})
	f.AddRecord(uddi.Record{Name: "alias.example.com", Type: "CNAME", Rdata: map[string]any{"cname": "www.example.com."}, ZoneID: zone1})
	f.AddRecord(uddi.Record{Name: "a-www.example.com", Type: "TXT", Rdata: map[string]any{"text": `"heritage=external-dns,external-dns/owner=k8s"`}, ZoneID: zone1})
	f.AddRecord(uddi.Record{Name: "_sip._tcp.example.com", Type: "SRV", Rdata: map[string]any{"priority": float64(10), "weight": float64(5), "port": float64(5060), "target": "sip.example.com."}, ZoneID: zone1})
	f.AddRecord(uddi.Record{Name: "example.com", Type: "MX", Rdata: map[string]any{"preference": float64(10), "exchange": "mail.example.com."}, ZoneID: zone1})
	f.AddRecord(uddi.Record{Name: "deleg.example.com", Type: "NS", Rdata: map[string]any{"dname": "ns1.example.net."}, ZoneID: zone1})
	f.AddRecord(uddi.Record{Name: "example.com", Type: "SOA", Rdata: map[string]any{"mname": "ns."}, ZoneID: zone1})
	f.AddRecord(uddi.Record{Name: "broken.example.com", Type: "A", Rdata: map[string]any{}, ZoneID: zone1})
	f.AddRecord(uddi.Record{Name: "app.sub.example.com", Type: "A", Rdata: map[string]any{"address": "10.1.0.1"}, ZoneID: zone2})
}

func findEndpoint(eps []*endpoint.Endpoint, name, typ string) *endpoint.Endpoint {
	for _, ep := range eps {
		if ep.DNSName == name && ep.RecordType == typ {
			return ep
		}
	}
	return nil
}

func TestNew(t *testing.T) {
	t.Run("requires client", func(t *testing.T) {
		_, err := New(context.Background(), Config{})
		require.Error(t, err)
	})
	t.Run("view resolution failure", func(t *testing.T) {
		fake := uddi.NewFake()
		_, err := New(context.Background(), Config{Client: fake, View: "missing"})
		require.ErrorIs(t, err, uddi.ErrNotFound)
	})
	t.Run("defaults", func(t *testing.T) {
		fake := uddi.NewFake()
		p, err := New(context.Background(), Config{Client: fake})
		require.NoError(t, err)
		assert.Equal(t, viewID, p.ViewID())
		assert.Equal(t, "default", fake.CallsTo("ResolveView")[0].Arg)
		df, ok := p.GetDomainFilter().(*endpoint.DomainFilter)
		require.True(t, ok)
		assert.False(t, df.IsConfigured())
		assert.True(t, df.Match("anything"))
	})
}

func TestRecords(t *testing.T) {
	h := newHarness(t, nil)
	seedRecords(h.fake)
	ctx := context.Background()

	eps, err := h.prov.Records(ctx)
	require.NoError(t, err)

	a := findEndpoint(eps, "www.example.com", "A")
	require.NotNil(t, a)
	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2"}, a.Targets)
	assert.Equal(t, endpoint.TTL(300), a.RecordTTL)

	aaaa := findEndpoint(eps, "www.example.com", "AAAA")
	require.NotNil(t, aaaa)
	assert.Equal(t, endpoint.Targets{"2001:db8::1"}, aaaa.Targets)
	assert.Equal(t, endpoint.TTL(0), aaaa.RecordTTL)

	assert.Equal(t, endpoint.Targets{"www.example.com"}, findEndpoint(eps, "alias.example.com", "CNAME").Targets, "trailing dot stripped")
	assert.Equal(t, endpoint.Targets{`"heritage=external-dns,external-dns/owner=k8s"`}, findEndpoint(eps, "a-www.example.com", "TXT").Targets, "TXT returned as stored")
	assert.Equal(t, endpoint.Targets{"10 5 5060 sip.example.com"}, findEndpoint(eps, "_sip._tcp.example.com", "SRV").Targets)
	assert.Equal(t, endpoint.Targets{"10 mail.example.com"}, findEndpoint(eps, "example.com", "MX").Targets)
	assert.Equal(t, endpoint.Targets{"ns1.example.net"}, findEndpoint(eps, "deleg.example.com", "NS").Targets)
	assert.Equal(t, endpoint.Targets{"10.1.0.1"}, findEndpoint(eps, "app.sub.example.com", "A").Targets)

	assert.Nil(t, findEndpoint(eps, "example.com", "SOA"), "unsupported type skipped")
	assert.Nil(t, findEndpoint(eps, "broken.example.com", "A"), "unreadable rdata skipped")
	assert.Len(t, eps, 8)
	assert.Contains(t, h.logs.String(), "unreadable rdata")

	// Both zones listed, other view's zone never touched.
	lists := h.fake.CallsTo("ListRecords")
	require.Len(t, lists, 2)
	assert.ElementsMatch(t, []string{zone1, zone2}, []string{lists[0].Arg, lists[1].Arg})
	assert.Len(t, h.fake.CallsTo("ListZones"), 1)

	// Second call hits the zone cache.
	_, err = h.prov.Records(ctx)
	require.NoError(t, err)
	assert.Len(t, h.fake.CallsTo("ListZones"), 1)
}

func TestRecordsDomainFilter(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.DomainFilter = endpoint.NewDomainFilter([]string{"sub.example.com"}) })
	seedRecords(h.fake)
	eps, err := h.prov.Records(context.Background())
	require.NoError(t, err)
	require.Len(t, eps, 1)
	assert.Equal(t, "app.sub.example.com", eps[0].DNSName)
	// example.com is kept as a parent zone, so both zones are listed, but
	// its out-of-filter records are dropped.
	assert.Len(t, h.fake.CallsTo("ListRecords"), 2)
}

func TestRecordsErrors(t *testing.T) {
	h := newHarness(t, nil)
	h.fake.FailWith("ListZones", &uddi.Error{Op: "ListZones", StatusCode: 503, Err: errors.New("down")})
	_, err := h.prov.Records(context.Background())
	require.Error(t, err)
	assert.True(t, uddi.IsRetryable(err))
	h.fake.FailWith("ListZones", nil)

	h.fake.FailWith("ListRecords", &uddi.Error{Op: "ListRecords", StatusCode: 403, Err: errors.New("forbidden")})
	_, err = h.prov.Records(context.Background())
	require.Error(t, err)
	assert.False(t, uddi.IsRetryable(err))
	assert.Contains(t, err.Error(), "example.com")
}

func TestAdjustEndpoints(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.DefaultTTL = 120 })
	in := []*endpoint.Endpoint{
		endpoint.NewEndpoint("txt.example.com.", "TXT", "heritage=external-dns,external-dns/owner=k8s", `"already quoted"`),
		endpoint.NewEndpointWithTTL("www.example.com", "A", 60, "10.0.0.1"),
		endpoint.NewEndpoint("v6.example.com", "AAAA", "2001:db8::1"),
		endpoint.NewEndpoint("ptr.example.com", "PTR", "x"),
		nil,
	}
	out, err := h.prov.AdjustEndpoints(in)
	require.NoError(t, err)
	require.Len(t, out, 3)
	assert.Equal(t, "txt.example.com", out[0].DNSName, "trailing dot stripped")
	assert.Equal(t, endpoint.Targets{`"heritage=external-dns,external-dns/owner=k8s"`, `"already quoted"`}, out[0].Targets)
	assert.Equal(t, endpoint.TTL(120), out[0].RecordTTL, "default TTL applied")
	assert.Equal(t, endpoint.TTL(60), out[1].RecordTTL, "explicit TTL kept")
	assert.Equal(t, endpoint.TTL(120), out[2].RecordTTL)
	assert.Contains(t, h.logs.String(), "unsupported record type")

	h2 := newHarness(t, nil)
	out, err = h2.prov.AdjustEndpoints([]*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "10.0.0.1")})
	require.NoError(t, err)
	assert.Equal(t, endpoint.TTL(0), out[0].RecordTTL, "no default TTL configured")
}

func TestApplyChangesCreate(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	err := h.prov.ApplyChanges(ctx, &plan.Changes{Create: []*endpoint.Endpoint{
		endpoint.NewEndpointWithTTL("www.example.com", "A", 300, "10.0.0.1", "10.0.0.2"),
		endpoint.NewEndpoint("alias.sub.example.com", "CNAME", "www.example.com"),
		endpoint.NewEndpoint("txt.example.com", "TXT", `"v=spf1 -all"`),
		endpoint.NewEndpoint("_sip._tcp.example.com", "SRV", "10 20 5060 sip.example.com"),
		endpoint.NewEndpoint("example.com", "MX", "10 mail.example.com"),
		endpoint.NewEndpoint("deleg.example.com", "NS", "ns1.example.net"),
		endpoint.NewEndpoint("v6.example.com", "AAAA", "2001:db8::1"),
	}})
	require.NoError(t, err)

	creates := h.fake.CallsTo("CreateRecord")
	require.Len(t, creates, 8)
	byKey := map[string]uddi.Record{}
	for _, c := range creates {
		byKey[c.Record.Type+" "+c.Record.Name+" "+fmtRdata(c.Record.Rdata)] = c.Record
	}
	a1 := byKey[`A www.example.com {"address":"10.0.0.1"}`]
	assert.Equal(t, zone1, a1.ZoneID)
	assert.Equal(t, viewID, a1.ViewID)
	assert.Equal(t, int64(300), *a1.TTL)
	assert.Equal(t, "managed by external-dns", a1.Comment)
	assert.Equal(t, map[string]string{"external-dns": "true"}, a1.Tags)
	_, ok := byKey[`A www.example.com {"address":"10.0.0.2"}`]
	assert.True(t, ok)

	cname := byKey[`CNAME alias.sub.example.com {"cname":"www.example.com."}`]
	assert.Equal(t, zone2, cname.ZoneID, "longest suffix zone")
	assert.Nil(t, cname.TTL, "unset TTL is sent as nil so the zone default applies")
	_, ok = byKey[`TXT txt.example.com {"text":"\"v=spf1 -all\""}`]
	assert.True(t, ok, "TXT text carries the quotes")
	_, ok = byKey[`SRV _sip._tcp.example.com {"port":5060,"priority":10,"target":"sip.example.com.","weight":20}`]
	assert.True(t, ok)
	_, ok = byKey[`MX example.com {"exchange":"mail.example.com.","preference":10}`]
	assert.True(t, ok)
	_, ok = byKey[`NS deleg.example.com {"dname":"ns1.example.net."}`]
	assert.True(t, ok)
	_, ok = byKey[`AAAA v6.example.com {"address":"2001:db8::1"}`]
	assert.True(t, ok)

	// Records now reflect the writes and the plan is stable.
	eps, err := h.prov.Records(ctx)
	require.NoError(t, err)
	assert.Len(t, eps, 7)
	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2"}, findEndpoint(eps, "www.example.com", "A").Targets)
}

func TestApplyChangesDelete(t *testing.T) {
	h := newHarness(t, nil)
	seedRecords(h.fake)
	ctx := context.Background()

	t.Run("uses index from Records", func(t *testing.T) {
		_, err := h.prov.Records(ctx)
		require.NoError(t, err)
		h.fake.ResetCalls()
		err = h.prov.ApplyChanges(ctx, &plan.Changes{Delete: []*endpoint.Endpoint{
			endpoint.NewEndpoint("www.example.com", "A", "10.0.0.1"),
			endpoint.NewEndpoint("alias.example.com", "CNAME", "www.example.com"),
		}})
		require.NoError(t, err)
		assert.Len(t, h.fake.CallsTo("DeleteRecord"), 2)
		assert.Empty(t, h.fake.CallsTo("ListRecords"), "no re-list needed")
		eps, _ := h.prov.Records(ctx)
		assert.Equal(t, endpoint.Targets{"10.0.0.2"}, findEndpoint(eps, "www.example.com", "A").Targets)
		assert.Nil(t, findEndpoint(eps, "alias.example.com", "CNAME"))
	})

	t.Run("refreshes index on miss", func(t *testing.T) {
		h := newHarness(t, nil)
		seedRecords(h.fake)
		err := h.prov.ApplyChanges(ctx, &plan.Changes{Delete: []*endpoint.Endpoint{
			endpoint.NewEndpoint("_sip._tcp.example.com", "SRV", "10 5 5060 sip.example.com"),
			endpoint.NewEndpoint("example.com", "MX", "10 mail.example.com"),
		}})
		require.NoError(t, err)
		assert.Len(t, h.fake.CallsTo("DeleteRecord"), 2)
		assert.Len(t, h.fake.CallsTo("ListRecords"), 1, "one refresh serves both deletes in the zone")
	})

	t.Run("missing record is not an error", func(t *testing.T) {
		h := newHarness(t, nil)
		err := h.prov.ApplyChanges(ctx, &plan.Changes{Delete: []*endpoint.Endpoint{
			endpoint.NewEndpoint("ghost.example.com", "A", "10.9.9.9"),
		}})
		require.NoError(t, err)
		assert.Empty(t, h.fake.CallsTo("DeleteRecord"))
		assert.Contains(t, h.logs.String(), "treating as already deleted")
	})

	t.Run("404 on delete is tolerated", func(t *testing.T) {
		h := newHarness(t, nil)
		seedRecords(h.fake)
		_, err := h.prov.Records(ctx)
		require.NoError(t, err)
		h.fake.FailWith("DeleteRecord", &uddi.Error{Op: "DeleteRecord", StatusCode: 404, Err: uddi.ErrNotFound})
		err = h.prov.ApplyChanges(ctx, &plan.Changes{Delete: []*endpoint.Endpoint{
			endpoint.NewEndpoint("deleg.example.com", "NS", "ns1.example.net"),
		}})
		require.NoError(t, err)
		assert.Contains(t, h.logs.String(), "already gone")
	})

	t.Run("index refresh failure propagates", func(t *testing.T) {
		h := newHarness(t, nil)
		h.fake.FailWith("ListRecords", &uddi.Error{Op: "ListRecords", StatusCode: 500, Err: errors.New("boom")})
		err := h.prov.ApplyChanges(ctx, &plan.Changes{Delete: []*endpoint.Endpoint{
			endpoint.NewEndpoint("x.example.com", "A", "10.0.0.1"),
		}})
		require.Error(t, err)
		assert.True(t, uddi.IsRetryable(err))
	})
}

func TestApplyChangesUpdate(t *testing.T) {
	ctx := context.Background()

	t.Run("target diff becomes create and delete", func(t *testing.T) {
		h := newHarness(t, nil)
		seedRecords(h.fake)
		_, err := h.prov.Records(ctx)
		require.NoError(t, err)
		h.fake.ResetCalls()

		err = h.prov.ApplyChanges(ctx, &plan.Changes{
			UpdateOld: []*endpoint.Endpoint{endpoint.NewEndpointWithTTL("www.example.com", "A", 300, "10.0.0.1", "10.0.0.2")},
			UpdateNew: []*endpoint.Endpoint{endpoint.NewEndpointWithTTL("www.example.com", "A", 300, "10.0.0.2", "10.0.0.3")},
		})
		require.NoError(t, err)
		dels := h.fake.CallsTo("DeleteRecord")
		creates := h.fake.CallsTo("CreateRecord")
		require.Len(t, dels, 1)
		require.Len(t, creates, 1)
		assert.Equal(t, "10.0.0.3", creates[0].Record.Rdata["address"])
		assert.Empty(t, h.fake.CallsTo("UpdateRecord"))

		calls := h.fake.Calls()
		assert.Equal(t, "DeleteRecord", calls[0].Method, "deletes run before creates")

		eps, _ := h.prov.Records(ctx)
		assert.ElementsMatch(t, []string{"10.0.0.2", "10.0.0.3"}, findEndpoint(eps, "www.example.com", "A").Targets)
	})

	t.Run("ttl-only change patches in place", func(t *testing.T) {
		h := newHarness(t, nil)
		seedRecords(h.fake)
		_, err := h.prov.Records(ctx)
		require.NoError(t, err)
		h.fake.ResetCalls()

		err = h.prov.ApplyChanges(ctx, &plan.Changes{
			UpdateOld: []*endpoint.Endpoint{endpoint.NewEndpointWithTTL("www.example.com", "A", 300, "10.0.0.1", "10.0.0.2")},
			UpdateNew: []*endpoint.Endpoint{endpoint.NewEndpointWithTTL("www.example.com", "A", 60, "10.0.0.1", "10.0.0.2")},
		})
		require.NoError(t, err)
		updates := h.fake.CallsTo("UpdateRecord")
		require.Len(t, updates, 2)
		for _, u := range updates {
			assert.NotEmpty(t, u.Record.ID)
			assert.Equal(t, int64(60), *u.Record.TTL)
			assert.Equal(t, "managed by external-dns", u.Record.Comment)
		}
		assert.Empty(t, h.fake.CallsTo("CreateRecord"))
		assert.Empty(t, h.fake.CallsTo("DeleteRecord"))

		eps, _ := h.prov.Records(ctx)
		assert.Equal(t, endpoint.TTL(60), findEndpoint(eps, "www.example.com", "A").RecordTTL)
	})

	t.Run("ttl change for a missing record falls back to create", func(t *testing.T) {
		h := newHarness(t, nil)
		err := h.prov.ApplyChanges(ctx, &plan.Changes{
			UpdateOld: []*endpoint.Endpoint{endpoint.NewEndpointWithTTL("new.example.com", "A", 300, "10.0.0.1")},
			UpdateNew: []*endpoint.Endpoint{endpoint.NewEndpointWithTTL("new.example.com", "A", 60, "10.0.0.1")},
		})
		require.NoError(t, err)
		assert.Len(t, h.fake.CallsTo("CreateRecord"), 1)
		assert.Empty(t, h.fake.CallsTo("UpdateRecord"))
	})

	t.Run("unpaired old and new", func(t *testing.T) {
		h := newHarness(t, nil)
		seedRecords(h.fake)
		_, err := h.prov.Records(ctx)
		require.NoError(t, err)
		h.fake.ResetCalls()
		err = h.prov.ApplyChanges(ctx, &plan.Changes{
			UpdateOld: []*endpoint.Endpoint{endpoint.NewEndpoint("alias.example.com", "CNAME", "www.example.com")},
			UpdateNew: []*endpoint.Endpoint{endpoint.NewEndpoint("fresh.example.com", "A", "10.0.0.7")},
		})
		require.NoError(t, err)
		assert.Len(t, h.fake.CallsTo("DeleteRecord"), 1)
		assert.Len(t, h.fake.CallsTo("CreateRecord"), 1)
	})

	t.Run("update api failure propagates", func(t *testing.T) {
		h := newHarness(t, nil)
		seedRecords(h.fake)
		_, err := h.prov.Records(ctx)
		require.NoError(t, err)
		h.fake.FailWith("UpdateRecord", &uddi.Error{Op: "UpdateRecord", StatusCode: 500, Err: errors.New("boom")})
		err = h.prov.ApplyChanges(ctx, &plan.Changes{
			UpdateOld: []*endpoint.Endpoint{endpoint.NewEndpointWithTTL("www.example.com", "AAAA", 0, "2001:db8::1")},
			UpdateNew: []*endpoint.Endpoint{endpoint.NewEndpointWithTTL("www.example.com", "AAAA", 60, "2001:db8::1")},
		})
		require.Error(t, err)
		assert.True(t, uddi.IsRetryable(err))
	})
}

func TestApplyChangesSkips(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	err := h.prov.ApplyChanges(ctx, &plan.Changes{Create: []*endpoint.Endpoint{
		endpoint.NewEndpoint("www.nozone.org", "A", "10.0.0.1"),
		endpoint.NewEndpoint("x.other.net", "A", "10.0.0.1"),
		endpoint.NewEndpoint("ptr.example.com", "PTR", "x"),
		endpoint.NewEndpoint("ok.example.com", "A", "10.0.0.1"),
	}})
	require.NoError(t, err)
	creates := h.fake.CallsTo("CreateRecord")
	require.Len(t, creates, 1)
	assert.Equal(t, "ok.example.com", creates[0].Record.Name)
	assert.Contains(t, h.logs.String(), "no matching zone")
	assert.Contains(t, h.logs.String(), "unsupported record type")

	require.NoError(t, h.prov.ApplyChanges(ctx, nil))
	require.NoError(t, h.prov.ApplyChanges(ctx, &plan.Changes{}))
	require.NoError(t, h.prov.ApplyChanges(ctx, &plan.Changes{Create: []*endpoint.Endpoint{nil}, Delete: []*endpoint.Endpoint{nil}, UpdateOld: []*endpoint.Endpoint{nil}, UpdateNew: []*endpoint.Endpoint{nil}}))
	assert.Len(t, h.fake.CallsTo("ListZones"), 1, "empty change sets never touch the API")
}

func TestApplyChangesDryRun(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.DryRun = true })
	seedRecords(h.fake)
	ctx := context.Background()
	err := h.prov.ApplyChanges(ctx, &plan.Changes{
		Create:    []*endpoint.Endpoint{endpoint.NewEndpoint("new.example.com", "A", "10.0.0.9")},
		Delete:    []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "10.0.0.1")},
		UpdateOld: []*endpoint.Endpoint{endpoint.NewEndpointWithTTL("www.example.com", "AAAA", 0, "2001:db8::1")},
		UpdateNew: []*endpoint.Endpoint{endpoint.NewEndpointWithTTL("www.example.com", "AAAA", 60, "2001:db8::1")},
	})
	require.NoError(t, err)
	assert.Empty(t, h.fake.CallsTo("CreateRecord"))
	assert.Empty(t, h.fake.CallsTo("DeleteRecord"))
	assert.Empty(t, h.fake.CallsTo("UpdateRecord"))
	assert.Len(t, h.fake.CallsTo("ListZones"), 1)
	logs := h.logs.String()
	assert.Contains(t, logs, "dry-run")
	assert.Contains(t, logs, `"action":"create"`)
	assert.Contains(t, logs, `"action":"delete"`)
	assert.Contains(t, logs, `"action":"update"`)
}

func TestApplyChangesErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("zone listing failure", func(t *testing.T) {
		h := newHarness(t, nil)
		h.fake.FailWith("ListZones", &uddi.Error{Op: "ListZones", StatusCode: 502, Err: errors.New("bad gateway")})
		err := h.prov.ApplyChanges(ctx, &plan.Changes{Create: []*endpoint.Endpoint{endpoint.NewEndpoint("a.example.com", "A", "10.0.0.1")}})
		require.Error(t, err)
		assert.True(t, uddi.IsRetryable(err))
	})

	t.Run("retryable error aborts immediately", func(t *testing.T) {
		h := newHarness(t, nil)
		h.fake.FailWith("CreateRecord", &uddi.Error{Op: "CreateRecord", StatusCode: 503, Err: errors.New("unavailable")})
		err := h.prov.ApplyChanges(ctx, &plan.Changes{Create: []*endpoint.Endpoint{
			endpoint.NewEndpoint("a.example.com", "A", "10.0.0.1"),
			endpoint.NewEndpoint("b.example.com", "A", "10.0.0.2"),
		}})
		require.Error(t, err)
		assert.True(t, uddi.IsRetryable(err))
		var apiErr *uddi.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 503, apiErr.StatusCode)
		assert.Len(t, h.fake.CallsTo("CreateRecord"), 1, "stopped after first failure")
		assert.False(t, errors.Is(err, provider.SoftError))
	})

	t.Run("non-retryable errors continue and are reported as soft", func(t *testing.T) {
		h := newHarness(t, nil)
		h.fake.FailWith("CreateRecord", &uddi.Error{Op: "CreateRecord", StatusCode: 400, Err: errors.New("invalid")})
		err := h.prov.ApplyChanges(ctx, &plan.Changes{Create: []*endpoint.Endpoint{
			endpoint.NewEndpoint("a.example.com", "A", "10.0.0.1"),
			endpoint.NewEndpoint("b.example.com", "A", "10.0.0.2"),
		}})
		require.Error(t, err)
		assert.True(t, errors.Is(err, provider.SoftError), "got %v", err)
		assert.Len(t, h.fake.CallsTo("CreateRecord"), 2, "kept going")
		assert.Contains(t, err.Error(), "a.example.com")
		assert.Contains(t, err.Error(), "b.example.com")
	})

	t.Run("invalid target is a soft error", func(t *testing.T) {
		h := newHarness(t, nil)
		err := h.prov.ApplyChanges(ctx, &plan.Changes{Create: []*endpoint.Endpoint{
			endpoint.NewEndpoint("a.example.com", "A", "not-an-ip"),
			endpoint.NewEndpoint("b.example.com", "MX", "garbage"),
			endpoint.NewEndpoint("c.example.com", "A", "10.0.0.3"),
		}})
		require.Error(t, err)
		assert.True(t, errors.Is(err, provider.SoftError))
		assert.Len(t, h.fake.CallsTo("CreateRecord"), 1, "valid change still applied")
	})

	t.Run("cancelled context stops the loop", func(t *testing.T) {
		h := newHarness(t, nil)
		_, err := h.prov.Records(ctx)
		require.NoError(t, err)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		err = h.prov.ApplyChanges(cctx, &plan.Changes{Create: []*endpoint.Endpoint{endpoint.NewEndpoint("a.example.com", "A", "10.0.0.1")}})
		require.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, h.fake.CallsTo("CreateRecord"))
	})
}

func TestPlanChangesOrdering(t *testing.T) {
	ops := planChanges(&plan.Changes{
		Create:    []*endpoint.Endpoint{endpoint.NewEndpoint("c.example.com", "A", "10.0.0.3")},
		Delete:    []*endpoint.Endpoint{endpoint.NewEndpoint("d.example.com", "A", "10.0.0.4")},
		UpdateOld: []*endpoint.Endpoint{endpoint.NewEndpointWithTTL("u.example.com", "A", 10, "1.1.1.1", "2.2.2.2")},
		UpdateNew: []*endpoint.Endpoint{endpoint.NewEndpointWithTTL("U.example.com.", "A", 20, "2.2.2.2", "3.3.3.3")},
	})
	var got []string
	for _, op := range ops {
		got = append(got, string(op.action)+" "+op.ep.DNSName+" "+op.target)
	}
	assert.Equal(t, []string{
		"delete u.example.com 1.1.1.1",
		"delete d.example.com 10.0.0.4",
		"update U.example.com 2.2.2.2",
		"create U.example.com 3.3.3.3",
		"create c.example.com 10.0.0.3",
	}, got)
}

func TestInvalidateZones(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	_, err := h.prov.Records(ctx)
	require.NoError(t, err)
	h.prov.InvalidateZones()
	_, err = h.prov.Records(ctx)
	require.NoError(t, err)
	assert.Len(t, h.fake.CallsTo("ListZones"), 2)
}

func TestApplyUnknownAction(t *testing.T) {
	h := newHarness(t, nil)
	err := h.prov.apply(context.Background(), uddi.Zone{ID: zone1, FQDN: "example.com"}, change{action: "explode", ep: endpoint.NewEndpoint("a.example.com", "A", "10.0.0.1"), target: "10.0.0.1"})
	require.Error(t, err)
}

func fmtRdata(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

// The live Portal stores a single-string TXT without the quotes it was
// written with. Records must hand the quoted form back (so the plan is
// stable) and deletes/updates carrying the quoted registry form must still
// find the record.
func TestTXTQuotingRoundTrip(t *testing.T) {
	ctx := context.Background()
	const quoted = `"heritage=external-dns,external-dns/owner=k8s"`
	const stored = `heritage=external-dns,external-dns/owner=k8s`

	t.Run("Records re-quotes stored text", func(t *testing.T) {
		h := newHarness(t, nil)
		h.fake.AddRecord(uddi.Record{Name: "a-www.example.com", Type: "TXT", Rdata: map[string]any{"text": stored}, ZoneID: zone1})
		eps, err := h.prov.Records(ctx)
		require.NoError(t, err)
		assert.Equal(t, endpoint.Targets{quoted}, findEndpoint(eps, "a-www.example.com", "TXT").Targets)
	})

	t.Run("delete with quoted target finds unquoted record", func(t *testing.T) {
		h := newHarness(t, nil)
		h.fake.AddRecord(uddi.Record{Name: "a-www.example.com", Type: "TXT", Rdata: map[string]any{"text": stored}, ZoneID: zone1})
		_, err := h.prov.Records(ctx)
		require.NoError(t, err)
		err = h.prov.ApplyChanges(ctx, &plan.Changes{Delete: []*endpoint.Endpoint{
			endpoint.NewEndpoint("a-www.example.com", "TXT", quoted),
		}})
		require.NoError(t, err)
		assert.Len(t, h.fake.CallsTo("DeleteRecord"), 1)
		assert.NotContains(t, h.logs.String(), "treating as already deleted")
	})

	t.Run("delete with unquoted target also matches after a quoted create", func(t *testing.T) {
		h := newHarness(t, nil)
		err := h.prov.ApplyChanges(ctx, &plan.Changes{Create: []*endpoint.Endpoint{
			endpoint.NewEndpoint("a-www.example.com", "TXT", quoted),
		}})
		require.NoError(t, err)
		err = h.prov.ApplyChanges(ctx, &plan.Changes{Delete: []*endpoint.Endpoint{
			endpoint.NewEndpoint("a-www.example.com", "TXT", stored),
		}})
		require.NoError(t, err)
		assert.Len(t, h.fake.CallsTo("DeleteRecord"), 1)
		assert.Empty(t, h.fake.CallsTo("ListRecords"), "index hit, no refresh")
	})

	t.Run("update with quoted old target retunes in place", func(t *testing.T) {
		h := newHarness(t, nil)
		h.fake.AddRecord(uddi.Record{Name: "a-www.example.com", Type: "TXT", Rdata: map[string]any{"text": stored}, ZoneID: zone1})
		_, err := h.prov.Records(ctx)
		require.NoError(t, err)
		old := endpoint.NewEndpointWithTTL("a-www.example.com", "TXT", 0, quoted)
		upd := endpoint.NewEndpointWithTTL("a-www.example.com", "TXT", 120, quoted)
		err = h.prov.ApplyChanges(ctx, &plan.Changes{UpdateOld: []*endpoint.Endpoint{old}, UpdateNew: []*endpoint.Endpoint{upd}})
		require.NoError(t, err)
		assert.Len(t, h.fake.CallsTo("UpdateRecord"), 1)
		assert.Empty(t, h.fake.CallsTo("CreateRecord"), "no duplicate TXT created")
	})
}

func TestKeyOfTXTCanonical(t *testing.T) {
	assert.Equal(t, keyOf("x.example.com", "TXT", `"abc"`), keyOf("X.example.com.", "TXT", "abc"))
	assert.NotEqual(t, keyOf("x.example.com", "A", `"abc"`), keyOf("x.example.com", "A", "abc"), "only TXT is unquoted")
}
