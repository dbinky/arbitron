package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The gather reconciler is what main.go hands the group sweep (P2-T14).
var _ groupReconciler = (*engine.GroupGatherReconciler)(nil)

// ---------------------------------------------------------------------------
// Manifests. Every task writes real files with `sh` into its own {{workspace}};
// every sink assertion reads the PARENT's working area back through a
// downstream tool_call node whose JSON output lands in the parent's persisted
// variables (the parent's workspace is removed when it completes).
// ---------------------------------------------------------------------------

// filesAllManifest: all_succeed + results_and_files with a plain-directory
// repo hint (src). a adds a.txt and nested/deep.txt after 2s; b adds b.txt and
// deletes the parent's drop.txt at once, so b finishes FIRST while a is
// authored first. check reads the parent's area after the gather.
const filesAllManifest = `{
  "name": "grp-files-all",
  "description": "all_succeed results_and_files",
  "inputs": [],
  "nodes": [
    {"id": "seed", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "mkdir -p \"$1/src\"; printf base > \"$1/base.txt\"; printf drop > \"$1/drop.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "group-1", "type": "parallel_group",
     "config": {"group": {
       "form": "branches",
       "tasks": [
         {"key": "a", "entry": "a-run", "inputs": {"hold": "2"}},
         {"key": "b", "entry": "b-run", "inputs": {"hold": "0"}}
       ],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results_and_files",
       "repoPath": "src",
       "resolverProfile": "resolver"}}},
    {"id": "a-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep \"$2\"; mkdir -p \"$1/nested\"; printf alpha > \"$1/a.txt\"; printf deep > \"$1/nested/deep.txt\"; echo '{}'", "sh", "{{workspace}}", "{{hold}}"]}},
    {"id": "b-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep \"$2\"; printf beta > \"$1/b.txt\"; rm \"$1/drop.txt\"; echo '{}'", "sh", "{{workspace}}", "{{hold}}"]}},
    {"id": "check", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "if [ -e \"$1/drop.txt\" ]; then d=false; else d=true; fi; printf '{\"a\":\"%s\",\"b\":\"%s\",\"deep\":\"%s\",\"base\":\"%s\",\"dropped\":%s}' \"$(cat \"$1/a.txt\")\" \"$(cat \"$1/b.txt\")\" \"$(cat \"$1/nested/deep.txt\")\" \"$(cat \"$1/base.txt\")\" \"$d\"", "sh", "{{workspace}}"]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "seed", "target": "group-1"},
    {"source": "group-1", "target": "a-run", "output": "task", "label": "a"},
    {"source": "group-1", "target": "b-run", "output": "task", "label": "b"},
    {"source": "group-1", "target": "check", "output": "success"},
    {"source": "check", "target": "onSuccess"},
    {"source": "group-1", "target": "onFailure", "output": "failure"}
  ]
}`

// filesConflictManifest: a and b both rewrite the parent's shared.txt line, so
// b's merge (after a's, author order) conflicts; a also adds a.txt.
const filesConflictManifest = `{
  "name": "grp-files-conflict",
  "description": "two branches edit the same line",
  "inputs": [],
  "nodes": [
    {"id": "seed", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "printf 'base\\n' > \"$1/shared.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "group-1", "type": "parallel_group",
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "a", "entry": "a-run"}, {"key": "b", "entry": "b-run"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results_and_files",
       "resolverProfile": "resolver"}}},
    {"id": "a-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf 'from-a\\n' > \"$1/shared.txt\"; printf alpha > \"$1/a.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "b-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf 'from-b\\n' > \"$1/shared.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "check-ok", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf '{\"shared\":\"%s\",\"a\":\"%s\"}' \"$(cat \"$1/shared.txt\")\" \"$(cat \"$1/a.txt\")\"", "sh", "{{workspace}}"]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "check-fail", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "if [ -e \"$1/a.txt\" ]; then a=true; else a=false; fi; printf '{\"shared\":\"%s\",\"aPresent\":%s}' \"$(cat \"$1/shared.txt\")\" \"$a\"", "sh", "{{workspace}}"]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "seed", "target": "group-1"},
    {"source": "group-1", "target": "a-run", "output": "task", "label": "a"},
    {"source": "group-1", "target": "b-run", "output": "task", "label": "b"},
    {"source": "group-1", "target": "check-ok", "output": "success"},
    {"source": "check-ok", "target": "onSuccess"},
    {"source": "group-1", "target": "check-fail", "output": "failure"},
    {"source": "check-fail", "target": "onFailure"}
  ]
}`

// filesFirstManifest: first_success + finish_independently. fast wins at
// once; the test gates slow outside the workspace until the parent sink runs.
const filesFirstManifest = `{
  "name": "grp-files-first",
  "description": "first_success finish_independently results_and_files",
  "inputs": [],
  "nodes": [
    {"id": "group-1", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "fast", "entry": "fast-run"}, {"key": "slow", "entry": "slow-run"}],
       "completionRule": "first_success",
       "unfinishedBehavior": "finish_independently",
       "gatherMode": "results_and_files",
       "resolverProfile": "resolver"}}},
    {"id": "fast-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf win > \"$1/win.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "slow-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep 3; printf late > \"$1/late.txt\"; echo '{\"wrote\":true}'", "sh", "{{workspace}}"]}},
    {"id": "late-check", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "if [ -e \"$1/late.txt\" ]; then l=true; else l=false; fi; printf '{\"win\":\"%s\",\"late\":%s}' \"$(cat \"$1/win.txt\")\" \"$l\"", "sh", "{{workspace}}"]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "group-1", "target": "fast-run", "output": "task", "label": "fast"},
    {"source": "group-1", "target": "slow-run", "output": "task", "label": "slow"},
    {"source": "group-1", "target": "late-check", "output": "success"},
    {"source": "late-check", "target": "onSuccess"},
    {"source": "group-1", "target": "onFailure", "output": "failure"}
  ]
}`

// filesFailedManifest: all_succeed + wait_for_remaining; a succeeds with
// a.txt, b writes b.txt and then fails (halt).
const filesFailedManifest = `{
  "name": "grp-files-failed",
  "description": "failed results_and_files group",
  "inputs": [],
  "nodes": [
    {"id": "seed", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "printf base > \"$1/base.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "group-1", "type": "parallel_group",
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "a", "entry": "a-run"}, {"key": "b", "entry": "b-run"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "wait_for_remaining",
       "gatherMode": "results_and_files",
       "resolverProfile": "resolver"}}},
    {"id": "a-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf alpha > \"$1/a.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "b-run", "type": "tool_call", "errorBehavior": {"type": "halt"},
     "config": {"command": "sh", "args": ["-c", "printf beta > \"$1/b.txt\"; exit 3", "sh", "{{workspace}}"]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "check-fail", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "if [ -e \"$1/a.txt\" ]; then a=true; else a=false; fi; if [ -e \"$1/b.txt\" ]; then b=true; else b=false; fi; printf '{\"aPresent\":%s,\"bPresent\":%s,\"base\":\"%s\"}' \"$a\" \"$b\" \"$(cat \"$1/base.txt\")\"", "sh", "{{workspace}}"]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "seed", "target": "group-1"},
    {"source": "group-1", "target": "a-run", "output": "task", "label": "a"},
    {"source": "group-1", "target": "b-run", "output": "task", "label": "b"},
    {"source": "group-1", "target": "onSuccess", "output": "success"},
    {"source": "group-1", "target": "check-fail", "output": "failure"},
    {"source": "check-fail", "target": "onFailure"}
  ]
}`

