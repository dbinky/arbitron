package main

import (
	"context"
	"github.com/google/uuid"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
)

var p3AgentTemplates = map[string]string{
	"templates/one.md":      "P3-STEP-ONE",
	"templates/two.md":      "P3-STEP-TWO",
	"templates/hold.md":     "P3-HOLD",
	"templates/branch-a.md": "P3-BRANCH-A",
	"templates/branch-b.md": "P3-BRANCH-B",
}

func p3Files(extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range p3AgentTemplates {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

const p3LoopManifest = `{
  "name": "p3-loop",
  "description": "agent, bundled script, agent on one remote home",
  "inputs": [],
  "nodes": [
    {"id": "one", "type": "agent_call", "entrypoint": true, "config": {"model_profile": "default-agent", "templateFile": "templates/one.md"}},
    {"id": "mid", "type": "script", "config": {"scriptFile": "scripts/mid.sh", "portable": true}},
    {"id": "two", "type": "agent_call", "config": {"model_profile": "default-agent", "templateFile": "templates/two.md"}}
  ],
  "edges": [{"source": "one", "target": "mid"}, {"source": "mid", "target": "two"}]
}`

const p3MidScript = "#!/bin/sh\ncat > /dev/null\nprintf '{\"mid\":\"ran\"}'\n"

const p3HoldManifest = `{
  "name": "p3-hold",
  "description": "a completed step, then a step that holds mid-flight",
  "inputs": [],
  "nodes": [
    {"id": "one", "type": "agent_call", "entrypoint": true, "config": {"model_profile": "default-agent", "templateFile": "templates/one.md"}},
    {"id": "two", "type": "agent_call", "config": {"model_profile": "default-agent", "templateFile": "templates/hold.md"}}
  ],
  "edges": [{"source": "one", "target": "two"}]
}`

const p3AdoptManifest = `{
  "name": "p3-adopt",
  "description": "one remote step held across a server restart",
  "inputs": [],
  "nodes": [
    {"id": "hold", "type": "agent_call", "entrypoint": true, "config": {"model_profile": "default-agent", "templateFile": "templates/hold.md"}}
  ],
  "edges": []
}`

const p3BuiltinGateManifest = `{
  "name": "p3-builtin-gate",
  "description": "a built-in tool step held across a server restart",
  "inputs": [],
  "nodes": [
    {"id": "b", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "echo run >> \"$1\"; touch \"$2/b.reached\"; n=0; while [ ! -f \"$2/b.release\" ]; do n=$((n+1)); [ \"$n\" -lt 2400 ] || exit 91; sleep .05; done; echo '{}'", "sh", "{{counter}}", "{{gateDir}}"]}}
  ],
  "edges": []
}`

const p3CrashManifest = `{
  "name": "p3-crash",
  "description": "one counted step; the run's terminal write is held",
  "inputs": [],
  "nodes": [
    {"id": "count", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "echo run >> \"$1\"; echo '{}'", "sh", "{{counter}}"]}}
  ],
  "edges": []
}`

const p3GateChildManifest = `{
  "name": "p3-gate-child",
  "description": "a gated built-in child",
  "inputs": [{"name": "gateDir", "type": "string", "required": true}],
  "nodes": [
    {"id": "gate", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "touch \"$1/child.reached\"; n=0; while [ ! -f \"$1/child.release\" ]; do n=$((n+1)); [ \"$n\" -lt 2400 ] || exit 91; sleep .05; done; echo '{}'", "sh", "{{gateDir}}"]}}
  ],
  "edges": []
}`

const p3WaitManifest = `{
  "name": "p3-wait",
  "description": "remote step, wait on a gated child, remote step",
  "inputs": [],
  "nodes": [
    {"id": "one", "type": "agent_call", "entrypoint": true, "config": {"model_profile": "default-agent", "templateFile": "templates/one.md"}},
    {"id": "wait", "type": "spawn_workflow",
     "config": {"childWorkflow": "p3-gate-child", "spawnMode": "wait_for_child", "inputs": {"gateDir": "{{gateDir}}"}}},
    {"id": "two", "type": "agent_call", "config": {"model_profile": "default-agent", "templateFile": "templates/two.md"}},
    {"id": "onFailure", "type": "transform", "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "one", "target": "wait"},
    {"source": "wait", "target": "two", "output": "success"},
    {"source": "wait", "target": "onFailure", "output": "failure"}
  ]
}`

const p3WaitEndManifest = `{
  "name": "p3-wait-end",
  "description": "remote step, wait on a gated child, server-only finish",
  "inputs": [],
  "nodes": [
    {"id": "one", "type": "agent_call", "entrypoint": true, "config": {"model_profile": "default-agent", "templateFile": "templates/one.md"}},
    {"id": "wait", "type": "spawn_workflow",
     "config": {"childWorkflow": "p3-gate-child", "spawnMode": "wait_for_child", "inputs": {"gateDir": "{{gateDir}}"}}},
    {"id": "done", "type": "transform", "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform", "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "one", "target": "wait"},
    {"source": "wait", "target": "done", "output": "success"},
    {"source": "wait", "target": "onFailure", "output": "failure"}
  ]
}`

const p3FilesManifest = `{
  "name": "p3-files",
  "description": "built-in results_and_files group",
  "inputs": [],
  "nodes": [
    {"id": "seed", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "printf base > \"$1/base.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "g", "type": "parallel_group",
     "config": {"group": {"form": "branches", "tasks": [{"key": "a", "entry": "a-run"}, {"key": "b", "entry": "b-run"}],
       "completionRule": "all_succeed", "unfinishedBehavior": "cancel_unfinished", "gatherMode": "results_and_files",
       "resolverProfile": "default-agent"}}},
    {"id": "a-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf a > \"$1/a.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "b-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "touch \"$2/bf.reached\"; n=0; while [ ! -f \"$2/bf.release\" ]; do n=$((n+1)); [ \"$n\" -lt 2400 ] || exit 91; sleep .05; done; printf b > \"$1/b.txt\"; echo '{}'", "sh", "{{workspace}}", "{{gateDir}}"]}},
    {"id": "check", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf '{\"a\":\"%s\",\"b\":\"%s\"}' \"$(cat \"$1/a.txt\")\" \"$(cat \"$1/b.txt\")\"", "sh", "{{workspace}}"]}},
    {"id": "onFailure", "type": "transform", "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "seed", "target": "g"},
    {"source": "g", "target": "a-run", "output": "task", "label": "a"},
    {"source": "g", "target": "b-run", "output": "task", "label": "b"},
    {"source": "g", "target": "check", "output": "success"},
    {"source": "g", "target": "onFailure", "output": "failure"}
  ]
}`

// Design §8 Happy path: agent_call → script → agent_call on ONE remote home
// pulls once and pushes after every step; every step runs at the home.
// Fails without DispatcherDeps.Workspace (agent steps degrade to the
// logged-out built-in runner and never place), without Hub.SetWorkspaceJobs,
// without ScriptExecutor.SetBundleStore (the script cannot leave the server),
// or without RouterDeps.RunnerTransfers (pulls and pushes 404).
func TestPhase3E2E_RemoteHomeLoop(t *testing.T) {
	st := bootP3Stack(t, map[string]p3Workflow{"p3-loop": {manifest: p3LoopManifest, files: p3Files(map[string]string{"scripts/mid.sh": p3MidScript})}}, "")
	mac1 := startWorkspaceRunner(t, st, "mac-1")

	id, _, err := st.ds.eng.Trigger(adminCtxForLocalUser(t, st.ds), "p3-loop", map[string]any{}, "normal")
	require.NoError(t, err)
	p3RequireCompleted(t, st.ds, id, 60*time.Second)

	for _, node := range []string{"one", "mid", "two"} {
		assert.Equal(t, []string{mac1.id}, p3CompletedOn(t, st.ds, id, node), "%s runs at the home", node)
	}
	assert.Equal(t, int64(1), mac1.xfer.manifests.Load(), "SINK: zero pulls after the first")
	assert.Zero(t, mac1.xfer.files.Load(), "an empty baseline has nothing to fetch, and the copy is never refetched")
	assert.Equal(t, int64(3), mac1.xfer.commits.Load(), "SINK: a push after each step")
	assert.Equal(t, int64(1), mac1.xfer.bundles.Load(), "the script runs from its bundle")
	assert.Equal(t, []string{mac1.id}, rrStrings(t, st.ds, `SELECT to_runner FROM home_moves WHERE run_id = ? AND to_runner NOT IN ('')`, id.String()),
		"one move to the home and none back to the server")
	assert.Contains(t, p3Output(t, st.ds, id, "mid"), "ran")
	out := p3Output(t, st.ds, id, "two")
	assert.Contains(t, out, "step1,step2,|", "the second agent step sees the first step's file in the reused copy")
	assert.Contains(t, out, "agent.log")
}

// Design §8 Failure + Chaos: the runner holding a step is cut off mid-step;
// the step repeats on another runner from the clean checkpoint and the
// completed step is preserved (not re-run).
func TestPhase3E2E_RunnerLostMidStepRepeatsOnCleanCheckpoint(t *testing.T) {
	st := bootP3Stack(t, map[string]p3Workflow{"p3-hold": {manifest: p3HoldManifest, files: p3Files(nil)}}, "")
	mac1 := startWorkspaceRunner(t, st, "mac-1", runneragent.OpAgent)
	id, _, err := st.ds.eng.Trigger(adminCtxForLocalUser(t, st.ds), "p3-hold", map[string]any{}, "normal")
	require.NoError(t, err)
	st.awaitFile(t, "hold.started", 30*time.Second)
	mac2 := startWorkspaceRunner(t, st, "mac-2", runneragent.OpAgent)

	mac1.conn.cut()
	p3RequireCompleted(t, st.ds, id, 60*time.Second)

	assert.Equal(t, []string{mac1.id}, p3CompletedOn(t, st.ds, id, "one"))
	assert.Equal(t, 1, st.tagCount(t, "P3-STEP-ONE"), "the completed step is not repeated")
	assert.Equal(t, []string{mac2.id}, p3CompletedOn(t, st.ds, id, "two"))
	assert.Equal(t, 2, st.tagCount(t, "P3-HOLD"), "the interrupted step repeats once")
	out := p3Output(t, st.ds, id, "two")
	assert.Contains(t, out, "step1,step2,|", "SINK: the repeat starts from the checkpoint holding step one's work")
	assert.NotContains(t, out, "partial.txt", "SINK: the interrupted attempt's edit never reaches the repeat")
	assert.Positive(t, p3NoticeCount(t, st.ds, id, "two"), "the repeated step carries the recovery notice")
}

// Design §8 regression: a crash after the step's result/checkpoint commit
// but before the run advanced replays without repeating the step. The
// terminal state write is held by a trigger, so the run is still "running"
// when the process dies. Removing eng.SetStepCommitter(mobility.Commits)
// from main.go makes the first case count 2.
func TestPhase3E2E_CrashAfterCommitReplays(t *testing.T) {
	for _, tc := range []struct {
		name      string
		committer bool
		want      int
	}{
		{"a committed step replays without repeating", true, 1},
		{"without the step committer the step repeats", false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := bootP3Stack(t, map[string]p3Workflow{"p3-crash": {manifest: p3CrashManifest}}, "")
			if !tc.committer {
				st.ds.eng.SetStepCommitter(nil)
			}
			_, err := st.ds.db.Exec(`CREATE TRIGGER p3_hold_terminal BEFORE UPDATE OF state ON instances
				WHEN OLD.workflow_name = 'p3-crash' AND OLD.state = 'running' AND NEW.state != 'running'
				BEGIN SELECT RAISE(ABORT, 'simulated crash before the run advanced'); END`)
			require.NoError(t, err)
			counter := filepath.Join(t.TempDir(), "counter")
			id, _, err := st.ds.eng.Trigger(adminCtxForLocalUser(t, st.ds), "p3-crash", map[string]any{"counter": counter}, "normal")
			require.NoError(t, err)
			require.Eventually(t, func() bool { return nodeExecutionCount(t, st.ds, id, "count") == 1 },
				20*time.Second, 20*time.Millisecond, "the step's result is recorded before the crash")
			st.ds.queueMgr.Stop()
			st.ds.eng.Shutdown() // the walker gives up on the held terminal write
			_, err = st.ds.db.Exec(`DROP TRIGGER p3_hold_terminal`)
			require.NoError(t, err)

			st.crashAndRestart(t)

			p3RequireCompleted(t, st.ds, id, 30*time.Second)
			assert.Equal(t, tc.want, p3CountLines(t, counter), "SINK: how often the step's external effect ran")
		})
	}
}

// Design §8 Edge: on a server restart a remote job re-attaches without
// repeating (job.adopted) and a built-in step repeats on its checkpoint.
func TestPhase3E2E_RestartAdoptsRemoteAndRepeatsBuiltIn(t *testing.T) {
	st := bootP3Stack(t, map[string]p3Workflow{
		"p3-adopt":        {manifest: p3AdoptManifest, files: p3Files(nil)},
		"p3-builtin-gate": {manifest: p3BuiltinGateManifest},
	}, "")
	mac1 := startWorkspaceRunner(t, st, "mac-1", runneragent.OpAgent)
	ctx := adminCtxForLocalUser(t, st.ds)
	counter := filepath.Join(t.TempDir(), "counter")
	remoteID, _, err := st.ds.eng.Trigger(ctx, "p3-adopt", map[string]any{}, "normal")
	require.NoError(t, err)
	builtinID, _, err := st.ds.eng.Trigger(ctx, "p3-builtin-gate", map[string]any{"counter": counter, "gateDir": st.gateDir}, "normal")
	require.NoError(t, err)
	st.awaitFile(t, "hold.started", 30*time.Second)
	st.awaitFile(t, "b.reached", 30*time.Second)

	mac1.conn.cut() // the old server's teardown must not reach the runner
	require.NoError(t, os.Remove(filepath.Join(st.gateDir, "b.reached")))
	st.crashAndRestart(t)
	mac1.conn.allow()

	st.awaitFile(t, "b.reached", 30*time.Second) // the built-in step re-entered
	st.release(t, "b")
	p3RequireCompleted(t, st.ds, builtinID, 30*time.Second)
	st.release(t, "hold")
	p3RequireCompleted(t, st.ds, remoteID, 60*time.Second)

	assert.Equal(t, 1, st.tagCount(t, "P3-HOLD"), "SINK: the remote step ran exactly once")
	assert.Equal(t, []string{mac1.id}, p3CompletedOn(t, st.ds, remoteID, "hold"))
	assert.Len(t, rrStrings(t, st.ds, `SELECT id FROM runner_jobs WHERE run_id = ? AND node_id = 'hold'`, remoteID.String()), 1, "no second job was dispatched")
	assert.NotEmpty(t, rrStrings(t, st.ds, `SELECT kind FROM pool_events WHERE run_id = ? AND kind = ?`, remoteID.String(), runner.EventJobAdopted))
	assert.Equal(t, 2, p3CountLines(t, counter), "SINK: the built-in step repeated")
	assert.Positive(t, p3NoticeCount(t, st.ds, builtinID, "b"))
}

// Design §8 Edge: a runner down for the whole restart grace becomes lost and
// its step is recovered elsewhere.
func TestPhase3E2E_RunnerMissingGraceIsRecovered(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out a 30 s restart grace")
	}
	st := bootP3Stack(t, map[string]p3Workflow{"p3-adopt": {manifest: p3AdoptManifest, files: p3Files(nil)}}, "  restartgraceseconds: 30\n")
	mac1 := startWorkspaceRunner(t, st, "mac-1", runneragent.OpAgent)
	id, _, err := st.ds.eng.Trigger(adminCtxForLocalUser(t, st.ds), "p3-adopt", map[string]any{}, "normal")
	require.NoError(t, err)
	st.awaitFile(t, "hold.started", 30*time.Second)
	mac1.conn.cut() // and never allowed back

	st.crashAndRestart(t)
	mac2 := startWorkspaceRunner(t, st, "mac-2", runneragent.OpAgent)

	p3RequireCompleted(t, st.ds, id, 90*time.Second)
	assert.Equal(t, []string{mac2.id}, p3CompletedOn(t, st.ds, id, "hold"))
	assert.Equal(t, 2, st.tagCount(t, "P3-HOLD"))
	assert.NotEmpty(t, rrStrings(t, st.ds, `SELECT kind FROM pool_events WHERE run_id = ? AND kind = ?`, id.String(), runner.EventJobRecovered))
	assert.Positive(t, p3NoticeCount(t, st.ds, id, "hold"))
}

