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

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Gates live outside copied workspaces. Every process announces admission and
// waits for an explicit release; test overload cannot turn a race into a skip.
type groupProcessGates struct{ dir string }

func newGroupProcessGates(t *testing.T) groupProcessGates {
	t.Helper()
	return groupProcessGates{dir: t.TempDir()}
}

func (g groupProcessGates) release(t *testing.T, key string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(g.dir, key+".release"), nil, 0o600))
}

func (g groupProcessGates) reached(t *testing.T, key string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(g.dir, key+".reached"))
		return err == nil
	}, 20*time.Second, 10*time.Millisecond, "task %s did not reach process gate", key)
}

const groupGateScript = `touch "$1/$2.reached"; n=0; while [ ! -f "$1/$2.release" ]; do n=$((n+1)); [ "$n" -lt 1200 ] || exit 91; sleep .05; done; printf '{"key":"%s"}' "$2"`

// Replace a manifest's sleep with a bounded process gate while retaining its
// actual file writes and result producer. The appended args are test controls,
// not hardcoded substitutes for task outputs.
func gateOrderedManifest(t *testing.T, manifest string, nodeIDs []string, gates groupProcessGates, workflows bool) string {
	t.Helper()
	var wf map[string]any
	require.NoError(t, json.Unmarshal([]byte(manifest), &wf))
	for _, raw := range wf["nodes"].([]any) {
		n := raw.(map[string]any)
		for _, id := range nodeIDs {
			if n["id"] != id {
				continue
			}
			cfg := n["config"].(map[string]any)
			args := cfg["args"].([]any)
			gate := `touch "$4/$2.reached"; n=0; while [ ! -f "$4/$2.release" ]; do n=$((n+1)); [ "$n" -lt 1200 ] || exit 91; sleep .05; done;`
			old := `sleep "$3";`
			if workflows {
				gate = `touch "$3/$1.reached"; n=0; while [ ! -f "$3/$1.release" ]; do n=$((n+1)); [ "$n" -lt 1200 ] || exit 91; sleep .05; done;`
				old = `sleep "$2";`
			}
			script := args[1].(string)
			require.Contains(t, script, old)
			args[1] = strings.Replace(script, old, gate, 1)
			cfg["args"] = append(args, gates.dir)
		}
	}
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	return string(b)
}

func releaseOrderedTasks(t *testing.T, ds *domainStack, parent uuid.UUID, gates groupProcessGates, firstKey, first, second string) {
	t.Helper()
	t.Cleanup(func() { gates.release(t, first); gates.release(t, second) })
	gates.reached(t, first)
	gates.reached(t, second)
	waitForState(t, ds, parent, instance.StateWaiting)
	run := groupRunOf(t, ds, parent, "group-1")
	gates.release(t, first)
	waitForState(t, ds, groupRosterTask(t, run, firstKey).ChildInstanceID, instance.StateCompleted)
	gates.release(t, second)
}

func gatedGroupManifest(t *testing.T, gates groupProcessGates) string {
	t.Helper()
	var wf map[string]any
	require.NoError(t, json.Unmarshal([]byte(groupBranchesManifest), &wf))
	wf["name"] = "grp-gated"
	nodes := wf["nodes"].([]any)
	for _, raw := range nodes {
		n := raw.(map[string]any)
		switch n["id"] {
		case "a-run", "b-run":
			key := strings.TrimSuffix(n["id"].(string), "-run")
			n["config"] = map[string]any{"command": "sh", "args": []string{"-c", groupGateScript, "sh", gates.dir, key}}
		case "group-1":
			cfg := n["config"].(map[string]any)["group"].(map[string]any)
			cfg["completionRule"], cfg["unfinishedBehavior"] = "first_success", "finish_independently"
		case "onSuccess":
			n["config"] = map[string]any{"operations": []any{map[string]any{"variable": "observedWinner", "operation": "set", "value": "{{group-1.winner}}"}}}
		}
	}
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	return string(b)
}

func bootGatedGroup(t *testing.T) (*domainStack, groupProcessGates, uuid.UUID, *instance.GroupRun) {
	t.Helper()
	g := newGroupProcessGates(t)
	ds, _ := bootGroupExecStack(t, 5, map[string]string{"grp-gated": gatedGroupManifest(t, g)})
	// Release before stack shutdown drains the still-held processes.
	t.Cleanup(func() { g.release(t, "a"); g.release(t, "b") })
	id := triggerGroupWorkflow(t, ds, "grp-gated")
	g.reached(t, "a")
	g.reached(t, "b")
	waitForState(t, ds, id, instance.StateWaiting)
	return ds, g, id, groupRunOf(t, ds, id, "group-1")
}

