// Package acceptance is the named acceptance harness for the vendor-abstraction
// MVP (spec docs/specs/feature-pipeline-replacement-mvp.md §8). It contains one
// scenario per acceptance criterion #1–#7, each an end-to-end run of the
// committed native-ralph-loop worked example
// (examples/workflows/native-ralph-loop/).
//
// HERMETIC & DETERMINISTIC: there are NO live LLM calls, NO network, and NO real
// credentials anywhere in this package. Every provider-dependent path is
// exercised through the existing test SEAMS:
//
//   - claude-cli / opencode subprocesses → a fake claudecli.CommandRunner that
//     replays canned stream-json / text and records the RunSpec (args, Dir, Env).
//   - Azure AI Foundry / OpenRouter HTTP → a fake http.RoundTripper that returns
//     canned chat-completions JSON and records the request URL/headers.
//   - persistence → the real SQLite repos over an in-memory database.
//   - the connection store → the real AES-GCM-backed ConnectionRepo with a test
//     master key.
//
// The harness wires the REAL domain (engine, executors, profile resolver,
// validation) and the REAL adapters over those fake I/O seams, mirroring the
// composition root in cmd/arbitron/main.go. The worked example is loaded as
// data from disk via the real filesystem repo/reader.
//
// Two assertions are load-bearing and called out by the design (§6) and the task
// plan: the configuration-diff assertion (#4, TestAcceptance_04_*) and the
// log-capture secrecy assertion (#7, TestAcceptance_07_*).
package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/crypto"
	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/adapter/filesystem"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	domainconfig "github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/arbitron/arbitron/domain/workflow"
)

// workedExample is the committed worked-example workflow folder name. The
// filesystem repo/reader is rooted at examples/workflows so this name resolves
// to that directory.
const workedExample = "native-ralph-loop"

// examplesRoot returns the absolute path to examples/workflows from this test
// package (test/acceptance), so the filesystem repo's path-traversal guard
// accepts the workflow name regardless of the test's working directory.
func examplesRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "examples", "workflows"))
	require.NoError(t, err)
	return abs
}

// ---------------------------------------------------------------------------
// Fake subprocess runner (claude-cli + opencode seam).
// ---------------------------------------------------------------------------

// fakeRunner is a claudecli.CommandRunner that replays canned stdout lines and
// records every RunSpec it received (so a scenario can assert the threaded args,
// the cmd.Dir, and the injected env). It is the single subprocess seam shared by
// the claude-cli chat/agent adapters and the opencode adapter.
//
// canned maps a "selector" → the stdout lines to replay. The selector is matched
// against the joined args so the SAME runner can serve both the tools-enabled
// agent invocation and the tool-suppressed chat invocation deterministically.
type fakeRunner struct {
	mu sync.Mutex
	// byMatch holds (substring-of-joined-args → canned lines). The first entry
	// whose key is a substring of the joined args wins; order is unspecified, so
	// keys must be mutually exclusive across the invocations a scenario drives.
	byMatch map[string][]string
	// fallback is replayed when no byMatch key matches AND no chat done-script
	// is armed.
	fallback []string
	// chatDoneScript, when non-nil, drives the tool-suppressed claude-cli CHAT
	// (assessor) invocations: each such call pops the next `done` value (the last
	// value repeats once exhausted) and replays a canned stream-json result whose
	// text is the assessor's structured {done, notes} JSON. The claude-cli chat
	// path delivers this as raw JSON text; the engine's processLLMOutput parses it
	// into a map under the assessor's outputSchema, so `assessor.done` resolves and
	// the loop exits through the real conditional — no self-cancel guard needed.
	chatDoneScript []bool
	chatCalls      int
	// specs records every RunSpec in call order.
	specs []claudecli.RunSpec
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{byMatch: make(map[string][]string)}
}

// on registers canned stdout lines for invocations whose joined args contain
// match.
func (f *fakeRunner) on(match string, lines ...string) *fakeRunner {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byMatch[match] = lines
	return f
}

