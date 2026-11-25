package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProspectiveBootstrapPersistsProof(t *testing.T) {
	const newest = "deepseek/deepseek-v4.1-flash"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Messages []any  `json:"messages"`
			Tools    []any  `json:"tools"`
		}
		if r.Header.Get("Authorization") != "Bearer bootstrap-key" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model == "" || len(body.Messages) == 0 || len(body.Tools) != 0 {
			http.Error(w, "invalid inputs", 400)
			return
		}
		calls.Add(1)
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, body.Model)
	}))
	defer upstream.Close()
	ds, closeStack := bootSinkRootWithClose(t, "profiles: []\n", func(string) {})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "bootstrap", Provider: "openrouter", BaseURL: upstream.URL}, "bootstrap-key"))
	deps := ds.refreshDeps
	fetch := &fixtureFetcher{body: refreshCatalogFixture}
	deps.Fetcher = fetch
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()
	profile := provider.Profile{Alias: "cold", Kind: provider.ProfileKindChat, Backend: "claude-code", ConnectionRef: "bootstrap", SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "openrouter", Tier: "flash"}}
	cand, err := refresh.candidates.Newest(ctx, profile)
	require.NoError(t, err, "cold creation must collect before looking for a candidate")
	assert.Equal(t, newest, cand.Model)
	assert.NotEmpty(t, cand.CanonicalID)
	assert.NotEmpty(t, cand.PathRevision)
	view, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, view.Evidence)
	require.NotEmpty(t, view.ProbeCursor)
	require.Len(t, view.Reports, 1)
	assert.Empty(t, view.Reports[0].Changes, "bootstrap must not propose profile writes")
	var profiles, events int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&profiles))
	assert.Zero(t, profiles)
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&events))
	assert.Equal(t, 2, events)
	profile.Model = cand.Model
	require.NoError(t, refresh.candidates.RevalidateStart(ctx, profile, cand))
	bad := cand
	bad.CanonicalID = "wrong"
	assert.Error(t, refresh.candidates.RevalidateStart(ctx, profile, bad))
	bad = cand
	bad.ExecutionRevision.PolicyRevision++
	assert.Error(t, refresh.candidates.RevalidateStart(ctx, profile, bad))
	other := profile
	other.FollowTarget = &provider.FollowTarget{Provider: "openrouter", Tier: "pro"}
	assert.Error(t, refresh.candidates.RevalidateStart(ctx, other, cand))
	before := calls.Load()
	configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
	refresh.stop()
	closeStack()
	reopened, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	defer reopened.Close(context.Background())
	again, err := reopened.modelRefresh.candidates.ValidateStart(ctx, profile)
	require.NoError(t, err)
	assert.Equal(t, cand, again)
	assert.Equal(t, before, calls.Load(), "restart must reuse SQLite proof, not probe again")
	public := modelcatalog.ProjectCatalog(view)
	for _, e := range public.Entries {
		assert.Equal(t, modelcatalog.UsabilityConfirmed, e.Usability)
	}
}

func TestProspectiveBootstrapFaultsNeverWriteProfiles(t *testing.T) {
	for _, fault := range []string{"unknown exact evidence", "commit fault", "policy fault", "history fault"} {
		t.Run(fault, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string `json:"model"`
				}
				if r.Header.Get("Authorization") != "Bearer bootstrap-key" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model == "" {
					http.Error(w, "bad input", 400)
					return
				}
				if fault == "unknown exact evidence" {
					body.Model = "fallback"
				}
				fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, body.Model)
			}))
			defer upstream.Close()
			ds := bootSinkRoot(t, "profiles: []\n", func(string) {})
			ctx := adminCtxForLocalUser(t, ds)
			crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
			require.NoError(t, err)
			require.NoError(t, newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory).Create(ctx, provider.Connection{Name: "bootstrap", Provider: "openrouter", BaseURL: upstream.URL}, "bootstrap-key"))
			keep, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "keep", Kind: provider.ProfileKindChat, ConnectionRef: "bootstrap", Model: "deepseek/deepseek-v4-flash"})
			require.NoError(t, err)
			deps := ds.refreshDeps
			deps.Fetcher = &fixtureFetcher{body: refreshCatalogFixture}
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			defer refresh.stop()
			ds.profileSvc.SetModelSelection(modelidentity.NewVersionResolver(), refresh.candidates)
			switch fault {
			case "commit fault":
				_, err = ds.db.Exec(`CREATE TRIGGER bootstrap_write_fault BEFORE INSERT ON model_usability_evidence BEGIN SELECT RAISE(ABORT,'disk fault'); END`)
			case "policy fault":
				_, err = ds.db.Exec(`UPDATE model_identity_policy SET rules='invalid' WHERE id=1`)
			case "history fault":
				_, err = ds.db.Exec(`INSERT INTO model_catalog_slices(provider,slice_json) VALUES('openrouter','invalid')`)
			}
			require.NoError(t, err)
			_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "refused", Kind: provider.ProfileKindChat, ConnectionRef: "bootstrap", SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "openrouter", Tier: "flash"}})
			require.Error(t, err)
			current, err := adaptsqlite.NewProfileRepo(ds.db).Get(ctx, "keep")
			require.NoError(t, err)
			assert.Equal(t, keep, current)
			var rows, events int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&rows))
			assert.Equal(t, 1, rows)
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&events))
			assert.Zero(t, events)
		})
	}
}

func TestProspectiveBootstrapSharesManualRefreshAdmission(t *testing.T) {
	ds := bootSinkRoot(t, "profiles: []\n", func(string) {})
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "bootstrap", Provider: "openrouter", BaseURL: "https://api.example.test"}, "bootstrap-key"))
	started, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	deps := ds.refreshDeps
	deps.FetchTransport = catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://models.dev/api.json" {
			return nil, fmt.Errorf("wrong URL")
		}
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()
	done := make(chan error, 1)
	go func() { _, err := refresh.coordinator.Refresh(ctx); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("manual refresh did not start")
	}
	_, err = refresh.candidates.Newest(ctx, provider.Profile{Alias: "cold", Kind: provider.ProfileKindChat, ConnectionRef: "bootstrap", SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "openrouter", Tier: "flash"}})
	assert.ErrorIs(t, err, modelcatalog.ErrRefreshBusy)
	unblock.Do(func() { close(release) })
	require.NoError(t, <-done)
	var rows int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&rows))
	assert.Zero(t, rows)
}
