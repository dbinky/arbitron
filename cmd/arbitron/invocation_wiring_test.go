package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeInvocationRoot writes a config and two workflows under root and returns
// the config path: inv-loop (a conditional back-edge) and inv-wait (a timer).
func writeInvocationRoot(t *testing.T, root string) string {
	t.Helper()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))
	writeWorkflow(t, workspaceDir, "inv-loop", `{
  "name": "inv-loop", "description": "loop", "inputs": [],
  "nodes": [
    {"id": "init", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "n", "operation": "set", "value": 0}]}},
    {"id": "bump", "type": "transform",
     "config": {"operations": [{"variable": "n", "operation": "increment", "value": 1}]}},
    {"id": "check", "type": "conditional",
     "config": {"conditions": [{"field": "n", "operator": "lt", "value": 2, "output": "again"}], "defaultOutput": "done"}},
    {"id": "finish", "type": "transform",
     "config": {"operations": [{"variable": "finished", "operation": "set", "value": true}]}}
  ],
  "edges": [
    {"source": "init", "target": "bump"},
    {"source": "bump", "target": "check"},
    {"source": "check", "target": "bump", "output": "again"},
    {"source": "check", "target": "finish", "output": "done"}
  ]
}`)
	writeWorkflow(t, workspaceDir, "inv-wait", `{
  "name": "inv-wait", "description": "timer", "inputs": [],
  "nodes": [
    {"id": "w", "type": "timer_delay", "entrypoint": true, "config": {"durationSeconds": "4"}},
    {"id": "after", "type": "transform",
     "config": {"operations": [{"variable": "done", "operation": "set", "value": true}]}}
  ],
  "edges": [{"source": "w", "target": "after"}]
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
	return configPath
}

type invocationRow struct {
	ID        string
	NodeID    string
	Visit     int
	Committed bool
}

// invocationRows reads the SINK: the persisted node_invocations rows of a run.
func invocationRows(t *testing.T, db *sql.DB, runID uuid.UUID) []invocationRow {
	t.Helper()
	rows, err := db.Query(`SELECT id, node_id, visit, committed_at IS NOT NULL FROM node_invocations WHERE run_id = ? ORDER BY node_id, visit`, runID.String())
	require.NoError(t, err)
	defer rows.Close()
	var out []invocationRow
	for rows.Next() {
		var r invocationRow
		require.NoError(t, rows.Scan(&r.ID, &r.NodeID, &r.Visit, &r.Committed))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func waitState(t *testing.T, ds *domainStack, id uuid.UUID, want instance.State) {
	t.Helper()
	require.Eventually(t, func() bool {
		inst, err := ds.instanceRepo.Get(context.Background(), id)
		return err == nil && inst.State == want
	}, 20*time.Second, 50*time.Millisecond, "run never reached %s", want)
}

// PROVENANCE PROBE: without eng.SetInvocationStore(adaptsqlite.NewInvocationRepo(db))
// the engine keeps its in-memory store and NO rows are written.
func TestInvocationWiring_NodeVisitsPersistAndBackEdgesGetNewInvocations(t *testing.T) {
	root := t.TempDir()
	ds, err := initDomain(context.Background(), writeInvocationRoot(t, root))
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })

	id, _, err := ds.eng.Trigger(context.Background(), "inv-loop", map[string]any{}, "normal")
	require.NoError(t, err)
	waitState(t, ds, id, instance.StateCompleted)

	rows := invocationRows(t, ds.db, id)
	byNode := map[string][]invocationRow{}
	for _, r := range rows {
		assert.True(t, r.Committed, "every visit of a completed run is committed: %+v", r)
		byNode[r.NodeID] = append(byNode[r.NodeID], r)
	}
	require.Len(t, byNode["init"], 1)
	require.Len(t, byNode["bump"], 2, "the back-edge revisits bump once")
	require.Len(t, byNode["check"], 2)
	require.Len(t, byNode["finish"], 1)
	assert.Equal(t, []int{1, 2}, []int{byNode["bump"][0].Visit, byNode["bump"][1].Visit})
	assert.NotEqual(t, byNode["bump"][0].ID, byNode["bump"][1].ID, "loop revisits have distinct durable invocation ids")
}

func TestInvocationWiring_RestartReusesOpenInvocation(t *testing.T) {
	root := t.TempDir()
	configPath := writeInvocationRoot(t, root)

	first, err := initStartedDomain(context.Background(), configPath)
	require.NoError(t, err)
	id, _, err := first.eng.Trigger(context.Background(), "inv-wait", map[string]any{}, "normal")
	require.NoError(t, err)
	waitState(t, first, id, instance.StateWaiting)
	open := invocationRows(t, first.db, id)
	require.Len(t, open, 1)
	assert.Equal(t, "w", open[0].NodeID)
	assert.False(t, open[0].Committed, "a waiting visit stays open across the restart")
	first.Close(context.Background())

	second, err := initStartedDomain(context.Background(), configPath)
	require.NoError(t, err)
	t.Cleanup(func() { second.Close(context.Background()) })
	waitState(t, second, id, instance.StateCompleted)

	rows := invocationRows(t, second.db, id)
	require.Len(t, rows, 2, "w (reused, now committed) + after; a restart must not begin a second w visit")
	assert.Equal(t, open[0].ID, rows[1].ID, "rows are ordered by node: after, w")
	assert.Equal(t, "w", rows[1].NodeID)
	assert.True(t, rows[1].Committed)
	assert.Equal(t, 1, rows[1].Visit)
}

func TestInvocationWiring_AllStampingSitesShareTheDurableStore(t *testing.T) {
	ds, err := initDomain(context.Background(), writeInvocationRoot(t, t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })

	_, durable := ds.invocations.(*adaptsqlite.InvocationRepo)
	require.True(t, durable, "the root must install the SQLite invocation repo")
	assert.Same(t, ds.invocations, ds.eng.InvocationStore())
	h, ok := ds.conversationHandler.(interface{ InvocationStore() engine.InvocationStore })
	require.True(t, ok)
	assert.Same(t, ds.invocations, h.InvocationStore(), "interactive turns must key under the persisted visit")
	require.NotNil(t, ds.groupResolution)
	assert.Same(t, ds.invocations, ds.groupResolution.InvocationStore(), "resolution sessions must key under the persisted visit")
}
