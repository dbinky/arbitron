package modelcatalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

type snapshotStore struct {
	modelcatalog.CatalogStore
	view modelcatalog.CatalogView
	err  error
}

func (s snapshotStore) Snapshot(context.Context) (modelcatalog.CatalogView, error) {
	return s.view, s.err
}

func adminIdentity(role identity.Role) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{Role: role})
}

func TestAdminServiceListAuthorizesAndRedacts(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	store := snapshotStore{view: modelcatalog.CatalogView{
		Catalog:  modelcatalog.CatalogSnapshot{Slices: []modelcatalog.CatalogSlice{{Provider: "vendor", Raw: json.RawMessage(`{"url":"https://catalog.example/api.json?token=SECRET"}`), Entries: []modelidentity.Entry{{CanonicalID: "vendor/m", Provider: "vendor", NativeID: "m", SpecificModel: "m", Tier: "premium"}}}}},
		Evidence: []modelcatalog.UsabilityEvidence{{CanonicalID: "vendor/m", PathKey: "p", ConfigRevision: "r", NativeModel: "m", ProofRef: "/private/SECRET", CheckedAt: at, State: modelcatalog.UsabilityConfirmed}},
	}}
	svc := &modelcatalog.AdminService{Store: store}

	_, err := svc.List(context.Background())
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
	_, err = svc.List(adminIdentity(identity.RoleUser))
	assert.ErrorIs(t, err, identity.ErrForbidden)

	view, err := svc.List(adminIdentity(identity.RoleAdmin))
	require.NoError(t, err)
	require.Len(t, view.Entries, 1)
	assert.Equal(t, modelcatalog.UsabilityConfirmed, view.Entries[0].Usability)
	raw, err := json.Marshal(view)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "SECRET")
	assert.NotContains(t, string(raw), "catalog.example")
}

func TestAdminServiceListStoreFaultAndMissingStore(t *testing.T) {
	ctx := adminIdentity(identity.RoleAdmin)
	_, err := (&modelcatalog.AdminService{Store: snapshotStore{err: errors.New("disk")}}).List(ctx)
	assert.Error(t, err)
	_, err = (&modelcatalog.AdminService{}).List(ctx)
	assert.Error(t, err, "a missing store must fail, not return an empty catalog")
}

func TestAdminServiceRefreshNowAuthorizesBeforeRunning(t *testing.T) {
	calls := 0
	svc := &modelcatalog.AdminService{Refresh: func(context.Context) (modelcatalog.RefreshReport, error) {
		calls++
		return modelcatalog.RefreshReport{ID: "r1", AnnouncementError: "https://hook.example/SECRET"}, nil
	}}
	_, err := svc.RefreshNow(adminIdentity(identity.RoleUser))
	assert.ErrorIs(t, err, identity.ErrForbidden)
	_, err = svc.RefreshNow(context.Background())
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
	assert.Zero(t, calls, "an unauthorized caller must not start a cycle")

	report, err := svc.RefreshNow(adminIdentity(identity.RoleAdmin))
	require.NoError(t, err)
	assert.Equal(t, "r1", report.ID)
	assert.Equal(t, "announcement_error", report.AnnouncementError, "projection redacts the raw announcement error")
	assert.Equal(t, 1, calls)
}

func TestAdminServiceRefreshNowReportsPendingPublicationAndFailure(t *testing.T) {
	ctx := adminIdentity(identity.RoleAdmin)
	pending := &modelcatalog.AdminService{Refresh: func(context.Context) (modelcatalog.RefreshReport, error) {
		return modelcatalog.RefreshReport{ID: "r2"}, provider.ErrPublicationPending
	}}
	report, err := pending.RefreshNow(ctx)
	require.NoError(t, err)
	assert.True(t, report.PublicationPending)

	failing := &modelcatalog.AdminService{Refresh: func(context.Context) (modelcatalog.RefreshReport, error) {
		return modelcatalog.RefreshReport{}, errors.New("fetch failed")
	}}
	_, err = failing.RefreshNow(ctx)
	assert.Error(t, err)
	_, err = (&modelcatalog.AdminService{}).RefreshNow(ctx)
	assert.Error(t, err)
}