// withFallback sets the canned lines replayed when no match registered via on()
// applies.
func (f *fakeRunner) withFallback(lines ...string) *fakeRunner {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fallback = lines
	return f
}

// withChatDoneScript arms the tool-suppressed claude-cli CHAT (assessor) seam
// with a per-pass `done` script (last value repeats). Each assessor pass replays
// a canned stream-json result whose text is the assessor's structured
// {done, notes} JSON, letting the worked example exit through the real
// conditional on `assessor.done` under the Claude binding.
func (f *fakeRunner) withChatDoneScript(doneSeq ...bool) *fakeRunner {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chatDoneScript = doneSeq
	return f
}

// isChatInvocation reports whether a joined-args string is a tool-suppressed
// chat (assessor) call rather than a tools-enabled agent (editor) call. The
// agent path always carries --dangerously-skip-permissions; the chat path never
// does.
func isChatInvocation(joined string) bool {
	return !strings.Contains(joined, "--dangerously-skip-permissions")
}

func (f *fakeRunner) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	joined := strings.Join(spec.Args, " ")

	var lines []string
	// An armed chat done-script takes precedence for chat (assessor) calls.
	if f.chatDoneScript != nil && isChatInvocation(joined) {
		idx := f.chatCalls
		f.chatCalls++
		if idx >= len(f.chatDoneScript) {
			idx = len(f.chatDoneScript) - 1
		}
		done := idx >= 0 && f.chatDoneScript[idx]
		lines = claudeAssessorLines(done)
	} else {
		lines = f.fallback
		for match, canned := range f.byMatch {
			if strings.Contains(joined, match) {
				lines = canned
				break
			}
		}
	}
	f.mu.Unlock()

	ch := make(chan string)
	go func() {
		defer close(ch)
		for _, l := range lines {
			ch <- l
		}
	}()
	return ch, func() error { return nil }, nil
}

// recordedSpecs returns a copy of every RunSpec seen so far.
func (f *fakeRunner) recordedSpecs() []claudecli.RunSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]claudecli.RunSpec, len(f.specs))
	copy(out, f.specs)
	return out
}

// specsMatching returns every recorded spec whose joined args contain match.
func (f *fakeRunner) specsMatching(match string) []claudecli.RunSpec {
	var out []claudecli.RunSpec
	for _, s := range f.recordedSpecs() {
		if strings.Contains(strings.Join(s.Args, " "), match) {
			out = append(out, s)
		}
	}
	return out
}