// Design §8 Edge: workspace deletion reaches a runner that was offline when
// the run's workspace was removed; the server checkpoints go with it.
func TestPhase3E2E_RemovalReachesOfflineRunner(t *testing.T) {
	st := bootP3Stack(t, map[string]p3Workflow{
		"p3-wait-end":   {manifest: p3WaitEndManifest, files: p3Files(nil)},
		"p3-gate-child": {manifest: p3GateChildManifest},
	}, "")
	mac1 := startWorkspaceRunner(t, st, "mac-1", runneragent.OpAgent)
	id, _, err := st.ds.eng.Trigger(adminCtxForLocalUser(t, st.ds), "p3-wait-end", map[string]any{"gateDir": st.gateDir}, "normal")
	require.NoError(t, err)
	st.awaitFile(t, "child.reached", 30*time.Second)
	owner, ok := p3HomeOwner(st.ds, id)
	require.True(t, ok)
	require.Equal(t, mac1.id, owner)
	require.DirExists(t, mac1.localCopy(id))

	mac1.conn.cut()
	rrAwaitLiveness(t, st.rr(), mac1.id, "lost")
	st.release(t, "child")
	p3RequireCompleted(t, st.ds, id, 30*time.Second)

	copyState := func() string {
		var s string
		if st.ds.db.QueryRow(`SELECT state FROM runner_copies WHERE run_id = ? AND runner_id = ?`, id.String(), mac1.id).Scan(&s) != nil {
			return ""
		}
		return s
	}
	require.Eventually(t, func() bool { return copyState() == "delete_pending" }, 20*time.Second, 50*time.Millisecond)
	assert.DirExists(t, mac1.localCopy(id), "an offline runner cannot have deleted it yet")
	var checkpoints int
	require.NoError(t, st.ds.db.QueryRow(`SELECT COUNT(*) FROM workspace_checkpoints WHERE run_id = ?`, id.String()).Scan(&checkpoints))
	assert.Zero(t, checkpoints, "the server checkpoints are removed with the workspace")

	mac1.conn.allow()
	require.Eventually(t, func() bool { _, err := os.Stat(mac1.localCopy(id)); return os.IsNotExist(err) },
		30*time.Second, 50*time.Millisecond, "SINK: the reconnecting runner deletes its copy")
	mac1.conn.cut()
	mac1.conn.allow()
	require.Eventually(t, func() bool { return copyState() == "deleted" }, 30*time.Second, 50*time.Millisecond,
		"the next hello confirms the deletion")
}