func TestGroupE2E_FirstSuccessUsesCommitOrderWithinOneTimestamp(t *testing.T) {
	t.Parallel()
	ds, _, parent, run := bootGatedGroup(t)
	stamp := "2026-10-16T10:00:00Z"
	// Raw terminal writes use the production table triggers, but deliberately
	// withhold callbacks while the actual task processes remain at their gates.
	for _, key := range []string{"b", "a"} {
		child := groupRosterTask(t, run, key).ChildInstanceID
		_, err := ds.db.Exec(`UPDATE instances SET state='completed', completed_at=?, updated_at=?, variables=? WHERE id=?`, stamp, stamp, `{"source":"`+key+`"}`, child.String())
		require.NoError(t, err)
	}
	stored := groupRunOf(t, ds, parent, "group-1")
	a, b := groupRosterTask(t, stored, "a"), groupRosterTask(t, stored, "b")
	require.NotNil(t, a.TerminalSeq)
	require.NotNil(t, b.TerminalSeq)
	assert.Equal(t, a.TerminalAt, b.TerminalAt)
	assert.Less(t, *b.TerminalSeq, *a.TerminalSeq)
	// Reverse delivery must still choose the earlier committed successful row.
	ds.eng.OnChildTerminal(context.Background(), a.ChildInstanceID)
	ds.eng.OnChildTerminal(context.Background(), b.ChildInstanceID)
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parent))
	assert.Equal(t, "b", parentVariables(t, ds, parent)["observedWinner"])
	out := groupOutputOf(t, ds, parent, "group-1")
	assert.Equal(t, "b", out["winner"])
	assert.Equal(t, "b", resultOf(t, groupTaskByKey(t, out, "b"))["source"])
	assert.Equal(t, 1, nodeExecutionCount(t, ds, parent, "onSuccess"))
}

func TestGroupE2E_CancelledTaskCannotPublishLateSuccess(t *testing.T) {
	t.Parallel()
	ds, _, parent, run := bootGatedGroup(t)
	a, b := groupRosterTask(t, run, "a"), groupRosterTask(t, run, "b")
	ctx := context.Background()
	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), a.ChildInstanceID))
	require.ErrorContains(t, ds.instanceRepo.SetCompleted(ctx, a.ChildInstanceID), "immutable")
	require.NoError(t, ds.instanceRepo.UpdateVariables(ctx, b.ChildInstanceID, map[string]any{"source": "survivor"}))
	require.NoError(t, ds.instanceRepo.SetCompleted(ctx, b.ChildInstanceID))
	ds.eng.OnChildTerminal(ctx, b.ChildInstanceID)
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parent))
	assert.Equal(t, "b", parentVariables(t, ds, parent)["observedWinner"])
	out := groupOutputOf(t, ds, parent, "group-1")
	assert.Equal(t, "cancelled", groupTaskByKey(t, out, "a")["status"])
	assert.Equal(t, "survivor", resultOf(t, groupTaskByKey(t, out, "b"))["source"])
}

