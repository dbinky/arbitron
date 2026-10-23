package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAutomaticAdvanceUsesConfiguredCapabilities(t *testing.T) {
	const initial, bad, good = "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash", "deepseek/deepseek-v4.2-flash"
	capabilities := filepath.Join(t.TempDir(), "capabilities.yaml")
	require.NoError(t, os.WriteFile(capabilities, []byte(`backends:
  claude-code:
    context_tiers: [default, 1m]
    effort_levels: [default, high]
    model_overrides:
      deepseek/deepseek-v4.1-flash:
        context_tiers: [default]
`), 0600))
	requests := make(chan string, 32)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if r.Header.Get("Authorization") != "Bearer axes-key" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model == "" {
			http.Error(w, "bad input", 400)
			return
		}
		requests <- body.Model
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, body.Model)
	}))
	defer upstream.Close()
	ds := bootSinkRoot(t, "capabilitiespath: "+capabilities+"\nprofiles: []\n", func(workspace string) {
		writeWorkflow(t, workspace, "auto-axes", `{"name":"auto-axes","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[]}`)
		writePinTemplate(t, workspace, "auto-axes")
	})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	require.NoError(t, adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy).Create(ctx, provider.Connection{Name: "axes", Provider: "openrouter", BaseURL: upstream.URL}, "axes-key"))
	deps := ds.refreshDeps
	fetch := &fixtureFetcher{body: refreshCatalogFixture}
	deps.Fetcher = fetch
	missingValidator := deps
	missingValidator.Profiles = nil
	_, err = newModelRefresh(missingValidator)
	require.Error(t, err, "a typed-nil capability validator must fail root construction")
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()
	ds.profileSvc.SetModelSelection(modelidentity.NewVersionResolver(), refresh.candidates)
	before, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "worker", Kind: provider.ProfileKindChat, Backend: "claude-code", ConnectionRef: "axes", Model: initial, ContextWindow: provider.Context1M, Effort: provider.EffortHigh, AllowedTools: []string{"Read"}, SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "openrouter", Tier: "flash"}})
	require.NoError(t, err)
	for len(requests) > 0 {
		<-requests
	}
	old, _, err := ds.eng.Trigger(ctx, "auto-axes", nil, "normal")
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, old).State)
	require.Equal(t, initial, <-requests)
	report, err := refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	assert.Empty(t, report.Changes, "incompatible Y must never reach the CAS")
	current, err := ds.profileSvc.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, before, current)
	fetch.body = fmt.Sprintf(`{"openrouter":{"id":"openrouter","models":{%q:{"id":%q},%q:{"id":%q},%q:{"id":%q}}}}`, initial, initial, bad, bad, good, good)
	report, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.Len(t, report.Changes, 1)
	assert.Equal(t, good, report.Changes[0].ToModel)
	current, err = ds.profileSvc.Get(ctx, "worker")
	require.NoError(t, err)
	expected := before.Clone()
	expected.Model = good
	expected.Revision++
	assert.Equal(t, expected, current)
	binding, err := ds.profileResolver.Resolve(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, good, binding.Model)
	var journal int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_advancement_journal`).Scan(&journal))
	assert.Equal(t, 1, journal)
	for len(requests) > 0 {
		<-requests
	}
	fresh, _, err := ds.eng.Trigger(ctx, "auto-axes", nil, "normal")
	require.NoError(t, err)
	run := waitTerminal(t, ds, fresh)
	require.Equal(t, instance.StateCompleted, run.State)
	require.Len(t, requests, 1)
	assert.Equal(t, good, <-requests)
	prior, err := ds.instanceRepo.Get(ctx, old)
	require.NoError(t, err)
	assert.Equal(t, initial, prior.ModelPins["worker"])
}
