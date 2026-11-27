package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
	configadapter "github.com/arbitron/arbitron/adapter/config"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/adapter/process"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	runnerprobe "github.com/arbitron/arbitron/adapter/runner/probe"
	"github.com/arbitron/arbitron/adapter/runneragent/copies"
	"github.com/arbitron/arbitron/adapter/runneragent/executor"
	"github.com/arbitron/arbitron/adapter/runneragent/sources"
	"github.com/arbitron/arbitron/adapter/runneragent/transfer"
	"github.com/arbitron/arbitron/adapter/runneragent/workspace"
	"github.com/arbitron/arbitron/adapter/runneragent/wsclient"
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
)

// p3Workflow is one manifest plus files under its workflow directory.
type p3Workflow struct {
	manifest string
	files    map[string]string // workflow-relative path -> content
}

// p3Stack is a started production stack served at a STABLE URL: a restart
// swaps the handler, so runner agents reconnect to the same address.
type p3Stack struct {
	ds           *domainStack
	srv          *httptest.Server
	handler      atomic.Pointer[http.Handler]
	configPath   string
	workspaceDir string // canonical server root; every runner advertises it
	runnerClaude string
	gateDir      string
	invocations  string // one line per runner-claude call: its P3 tag
}

func (st *p3Stack) rr() *rrStack {
	return &rrStack{ds: st.ds, srv: st.srv, workspaceDir: st.workspaceDir}
}

func (st *p3Stack) serve(ds *domainStack) {
	var h http.Handler = adapthttp.NewRouterFromDeps(ds.httpDeps())
	st.handler.Store(&h)
	st.ds = ds
}

// p3BuiltinClaude is the SERVER's claude: logged out, so the built-in runner
// has no healthy Anthropic source and every agent step must go remote.
const p3BuiltinClaude = "#!/bin/sh\n" +
	"case \"$1\" in\n" +
	"  --version) echo '2.1.300 (Claude Code)'; exit 0 ;;\n" +
	"  auth) echo '{\"loggedIn\":false}'; exit 1 ;;\n" +
	"esac\n" +
	"echo 'the built-in claude is logged out in this test' >&2\n" +
	"exit 1\n"

// p3RunnerClaudeTemplate is the RUNNERS' claude. It runs in the step's local
// workspace copy (cwd), keys its behaviour on the P3-* tag in the prompt,
// logs every call, and answers "<agent.log lines>|<ls of the copy>".
const p3RunnerClaudeTemplate = `#!/bin/sh
case "$1" in
  --version) echo '2.1.300 (Claude Code)'; exit 0 ;;
  auth) echo '{"loggedIn":true}'; exit 0 ;;
esac
gate='@GATE@'
prompt=$(cat)
m=$(echo "$*" | sed -n 's/.*--model \([^ ]*\).*/\1/p')
tag=$(printf '%s' "$prompt" | grep -o 'P3-[A-Z0-9-]*' | head -n 1)
echo "$tag" >> '@INVOCATIONS@'
wait_for() { n=0; while [ ! -f "$1" ]; do n=$((n+1)); [ "$n" -lt 2400 ] || exit 91; sleep .05; done; }
case "$tag" in
  P3-STEP-ONE) echo step1 >> agent.log ;;
  P3-STEP-TWO) echo step2 >> agent.log ;;
  P3-HOLD)
    if [ ! -f "$gate/hold.used" ]; then
      touch "$gate/hold.used"
      echo partial > partial.txt
      touch "$gate/hold.started"
      wait_for "$gate/hold.release"
      rm -f partial.txt
    fi
    echo step2 >> agent.log ;;
  P3-BRANCH-A) touch "$gate/branch-a.started"; wait_for "$gate/branch-b.started"; echo a > branch-a.txt ;;
  P3-BRANCH-B) touch "$gate/branch-b.started"; wait_for "$gate/branch-a.started"; echo b > branch-b.txt ;;
esac
log=''
if [ -f agent.log ]; then log=$(tr '\n' ',' < agent.log); fi
files=$(ls | tr '\n' ',')
printf '{"type":"result","is_error":false,"result":"%s|%s","session_id":"s","usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{"%s":{}}}\n' "$log" "$files" "$m"
`