// filesNestedGatherManifest: the outer files group's only task t is itself a
// files group (x writes x.txt, y writes y.txt) whose continuations stay in t's
// body; t-mark reads t's OWN area after the inner gather.
const filesNestedGatherManifest = `{
  "name": "grp-files-nested-gather",
  "description": "nested results_and_files groups",
  "inputs": [],
  "nodes": [
    {"id": "outer", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "t", "entry": "inner"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results_and_files",
       "resolverProfile": "resolver"}}},
    {"id": "inner", "type": "parallel_group",
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "x", "entry": "x-run"}, {"key": "y", "entry": "y-run"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results_and_files",
       "resolverProfile": "resolver"}}},
    {"id": "x-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf ex > \"$1/x.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "y-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf why > \"$1/y.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "t-mark", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf '{\"x\":\"%s\",\"y\":\"%s\"}' \"$(cat \"$1/x.txt\")\" \"$(cat \"$1/y.txt\")\"", "sh", "{{workspace}}"]}},
    {"id": "t-bad", "type": "transform",
     "config": {"operations": [{"variable": "inner_branch", "operation": "set", "value": "failure"}]}},
    {"id": "check", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf '{\"x\":\"%s\",\"y\":\"%s\"}' \"$(cat \"$1/x.txt\")\" \"$(cat \"$1/y.txt\")\"", "sh", "{{workspace}}"]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "outer", "target": "inner", "output": "task", "label": "t"},
    {"source": "inner", "target": "x-run", "output": "task", "label": "x"},
    {"source": "inner", "target": "y-run", "output": "task", "label": "y"},
    {"source": "inner", "target": "t-mark", "output": "success"},
    {"source": "inner", "target": "t-bad", "output": "failure"},
    {"source": "outer", "target": "check", "output": "success"},
    {"source": "check", "target": "onSuccess"},
    {"source": "outer", "target": "onFailure", "output": "failure"}
  ]
}`

// filesBadRepoManifest: the repo hint is a template whose runtime value climbs
// out of the parent's working area.
const filesBadRepoManifest = `{
  "name": "grp-files-badrepo",
  "description": "escaping repoPath",
  "inputs": [],
  "nodes": [
    {"id": "prep", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "repo_dir", "operation": "set", "value": "../outside"}]}},
    {"id": "group-1", "type": "parallel_group",
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "only", "entry": "only-set"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results_and_files",
       "repoPath": "{{repo_dir}}",
       "resolverProfile": "resolver"}}},
    {"id": "only-set", "type": "transform",
     "config": {"operations": [{"variable": "only_value", "operation": "set", "value": "x"}]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "prep", "target": "group-1"},
    {"source": "group-1", "target": "only-set", "output": "task", "label": "only"},
    {"source": "group-1", "target": "onSuccess", "output": "success"},
    {"source": "group-1", "target": "onFailure", "output": "failure"}
  ]
}`

// fakeResolverScript stands in for the claude CLI behind the "resolver" agent
// profile: it drains the prompt, changes nothing in the staging area, and
// falsely claims success (a stream-json transcript in the adapter's shape).
// Only the actual index can establish resolution; this must still notify. Its
// result reports the pinned model it ran as (modelUsage), the executed-model
// evidence every pinned Claude mission must carry.
const fakeResolverScript = `#!/bin/sh
case "$1" in
  --version) echo "2.1.330 (Claude Code)"; exit 0 ;;
  auth) echo '{"loggedIn":true}'; exit 0 ;;
esac
cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"fake-resolver"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"All conflicts resolved."}]}}'
printf '%s\n' '{"type":"result","is_error":false,"result":"All conflicts resolved.","session_id":"fake-resolver","usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{"claude-sonnet-4-5":{}}}'
`

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func requireHostGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("file gathering needs a git binary on the host")
	}
}

// writeFilesStack writes the manifests and a local-mode config whose workspace
// root is a SYMLINK (to prove the canonical-root wiring), whose claude binary
// is fakeResolverScript, and which seeds an agent-kind "resolver" profile on a
// fixed model version (config profiles must name one to be seeded).
func writeFilesStack(t *testing.T, manifests map[string]string) groupStackPaths {
	t.Helper()
	requireHostGit(t)
	root := t.TempDir()
	realWS := filepath.Join(root, "workspaces-real")
	require.NoError(t, os.MkdirAll(realWS, 0o700))
	// Failed gathers deliberately retain debugging copies, including read-only
	// directories. Only test teardown relaxes these after the stack stops.
	t.Cleanup(func() {
		_ = filepath.WalkDir(realWS, func(path string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				return os.Chmod(path, 0o700)
			}
			return err
		})
	})
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.Symlink(realWS, workspaceDir))
	for name, manifest := range manifests {
		writeWorkflow(t, workspaceDir, name, manifest)
	}
	fakeClaude := filepath.Join(root, "fake-claude")
	require.NoError(t, os.WriteFile(fakeClaude, []byte(fakeResolverScript), 0o700))

	configPath := filepath.Join(root, "config.yaml")
	cfg := "" +
		"server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: " + yamlQuote(filepath.Join(root, "arbitron.db")) + "\n" +
		"workspace:\n  path: " + yamlQuote(workspaceDir) + "\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: " + yamlQuote(filepath.Join(root, "arbitron.log")) + "\n" +
		"queue:\n  globalcap: 10\n  perusercap: 5\n" +
		"llm:\n  binarypath: " + yamlQuote(fakeClaude) + "\n" +
		"profiles:\n" +
		"  - alias: \"resolver\"\n    kind: \"agent\"\n    model: \"claude-sonnet-4-5\"\n    allowedtools: [\"Read\", \"Edit\", \"Bash\"]\n"
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))
	return groupStackPaths{root: root, workspaceDir: workspaceDir, configPath: configPath}
}

func bootFilesStack(t *testing.T, manifests map[string]string) (*domainStack, groupStackPaths) {
	t.Helper()
	paths := writeFilesStack(t, manifests)
	ds, err := initStartedDomain(context.Background(), paths.configPath)
	require.NoError(t, err, "the composition root must boot with file gathering wired")
	t.Cleanup(func() { ds.Close(context.Background()) })
	return ds, paths
}

func canonicalRoot(t *testing.T, paths groupStackPaths) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(paths.workspaceDir)
	require.NoError(t, err)
	return p
}