// Design §8 Success: drain completes with progress reported and homes
// released; the drained copy is deleted and the run continues on the target.
func TestPhase3E2E_DrainMovesHomeAndDeletesCopy(t *testing.T) {
	st := bootP3Stack(t, map[string]p3Workflow{
		"p3-wait":       {manifest: p3WaitManifest, files: p3Files(nil)},
		"p3-gate-child": {manifest: p3GateChildManifest},
	}, "")
	mac1 := startWorkspaceRunner(t, st, "mac-1", runneragent.OpAgent)
	id, _, err := st.ds.eng.Trigger(adminCtxForLocalUser(t, st.ds), "p3-wait", map[string]any{"gateDir": st.gateDir}, "normal")
	require.NoError(t, err)
	st.awaitFile(t, "child.reached", 30*time.Second)
	owner, ok := p3HomeOwner(st.ds, id)
	require.True(t, ok)
	require.Equal(t, mac1.id, owner)
	mac2 := startWorkspaceRunner(t, st, "mac-2", runneragent.OpAgent)

	code, body := rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/"+mac1.id+"/drain", nil)
	require.Equal(t, http.StatusOK, code, string(body))

	require.Eventually(t, func() bool {
		r, ok := rrRunner(st.srv, mac1.id)
		if !ok {
			return false
		}
		d, _ := r["drain"].(map[string]any)
		return d != nil && d["complete"] == true
	}, 40*time.Second, 100*time.Millisecond, "runner_get reports the drain complete")
	owner, _ = p3HomeOwner(st.ds, id)
	assert.Equal(t, mac2.id, owner, "SINK: the home moved to the eligible runner")
	require.Eventually(t, func() bool { _, err := os.Stat(mac1.localCopy(id)); return os.IsNotExist(err) },
		20*time.Second, 50*time.Millisecond, "SINK: the drained runner deleted its copy")
	kinds := rrStrings(t, st.ds, `SELECT kind FROM pool_events WHERE runner_id = ? AND kind IN (?, ?)`, mac1.id, runner.EventRunnerDraining, runner.EventRunnerDrained)
	assert.Contains(t, kinds, runner.EventRunnerDraining)
	assert.Contains(t, kinds, runner.EventRunnerDrained)

	st.release(t, "child")
	p3RequireCompleted(t, st.ds, id, 60*time.Second)
	assert.Equal(t, []string{mac2.id}, p3CompletedOn(t, st.ds, id, "two"))
	assert.Contains(t, p3Output(t, st.ds, id, "two"), "step1,step2,|", "the target pulled the moved workspace")
}

