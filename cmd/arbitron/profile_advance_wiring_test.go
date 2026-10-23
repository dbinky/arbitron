package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdvanceWiringPublishesNewRun drives the real initDomain: AdvanceModel is
// injected with the SQLite store, the cache publisher and the shared gate. New
// triggers execute the committed model through the real Claude chat adapter.
// The evidence revision and its token come from the stack's own
// ExecutionPathInventory, the gate's production revision source; the store
// re-validates that token's persisted revisions inside the commit transaction.
func TestAdvanceWiringPublishesNewRun(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	workspace := filepath.Join(root, "workspace")
	writeWorkflow(t, workspace, "advance-exec", `{"name":"advance-exec","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}},{"id":"after","type":"llm_call","config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[{"source":"ask","target":"after"}]}`)
	writePinTemplate(t, workspace, "advance-exec")
	argvLog := filepath.Join(root, "argv")
	releaseFile := filepath.Join(root, "release")
	binary := filepath.Join(root, "claude")
	require.NoError(t, os.WriteFile(binary, []byte(`#!/bin/sh
printf '%s\n' "$PWD $*" >> '`+argvLog+`'
prompt=$(cat)
[ "$prompt" = hello ] || exit 1
m=
format=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --model) m="$2"; shift ;;
    --output-format) format="$2"; shift ;;
    --fallback-model) exit 1 ;;
  esac
  shift
done
[ "$format" = stream-json ] || exit 1
case "$m" in
  claude-sonnet-4-5)
    while [ ! -f '`+releaseFile+`' ]; do sleep 0.05; done
    printf '%s\n' '{"type":"result","result":"executed-X","modelUsage":{"claude-sonnet-4-5":{}}}' ;;
  claude-sonnet-4-6) printf '%s\n' '{"type":"result","result":"executed-Y","modelUsage":{"claude-sonnet-4-6":{}}}' ;;
  *) exit 1 ;;
esac
`), 0700))
	contents := "auth:\n  mode: local\ndatabase:\n  path: " + filepath.Join(root, "db") + "\nworkspace:\n  path: " + filepath.Join(root, "workspace") + "\nsamples:\n  seed: false\nlogging:\n  file: " + filepath.Join(root, "log") + `
llm:
  binarypath: ` + binary + `
profiles:
  - alias: worker
    kind: chat
    backend: claude-code
    model: claude-sonnet-4-5
    selection_mode: following
    follow_target:
      provider: anthropic
      tier: sonnet
`
	require.NoError(t, os.WriteFile(path, []byte(contents), 0600))
	ds, err := initDomain(context.Background(), path)
	require.NoError(t, err)
	defer ds.Close(context.Background())
	// Release the leaf before closing the stack even if an assertion fails.
	defer func() { _ = os.WriteFile(releaseFile, nil, 0600) }()
	admin := adminCtxForLocalUser(t, ds)

	storeConfiguredFollowing(t, admin, ds, "worker")
	expected, err := ds.profileSvc.Get(admin, "worker")
	require.NoError(t, err)
	request := func() provider.AdvanceRequest {
		snap, err := ds.pathInventory.Snapshot(context.Background())
		require.NoError(t, err)
		return provider.AdvanceRequest{ID: "adv-1", RefreshID: "refresh-1", Expected: expected, Model: "claude-sonnet-4-6", CanonicalID: "anthropic/claude-sonnet-4-6",
			PathRevision: snap.Revision, ExecutionRevision: snap.ExecutionRevision}
	}
	req := request()
	model := func() string {
		b, err := ds.profileResolver.Resolve(admin, "worker")
		require.NoError(t, err)
		return b.Model
	}
	require.Equal(t, "claude-sonnet-4-5", model())
	oldID, _, err := ds.eng.Trigger(admin, "advance-exec", nil, "normal")
	require.NoError(t, err)
	waitArgvLines(t, argvLog, 1)
	old, err := ds.instanceRepo.Get(admin, oldID)
	require.NoError(t, err)
	require.Equal(t, instance.StateRunning, old.State)
	// The first call is blocked in the harness; the second will resolve only
	// after publication. Use the actual active inventory's current token.
	req = request()

	// Stale evidence: a policy revision bumped by another writer after the
	// snapshot. Nothing commits and the working model keeps resolving.
	_, err = ds.db.Exec(`UPDATE model_identity_policy SET revision = revision + 1 WHERE id = 1`)
	require.NoError(t, err)
	_, err = ds.profileSvc.AdvanceModel(admin, req)
	require.ErrorIs(t, err, provider.ErrExecutionRevisionStale)
	assert.Equal(t, "claude-sonnet-4-5", model())

	// A token missing from an otherwise current request never commits.
	req = request()
	tokenless := req
	tokenless.ExecutionRevision = provider.ExecutionRevision{ID: req.PathRevision}
	_, err = ds.profileSvc.AdvanceModel(admin, tokenless)
	require.ErrorIs(t, err, provider.ErrExecutionRevisionUnavailable)
	assert.Equal(t, "claude-sonnet-4-5", model())

	adv, err := ds.profileSvc.AdvanceModel(admin, req)
	require.NoError(t, err)
	assert.Equal(t, int64(2), adv.Revision)
	assert.Equal(t, "claude-sonnet-4-6", model(), "a new run must resolve the advanced model")

	stored, err := ds.profileSvc.Get(admin, "worker")
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", stored.Model)
	assert.Equal(t, int64(2), stored.Revision)
	var journaled int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_advancement_journal WHERE id = 'adv-1' AND to_model = 'claude-sonnet-4-6'`).Scan(&journaled))
	assert.Equal(t, 1, journaled)

	// A retry of the same request after its commit replays the journaled result
	// (G18), even though the commit itself moved the inventory digest: the
	// original advancement comes back with no third revision or journal row.
	replayed, err := ds.profileSvc.AdvanceModel(admin, req)
	require.NoError(t, err)
	assert.Equal(t, adv.ID, replayed.ID)
	assert.Equal(t, "claude-sonnet-4-6", replayed.ToModel)
	assert.Equal(t, int64(2), replayed.Revision)
	stored, err = ds.profileSvc.Get(admin, "worker")
	require.NoError(t, err)
	assert.Equal(t, int64(2), stored.Revision)
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_advancement_journal WHERE id = 'adv-1'`).Scan(&journaled))
	assert.Equal(t, 1, journaled)
	require.NoError(t, os.WriteFile(releaseFile, nil, 0600))
	assertRun := func(run *instance.Instance, want, output string) {
		t.Helper()
		assert.Equal(t, instance.StateCompleted, run.State)
		assert.Equal(t, want, run.ModelPins["worker"])
		for _, node := range []string{"ask", "after"} {
			var actual string
			require.NoError(t, ds.db.QueryRow(`SELECT output FROM node_executions WHERE instance_id=? AND node_id=?`, run.ID.String(), node).Scan(&actual))
			assert.Contains(t, actual, output, "node %s", node)
		}
	}
	assertRun(waitTerminal(t, ds, oldID), "claude-sonnet-4-5", "executed-X")
	newID, _, err := ds.eng.Trigger(admin, "advance-exec", nil, "normal")
	require.NoError(t, err)
	assertRun(waitTerminal(t, ds, newID), "claude-sonnet-4-6", "executed-Y")
	oldStored, err := ds.instanceRepo.Get(admin, old.ID)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-5", oldStored.ModelPins["worker"])
	argv := readArgvLog(t, argvLog)
	require.Len(t, argv, 4)
	for _, line := range argv[:2] {
		assert.Contains(t, line, "--model claude-sonnet-4-5")
	}
	for _, line := range argv[2:] {
		assert.Contains(t, line, "--model claude-sonnet-4-6")
	}
	for _, line := range argv {
		assert.NotContains(t, line, "--fallback-model")
	}
}
