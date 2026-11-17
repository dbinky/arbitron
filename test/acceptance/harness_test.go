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
	"fmt"
	"io"
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
	"github.com/arbitron/arbitron/domain/modelidentity"
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
	// opencodeModel is the -m value of the latest `opencode run`, echoed by the
	// following `opencode export`.
	opencodeModel string
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
	// The modern opencode adapter probes `--version` once before its first run;
	// answer with a compatible version so the probe passes hermetically.
	if strings.Contains(joined, "--version") {
		f.mu.Unlock()
		ch := make(chan string, 1)
		ch <- "opencode 99.0.0"
		close(ch)
		return ch, func() error { return nil }, nil
	}
	// opencode run/export: report the session the way the real binary does, with
	// the provider and model it was actually asked to run (taken from -m), so a
	// pinned run's execution evidence is the fake's honest echo, not a bypass.
	if len(spec.Args) > 0 && (spec.Args[0] == "export" || spec.Args[0] == "run") {
		if out, ok := f.opencodeLines(spec.Args); ok {
			f.mu.Unlock()
			ch := make(chan string, len(out))
			for _, l := range out {
				ch <- l
			}
			close(ch)
			return ch, func() error { return nil }, nil
		}
	}
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
	writeCodexRollout(spec, lines)

	ch := make(chan string)
	go func() {
		defer close(ch)
		for _, l := range lines {
			ch <- l
		}
	}()
	return ch, func() error { return nil }, nil
}

// writeCodexRollout stores the session rollout a real `codex exec` leaves under
// $CODEX_HOME, naming the thread the stream announced and the model the run was
// started with (-m), which is the evidence a pinned run is verified against. It
// writes nothing unless the launch env carries an explicit CODEX_HOME.
func writeCodexRollout(spec claudecli.RunSpec, lines []string) {
	if len(spec.Args) == 0 || spec.Args[0] != "exec" {
		return
	}
	var home, model, thread string
	for _, kv := range spec.Env {
		if v, ok := strings.CutPrefix(kv, "CODEX_HOME="); ok {
			home = v
		}
	}
	for i, a := range spec.Args {
		if a == "-m" && i+1 < len(spec.Args) {
			model = spec.Args[i+1]
		}
	}
	for _, l := range lines {
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if json.Unmarshal([]byte(l), &ev) == nil && ev.Type == "thread.started" {
			thread = ev.ThreadID
		}
	}
	if home == "" || model == "" || thread == "" {
		return
	}
	dir := filepath.Join(home, "sessions", "2026", "01", "01")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	meta, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": thread}})
	turn, _ := json.Marshal(map[string]any{"type": "turn_context", "payload": map[string]any{"model": model}})
	_ = os.WriteFile(filepath.Join(dir, "rollout-2026-01-01T00-00-00-"+thread+".jsonl"), []byte(string(meta)+"\n"+string(turn)+"\n"), 0o644)
}