func TestGroupE2E_MissingTerminalTriggerCannotFabricateSuccess(t *testing.T) {
	ds, _, parent, run := bootGatedGroup(t)
	var trigger string
	require.NoError(t, ds.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='group_task_terminal_update'`).Scan(&trigger))
	_, err := ds.db.Exec(`DROP TRIGGER group_task_terminal_update`)
	require.NoError(t, err)
	t.Cleanup(func() { _, err := ds.db.Exec(trigger); require.NoError(t, err) })
	a := groupRosterTask(t, run, "a")
	require.NoError(t, ds.instanceRepo.SetCompleted(context.Background(), a.ChildInstanceID))
	ds.eng.OnChildTerminal(context.Background(), a.ChildInstanceID)
	fixtures, ok := ds.groupRunRepo.(*adaptsqlite.GroupRunRepo)
	require.True(t, ok, "the fixture seam lives on the SQLite adapter, not the domain port")
	_, _, err = fixtures.RecordTaskTerminal(context.Background(), run.ID, "a", instance.GroupTaskStatusCompleted, map[string]any{"fake": true}, nil, time.Now())
	require.ErrorContains(t, err, "receipt")
	stored := groupRunOf(t, ds, parent, "group-1")
	assert.Nil(t, stored.Decision)
	assert.Nil(t, groupRosterTask(t, stored, "a").TerminalSeq)
	assert.NotContains(t, parentVariables(t, ds, parent), "observedWinner")
	// Cleanup restores production capture before processes are released. This
	// deliberately corrupt terminal stays unrecoverable rather than relaxing
	// immutability or fabricating its receipt.
}

func TestGroupE2E_TerminalStorageFailureCannotPublishSuccess(t *testing.T) {
	ds, _, parent, run := bootGatedGroup(t)
	_, err := ds.db.Exec(`CREATE TRIGGER reject_group_receipt BEFORE UPDATE OF terminal_seq ON group_tasks BEGIN SELECT RAISE(ABORT, 'receipt storage failed'); END`)
	require.NoError(t, err)
	t.Cleanup(func() { _, err := ds.db.Exec(`DROP TRIGGER reject_group_receipt`); require.NoError(t, err) })
	a := groupRosterTask(t, run, "a")
	require.ErrorContains(t, ds.instanceRepo.SetCompleted(context.Background(), a.ChildInstanceID), "receipt storage failed")
	ds.eng.OnChildTerminal(context.Background(), a.ChildInstanceID)
	child, err := ds.instanceRepo.Get(context.Background(), a.ChildInstanceID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateRunning, child.State)
	assert.Nil(t, groupRunOf(t, ds, parent, "group-1").Decision)
	assert.NotContains(t, parentVariables(t, ds, parent), "observedWinner")
}

func TestGroupE2E_ResultsFormsIsolateLinkedWorktreeMetadata(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not installed on this host")
	}
	for _, form := range []string{"branches", "workflows"} {
		t.Run(form, func(t *testing.T) {
			// The parent creates the bundled sample's src + linked work layout.
			seed := `set -eu; cd "$1"; git init -q src; git -C src config user.name Parent; git -C src config user.email parent@example.com; echo inherited > src/seed.txt; git -C src add .; git -C src commit -qm baseline; git -C src worktree add -qb linked ../work; echo staged > work/staged.txt; git -C work add staged.txt; echo dirty > work/dirty.txt; echo '{}'`
			edit := `set -eu; cd "$1/work"; test "$(cat seed.txt)" = inherited; test "$(git diff --cached --name-only)" = staged.txt; test "$(cat dirty.txt)" = dirty; git config user.name Task; git config user.email task@example.com; echo "$2" > branch.txt; git add branch.txt; git commit -qm "$2"; printf '{"inherited":"%s","own":"%s","history":"%s"}' "$(cat seed.txt)" "$(cat branch.txt)" "$(git log --format=%s | paste -sd , -)"`
			check := `set -eu; cd "$1"; test ! -e work/branch.txt; test "$(git -C src rev-parse linked)" = "$(git -C src rev-parse HEAD)"; printf '{"workHead":"%s","srcHead":"%s","index":"%s","identity":"%s"}' "$(git -C work log -1 --format=%s)" "$(git -C src log -1 --format=%s)" "$(git -C work diff --cached --name-only)" "$(git -C work config user.name)"`
			tool := func(id, script string, args ...string) map[string]any {
				return map[string]any{"id": id, "type": "tool_call", "config": map[string]any{"command": "sh", "args": append([]string{"-c", script, "sh"}, args...)}}
			}
			nodes := []any{tool("seed", seed, "{{workspace}}"), tool("check", check, "{{workspace}}")}
			nodes[0].(map[string]any)["entrypoint"] = true
			manifests := map[string]string{}
			tasks := []any{}
			edges := []any{map[string]any{"source": "seed", "target": "g"}, map[string]any{"source": "g", "target": "check", "output": "success"}, map[string]any{"source": "check", "target": "ok"}, map[string]any{"source": "g", "target": "bad", "output": "failure"}}
			for _, key := range []string{"a", "b"} {
				task := map[string]any{"key": key, "inputs": map[string]any{"label": key}}
				if form == "branches" {
					task["entry"] = key
					nodes = append(nodes, tool(key, edit, "{{workspace}}", "{{label}}"))
					edges = append(edges, map[string]any{"source": "g", "target": key, "output": "task", "label": key})
				} else {
					task["workflow"] = "git-child"
				}
				tasks = append(tasks, task)
			}
			child := tool("edit", edit, "{{workspace}}", "{{label}}")
			child["entrypoint"] = true
			childBytes, err := json.Marshal(map[string]any{"name": "git-child", "inputs": []any{map[string]any{"name": "label", "type": "string", "required": true}}, "nodes": []any{child}, "edges": []any{}})
			require.NoError(t, err)
			manifests["git-child"] = string(childBytes)
			nodes = append(nodes, map[string]any{"id": "g", "type": "parallel_group", "config": map[string]any{"group": map[string]any{"form": form, "tasks": tasks, "completionRule": "all_succeed", "unfinishedBehavior": "wait_for_remaining", "gatherMode": "results"}}})
			for _, key := range []string{"ok", "bad"} {
				nodes = append(nodes, map[string]any{"id": key, "type": "transform", "config": map[string]any{"operations": []any{map[string]any{"variable": "route", "operation": "set", "value": key}}}})
			}
			parentBytes, err := json.Marshal(map[string]any{"name": "git-parent", "nodes": nodes, "edges": edges})
			require.NoError(t, err)
			manifests["git-parent"] = string(parentBytes)
			ds, _ := bootGroupExecStack(t, 5, manifests)
			parent := triggerGroupWorkflow(t, ds, "git-parent")
			require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parent))
			require.Equal(t, "ok", parentVariables(t, ds, parent)["route"])
			out := groupOutputOf(t, ds, parent, "g")
			for _, key := range []string{"a", "b"} {
				result := resultOf(t, groupTaskByKey(t, out, key))
				node := key
				if form == "workflows" {
					node = "edit"
				}
				assert.Equal(t, map[string]any{"inherited": "inherited", "own": key, "history": key + ",baseline"}, result[node], "each fork inherits data and only its own commit")
			}
			// Successful areas are removed by the engine. Inspect their actual
			// bytes/refs/index in the downstream node before cleanup and assert
			// its persisted output, not vanished directories after completion.
			assert.Equal(t, map[string]any{"workHead": "baseline", "srcHead": "baseline", "index": "staged.txt", "identity": "Parent"}, parentVariables(t, ds, parent)["check"])
		})
	}
}
