// adapter/runneragent/executor/workspace_jobs_test.go
package executor_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	configadapter "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/adapter/runneragent/copies"
	"github.com/arbitron/arbitron/adapter/runneragent/executor"
	"github.com/arbitron/arbitron/adapter/runneragent/transfer/transfertest"
	"github.com/arbitron/arbitron/adapter/runneragent/workspace"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const wsRoot = "/srv/arbitron/workspaces"

type jobFixture struct {
	srv       *transfertest.Server
	jobs      *executor.WorkspaceJobs
	syncer    *workspace.Syncer
	localRoot string
	runID     uuid.UUID
	workDir   string
}

func newJobFixture(t *testing.T, harness claudecli.CommandRunner) *jobFixture {
	t.Helper()
	ctx := context.Background()
	seed := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(seed, "input.txt"), []byte("in"), 0o644))
	runID := uuid.New()
	srv := transfertest.NewServer(t.TempDir(), runID)
	_, err := srv.Seed(ctx, seed)
	require.NoError(t, err)
	localRoot := t.TempDir()
	store, err := copies.NewFileStore(filepath.Join(t.TempDir(), "copies"))
	require.NoError(t, err)
	syncer, err := workspace.NewSyncer(workspace.SyncerDeps{WorkspaceRoot: wsRoot, LocalRoot: localRoot, Transfer: srv, Copies: store})
	require.NoError(t, err)
	cache, err := executor.NewBundleCache(filepath.Join(t.TempDir(), "bundles"), srv)
	require.NoError(t, err)
	rules, err := configadapter.LoadCapabilityRules("")
	require.NoError(t, err)
	jobs, err := executor.NewWorkspaceJobs(executor.WorkspaceJobsDeps{
		Syncer: syncer, Bundles: cache, Process: process.NewLocalProcessRunner(),
		ClaudeBinary: "claude", CodexBinary: "codex", HarnessRunner: harness, Rules: rules,
		LookPath: func(name string) (string, error) {
			if name == "claude" || name == "codex" {
				return "/usr/local/bin/" + name, nil
			}
			return exec.LookPath(name)
		},
	})
	require.NoError(t, err)
	jobs.SetInventory(staticSources{claudeAgentSource(t)})
	return &jobFixture{srv: srv, jobs: jobs, syncer: syncer, localRoot: localRoot, runID: runID, workDir: wsRoot + "/instances/" + runID.String()}
}

// newNativeFixture selects the current advertised source inventory.
func newNativeFixture(t *testing.T, harness claudecli.CommandRunner, inv runneragent.LocalSources) *jobFixture {
	t.Helper()
	f := newJobFixture(t, harness)
	f.jobs.SetInventory(inv)
	return f
}

func (f *jobFixture) plan(t *testing.T) *runneragent.WorkspaceJob {
	t.Helper()
	ws, err := f.srv.Plan(context.Background(), f.workDir, 1)
	require.NoError(t, err)
	return &ws
}

func (f *jobFixture) addBundle(t *testing.T, scriptDir, script string) (string, string) {
	t.Helper()
	ref, data, err := process.BuildScriptBundle(filepath.Join(scriptDir, script))
	require.NoError(t, err)
	f.srv.AddBundle(ref.Hash, data)
	return ref.Hash, ref.Entrypoint
}

func TestWorkspaceJobs_LibScriptFindsSiblingsRemotely(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell")
	}
	f := newJobFixture(t, nil)
	lib := t.TempDir() // stands in for the server's shared _lib/tools folder
	require.NoError(t, os.WriteFile(filepath.Join(lib, "run.sh"), []byte("#!/bin/sh\n. ./common.sh\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(lib, "common.sh"), []byte(`printf '{"sibling":"found","arg":"%s"}' "$1"`+"\n"), 0o644))
	hash, entry := f.addBundle(t, lib, "run.sh")

	run := func(arg string) runneragent.Result {
		ws := f.plan(t)
		return f.jobs.Execute(context.Background(), runneragent.Dispatch{Op: runneragent.OpProcess, Workspace: ws, Process: &runneragent.ProcessJob{
			Kind: "script", Command: "/bin/sh", Args: []string{"./" + entry, arg}, Stdin: []byte(`{}`), WorkDir: ws.WorkDir,
			BundleHash: hash, BundleEntrypoint: entry, BundleToken: transfertest.BundleToken}})
	}
	res := run("x")
	require.True(t, res.OK, res.ErrorText)
	require.NotNil(t, res.Process)
	assert.JSONEq(t, `{"sibling":"found","arg":"x"}`, res.Process.Stdout)
	assert.Equal(t, 1, f.srv.Commits(), "a successful workspace step is pushed before its result")

	res = run("y")
	require.True(t, res.OK, res.ErrorText)
	assert.Equal(t, 1, f.srv.BundleFetches(), "bundles are cached by hash")
}

func TestWorkspaceJobs_PassthroughValuesComeFromRunner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell")
	}
	t.Setenv("RUNNER_ONLY_SECRET", "from-the-runner")
	f := newJobFixture(t, nil)
	ws := f.plan(t)
	res := f.jobs.Execute(context.Background(), runneragent.Dispatch{Op: runneragent.OpProcess, Workspace: ws, Process: &runneragent.ProcessJob{
		Kind: "tool_call", Command: "sh", Args: []string{"-c", `printf '{"v":"%s","mode":"%s"}' "$RUNNER_ONLY_SECRET" "$MODE"`},
		Dir: ws.WorkDir, WorkDir: ws.WorkDir, Passthrough: []string{"RUNNER_ONLY_SECRET"}, NodeEnv: map[string]string{"MODE": "server-resolved"}}})
	require.True(t, res.OK, res.ErrorText)
	assert.JSONEq(t, `{"v":"from-the-runner","mode":"server-resolved"}`, res.Process.Stdout)
}

