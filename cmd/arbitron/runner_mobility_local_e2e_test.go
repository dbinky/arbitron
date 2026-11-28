package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
)

// clone (makes the home non-portable) -> edit (held mid-flight; records the
// files it starts with) -> tail (held, so retention is observed before the run
// ends and any completion cleanup runs). Portable opts into workspace receipts;
// no remote runner is connected, so all three steps execute on the built-in.
const p3PartialManifest = `{
  "name": "p3-partial",
  "description": "a partial clone makes the home non-portable; the next step holds mid-flight",
  "inputs": [],
  "nodes": [
    {"id": "clone", "type": "tool_call", "entrypoint": true,
     "config": {"portable": true, "command": "sh", "args": ["-c", "git clone -q --filter=blob:none --no-checkout \"file://$1\" \"$2/repo\" && echo kept > \"$2/kept.txt\" && echo '{}'", "sh", "{{origin}}", "{{workspace}}"]}},
    {"id": "edit", "type": "tool_call",
     "config": {"portable": true, "command": "sh", "args": ["-c", "echo run >> \"$1\"; ls \"$3\" | tr '\\n' ',' >> \"$1.seen\"; echo >> \"$1.seen\"; echo partial > \"$3/partial.txt\"; touch \"$2/np.reached\"; n=0; while [ ! -f \"$2/np.release\" ]; do n=$((n+1)); [ \"$n\" -lt 2400 ] || exit 91; sleep .05; done; rm -f \"$3/partial.txt\"; echo done > \"$3/edit.txt\"; echo '{}'", "sh", "{{counter}}", "{{gateDir}}", "{{workspace}}"]}},
    {"id": "tail", "type": "tool_call",
     "config": {"portable": true, "command": "sh", "args": ["-c", "touch \"$1/tail.reached\"; n=0; while [ ! -f \"$1/tail.release\" ]; do n=$((n+1)); [ \"$n\" -lt 2400 ] || exit 91; sleep .05; done; echo '{}'", "sh", "{{gateDir}}"]}}
  ],
  "edges": [{"source": "clone", "target": "edit"}, {"source": "edit", "target": "tail"}]
}`

// p3PartialOrigin serves blob-filtered clones with missing local promisor objects.
func p3PartialOrigin(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for the partial-clone fixture")
	}
	root := t.TempDir()
	origin, work := filepath.Join(root, "origin.git"), filepath.Join(root, "work")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	git(root, "init", "-q", "--bare", origin)
	git(origin, "config", "uploadpack.allowfilter", "true")
	git(root, "init", "-q", work)
	require.NoError(t, os.WriteFile(filepath.Join(work, "big.txt"), []byte("blob that stays on the promisor remote"), 0o600))
	git(work, "add", "big.txt")
	git(work, "commit", "-q", "-m", "one")
	git(work, "push", "-q", origin, "HEAD:refs/heads/main")
	git(origin, "symbolic-ref", "HEAD", "refs/heads/main")
	return origin
}

func p3LocalCheckpoint(t *testing.T, st *p3Stack, id string) runner.LocalCheckpointRef {
	t.Helper()
	got := rrStrings(t, st.ds, `SELECT local_checkpoint FROM workspace_homes WHERE run_id = ?`, id)
	require.Len(t, got, 1)
	ref, err := runner.ParseLocalCheckpointRef(got[0])
	require.NoError(t, err, "SINK: a committed local checkpoint (got %q)", got[0])
	return ref
}

