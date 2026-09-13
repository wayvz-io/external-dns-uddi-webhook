package uddi

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFakeRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	f.AddZone(Zone{ID: "dns/auth_zone/1", FQDN: "example.com", ViewID: "dns/view/default", PrimaryType: "cloud"})
	f.AddZone(Zone{ID: "dns/auth_zone/2", FQDN: "sub.example.com", ViewID: "dns/view/default", PrimaryType: "cloud"})
	f.AddZone(Zone{ID: "dns/auth_zone/3", FQDN: "other.org", ViewID: "dns/view/other", PrimaryType: "cloud"})

	id, err := f.ResolveView(ctx, "default")
	require.NoError(t, err)
	assert.Equal(t, "dns/view/default", id)
	_, err = f.ResolveView(ctx, "nope")
	require.ErrorIs(t, err, ErrNotFound)
	assert.False(t, IsRetryable(err))

	zones, err := f.ListZones(ctx, "dns/view/default")
	require.NoError(t, err)
	assert.Len(t, zones, 2)

	ttl := int64(60)
	created, err := f.CreateRecord(ctx, Record{Name: "www.sub.example.com", Type: "A", Rdata: map[string]any{"address": "10.0.0.1"}, TTL: &ttl})
	require.NoError(t, err)
	assert.NotEmpty(t, created.ID)
	assert.Equal(t, "dns/auth_zone/2", created.ZoneID, "longest suffix zone wins")

	apex, err := f.CreateRecord(ctx, Record{Name: "example.com", Type: "TXT", Rdata: map[string]any{"text": `"v=spf1 -all"`}})
	require.NoError(t, err)
	assert.Equal(t, "dns/auth_zone/1", apex.ZoneID)

	orphan, err := f.CreateRecord(ctx, Record{Name: "x.unknown.net", Type: "A", Rdata: map[string]any{"address": "10.0.0.2"}})
	require.NoError(t, err)
	assert.Empty(t, orphan.ZoneID)

	recs, err := f.ListRecords(ctx, "dns/auth_zone/2")
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, created.ID, recs[0].ID)
	// Mutating the returned copy must not affect the store.
	recs[0].Rdata["address"] = "mutated"
	recs, _ = f.ListRecords(ctx, "dns/auth_zone/2")
	assert.Equal(t, "10.0.0.1", recs[0].Rdata["address"])

	ttl2 := int64(120)
	updated, err := f.UpdateRecord(ctx, Record{ID: created.ID, TTL: &ttl2, Comment: "c", Tags: map[string]string{"a": "b"}})
	require.NoError(t, err)
	assert.Equal(t, int64(120), *updated.TTL)
	assert.Equal(t, "c", updated.Comment)
	assert.Equal(t, "b", updated.Tags["a"])
	_, err = f.UpdateRecord(ctx, Record{ID: "dns/record/missing"})
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, f.DeleteRecord(ctx, created.ID))
	err = f.DeleteRecord(ctx, created.ID)
	require.ErrorIs(t, err, ErrNotFound)

	all := f.Records()
	assert.Len(t, all, 2)

	calls := f.Calls()
	assert.Equal(t, "ResolveView", calls[0].Method)
	assert.Len(t, f.CallsTo("CreateRecord"), 3)
	assert.Len(t, f.CallsTo("DeleteRecord"), 2)
	f.ResetCalls()
	assert.Empty(t, f.Calls())

	pre := f.AddRecord(Record{ID: "dns/record/fixed", Name: "fixed.example.com", Type: "A", ZoneID: "dns/auth_zone/1"})
	assert.Equal(t, "dns/record/fixed", pre.ID)
}

func TestFakeErrorInjectionAndContext(t *testing.T) {
	f := NewFake()
	boom := &Error{Op: "x", StatusCode: 500, Err: errors.New("boom")}
	for _, m := range []string{"ResolveView", "ListZones", "ListRecords", "CreateRecord", "UpdateRecord", "DeleteRecord"} {
		f.FailWith(m, boom)
	}
	ctx := context.Background()
	_, err := f.ResolveView(ctx, "default")
	assert.ErrorIs(t, err, boom)
	_, err = f.ListZones(ctx, "v")
	assert.ErrorIs(t, err, boom)
	_, err = f.ListRecords(ctx, "z")
	assert.ErrorIs(t, err, boom)
	_, err = f.CreateRecord(ctx, Record{})
	assert.ErrorIs(t, err, boom)
	_, err = f.UpdateRecord(ctx, Record{ID: "x"})
	assert.ErrorIs(t, err, boom)
	assert.ErrorIs(t, f.DeleteRecord(ctx, "x"), boom)

	for _, m := range []string{"ResolveView", "ListZones", "ListRecords", "CreateRecord", "UpdateRecord", "DeleteRecord"} {
		f.FailWith(m, nil)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = f.ResolveView(cancelled, "default")
	assert.ErrorIs(t, err, context.Canceled)
	_, err = f.ListZones(cancelled, "v")
	assert.ErrorIs(t, err, context.Canceled)
	_, err = f.ListRecords(cancelled, "z")
	assert.ErrorIs(t, err, context.Canceled)
	_, err = f.CreateRecord(cancelled, Record{})
	assert.ErrorIs(t, err, context.Canceled)
	_, err = f.UpdateRecord(cancelled, Record{ID: "x"})
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, f.DeleteRecord(cancelled, "x"), context.Canceled)
}