// Every RemoveInstanceDir site is hooked: the parent's workspace (engine
// completion cleanup) and the task workspaces (group pruner) take their
// server checkpoints with them. Built-in only.
func TestBuildWorkspaceMobility_HooksEveryRemovalSite(t *testing.T) {
	requireHostGit(t)
	st := bootP3Stack(t, map[string]p3Workflow{"p3-files": {manifest: p3FilesManifest}}, "")
	id, _, err := st.ds.eng.Trigger(adminCtxForLocalUser(t, st.ds), "p3-files", map[string]any{"gateDir": st.gateDir}, "normal")
	require.NoError(t, err)
	st.awaitFile(t, "bf.reached", 30*time.Second)
	run := groupRunOf(t, st.ds, id, "g")
	runs := []string{id.String()}
	for _, task := range run.Tasks {
		runs = append(runs, task.ChildInstanceID.String())
	}
	count := func(runID string) int {
		var n int
		if st.ds.db.QueryRow(`SELECT COUNT(*) FROM workspace_checkpoints WHERE run_id = ?`, runID).Scan(&n) != nil {
			return -1
		}
		return n
	}
	a := run.Tasks[0].ChildInstanceID.String()
	require.Eventually(t, func() bool { return count(id.String()) > 0 && count(a) > 0 }, 20*time.Second, 50*time.Millisecond,
		"built-in workspace steps publish checkpoints for the parent and the finished task")

	st.release(t, "bf")
	p3RequireCompleted(t, st.ds, id, 60*time.Second)
	assert.JSONEq(t, `{"a":"a","b":"b"}`, p3Output(t, st.ds, id, "check"))
	for _, r := range runs {
		require.Eventually(t, func() bool { return count(r) == 0 }, 30*time.Second, 50*time.Millisecond,
			"SINK: run %s's checkpoints are deleted when its workspace is removed", r)
	}
}