// opencodeLines answers an `opencode run -m provider/model` with a completed
// assistant step and the follow-up `opencode export <session>` with the matching
// session envelope. Caller holds f.mu.
func (f *fakeRunner) opencodeLines(args []string) ([]string, bool) {
	const sessionID = "ses_acceptance"
	const messageID = "msg_acceptance"
	switch args[0] {
	case "run":
		for i, a := range args {
			if a == "-m" && i+1 < len(args) {
				f.opencodeModel = args[i+1]
				step := func(kind, extra string) string {
					return `{"type":"` + kind + `","sessionID":"` + sessionID + `","part":{"sessionID":"` + sessionID +
						`","messageID":"` + messageID + `"` + extra + `}}`
				}
				return []string{
					step("step_start", ""),
					step("text", `,"type":"text","text":"Editing the workspace."`),
					step("step_finish", `,"tokens":{"input":3,"output":2}`),
				}, true
			}
		}
	case "export":
		providerID, modelID, _ := strings.Cut(f.opencodeModel, "/")
		env, _ := json.Marshal(map[string]any{
			"info": map[string]any{"id": sessionID},
			"messages": []map[string]any{{"info": map[string]any{
				"id": messageID, "sessionID": sessionID, "role": "assistant",
				"providerID": providerID, "modelID": modelID,
				"time": map[string]any{"completed": 1},
			}}},
		})
		return []string{string(env)}, true
	}
	return nil, false
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
	URL     string
	Auth    string // Authorization header (Bearer, OpenAI/OpenRouter)
	APIKey  string // api-key header (Azure)
	XAPIKey string // x-api-key header (Anthropic Messages API)
	Model   string // model named in the request body
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
	// onRequest, when set, runs after the request is recorded and before the
	// reply is produced; n is the 1-based request number. A scenario uses it to
	// change the world while a run is mid-flight.
	onRequest func(n int)
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
	// A pinned run only accepts a response that names the model it asked for, so
	// the canned reply echoes the requested model like a real endpoint does.
	model := "fake-model"
	if req.Body != nil {
		var sent struct {
			Model string `json:"model"`
		}
		if raw, err := io.ReadAll(req.Body); err == nil && json.Unmarshal(raw, &sent) == nil && sent.Model != "" {
			model = sent.Model
		}
	}

	t.mu.Lock()
	t.requests = append(t.requests, recordedRequest{
		URL:     req.URL.String(),
		Auth:    req.Header.Get("Authorization"),
		APIKey:  req.Header.Get("api-key"),
		XAPIKey: req.Header.Get("x-api-key"),
		Model:   model,
	})
	n := len(t.requests)
	done := t.nextDone()
	hook := t.onRequest
	t.mu.Unlock()
	if hook != nil {
		hook(n)
	}

	// The assessor declares an outputSchema, so the chat adapters parse the
	// assistant content as JSON. Emit a structured {done, notes} object as the
	// message content (itself a JSON string).
	content, _ := json.Marshal(map[string]any{
		"done":  done,
		"notes": "assessment via hermetic fake transport",
	})

	// Shape the canned body by the adapter dialect inferred from the request
	// path: the Anthropic Messages API (anthropicapi adapter) POSTs to
	// {base-url}/v1/messages and expects a `content`-block response; the
	// OpenAI-compatible adapters (openrouter, azurefoundry, openai/ollama) POST to
	// {base-url}/chat/completions and expect a `choices` response. Existing
	// scenarios only ever hit /chat/completions, so this branch is additive.
	var body []byte
	if strings.Contains(req.URL.Path, "/messages") {
		body, _ = json.Marshal(map[string]any{
			"type":    "message",
			"role":    "assistant",
			"model":   model,
			"content": []map[string]any{{"type": "text", "text": string(content)}},
			"usage":   map[string]any{"input_tokens": 5, "output_tokens": 2},
		})
	} else {
		body, _ = json.Marshal(map[string]any{
			"model": model,
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": string(content)}},
			},
			"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 2},
		})
	}
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
	// workflowName overrides the example folder the harness copies + runs. Empty
	// means the default worked example (native-ralph-loop); a scenario that
	// exercises a different committed example (e.g. mixed-chat-providers) sets it.
	workflowName string
}