func backingDirOf(t *testing.T, paths groupStackPaths, runID uuid.UUID) string {
	t.Helper()
	return filepath.Join(canonicalRoot(t, paths), ".group-backing", runID.String()+".git")
}

func taskWorkspaceDir(t *testing.T, paths groupStackPaths, id uuid.UUID) string {
	t.Helper()
	return filepath.Join(canonicalRoot(t, paths), "instances", id.String())
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// checkOutput is a downstream node's JSON output in the parent's PERSISTED
// variables — what the node read from the parent's working area.
func checkOutput(t *testing.T, ds *domainStack, parentID uuid.UUID, nodeID string) map[string]any {
	t.Helper()
	out, ok := parentVariables(t, ds, parentID)[nodeID].(map[string]any)
	require.True(t, ok, "node %q must have run and recorded its reading of the working area", nodeID)
	return out
}

func waitForRunState(t *testing.T, ds *domainStack, parentID uuid.UUID, nodeID string, want instance.GroupRunState) *instance.GroupRun {
	t.Helper()
	var run *instance.GroupRun
	require.Eventually(t, func() bool {
		r, err := ds.groupRunRepo.GetByParentNode(context.Background(), parentID, nodeID)
		if err != nil || r == nil {
			return false
		}
		run = r
		return r.State == want
	}, 40*time.Second, 150*time.Millisecond, "group run %s/%s never reached %s", parentID, nodeID, want)
	return run
}

// waitForOpenResolution waits until group-1 is resolving conflicts with a
// persisted active session and the parent is suspended.
func waitForOpenResolution(t *testing.T, ds *domainStack, parentID uuid.UUID) *instance.GroupRun {
	t.Helper()
	waitForRunState(t, ds, parentID, "group-1", instance.GroupRunResolvingConflicts)
	waitForState(t, ds, parentID, instance.StateWaiting)
	var run *instance.GroupRun
	require.Eventually(t, func() bool {
		r, err := ds.groupRunRepo.GetByParentNode(context.Background(), parentID, "group-1")
		if err != nil || r == nil || r.Gather == nil || r.Gather.ActiveSession == nil {
			return false
		}
		// The session is recorded before the parent parks for its operator; an
		// EndSession sent in between is refused as not yet waiting.
		parent, err := ds.instanceRepo.Get(context.Background(), parentID)
		if err != nil || parent.State != instance.StateWaiting {
			return false
		}
		run = r
		return true
	}, 20*time.Second, 100*time.Millisecond, "the resolution session must be recorded on the run while the parent waits on it")
	return run
}

func stagingDirOf(t *testing.T, run *instance.GroupRun) string {
	t.Helper()
	require.NotNil(t, run.Gather)
	dir, _ := run.Gather.Handle["stagingDir"].(string)
	require.NotEmpty(t, dir, "the persisted gather handle names the staging area")
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	// No background auto-gc/maintenance (as adapter/git/runner.go): newer git
	// detaches it after a commit and its objects/maintenance.lock races the
	// snapshot, which rightly refuses a locked repository.
	cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0", "GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// resolveConflictInStaging does what an operator (or the resolver agent)
// does in the staging area: edit the conflicted file and stage it.
func resolveConflictInStaging(t *testing.T, run *instance.GroupRun, content string) {
	t.Helper()
	dir := stagingDirOf(t, run)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte(content), 0o644))
	gitIn(t, dir, "add", "--", "shared.txt")
}

// loseSessionEndWake records the session's end on the open attempt exactly as
// GroupResolutionBridge.OnResolutionSessionEnded does, but WITHOUT the wake —
// the state a process death between the two leaves behind.
func loseSessionEndWake(t *testing.T, ds *domainStack, parentID uuid.UUID) {
	t.Helper()
	run := groupRunOf(t, ds, parentID, "group-1")
	require.NotNil(t, run.Gather)
	rep := run.Gather.Clone()
	now := time.Now().UTC()
	for i := range rep.Attempts {
		if rep.Attempts[i].EndedAt == nil {
			rep.Attempts[i].EndedAt = &now
		}
	}
	require.NoError(t, ds.groupRunRepo.SetGatherReport(context.Background(), run.ID, rep))
}

func groupSessionRows(t *testing.T, ds *domainStack, parentID uuid.UUID) []instance.NodeExecution {
	t.Helper()
	execs, err := ds.nodeExecRepo.ListByInstance(context.Background(), parentID)
	require.NoError(t, err)
	var out []instance.NodeExecution
	for _, e := range execs {
		if e.NodeID == "group-1" && e.LLMConversation != nil && e.LLMConversation["kind"] == engine.ConversationKindGroupResolution {
			out = append(out, e)
		}
	}
	return out
}

func hasNotification(notes []notification.Notification, id uuid.UUID, typ notification.Type) bool {
	for _, n := range notes {
		if n.InstanceID == id && n.Type == typ {
			return true
		}
	}
	return false
}