func TestBuildWorkspaceMobility_TransferRoutesAreMounted(t *testing.T) {
	st := bootP3Stack(t, nil, "")
	code, body := rrDo(t, http.MethodGet, st.srv.URL+"/api/v1/runners/transfers/manifest", nil)
	assert.Equal(t, http.StatusUnauthorized, code, "mounted and refusing an unauthenticated runner: %s", body)
}

// unstick 2026-11-24: one shared lease repo bound to the process boot ID, exposed for P4 T13.
func TestBuildWorkspaceMobility_OneLeaseRepoBoundToTheBootID(t *testing.T) {
	st := bootP3Stack(t, nil, "")
	m := st.ds.mobility
	require.NotNil(t, m.Leases)
	require.NotEmpty(t, m.BootID)
	ref, err := m.Leases.Claim(context.Background(), "run:"+uuid.NewString(), "group:g1", runner.BuiltInRunnerID)
	require.NoError(t, err)
	var boot string
	require.NoError(t, st.ds.db.QueryRow(`SELECT boot_id FROM workspace_lease_holders WHERE resource_key = ? AND generation = ?`, ref.Key, ref.Generation).Scan(&boot))
	assert.Equal(t, m.BootID, boot, "server-side claims record THIS boot, so a later boot can prove server_restart")
}