// exampleName returns the example workflow folder this binding runs (defaulting
// to the worked example when unset).
func (b binding) exampleName() string {
	if b.workflowName == "" {
		return workedExample
	}
	return b.workflowName
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
			{Alias: "editor", Kind: provider.ProfileKindAgent, ConnectionRef: "", Model: "claude-sonnet-4", Backend: "claude-code"},
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

// codexEditorLines is a canned codex-cli `codex exec --json` JSONL transcript for
// the editor agent run: a thread start, a command tool call, a terminal
// agent_message, and a turn.completed carrying usage. The codex AgentProvider
// folds these into AgentResult.Summary + Steps.
var codexEditorLines = []string{
	`{"type":"thread.started","thread_id":"codex-thread-1"}`,
	`{"type":"turn.started"}`,
	`{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"sed -i edit focus-areas.md","status":"completed"}}`,
	`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Edited the workspace toward the focus area."}}`,
	`{"type":"turn.completed","usage":{"input_tokens":11,"output_tokens":5}}`,
}

// codexBinding is the codex-subscription configuration: editor=agent on the
// codex backend (subscription-only — empty ConnectionRef, no key, like the
// Claude no-key path) and assessor=chat over an OpenRouter HTTP endpoint that
// reports done so the loop exits on `assessor.done`. It proves the THIRD agent
// backend (codex) runs an agent_call mission through the shared fake runner with
// NO API key configured.
func codexBinding(runner *fakeRunner, transport *fakeTransport) binding {
	return binding{
		name: "codex",
		profiles: []provider.Profile{
			{Alias: "editor", Kind: provider.ProfileKindAgent, ConnectionRef: "", Model: "gpt-6.1-sol", Backend: "codex"},
			{Alias: "assessor", Kind: provider.ProfileKindChat, ConnectionRef: "openrouter-chat", Model: "deepseek/deepseek-v4-flash"},
		},
		connections: []seedConn{
			{
				conn: provider.Connection{Name: "openrouter-chat", Provider: "openrouter", BaseURL: "https://openrouter.example/api/v1"},
				key:  openrouterKey,
			},
		},
		runner:    runner,
		transport: transport,
	}
}

// openWeightBinding is the open-weight configuration: the SAME aliases
// (editor=agent, assessor=chat) now backed by connections — editor via opencode
// (connection-backed key env-injected at launch) and assessor via an OpenRouter
// HTTP chat endpoint. The assessor transport reports done=true so the loop exits
// on `assessor.done` (native HTTP chat adapters parse structured output to a map).
func openWeightBinding(runner *fakeRunner, transport *fakeTransport) binding {
	return binding{
		name: "open-weight",
		profiles: []provider.Profile{
			{Alias: "editor", Kind: provider.ProfileKindAgent, ConnectionRef: "opencode-openweight", Model: "deepseek/deepseek-v4-pro", Backend: "opencode"},
			{Alias: "assessor", Kind: provider.ProfileKindChat, ConnectionRef: "openrouter-chat", Model: "deepseek/deepseek-v4-flash"},
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
	resolver    *providersadapter.DynamicProfileResolver
	connChecker *providersadapter.ConnChecker
	fileReader  *filesystem.FileReaderAdapter
	defaultUser uuid.UUID
	logBuf      *bytes.Buffer
	binding     binding

	profileStore *editableProfileStore
	profileCache *providersadapter.ProfileCache
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

	// Real DB-backed profile resolver over the real connection store. The cache
	// is built from this binding's fixed profile set (the acceptance analogue of
	// the boot-time seed).
	profileStore := newEditableProfileStore(b.profiles)
	profileCache := providersadapter.NewProfileCache(profileStore)
	require.NoError(t, profileCache.Load(context.Background()))
	resolver := providersadapter.NewDynamicProfileResolver(profileCache, connStore)
	connChecker := providersadapter.NewConnChecker(b.profiles, connStore)

	// Real filesystem repo + reader. The worked example is committed flat under
	// examples/workflows; the scope-aware repo expects per-user partitions, so
	// copy it into a temp basePath under the default user's private workspace
	// (the owner the engine resolves for background/system triggers).
	defaultUser, err := users.GetDefaultUser(context.Background())
	require.NoError(t, err)
	root := t.TempDir()
	example := b.exampleName()
	copyDirTree(t, filepath.Join(examplesRoot(t), example),
		filepath.Join(root, "users", defaultUser.String(), example))
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
		CodexBinaryPath:    "codex",
		// Mirror the composition root's open-by-default agent posture (config
		// llm.agentallowalltools defaults to true) so the opencode agent path runs
		// as it does in production.
		OpencodeAllowAllTools: true,
	})

	// Real pin path, as the composition root builds it (cmd/arbitron/main.go):
	// the batch executors resolve through PinningResolver.ForRun, so a run keeps
	// the models pinned at trigger however its profiles are edited afterwards.
	pinningResolver := provider.NewPinningResolver(
		resolver, providersadapter.NewPinLookup(instances), engine.InstanceIDFromContext)
	runResolver := pinningResolver.ForRun()

	// Real executors: agent_call + llm_call over the factory/pinning resolver;
	// real transform + conditional.
	executors := map[workflow.NodeType]engine.NodeExecutor{
		workflow.NodeTypeAgentCall:   engine.NewAgentCallExecutor(fileReader, factory, runResolver, "claude-code"),
		workflow.NodeTypeLLMCall:     engine.NewLLMCallExecutor(fileReader, factory, runResolver),
		workflow.NodeTypeTransform:   engine.NewTransformExecutor(),
		workflow.NodeTypeConditional: engine.NewConditionalExecutor(),
	}

	bus := eventbus.NewNoOp()
	eng := engine.NewEngine(
		workflowRepo, instances, nodeExecs, snapshots, users,
		executors, bus, nil, queueRepo,
		"",
	)
	eng.SetWorkspaceManager(wsMgr)
	// Trigger requires the pin collaborators: the real snapshotter over the real
	// cache, connection store and route inventory, and a real execution gate. Only
	// the version grammar is fixture-supplied (the fixtures' models
	// gpt-4o, ..., carry no provable fixed version); every route is still checked.
	eng.SetProfileResolver(pinningResolver)
	inventory, err := providersadapter.NewExecutionPathInventory(providersadapter.InventoryDeps{
		Profiles:    profileStore,
		Connections: connStore,
		Workflows:   workflowRepo,
		ActiveRuns:  instances,
		Policy:      fixturePolicy{},
		Fingerprint: func(b []byte) string { return fmt.Sprintf("%x", b) },
		Harness:     func(string) ([]byte, error) { return nil, nil },
		Factory:     factory,
		Interactive: providersadapter.NewInteractiveStreamSelector(factory, nil, "claude-code"),

		DefaultAgentBackend: "claude-code",
	})
	require.NoError(t, err)
	eng.SetModelSnapshotter(providersadapter.NewModelSnapshotter(profileCache, connStore, fixtureVersions{}, inventory, nil))
	eng.SetModelPinGate(provider.NewExecutionGate())

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

		profileStore: profileStore,
		profileCache: profileCache,
	}
}