func gatherTaskReport(t *testing.T, run *instance.GroupRun, key string) instance.GatherTaskReport {
	t.Helper()
	require.NotNil(t, run.Gather)
	for _, tr := range run.Gather.Tasks {
		if tr.Key == key {
			return tr
		}
	}
	t.Fatalf("gather report of run %s has no task %q", run.ID, key)
	return instance.GatherTaskReport{}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// HAPPY PATH. Wiring lines 3, 4, 7, 8, 13 are load-bearing here.
func TestGroupFilesE2E_AllSucceedGathersEveryTaskIntoTheParentInAuthorOrder(t *testing.T) {
	t.Parallel()
	ds, paths := bootFilesStack(t, map[string]string{"grp-files-all": filesAllManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-files-all")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID),
		"a failed parent here means the file area, the executor's gatherer or the launcher preflight is not wired")
	assert.Equal(t, "success", branchVariable(t, ds, parentID), "a missing finalizer routes failure (no finalized refs)")

	check := checkOutput(t, ds, parentID, "check")
	assert.Equal(t, "alpha", check["a"])
	assert.Equal(t, "beta", check["b"])
	assert.Equal(t, "deep", check["deep"], "new files outside the repo hint are gathered too")
	assert.Equal(t, "base", check["base"], "the parent's own files survive the gather")
	assert.Equal(t, true, check["dropped"], "a task's deletion is gathered")

	run := groupRunOf(t, ds, parentID, "group-1")
	require.NotNil(t, run.Gather, "the merge report is persisted")
	assert.Equal(t, "results_and_files", run.Gather.Mode)
	assert.Equal(t, "applied", run.Gather.State)
	require.Len(t, run.Gather.Tasks, 2)
	assert.Equal(t, "a", run.Gather.Tasks[0].Key, "the report lists tasks in author order")
	assert.True(t, run.Gather.Tasks[0].Merged)
	assert.True(t, run.Gather.Tasks[1].Merged)
	assert.Equal(t, "src", run.Config["resolvedRepoPath"], "the launcher resolved and recorded the repo hint")
	a, b := groupRosterTask(t, run, "a"), groupRosterTask(t, run, "b")
	require.NotNil(t, a.TerminalSeq)
	require.NotNil(t, b.TerminalSeq)
	assert.Less(t, *b.TerminalSeq, *a.TerminalSeq, "b finished first, yet a was merged first")
	assert.NotEmpty(t, a.FinalizedRef, "each task's files were finalized before it completed")

	out := groupOutputOf(t, ds, parentID, "group-1")
	gather, ok := out["gather"].(map[string]any)
	require.True(t, ok, "the frozen group output downstream nodes read carries the merge report")
	assert.Equal(t, "applied", gather["state"])

	require.Eventually(t, func() bool {
		return !pathExists(backingDirOf(t, paths, run.ID)) &&
			!pathExists(taskWorkspaceDir(t, paths, a.ChildInstanceID)) &&
			!pathExists(taskWorkspaceDir(t, paths, b.ChildInstanceID))
	}, 20*time.Second, 100*time.Millisecond,
		"the backing snapshot and the retained task worktrees are pruned once every writer stopped (well before the 60s sweep)")
}

func TestGroupFilesE2E_FirstSuccessMergesOnlyTheWinnerAndALateLoserNeverReachesTheParent(t *testing.T) {
	t.Parallel()
	gates := newGroupProcessGates(t)
	var wf map[string]any
	require.NoError(t, json.Unmarshal([]byte(filesFirstManifest), &wf))
	nodes := wf["nodes"].([]any)
	for _, raw := range nodes {
		n := raw.(map[string]any)
		if n["id"] == "late-check" {
			nodes = append(nodes, map[string]any{"id": "final-check", "type": "tool_call", "config": n["config"]})
			break
		}
	}
	edges := wf["edges"].([]any)
	for _, raw := range edges {
		e := raw.(map[string]any)
		if e["source"] == "late-check" {
			e["target"] = "final-check"
		}
	}
	wf["nodes"], wf["edges"] = nodes, append(edges, map[string]any{"source": "final-check", "target": "onSuccess"})
	data, err := json.Marshal(wf)
	require.NoError(t, err)
	manifest := gateBehaviorManifest(t, string(data), gates, "fast-run", "slow-run", "final-check")
	ds, paths := bootFilesStack(t, map[string]string{"grp-files-first": manifest})
	t.Cleanup(func() {
		gates.release(t, "fast-run")
		gates.release(t, "slow-run")
		gates.release(t, "final-check")
	})
	ctx := context.Background()

	parentID := triggerGroupWorkflow(t, ds, "grp-files-first")
	gates.reached(t, "slow-run")
	gates.reached(t, "fast-run")
	gates.release(t, "fast-run")
	gates.reached(t, "final-check")
	assert.Equal(t, "win", checkOutput(t, ds, parentID, "late-check")["win"], "the parent sink ran before the loser is released")

	run := groupRunOf(t, ds, parentID, "group-1")
	require.NotNil(t, run.Decision)
	assert.Equal(t, "fast", run.Decision.WinnerKey)
	slowTask := groupRosterTask(t, run, "slow")
	assert.True(t, slowTask.ContinuingIndependently)
	assert.Empty(t, slowTask.FinalizedRef, "a task finishing after the decision is never finalized")
	assert.False(t, stateOf(t, ds, slowTask.ChildInstanceID).IsTerminal(), "the loser is still gated after the parent sink")
	// RETENTION: the group is decided and gathered, but an independent writer is live, so the pruner
	// (Watch on group.completed, plus the engine's retention) must keep its snapshot and workspaces.
	require.Never(t, func() bool {
		return !pathExists(backingDirOf(t, paths, run.ID)) ||
			!pathExists(taskWorkspaceDir(t, paths, slowTask.ChildInstanceID))
	}, 500*time.Millisecond, 50*time.Millisecond,
		"the backing snapshot and the live loser's workspace survive while an independent task still writes")
	gates.release(t, "slow-run")
	waitForState(t, ds, slowTask.ChildInstanceID, instance.StateCompleted)
	slow, err := ds.instanceRepo.Get(ctx, slowTask.ChildInstanceID)
	require.NoError(t, err)
	wrote, _ := slow.Variables["slow-run"].(map[string]any)
	assert.Equal(t, true, wrote["wrote"], "the loser really wrote late.txt in its own working area")

	assert.Empty(t, groupRosterTask(t, groupRunOf(t, ds, parentID, "group-1"), "slow").FinalizedRef)
	assert.False(t, pathExists(filepath.Join(taskWorkspaceDir(t, paths, parentID), "late.txt")))
	gates.release(t, "final-check")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))
	check := checkOutput(t, ds, parentID, "final-check")
	assert.Equal(t, "win", check["win"], "the winner's changes were gathered")
	assert.Equal(t, false, check["late"], "SINK: the independent loser's late write never reached the parent's working area")
	assert.True(t, gatherTaskReport(t, run, "fast").Merged)
	slowRep := gatherTaskReport(t, run, "slow")
	assert.False(t, slowRep.Merged)
	assert.Contains(t, slowRep.Reason, "not the winner")

	require.Eventually(t, func() bool { return !pathExists(backingDirOf(t, paths, run.ID)) },
		20*time.Second, 100*time.Millisecond, "the snapshot is released once the loser stopped writing")
	require.Eventually(t, func() bool { return !pathExists(taskWorkspaceDir(t, paths, slowTask.ChildInstanceID)) },
		20*time.Second, 100*time.Millisecond, "the loser's workspace is pruned once it stopped writing")
}

