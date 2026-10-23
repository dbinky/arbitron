package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The agent must inspect the prompt's real refs and change the real index. Like
// the real CLI it reports the model that served the turn: the resolver alias is
// pinned at trigger, so the pinned-call verification requires modelUsage.
const repairingResolverScript = `#!/bin/sh
set -eu
prompt=$(cat)
printf '%s' "$prompt" | grep 'MERGE_HEAD' >/dev/null
test "$(git show MERGE_HEAD:shared.txt)" = from-b
test "$(git show MERGE_HEAD^:shared.txt)" = base
printf 'combined\n' > shared.txt
git add -- shared.txt
printf '%s\n' '{"type":"system","subtype":"init","session_id":"repairing-resolver"}'
printf '%s\n' '{"type":"result","is_error":false,"result":"Resolved","session_id":"repairing-resolver","usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{"claude-sonnet-4-5":{}}}'
`

func TestGroupFilesE2E_AutomaticRepairContinuesWithoutOperator(t *testing.T) {
	paths := writeFilesStack(t, map[string]string{"grp-files-conflict": filesConflictManifest})
	require.NoError(t, os.WriteFile(filepath.Join(paths.root, "fake-claude"), []byte(repairingResolverScript), 0o700))
	ds, err := initStartedDomain(context.Background(), paths.configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	id := triggerGroupWorkflow(t, ds, "grp-files-conflict")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, id))
	assert.Equal(t, "success", branchVariable(t, ds, id))
	assert.Equal(t, "combined", checkOutput(t, ds, id, "check-ok")["shared"])
	assert.Equal(t, "alpha", checkOutput(t, ds, id, "check-ok")["a"])
	run := groupRunOf(t, ds, id, "group-1")
	require.NotNil(t, run.Gather)
	require.Len(t, run.Gather.Attempts, 1)
	assert.Equal(t, engine.ResolutionOutcomeResolved, run.Gather.Attempts[0].Outcome)
	rows := groupSessionRows(t, ds, id)
	require.Len(t, rows, 1)
	assert.False(t, engine.IsOpenGroupResolutionSession(rows[0].LLMConversation))
	notes, err := ds.notifRepo.List(context.Background(), notification.ListFilters{Limit: 200})
	require.NoError(t, err)
	assert.False(t, hasNotification(notes, id, notification.TypeConflictResolution))
}

// A resolver's success response cannot turn an aborted merge into success.
// Both automatic verification and operator end must reach rollback/failure.
func TestGroupFilesE2E_AbortedResolutionRollsBackInsteadOfDroppingTaskChanges(t *testing.T) {
	for _, phase := range []string{"autonomous", "operator_end"} {
		t.Run(phase, func(t *testing.T) {
			paths := writeFilesStack(t, map[string]string{"grp-files-conflict": filesConflictManifest})
			if phase == "autonomous" {
				script := strings.Replace(repairingResolverScript, "printf 'combined\\n' > shared.txt\ngit add -- shared.txt", "git merge --abort", 1)
				require.NotEqual(t, repairingResolverScript, script)
				require.NoError(t, os.WriteFile(filepath.Join(paths.root, "fake-claude"), []byte(script), 0o700))
			}
			ds, err := initStartedDomain(context.Background(), paths.configPath)
			require.NoError(t, err)
			t.Cleanup(func() { ds.Close(context.Background()) })
			id := triggerGroupWorkflow(t, ds, "grp-files-conflict")
			if phase == "operator_end" {
				run := waitForOpenResolution(t, ds, id)
				gitIn(t, stagingDirOf(t, run), "merge", "--abort")
				_, err = ds.conversationHandler.EndSession(adminCtxForLocalUser(t, ds), id)
				require.NoError(t, err)
			}
			require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, id))
			require.Equal(t, "failure", branchVariable(t, ds, id), "task b was never merged")
			assert.Equal(t, "base", checkOutput(t, ds, id, "check-fail")["shared"])
			assert.Equal(t, false, checkOutput(t, ds, id, "check-fail")["aPresent"])
			assert.NotContains(t, parentVariables(t, ds, id), "check-ok")
			final := groupRunOf(t, ds, id, "group-1")
			assert.Equal(t, "success", final.Decision.Outcome)
			assert.Equal(t, "failure", final.ContinuationPort)
			assert.Equal(t, "rolled_back", final.Gather.RollbackStatus)
			assert.False(t, gatherTaskReport(t, final, "b").Merged)
			assert.Contains(t, final.Gather.Error, "pending merge")
			rows := groupSessionRows(t, ds, id)
			require.Len(t, rows, 1)
			assert.False(t, engine.IsOpenGroupResolutionSession(rows[0].LLMConversation), "aborting must close the durable session")
		})
	}
}

