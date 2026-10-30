package main

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// instance_trigger without a priority reaches the engine as "". Against the real
// SQLite store the instances CHECK (priority IN ('high','normal','low')) refused
// that insert; the engine now defaults it to "normal" before persisting.
func TestTriggerWiringOmittedPriorityPersistsNormal(t *testing.T) {
	ds := bootSinkRoot(t, "", func(ws string) {
		writeWorkflow(t, ws, "no-priority", `{"name":"no-priority","inputs":[],
  "nodes":[{"id":"t","type":"transform","entrypoint":true,"config":{"operations":[{"variable":"done","operation":"set","value":true}]}}],
  "edges":[]}`)
	})
	ctx := adminCtxForLocalUser(t, ds)
	id, _, err := ds.eng.TriggerWithDepartment(ctx, "no-priority", map[string]any{}, "", "")
	require.NoError(t, err)
	waitForState(t, ds, id, instance.StateCompleted)
	inst, err := ds.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.PriorityNormal, inst.Priority)
}
