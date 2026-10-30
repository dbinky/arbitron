package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bootSubtreeStack boots the REAL composition root with a three-level
// fire-and-forget chain: cs-root -> cs-mid -> cs-leaf. Root and mid complete
// almost immediately; the leaf sleeps 30s. 30s is far beyond every wait in
// these tests, so a leaf observed as "cancelled" can only have been cancelled
// (process-group SIGKILL via the run context), never confused with finishing.
func bootSubtreeStack(t *testing.T) *domainStack {
	t.Helper()
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))

	writeWorkflow(t, workspaceDir, "cs-leaf", `{
  "name": "cs-leaf",
  "description": "long-running leaf",
  "inputs": [],
  "nodes": [
    {"id": "hold", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "sleep 30; echo '{}'"]}}
  ],
  "edges": []
}`)
	writeWorkflow(t, workspaceDir, "cs-mid", `{
  "name": "cs-mid",
  "description": "completes after spawning the leaf",
  "inputs": [],
  "nodes": [
    {"id": "m1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "mid", "operation": "set", "value": true}]}},
    {"id": "spawn-leaf", "type": "spawn_workflow",
     "config": {"childWorkflow": "cs-leaf", "spawnMode": "fire_and_forget"}}
  ],
  "edges": [
    {"source": "m1", "target": "spawn-leaf"}
  ]
}`)
	writeWorkflow(t, workspaceDir, "cs-root", `{
  "name": "cs-root",
  "description": "completes after spawning the mid",
  "inputs": [],
  "nodes": [
    {"id": "r1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "root", "operation": "set", "value": true}]}},
    {"id": "spawn-mid", "type": "spawn_workflow",
     "config": {"childWorkflow": "cs-mid", "spawnMode": "fire_and_forget"}}
  ],
  "edges": [
    {"source": "r1", "target": "spawn-mid"}
  ]
}`)

	configPath := filepath.Join(root, "config.yaml")
	cfg := "" +
		"server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: " + yamlQuote(filepath.Join(root, "arbitron.db")) + "\n" +
		"workspace:\n  path: " + yamlQuote(workspaceDir) + "\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: " + yamlQuote(filepath.Join(root, "arbitron.log")) + "\n" +
		"queue:\n  globalcap: 5\n  perusercap: 5\n"
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))

	ds, err := initStartedDomain(context.Background(), configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	return ds
}

// onlyChild waits for parentID to have exactly one child and returns it.
func onlyChild(t *testing.T, ds *domainStack, parentID uuid.UUID) uuid.UUID {
	t.Helper()
	var kids []instance.InstanceSummary
	require.Eventually(t, func() bool {
		kids = childrenOf(t, ds, parentID)
		return len(kids) == 1
	}, 40*time.Second, 100*time.Millisecond, "parent %s never produced exactly one child", parentID)
	return kids[0].ID
}

// A completed parent's still-running fire-and-forget child is stopped by
// cancelling the parent, and the parent's own outcome is untouched — through
// the real SQLite ListChildren the composition root installs.
func TestCancelSubtreeE2E_CompletedParentStopsRunningChildThroughCompositionRoot(t *testing.T) {
	t.Parallel()
	ds := bootSubtreeStack(t)

	midID, _, err := ds.eng.Trigger(context.Background(), "cs-mid", map[string]any{}, "normal")
	require.NoError(t, err)
	waitForState(t, ds, midID, instance.StateCompleted)
	leafID := onlyChild(t, ds, midID)
	waitForState(t, ds, leafID, instance.StateRunning)
	before, err := ds.instanceRepo.Get(context.Background(), midID)
	require.NoError(t, err)
	require.NotNil(t, before.CompletedAt)
	require.NotEmpty(t, before.Variables)

	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), midID))

	waitForState(t, ds, leafID, instance.StateCancelled)
	mid, err := ds.instanceRepo.Get(context.Background(), midID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateCompleted, mid.State, "the completed parent's outcome is never reopened")
	assert.Equal(t, before, mid, "state, timestamps, variables and outcome are all unchanged")

	// The request was recorded durably on the real row (migration 019 column,
	// written through the InstanceRepo the composition root installed), so an
	// interrupted walk would be finished by the orphan sweep.
	var stamp *string
	require.NoError(t, ds.db.QueryRow(`SELECT subtree_cancel_requested_at FROM instances WHERE id = ?`,
		midID.String()).Scan(&stamp))
	require.NotNil(t, stamp, "Engine.Cancel on a terminal run must persist the subtree-cancellation intent")
}