func TestGroupFilesE2E_FailedGroupMergesNothingAndRetainsDebuggingContent(t *testing.T) {
	t.Parallel()
	ds, paths := bootFilesStack(t, map[string]string{"grp-files-failed": filesFailedManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-files-failed")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "failure", branchVariable(t, ds, parentID))

	check := checkOutput(t, ds, parentID, "check-fail")
	assert.Equal(t, false, check["aPresent"], "a succeeded, but a failed group merges no partial work")
	assert.Equal(t, false, check["bPresent"])
	assert.Equal(t, "base", check["base"])

	run := groupRunOf(t, ds, parentID, "group-1")
	require.NotNil(t, run.Decision)
	assert.Equal(t, "failure", run.Decision.Outcome)
	require.NotNil(t, run.Gather)
	assert.Equal(t, "not_applicable", run.Gather.State)
	for _, tr := range run.Gather.Tasks {
		assert.False(t, tr.Merged)
		assert.Equal(t, "group failed: no tasks merged", tr.Reason)
	}

	a, b := groupRosterTask(t, run, "a"), groupRosterTask(t, run, "b")
	require.Eventually(t, func() bool { return !pathExists(backingDirOf(t, paths, run.ID)) },
		20*time.Second, 100*time.Millisecond, "the backing snapshot is still released")
	got, err := os.ReadFile(filepath.Join(taskWorkspaceDir(t, paths, a.ChildInstanceID), "a.txt"))
	require.NoError(t, err, "the completed task's workspace is retained for debugging")
	assert.Equal(t, "alpha", string(got))
	assert.True(t, pathExists(taskWorkspaceDir(t, paths, b.ChildInstanceID)), "the failed task's workspace is retained by the engine")
}

// Recreating the vanished task area at finalization used to publish an empty
// tree as a successful candidate, allowing a gather to delete the parent's files.
func TestGroupFilesE2E_VanishedTaskWorkspaceFailsWithoutDeletingParentFiles(t *testing.T) {
	t.Parallel()
	var manifest map[string]any
	require.NoError(t, json.Unmarshal([]byte(filesFailedManifest), &manifest))
	var checkArgs any
	for _, raw := range manifest["nodes"].([]any) {
		node := raw.(map[string]any)
		var script string
		switch node["id"] {
		case "a-run":
			script = `rm -rf "$1"; echo '{}'`
		case "b-run":
			script = `printf beta > "$1/b.txt"; echo '{}'`
		case "check-fail":
			checkArgs = node["config"].(map[string]any)["args"]
			continue
		default:
			continue
		}
		node["config"].(map[string]any)["args"] = []string{"-c", script, "sh", "{{workspace}}"}
	}
	// Read the parent's area on the success route too, so an empty-tree ref that
	// is wrongly gathered is caught at the sink (base.txt deleted), not only by
	// the route assertion.
	manifest["nodes"] = append(manifest["nodes"].([]any), map[string]any{
		"id": "check-success", "type": "tool_call",
		"config": map[string]any{"command": "sh", "args": checkArgs},
	})
	for _, raw := range manifest["edges"].([]any) {
		edge := raw.(map[string]any)
		if edge["source"] == "group-1" && edge["output"] == "success" {
			edge["target"] = "check-success"
		}
	}
	manifest["edges"] = append(manifest["edges"].([]any), map[string]any{"source": "check-success", "target": "onSuccess"})
	raw, err := json.Marshal(manifest)
	require.NoError(t, err)
	ds, paths := bootFilesStack(t, map[string]string{"grp-files-failed": string(raw)})
	parentID := triggerGroupWorkflow(t, ds, "grp-files-failed")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "failure", branchVariable(t, ds, parentID))
	vars := parentVariables(t, ds, parentID)
	for _, node := range []string{"check-fail", "check-success"} {
		if check, ok := vars[node].(map[string]any); ok {
			assert.Equal(t, "base", check["base"], "%s: the parent's original bytes survive whichever route ran", node)
		}
	}

	run := groupRunOf(t, ds, parentID, "group-1")
	a := groupRosterTask(t, run, "a")
	child, err := ds.instanceRepo.Get(context.Background(), a.ChildInstanceID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateFailed, child.State)
	assert.Empty(t, a.FinalizedRef, "missing files must never become a successful empty-tree candidate")
	assert.NoDirExists(t, taskWorkspaceDir(t, paths, a.ChildInstanceID), "finalization must not recreate a vanished workspace")
	check := checkOutput(t, ds, parentID, "check-fail")
	assert.Equal(t, "base", check["base"], "the parent's original bytes survive")
	assert.Equal(t, false, check["bPresent"], "no partial work is gathered")
}

// Wiring lines 1 and 6 are load-bearing here.
func TestGroupFilesE2E_ConflictOpensAPersistedSessionAndWaitsUntilResolved(t *testing.T) {
	t.Parallel()
	ds, _ := bootFilesStack(t, map[string]string{"grp-files-conflict": filesConflictManifest})
	ctx := context.Background()

	parentID := triggerGroupWorkflow(t, ds, "grp-files-conflict")
	run := waitForOpenResolution(t, ds, parentID)

	require.Len(t, run.Gather.Attempts, 1)
	att := run.Gather.Attempts[0]
	assert.Equal(t, "b", att.TaskKey, "a merged cleanly first (author order); b conflicts")
	assert.Equal(t, []string{"shared.txt"}, att.ConflictPaths)
	assert.Nil(t, att.EndedAt)

	rows := groupSessionRows(t, ds, parentID)
	require.Len(t, rows, 1, "the session is persisted on the parent at the group node")
	assert.True(t, engine.IsOpenGroupResolutionSession(rows[0].LLMConversation))
	assert.Equal(t, run.ID.String(), rows[0].LLMConversation["groupRunId"])

	notes, err := ds.notifRepo.List(ctx, notification.ListFilters{Limit: 200})
	require.NoError(t, err)
	assert.True(t, hasNotification(notes, parentID, notification.TypeConflictResolution), "an operator is told a human decision is needed")

	assert.Never(t, func() bool {
		inst, gerr := ds.instanceRepo.Get(ctx, parentID)
		return gerr != nil || inst.State != instance.StateWaiting
	}, 3*time.Second, 200*time.Millisecond, "downstream work never runs while the conflict is open")
	_, ran := parentVariables(t, ds, parentID)["check-ok"]
	assert.False(t, ran)

	resolveConflictInStaging(t, run, "resolved\n")
	_, err = ds.conversationHandler.EndSession(adminCtxForLocalUser(t, ds), parentID)
	require.NoError(t, err, "ending the session means 'finish and verify'")

	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))
	check := checkOutput(t, ds, parentID, "check-ok")
	assert.Equal(t, "resolved", check["shared"], "SINK: the resolved content reached the parent's working area")
	assert.Equal(t, "alpha", check["a"], "a's clean merge is kept")
	final := groupRunOf(t, ds, parentID, "group-1")
	assert.Equal(t, "applied", final.Gather.State)
	assert.Equal(t, "resolved", final.Gather.Attempts[0].Outcome)
	assert.Contains(t, gatherTaskReport(t, final, "b").Reason, "after conflict resolution")
}

// ORDER ON REAL GIT. The overlapping edit conflicts on whichever task is merged
// SECOND, so the conflicting task names the merge order. a is delayed past b's
// terminal, so completion order is b, a; author order (a, b) must still decide
// the merge. Were the gatherer to merge in terminal order, a would conflict.
func TestGroupFilesE2E_OverlappingEditsMergeInAuthorOrderEvenWhenTheLaterTaskFinishesFirst(t *testing.T) {
	t.Parallel()
	slowA := strings.Replace(filesConflictManifest,
		`printf 'from-a\\n' > \"$1/shared.txt\"`, `sleep 2; printf 'from-a\\n' > \"$1/shared.txt\"`, 1)
	require.NotEqual(t, filesConflictManifest, slowA, "the a-run command must have been delayed")
	ds, _ := bootFilesStack(t, map[string]string{"grp-files-conflict": slowA})

	parentID := triggerGroupWorkflow(t, ds, "grp-files-conflict")
	run := waitForOpenResolution(t, ds, parentID)

	a, b := groupRosterTask(t, run, "a"), groupRosterTask(t, run, "b")
	require.NotNil(t, a.TerminalSeq)
	require.NotNil(t, b.TerminalSeq)
	require.Less(t, *b.TerminalSeq, *a.TerminalSeq, "precondition: b finished before a")

	require.Len(t, run.Gather.Attempts, 1)
	assert.Equal(t, "b", run.Gather.Attempts[0].TaskKey, "a was merged first (author order), so b is the task that conflicts")
	assert.Equal(t, []string{"shared.txt"}, run.Gather.Attempts[0].ConflictPaths)
	assert.True(t, gatherTaskReport(t, run, "a").Merged, "a merged cleanly before b's conflict")
	assert.False(t, gatherTaskReport(t, run, "b").Merged)
}