// chatInvocationSpecs returns the recorded tool-suppressed CHAT (assessor)
// invocations — every spec that is not a tools-enabled agent call.
func chatInvocationSpecs(f *fakeRunner) []claudecli.RunSpec {
	var out []claudecli.RunSpec
	for _, s := range f.recordedSpecs() {
		if isChatInvocation(strings.Join(s.Args, " ")) {
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Fake HTTP transport (openrouter + azurefoundry chat seam).
// ---------------------------------------------------------------------------

// recordedRequest captures the salient parts of an outbound HTTP request so a
// scenario can assert which endpoint/credential a node resolved to without ever
// touching the network.
type recordedRequest struct {
	URL    string
	Auth   string // Authorization header
	APIKey string // api-key header (Azure)
}

// fakeTransport is an http.RoundTripper that returns a canned chat-completions
// response (structured {done, notes} JSON content) and records each request's
// URL + auth headers. It is the network seam for the Azure/OpenRouter chat
// adapters: no real socket is ever opened.
type fakeTransport struct {
	mu       sync.Mutex
	requests []recordedRequest
	// doneScript is the per-call sequence of `done` booleans the canned assessor
	// response reports. The last entry repeats once exhausted, so a single
	// false→…→true script controls exactly how many loop passes occur before the
	// `assessor.done` exit fires.
	doneScript []bool
	calls      int
}

// newFakeTransport reports the given done value on every call.
func newFakeTransport(done bool) *fakeTransport {
	return &fakeTransport{doneScript: []bool{done}}
}

// newScriptedTransport reports done per the given script (last value repeats).
func newScriptedTransport(doneSeq ...bool) *fakeTransport {
	return &fakeTransport{doneScript: doneSeq}
}

// nextDone returns the done value for the current call and advances the script.
func (t *fakeTransport) nextDone() bool {
	idx := t.calls
	t.calls++
	if idx >= len(t.doneScript) {
		idx = len(t.doneScript) - 1
	}
	if idx < 0 {
		return false
	}
	return t.doneScript[idx]
}

func (t *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.requests = append(t.requests, recordedRequest{
		URL:    req.URL.String(),
		Auth:   req.Header.Get("Authorization"),
		APIKey: req.Header.Get("api-key"),
	})
	done := t.nextDone()
	t.mu.Unlock()

	// The assessor declares an outputSchema, so the chat adapters parse the
	// assistant content as JSON. Emit a structured {done, notes} object as the
	// message content (itself a JSON string, OpenAI/Azure chat-completions shape).
	content, _ := json.Marshal(map[string]any{
		"done":  done,
		"notes": "assessment via hermetic fake transport",
	})
	body, _ := json.Marshal(map[string]any{
		"model": "fake-model",
		"choices": []map[string]any{
			{"message": map[string]any{"role": "assistant", "content": string(content)}},
		},
		"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 2},
	})
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       newBody(body),
		Request:    req,
	}, nil
}

func (t *fakeTransport) recorded() []recordedRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]recordedRequest, len(t.requests))
	copy(out, t.requests)
	return out
}

// newBody wraps bytes as an io.ReadCloser for an http.Response.
func newBody(b []byte) *readCloser { return &readCloser{Reader: bytes.NewReader(b)} }

type readCloser struct{ *bytes.Reader }

func (readCloser) Close() error { return nil }

// ---------------------------------------------------------------------------
// Canned stream-json for the claude-cli subprocess seam.
// ---------------------------------------------------------------------------

// claudeAgentLines is a canned stream-json transcript for a tools-enabled agent
// run: an init event, an assistant turn, a tool-use (user) turn, and a terminal
// result. AgentAdapter folds these into AgentResult.Steps + Summary.
var claudeAgentLines = []string{
	`{"type":"system","subtype":"init","session_id":"agent-1"}`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"Editing the top focus area."}]}}`,
	`{"type":"user","message":{"content":[{"type":"tool_result","content":"applied edit"}]}}`,
	`{"type":"result","is_error":false,"result":"Edited the workspace toward the focus area.","session_id":"agent-1","usage":{"input_tokens":11,"output_tokens":5}}`,
}

// claudeAssessorLines is a canned stream-json transcript for the tool-suppressed
// chat assessor. The result text is a JSON object string; the claude-cli
// ChatAdapter returns it verbatim (claude-cli delivers structured output as raw
// JSON text). Under the assessor's outputSchema the engine's processLLMOutput
// now parses that raw text into a map (provider-agnostic structured output), so
// `assessor.done` resolves and the Claude binding exits through the real
// conditional on done — just like the HTTP chat backends.
func claudeAssessorLines(done bool) []string {
	content := `{\"done\":false,\"notes\":\"more work remains\"}`
	if done {
		content = `{\"done\":true,\"notes\":\"focus areas satisfied\"}`
	}
	return []string{
		`{"type":"system","subtype":"init","session_id":"chat-1"}`,
		`{"type":"result","is_error":false,"result":"` + content + `","session_id":"chat-1","usage":{"input_tokens":4,"output_tokens":2}}`,
	}
}

// ---------------------------------------------------------------------------
// Test crypto / connection store.
// ---------------------------------------------------------------------------