// PROVENANCE PROBE for the real InstanceRepo.ListChildren: root and mid are
// BOTH completed, the leaf is running two levels down. A repository that
// filtered terminal rows (or an engine still walking ListActiveChildren) never
// lists the completed mid and the leaf keeps running.
func TestCancelSubtreeE2E_CascadeCrossesCompletedIntermediateThroughCompositionRoot(t *testing.T) {
	t.Parallel()
	ds := bootSubtreeStack(t)

	rootID, _, err := ds.eng.Trigger(context.Background(), "cs-root", map[string]any{}, "normal")
	require.NoError(t, err)
	waitForState(t, ds, rootID, instance.StateCompleted)
	midID := onlyChild(t, ds, rootID)
	waitForState(t, ds, midID, instance.StateCompleted)
	leafID := onlyChild(t, ds, midID)
	waitForState(t, ds, leafID, instance.StateRunning)

	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), rootID))

	waitForState(t, ds, leafID, instance.StateCancelled)
	waitForState(t, ds, midID, instance.StateCompleted)
	waitForState(t, ds, rootID, instance.StateCompleted)
}

// A completed run with nothing left running keeps the old refusal.
func TestCancelSubtreeE2E_TerminalRunWithNothingActiveStillRefuses(t *testing.T) {
	t.Parallel()
	ds := bootSubtreeStack(t)

	leafless, _, err := ds.eng.Trigger(context.Background(), "cs-mid", map[string]any{}, "normal")
	require.NoError(t, err)
	waitForState(t, ds, leafless, instance.StateCompleted)
	leafID := onlyChild(t, ds, leafless)
	waitForState(t, ds, leafID, instance.StateRunning)
	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), leafID))
	waitForState(t, ds, leafID, instance.StateCancelled)

	err = ds.eng.Cancel(adminCtxForLocalUser(t, ds), leafless)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrCannotCancel)
}

// PERIODIC ORPHAN SWEEP PROVENANCE (check 3). The real composition root's
// orphan ticker is the only thing that can stop a running descendant whose
// terminal ancestor carries a subtree-cancellation intent but whose cascade
// never ran (the state an interrupted Engine.Cancel walk leaves). The intent is
// stamped AFTER boot, so the startup pass never saw it; nothing here calls
// Engine.Cancel or any Reconcile. Removing `go sweepSpawnReconcilers` from
// initDomain (or feeding it a no-op reconciler) leaves the leaf running.
func TestCancelSubtreeE2E_OrphanTickerFinishesInterruptedTerminalParentWalk(t *testing.T) {
	prev := spawnSweepInterval
	spawnSweepInterval = 200 * time.Millisecond
	t.Cleanup(func() { spawnSweepInterval = prev })

	ds := bootSubtreeStack(t)
	ctx := context.Background()

	rootID, _, err := ds.eng.Trigger(ctx, "cs-root", map[string]any{}, "normal")
	require.NoError(t, err)
	waitForState(t, ds, rootID, instance.StateCompleted)
	midID := onlyChild(t, ds, rootID)
	waitForState(t, ds, midID, instance.StateCompleted)
	leafID := onlyChild(t, ds, midID)
	waitForState(t, ds, leafID, instance.StateRunning)

	// Unstamped completed ancestors legitimately keep independent work: give
	// the ticker several periods to prove it does not touch the leaf yet.
	time.Sleep(700 * time.Millisecond)
	require.Equal(t, instance.StateRunning, stateOf(t, ds, leafID), "no intent recorded: the leaf is legitimate independent work")

	// Record only the intent on the completed root, as an interrupted cancel
	// would. The root's walk never reached mid or leaf.
	// Written with the same statement InstanceRepo.MarkSubtreeCancelRequested uses.
	_, err = ds.db.Exec(`UPDATE instances
   SET subtree_cancel_requested_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
 WHERE id = ? AND state IN ('completed','failed','cancelled')`, rootID.String())
	require.NoError(t, err)

	waitForState(t, ds, leafID, instance.StateCancelled)
	assert.Equal(t, instance.StateCompleted, stateOf(t, ds, midID), "completed intermediates are never reopened")
	assert.Equal(t, instance.StateCompleted, stateOf(t, ds, rootID), "the stamped parent's outcome is never reopened")
}