// Drive initDomain + startRuntime through promisor detection, durable local
// promotion, restart restoration and retention before terminal cleanup.
func TestPhase3E2E_NonPortableRestartRestoresLocalCheckpoint(t *testing.T) {
	origin := p3PartialOrigin(t)
	st := bootP3Stack(t, map[string]p3Workflow{"p3-partial": {manifest: p3PartialManifest}}, "")
	t.Cleanup(func() { st.release(t, "np"); st.release(t, "tail") })
	counter := filepath.Join(t.TempDir(), "counter")
	id, _, err := st.ds.eng.Trigger(adminCtxForLocalUser(t, st.ds), "p3-partial",
		map[string]any{"origin": origin, "counter": counter, "gateDir": st.gateDir}, "normal")
	require.NoError(t, err)
	st.awaitFile(t, "np.reached", 60*time.Second)

	assert.Equal(t, []string{runner.NonPortablePromisorReason},
		rrStrings(t, st.ds, `SELECT non_portable_reason FROM workspace_homes WHERE run_id = ?`, id.String()),
		"the partial clone made the home non-portable")
	afterClone := p3LocalCheckpoint(t, st, id.String())

	require.NoError(t, os.Remove(filepath.Join(st.gateDir, "np.reached")))
	st.crashAndRestart(t)
	st.awaitFile(t, "np.reached", 30*time.Second)
	st.release(t, "np")
	st.awaitFile(t, "tail.reached", 30*time.Second)

	assert.Equal(t, 2, p3CountLines(t, counter), "SINK: the interrupted step repeated once")
	seen, err := os.ReadFile(counter + ".seen")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(seen)), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, "kept.txt,repo,", lines[0])
	assert.Equal(t, lines[0], lines[1], "SINK: the repeat started from the restored local checkpoint, not the dirty copy")
	assert.Positive(t, p3NoticeCount(t, st.ds, id, "edit"))

	afterEdit := p3LocalCheckpoint(t, st, id.String())
	assert.Equal(t, afterClone.Version+1, afterEdit.Version, "SINK: the repeated step committed the next local checkpoint")
	versions, err := filepath.Glob(filepath.Join(filepath.Dir(st.configPath), "checkpoints", "local", id.String(), "v*"))
	require.NoError(t, err)
	assert.Len(t, versions, 1, "SINK: only the latest local checkpoint is retained")

	st.release(t, "tail")
	p3RequireCompleted(t, st.ds, id, 30*time.Second)
	localRun := filepath.Join(filepath.Dir(st.configPath), "checkpoints", "local", id.String())
	require.Eventually(t, func() bool {
		_, err := os.Stat(localRun)
		return os.IsNotExist(err)
	}, 30*time.Second, 50*time.Millisecond, "SINK: terminal workspace removal deletes the local copies too")
}

// A failed local copy cannot commit a succeeded execution or advance the run.
func TestPhase3E2E_NonPortableLocalCopyFailureLeavesStepUndone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not bind root")
	}
	origin := p3PartialOrigin(t)
	st := bootP3Stack(t, map[string]p3Workflow{"p3-partial": {manifest: p3PartialManifest}}, "")
	t.Cleanup(func() { st.release(t, "np"); st.release(t, "tail") })
	counter := filepath.Join(t.TempDir(), "counter")
	id, _, err := st.ds.eng.Trigger(adminCtxForLocalUser(t, st.ds), "p3-partial",
		map[string]any{"origin": origin, "counter": counter, "gateDir": st.gateDir}, "normal")
	require.NoError(t, err)
	st.awaitFile(t, "np.reached", 60*time.Second)
	before := p3LocalCheckpoint(t, st, id.String())

	localRun := filepath.Join(filepath.Dir(st.configPath), "checkpoints", "local", id.String())
	require.NoError(t, os.Chmod(localRun, 0o500))
	t.Cleanup(func() { _ = os.Chmod(localRun, 0o700) })
	st.release(t, "np")

	require.Eventually(t, func() bool {
		inst, err := st.ds.instanceRepo.Get(context.Background(), id)
		return err == nil && inst.State.IsTerminal()
	}, 60*time.Second, 50*time.Millisecond)
	inst, err := st.ds.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.StateFailed, inst.State)
	assert.Empty(t, rrStrings(t, st.ds,
		`SELECT id FROM node_executions WHERE instance_id = ? AND node_id = 'edit' AND status = 'succeeded'`, id.String()),
		"SINK: the step is not done")
	assert.Equal(t, before, p3LocalCheckpoint(t, st, id.String()), "SINK: the committed local checkpoint is unchanged")
	_, err = os.Stat(filepath.Join(st.gateDir, "tail.reached"))
	assert.True(t, os.IsNotExist(err), "the run never advanced")
}