const p3BranchesManifest = `{
  "name": "p3-branches",
  "description": "parallel branches on independent homes, gathered into the parent",
  "inputs": [],
  "nodes": [
    {"id": "one", "type": "agent_call", "entrypoint": true, "config": {"model_profile": "default-agent", "templateFile": "templates/one.md"}},
    {"id": "g", "type": "parallel_group",
     "config": {"group": {"form": "branches", "tasks": [{"key": "a", "entry": "a-run"}, {"key": "b", "entry": "b-run"}],
       "completionRule": "all_succeed", "unfinishedBehavior": "cancel_unfinished", "gatherMode": "results_and_files",
       "resolverProfile": "default-agent"}}},
    {"id": "a-run", "type": "agent_call", "config": {"model_profile": "default-agent", "templateFile": "templates/branch-a.md"}},
    {"id": "b-run", "type": "agent_call", "config": {"model_profile": "default-agent", "templateFile": "templates/branch-b.md"}},
    {"id": "two", "type": "agent_call", "config": {"model_profile": "default-agent", "templateFile": "templates/two.md"}},
    {"id": "onFailure", "type": "transform", "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "one", "target": "g"},
    {"source": "g", "target": "a-run", "output": "task", "label": "a"},
    {"source": "g", "target": "b-run", "output": "task", "label": "b"},
    {"source": "g", "target": "two", "output": "success"},
    {"source": "g", "target": "onFailure", "output": "failure"}
  ]
}`

