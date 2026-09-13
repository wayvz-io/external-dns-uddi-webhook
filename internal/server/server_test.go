package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/pkg/apis/externaldns"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider/webhook"

	"github.com/wayvz-io/external-dns-uddi-webhook/internal/provider"
	"github.com/wayvz-io/external-dns-uddi-webhook/internal/uddi"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

func newProvider(t *testing.T, df *endpoint.DomainFilter) (*provider.Provider, *uddi.Fake) {
	t.Helper()
	fake := uddi.NewFake()
	fake.AddZone(uddi.Zone{ID: "dns/auth_zone/z1", FQDN: "example.com", ViewID: "dns/view/default", PrimaryType: "cloud"})
	fake.AddRecord(uddi.Record{Name: "www.example.com", Type: "A", Rdata: map[string]any{"address": "10.0.0.1"}, ZoneID: "dns/auth_zone/z1"})
	p, err := provider.New(context.Background(), provider.Config{
		Client:       fake,
		DomainFilter: df,
		DefaultTTL:   120,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	return p, fake
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func httpStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestHealthzBeforeStart(t *testing.T) {
	p, _ := newProvider(t, nil)
	s := New(Config{}, p)
	assert.Nil(t, s.HealthzAddr())
	assert.False(t, s.Ready())

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "external_dns_uddi_webhook")
}

func TestContractWithUpstreamWebhookClient(t *testing.T) {
	df := endpoint.NewDomainFilterWithExclusions([]string{"example.com"}, []string{"private.example.com"})
	p, fake := newProvider(t, df)
	apiAddr := freeAddr(t)

	s := New(Config{
		ServerAddr:   apiAddr,
		HealthzAddr:  "127.0.0.1:0",
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, s.Start(ctx))
	healthURL := fmt.Sprintf("http://%s/healthz", s.HealthzAddr())

	// Readiness flips once StartHTTPApi has bound its listener.
	waitFor(t, func() bool { return httpStatus(t, healthURL) == http.StatusOK })
	assert.True(t, s.Ready())
	assert.Equal(t, http.StatusOK, httpStatus(t, fmt.Sprintf("http://%s/readyz", s.HealthzAddr())))
	assert.Equal(t, http.StatusOK, httpStatus(t, fmt.Sprintf("http://%s/metrics", s.HealthzAddr())))

	// Drive the API exactly as external-dns does.
	cfg := &externaldns.Config{
		WebhookProviderURL:          "http://" + apiAddr,
		WebhookProviderReadTimeout:  5 * time.Second,
		WebhookProviderWriteTimeout: 10 * time.Second,
	}
	client, err := webhook.New(ctx, cfg, nil)
	require.NoError(t, err, "negotiation must succeed with the upstream media type")

	// Negotiated domain filter round-trips.
	got := client.GetDomainFilter()
	assert.True(t, got.Match("www.example.com"))
	assert.False(t, got.Match("db.private.example.com"))
	assert.False(t, got.Match("example.org"))

	// Records.
	eps, err := client.Records(ctx)
	require.NoError(t, err)
	require.Len(t, eps, 1)
	assert.Equal(t, "www.example.com", eps[0].DNSName)
	assert.Equal(t, endpoint.Targets{"10.0.0.1"}, eps[0].Targets)

	// AdjustEndpoints: TXT quoting + default TTL + unsupported dropped.
	adjusted, err := client.AdjustEndpoints([]*endpoint.Endpoint{
		endpoint.NewEndpoint("txt.example.com", "TXT", "heritage=external-dns,external-dns/owner=k8s"),
		endpoint.NewEndpoint("ptr.example.com", "PTR", "x"),
	})
	require.NoError(t, err)
	require.Len(t, adjusted, 1)
	assert.Equal(t, endpoint.Targets{`"heritage=external-dns,external-dns/owner=k8s"`}, adjusted[0].Targets)
	assert.Equal(t, endpoint.TTL(120), adjusted[0].RecordTTL)

	// ApplyChanges: create + delete, then observe through Records.
	err = client.ApplyChanges(ctx, &plan.Changes{
		Create: []*endpoint.Endpoint{
			endpoint.NewEndpointWithTTL("new.example.com", "CNAME", 60, "www.example.com"),
			adjusted[0],
		},
		Delete: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "10.0.0.1")},
	})
	require.NoError(t, err)
	assert.Len(t, fake.CallsTo("CreateRecord"), 2)
	assert.Len(t, fake.CallsTo("DeleteRecord"), 1)

	eps, err = client.Records(ctx)
	require.NoError(t, err)
	names := map[string]string{}
	for _, ep := range eps {
		names[ep.RecordType+" "+ep.DNSName] = ep.Targets[0]
	}
	assert.Equal(t, map[string]string{
		"CNAME new.example.com": "www.example.com",
		"TXT txt.example.com":   `"heritage=external-dns,external-dns/owner=k8s"`,
	}, names)

	// Provider failures surface as errors to the upstream client.
	fake.FailWith("ListRecords", &uddi.Error{Op: "ListRecords", StatusCode: 500, Err: fmt.Errorf("boom")})
	_, err = client.Records(ctx)
	require.Error(t, err)
	fake.FailWith("ListRecords", nil)

	// Shutdown flips readiness and stops the health listener.
	require.NoError(t, s.Shutdown(context.Background()))
	assert.False(t, s.Ready())
	waitFor(t, func() bool { return httpStatus(t, healthURL) == 0 })
}

func TestRunStopsOnContextCancel(t *testing.T) {
	p, _ := newProvider(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{ServerAddr: freeAddr(t), HealthzAddr: "127.0.0.1:0", ShutdownTimeout: time.Second}, p)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRunHealthzBindFailure(t *testing.T) {
	p, _ := newProvider(t, nil)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer busy.Close()

	err = Run(context.Background(), Config{ServerAddr: freeAddr(t), HealthzAddr: busy.Addr().String()}, p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listen healthz")
}

func TestStartReadinessNotFlippedWhenContextCancelledFirst(t *testing.T) {
	p, _ := newProvider(t, nil)
	s := New(Config{ServerAddr: freeAddr(t), HealthzAddr: "127.0.0.1:0"}, p)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, s.Start(ctx))
	require.NoError(t, s.Shutdown(context.Background()))
}
