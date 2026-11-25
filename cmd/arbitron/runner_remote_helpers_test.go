package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runneragent/sources"
	"github.com/arbitron/arbitron/adapter/runneragent/wsclient"
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
)

// rrRunnersYAML shortens liveness so chaos tests finish in seconds and sets
// step limit 1 on every runner (built-in row and new keys alike) so N
// concurrent steps MUST spread across N runners.
const rrRunnersYAML = "runners:\n" +
	"  defaultsteplimit: 1\n" +
	"  heartbeatseconds: 1\n" +
	"  suspectafterseconds: 3\n" +
	"  lostafterseconds: 6\n" +
	"  offerttlseconds: 3\n"

// rrTemplateMarker is the llm_call template body; a remote dispatch must carry it.
const rrTemplateMarker = "rr-template-marker"

const rrSingleManifest = `{
  "name": "rr-single",
  "description": "one hermetic chat step",
  "inputs": [],
  "nodes": [
    {"id": "ask", "type": "llm_call", "entrypoint": true,
     "config": {"model_profile": "default-chat", "templateFile": "templates/p.md"}}
  ],
  "edges": []
}`

const rrFanoutManifest = `{
  "name": "rr-fanout",
  "description": "three concurrent llm_call branches",
  "inputs": [],
  "nodes": [
    {"id": "start", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "started", "operation": "set", "value": true}]}},
    {"id": "g", "type": "parallel_group",
     "config": {"group": {"form": "branches",
       "tasks": [{"key": "a", "entry": "a1"}, {"key": "b", "entry": "b1"}, {"key": "c", "entry": "c1"}],
       "completionRule": "all_succeed", "unfinishedBehavior": "wait_for_remaining", "gatherMode": "results"}}},
    {"id": "a1", "type": "llm_call", "config": {"model_profile": "default-chat", "templateFile": "templates/p.md"}},
    {"id": "b1", "type": "llm_call", "config": {"model_profile": "default-chat", "templateFile": "templates/p.md"}},
    {"id": "c1", "type": "llm_call", "config": {"model_profile": "default-chat", "templateFile": "templates/p.md"}},
    {"id": "done", "type": "transform",
     "config": {"operations": [{"variable": "outcome", "operation": "set", "value": "success"}]}},
    {"id": "fail", "type": "transform",
     "config": {"operations": [{"variable": "outcome", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "start", "target": "g"},
    {"source": "g", "target": "a1", "output": "task", "label": "a"},
    {"source": "g", "target": "b1", "output": "task", "label": "b"},
    {"source": "g", "target": "c1", "output": "task", "label": "c"},
    {"source": "g", "target": "done", "output": "success"},
    {"source": "g", "target": "fail", "output": "failure"}
  ]
}`

// rrStack is a started production stack plus the router served over HTTP.
type rrStack struct {
	ds           *domainStack
	srv          *httptest.Server
	workspaceDir string
	goFile       string // once it exists, the built-in fake claude answers
	configPath   string
	addr         string
}

// bootRemoteRunnerStack boots the REAL composition root (initDomain + Start)
// with the given workflows, a fake built-in claude and rrRunnersYAML.
// builtinBlocks=true holds every built-in chat until goFile is created.
func bootRemoteRunnerStack(t *testing.T, manifests map[string]string, builtinBlocks bool) *rrStack {
	t.Helper()
	rrPinReleasedVersion(t)
	if runtime.GOOS == "windows" {
		t.Skip("the fake harness is a POSIX shell script")
	}
	pinSinkRootEnv(t)
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))
	for name, manifest := range manifests {
		writeWorkflow(t, workspaceDir, name, manifest)
		tplDir := filepath.Join(workspaceDir, "users", localDefaultUser.String(), name, "templates")
		require.NoError(t, os.MkdirAll(tplDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(tplDir, "p.md"), []byte(rrTemplateMarker), 0o600))
	}
	goFile := filepath.Join(root, "builtin-go")
	if !builtinBlocks {
		require.NoError(t, os.WriteFile(goFile, nil, 0o600))
	}
	bin := rrFakeClaude(t, root, goFile)
	configPath := filepath.Join(root, "config.yaml")
	cfg := "" +
		"server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: " + yamlQuote(filepath.Join(root, "arbitron.db")) + "\n" +
		"workspace:\n  path: " + yamlQuote(workspaceDir) + "\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: " + yamlQuote(filepath.Join(root, "arbitron.log")) + "\n" +
		"queue:\n  globalcap: 5\n  perusercap: 5\n" +
		"llm:\n  binarypath: " + yamlQuote(bin) + "\n" +
		groupTestProfilesYAML +
		rrRunnersYAML
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))
	ds, err := initStartedDomain(context.Background(), configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	srv := gsServeAPI(t, ds)
	return &rrStack{ds: ds, srv: srv, workspaceDir: ds.cfg.Workspace.Path, goFile: goFile, configPath: configPath, addr: srv.Listener.Addr().String()}
}