// testMasterKey is a fixed 32-byte master key for the AES-GCM connection crypto.
// It exists only in-process for the hermetic store; it authenticates no real
// endpoint.
var testMasterKey = bytes.Repeat([]byte{0x2a}, 32)

// ---------------------------------------------------------------------------
// Binding: the profile/connection registry + provider Deps for one run.
// ---------------------------------------------------------------------------

// binding fully describes one configuration the worked example runs under: the
// profile registry (alias→profile), the connections to seed (name→conn+key),
// and the fake I/O seams the adapters use. The same workflow.json graph runs
// under any binding; only these external bindings differ (#4).
type binding struct {
	name        string
	profiles    []provider.Profile
	connections []seedConn
	runner      *fakeRunner
	transport   *fakeTransport
}

// seedConn is a connection plus its plaintext key to seal into the store.
type seedConn struct {
	conn provider.Connection
	key  string
}

// claudeBinding is the Claude-subscription configuration: editor=agent and
// assessor=chat, both with an empty ConnectionRef (the no-key ambient-login
// path). No connection or key is required.
func claudeBinding(runner *fakeRunner) binding {
	return binding{
		name: "claude",
		profiles: []provider.Profile{
			{Alias: "editor", Kind: provider.ProfileKindAgent, ConnectionRef: "", Model: "claude-sonnet-4"},
			{Alias: "assessor", Kind: provider.ProfileKindChat, ConnectionRef: "", Model: "claude-sonnet-4"},
		},
		runner: runner,
	}
}

// Sentinel keys used across the secrecy scenarios. They authenticate no real
// endpoint; they exist only to be traced through the connection store, the
// subprocess env, and the log buffer.
const (
	openrouterKey = "sk-SENTINEL-openrouter-DO-NOT-LOG"
	opencodeKey   = "sk-SENTINEL-opencode-DO-NOT-LOG"
)

// openWeightBinding is the open-weight configuration: the SAME aliases
// (editor=agent, assessor=chat) now backed by connections — editor via opencode
// (connection-backed key env-injected at launch) and assessor via an OpenRouter
// HTTP chat endpoint. The assessor transport reports done=true so the loop exits
// on `assessor.done` (native HTTP chat adapters parse structured output to a map).
func openWeightBinding(runner *fakeRunner, transport *fakeTransport) binding {
	return binding{
		name: "open-weight",
		profiles: []provider.Profile{
			{Alias: "editor", Kind: provider.ProfileKindAgent, ConnectionRef: "opencode-openweight", Model: "qwen/qwen3-coder"},
			{Alias: "assessor", Kind: provider.ProfileKindChat, ConnectionRef: "openrouter-chat", Model: "deepseek/deepseek-r1"},
		},
		connections: []seedConn{
			{
				conn: provider.Connection{Name: "opencode-openweight", Provider: "openrouter", BaseURL: "https://openweight.example/v1"},
				key:  opencodeKey,
			},
			{
				conn: provider.Connection{Name: "openrouter-chat", Provider: "openrouter", BaseURL: "https://openrouter.example/api/v1"},
				key:  openrouterKey,
			},
		},
		runner:    runner,
		transport: transport,
	}
}

// ---------------------------------------------------------------------------
// Harness: real domain + real adapters over fake I/O seams.
// ---------------------------------------------------------------------------

// harness holds the wired engine and the seams a scenario inspects.
type harness struct {
	engine      *engine.Engine
	instances   *adaptsqlite.InstanceRepo
	nodeExecs   *adaptsqlite.NodeExecutionRepo
	snapshots   *adaptsqlite.VariableSnapshotRepo
	resolver    *providersadapter.StaticProfileResolver
	connChecker *providersadapter.ConnChecker
	fileReader  *filesystem.FileReaderAdapter
	defaultUser uuid.UUID
	logBuf      *bytes.Buffer
	binding     binding
}

