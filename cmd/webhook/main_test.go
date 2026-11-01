package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, port, _ := net.SplitHostPort(l.Addr().String())
	require.NoError(t, l.Close())
	return port
}

// portalViewID is a view that only the zone list knows, like a view behind a
// key restricted to an access view.
const portalViewID = "dns/view/f0e0aca3-693f-4a90-80ef-ea3d09ed8fb5"

func fakePortal(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/ddi/v1/dns/view":
			if strings.Contains(r.URL.Query().Get("_filter"), `"default"`) {
				_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"id": "dns/view/1", "name": "default"}}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{}})
		case "/api/ddi/v1/dns/auth_zone":
			if strings.Contains(r.URL.Query().Get("_filter"), strconv.Quote(portalViewID)) {
				_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"id": "dns/auth_zone/1", "fqdn": "example.com.", "view": portalViewID, "primary_type": "cloud"}}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{}})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunConfigError(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), map[string]string{}, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INFOBLOX_PORTAL_KEY")
}

func TestRunProviderInitError(t *testing.T) {
	for _, tc := range []struct{ view, cause string }{
		{"missing", "likely restricted to an access view"},
		{"dns/view/00000000-0000-4000-8000-000000000000", "no cloud primary zones"},
	} {
		t.Run(tc.view, func(t *testing.T) {
			portal := fakePortal(t)
			var out bytes.Buffer
			err := run(context.Background(), map[string]string{
				"INFOBLOX_PORTAL_KEY": "k",
				"INFOBLOX_PORTAL_URL": portal.URL,
				"UDDI_VIEW":           tc.view,
			}, &out)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "initialise provider")
			assert.Contains(t, err.Error(), tc.cause)
			assert.NotContains(t, out.String(), `"k"`, "api key must not be logged")
		})
	}
}

func TestRunServesUntilCancelled(t *testing.T) {
	for _, tc := range []struct{ view, viewID string }{
		{"default", "dns/view/1"},
		{portalViewID, portalViewID},
	} {
		t.Run(tc.view, func(t *testing.T) { testRunServesUntilCancelled(t, tc.view, tc.viewID) })
	}
}

func testRunServesUntilCancelled(t *testing.T, view, viewID string) {
	portal := fakePortal(t)
	healthzPort := freePort(t)
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, map[string]string{
			"INFOBLOX_PORTAL_KEY": "k",
			"INFOBLOX_PORTAL_URL": portal.URL,
			"UDDI_VIEW":           view,
			"SERVER_HOST":         "127.0.0.1",
			"SERVER_PORT":         freePort(t),
			"HEALTHZ_HOST":        "127.0.0.1",
			"HEALTHZ_PORT":        healthzPort,
			"LOG_LEVEL":           "debug",
		}, &out)
	}()

	// Disable keep-alives so each health check closes its connection. A new
	// connection can otherwise lose a race with a reused idle connection and
	// remain on the listener without a request. Shutdown waits five seconds for
	// that connection, which can exceed this test's deadline. See go.dev/issue/22682.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	url := fmt.Sprintf("http://127.0.0.1:%s/healthz", healthzPort)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	resp, err := client.Get(url)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop")
	}
	assert.Contains(t, out.String(), "starting external-dns-uddi-webhook")
	assert.Contains(t, out.String(), `"portal_key":"[REDACTED]"`)
	assert.Contains(t, out.String(), `"view_id":"`+viewID+`"`)
	_, err = strconv.Atoi(healthzPort)
	require.NoError(t, err)
}
