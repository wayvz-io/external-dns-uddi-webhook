package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/external-dns/endpoint"

	"github.com/wayvz-io/external-dns-uddi-webhook/internal/uddi"
)

func TestFindZone(t *testing.T) {
	zones := []uddi.Zone{
		{ID: "z1", FQDN: "example.com"},
		{ID: "z2", FQDN: "sub.example.com"},
		{ID: "z3", FQDN: "Other.ORG"},
	}
	tests := []struct {
		name   string
		wantID string
		found  bool
	}{
		{"www.example.com", "z1", true},
		{"example.com", "z1", true},
		{"example.com.", "z1", true},
		{"www.sub.example.com", "z2", true},
		{"sub.example.com", "z2", true},
		{"deep.a.b.sub.example.com", "z2", true},
		{"WWW.Other.org", "z3", true},
		{"notexample.com", "", false},
		{"example.org", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		z, ok := findZone(zones, tc.name)
		assert.Equal(t, tc.found, ok, tc.name)
		assert.Equal(t, tc.wantID, z.ID, tc.name)
	}
}

func TestZoneCache(t *testing.T) {
	fake := uddi.NewFake()
	fake.AddZone(uddi.Zone{ID: "z1", FQDN: "example.com", ViewID: "v"})
	fake.AddZone(uddi.Zone{ID: "z2", FQDN: "example.org", ViewID: "v"})
	fake.AddZone(uddi.Zone{ID: "z3", FQDN: "", ViewID: "v"})

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := &zoneCache{
		client: fake,
		viewID: "v",
		filter: endpoint.NewDomainFilter([]string{"sub.example.com"}),
		ttl:    time.Minute,
		now:    func() time.Time { return now },
	}
	ctx := context.Background()

	zones, err := c.get(ctx)
	require.NoError(t, err)
	require.Len(t, zones, 1, "parent of the filter is kept, unrelated zone dropped, empty fqdn dropped")
	assert.Equal(t, "z1", zones[0].ID)
	assert.Len(t, fake.CallsTo("ListZones"), 1)

	_, err = c.get(ctx)
	require.NoError(t, err)
	assert.Len(t, fake.CallsTo("ListZones"), 1, "served from cache")

	now = now.Add(2 * time.Minute)
	_, err = c.get(ctx)
	require.NoError(t, err)
	assert.Len(t, fake.CallsTo("ListZones"), 2, "expired")

	c.invalidate()
	_, err = c.get(ctx)
	require.NoError(t, err)
	assert.Len(t, fake.CallsTo("ListZones"), 3, "invalidated")

	c.invalidate()
	fake.FailWith("ListZones", errors.New("boom"))
	_, err = c.get(ctx)
	require.Error(t, err)
}

func TestZoneCacheZeroTTLAlwaysRefreshes(t *testing.T) {
	fake := uddi.NewFake()
	fake.AddZone(uddi.Zone{ID: "z1", FQDN: "example.com", ViewID: "v"})
	c := &zoneCache{client: fake, viewID: "v", filter: endpoint.NewDomainFilter(nil), now: time.Now}
	_, _ = c.get(context.Background())
	_, _ = c.get(context.Background())
	assert.Len(t, fake.CallsTo("ListZones"), 2)
}
