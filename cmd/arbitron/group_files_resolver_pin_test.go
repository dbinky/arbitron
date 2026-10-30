package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingResolverScript is fakeResolverScript that logs every launch's argv
// and, like the real CLI, reports the requested --model as the one that served
// the turn (pinned verification checks modelUsage).
func recordingResolverScript(argvLog string) string {
	return "#!/bin/sh\n" +
		"echo \"$*\" >> '" + argvLog + "'\n" +
		"cat >/dev/null\n" +
		"m=$(echo \"$*\" | sed -n 's/.*--model \\([^ ]*\\).*/\\1/p')\n" +
		"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"fake-resolver\"}'\n" +
		"printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"Working on it.\"}]}}'\n" +
		"echo '{\"type\":\"result\",\"is_error\":false,\"result\":\"Working on it.\",\"session_id\":\"fake-resolver\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1},\"modelUsage\":{\"'\"$m\"'\":{}}}'\n"
}

// The parallel_group resolver is a run model consumer: its alias is pinned at
// trigger, and BOTH the bridge's autonomous conflict turn and the human
// continuation through the conversation handler run the trigger-time model
// after the live profile advances. Before the fix the parent pinned nothing for
// "resolver", the autonomous turn used the live model, and the first human
// message failed with ErrPinUnavailable.
func TestGroupFilesE2E_ResolverTurnsRunTheTriggerTimeModel(t *testing.T) {
	t.Parallel()
	paths := writeFilesStack(t, nil)
	argvLog := filepath.Join(paths.root, "resolver-argv.log")
	require.NoError(t, os.WriteFile(filepath.Join(paths.root, "fake-claude"), []byte(recordingResolverScript(argvLog)), 0o700))
	gate := filepath.Join(paths.root, "release-b")
	gated := strings.Replace(filesConflictManifest, `printf 'from-b\\n'`, `while [ ! -e `+gate+` ]; do sleep 0.05; done; printf 'from-b\\n'`, 1)
	require.NotEqual(t, filesConflictManifest, gated)
	writeWorkflow(t, paths.workspaceDir, "grp-files-conflict", gated)

	ds, err := initStartedDomain(context.Background(), paths.configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	ctx := adminCtxForLocalUser(t, ds)

	parentID := triggerGroupWorkflow(t, ds, "grp-files-conflict")
	parent, err := ds.instanceRepo.Get(ctx, parentID)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"resolver": "claude-sonnet-4-5"}, parent.ModelPins, "the resolver alias is pinned at trigger")

	// Advance the live profile before any resolver turn exists.
	resolver, err := ds.profileSvc.Get(ctx, "resolver")
	require.NoError(t, err)
	resolver.Model = "claude-sonnet-4-6"
	_, _, err = ds.profileSvc.Update(ctx, resolver)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(gate, nil, 0o600))

	waitForOpenResolution(t, ds, parentID)
	waitArgvLines(t, argvLog, 1) // the autonomous conflict turn
	require.NoError(t, ds.conversationHandler.HandleMessage(ctx, parentID, "please resolve"), "the human continuation must resolve the parent's pin")
	waitArgvLines(t, argvLog, 2)

	argv := readArgvLog(t, argvLog)
	require.GreaterOrEqual(t, len(argv), 2)
	for i, line := range argv {
		assert.Contains(t, line, "--model claude-sonnet-4-5", "resolver turn %d must carry the trigger-time model: %s", i+1, line)
		assert.NotContains(t, line, "claude-sonnet-4-6", "resolver turn %d leaked the live edit: %s", i+1, line)
	}
	inst, err := ds.instanceRepo.Get(ctx, parentID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateWaiting, inst.State)
}