// Inject actual parent files/modes before the snapshot, then read inherited
// task modes and post-gather parent modes through ordinary tool_call producers.
func filesPermissionManifest(t *testing.T, manifest string) string {
	t.Helper()
	var wf map[string]any
	require.NoError(t, json.Unmarshal([]byte(manifest), &wf))
	nodes := wf["nodes"].([]any)
	edges := wf["edges"].([]any)
	for _, raw := range nodes {
		n := raw.(map[string]any)
		if n["id"] == "seed" {
			args := n["config"].(map[string]any)["args"].([]any)
			args[1] = `printf secret > "$1/private.txt"; printf '#!/bin/sh\nexit 0\n' > "$1/run.sh"; mkdir "$1/readonly"; printf kept > "$1/readonly/kept"; chmod 600 "$1/private.txt"; chmod 751 "$1/run.sh"; chmod 555 "$1/readonly"; ` + args[1].(string)
		}
	}
	// Each task reports its starting permissions before mutating any files.
	// Both continuations report the parent's permissions after apply/rollback.
	for _, id := range []string{"a-run", "b-run", "onSuccess", "onFailure"} {
		modeID := "modes-" + id
		for _, raw := range edges {
			e := raw.(map[string]any)
			if e["target"] == id {
				e["target"] = modeID
			}
		}
		for _, raw := range nodes {
			n := raw.(map[string]any)
			if n["type"] != "parallel_group" {
				continue
			}
			for _, task := range n["config"].(map[string]any)["group"].(map[string]any)["tasks"].([]any) {
				task := task.(map[string]any)
				if task["entry"] == id {
					task["entry"] = modeID
				}
			}
		}
		nodes = append(nodes, map[string]any{"id": modeID, "type": "tool_call", "config": map[string]any{
			"command": "sh", "args": []string{"-c", `mode() { LC_ALL=C ls -ld "$1" | cut -c1-10; }; printf '{"private":"%s","exec":"%s","dir":"%s","bytes":"%s"}' "$(mode "$1/private.txt")" "$(mode "$1/run.sh")" "$(mode "$1/readonly")" "$(cat "$1/private.txt")"`, "sh", "{{workspace}}"},
		}})
		edges = append(edges, map[string]any{"source": modeID, "target": id})
	}
	wf["nodes"], wf["edges"] = nodes, edges
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	return string(b)
}

func assertFileModes(t *testing.T, output map[string]any) {
	t.Helper()
	assert.Equal(t, "-rw-------", output["private"])
	assert.Equal(t, "-rwxr-x--x", output["exec"])
	assert.Equal(t, "dr-xr-xr-x", output["dir"])
	assert.Equal(t, "secret", output["bytes"])
}

func assertInheritedFileModes(t *testing.T, ds *domainStack, id uuid.UUID) {
	t.Helper()
	out := groupOutputOf(t, ds, id, "group-1")
	for _, key := range []string{"a", "b"} {
		result := resultOf(t, groupTaskByKey(t, out, key))
		modes, ok := result["modes-"+key+"-run"].(map[string]any)
		require.True(t, ok)
		assertFileModes(t, modes)
	}
}

