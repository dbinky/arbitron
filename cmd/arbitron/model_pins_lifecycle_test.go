package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	lifecycleModelX = "deepseek/deepseek-v4-flash"
	lifecycleModelY = "deepseek/deepseek-v4.1-flash"
)

// Only the HTTP leaf is fake. It records the actual request body, refuses an
// empty model, and can fail the first call AFTER the live profile edit lands.
func lifecycleSink(t *testing.T, failFirst bool) (*modelSink, func()) {
	t.Helper()
	s := &modelSink{arrived: make(chan struct{}, 1)}
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model == "" {
			http.Error(w, "non-empty model required", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.models = append(s.models, body.Model)
		first := len(s.models) == 1
		s.mu.Unlock()
		if first {
			s.arrived <- struct{}{}
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
			if failFirst {
				http.Error(w, "injected first-attempt failure", http.StatusBadRequest)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"{\"topic\":\"x\"}"},"finish_reason":"stop"}]}`, body.Model)
	}))
	t.Cleanup(func() { release(); s.srv.Close() })
	return s, release
}

func lifecycleProfile(model string) provider.Profile {
	return provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, ConnectionRef: "sink", Model: model}
}

func installLifecycleProfile(t *testing.T, ds *domainStack, sink *modelSink) context.Context {
	t.Helper()
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "sink", Provider: "openrouter", BaseURL: sink.srv.URL}, "k"))
	_, _, err = ds.profileSvc.Create(ctx, lifecycleProfile(lifecycleModelX))
	require.NoError(t, err)
	return ctx
}

func awaitLifecycleRequest(t *testing.T, sink *modelSink) {
	t.Helper()
	select {
	case <-sink.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("model request never reached HTTP sink")
	}
}

func assertLifecyclePins(t *testing.T, ds *domainStack, id uuid.UUID, model string) {
	t.Helper()
	inst, err := ds.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.ModelPinsVersionSnapshot, inst.ModelPinsVersion)
	assert.Equal(t, map[string]string{"pin-alias": model}, inst.ModelPins)
}

// The named failure-lens fixture lives in the composition-root package so it
// can use real initDomain, migrated SQLite, queueing and HTTP adapters. Every
// continuation resolves after X -> Y; only newly created runs may send Y.
func TestRetryResumeRerunAndSpawnPins(t *testing.T) {
	for _, mode := range []string{"automatic retry", "explicit Retry and Rerun", "pause and Resume"} {
		t.Run(mode, func(t *testing.T) {
			ds := bootSinkRoot(t, "", func(dir string) {
				errorBehavior, extraNode, edges := "", "", "[]"
				if mode == "automatic retry" {
					errorBehavior = `,"errorBehavior":{"type":"retry","retries":1}`
				}
				if mode == "pause and Resume" {
					extraNode = `,{"id":"ask2","type":"llm_call","config":{"model_profile":"pin-alias","templateFile":"templates/p.md"}}`
					edges = `[{"source":"ask1","target":"ask2"}]`
				}
				writeWorkflow(t, dir, "pin-lifecycle", fmt.Sprintf(`{
  "name":"pin-lifecycle","inputs":[],
  "nodes":[{"id":"ask1","type":"llm_call","entrypoint":true,
    "config":{"model_profile":"pin-alias","templateFile":"templates/p.md"}%s}%s],
  "edges":%s}`, errorBehavior, extraNode, edges))
				writePinTemplate(t, dir, "pin-lifecycle")
			})
			sink, release := lifecycleSink(t, mode != "pause and Resume")
			ctx := installLifecycleProfile(t, ds, sink)
			id, _, err := ds.eng.Trigger(ctx, "pin-lifecycle", map[string]any{}, "normal")
			require.NoError(t, err)
			awaitLifecycleRequest(t, sink)
			if mode == "pause and Resume" {
				require.NoError(t, ds.eng.Pause(ctx, id))
			}
			_, _, err = ds.profileSvc.Update(ctx, lifecycleProfile(lifecycleModelY))
			require.NoError(t, err)
			release()

			switch mode {
			case "automatic retry":
				require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
				trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, trace, 2)
				assert.Equal(t, instance.NodeStatusFailed, trace[0].Status)
				assert.Equal(t, instance.NodeStatusSucceeded, trace[1].Status)
				assert.Equal(t, 2, trace[1].Attempt)
			case "explicit Retry and Rerun":
				failed := waitTerminal(t, ds, id)
				require.Equal(t, instance.StateFailed, failed.State)
				assert.NotContains(t, failed.Variables, "ask1", "failed output must not be accepted")
				ds.eng.Shutdown() // finish terminal cache release before explicit Retry
				require.NoError(t, ds.eng.Retry(ctx, id, map[string]any{"edited": true}))
				require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
			case "pause and Resume":
				ds.eng.Shutdown() // paused walker must exit before starting its successor
				paused, err := ds.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				require.Equal(t, instance.StatePaused, paused.State)
				require.Equal(t, []string{lifecycleModelX}, sink.seen())
				require.NoError(t, ds.eng.Resume(ctx, id))
				require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
			}
			// pause and Resume: ask1 finishes during the pause and is not
			// re-executed; Resume continues at ask2. Every mode makes two calls.
			expected := []string{lifecycleModelX, lifecycleModelX}
			assert.Equal(t, expected, sink.seen())
			assertLifecyclePins(t, ds, id, lifecycleModelX)
			if mode == "explicit Retry and Rerun" {
				ds.eng.Shutdown()
				fresh, err := ds.eng.Rerun(ctx, id)
				require.NoError(t, err)
				require.NotEqual(t, id, fresh)
				require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, fresh).State)
				assert.Equal(t, append(expected, lifecycleModelY), sink.seen())
				assertLifecyclePins(t, ds, fresh, lifecycleModelY)
			}
		})
	}

	t.Run("reopen and crash recovery", func(t *testing.T) {
		ds, closeStack := bootSinkRootWithClose(t, "", func(dir string) {
			writeWorkflow(t, dir, "pin-restart", `{"name":"pin-restart","inputs":[],"nodes":[
  {"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"pin-alias","templateFile":"templates/p.md"}}
],"edges":[]}`)
			writePinTemplate(t, dir, "pin-restart")
		})
		sink, release := lifecycleSink(t, false)
		release()
		ctx := installLifecycleProfile(t, ds, sink)
		// Prevent a walker from starting while we construct the on-disk crash
		// boundary: a triggered X run marked running at its first provider node.
		ds.queueMgr.UpdateConfig(1, 1)
		require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{
			ID: uuid.New(), OwnerID: localDefaultUser, WorkflowName: "capacity-holder",
			State: instance.StateRunning, Priority: instance.PriorityNormal,
			WorkflowSnapshot: map[string]any{}, Variables: map[string]any{},
			ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		}))
		id, started, err := ds.eng.Trigger(ctx, "pin-restart", map[string]any{}, "normal")
		require.NoError(t, err)
		require.False(t, started)
		require.NoError(t, ds.instanceRepo.UpdateCurrentNode(ctx, id, "ask"))
		require.NoError(t, ds.instanceRepo.UpdateState(ctx, id, instance.StateRunning))
		_, _, err = ds.profileSvc.Update(ctx, lifecycleProfile(lifecycleModelY))
		require.NoError(t, err)
		configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
		closeStack()

		// initDomain constructs a NEW PinLookup and recovers running rows.
		reopened, err := initRecoveryTestDomain(context.Background(), configPath)
		require.NoError(t, err)
		t.Cleanup(func() { reopened.Close(context.Background()) })
		recovered, err := reopened.instanceRepo.Get(ctx, id)
		require.NoError(t, err)
		require.Equal(t, instance.StateFailed, recovered.State)
		require.NotNil(t, recovered.FailedNodeID)
		assert.Equal(t, "ask", *recovered.FailedNodeID)
		assertLifecyclePins(t, reopened, id, lifecycleModelX)
		require.Empty(t, sink.seen())
		require.NoError(t, reopened.eng.Retry(ctx, id, nil))
		require.Equal(t, instance.StateCompleted, waitTerminal(t, reopened, id).State)
		assert.Equal(t, []string{lifecycleModelX}, sink.seen(), "restart must reload X from SQLite despite live Y")
	})

	t.Run("spawn pins fresh while parent prepares on X", func(t *testing.T) {
		ds := bootSinkRoot(t, "", func(dir string) {
			writeWorkflow(t, dir, "pin-child-lifecycle", `{"name":"pin-child-lifecycle",
  "inputs":[{"name":"topic","type":"string","required":true}],"nodes":[
  {"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"pin-alias","templateFile":"templates/p.md"}}
],"edges":[]}`)
			writePinTemplate(t, dir, "pin-child-lifecycle")
			writeWorkflow(t, dir, "pin-parent-lifecycle", `{"name":"pin-parent-lifecycle","inputs":[],"nodes":[
  {"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"pin-alias","templateFile":"templates/p.md"}},
  {"id":"spawn","type":"spawn_workflow","config":{"childWorkflow":"pin-child-lifecycle","spawnMode":"fire_and_forget","model_profile":"pin-alias"}}
],"edges":[{"source":"ask","target":"spawn"}]}`)
			writePinTemplate(t, dir, "pin-parent-lifecycle")
		})
		sink, release := lifecycleSink(t, false)
		ctx := installLifecycleProfile(t, ds, sink)
		parent, _, err := ds.eng.Trigger(ctx, "pin-parent-lifecycle", map[string]any{}, "normal")
		require.NoError(t, err)
		awaitLifecycleRequest(t, sink)
		_, _, err = ds.profileSvc.Update(ctx, lifecycleProfile(lifecycleModelY))
		require.NoError(t, err)
		release()
		require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, parent).State)
		children := childrenOf(t, ds, parent)
		require.Len(t, children, 1)
		child := waitTerminal(t, ds, children[0].ID)
		require.Equal(t, instance.StateCompleted, child.State)
		assert.Equal(t, "x", child.OriginalVariables["topic"])
		assert.Equal(t, []string{lifecycleModelX, lifecycleModelX, lifecycleModelY}, sink.seen())
		assertLifecyclePins(t, ds, parent, lifecycleModelX)
		assertLifecyclePins(t, ds, child.ID, lifecycleModelY)
	})
}
