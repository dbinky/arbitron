package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroupFilesE2E_ParentCancelledDuringAutonomousResolution(t *testing.T) {
	t.Parallel()
	paths := writeFilesStack(t, map[string]string{"grp-files-conflict": filesConflictManifest})
	// The marker is written from inside the real staging area, only after the
	// CLI adapter launched the autonomous turn. Cancellation must kill the
	// process and roll back without ever publishing an interactive session.
	script := "#!/bin/sh\nset -eu\ncat >/dev/null\nprintf ready > resolver-ready\nexec sleep 60\n"
	require.NoError(t, os.WriteFile(filepath.Join(paths.root, "fake-claude"), []byte(script), 0o700))
	ds, err := initStartedDomain(context.Background(), paths.configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	id := triggerGroupWorkflow(t, ds, "grp-files-conflict")
	var staging string
	require.Eventually(t, func() bool {
		run, err := ds.groupRunRepo.GetByParentNode(context.Background(), id, "group-1")
		if err != nil || run == nil || run.Gather == nil {
			return false
		}
		staging, _ = run.Gather.Handle["stagingDir"].(string)
		return staging != "" && pathExists(filepath.Join(staging, "resolver-ready"))
	}, 20*time.Second, 50*time.Millisecond, "the autonomous resolver never started")
	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), id))
	require.Eventually(t, func() bool {
		run, err := ds.groupRunRepo.GetByParentNode(context.Background(), id, "group-1")
		return err == nil && run != nil && run.State == instance.GroupRunDone
	}, 10*time.Second, 50*time.Millisecond, "cancel must settle without waiting for the resolver or sweep")
	run := groupRunOf(t, ds, id, "group-1")
	require.Len(t, run.Gather.Attempts, 1)
	assert.Equal(t, "cancelled", run.Gather.Attempts[0].Outcome)
	assert.NotNil(t, run.Gather.Attempts[0].EndedAt)
	assert.Equal(t, "failure", run.ContinuationPort)
	assert.Equal(t, "rolled_back", run.Gather.RollbackStatus)
	assert.Nil(t, run.Gather.ActiveSession)
	assert.Empty(t, groupSessionRows(t, ds, id))
	parentDir := taskWorkspaceDir(t, paths, id)
	content, err := os.ReadFile(filepath.Join(parentDir, "shared.txt"))
	require.NoError(t, err)
	assert.Equal(t, "base\n", string(content))
	assert.False(t, pathExists(filepath.Join(parentDir, "a.txt")))
	assert.Equal(t, instance.StateCancelled, stateOf(t, ds, id))
	vars := parentVariables(t, ds, id)
	for _, node := range []string{"check-ok", "onSuccess", "check-fail", "onFailure"} {
		assert.NotContains(t, vars, node, "a cancelled parent must execute neither continuation")
	}
}