// bootP3Stack boots the REAL composition root (initDomain + Start) with the
// given workflows, the logged-out built-in claude, rrRunnersYAML plus
// extraRunners (lines under "runners:"), and serves httpDeps() at a stable URL.
func bootP3Stack(t *testing.T, workflows map[string]p3Workflow, extraRunners string) *p3Stack {
	t.Helper()
	rrPinReleasedVersion(t)
	if runtime.GOOS == "windows" {
		t.Skip("the fake harnesses are POSIX shell scripts")
	}
	pinSinkRootEnv(t)
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))
	for name, wf := range workflows {
		writeWorkflow(t, workspaceDir, name, wf.manifest)
		base := filepath.Join(workspaceDir, "users", localDefaultUser.String(), name)
		for rel, content := range wf.files {
			p := filepath.Join(base, filepath.FromSlash(rel))
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
			mode := os.FileMode(0o600)
			if strings.HasSuffix(rel, ".sh") {
				mode = 0o700
			}
			require.NoError(t, os.WriteFile(p, []byte(content), mode))
		}
	}
	canonical, err := canonicalWorkspacePath(workspaceDir)
	require.NoError(t, err)
	gateDir := t.TempDir()
	invocations := filepath.Join(root, "invocations.log")
	builtin := filepath.Join(root, "builtin-claude.sh")
	require.NoError(t, os.WriteFile(builtin, []byte(p3BuiltinClaude), 0o700))
	runnerClaude := filepath.Join(root, "runner-claude.sh")
	script := strings.NewReplacer("@GATE@", gateDir, "@INVOCATIONS@", invocations).Replace(p3RunnerClaudeTemplate)
	require.NoError(t, os.WriteFile(runnerClaude, []byte(script), 0o700))

	configPath := filepath.Join(root, "config.yaml")
	cfg := "" +
		"server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: " + yamlQuote(filepath.Join(root, "arbitron.db")) + "\n" +
		"workspace:\n  path: " + yamlQuote(workspaceDir) + "\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: " + yamlQuote(filepath.Join(root, "arbitron.log")) + "\n" +
		"queue:\n  globalcap: 5\n  perusercap: 5\n" +
		"llm:\n  binarypath: " + yamlQuote(builtin) + "\n  allowedtools: [Read, Write, Bash]\n" +
		groupTestProfilesYAML +
		rrRunnersYAML + extraRunners
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))

	st := &p3Stack{configPath: configPath, workspaceDir: canonical, runnerClaude: runnerClaude, gateDir: gateDir, invocations: invocations}
	ds, err := initStartedDomain(context.Background(), configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	// Establish the logged-out fixture through its real auth producer before
	// triggering work; startup health sampling is asynchronous.
	health, err := ds.runners.monitor.ProbeNow(context.Background(), runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredAnthropicSubscription, ""))
	require.NoError(t, err)
	require.Equal(t, runner.AuthLoggedOut, health.Auth)
	st.serve(ds)
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(*st.handler.Load()).ServeHTTP(w, r)
	}))
	t.Cleanup(st.srv.Close)
	// Held fake harnesses and gates must never outlive the test.
	t.Cleanup(func() {
		for _, g := range []string{"hold", "b", "child", "bf"} {
			st.release(t, g)
		}
	})
	return st
}

// crashAndRestart severs the database under the live process (no graceful
// drain, like a kill), boots a second stack on the same files and serves it
// at the same URL.
func (st *p3Stack) crashAndRestart(t *testing.T) {
	t.Helper()
	old := st.ds
	priorRunners, err := old.runners.registry.ListRunners(context.Background())
	require.NoError(t, err)
	require.NoError(t, old.db.Close())
	t.Cleanup(func() {
		for _, g := range []string{"hold", "b", "child", "bf"} {
			st.release(t, g)
		}
		// The simulated dead process still has in-memory hub waiters. Release
		// only that abandoned hub; its closed DB cannot change the new stack.
		for _, r := range priorRunners {
			if !r.BuiltIn {
				_ = old.remote.Hub.RevokeRunner(context.Background(), r.ID)
			}
		}
		old.queueMgr.Stop()
		old.eng.Shutdown()
	})
	ds, err := initStartedDomain(context.Background(), st.configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	st.serve(ds)
}

func (st *p3Stack) release(t *testing.T, gate string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(st.gateDir, gate+".release"), nil, 0o600))
}

func (st *p3Stack) awaitFile(t *testing.T, name string, within time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(st.gateDir, name))
		return err == nil
	}, within, 20*time.Millisecond, "%s never appeared", name)
}

// tagCount counts runner-claude invocations carrying tag.
func (st *p3Stack) tagCount(t *testing.T, tag string) int {
	t.Helper()
	b, err := os.ReadFile(st.invocations)
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if l == tag {
			n++
		}
	}
	return n
}