func TestWorkspaceJobs_ToolCallRunsInTheLocalWorkspaceCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell")
	}
	f := newJobFixture(t, nil)
	ws := f.plan(t)
	res := f.jobs.Execute(context.Background(), runneragent.Dispatch{Op: runneragent.OpProcess, Workspace: ws, Process: &runneragent.ProcessJob{
		Kind: "tool_call", Command: "sh", Args: []string{"-c", `cat input.txt > copied.txt; printf '{"ok":true}'`},
		Dir: ws.WorkDir, WorkDir: ws.WorkDir}})
	require.True(t, res.OK, res.ErrorText)
	_, err := f.srv.PromotePrepared(context.Background()) // the server's validated commit (P3 T5); the push only prepared
	require.NoError(t, err)
	b, err := f.srv.ReadLatest(context.Background(), "copied.txt")
	require.NoError(t, err, "the tool ran in the pulled copy and its output was pushed")
	assert.Equal(t, "in", string(b))
}

func TestWorkspaceJobs_FailedStepIsNotPushedAndNextAttemptSeesCleanCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell")
	}
	f := newJobFixture(t, nil)
	ws := f.plan(t)
	res := f.jobs.Execute(context.Background(), runneragent.Dispatch{Op: runneragent.OpProcess, Workspace: ws, Process: &runneragent.ProcessJob{
		Kind: "tool_call", Command: "sh", Args: []string{"-c", `echo partial > input.txt; exit 3`}, Dir: ws.WorkDir, WorkDir: ws.WorkDir}})
	require.True(t, res.OK, "a non-zero exit is a completed process; the server executor decides failure")
	assert.Equal(t, 3, res.Process.ExitCode)
	assert.Zero(t, f.srv.Commits(), "a failed step publishes nothing")
	b, err := f.srv.ReadLatest(context.Background(), "input.txt")
	require.NoError(t, err)
	assert.Equal(t, "in", string(b), "server checkpoint unchanged")

	ws = f.plan(t)
	res = f.jobs.Execute(context.Background(), runneragent.Dispatch{Op: runneragent.OpProcess, Workspace: ws, Process: &runneragent.ProcessJob{
		Kind: "tool_call", Command: "sh", Args: []string{"-c", `printf '{"saw":"%s"}' "$(cat input.txt)"`}, Dir: ws.WorkDir, WorkDir: ws.WorkDir}})
	require.True(t, res.OK, res.ErrorText)
	assert.JSONEq(t, `{"saw":"in"}`, res.Process.Stdout, "the retry restored the committed content first")
}

