package provider

import (
	"context"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/external-dns/endpoint"

	"github.com/wayvz-io/external-dns-uddi-webhook/internal/uddi"
)

// zoneCache memoises the manageable zones of a view for a TTL.
type zoneCache struct {
	client uddi.Client
	viewID string
	filter *endpoint.DomainFilter
	ttl    time.Duration
	now    func() time.Time

	mu     sync.Mutex
	zones  []uddi.Zone
	expiry time.Time
}

func (c *zoneCache) get(ctx context.Context) ([]uddi.Zone, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.zones != nil && c.now().Before(c.expiry) {
		return c.zones, nil
	}
	all, err := c.client.ListZones(ctx, c.viewID)
	if err != nil {
		return nil, err
	}
	zones := make([]uddi.Zone, 0, len(all))
	for _, z := range all {
		if z.FQDN == "" {
			continue
		}
		// Keep zones that are themselves in scope or are parents of a
		// configured filter (e.g. filter "sub.example.com", zone "example.com").
		if c.filter.Match(z.FQDN) || c.filter.MatchParent(z.FQDN) {
			zones = append(zones, z)
		}
	}
	c.zones = zones
	c.expiry = c.now().Add(c.ttl)
	return zones, nil
}

func (c *zoneCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.zones = nil
}

// findZone returns the zone whose FQDN is the longest suffix of name.
func findZone(zones []uddi.Zone, name string) (uddi.Zone, bool) {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	var best uddi.Zone
	found := false
	for _, z := range zones {
		fqdn := strings.ToLower(z.FQDN)
		if name != fqdn && !strings.HasSuffix(name, "."+fqdn) {
			continue
		}
		if !found || len(fqdn) > len(best.FQDN) {
			best, found = z, true
		}
	}
	return best, found
}