// countingTransfer wraps the REAL transfer client and counts each call.
type countingTransfer struct {
	inner                                     transfer.Transfer
	manifests, files, parts, commits, bundles atomic.Int64
}

func (c *countingTransfer) Manifest(ctx context.Context, token string) (runner.Manifest, error) {
	c.manifests.Add(1)
	return c.inner.Manifest(ctx, token)
}

func (c *countingTransfer) FetchFile(ctx context.Context, token, path string) (io.ReadCloser, error) {
	c.files.Add(1)
	return c.inner.FetchFile(ctx, token, path)
}

func (c *countingTransfer) PutPart(ctx context.Context, token string, index int, data []byte) error {
	c.parts.Add(1)
	return c.inner.PutPart(ctx, token, index, data)
}

func (c *countingTransfer) CommitPush(ctx context.Context, token string, pc transfer.PushCommit) (runner.CheckpointRef, error) {
	c.commits.Add(1)
	return c.inner.CommitPush(ctx, token, pc)
}

func (c *countingTransfer) FetchBundle(ctx context.Context, token, hash string) (io.ReadCloser, error) {
	c.bundles.Add(1)
	return c.inner.FetchBundle(ctx, token, hash)
}

// p3WorkspaceExec is the runner's JobExecutor: workspace jobs only.
type p3WorkspaceExec struct{ jobs *executor.WorkspaceJobs }

func (e p3WorkspaceExec) Supports(d runneragent.Dispatch) (bool, string) {
	if d.Op == runneragent.OpProcess || d.Op == runneragent.OpAgent {
		return e.jobs.Supports(d)
	}
	return false, "the phase 3 test runner executes workspace jobs only"
}

func (e p3WorkspaceExec) Execute(ctx context.Context, d runneragent.Dispatch, _ runneragent.EmitFunc) runneragent.Result {
	return e.jobs.Execute(ctx, d)
}

// p3Runner is one in-memory remote runner.
type p3Runner struct {
	id        string
	keyID     string
	conn      *rrConn
	localRoot string // its workspace copies live here (production uses the server root itself)
	xfer      *countingTransfer
}

// localCopy is where this runner keeps run's workspace copy.
func (r *p3Runner) localCopy(run uuid.UUID) string {
	return filepath.Join(r.localRoot, "instances", run.String())
}