func TestWorkspaceJobs_SupportsRejectsMissingCommandForeignRootAndCredential(t *testing.T) {
	f := newJobFixture(t, nil)
	ws := f.plan(t)
	ok, reason := f.jobs.Supports(runneragent.Dispatch{Op: runneragent.OpProcess, Workspace: ws, Process: &runneragent.ProcessJob{Kind: "tool_call", Command: "definitely-not-installed-xyz", WorkDir: ws.WorkDir}})
	assert.False(t, ok)
	assert.Equal(t, "command_unavailable: definitely-not-installed-xyz", reason)

	bad := *ws
	bad.WorkDir = "/somewhere/else"
	ok, reason = f.jobs.Supports(runneragent.Dispatch{Op: runneragent.OpProcess, Workspace: &bad, Process: &runneragent.ProcessJob{Kind: "tool_call", Command: "sh", WorkDir: bad.WorkDir}})
	assert.False(t, ok)
	assert.Contains(t, reason, "outside the configured workspace root")

	ok, reason = f.jobs.Supports(runneragent.Dispatch{Op: runneragent.OpProcess, Workspace: ws, Process: &runneragent.ProcessJob{Kind: "script", Command: "sh", WorkDir: ws.WorkDir}})
	assert.False(t, ok, "a script without a bundle is never run remotely")
	assert.Contains(t, reason, "bundle")

	ok, reason = f.jobs.Supports(runneragent.Dispatch{Op: runneragent.OpAgent, Workspace: ws, Agent: &runneragent.AgentJob{WorkDir: ws.WorkDir, CredentialKind: string(runner.CredLunaRoute), Backend: "codex"}})
	assert.False(t, ok)
	assert.Contains(t, reason, "not supported on remote runners")

	ok, _ = f.jobs.Supports(runneragent.Dispatch{Op: runneragent.OpProcess, Workspace: ws, Process: &runneragent.ProcessJob{Kind: "tool_call", Command: "sh", WorkDir: ws.WorkDir}})
	assert.True(t, ok)

	// RR-09 decision 2026-11-22: declared `requires` are checked in order; the first miss names the command.
	ok, reason = f.jobs.Supports(runneragent.Dispatch{Op: runneragent.OpProcess, Workspace: ws, Process: &runneragent.ProcessJob{Kind: "tool_call", Command: "sh", WorkDir: ws.WorkDir, Requires: []string{"sh", "definitely-not-installed-req", "also-missing"}}})
	assert.False(t, ok)
	assert.Equal(t, "command_unavailable: definitely-not-installed-req", reason)
	ok, _ = f.jobs.Supports(runneragent.Dispatch{Op: runneragent.OpProcess, Workspace: ws, Process: &runneragent.ProcessJob{Kind: "tool_call", Command: "sh", WorkDir: ws.WorkDir, Requires: []string{"sh"}}})
	assert.True(t, ok, "present requires accept")

	res := f.jobs.Execute(context.Background(), runneragent.Dispatch{Op: runneragent.OpProcess, Workspace: ws, Process: &runneragent.ProcessJob{Kind: "tool_call", Command: "definitely-not-installed-xyz", WorkDir: ws.WorkDir}})
	assert.False(t, res.OK, "Execute re-checks and fails closed")
	assert.Equal(t, runner.OutcomeOther, res.Outcome.Class)
}

// fakeHarness emulates the claude CLI: it records the RunSpec, edits a file
// in its cwd (the workspace copy) and emits a stream-json result.
type fakeHarness struct {
	mu    sync.Mutex
	specs []claudecli.RunSpec
}

func (f *fakeHarness) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	if err := os.WriteFile(filepath.Join(spec.Dir, "agent-out.md"), []byte("edited by agent"), 0o644); err != nil {
		return nil, nil, err
	}
	ch := make(chan string, 1)
	ch <- `{"type":"result","is_error":false,"result":"mission complete","session_id":"s1","usage":{"input_tokens":3,"output_tokens":2}}`
	close(ch)
	return ch, func() error { return nil }, nil
}

func TestWorkspaceJobs_AgentRunsAtTheWorkspaceAndPushes(t *testing.T) {
	h := &fakeHarness{}
	f := newJobFixture(t, h)
	ws := f.plan(t)
	res := f.jobs.Execute(context.Background(), runneragent.Dispatch{Op: runneragent.OpAgent, Workspace: ws, Agent: &runneragent.AgentJob{
		Provider: "claude-cli", Model: "claude-opus-5-5", Prompt: "edit", WorkDir: ws.WorkDir, ToolScope: []string{"Edit"},
		CredentialKind: string(runner.CredAnthropicSubscription), Backend: "claude-code"}})
	require.True(t, res.OK, res.ErrorText)
	require.NotNil(t, res.Agent)
	assert.Equal(t, "mission complete", res.Agent.Summary)
	assert.Equal(t, 3, res.Agent.TokensIn)
	require.Len(t, h.specs, 1)
	assert.Equal(t, filepath.Join(f.localRoot, "instances", f.runID.String()), h.specs[0].Dir, "the harness runs in the workspace copy")
	assert.True(t, strings.HasSuffix(h.specs[0].Bin, "claude"))
	_, err := f.srv.PromotePrepared(context.Background()) // the server's validated commit (P3 T5); the push only prepared
	require.NoError(t, err)
	b, err := f.srv.ReadLatest(context.Background(), "agent-out.md")
	require.NoError(t, err, "the agent's edit was pushed")
	assert.Equal(t, "edited by agent", string(b))
}
