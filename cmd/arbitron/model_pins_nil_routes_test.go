package main

import (
	"context"
	"testing"
	"time"

	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A typed-nil inventory must behave like an absent one: refuse the trigger,
// even with no aliases, without panicking or creating executable work. Removing
// the constructor's nil normalization panics for alias workflows and silently
// creates a model-free run. The restored real inventory proves the sink is fed.
func TestModelPinsMissingRouteInventoryCreatesNothing(t *testing.T) {
	for routeName, routes := range map[string]provider.WorkflowVersionRequestSource{
		"nil":       nil,
		"typed nil": (*providersadapter.ExecutionPathInventory)(nil),
	} {
		for _, name := range []string{"with-model", "no-models"} {
			t.Run(routeName+"/"+name, func(t *testing.T) {
				ds := bootSinkRoot(t, "", func(dir string) {
					writeWorkflow(t, dir, "with-model", `{"name":"with-model","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"pin-alias","templateFile":"templates/p.md"}}],"edges":[]}`)
					writePinTemplate(t, dir, "with-model")
					writeWorkflow(t, dir, "no-models", `{"name":"no-models","nodes":[{"id":"done","type":"transform","entrypoint":true,"config":{"operations":[{"variable":"ok","operation":"set","value":true}]}}],"edges":[]}`)
				})
				sink := newModelSink(t)
				ctx := installLifecycleProfile(t, ds, sink)
				cache := providersadapter.NewProfileCache(adaptsqlite.NewProfileRepo(ds.db))
				require.NoError(t, cache.Load(ctx))
				ds.eng.SetModelSnapshotter(providersadapter.NewModelSnapshotter(cache, ds.connStore, modelidentity.NewVersionResolver(), routes))

				var id uuid.UUID
				var started bool
				var err error
				require.NotPanics(t, func() {
					id, started, err = ds.eng.Trigger(ctx, name, nil, "normal")
				})
				require.ErrorContains(t, err, "route source")
				assert.Equal(t, uuid.Nil, id)
				assert.False(t, started)
				ds.eng.Shutdown()
				assert.Zero(t, countInstances(t, ds), "capture failure must precede persistence and enqueue")
				assert.Empty(t, sink.seen(), "a refused capture must never invoke the provider")

				// A failed capture must also release the read lease so the same
				// engine can accept a correctly configured snapshotter and run.
				leaseCtx, cancel := context.WithTimeout(ctx, time.Second)
				require.NoError(t, ds.executionGate.WithMutation(leaseCtx, func(context.Context) error { return nil }))
				cancel()
				ds.eng.SetModelSnapshotter(providersadapter.NewModelSnapshotter(cache, ds.connStore, modelidentity.NewVersionResolver(), ds.pathInventory))
				id, _, err = ds.eng.Trigger(ctx, name, nil, "normal")
				require.NoError(t, err)
				require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
				saved, err := ds.instanceRepo.Get(context.Background(), id)
				require.NoError(t, err)
				assert.Equal(t, instance.ModelPinsVersionSnapshot, saved.ModelPinsVersion)
				require.NotNil(t, saved.ModelPins)
				if name == "with-model" {
					assert.Equal(t, map[string]string{"pin-alias": lifecycleModelX}, saved.ModelPins)
					assert.Equal(t, []string{lifecycleModelX}, sink.seen())
				} else {
					assert.Empty(t, saved.ModelPins)
					assert.Empty(t, sink.seen())
				}
			})
		}
	}
}