// fileChecker returns the owner-bound workflow.FileChecker for this harness's
// default user, used by validation scenarios (the FileReader is owner-aware).
func (h *harness) fileChecker() workflow.FileChecker {
	return harnessFileChecker{fr: h.fileReader, owner: h.defaultUser}
}

type harnessFileChecker struct {
	fr    *filesystem.FileReaderAdapter
	owner uuid.UUID
}

func (c harnessFileChecker) FileExists(workflowName, relativePath string) bool {
	return c.fr.FileExists(c.owner, workflowName, relativePath)
}

// newHarness wires the full stack for one binding. It installs a buffer-backed
// *slog.Logger as the process default for the #7 log-capture secrecy assertion,
// restoring the previous default on cleanup.
func newHarness(t *testing.T, b binding) *harness {
	t.Helper()

	// Buffer-backed logger installed everywhere via slog.Default() so the #7
	// secrecy assertion can scan ALL log output produced during resolve-and-run.
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Real SQLite over an in-memory DB (hermetic persistence).
	db, err := adaptsqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))

	instances := adaptsqlite.NewInstanceRepo(db)
	nodeExecs := adaptsqlite.NewNodeExecutionRepo(db)
	snapshots := adaptsqlite.NewVariableSnapshotRepo(db)
	users := adaptsqlite.NewUserRepo(db)
	queueRepo := adaptsqlite.NewQueueRepo(db)

	// Real AES-GCM crypto + real connection store; seal each binding's keys.
	cryptoImpl, err := crypto.NewAESGCM(testMasterKey)
	require.NoError(t, err)
	connStore := adaptsqlite.NewConnectionRepo(db, cryptoImpl)
	for _, sc := range b.connections {
		require.NoError(t, connStore.Create(context.Background(), sc.conn, sc.key))
	}

	// Real profile resolver over the real connection store.
	resolver, err := providersadapter.NewStaticProfileResolver(b.profiles, connStore)
	require.NoError(t, err)
	connChecker := providersadapter.NewConnChecker(b.profiles, connStore)

	// Real filesystem repo + reader. The worked example is committed flat under
	// examples/workflows; the scope-aware repo expects per-user partitions, so
	// copy it into a temp basePath under the default user's private workspace
	// (the owner the engine resolves for background/system triggers).
	defaultUser, err := users.GetDefaultUser(context.Background())
	require.NoError(t, err)
	root := t.TempDir()
	copyDirTree(t, filepath.Join(examplesRoot(t), workedExample),
		filepath.Join(root, "users", defaultUser.String(), workedExample))
	workflowRepo := filesystem.NewWorkflowRepo(root)
	fileReader := filesystem.NewFileReader(root)
	wsMgr, err := filesystem.NewWorkspaceManager(t.TempDir())
	require.NoError(t, err)

	// Real claude-cli chat/agent adapters over the fake runner. WorkspaceRoot is
	// left empty (test convenience) so the pin check is disabled for the temp
	// workspace.
	runner := b.runner
	claudeChat := claudecli.NewChatAdapter(claudecli.Config{BinaryPath: "claude", Runner: runner})
	claudeAgent := claudecli.NewAgentAdapter(claudecli.Config{
		BinaryPath:   "claude",
		Runner:       runner,
		AllowedTools: []string{"Read", "Write", "Edit", "Bash"},
	})

	// Real provider factory: claude-cli pre-wired, opencode over the same fake
	// runner, Azure/OpenRouter over the fake HTTP transport.
	var httpClient *http.Client
	if b.transport != nil {
		httpClient = &http.Client{Transport: b.transport}
	}
	factory := providersadapter.NewFactory(providersadapter.Deps{
		ClaudeChat:         claudeChat,
		ClaudeAgent:        claudeAgent,
		HTTPClient:         httpClient,
		Runner:             runner,
		OpencodeBinaryPath: "opencode",
	})

	// Real executors: agent_call + llm_call over the factory/resolver; real
	// transform + conditional.
	executors := map[workflow.NodeType]engine.NodeExecutor{
		workflow.NodeTypeAgentCall:   engine.NewAgentCallExecutor(fileReader, factory, resolver),
		workflow.NodeTypeLLMCall:     engine.NewLLMCallExecutor(fileReader, factory, resolver),
		workflow.NodeTypeTransform:   engine.NewTransformExecutor(),
		workflow.NodeTypeConditional: engine.NewConditionalExecutor(),
	}

	bus := eventbus.NewNoOp()
	eng := engine.NewEngine(
		workflowRepo, instances, nodeExecs, snapshots, users,
		executors, bus, nil, queueRepo,
	)
	eng.SetWorkspaceManager(wsMgr)

	// Real queue manager (GlobalCap=0 → unlimited) wired to the engine.
	cfg := &domainconfig.Config{}
	queueMgr := queue.NewManager(queueRepo, instances, cfg, eng)
	eng.SetQueueManager(queueMgr)

	return &harness{
		engine:      eng,
		instances:   instances,
		nodeExecs:   nodeExecs,
		snapshots:   snapshots,
		resolver:    resolver,
		connChecker: connChecker,
		fileReader:  fileReader,
		defaultUser: defaultUser,
		logBuf:      &logBuf,
		binding:     b,
	}
}