// startWorkspaceRunner creates a runner key over REST and runs a real
// runneragent.Agent through the real wsclient with the P3 T9 workspace stack.
// caps defaults to process + agent.
func startWorkspaceRunner(t *testing.T, st *p3Stack, name string, caps ...string) *p3Runner {
	t.Helper()
	plaintext, runnerID, keyID := rrCreateKey(t, st.rr(), name)
	xfer := &countingTransfer{inner: transfer.NewClient(st.srv.URL, plaintext, nil, nil)}
	localRoot := t.TempDir()
	copyStore, err := copies.NewFileStore(t.TempDir())
	require.NoError(t, err)
	syncer, err := workspace.NewSyncer(workspace.SyncerDeps{WorkspaceRoot: st.workspaceDir, LocalRoot: localRoot, Transfer: xfer, Copies: copyStore})
	require.NoError(t, err)
	bundles, err := executor.NewBundleCache(t.TempDir(), xfer)
	require.NoError(t, err)
	rules, err := configadapter.LoadCapabilityRules("")
	require.NoError(t, err)
	jobs, err := executor.NewWorkspaceJobs(executor.WorkspaceJobsDeps{
		Syncer: syncer, Bundles: bundles, Process: process.NewLocalProcessRunner(),
		ClaudeBinary: st.runnerClaude, MaxResponseBytes: 1 << 20, Rules: rules,
	})
	require.NoError(t, err)
	client, err := wsclient.New(wsclient.Options{ServerURL: st.srv.URL, RunnerKey: plaintext})
	require.NoError(t, err)
	conn := newRRConn(client)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	if len(caps) == 0 {
		caps = []string{runneragent.OpProcess, runneragent.OpAgent}
	}
	scratch := t.TempDir()
	cmdRunner := claudecli.NewExecRunner()
	factory := providersadapter.NewFactory(providersadapter.Deps{
		ClaudeAgentConfig: claudecli.Config{BinaryPath: st.runnerClaude, WorkspaceRoot: scratch, Runner: cmdRunner, CapabilityRules: rules},
		CapabilityRules:   rules,
	})
	discover := sources.DiscoverInput{ClaudeBinary: st.runnerClaude, Runner: cmdRunner, Rules: rules,
		Native: sources.NativeExecutionInput{Rules: rules, WorkspaceEnabled: true,
			Codex: codex.Config{BinaryPath: filepath.Join(scratch, "no-codex")}}}
	verifier := sources.NewClaudeModelVerifier(runnerprobe.NewConfirmationExecutor(factory), scratch, nil)
	inv := sources.NewRefreshing(context.Background(), sources.VerifiedDiscover(discover, verifier), time.Now)
	inv.EnableClaudeVerification(verifier, discover)
	agent, err := runneragent.New(runneragent.Config{
		OS: "darwin", Arch: "arm64",
		ProtocolVersion: runnerproto.ProtocolVersion, RunnerVersion: rrReleasedVersion,
		HarnessVersions: map[string]string{"claude": "2.1.300 (Claude Code)"},
		WorkspaceRoot:   st.workspaceDir, ScratchRoot: scratch,
		Capabilities: caps,
		Heartbeat:    time.Second, ProbeInterval: time.Minute,
		BackoffBase: 50 * time.Millisecond, BackoffCap: 200 * time.Millisecond,
	}, runneragent.Deps{
		Conn: conn, Exec: p3WorkspaceExec{jobs: jobs}, Sources: inv, Models: inv, Telemetry: rrTelemetry{t: runner.Telemetry{}},
		Spool: runneragent.NewMemorySpool(1024), Identity: rrIdentity{key: priv}, Copies: syncer, Pulls: syncer,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = agent.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	rrAwaitLiveness(t, st.rr(), runnerID, "online")
	return &p3Runner{id: runnerID, keyID: keyID, conn: conn, localRoot: localRoot, xfer: xfer}
}

// p3RequireCompleted waits up to within for a terminal state and fails with
// the node errors unless the run completed.
func p3RequireCompleted(t *testing.T, ds *domainStack, id uuid.UUID, within time.Duration) {
	t.Helper()
	var inst *instance.Instance
	require.Eventually(t, func() bool {
		var err error
		inst, err = ds.instanceRepo.Get(context.Background(), id)
		return err == nil && inst.State.IsTerminal()
	}, within, 50*time.Millisecond, "run %s never reached a terminal state", id)
	if inst.State != instance.StateCompleted {
		errs := rrStrings(t, ds, `SELECT node_id || ': ' || error FROM node_executions WHERE instance_id = ? AND error IS NOT NULL AND error != ''`, id.String())
		t.Fatalf("run %s ended %s; node errors: %v", id, inst.State, errs)
	}
}

// p3Output is the latest succeeded output of node in run.
func p3Output(t *testing.T, ds *domainStack, id uuid.UUID, node string) string {
	t.Helper()
	outs := rrStrings(t, ds, `SELECT COALESCE(output, '') FROM node_executions WHERE instance_id = ? AND node_id = ? AND status = 'succeeded' ORDER BY started_at DESC`, id.String(), node)
	require.NotEmpty(t, outs, "node %s of run %s has no succeeded execution", node, id)
	return outs[0]
}

// p3CompletedOn lists, in order, the runners whose job for node completed.
func p3CompletedOn(t *testing.T, ds *domainStack, id uuid.UUID, node string) []string {
	t.Helper()
	return rrStrings(t, ds, `SELECT runner_id FROM runner_jobs WHERE run_id = ? AND node_id = ? AND state = 'completed' ORDER BY created_at`, id.String(), node)
}

// p3HomeOwner never calls require: safe inside Eventually.
func p3HomeOwner(ds *domainStack, id uuid.UUID) (string, bool) {
	var owner string
	err := ds.db.QueryRow(`SELECT runner_id FROM workspace_homes WHERE run_id = ?`, id.String()).Scan(&owner)
	return owner, err == nil
}

// p3NoticeCount counts recovery notices on node's executions in the trace.
func p3NoticeCount(t *testing.T, ds *domainStack, id uuid.UUID, node string) int {
	t.Helper()
	trace, err := ds.eng.GetTrace(adminCtxForLocalUser(t, ds), id)
	require.NoError(t, err)
	n := 0
	for _, e := range trace {
		if e.NodeID == node {
			n += len(e.Recovery)
		}
	}
	return n
}

func p3CountLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	return strings.Count(string(b), "\n")
}