// rrFakeClaude is the server's built-in claude: a supported --version and a
// logged-in `auth status` (so P1 T13 inventory registers a healthy
// anthropic_subscription source), and a chat answer "builtin-answer" that
// waits for goFile.
func rrFakeClaude(t *testing.T, dir, goFile string) string {
	t.Helper()
	bin := filepath.Join(dir, "fake-claude.sh")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  --version) echo '2.1.300 (Claude Code)'; exit 0 ;;\n" +
		"  auth) echo '{\"loggedIn\":true}'; exit 0 ;;\n" +
		"esac\n" +
		"while [ ! -f '" + goFile + "' ]; do sleep 0.05; done\n" +
		"cat > /dev/null\n" +
		"m=$(echo \"$*\" | sed -n 's/.*--model \\([^ ]*\\).*/\\1/p')\n" +
		"echo '{\"type\":\"result\",\"is_error\":false,\"result\":\"builtin-answer\",\"session_id\":\"s\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1},\"modelUsage\":{\"'\"$m\"'\":{}}}'\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))
	return bin
}

// rrExecutor is the remote runner's JobExecutor. It honours the dispatch it
// is given (records it, answers with the dispatched model) and, when block is
// set, finishes only on release — ignoring ctx on purpose, like a partitioned
// machine that completes its work late.
type rrExecutor struct {
	answer      string
	block       bool
	started     chan runneragent.Dispatch
	release     chan struct{}
	once        sync.Once
	mu          sync.Mutex
	inflight    int
	maxInflight int
	got         []runneragent.Dispatch
}

func newRRExecutor(t *testing.T, answer string, block bool) *rrExecutor {
	t.Helper()
	e := &rrExecutor{answer: answer, block: block, started: make(chan runneragent.Dispatch, 32), release: make(chan struct{})}
	t.Cleanup(e.releaseAll)
	return e
}

func (e *rrExecutor) releaseAll() { e.once.Do(func() { close(e.release) }) }

func (e *rrExecutor) Supports(d runneragent.Dispatch) (bool, string) {
	if d.Op != runneragent.OpChat || d.Chat == nil {
		return false, "rr test executor runs chat only"
	}
	return true, ""
}

func (e *rrExecutor) Execute(_ context.Context, d runneragent.Dispatch, _ runneragent.EmitFunc) runneragent.Result {
	e.mu.Lock()
	e.inflight++
	if e.inflight > e.maxInflight {
		e.maxInflight = e.inflight
	}
	e.got = append(e.got, d)
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.inflight--
		e.mu.Unlock()
	}()
	e.started <- d
	if e.block {
		<-e.release
	}
	return runneragent.Result{OK: true, Chat: &runneragent.ChatResult{
		Output: e.answer, Model: d.Chat.Model, SessionID: "rr", TokensIn: 1, TokensOut: 1}}
}

func (e *rrExecutor) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.got)
}

func (e *rrExecutor) peak() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.maxInflight
}

func rrAwaitDispatch(t *testing.T, e *rrExecutor) runneragent.Dispatch {
	t.Helper()
	select {
	case d := <-e.started:
		return d
	case <-time.After(30 * time.Second):
		t.Fatal("the remote runner never received a dispatch")
		return runneragent.Dispatch{}
	}
}

// rrSources is one healthy Claude subscription source (Families anthropic).
type rrSources struct{}

func rrAnthropic() []runneragent.Source {
	return []runneragent.Source{{Kind: runner.CredAnthropicSubscription,
		Families: []modelidentity.Family{"anthropic"},
		Backends: []string{""}, Capabilities: rrChatCapabilities(rrFP, rrModel),
		Health: runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityAvailable}}}
}

func (rrSources) Sources(context.Context) []runneragent.Source { return rrAnthropic() }
func (rrSources) Probe(context.Context) []runneragent.Source   { return rrAnthropic() }

