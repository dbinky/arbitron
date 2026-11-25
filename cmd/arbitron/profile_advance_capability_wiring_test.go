package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdvanceWiringRejectsIncompatibleCandidateCapabilities(t *testing.T) {
	const initial = "deepseek/deepseek-v4-flash"
	const incompatible = "deepseek/deepseek-v4.1-flash"
	const compatible = "deepseek/deepseek-v4.2-flash"
	capabilities := filepath.Join(t.TempDir(), "capabilities.yaml")
	require.NoError(t, os.WriteFile(capabilities, []byte(`backends:
  claude-code:
    context_tiers: [default, 1m]
    effort_levels: [default, high]
    model_overrides:
      deepseek/deepseek-v4.1-flash:
        context_tiers: [default]
`), 0600))
	var mu sync.Mutex
	expected := initial
	var models []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
			Stream bool `json:"stream"`
		}
		err := json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		want := expected
		models = append(models, body.Model)
		mu.Unlock()
		if err != nil || r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer capability-key" || body.Model != want || body.Stream || len(body.Messages) == 0 || body.Messages[len(body.Messages)-1].Content == "" {
			http.Error(w, "invalid model/auth/route/prompt", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"capability-ok"},"finish_reason":"stop"}]}`, want)
	}))
	defer upstream.Close()
	ds := bootSinkRoot(t, "capabilitiespath: "+capabilities+"\n"+`profiles:
  - alias: worker
    kind: chat
    backend: claude-code
    connectionref: capability-sink
    model: deepseek/deepseek-v4-flash
    allowedtools: [Read]
    selection_mode: following
    follow_target:
      provider: openrouter
      tier: flash
`, func(workspace string) {
		writeWorkflow(t, workspace, "capability-exec", `{"name":"capability-exec","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[]}`)
		writePinTemplate(t, workspace, "capability-exec")
	})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "capability-sink", Provider: "openrouter", BaseURL: upstream.URL}, "capability-key"))
	storeConfiguredFollowing(t, ctx, ds, "worker")
	prior, err := ds.profileSvc.Get(ctx, "worker")
	require.NoError(t, err)
	// ProfileConfig has no axes fields. Establish the persisted working
	// following row with the real repository, then publish through an unchanged
	// admin patch; candidate initialization is a separate runtime obligation.
	configured := prior.Clone()
	configured.ContextWindow, configured.Effort = provider.Context1M, provider.EffortHigh
	_, err = adaptsqlite.NewProfileRepo(ds.db).UpdateExpected(ctx, prior, configured)
	require.NoError(t, err)
	prior, _, err = ds.profileSvc.UpdatePatch(ctx, "worker", provider.ProfilePatch{})
	require.NoError(t, err)
	require.Equal(t, provider.Context1M, prior.ContextWindow)
	require.Equal(t, provider.EffortHigh, prior.Effort)
	execute := func(want string) *instance.Instance {
		t.Helper()
		id, _, err := ds.eng.Trigger(ctx, "capability-exec", nil, "normal")
		require.NoError(t, err)
		run := waitTerminal(t, ds, id)
		assert.Equal(t, instance.StateCompleted, run.State)
		assert.Equal(t, want, run.ModelPins["worker"])
		var output string
		require.NoError(t, ds.db.QueryRow(`SELECT output FROM node_executions WHERE instance_id=? AND node_id='ask'`, id.String()).Scan(&output))
		assert.Contains(t, output, "capability-ok")
		return run
	}
	old := execute(initial)
	snap, err := ds.pathInventory.Snapshot(ctx)
	require.NoError(t, err)
	req := provider.AdvanceRequest{ID: "capability-reject", Expected: prior, Model: incompatible, CanonicalID: incompatible, PathRevision: snap.Revision, ExecutionRevision: snap.ExecutionRevision}
	// A mutable replacement must not reach SQL merely because its capability
	// axes are supported. This uses initDomain's actual route/version sources.
	mutable := req
	mutable.ID, mutable.Model, mutable.CanonicalID = "mutable-reject", "deepseek/latest", "deepseek/latest"
	adv, err := ds.profileSvc.AdvanceModel(ctx, mutable)
	assert.ErrorIs(t, err, provider.ErrSpecificVersionUnavailable)
	assert.Empty(t, adv)
	adv, err = ds.profileSvc.AdvanceModel(ctx, req)
	assert.ErrorIs(t, err, provider.ErrAxisNotApplicable)
	assert.Empty(t, adv)
	stored, err := ds.profileSvc.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, prior, stored, "rejected model must leave the entire authoritative row/revision unchanged")
	binding, err := ds.profileResolver.Resolve(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, initial, binding.Model)
	var journal, acks int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_advancement_journal`).Scan(&journal))
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_publication_acks`).Scan(&acks))
	assert.Zero(t, journal)
	assert.Zero(t, acks)
	execute(initial)
	// The same profile configuration is valid on a compatible replacement.
	snap, err = ds.pathInventory.Snapshot(ctx)
	require.NoError(t, err)
	req.ID, req.Model, req.CanonicalID = "capability-accept", compatible, compatible
	req.PathRevision, req.ExecutionRevision = snap.Revision, snap.ExecutionRevision
	adv, err = ds.profileSvc.AdvanceModel(ctx, req)
	assert.NoError(t, err)
	assert.Equal(t, compatible, adv.ToModel)
	stored, err = ds.profileSvc.Get(ctx, "worker")
	require.NoError(t, err)
	want := prior.Clone()
	want.Model, want.Revision = compatible, prior.Revision+1
	assert.Equal(t, want, stored, "advancement changes only model and revision")
	mu.Lock()
	expected = compatible
	mu.Unlock()
	execute(compatible)
	oldStored, err := ds.instanceRepo.Get(context.Background(), old.ID)
	require.NoError(t, err)
	assert.Equal(t, initial, oldStored.ModelPins["worker"])
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_advancement_journal WHERE id='capability-accept' AND to_model=?`, compatible).Scan(&journal))
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_publication_acks WHERE advancement_id='capability-accept'`).Scan(&acks))
	assert.Equal(t, 1, journal)
	assert.Equal(t, 1, acks)
	mu.Lock()
	assert.Equal(t, []string{initial, initial, compatible}, models)
	mu.Unlock()
}