// fixtureVersions accepts every fixture model as its own fixed version. The
// grammar is proved in domain/modelidentity; here only the route set matters.
type fixtureVersions struct{}

func (fixtureVersions) SpecificVersion(_ context.Context, req provider.VersionRequest) (string, error) {
	if req.Model == "" {
		return "", fmt.Errorf("acceptance fixture: empty model")
	}
	return req.Model, nil
}

// fixturePolicy is an empty identity policy; pin capture never reads it.
type fixturePolicy struct{}

func (fixturePolicy) Snapshot(context.Context) (modelidentity.IdentityPolicy, error) {
	return modelidentity.IdentityPolicy{Revision: 1}, nil
}

// editableProfileStore is an in-memory ProfileStore whose profiles a scenario
// can edit after a run has been triggered.
type editableProfileStore struct {
	mu       sync.Mutex
	profiles []provider.Profile
}

func newEditableProfileStore(profiles []provider.Profile) *editableProfileStore {
	s := &editableProfileStore{}
	for _, p := range profiles {
		s.profiles = append(s.profiles, p.Clone())
	}
	return s
}

func (s *editableProfileStore) Create(context.Context, provider.Profile) error {
	return provider.ErrReadOnlyStore
}
func (s *editableProfileStore) Delete(context.Context, string) error {
	return provider.ErrReadOnlyStore
}

func (s *editableProfileStore) Update(_ context.Context, p provider.Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.profiles {
		if s.profiles[i].Alias == p.Alias {
			s.profiles[i] = p.Clone()
			return nil
		}
	}
	return provider.ErrProfileNotFound
}

func (s *editableProfileStore) Get(_ context.Context, alias string) (provider.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.profiles {
		if p.Alias == alias {
			return p.Clone(), nil
		}
	}
	return provider.Profile{}, provider.ErrProfileNotFound
}

func (s *editableProfileStore) List(context.Context) ([]provider.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]provider.Profile, len(s.profiles))
	for i, p := range s.profiles {
		out[i] = p.Clone()
	}
	return out, nil
}

func (s *editableProfileStore) ListInternal(ctx context.Context) ([]provider.Profile, error) {
	return s.List(ctx)
}

func (s *editableProfileStore) Count(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.profiles), nil
}

// editProfileModel changes a profile's model and recaches it live, the way an
// admin edit does: runs triggered earlier must keep their pinned model.
func (h *harness) editProfileModel(t *testing.T, alias, model string) {
	t.Helper()
	ctx := context.Background()
	p, err := h.profileStore.Get(ctx, alias)
	require.NoError(t, err)
	p.Model = model
	require.NoError(t, h.profileStore.Update(ctx, p))
	require.NoError(t, h.profileCache.Reload(ctx))
}

// runResult is the outcome of running the worked example to a terminal state.
type runResult struct {
	instance *instance.Instance
}

// trigger starts the worked example under this harness's binding and returns the
// instance id without waiting.
func (h *harness) trigger(t *testing.T, maxIterations float64) uuid.UUID {
	t.Helper()
	id, _, err := h.engine.Trigger(context.Background(), workedExample, map[string]any{
		"mission":        "tidy the workspace tracking docs",
		"max_iterations": maxIterations,
	}, "normal")
	require.NoError(t, err)
	return id
}

// runExample triggers this harness's bound example workflow with the given
// inputs and waits for it to reach a terminal state. It is the generic driver
// for scenarios that run a committed example other than the native-ralph-loop
// worked example (e.g. mixed-chat-providers), so they are not tied to the
// loop-specific mission/max_iterations inputs.
func (h *harness) runExample(t *testing.T, inputs map[string]any) runResult {
	t.Helper()
	id, _, err := h.engine.Trigger(context.Background(), h.binding.exampleName(), inputs, "normal")
	require.NoError(t, err)
	inst := h.waitForTerminal(t, id, 10*time.Second)
	return runResult{instance: inst}
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
				if inst.State == instance.StateFailed {
					nodes, _ := h.nodeExecs.ListByInstance(ctx, id)
					for _, n := range nodes {
						if n.Status == instance.NodeStatusFailed {
							t.Logf("failed node %s: %v", n.NodeID, n.Error)
						}
					}
				}
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