// runResult is the outcome of running the worked example to a terminal state.
type runResult struct {
	instance *instance.Instance
}

// trigger starts the worked example under this harness's binding and returns the
// instance id without waiting.
func (h *harness) trigger(t *testing.T, maxIterations float64) uuid.UUID {
	t.Helper()
	id, _, err := h.engine.Trigger(workedExample, map[string]any{
		"mission":        "tidy the workspace tracking docs",
		"max_iterations": maxIterations,
	}, "normal")
	require.NoError(t, err)
	return id
}

// runWorkedExample triggers the worked example under this harness's binding and
// waits for it to reach a terminal (completed/failed/cancelled) state. It is the
// shared driver for the success scenarios.
func (h *harness) runWorkedExample(t *testing.T, maxIterations float64) runResult {
	t.Helper()
	id := h.trigger(t, maxIterations)
	inst := h.waitForTerminal(t, id, 10*time.Second)
	return runResult{instance: inst}
}

// waitForTerminal polls until the instance reaches a terminal state or the
// timeout elapses.
func (h *harness) waitForTerminal(t *testing.T, id uuid.UUID, timeout time.Duration) *instance.Instance {
	t.Helper()
	ctx := context.Background()
	deadline := time.After(timeout)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			inst, _ := h.instances.Get(ctx, id)
			if inst != nil {
				t.Fatalf("timed out waiting for instance %s to terminate; state=%s", id, inst.State)
			}
			t.Fatalf("timed out waiting for instance %s; not found", id)
		case <-tick.C:
			inst, err := h.instances.Get(ctx, id)
			if err != nil {
				continue
			}
			switch inst.State {
			case instance.StateCompleted, instance.StateFailed, instance.StateCancelled:
				return inst
			}
		}
	}
}

// loadWorkedExampleGraph loads the worked example via the real filesystem repo
// (the same definition the engine snapshots and runs). The example is copied
// into a temp basePath under a private partition for the scope-aware repo.
func loadWorkedExampleGraph(t *testing.T) *workflow.Workflow {
	t.Helper()
	owner := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	root := t.TempDir()
	copyDirTree(t, filepath.Join(examplesRoot(t), workedExample),
		filepath.Join(root, "users", owner.String(), workedExample))
	repo := filesystem.NewWorkflowRepo(root)
	wf, err := repo.Load(context.Background(), workflow.Private(owner), workedExample)
	require.NoError(t, err)
	return wf
}

// copyDirTree recursively copies src into dst.
func copyDirTree(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dst, 0o755))
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyDirTree(t, s, d)
			continue
		}
		data, err := os.ReadFile(s)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(d, data, 0o644))
	}
}