// rrTelemetry reports a fixed machine state, freshly timestamped.
type rrTelemetry struct{ t runner.Telemetry }

func (r rrTelemetry) Sample(context.Context) (runner.Telemetry, error) {
	out := r.t
	out.SampledAt = time.Now().UTC()
	return out, nil
}

type rrIdentity struct{ key ed25519.PrivateKey }

func (r rrIdentity) Key(context.Context) (ed25519.PrivateKey, error) { return r.key, nil }

type rrInbound struct {
	Kind       string
	JobID      string
	Generation int64
}

// rrConn wraps the real wsclient connection: it records every server message
// and can cut the live socket and hold redials (connection-kill chaos).
type rrConn struct {
	inner runneragent.Connection
	mu    sync.Mutex
	gate  chan struct{}
	cur   runneragent.Session
	seen  []rrInbound
}

func newRRConn(inner runneragent.Connection) *rrConn {
	gate := make(chan struct{})
	close(gate)
	return &rrConn{inner: inner, gate: gate}
}

func (c *rrConn) Dial(ctx context.Context) (runneragent.Session, error) {
	c.mu.Lock()
	gate := c.gate
	c.mu.Unlock()
	select {
	case <-gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s, err := c.inner.Dial(ctx)
	if err != nil {
		return nil, err
	}
	rs := &rrSession{Session: s, c: c}
	c.mu.Lock()
	c.cur = rs
	c.mu.Unlock()
	return rs, nil
}

// cut closes the live socket and blocks redials until allow.
func (c *rrConn) cut() {
	c.mu.Lock()
	c.gate = make(chan struct{})
	s := c.cur
	c.cur = nil
	c.mu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}

func (c *rrConn) allow() {
	c.mu.Lock()
	defer c.mu.Unlock()
	close(c.gate)
}

func (c *rrConn) received(kind, jobID string, gen int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, in := range c.seen {
		if in.Kind == kind && in.JobID == jobID && in.Generation == gen {
			return true
		}
	}
	return false
}

type rrSession struct {
	runneragent.Session
	c *rrConn
}

func (s *rrSession) Recv(ctx context.Context) (runneragent.Inbound, error) {
	in, err := s.Session.Recv(ctx)
	if err == nil {
		s.c.mu.Lock()
		s.c.seen = append(s.c.seen, rrInbound{Kind: in.Kind, JobID: in.JobID, Generation: in.Generation})
		s.c.mu.Unlock()
	}
	return in, err
}

// startRemoteAgent runs a real runneragent.Agent against the served router
// through the real wsclient (Authorization: Runner <key>). workspaceRoot is
// what the runner advertises in hello.
func startRemoteAgent(t *testing.T, st *rrStack, plaintext, workspaceRoot string, exec runneragent.JobExecutor, tel runner.Telemetry) (*rrConn, *runneragent.MemorySpool) {
	t.Helper()
	return startRemoteAgentVersion(t, st, plaintext, workspaceRoot, exec, tel, rrReleasedVersion)
}

func startRemoteAgentVersion(t *testing.T, st *rrStack, plaintext, workspaceRoot string, exec runneragent.JobExecutor, tel runner.Telemetry, runnerVersion string) (*rrConn, *runneragent.MemorySpool) {
	t.Helper()
	client, err := wsclient.New(wsclient.Options{ServerURL: st.srv.URL, RunnerKey: plaintext})
	require.NoError(t, err)
	conn := newRRConn(client)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	spool := runneragent.NewMemorySpool(1024)
	agent, err := runneragent.New(runneragent.Config{
		OS: "darwin", Arch: "arm64",
		ProtocolVersion: runnerproto.ProtocolVersion, RunnerVersion: runnerVersion,
		HarnessVersions: map[string]string{"claude": "2.1.300 (Claude Code)"},
		WorkspaceRoot:   workspaceRoot, ScratchRoot: t.TempDir(),
		Capabilities: []string{runneragent.OpChat, runneragent.OpStream},
		Heartbeat:    time.Second, ProbeInterval: time.Minute,
		BackoffBase: 50 * time.Millisecond, BackoffCap: 200 * time.Millisecond,
	}, runneragent.Deps{
		Conn: conn, Exec: exec, Sources: rrSources{}, Telemetry: rrTelemetry{t: tel},
		Spool: spool, Identity: rrIdentity{key: priv},
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
		if e, ok := exec.(interface{ releaseAll() }); ok {
			e.releaseAll()
		}
		<-done
	})
	return conn, spool
}

// rrDo performs one REST call as the local-mode admin. Not for Eventually.
func rrDo(t *testing.T, method, url string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, out
}

// rrCreateKey creates a runner key through REST (P2 T3) and returns the
// plaintext, the runner it registered, and the key ID.
func rrCreateKey(t *testing.T, st *rrStack, name string) (plaintext, runnerID, keyID string) {
	t.Helper()
	code, body := rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/keys", map[string]any{"name": name})
	require.Equal(t, http.StatusCreated, code, string(body))
	var out struct {
		Key struct {
			ID       string `json:"id"`
			RunnerID string `json:"runnerId"`
		} `json:"key"`
		Plaintext string `json:"plaintext"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	require.Regexp(t, `^ark_`, out.Plaintext)
	require.NotEmpty(t, out.Key.RunnerID)
	return out.Plaintext, out.Key.RunnerID, out.Key.ID
}

// rrRunner reads GET /api/v1/runners/{id}; safe inside Eventually.
func rrRunner(srv *httptest.Server, id string) (map[string]any, bool) {
	resp, err := http.Get(srv.URL + "/api/v1/runners/" + id)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var out map[string]any
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return nil, false
	}
	return out, true
}

func rrLiveness(srv *httptest.Server, id string) string {
	r, ok := rrRunner(srv, id)
	if !ok {
		return ""
	}
	s, _ := r["liveness"].(string)
	return s
}

func rrCompat(srv *httptest.Server, id string) string {
	r, ok := rrRunner(srv, id)
	if !ok {
		return ""
	}
	c, _ := r["compat"].(map[string]any)
	s, _ := c["state"].(string)
	return s
}

func rrAwaitLiveness(t *testing.T, st *rrStack, id, want string) {
	t.Helper()
	require.Eventually(t, func() bool { return rrLiveness(st.srv, id) == want },
		30*time.Second, 50*time.Millisecond, "runner %s never became %s (last %q)", id, want, rrLiveness(st.srv, id))
}

// rrRunningByRunner counts running runner_jobs per runner; safe inside Eventually.
func rrRunningByRunner(ds *domainStack) map[string]int {
	out := map[string]int{}
	rows, err := ds.db.Query(`SELECT runner_id, COUNT(*) FROM runner_jobs WHERE state = 'running' AND runner_id IS NOT NULL GROUP BY runner_id`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if rows.Scan(&id, &n) == nil {
			out[id] = n
		}
	}
	return out
}

func rrStrings(t *testing.T, ds *domainStack, query string, args ...any) []string {
	t.Helper()
	rows, err := ds.db.Query(query, args...)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

// rrRequireCompleted waits for the run and fails with the node errors if it
// did not complete.
func rrRequireCompleted(t *testing.T, ds *domainStack, id uuid.UUID) {
	t.Helper()
	inst := waitTerminal(t, ds, id)
	if inst.State != instance.StateCompleted {
		errs := rrStrings(t, ds, `SELECT node_id || ': ' || error FROM node_executions WHERE error IS NOT NULL AND error != ''`)
		t.Fatalf("run %s ended %s; node errors: %v", id, inst.State, errs)
	}
}

const rrReleasedVersion = "0.2.0"
const rrModel = "claude-sonnet-4-5"

var rrFP = func() string {
	fp, _ := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "claude-cli"})
	return fp
}()

func rrPinReleasedVersion(t *testing.T) {
	t.Helper()
	orig := version
	t.Cleanup(func() { version = orig })
	version = rrReleasedVersion
}
func rrClaudeChatFingerprint(t testing.TB) string {
	fp, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "claude-cli"})
	require.NoError(t, err)
	return fp
}
func rrChatCapabilities(fp string, models ...string) []runner.ExecutionCapability {
	var out []runner.ExecutionCapability
	for _, op := range []runner.OpType{runner.OpChat, runner.OpStream} {
		out = append(out, runner.ExecutionCapability{Op: op, Provider: "claude-cli", EndpointFingerprint: fp, Families: []modelidentity.Family{"anthropic"}, Models: append([]string(nil), models...), ContextWindows: []provider.ContextWindow{provider.ContextDefault}, Efforts: []provider.Effort{provider.EffortDefault}, Tools: append([]string(nil), sources.ClaudeCodeTools...), ExecutedModel: true})
	}
	return out
}
