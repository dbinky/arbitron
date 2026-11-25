package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A corrupt pin row must fail the run, not just refuse the provider request
// while leaving a running row that permanently occupies queue capacity.
func TestModelPinsCorruptionFailsRunAndFreesCapacity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload any
		version any
	}{
		{"NULL snapshot", nil, 1},
		{"BLOB version 0", nil, []byte("0")},
		{"BLOB version 1", `{"pin-alias":"deepseek/deepseek-v4-flash"}`, []byte("1")},
		{"payload on legacy", `{}`, 0},
		{"invalid JSON", `{broken`, 1},
		{"unsupported version", `{}`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds := bootSinkStack(t)
			sink := newModelSink(t)
			ctx := adminCtxForLocalUser(t, ds)
			crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
			require.NoError(t, err)
			conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
			require.NoError(t, conns.Create(ctx, provider.Connection{Name: "sink", Provider: "openrouter", BaseURL: sink.srv.URL}, "k"))
			_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, ConnectionRef: "sink", Model: "deepseek/deepseek-v4-flash"})
			require.NoError(t, err)

			// Occupy the sole slot so we can corrupt a genuinely triggered row
			// before its graph walker starts. The holder has no walker.
			ds.queueMgr.UpdateConfig(1, 1)
			require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{
				ID: uuid.New(), OwnerID: localDefaultUser, WorkflowName: "capacity-holder",
				State: instance.StateRunning, Priority: instance.PriorityNormal,
				WorkflowSnapshot: map[string]any{}, Variables: map[string]any{},
				ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
			}))
			id, started, err := ds.eng.Trigger(ctx, "pin-llm", nil, "normal")
			require.NoError(t, err)
			require.False(t, started)
			_, _, err = ds.profileSvc.Update(ctx, lifecycleProfile(lifecycleModelY))
			require.NoError(t, err)
			_, err = ds.db.Exec(`UPDATE instances SET model_pins = ?, model_pins_version = ? WHERE id = ?`, tc.payload, tc.version, id.String())
			require.NoError(t, err)
			if _, blob := tc.version.([]byte); blob {
				var storageType string
				require.NoError(t, ds.db.QueryRow(`SELECT typeof(model_pins_version) FROM instances WHERE id = ?`, id.String()).Scan(&storageType))
				require.Equal(t, "blob", storageType)
			}
			_, err = ds.instanceRepo.Get(ctx, id)
			assert.ErrorContains(t, err, "model pins")

			events, unsubscribe := ds.bus.Subscribe(id)
			defer unsubscribe()
			require.NoError(t, ds.eng.StartInstance(context.Background(), id))
			ds.eng.Shutdown() // synchronously finish the failing graph walker

			var state string
			require.NoError(t, ds.db.QueryRow(`SELECT state FROM instances WHERE id = ?`, id.String()).Scan(&state))
			assert.Equal(t, string(instance.StateFailed), state)
			running, err := ds.queueRepo.CountRunning(ctx)
			require.NoError(t, err)
			assert.Equal(t, 1, running, "only the capacity holder may still be running")
			assert.Empty(t, sink.seen(), "a corrupt pin must never reach the provider")
			trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
			require.NoError(t, err)
			assert.Empty(t, trace, "no node output may be accepted")
			foundFailure := false
			for len(events) > 0 {
				event := <-events
				if event.Type == engine.EventTypeInstanceFailed {
					foundFailure = true
					data, ok := event.Data.(map[string]any)
					require.True(t, ok)
					assert.Contains(t, data["error"], "model pins")
				}
			}
			assert.True(t, foundFailure, "surface the pin corruption in the failure event")
		})
	}
}

// A corrupt QUEUED row is reported by queue admission and failed by the engine
// through the complete terminal path: instance.failed event, failed state, and
// the queue keeps draining (the healthy run behind it starts). An inventory read
// never transitions a corrupt RUNNING row.
func TestModelPinsCorruptQueuedRowFailsThroughEngineRoot(t *testing.T) {
	ds := bootSinkStack(t)
	sink := newModelSink(t)
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "sink", Provider: "openrouter", BaseURL: sink.srv.URL}, "k"))
	_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, ConnectionRef: "sink", Model: "deepseek/deepseek-v4-flash"})
	require.NoError(t, err)

	// Hold the sole slot, queue a run to corrupt and a healthy one behind it.
	ds.queueMgr.UpdateConfig(1, 1)
	holder := uuid.New()
	require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{
		ID: holder, OwnerID: localDefaultUser, WorkflowName: "capacity-holder",
		State: instance.StateRunning, Priority: instance.PriorityNormal,
		WorkflowSnapshot: map[string]any{}, Variables: map[string]any{},
		ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
	}))
	bad, started, err := ds.eng.Trigger(ctx, "pin-llm", nil, "high")
	require.NoError(t, err)
	require.False(t, started)
	good, started, err := ds.eng.Trigger(ctx, "pin-llm", nil, "normal")
	require.NoError(t, err)
	require.False(t, started)
	_, err = ds.db.Exec(`UPDATE instances SET model_pins = '{broken' WHERE id = ?`, bad.String())
	require.NoError(t, err)

	// Inventory reads are read-only: a corrupt live-resolving row keeps its
	// state (paused, so it holds no capacity; the adapter test covers running).
	corruptPaused := uuid.New()
	require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{
		ID: corruptPaused, OwnerID: localDefaultUser, WorkflowName: "corrupt-paused",
		State: instance.StatePaused, Priority: instance.PriorityNormal,
		WorkflowSnapshot: map[string]any{}, Variables: map[string]any{},
		ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
	}))
	_, err = ds.db.Exec(`UPDATE instances SET model_pins = '{broken' WHERE id = ?`, corruptPaused.String())
	require.NoError(t, err)
	_, err = ds.pathInventory.Snapshot(ctx)
	require.NoError(t, err)
	var state string
	require.NoError(t, ds.db.QueryRow(`SELECT state FROM instances WHERE id = ?`, corruptPaused.String()).Scan(&state))
	assert.Equal(t, string(instance.StatePaused), state, "an inventory read must not transition a run")

	events, unsubscribe := ds.bus.Subscribe(bad)
	defer unsubscribe()
	// Free the slot: admission now reaches the corrupt head first.
	require.NoError(t, ds.instanceRepo.SetCompleted(ctx, holder))
	require.NoError(t, ds.queueMgr.Evaluate(ctx))

	require.NoError(t, ds.db.QueryRow(`SELECT state FROM instances WHERE id = ?`, bad.String()).Scan(&state))
	assert.Equal(t, string(instance.StateFailed), state)
	var failedEvent bool
	require.Eventually(t, func() bool {
		for len(events) > 0 {
			if (<-events).Type == engine.EventTypeInstanceFailed {
				failedEvent = true
			}
		}
		return failedEvent
	}, 5*time.Second, 10*time.Millisecond, "the engine's terminal path publishes instance.failed")
	inst := waitTerminal(t, ds, good)
	assert.Equal(t, instance.StateCompleted, inst.State, "the corrupt head must not wedge the queue")
	trace, err := ds.nodeExecRepo.ListByInstance(ctx, bad)
	require.NoError(t, err)
	assert.Empty(t, trace, "the corrupt run never executed a node")
}