// Named by P3-T9. Wiring line 6 is load-bearing here.
func TestGroupFilesE2E_ConflictAbandon_RoutesFailureAndRestoresParentFiles(t *testing.T) {
	t.Parallel()
	ds, _ := bootFilesStack(t, map[string]string{"grp-files-conflict": filesConflictManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-files-conflict")
	waitForOpenResolution(t, ds, parentID)
	require.NoError(t, ds.conversationHandler.AbandonSession(adminCtxForLocalUser(t, ds), parentID))

	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "failure", branchVariable(t, ds, parentID))
	check := checkOutput(t, ds, parentID, "check-fail")
	assert.Equal(t, "base", check["shared"], "SINK: the parent's file is exactly as before the gather")
	assert.Equal(t, false, check["aPresent"], "not even a's clean merge was applied")

	final := groupRunOf(t, ds, parentID, "group-1")
	require.NotNil(t, final.Decision)
	assert.Equal(t, "success", final.Decision.Outcome, "the task decision is never rewritten")
	assert.Equal(t, "failure", final.ContinuationPort)
	assert.Equal(t, "rolled_back", final.Gather.State)
	assert.Equal(t, "rolled_back", final.Gather.RollbackStatus)
	assert.Equal(t, "abandoned", final.Gather.Attempts[0].Outcome)
	rows := groupSessionRows(t, ds, parentID)
	require.Len(t, rows, 1)
	assert.Equal(t, "abandoned", rows[0].LLMConversation["endReason"])
}

// Wiring line 13 (Watch) is load-bearing here: the sweep is 60s away.
func TestGroupFilesE2E_ParentCancelledMidSession_AbortsAndStaysCancelled(t *testing.T) {
	t.Parallel()
	ds, _ := bootFilesStack(t, map[string]string{"grp-files-conflict": filesConflictManifest})
	ctx := context.Background()

	parentID := triggerGroupWorkflow(t, ds, "grp-files-conflict")
	run := waitForOpenResolution(t, ds, parentID)
	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), parentID))

	require.Eventually(t, func() bool {
		r, err := ds.groupRunRepo.Get(ctx, run.ID)
		return err == nil && r.State == instance.GroupRunDone
	}, 20*time.Second, 100*time.Millisecond, "the cancellation aborts the gather at once, not at the next sweep")
	final := groupRunOf(t, ds, parentID, "group-1")
	assert.Equal(t, "failure", final.ContinuationPort)
	assert.Equal(t, "cancelled", final.Gather.Attempts[0].Outcome)
	assert.Nil(t, final.Gather.ActiveSession)
	assert.Equal(t, "rolled_back", final.Gather.RollbackStatus)
	rows := groupSessionRows(t, ds, parentID)
	require.Len(t, rows, 1)
	assert.Equal(t, "cancelled", rows[0].LLMConversation["endReason"])

	assert.Never(t, func() bool {
		inst, err := ds.instanceRepo.Get(ctx, parentID)
		return err != nil || inst.State != instance.StateCancelled
	}, 3*time.Second, 200*time.Millisecond, "the parent stays cancelled")
	assert.Nil(t, branchVariable(t, ds, parentID), "neither continuation port ran")
}