func TestGroupFilesE2E_StartingCopyAndGatherPreservePermissions(t *testing.T) {
	ds, paths := bootFilesStack(t, map[string]string{"grp-files-all": filesPermissionManifest(t, filesAllManifest)})
	id := triggerGroupWorkflow(t, ds, "grp-files-all")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, id))
	require.Equal(t, "success", branchVariable(t, ds, id))
	assertInheritedFileModes(t, ds, id)
	assertFileModes(t, checkOutput(t, ds, id, "modes-onSuccess"))
	assert.Equal(t, "alpha", checkOutput(t, ds, id, "check")["a"])
	run := groupRunOf(t, ds, id, "group-1")
	require.Eventually(t, func() bool {
		return !pathExists(backingDirOf(t, paths, run.ID)) &&
			!pathExists(taskWorkspaceDir(t, paths, groupRosterTask(t, run, "a").ChildInstanceID)) &&
			!pathExists(taskWorkspaceDir(t, paths, groupRosterTask(t, run, "b").ChildInstanceID))
	}, 20*time.Second, 100*time.Millisecond, "read-only copied directories must not prevent production pruning")
}

func TestGroupFilesE2E_FailedApplyRoutesFailureAndRestoresBytesAndModes(t *testing.T) {
	paths := writeFilesStack(t, map[string]string{"grp-files-conflict": filesPermissionManifest(t, filesConflictManifest)})
	// Fail the real checkout only in the parent's area, once, after a partial
	// write. Rollback then uses the real Git process and original manifest.
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	bin := filepath.Join(paths.root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700))
	armed := filepath.Join(paths.root, "fail-apply")
	wrapper := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = checkout-index ] && [ -f '" + armed + "' ]; then\n  rm '" + armed + "'\n  printf partial > private.txt\n  chmod 644 private.txt\n  exit 93\n fi\ndone\nexec '" + realGit + "' \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ds, err := initStartedDomain(context.Background(), paths.configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	id := triggerGroupWorkflow(t, ds, "grp-files-conflict")
	run := waitForOpenResolution(t, ds, id)
	resolveConflictInStaging(t, run, "resolved\n")
	require.NoError(t, os.WriteFile(armed, nil, 0o600))
	_, err = ds.conversationHandler.EndSession(adminCtxForLocalUser(t, ds), id)
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, id))
	assert.False(t, pathExists(armed), "real Git checkout reached the forced apply failure")
	assert.Equal(t, "failure", branchVariable(t, ds, id))
	assertInheritedFileModes(t, ds, id)
	assertFileModes(t, checkOutput(t, ds, id, "modes-onFailure"))
	check := checkOutput(t, ds, id, "check-fail")
	assert.Equal(t, "base", check["shared"])
	assert.Equal(t, false, check["aPresent"])
	final := groupRunOf(t, ds, id, "group-1")
	assert.Equal(t, "success", final.Decision.Outcome)
	assert.Equal(t, "failure", final.ContinuationPort)
	assert.Equal(t, "rolled_back", final.Gather.RollbackStatus)
	assert.True(t, strings.Contains(final.Gather.Error, "apply"), final.Gather.Error)
	out := groupOutputOf(t, ds, id, "group-1")
	assert.Equal(t, "failure", out["outcome"])
	assert.Equal(t, "success", out["taskOutcome"])
}

