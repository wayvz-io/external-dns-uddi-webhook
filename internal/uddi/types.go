// Package uddi wraps the Infoblox Universal DDI API behind a small interface
// that the provider (and tests) can depend on.
package uddi

import "context"

// Zone is an authoritative DNS zone in a view.
type Zone struct {
	// ID is the resource id, e.g. "dns/auth_zone/<uuid>".
	ID string
	// FQDN is the zone name without trailing dot.
	FQDN string
	// ViewID is the owning view's resource id, e.g. "dns/view/<uuid>".
	ViewID string
	// PrimaryType is "cloud" or "external".
	PrimaryType string
}

// Record is a single DNS resource record.
type Record struct {
	// ID is the resource id, e.g. "dns/record/<uuid>". Empty before creation.
	ID string
	// Name is the absolute owner name without trailing dot.
	Name string
	// Type is the record type, e.g. "A".
	Type string
	// Rdata is the type-specific record data as accepted by the API.
	Rdata map[string]any
	// TTL is the record TTL; nil means "inherit the zone default".
	TTL *int64
	// ZoneID is the owning zone's resource id.
	ZoneID string
	// ViewID is the owning view's resource id.
	ViewID string
	// Comment is a free-form description.
	Comment string
	// Tags are key/value tags attached to the record.
	Tags map[string]string
}

// Client is the subset of the Universal DDI API the provider needs.
type Client interface {
	// ResolveView returns the resource id of the view with the given name.
	ResolveView(ctx context.Context, name string) (string, error)
	// ListZones returns all authoritative zones in the view that this
	// installation may manage (primary_type == "cloud").
	ListZones(ctx context.Context, viewID string) ([]Zone, error)
	// ListRecords returns all records of the zone.
	ListRecords(ctx context.Context, zoneID string) ([]Record, error)
	// CreateRecord creates the record and returns it with its ID populated.
	CreateRecord(ctx context.Context, rec Record) (Record, error)
	// UpdateRecord patches rdata, TTL and comment of an existing record.
	UpdateRecord(ctx context.Context, rec Record) (Record, error)
	// DeleteRecord deletes the record with the given id.
	DeleteRecord(ctx context.Context, id string) error
}