func TestGroupFilesE2E_NestedGroupGathersIntoItsImmediateParent(t *testing.T) {
	t.Parallel()
	ds, paths := bootFilesStack(t, map[string]string{"grp-files-nested-gather": filesNestedGatherManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-files-nested-gather")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))
	check := checkOutput(t, ds, parentID, "check")
	assert.Equal(t, "ex", check["x"])
	assert.Equal(t, "why", check["y"])

	outer := groupRunOf(t, ds, parentID, "outer")
	require.NotNil(t, outer.Gather)
	assert.Equal(t, "applied", outer.Gather.State)
	tTask := groupRosterTask(t, outer, "t")
	inner := groupRunOf(t, ds, tTask.ChildInstanceID, "inner")
	assert.Equal(t, tTask.ChildInstanceID, inner.ParentInstanceID, "the nested group's parent is the enclosing task, not the root")
	require.NotNil(t, inner.Gather)
	assert.Equal(t, "applied", inner.Gather.State)
	mark, ok := parentVariables(t, ds, tTask.ChildInstanceID)["t-mark"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ex", mark["x"], "the nested gather wrote into the task's own working area first")
	assert.Equal(t, "why", mark["y"])

	require.Eventually(t, func() bool {
		return !pathExists(backingDirOf(t, paths, outer.ID)) && !pathExists(backingDirOf(t, paths, inner.ID))
	}, 20*time.Second, 100*time.Millisecond, "both levels release their snapshots")
}

func TestGroupFilesE2E_ResultsOnlyModeCopiesStartingFilesWithoutMerging(t *testing.T) {
	t.Parallel()
	ds, paths := bootFilesStack(t, map[string]string{"grp-branches": groupBranchesManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-branches")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))
	check := checkOutput(t, ds, parentID, "check-parent")
	assert.Equal(t, false, check["leaked"], "no task file is merged in results mode")
	assert.Equal(t, true, check["seedKept"])

	out := groupOutputOf(t, ds, parentID, "group-1")
	aRun, ok := resultOf(t, groupTaskByKey(t, out, "a"))["a-run"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "parent-seed", aRun["seed"], "the task started from a copy of the parent's files")
	gather, ok := out["gather"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "results", gather["mode"])
	run := groupRunOf(t, ds, parentID, "group-1")
	assert.Nil(t, run.Gather)
	assert.False(t, pathExists(backingDirOf(t, paths, run.ID)), "results-only groups never create a git backing store")
}

// Wiring line 11 (startup gather reconcile) is load-bearing here.
func TestGroupFilesE2E_RestartRestoresAnInterruptedResolution(t *testing.T) {
	t.Parallel()
	paths := writeFilesStack(t, map[string]string{"grp-files-conflict": filesConflictManifest})
	gates := newGroupProcessGates(t)
	resolver := strings.Replace(fakeResolverScript, "cat >/dev/null", "cat >/dev/null\n"+strings.ReplaceAll(groupGateScript, `printf '{"key":"%s"}' "$2"`, ":"), 1)
	resolver = strings.Replace(resolver, "#!/bin/sh", "#!/bin/sh\ncase \"$1\" in --version) echo '2.1.330 (Claude Code)'; exit 0 ;; auth) echo '{\"loggedIn\":true}'; exit 0 ;; esac\nset -- '"+gates.dir+"' resolver", 1)
	require.NoError(t, os.WriteFile(filepath.Join(paths.root, "fake-claude"), []byte(resolver), 0o700))
	ctx := context.Background()

	ds1, err := initStartedDomain(ctx, paths.configPath)
	require.NoError(t, err)
	closed := false
	t.Cleanup(func() {
		gates.release(t, "resolver")
		if !closed {
			ds1.Close(context.Background())
		}
	})
	parentID := triggerGroupWorkflow(t, ds1, "grp-files-conflict")
	gates.reached(t, "resolver")
	gates.release(t, "resolver")
	run := waitForOpenResolution(t, ds1, parentID)
	resolveConflictInStaging(t, run, "resolved\n")
	loseSessionEndWake(t, ds1, parentID)
	ds1.Close(ctx) // Stop producers before reboot; waiting state and gather remain durable.
	closed = true

	ds2, err := initStartedDomain(ctx, paths.configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds2.Close(context.Background()) })

	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds2, parentID),
		"the startup gather reconcile wakes the parent whose wake was lost (the sweep is 60s away)")
	assert.Equal(t, "success", branchVariable(t, ds2, parentID))
	assert.Equal(t, "resolved", checkOutput(t, ds2, parentID, "check-ok")["shared"])
	rows := groupSessionRows(t, ds2, parentID)
	require.Len(t, rows, 1)
	assert.False(t, engine.IsOpenGroupResolutionSession(rows[0].LLMConversation), "the interrupted session's record is closed")
}

// Wiring line 12 (gather sweep goroutine) is load-bearing here.
func TestGroupFilesE2E_SweepWakesAParentWhoseSessionEndWasLost(t *testing.T) {
	prev := groupSweepInterval
	groupSweepInterval = time.Second
	t.Cleanup(func() { groupSweepInterval = prev })

	ds, _ := bootFilesStack(t, map[string]string{"grp-files-conflict": filesConflictManifest})
	ctx := context.Background()
	parentID := triggerGroupWorkflow(t, ds, "grp-files-conflict")
	run := waitForOpenResolution(t, ds, parentID)
	resolveConflictInStaging(t, run, "resolved\n")
	loseSessionEndWake(t, ds, parentID)

	require.Eventually(t, func() bool {
		inst, err := ds.instanceRepo.Get(ctx, parentID)
		return err == nil && inst.State == instance.StateCompleted
	}, 20*time.Second, 150*time.Millisecond, "only the periodic gather reconcile can wake this parent")
	assert.Equal(t, "resolved", checkOutput(t, ds, parentID, "check-ok")["shared"])
}

func TestGroupFilesE2E_InvalidRepoPathFailsBeforeAnyTaskStarts(t *testing.T) {
	ds, _ := bootFilesStack(t, map[string]string{"grp-files-badrepo": filesBadRepoManifest})
	ctx := context.Background()

	parentID := triggerGroupWorkflow(t, ds, "grp-files-badrepo")
	require.Equal(t, instance.StateFailed, waitForTerminalState(t, ds, parentID))
	run, err := ds.groupRunRepo.GetByParentNode(ctx, parentID, "group-1")
	require.NoError(t, err)
	assert.Nil(t, run, "nothing is persisted for a group that could not be prepared")
	assert.Empty(t, childrenOf(t, ds, parentID), "no task was started")

	execs, err := ds.nodeExecRepo.ListByInstance(ctx, parentID)
	require.NoError(t, err)
	var msg string
	for _, e := range execs {
		if e.NodeID == "group-1" && e.Status == instance.NodeStatusFailed {
			msg = fmt.Sprint(e.Error["error"])
		}
	}
	assert.Contains(t, msg, "parallel_group could not be prepared")
	assert.Contains(t, msg, "repoPath", "the author is told which setting to fix")
}

// filesGrandchildManifest: one results_and_files branch task writes t.txt and
// then spawns a fire-and-forget grandchild (grp-files-leaf) that the test holds
// at a process gate. The task completes — and the group gathers — while the
// grandchild is still alive below the terminal task.
const filesGrandchildManifest = `{
  "name": "grp-files-grandchild",
  "description": "files task with a live fire-and-forget grandchild",
  "inputs": [],
  "nodes": [
    {"id": "group-1", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "t", "entry": "t-run"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "wait_for_remaining",
       "gatherMode": "results_and_files",
       "resolverProfile": "resolver"}}},
    {"id": "t-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf task > \"$1/t.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "spawn-leaf", "type": "spawn_workflow",
     "config": {"childWorkflow": "grp-files-leaf", "spawnMode": "fire_and_forget"}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "group-1", "target": "t-run", "output": "task", "label": "t"},
    {"source": "t-run", "target": "spawn-leaf"},
    {"source": "group-1", "target": "onSuccess", "output": "success"},
    {"source": "group-1", "target": "onFailure", "output": "failure"}
  ]
}`

const filesLeafManifestFmt = `{
  "name": "grp-files-leaf",
  "description": "gated grandchild",
  "inputs": [],
  "nodes": [
    {"id": "leaf-hold", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", %q, "sh", %q, "leaf"]}}
  ],
  "edges": []
}`

// Area 5 wiring check 6: a REAL grandchild below a completed files task pins
// the task's workspace, finalized ref and the group's backing snapshot (the
// descendant lookup is wired through SetSpawnLinks into the pruner); once the
// grandchild terminates, only the gather sweep (the grandchild is not a group
// task, so no Watch event names the run) releases them.
func TestGroupFilesE2E_GrandchildBelowCompletedTaskPinsResourcesUntilTheSweepReleasesThem(t *testing.T) {
	prev := groupSweepInterval
	groupSweepInterval = 500 * time.Millisecond
	t.Cleanup(func() { groupSweepInterval = prev })

	gates := newGroupProcessGates(t)
	leaf := fmt.Sprintf(filesLeafManifestFmt, groupGateScript, gates.dir)
	ds, paths := bootFilesStack(t, map[string]string{
		"grp-files-grandchild": filesGrandchildManifest,
		"grp-files-leaf":       leaf,
	})
	t.Cleanup(func() { gates.release(t, "leaf") })

	parentID := triggerGroupWorkflow(t, ds, "grp-files-grandchild")
	gates.reached(t, "leaf")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	run := groupRunOf(t, ds, parentID, "group-1")
	require.Equal(t, instance.GroupRunDone, run.State)
	task := groupRosterTask(t, run, "t")
	assert.NotEmpty(t, task.FinalizedRef, "the completed files task was finalized into a ref before completing")
	assert.Equal(t, instance.StateCompleted, stateOf(t, ds, task.ChildInstanceID))

	kids := childrenOf(t, ds, task.ChildInstanceID)
	require.Len(t, kids, 1, "the grandchild really hangs below the completed task")
	assert.False(t, kids[0].State.IsTerminal(), "the grandchild is still live at its gate")

	taskDir := taskWorkspaceDir(t, paths, task.ChildInstanceID)
	backing := backingDirOf(t, paths, run.ID)
	require.True(t, pathExists(backing))
	// Several sweep ticks and the group.completed Watch event have all run.
	require.Never(t, func() bool { return !pathExists(backing) || !pathExists(taskDir) },
		2*time.Second, 50*time.Millisecond,
		"a live grandchild pins the completed task's workspace and the backing snapshot")

	gates.release(t, "leaf")
	waitForState(t, ds, kids[0].ID, instance.StateCompleted)
	require.Eventually(t, func() bool { return !pathExists(backing) && !pathExists(taskDir) },
		20*time.Second, 100*time.Millisecond,
		"once the grandchild stopped, the sweep releases the task workspace and the snapshot")
}

// filesLateGrandchildManifestFmt: first_success + finish_independently. "fast"
// wins at once; "slow" is held at a gate, then completes AFTER the group is done
// and spawns a gated grandchild. With the gather over, the engine's own
// descendant check (fed by SetSpawnLinks) is the only thing keeping slow's
// workspace once it turns terminal.
const filesLateGrandchildManifestFmt = `{
  "name": "grp-files-late-grandchild",
  "description": "independent loser completes after the decision, with a live grandchild",
  "inputs": [],
  "nodes": [
    {"id": "group-1", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "fast", "entry": "fast-run"}, {"key": "slow", "entry": "slow-run"}],
       "completionRule": "first_success",
       "unfinishedBehavior": "finish_independently",
       "gatherMode": "results_and_files",
       "resolverProfile": "resolver"}}},
    {"id": "fast-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf win > \"$1/win.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "slow-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", %[1]q, "sh", %[2]q, "slow"]}},
    {"id": "spawn-leaf", "type": "spawn_workflow",
     "config": {"childWorkflow": "grp-files-leaf", "spawnMode": "fire_and_forget"}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "group-1", "target": "fast-run", "output": "task", "label": "fast"},
    {"source": "group-1", "target": "slow-run", "output": "task", "label": "slow"},
    {"source": "slow-run", "target": "spawn-leaf"},
    {"source": "group-1", "target": "onSuccess", "output": "success"},
    {"source": "group-1", "target": "onFailure", "output": "failure"}
  ]
}`

// Area 5 wiring check 4: SetSpawnLinks feeds Engine.hasActiveDescendants, so a
// task that turns terminal after the group finished keeps its workspace while a
// spawned descendant lives, and the sweep frees it once nothing is live.
func TestGroupFilesE2E_LateTaskWithLiveGrandchildKeepsItsWorkspaceUntilTheSweepReleasesIt(t *testing.T) {
	prev := groupSweepInterval
	groupSweepInterval = 500 * time.Millisecond
	t.Cleanup(func() { groupSweepInterval = prev })

	gates := newGroupProcessGates(t)
	leaf := fmt.Sprintf(filesLeafManifestFmt, groupGateScript, gates.dir)
	late := fmt.Sprintf(filesLateGrandchildManifestFmt, groupGateScript, gates.dir)
	ds, paths := bootFilesStack(t, map[string]string{"grp-files-late-grandchild": late, "grp-files-leaf": leaf})
	t.Cleanup(func() { gates.release(t, "slow"); gates.release(t, "leaf") })

	parentID := triggerGroupWorkflow(t, ds, "grp-files-late-grandchild")
	gates.reached(t, "slow")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	run := groupRunOf(t, ds, parentID, "group-1")
	require.NotNil(t, run.Decision)
	slow := groupRosterTask(t, run, "slow")
	assert.False(t, stateOf(t, ds, slow.ChildInstanceID).IsTerminal(), "slow is still at its gate after the group finished")

	taskDir := taskWorkspaceDir(t, paths, slow.ChildInstanceID)
	backing := backingDirOf(t, paths, run.ID)
	gates.release(t, "slow")
	gates.reached(t, "leaf")
	waitForState(t, ds, slow.ChildInstanceID, instance.StateCompleted)
	kids := childrenOf(t, ds, slow.ChildInstanceID)
	require.Len(t, kids, 1)
	assert.False(t, kids[0].State.IsTerminal())

	require.Never(t, func() bool { return !pathExists(taskDir) || !pathExists(backing) },
		2*time.Second, 50*time.Millisecond,
		"the completed task's workspace survives while its fire-and-forget descendant lives")

	gates.release(t, "leaf")
	waitForState(t, ds, kids[0].ID, instance.StateCompleted)
	require.Eventually(t, func() bool { return !pathExists(taskDir) && !pathExists(backing) },
		20*time.Second, 100*time.Millisecond, "the sweep frees the workspace and snapshot once the descendant stopped")
}

// A deleted results snapshot cannot serve as the sweep's inventory of retained
// task workspaces: completed roster entries must remain discoverable instead.
func TestGroupResultsE2E_LastDescendantReleasesWorkspaceAfterSnapshotWasReleased(t *testing.T) {
	prev := groupSweepInterval
	groupSweepInterval = 100 * time.Millisecond
	t.Cleanup(func() { groupSweepInterval = prev })
	gates := newGroupProcessGates(t)
	manifest := strings.ReplaceAll(filesGrandchildManifest, "results_and_files", "results")
	ds, paths := bootFilesStack(t, map[string]string{
		"grp-files-grandchild": manifest,
		"grp-files-leaf":       fmt.Sprintf(filesLeafManifestFmt, groupGateScript, gates.dir),
	})
	t.Cleanup(func() { gates.release(t, "leaf") })
	parentID := triggerGroupWorkflow(t, ds, "grp-files-grandchild")
	gates.reached(t, "leaf")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	run := groupRunOf(t, ds, parentID, "group-1")
	task := groupRosterTask(t, run, "t")
	require.Equal(t, instance.GroupRunDone, run.State)
	require.Equal(t, instance.StateCompleted, stateOf(t, ds, task.ChildInstanceID))
	require.NotEmpty(t, run.WorkspaceSnapshot)
	require.False(t, pathExists(run.WorkspaceSnapshot), "the normal launcher already released the snapshot")
	taskDir := taskWorkspaceDir(t, paths, task.ChildInstanceID)
	require.FileExists(t, filepath.Join(taskDir, "t.txt"))
	require.Never(t, func() bool { return !pathExists(taskDir) }, 500*time.Millisecond, 25*time.Millisecond,
		"a running descendant still pins the completed results task")
	kids := childrenOf(t, ds, task.ChildInstanceID)
	require.Len(t, kids, 1)
	gates.release(t, "leaf")
	waitForState(t, ds, kids[0].ID, instance.StateCompleted)
	require.Eventually(t, func() bool { return !pathExists(taskDir) }, 3*time.Second, 25*time.Millisecond,
		"the sweep must find retained results tasks even without a snapshot")
}