// A failed final-output write happens AFTER the real parent files changed.
// Node ErrorBehavior must not expose that area to a downstream node until the
// frozen output exists. Removing the gather-error barrier makes each case fail.
func TestGroupFilesE2E_OutputStorageFailureBlocksErrorBehaviorUntilRecovered(t *testing.T) {
	for _, behavior := range []string{"skip", "route", "retry"} {
		t.Run(behavior, func(t *testing.T) {
			var manifest map[string]any
			require.NoError(t, json.Unmarshal([]byte(filesAllManifest), &manifest))
			for _, raw := range manifest["nodes"].([]any) {
				node := raw.(map[string]any)
				if node["id"] == "group-1" {
					node["errorBehavior"] = map[string]any{"type": behavior, "errorNodeId": "onFailure", "retries": 2}
				}
			}
			raw, err := json.Marshal(manifest)
			require.NoError(t, err)
			ds, _ := bootFilesStack(t, map[string]string{"grp-files-all": string(raw)})
			_, err = ds.db.Exec(`CREATE TRIGGER test_reject_gather_output BEFORE UPDATE OF final_output ON group_runs
WHEN NEW.gather_mode = 'results_and_files' AND NEW.state = 'done'
BEGIN SELECT RAISE(ABORT, 'test gather output storage unavailable'); END`)
			require.NoError(t, err)
			id := triggerGroupWorkflow(t, ds, "grp-files-all")
			require.Eventually(t, func() bool {
				parent, err := ds.instanceRepo.Get(context.Background(), id)
				if err != nil {
					return false
				}
				if parent.State.IsTerminal() {
					return true // diagnose unsafe completion/failure immediately
				}
				run, err := ds.groupRunRepo.GetByParentNode(context.Background(), id, "group-1")
				return err == nil && run != nil && run.Gather != nil && run.Gather.State == "applied" && parent.State == instance.StateWaiting
			}, 20*time.Second, 20*time.Millisecond)
			parent, err := ds.instanceRepo.Get(context.Background(), id)
			require.NoError(t, err)
			require.Equal(t, instance.StateWaiting, parent.State, "storage errors must bypass node ErrorBehavior")
			require.NotNil(t, parent.CurrentNodeID)
			assert.Equal(t, "group-1", *parent.CurrentNodeID)
			assert.NotContains(t, parent.Variables, "branch")
			assert.NotContains(t, parent.Variables, "check")
			assert.NotContains(t, parent.Variables, "group-1")
			run := groupRunOf(t, ds, id, "group-1")
			assert.Equal(t, instance.GroupRunGathering, run.State)
			assert.Nil(t, run.FinalOutput)
			assert.False(t, run.ContinuationDelivered)
			workspace := parent.Variables[engine.ReservedVarWorkspace].(string)
			content, err := os.ReadFile(filepath.Join(workspace, "a.txt"))
			require.NoError(t, err)
			assert.Equal(t, "alpha", string(content), "failure occurred after the real Apply")
			rows, err := ds.nodeExecRepo.ListByInstance(context.Background(), id)
			require.NoError(t, err)
			failedAttempts := 0
			for _, row := range rows {
				if row.NodeID == "group-1" && row.Status == instance.NodeStatusFailed {
					failedAttempts++
					assert.Contains(t, row.Error["error"], "test gather output storage unavailable")
				}
			}
			assert.Equal(t, 1, failedAttempts, "reconciliation, rather than node retry, owns recovery")

			_, err = ds.db.Exec(`DROP TRIGGER test_reject_gather_output`)
			require.NoError(t, err)
			// Drive the same state-and-node CAS used by gather reconciliation,
			// without waiting a minute for the production sweep timer.
			waker := ds.queueRepo.(instance.NodeAddressedWaker)
			woke, err := waker.SetQueuedIfWaitingAtNode(context.Background(), id, "group-1", time.Now())
			require.NoError(t, err)
			require.True(t, woke)
			ds.queueMgr.NotifyCapacityFreed(context.Background())
			require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, id))
			assert.Equal(t, workflow.GroupPortSuccess, branchVariable(t, ds, id))
			assert.Equal(t, "alpha", checkOutput(t, ds, id, "check")["a"])
			assert.Equal(t, "beta", checkOutput(t, ds, id, "check")["b"])
			final := groupRunOf(t, ds, id, "group-1")
			assert.Equal(t, run.ID, final.ID)
			assert.Equal(t, "success", final.FinalOutput["outcome"])
			assert.True(t, final.ContinuationDelivered)
		})
	}
}