// Both branches wait for the other native launch. With cap one they must run
// on independent homes; the final remote launch must read the gathered bytes.
func TestPhase3E2E_ParallelBranchesPlaceIndependentlyAndGatherSeesAll(t *testing.T) {
	requireHostGit(t)
	st := bootP3Stack(t, map[string]p3Workflow{"p3-branches": {manifest: p3BranchesManifest, files: p3Files(nil)}}, "")
	startWorkspaceRunner(t, st, "mac-1", runneragent.OpAgent)
	startWorkspaceRunner(t, st, "mac-2", runneragent.OpAgent)

	id, _, err := st.ds.eng.Trigger(adminCtxForLocalUser(t, st.ds), "p3-branches", map[string]any{}, "normal")
	require.NoError(t, err)
	p3RequireCompleted(t, st.ds, id, 90*time.Second)

	run := groupRunOf(t, st.ds, id, "g")
	require.Len(t, run.Tasks, 2)
	var onA, onB []string
	for _, task := range run.Tasks {
		switch task.Key {
		case "a":
			onA = p3CompletedOn(t, st.ds, task.ChildInstanceID, "a-run")
		case "b":
			onB = p3CompletedOn(t, st.ds, task.ChildInstanceID, "b-run")
		}
	}
	require.Len(t, onA, 1)
	require.Len(t, onB, 1)
	assert.NotEqual(t, onA[0], onB[0], "SINK: the branches ran on different runners at the same time")
	require.NotNil(t, run.Gather)
	out := p3Output(t, st.ds, id, "two")
	listing := out[strings.Index(out, "|"):]
	assert.Contains(t, listing, "branch-a.txt", "SINK: the gather saw branch a's files")
	assert.Contains(t, listing, "branch-b.txt", "SINK: the gather saw branch b's files")
	assert.Contains(t, out, "step1,step2,", "the parent's own remote work survived the group")
}

func TestPhase3E2E_RevokedKeyReleasesHomesAndRecoversSteps(t *testing.T) {
	st := bootP3Stack(t, map[string]p3Workflow{"p3-hold": {manifest: p3HoldManifest, files: p3Files(nil)}}, "")
	mac1 := startWorkspaceRunner(t, st, "mac-1", runneragent.OpAgent)
	id, _, err := st.ds.eng.Trigger(adminCtxForLocalUser(t, st.ds), "p3-hold", map[string]any{}, "normal")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.ds.eng.Cancel(adminCtxForLocalUser(t, st.ds), id) })
	st.awaitFile(t, "hold.started", 30*time.Second)

	code, body := rrDo(t, http.MethodDelete, st.srv.URL+"/api/v1/runners/keys/"+mac1.keyID, map[string]any{"acknowledge": true})
	require.Equal(t, http.StatusOK, code, string(body))
	rrAwaitLiveness(t, st.rr(), mac1.id, "lost")
	require.Eventually(t, func() bool { o, ok := p3HomeOwner(st.ds, id); return ok && o == "" },
		20*time.Second, 50*time.Millisecond, "SINK: the revoked runner's home is released to the server checkpoint")

	mac2 := startWorkspaceRunner(t, st, "mac-2", runneragent.OpAgent)
	p3RequireCompleted(t, st.ds, id, 60*time.Second)

	assert.Equal(t, []string{mac2.id}, p3CompletedOn(t, st.ds, id, "two"))
	moves := rrStrings(t, st.ds, `SELECT from_runner || '>' || to_runner FROM home_moves WHERE run_id = ? ORDER BY generation`, id.String())
	assert.Contains(t, moves, mac1.id+">", "the release is recorded in the home history")
	assert.Contains(t, moves, ">"+mac2.id)
	out := p3Output(t, st.ds, id, "two")
	assert.Contains(t, out, "step1,step2,|")
	assert.NotContains(t, out, "partial.txt")
	assert.Positive(t, p3NoticeCount(t, st.ds, id, "two"))
}
