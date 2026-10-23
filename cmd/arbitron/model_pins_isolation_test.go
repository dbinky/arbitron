package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing isolation from ListRunning prevents reboot; removing it from
// NextEligible stalls the healthy queued row. Resolving live sends Y, not X.
func TestModelPinsCorruptRowsDoNotBlockRestartOrQueue(t *testing.T) {
	ds, closeStack := bootSinkRootWithClose(t, "", func(dir string) {
		writeWorkflow(t, dir, "isolated", `{"name":"isolated","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"pin-alias","templateFile":"templates/p.md"}}],"edges":[]}`)
		writePinTemplate(t, dir, "isolated")
	})
	sink := newModelSink(t)
	ctx := installLifecycleProfile(t, ds, sink)
	ds.queueMgr.UpdateConfig(1, 1)
	holder := uuid.New()
	require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{
		ID: holder, OwnerID: localDefaultUser, WorkflowName: "holder",
		State: instance.StateRunning, Priority: instance.PriorityNormal,
		WorkflowSnapshot: map[string]any{}, Variables: map[string]any{},
		ModelPins: map[string]string{}, ModelPinsVersion: 1,
	}))
	ids := make([]uuid.UUID, 4)
	for i := range ids {
		priority := "normal"
		if i == 1 {
			priority = "high"
		} // corrupt queue head precedes healthy work
		id, started, err := ds.eng.Trigger(ctx, "isolated", nil, priority)
		require.NoError(t, err)
		require.False(t, started)
		ids[i] = id
	}
	_, _, err := ds.profileSvc.Update(ctx, lifecycleProfile(lifecycleModelY))
	require.NoError(t, err)
	dbPath := ds.cfg.Database.Path
	configPath := filepath.Join(filepath.Dir(dbPath), "config.yaml")
	closeStack()

	// Simulate disk corruption and interrupted execution while the service is down.
	db, err := adaptsqlite.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`UPDATE instances SET state = 'completed' WHERE id = ?`, holder.String())
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE instances SET state = 'running', current_node_id = 'ask' WHERE id IN (?,?)`, ids[0].String(), ids[2].String())
	require.NoError(t, err)
	for _, id := range ids[:2] {
		_, err = db.Exec(`UPDATE instances SET model_pins = '{broken', model_pins_version = 1 WHERE id = ?`, id.String())
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())

	restarted, err := initRecoveryTestDomain(context.Background(), configPath)
	require.NoError(t, err, "one corrupt running row must not abort startup")
	t.Cleanup(func() { restarted.Close(context.Background()) })
	restarted.queueMgr.UpdateConfig(1, 1)
	require.NoError(t, restarted.queueMgr.Evaluate(ctx), "a corrupt queue head must not stop draining")
	require.Equal(t, instance.StateCompleted, waitTerminal(t, restarted, ids[3]).State)
	require.NoError(t, restarted.eng.Retry(ctx, ids[2], nil))
	require.Equal(t, instance.StateCompleted, waitTerminal(t, restarted, ids[2]).State)
	restarted.eng.Shutdown()
	assert.Equal(t, []string{lifecycleModelX, lifecycleModelX}, sink.seen())
	for _, id := range ids[2:] {
		assertLifecyclePins(t, restarted, id, lifecycleModelX)
	}
	for _, id := range ids[:2] {
		var state, raw string
		var version int
		var completed sql.NullString
		require.NoError(t, restarted.db.QueryRow(`SELECT state, model_pins, model_pins_version, completed_at FROM instances WHERE id = ?`, id.String()).Scan(&state, &raw, &version, &completed))
		assert.Equal(t, "failed", state)
		assert.Equal(t, "{broken", raw, "retain the original corrupt payload")
		assert.Equal(t, 1, version)
		assert.True(t, completed.Valid)
		_, err := restarted.instanceRepo.Get(ctx, id)
		require.ErrorContains(t, err, "model pins", "never reinterpret corruption as legacy")
		trace, err := restarted.nodeExecRepo.ListByInstance(ctx, id)
		require.NoError(t, err)
		assert.Empty(t, trace, "no corrupt row may execute a node")
	}
	running, err := restarted.queueRepo.CountRunning(ctx)
	require.NoError(t, err)
	assert.Zero(t, running, "failed corrupt runs must release capacity")
}
