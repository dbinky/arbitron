# claude-cli LLM Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement `claude -p` as arbitron's first working LLM provider, including engine-managed per-instance workspaces and an interactive layer reworked for CLI session resumption.

**Architecture:** A new `adapter/claudecli` package shells out to the `claude` CLI (`--print --output-format stream-json`) and implements `engine.LLMProvider`. The engine creates a per-instance workspace directory that agent nodes use as their working directory. The interactive/conversation layer is reworked from Messages-API `complete_session` tool semantics to `claude -p` session resumption with a sentinel-block completion signal.

**Tech Stack:** Go, testify (`github.com/stretchr/testify`), koanf, `os/exec`, the `claude` CLI.

**Spec:** `docs/superpowers/specs/2026-05-21-claude-cli-llm-provider-design.md`

---

## File Structure

**New files:**
- `adapter/claudecli/streamjson.go` — pure stream-json event parser.
- `adapter/claudecli/streamjson_test.go`
- `adapter/claudecli/runner.go` — `CommandRunner` interface + `os/exec` implementation.
- `adapter/claudecli/runner_test.go`
- `adapter/claudecli/claude.go` — binary resolution, arg building, env.
- `adapter/claudecli/claude_test.go`
- `adapter/claudecli/provider.go` — `Provider` (implements `engine.LLMProvider`).
- `adapter/claudecli/provider_test.go`
- `adapter/claudecli/fake_runner_test.go` — shared fake `CommandRunner` for tests.
- `adapter/filesystem/workspace.go` — `WorkspaceManager` filesystem adapter.
- `adapter/filesystem/workspace_test.go`
- `domain/engine/sentinel.go` — interactive completion sentinel constants + parser.
- `domain/engine/sentinel_test.go`

**Modified files:**
- `domain/engine/ports.go` — add port fields + `WorkspaceManager` interface.
- `domain/engine/engine.go` — workspace lifecycle in `run()`; `SetWorkspaceManager`.
- `domain/engine/executor_llm.go` — set `LLMRequest.WorkDir` from `{{workspace}}`.
- `domain/engine/executor_interactive.go` — session-id capture; drop tools.
- `domain/engine/conversation.go` — resume via session id; sentinel completion.
- `domain/workflow/validation.go` — reject reserved input name `workspace`.
- `domain/config/config.go` — new `LLMConfig` fields.
- `adapter/config/loader.go` — defaults for new config fields.
- `cmd/arbitron/main.go` — construct `claudecli.Provider` + `WorkspaceManager`.

---

## Part A — Port fields

### Task 1: Add LLM port fields

**Files:**
- Modify: `domain/engine/ports.go`

These are additive struct fields with no behavior of their own; they are exercised by later tasks. Verified by compilation.

- [ ] **Step 1: Add the fields**

In `domain/engine/ports.go`, add to `LLMRequest`:

```go
	// WorkDir is the working directory for subprocess-backed providers
	// (e.g. claude-cli). Empty means the provider's default.
	WorkDir string
	// SessionID, when non-empty, resumes an existing provider session
	// (claude-cli: `--resume`).
	SessionID string
```

Add to `LLMResponse`:

```go
	// SessionID identifies the provider session for resumption. Empty for
	// providers without session support.
	SessionID string
```

Add to `LLMStreamChunk`:

```go
	// SessionID is set on the terminal chunk for providers with session
	// support, so callers can persist it for resumption.
	SessionID string
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./...`
Expected: success, no output.

- [ ] **Step 3: Commit**

```bash
git add domain/engine/ports.go
git commit -m "feat: add WorkDir/SessionID fields to LLM provider port"
```

---

## Part B — claude-cli adapter

### Task 2: stream-json event parser

**Files:**
- Create: `adapter/claudecli/streamjson.go`
- Test: `adapter/claudecli/streamjson_test.go`

The `claude --output-format stream-json` output is newline-delimited JSON. Event types: `system` (init; has `session_id`), `assistant` (`message.content[]` text blocks), `user` (tool results; ignored for output), `result` (terminal; `session_id`, `result`, `is_error`, `usage`).

- [ ] **Step 1: Write the failing test**

Create `adapter/claudecli/streamjson_test.go`:

```go
package claudecli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEvent(t *testing.T) {
	t.Run("system init event yields session id", func(t *testing.T) {
		line := []byte(`{"type":"system","subtype":"init","session_id":"sess-1","model":"claude"}`)
		ev, ok, err := parseEvent(line)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, eventSystem, ev.Kind)
		assert.Equal(t, "sess-1", ev.SessionID)
	})

	t.Run("assistant event concatenates text blocks", func(t *testing.T) {
		line := []byte(`{"type":"assistant","session_id":"s","message":{"content":[{"type":"text","text":"Hello "},{"type":"text","text":"world"}]}}`)
		ev, ok, err := parseEvent(line)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, eventAssistant, ev.Kind)
		assert.Equal(t, "Hello world", ev.Text)
	})

	t.Run("assistant event ignores non-text blocks", func(t *testing.T) {
		line := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","text":""},{"type":"text","text":"hi"}]}}`)
		ev, ok, err := parseEvent(line)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, "hi", ev.Text)
	})

	t.Run("result event yields text, tokens, session id", func(t *testing.T) {
		line := []byte(`{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"s2","usage":{"input_tokens":10,"output_tokens":5}}`)
		ev, ok, err := parseEvent(line)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, eventResult, ev.Kind)
		assert.Equal(t, "done", ev.Text)
		assert.Equal(t, "s2", ev.SessionID)
		assert.False(t, ev.IsError)
		assert.Equal(t, 10, ev.TokensIn)
		assert.Equal(t, 5, ev.TokensOut)
	})

	t.Run("result event with is_error true", func(t *testing.T) {
		line := []byte(`{"type":"result","is_error":true,"result":"boom","session_id":"s"}`)
		ev, ok, err := parseEvent(line)
		require.NoError(t, err)
		require.True(t, ok)
		assert.True(t, ev.IsError)
	})

	t.Run("user event recognised but carries no text", func(t *testing.T) {
		line := []byte(`{"type":"user","session_id":"s","message":{"content":[{"type":"tool_result"}]}}`)
		ev, ok, err := parseEvent(line)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, eventUser, ev.Kind)
		assert.Equal(t, "", ev.Text)
	})

	t.Run("blank line is skipped", func(t *testing.T) {
		_, ok, err := parseEvent([]byte("   "))
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("unknown event type is skipped", func(t *testing.T) {
		_, ok, err := parseEvent([]byte(`{"type":"stream_event"}`))
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("malformed JSON returns an error", func(t *testing.T) {
		_, ok, err := parseEvent([]byte(`{not json`))
		require.Error(t, err)
		assert.False(t, ok)
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/claudecli/ -run TestParseEvent -v`
Expected: FAIL — `undefined: parseEvent` / `undefined: eventSystem` (package does not compile).

- [ ] **Step 3: Write minimal implementation**

Create `adapter/claudecli/streamjson.go`:

```go
// Package claudecli implements engine.LLMProvider by shelling out to the
// `claude` CLI in headless (`--print`) mode.
package claudecli

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// eventKind identifies a claude stream-json event type.
type eventKind string

const (
	eventSystem    eventKind = "system"
	eventAssistant eventKind = "assistant"
	eventUser      eventKind = "user"
	eventResult    eventKind = "result"
)

// streamEvent is a parsed claude stream-json event reduced to the fields
// arbitron needs.
type streamEvent struct {
	Kind      eventKind
	SessionID string
	Text      string // assistant text content, or the result event's final text
	IsError   bool   // result events only
	TokensIn  int    // result events only
	TokensOut int    // result events only
}

// rawEnvelope is the on-the-wire shape of a stream-json line.
type rawEnvelope struct {
	Type      string      `json:"type"`
	SessionID string      `json:"session_id"`
	IsError   bool        `json:"is_error"`
	Result    string      `json:"result"`
	Message   *rawMessage `json:"message"`
	Usage     *rawUsage   `json:"usage"`
}

type rawMessage struct {
	Content []rawContentBlock `json:"content"`
}

type rawContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type rawUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// parseEvent parses a single stream-json line. ok is false for blank lines and
// unrecognised event types (both are skipped without error). A non-nil error
// means the line was not valid JSON.
func parseEvent(line []byte) (streamEvent, bool, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return streamEvent{}, false, nil
	}

	var env rawEnvelope
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return streamEvent{}, false, fmt.Errorf("claudecli: parse stream-json line: %w", err)
	}

	switch eventKind(env.Type) {
	case eventSystem:
		return streamEvent{Kind: eventSystem, SessionID: env.SessionID}, true, nil
	case eventUser:
		return streamEvent{Kind: eventUser, SessionID: env.SessionID}, true, nil
	case eventAssistant:
		return streamEvent{
			Kind:      eventAssistant,
			SessionID: env.SessionID,
			Text:      assistantText(env.Message),
		}, true, nil
	case eventResult:
		ev := streamEvent{
			Kind:      eventResult,
			SessionID: env.SessionID,
			Text:      env.Result,
			IsError:   env.IsError,
		}
		if env.Usage != nil {
			ev.TokensIn = env.Usage.InputTokens
			ev.TokensOut = env.Usage.OutputTokens
		}
		return ev, true, nil
	default:
		return streamEvent{}, false, nil
	}
}

// assistantText concatenates the text blocks of an assistant message.
func assistantText(msg *rawMessage) string {
	if msg == nil {
		return ""
	}
	var b bytes.Buffer
	for _, block := range msg.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./adapter/claudecli/ -run TestParseEvent -v`
Expected: PASS — all subtests.

- [ ] **Step 5: Commit**

```bash
git add adapter/claudecli/streamjson.go adapter/claudecli/streamjson_test.go
git commit -m "feat: add claude-cli stream-json event parser"
```

---

### Task 3: CommandRunner interface and os/exec implementation

**Files:**
- Create: `adapter/claudecli/runner.go`
- Test: `adapter/claudecli/runner_test.go`

`CommandRunner` is the testability seam: the real implementation wraps `os/exec`; tests inject a fake. It streams stdout lines on a channel and exposes the process exit error via a `wait` function.

- [ ] **Step 1: Write the failing test**

Create `adapter/claudecli/runner_test.go`:

```go
package claudecli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecRunner(t *testing.T) {
	t.Run("streams stdout lines and exits cleanly", func(t *testing.T) {
		r := newExecRunner()
		lines, wait, err := r.Run(context.Background(), RunSpec{
			Bin:   "printf",
			Args:  []string{`one\ntwo\n`},
			Stdin: "",
		})
		require.NoError(t, err)

		var got []string
		for line := range lines {
			got = append(got, line)
		}
		require.NoError(t, wait())
		assert.Equal(t, []string{"one", "two"}, got)
	})

	t.Run("non-zero exit is reported by wait", func(t *testing.T) {
		r := newExecRunner()
		lines, wait, err := r.Run(context.Background(), RunSpec{Bin: "false"})
		require.NoError(t, err)
		for range lines { //nolint:revive // drain
		}
		assert.Error(t, wait())
	})

	t.Run("unknown binary fails at Start", func(t *testing.T) {
		r := newExecRunner()
		_, _, err := r.Run(context.Background(), RunSpec{Bin: "definitely-not-a-real-binary-xyz"})
		assert.Error(t, err)
	})

	t.Run("stdin is delivered to the process", func(t *testing.T) {
		r := newExecRunner()
		lines, wait, err := r.Run(context.Background(), RunSpec{Bin: "cat", Stdin: "piped-input"})
		require.NoError(t, err)
		var got string
		for line := range lines {
			got += line
		}
		require.NoError(t, wait())
		assert.Equal(t, "piped-input", got)
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/claudecli/ -run TestExecRunner -v`
Expected: FAIL — `undefined: newExecRunner` / `undefined: RunSpec`.

- [ ] **Step 3: Write minimal implementation**

Create `adapter/claudecli/runner.go`:

```go
package claudecli

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// maxLineBytes bounds a single stream-json line (tool results can be large).
const maxLineBytes = 4 * 1024 * 1024

// RunSpec describes a subprocess invocation.
type RunSpec struct {
	Bin   string
	Args  []string
	Stdin string
	Dir   string
	Env   []string
}

// CommandRunner starts a subprocess and streams its stdout line by line.
// The lines channel is closed when stdout reaches EOF; wait then blocks until
// the process exits and returns its exit error (nil on success).
type CommandRunner interface {
	Run(ctx context.Context, spec RunSpec) (lines <-chan string, wait func() error, err error)
}

// execRunner is the production CommandRunner backed by os/exec.
type execRunner struct{}

// newExecRunner constructs the os/exec-backed CommandRunner.
func newExecRunner() *execRunner { return &execRunner{} }

// Run starts the process described by spec.
func (r *execRunner) Run(ctx context.Context, spec RunSpec) (<-chan string, func() error, error) {
	cmd := exec.CommandContext(ctx, spec.Bin, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("claudecli: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("claudecli: start %q: %w", spec.Bin, err)
	}

	lines := make(chan string)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	wait := func() error { return cmd.Wait() }
	return lines, wait, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./adapter/claudecli/ -run TestExecRunner -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapter/claudecli/runner.go adapter/claudecli/runner_test.go
git commit -m "feat: add CommandRunner seam for claude-cli adapter"
```

---

### Task 4: Binary resolution and argument building

**Files:**
- Create: `adapter/claudecli/claude.go`
- Test: `adapter/claudecli/claude_test.go`

- [ ] **Step 1: Write the failing test**

Create `adapter/claudecli/claude_test.go`:

```go
package claudecli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/engine"
)

func TestBuildArgs(t *testing.T) {
	t.Run("base args are always present", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, "")
		assert.Equal(t, []string{
			"--print", "--verbose", "--output-format", "stream-json",
			"--dangerously-skip-permissions",
		}, args)
	})

	t.Run("request model overrides default model", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{Model: "claude-opus-4-7"}, "claude-sonnet-4-6")
		assert.Contains(t, args, "--model")
		assert.Contains(t, args, "claude-opus-4-7")
		assert.NotContains(t, args, "claude-sonnet-4-6")
	})

	t.Run("default model used when request model is empty", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, "claude-sonnet-4-6")
		assert.Contains(t, args, "--model")
		assert.Contains(t, args, "claude-sonnet-4-6")
	})

	t.Run("no --model flag when both are empty", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, "")
		assert.NotContains(t, args, "--model")
	})

	t.Run("session id adds --resume", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{SessionID: "sess-9"}, "")
		assert.Contains(t, args, "--resume")
		assert.Contains(t, args, "sess-9")
	})

	t.Run("no --resume when session id is empty", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, "")
		assert.NotContains(t, args, "--resume")
	})

	t.Run("system prompt adds --append-system-prompt", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{SystemPrompt: "be terse"}, "")
		assert.Contains(t, args, "--append-system-prompt")
		assert.Contains(t, args, "be terse")
	})
}

func TestResolveBinary(t *testing.T) {
	t.Run("explicit path is returned unchanged", func(t *testing.T) {
		assert.Equal(t, "/custom/claude", resolveBinary("/custom/claude"))
	})

	t.Run("empty path resolves to a non-empty candidate", func(t *testing.T) {
		assert.NotEmpty(t, resolveBinary(""))
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/claudecli/ -run 'TestBuildArgs|TestResolveBinary' -v`
Expected: FAIL — `undefined: buildArgs` / `undefined: resolveBinary`.

- [ ] **Step 3: Write minimal implementation**

Create `adapter/claudecli/claude.go`:

```go
package claudecli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
)

// buildArgs assembles the `claude` CLI arguments for one invocation.
// defaultModel is used when the request does not name a model.
func buildArgs(req engine.LLMRequest, defaultModel string) []string {
	args := []string{
		"--print", "--verbose", "--output-format", "stream-json",
		"--dangerously-skip-permissions",
	}

	model := req.Model
	if model == "" {
		model = defaultModel
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if req.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", req.SystemPrompt)
	}
	if req.SessionID != "" {
		args = append(args, "--resume", req.SessionID)
	}
	return args
}

// resolveBinary returns the claude binary path. An explicit path is returned
// unchanged; an empty path triggers a PATH lookup then common install dirs.
func resolveBinary(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, c := range []string{
		filepath.Join(home, ".local", "bin", "claude"),
		"/usr/local/bin/claude",
		"/opt/homebrew/bin/claude",
	} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "claude"
}

// augmentedEnv returns the process environment with common binary directories
// appended to PATH so the agent subprocess can find git/gh.
func augmentedEnv() []string {
	env := os.Environ()
	home, _ := os.UserHomeDir()
	extra := []string{
		filepath.Join(home, ".local", "bin"),
		"/usr/local/bin", "/opt/homebrew/bin",
	}
	for i, e := range env {
		if !strings.HasPrefix(e, "PATH=") {
			continue
		}
		path := strings.TrimPrefix(e, "PATH=")
		for _, d := range extra {
			if !strings.Contains(path, d) {
				path += string(os.PathListSeparator) + d
			}
		}
		env[i] = "PATH=" + path
		break
	}
	return env
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./adapter/claudecli/ -run 'TestBuildArgs|TestResolveBinary' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapter/claudecli/claude.go adapter/claudecli/claude_test.go
git commit -m "feat: add claude-cli binary resolution and arg building"
```

---

### Task 5: Provider.Call

**Files:**
- Create: `adapter/claudecli/provider.go`
- Create: `adapter/claudecli/fake_runner_test.go`
- Test: `adapter/claudecli/provider_test.go`

- [ ] **Step 1: Write the failing test**

Create `adapter/claudecli/fake_runner_test.go` (shared test helper):

```go
package claudecli

import "context"

// fakeRunner is a CommandRunner that replays canned stdout lines.
type fakeRunner struct {
	lines     []string
	waitErr   error
	startErr  error
	gotSpec   RunSpec
	runCalled bool
}

func (f *fakeRunner) Run(_ context.Context, spec RunSpec) (<-chan string, func() error, error) {
	f.runCalled = true
	f.gotSpec = spec
	if f.startErr != nil {
		return nil, nil, f.startErr
	}
	ch := make(chan string)
	go func() {
		defer close(ch)
		for _, l := range f.lines {
			ch <- l
		}
	}()
	return ch, func() error { return f.waitErr }, nil
}
```

Create `adapter/claudecli/provider_test.go`:

```go
package claudecli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
)

func TestProviderCall(t *testing.T) {
	t.Run("happy path returns result text, session id, tokens", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"system","subtype":"init","session_id":"s1"}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}`,
			`{"type":"result","is_error":false,"result":"final answer","session_id":"s1","usage":{"input_tokens":7,"output_tokens":3}}`,
		}}
		p := New("claude", "", 0, fr)

		resp, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "hi"})
		require.NoError(t, err)
		assert.Equal(t, "final answer", resp.Output)
		assert.Equal(t, "s1", resp.SessionID)
		assert.Equal(t, 7, resp.TokensIn)
		assert.Equal(t, 3, resp.TokensOut)
	})

	t.Run("prompt and workdir are passed to the runner", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","result":"ok","session_id":"s"}`,
		}}
		p := New("claude", "", 0, fr)

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "do it", WorkDir: "/tmp/ws"})
		require.NoError(t, err)
		assert.Equal(t, "do it", fr.gotSpec.Stdin)
		assert.Equal(t, "/tmp/ws", fr.gotSpec.Dir)
	})

	t.Run("result event with is_error true returns an error", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","is_error":true,"result":"model failure","session_id":"s"}`,
		}}
		p := New("claude", "", 0, fr)

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "model failure")
	})

	t.Run("missing result event returns an error", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
		}}
		p := New("claude", "", 0, fr)

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no result event")
	})

	t.Run("process exit error is surfaced", func(t *testing.T) {
		fr := &fakeRunner{
			lines:   []string{`{"type":"result","result":"ok","session_id":"s"}`},
			waitErr: errors.New("exit status 1"),
		}
		p := New("claude", "", 0, fr)

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "x"})
		require.Error(t, err)
	})

	t.Run("start failure is surfaced", func(t *testing.T) {
		fr := &fakeRunner{startErr: errors.New("binary not found")}
		p := New("claude", "", 0, fr)

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "binary not found")
	})

	t.Run("malformed stream-json line returns an error", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{`{bad json`}}
		p := New("claude", "", 0, fr)

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "x"})
		require.Error(t, err)
	})
}

func TestProviderImplementsPort(t *testing.T) {
	var _ engine.LLMProvider = (*Provider)(nil)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/claudecli/ -run 'TestProviderCall|TestProviderImplementsPort' -v`
Expected: FAIL — `undefined: New` / `undefined: Provider`.

- [ ] **Step 3: Write minimal implementation**

Create `adapter/claudecli/provider.go`:

```go
package claudecli

import (
	"context"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
)

// Provider implements engine.LLMProvider by invoking the `claude` CLI in
// headless (`--print`) mode.
type Provider struct {
	binPath      string
	defaultModel string
	timeout      time.Duration
	runner       CommandRunner
}

var _ engine.LLMProvider = (*Provider)(nil)

// New constructs a claude-cli Provider. An empty binaryPath is resolved from
// PATH and common install directories. A zero timeout means no timeout. A nil
// runner uses the production os/exec runner.
func New(binaryPath, defaultModel string, timeout time.Duration, runner CommandRunner) *Provider {
	if runner == nil {
		runner = newExecRunner()
	}
	return &Provider{
		binPath:      resolveBinary(binaryPath),
		defaultModel: defaultModel,
		timeout:      timeout,
		runner:       runner,
	}
}

// spec builds the RunSpec for a request.
func (p *Provider) spec(req engine.LLMRequest) RunSpec {
	return RunSpec{
		Bin:   p.binPath,
		Args:  buildArgs(req, p.defaultModel),
		Stdin: req.Prompt,
		Dir:   req.WorkDir,
		Env:   augmentedEnv(),
	}
}

// withTimeout derives a context honouring the provider timeout.
func (p *Provider) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if p.timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, p.timeout)
}

// Call runs claude to completion and returns the terminal result event.
func (p *Provider) Call(ctx context.Context, req engine.LLMRequest) (*engine.LLMResponse, error) {
	runCtx, cancel := p.withTimeout(ctx)
	defer cancel()

	lines, wait, err := p.runner.Run(runCtx, p.spec(req))
	if err != nil {
		return nil, fmt.Errorf("claudecli: call: %w", err)
	}

	var result streamEvent
	var haveResult bool
	for line := range lines {
		ev, ok, parseErr := parseEvent([]byte(line))
		if parseErr != nil {
			return nil, parseErr
		}
		if ok && ev.Kind == eventResult {
			result, haveResult = ev, true
		}
	}
	if waitErr := wait(); waitErr != nil {
		return nil, fmt.Errorf("claudecli: claude exited with error: %w", waitErr)
	}
	if !haveResult {
		return nil, fmt.Errorf("claudecli: no result event in claude output")
	}
	if result.IsError {
		return nil, fmt.Errorf("claudecli: claude reported an error: %s", result.Text)
	}

	return &engine.LLMResponse{
		Output:    result.Text,
		SessionID: result.SessionID,
		Model:     req.Model,
		TokensIn:  result.TokensIn,
		TokensOut: result.TokensOut,
	}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./adapter/claudecli/ -run 'TestProviderCall|TestProviderImplementsPort' -v`
Expected: FAIL to compile — `Provider` has no `StreamCall` method, so it does not satisfy `engine.LLMProvider`. Add a temporary stub to `provider.go` so the package compiles and `Call` tests run:

```go
// StreamCall is implemented in Task 6.
func (p *Provider) StreamCall(_ context.Context, _ engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
	return nil, fmt.Errorf("claudecli: StreamCall not yet implemented")
}
```

Re-run: `go test ./adapter/claudecli/ -run 'TestProviderCall|TestProviderImplementsPort' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapter/claudecli/provider.go adapter/claudecli/provider_test.go adapter/claudecli/fake_runner_test.go
git commit -m "feat: implement claude-cli Provider.Call"
```

---

### Task 6: Provider.StreamCall

**Files:**
- Modify: `adapter/claudecli/provider.go`
- Test: `adapter/claudecli/provider_test.go`

- [ ] **Step 1: Write the failing test**

Append to `adapter/claudecli/provider_test.go`:

```go
func collectChunks(t *testing.T, ch <-chan engine.LLMStreamChunk) []engine.LLMStreamChunk {
	t.Helper()
	var out []engine.LLMStreamChunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

func TestProviderStreamCall(t *testing.T) {
	t.Run("emits assistant text then a terminal chunk", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"system","subtype":"init","session_id":"s1"}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"part one "}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"part two"}]}}`,
			`{"type":"result","is_error":false,"result":"part one part two","session_id":"s1","usage":{"input_tokens":4,"output_tokens":9}}`,
		}}
		p := New("claude", "", 0, fr)

		ch, err := p.StreamCall(context.Background(), engine.LLMRequest{Prompt: "go"})
		require.NoError(t, err)
		chunks := collectChunks(t, ch)

		require.NotEmpty(t, chunks)
		var text string
		for _, c := range chunks {
			text += c.Token
		}
		assert.Equal(t, "part one part two", text)

		last := chunks[len(chunks)-1]
		assert.True(t, last.Done)
		assert.Equal(t, "s1", last.SessionID)
		assert.Equal(t, 4, last.TokensIn)
		assert.Equal(t, 9, last.TokensOut)
	})

	t.Run("captures session id even without a system event", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","result":"done","session_id":"s2"}`,
		}}
		p := New("claude", "", 0, fr)

		ch, err := p.StreamCall(context.Background(), engine.LLMRequest{Prompt: "go"})
		require.NoError(t, err)
		chunks := collectChunks(t, ch)
		assert.Equal(t, "s2", chunks[len(chunks)-1].SessionID)
	})

	t.Run("malformed line yields an error chunk", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{`{bad`}}
		p := New("claude", "", 0, fr)

		ch, err := p.StreamCall(context.Background(), engine.LLMRequest{Prompt: "go"})
		require.NoError(t, err)
		chunks := collectChunks(t, ch)

		require.NotEmpty(t, chunks)
		assert.Error(t, chunks[len(chunks)-1].Error)
	})

	t.Run("process exit error yields an error chunk", func(t *testing.T) {
		fr := &fakeRunner{
			lines:   []string{`{"type":"result","result":"ok","session_id":"s"}`},
			waitErr: errors.New("exit status 1"),
		}
		p := New("claude", "", 0, fr)

		ch, err := p.StreamCall(context.Background(), engine.LLMRequest{Prompt: "go"})
		require.NoError(t, err)
		chunks := collectChunks(t, ch)
		assert.Error(t, chunks[len(chunks)-1].Error)
	})

	t.Run("start failure returns an error from StreamCall itself", func(t *testing.T) {
		fr := &fakeRunner{startErr: errors.New("no binary")}
		p := New("claude", "", 0, fr)

		_, err := p.StreamCall(context.Background(), engine.LLMRequest{Prompt: "go"})
		require.Error(t, err)
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/claudecli/ -run TestProviderStreamCall -v`
Expected: FAIL — assistant chunks are empty / `Done` never set (the Task 5 stub returns an error).

- [ ] **Step 3: Write minimal implementation**

In `adapter/claudecli/provider.go`, replace the Task 5 `StreamCall` stub with:

```go
// StreamCall runs claude and streams assistant text as it arrives. The final
// chunk has Done=true and carries the session id and token counts. Any failure
// is reported as a chunk with a non-nil Error.
func (p *Provider) StreamCall(ctx context.Context, req engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
	runCtx, cancel := p.withTimeout(ctx)

	lines, wait, err := p.runner.Run(runCtx, p.spec(req))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claudecli: stream call: %w", err)
	}

	out := make(chan engine.LLMStreamChunk)
	go func() {
		defer cancel()
		defer close(out)

		var sessionID string
		var tokensIn, tokensOut int

		for line := range lines {
			ev, ok, parseErr := parseEvent([]byte(line))
			if parseErr != nil {
				out <- engine.LLMStreamChunk{Error: parseErr}
				return
			}
			if !ok {
				continue
			}
			if ev.SessionID != "" {
				sessionID = ev.SessionID
			}
			switch ev.Kind {
			case eventAssistant:
				if ev.Text != "" {
					out <- engine.LLMStreamChunk{Token: ev.Text}
				}
			case eventResult:
				tokensIn, tokensOut = ev.TokensIn, ev.TokensOut
				if ev.IsError {
					out <- engine.LLMStreamChunk{Error: fmt.Errorf("claudecli: claude reported an error: %s", ev.Text)}
					return
				}
			}
		}

		if waitErr := wait(); waitErr != nil {
			out <- engine.LLMStreamChunk{Error: fmt.Errorf("claudecli: claude exited with error: %w", waitErr)}
			return
		}

		out <- engine.LLMStreamChunk{
			Done:      true,
			SessionID: sessionID,
			TokensIn:  tokensIn,
			TokensOut: tokensOut,
		}
	}()

	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./adapter/claudecli/ -v`
Expected: PASS — all adapter tests.

- [ ] **Step 5: Commit**

```bash
git add adapter/claudecli/provider.go adapter/claudecli/provider_test.go
git commit -m "feat: implement claude-cli Provider.StreamCall"
```

---

## Part C — Engine-managed workspace

### Task 7: WorkspaceManager port and filesystem adapter

**Files:**
- Modify: `domain/engine/ports.go`
- Create: `adapter/filesystem/workspace.go`
- Test: `adapter/filesystem/workspace_test.go`

- [ ] **Step 1: Write the failing test**

Create `adapter/filesystem/workspace_test.go`:

```go
package filesystem

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceManager(t *testing.T) {
	t.Run("EnsureInstanceDir creates the directory and returns its path", func(t *testing.T) {
		base := t.TempDir()
		wm := NewWorkspaceManager(base)
		id := uuid.New()

		path, err := wm.EnsureInstanceDir(id)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(base, "instances", id.String()), path)

		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		assert.True(t, info.IsDir())
	})

	t.Run("EnsureInstanceDir is idempotent", func(t *testing.T) {
		wm := NewWorkspaceManager(t.TempDir())
		id := uuid.New()

		p1, err := wm.EnsureInstanceDir(id)
		require.NoError(t, err)
		p2, err := wm.EnsureInstanceDir(id)
		require.NoError(t, err)
		assert.Equal(t, p1, p2)
	})

	t.Run("RemoveInstanceDir deletes the directory", func(t *testing.T) {
		wm := NewWorkspaceManager(t.TempDir())
		id := uuid.New()
		path, err := wm.EnsureInstanceDir(id)
		require.NoError(t, err)

		require.NoError(t, wm.RemoveInstanceDir(id))
		_, statErr := os.Stat(path)
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("RemoveInstanceDir on a missing directory is not an error", func(t *testing.T) {
		wm := NewWorkspaceManager(t.TempDir())
		assert.NoError(t, wm.RemoveInstanceDir(uuid.New()))
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/filesystem/ -run TestWorkspaceManager -v`
Expected: FAIL — `undefined: NewWorkspaceManager`.

- [ ] **Step 3: Write minimal implementation**

In `domain/engine/ports.go`, add the port (place after the `FileReader` interface):

```go
// WorkspaceManager provides per-instance working directories for agent nodes.
type WorkspaceManager interface {
	// EnsureInstanceDir creates (idempotently) the working directory for an
	// instance and returns its absolute path.
	EnsureInstanceDir(instanceID uuid.UUID) (string, error)
	// RemoveInstanceDir deletes an instance's working directory. Removing a
	// directory that does not exist is not an error.
	RemoveInstanceDir(instanceID uuid.UUID) error
}
```

Create `adapter/filesystem/workspace.go`:

```go
package filesystem

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// WorkspaceManagerAdapter implements engine.WorkspaceManager by creating
// per-instance directories under basePath/instances/<instanceID>/.
type WorkspaceManagerAdapter struct {
	basePath string
}

// NewWorkspaceManager creates a WorkspaceManagerAdapter rooted at basePath.
func NewWorkspaceManager(basePath string) *WorkspaceManagerAdapter {
	return &WorkspaceManagerAdapter{basePath: basePath}
}

// instanceDir returns the directory path for an instance.
func (w *WorkspaceManagerAdapter) instanceDir(instanceID uuid.UUID) string {
	return filepath.Join(w.basePath, "instances", instanceID.String())
}

// EnsureInstanceDir creates the instance directory if needed and returns it.
func (w *WorkspaceManagerAdapter) EnsureInstanceDir(instanceID uuid.UUID) (string, error) {
	dir := w.instanceDir(instanceID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("filesystem: ensure instance dir: %w", err)
	}
	return dir, nil
}

// RemoveInstanceDir deletes the instance directory; a missing dir is ignored.
func (w *WorkspaceManagerAdapter) RemoveInstanceDir(instanceID uuid.UUID) error {
	if err := os.RemoveAll(w.instanceDir(instanceID)); err != nil {
		return fmt.Errorf("filesystem: remove instance dir: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./adapter/filesystem/ -run TestWorkspaceManager -v && go build ./...`
Expected: PASS, build succeeds.

- [ ] **Step 5: Commit**

```bash
git add domain/engine/ports.go adapter/filesystem/workspace.go adapter/filesystem/workspace_test.go
git commit -m "feat: add WorkspaceManager port and filesystem adapter"
```

---

### Task 8: Engine workspace lifecycle

**Files:**
- Modify: `domain/engine/engine.go`
- Modify: `domain/engine/executor_llm.go`
- Test: `domain/engine/engine_test.go` (add cases), `domain/engine/executor_llm_test.go` (add case)

The engine creates the workspace on run start, seeds `{{workspace}}` into the variable store, and deletes the workspace on successful completion. A nil `workspaceManager` disables the feature (keeps existing tests valid).

- [ ] **Step 1: Write the failing tests**

The harness in `engine_test.go` provides `newTestFixtures()` (struct `f` with
`engine`, `instanceRepo`, `workflowRepo`, `executor`), `waitForInstance()`,
`linearWorkflow()`, and `mockExecutor.onNode()`. `newTestFixtures()` leaves the
workspace manager unset (nil → feature disabled), so existing tests are
unaffected.

Add to `domain/engine/engine_test.go`:

```go
// fakeWorkspaceManager records workspace lifecycle calls.
type fakeWorkspaceManager struct {
	mu      sync.Mutex
	ensured []uuid.UUID
	removed []uuid.UUID
}

func (f *fakeWorkspaceManager) EnsureInstanceDir(id uuid.UUID) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, id)
	return "/tmp/arbitron-test-ws/" + id.String(), nil
}

func (f *fakeWorkspaceManager) RemoveInstanceDir(id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeWorkspaceManager) ensuredIDs() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]uuid.UUID, len(f.ensured))
	copy(cp, f.ensured)
	return cp
}

func (f *fakeWorkspaceManager) removedIDs() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]uuid.UUID, len(f.removed))
	copy(cp, f.removed)
	return cp
}

func TestEngine_WorkspaceLifecycle_RemovedOnSuccess(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	fwm := &fakeWorkspaceManager{}
	f.engine.SetWorkspaceManager(fwm)

	id, _, err := f.engine.Trigger("linear-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)

	assert.Equal(t, []uuid.UUID{id}, fwm.ensuredIDs())
	assert.Equal(t, []uuid.UUID{id}, fwm.removedIDs())
}

func TestEngine_WorkspaceLifecycle_KeptOnFailure(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	fwm := &fakeWorkspaceManager{}
	f.engine.SetWorkspaceManager(fwm)

	// Force node A to fail so the instance reaches StateFailed.
	f.executor.onNode("A", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("forced node failure")
	})

	id, _, err := f.engine.Trigger("linear-wf", nil, "normal")
	require.NoError(t, err)

	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	assert.Equal(t, []uuid.UUID{id}, fwm.ensuredIDs(), "workspace must be created")
	assert.Empty(t, fwm.removedIDs(), "workspace must be kept on failure")
}

func TestEngine_WorkspaceSeededAsVariable(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	fwm := &fakeWorkspaceManager{}
	f.engine.SetWorkspaceManager(fwm)

	var seenWorkspace any
	f.executor.onNode("A", func(_ context.Context, _ workflow.Node, vars *variable.VariableStore) (*engine.NodeResult, error) {
		seenWorkspace, _ = vars.Get("workspace")
		return &engine.NodeResult{}, nil
	})

	id, _, err := f.engine.Trigger("linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	assert.Equal(t, "/tmp/arbitron-test-ws/"+id.String(), seenWorkspace)
}
```

> `engine_test.go` already imports `sync`, `fmt`, `context`, `time`, `uuid`,
> and the `workflow`/`variable`/`instance` packages — no new imports needed.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./domain/engine/ -run TestEngine_Workspace -v`
Expected: FAIL — `undefined: (*engine.Engine).SetWorkspaceManager`.

- [ ] **Step 3: Write minimal implementation**

In `domain/engine/engine.go`:

1. Add a field to the `Engine` struct, alongside the other optional dependencies (`scheduler`, `webhookRegistry`, etc.):

```go
	workspaceManager WorkspaceManager
```

2. Add a setter (mirror `SetQueueManager` near line 1021):

```go
// SetWorkspaceManager wires the per-instance workspace manager. When unset,
// workspace directories are not created and {{workspace}} is not seeded.
func (e *Engine) SetWorkspaceManager(wm WorkspaceManager) {
	e.workspaceManager = wm
}
```

3. In `run()`, immediately after the variable store is built (the line
   `vars := variable.FromSnapshotWithNodeIDs(inst.Variables, nodeIDs)`,
   currently `engine.go:284`), seed the workspace:

```go
	if e.workspaceManager != nil {
		workspacePath, wsErr := e.workspaceManager.EnsureInstanceDir(instanceID)
		if wsErr != nil {
			slog.Error("failed to ensure instance workspace", "instanceID", instanceID, "error", wsErr)
			e.failInstance(ctx, instanceID, currentNodeID)
			return
		}
		vars.SetInput("workspace", workspacePath)
	}
```

4. In `run()`, in the success terminal block (currently `engine.go:364`,
   right after `e.instances.SetCompleted` succeeds), remove the workspace:

```go
		if e.workspaceManager != nil {
			if rmErr := e.workspaceManager.RemoveInstanceDir(instanceID); rmErr != nil {
				slog.Warn("failed to remove instance workspace", "instanceID", instanceID, "error", rmErr)
			}
		}
```

   Place this inside the existing `else` branch (the path taken when
   `SetCompleted` returns no error), after `emitNotification`.

In `domain/engine/executor_llm.go`, set `WorkDir` from the variable store. After
the line that builds `prompt := variable.Resolve(...)`, change the `LLMRequest`
construction to include the workspace:

```go
	workDir, _ := vars.Get("workspace")
	workDirStr, _ := workDir.(string)

	req := LLMRequest{
		Model:        node.Config.Model,
		Prompt:       prompt,
		MaxTokens:    node.Config.MaxTokens,
		Temperature:  node.Config.Temperature,
		OutputSchema: node.Config.OutputSchema,
		WorkDir:      workDirStr,
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./domain/engine/ -run TestEngineWorkspaceLifecycle -v && go test ./domain/engine/ ./adapter/... -count=1`
Expected: PASS — new test passes, no regressions.

- [ ] **Step 5: Commit**

```bash
git add domain/engine/engine.go domain/engine/executor_llm.go domain/engine/engine_test.go domain/engine/executor_llm_test.go
git commit -m "feat: engine-managed per-instance workspace lifecycle"
```

---

### Task 9: Reject reserved input name `workspace`

**Files:**
- Modify: `domain/workflow/validation.go`
- Test: `domain/workflow/validation_test.go`

- [ ] **Step 1: Write the failing test**

Add to `domain/workflow/validation_test.go`:

```go
func TestValidateReservedInputName(t *testing.T) {
	t.Run("input named workspace is rejected", func(t *testing.T) {
		wf := &Workflow{
			Nodes: []Node{{ID: "n1", Type: NodeTypeTransform, Entrypoint: true,
				Config: NodeConfig{Operations: []Operation{{Variable: "x", Operation: "set", Value: "y"}}}}},
			Inputs: []InputField{{Name: "workspace", Type: "string"}},
		}
		errs := Validate(wf, nil)
		var found bool
		for _, e := range errs {
			if e.Code == "RESERVED_INPUT_NAME" {
				found = true
			}
		}
		assert.True(t, found, "expected RESERVED_INPUT_NAME error")
	})

	t.Run("non-reserved input names are accepted", func(t *testing.T) {
		wf := &Workflow{
			Nodes: []Node{{ID: "n1", Type: NodeTypeTransform, Entrypoint: true,
				Config: NodeConfig{Operations: []Operation{{Variable: "x", Operation: "set", Value: "y"}}}}},
			Inputs: []InputField{{Name: "prNumber", Type: "string"}},
		}
		errs := Validate(wf, nil)
		for _, e := range errs {
			assert.NotEqual(t, "RESERVED_INPUT_NAME", e.Code)
		}
	})
}
```

> Implementer note: match the import style and `assert` usage of the existing `validation_test.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./domain/workflow/ -run TestValidateReservedInputName -v`
Expected: FAIL — no `RESERVED_INPUT_NAME` error is produced.

- [ ] **Step 3: Write minimal implementation**

In `domain/workflow/validation.go`, add a reserved-names set near `validInputTypes`:

```go
// reservedInputNames are variable names the engine seeds itself; workflow
// authors must not declare inputs with these names.
var reservedInputNames = map[string]bool{
	"workspace": true,
}
```

Add a validator function:

```go
// validateReservedInputNames checks that no input uses an engine-reserved name.
func validateReservedInputNames(wf *Workflow) []ValidationError {
	var errs []ValidationError
	for _, inp := range wf.Inputs {
		if reservedInputNames[inp.Name] {
			errs = append(errs, ValidationError{
				Code:    "RESERVED_INPUT_NAME",
				Message: "input name is reserved by the engine: " + inp.Name,
			})
		}
	}
	return errs
}
```

Call it from `Validate`, alongside the other input checks:

```go
	errs = append(errs, validateReservedInputNames(wf)...)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./domain/workflow/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add domain/workflow/validation.go domain/workflow/validation_test.go
git commit -m "feat: reject workflow inputs using the reserved name 'workspace'"
```

---

## Part D — Interactive rework

### Task 10: Sentinel constants and parser

**Files:**
- Create: `domain/engine/sentinel.go`
- Test: `domain/engine/sentinel_test.go`

The interactive completion signal is a sentinel block the agent emits:
`<arbitron:result>{...json...}</arbitron:result>`.

- [ ] **Step 1: Write the failing test**

Create `domain/engine/sentinel_test.go`:

```go
package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractSentinelResult(t *testing.T) {
	t.Run("extracts and parses a well-formed sentinel block", func(t *testing.T) {
		text := "Here is the outcome.\n<arbitron:result>{\"selected\":[1,2]}</arbitron:result>\nDone."
		out, ok, err := extractSentinelResult(text)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []any{float64(1), float64(2)}, out["selected"])
	})

	t.Run("no sentinel returns ok=false without error", func(t *testing.T) {
		_, ok, err := extractSentinelResult("just a normal reply")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("sentinel with invalid JSON returns an error", func(t *testing.T) {
		_, ok, err := extractSentinelResult("<arbitron:result>{not json}</arbitron:result>")
		require.Error(t, err)
		assert.False(t, ok)
	})

	t.Run("whitespace inside the sentinel is tolerated", func(t *testing.T) {
		out, ok, err := extractSentinelResult("<arbitron:result>\n  {\"a\":1}\n</arbitron:result>")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, float64(1), out["a"])
	})

	t.Run("last sentinel wins when several are present", func(t *testing.T) {
		out, ok, err := extractSentinelResult(
			"<arbitron:result>{\"v\":1}</arbitron:result> ... <arbitron:result>{\"v\":2}</arbitron:result>")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, float64(2), out["v"])
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./domain/engine/ -run TestExtractSentinelResult -v`
Expected: FAIL — `undefined: extractSentinelResult`.

- [ ] **Step 3: Write minimal implementation**

Create `domain/engine/sentinel.go`:

```go
package engine

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// InteractiveResultSentinelOpen / Close delimit the JSON result an
	// interactive agent emits to signal session completion. They replace the
	// Messages-API `complete_session` tool.
	InteractiveResultSentinelOpen  = "<arbitron:result>"
	InteractiveResultSentinelClose = "</arbitron:result>"
)

// extractSentinelResult scans agent output for the last completion sentinel
// block and parses its JSON payload. ok is false when no sentinel is present.
// A non-nil error means a sentinel was present but its payload was not valid
// JSON.
func extractSentinelResult(text string) (map[string]any, bool, error) {
	openIdx := strings.LastIndex(text, InteractiveResultSentinelOpen)
	if openIdx == -1 {
		return nil, false, nil
	}
	rest := text[openIdx+len(InteractiveResultSentinelOpen):]
	closeIdx := strings.Index(rest, InteractiveResultSentinelClose)
	if closeIdx == -1 {
		return nil, false, nil
	}
	payload := strings.TrimSpace(rest[:closeIdx])

	var out map[string]any
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return nil, false, fmt.Errorf("engine: parse interactive result sentinel: %w", err)
	}
	return out, true, nil
}

// sentinelInstruction returns the text appended to an interactive agent's
// prompt telling it how to complete the session. schema is the node's
// outputSchema (may be nil).
func sentinelInstruction(schema any) string {
	var b strings.Builder
	b.WriteString("\n\nWhen the conversation has reached its conclusion and you ")
	b.WriteString("have the final result, emit exactly one line:\n")
	b.WriteString(InteractiveResultSentinelOpen)
	b.WriteString("{...}")
	b.WriteString(InteractiveResultSentinelClose)
	b.WriteString("\nwhere {...} is a JSON object")
	if schema != nil {
		if schemaJSON, err := json.Marshal(schema); err == nil {
			b.WriteString(" conforming to this schema:\n")
			b.Write(schemaJSON)
		}
	}
	b.WriteString("\n")
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./domain/engine/ -run TestExtractSentinelResult -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add domain/engine/sentinel.go domain/engine/sentinel_test.go
git commit -m "feat: add interactive completion sentinel parser"
```

---

### Task 11: Rework InteractiveExecutor for session capture

**Files:**
- Modify: `domain/engine/executor_interactive.go`
- Test: `domain/engine/executor_interactive_test.go`

The first interactive turn drops the `complete_session` tool, appends the
sentinel instruction to the prompt, captures the session id from the stream,
and persists it in `LLMConversation` under `sessionId`. It also reads
`{{workspace}}` for `WorkDir`.

- [ ] **Step 1: Update existing tests and add the session-capture test**

The rework changes three things tests observe: (a) the request carries no
`Tools`, (b) the sentinel instruction is appended to the prompt, (c) the session
id is captured into `LLMConversation["sessionId"]`. `executor_interactive_test.go`
already has `interactiveTestSetup()`, `interactiveNode()`,
`buildInteractiveExecutor()`, the `mockStreamingLLMProvider` (with
`streamCallFn` + `getCapturedReqs()`), and the `cannedStream` helper. Make these
edits:

**1a. `TestInteractiveExecutor_StreamCallInvokedWithCorrectRequest`** — replace
the three `complete_session` tool assertions (`require.Len(t, req.Tools, 1)` and
the two lines after it) with:

```go
	// The reworked executor sends no tools — claude-cli has no injectable tools.
	assert.Empty(t, req.Tools)
```

The `MaxTokens`/`Temperature`/`SystemPrompt`/`Messages` assertions in that test
stay valid and unchanged.

**1b. `TestInteractiveExecutor_UserMessageCombinesOrchestrationAndTemplate`** —
the prompt now has the sentinel instruction appended, so the exact-equality
check no longer holds. Replace the final `assert.Equal(...)` block with:

```go
	// User message = orchestration + "\n\n" + resolved template, followed by
	// the completion-sentinel instruction.
	assert.Contains(t, reqs[0].Prompt, "Step 1: Do this\nStep 2: Do that\n\nName: Bob\nAge: 30")
	assert.Contains(t, reqs[0].Prompt, engine.InteractiveResultSentinelOpen)
```

**1c. Delete `TestInteractiveExecutor_BuildsCompleteSessionTool`** entirely — the
`complete_session` tool no longer exists.

**1d. Add a session-aware stream helper and the capture test:**

```go
// cannedStreamWithSession is cannedStream plus a session id on the Done chunk.
func cannedStreamWithSession(tokens []string, sessionID string) func(ctx context.Context, req engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
	return func(_ context.Context, _ engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
		ch := make(chan engine.LLMStreamChunk, len(tokens)+1)
		for _, tok := range tokens {
			ch <- engine.LLMStreamChunk{Token: tok}
		}
		ch <- engine.LLMStreamChunk{Done: true, SessionID: sessionID}
		close(ch)
		return ch, nil
	}
}

func TestInteractiveExecutor_CapturesSessionIDAndWorkDir(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")
	llmProvider.streamCallFn = cannedStreamWithSession([]string{"hello"}, "isess-1")

	vars := variable.NewStore()
	vars.SetInput("workspace", "/tmp/ws")

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)
	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), vars, "test-wf")
	require.NoError(t, err)

	// WorkDir flows from the seeded {{workspace}} variable; no tools; sentinel present.
	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Equal(t, "/tmp/ws", reqs[0].WorkDir)
	assert.Empty(t, reqs[0].Tools)
	assert.Contains(t, reqs[0].Prompt, engine.InteractiveResultSentinelOpen)

	// Session id is persisted under LLMConversation["sessionId"] for resumption.
	execs, _ := nodeExecRepo.ListByInstance(context.Background(), instanceID)
	require.Len(t, execs, 1)
	assert.Equal(t, "isess-1", execs[0].LLMConversation["sessionId"])
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./domain/engine/ -run TestInteractiveExecutor -v`
Expected: FAIL — `TestInteractiveExecutor_CapturesSessionIDAndWorkDir` fails
(no `sessionId` persisted, prompt lacks the sentinel) and the edited assertions
in 1a/1b fail (the executor still builds tools and an exact-match prompt).

- [ ] **Step 3: Write minimal implementation**

In `domain/engine/executor_interactive.go`, in `Execute`:

1. Delete step 6 (the `complete_session` tool construction). `tools` is gone.
2. Read the workspace from the variable store and append the sentinel
   instruction. Replace the `userMessage` / `req` construction (steps 7) with:

```go
	workDir, _ := vars.Get("workspace")
	workDirStr, _ := workDir.(string)

	userMessage := string(orchBytes) + "\n\n" + resolvedTemplate +
		sentinelInstruction(node.Config.OutputSchema)

	req := LLMRequest{
		SystemPrompt: node.Config.SystemPrompt,
		Prompt:       userMessage,
		Model:        node.Config.Model,
		MaxTokens:    node.Config.MaxTokens,
		Temperature:  node.Config.Temperature,
		WorkDir:      workDirStr,
	}
```

   (`MaxTokens`/`Temperature` are kept — the cli adapter ignores them but a
   future Messages-API adapter may use them, and keeping them avoids churning
   the `StreamCallInvokedWithCorrectRequest` assertions. Only `Tools` is dropped.)

3. While consuming the stream (step 9), capture the session id:

```go
		if chunk.SessionID != "" {
			sessionID = chunk.SessionID
		}
```

   (declare `var sessionID string` before the loop.)

4. In the conversation map (step 10), add the session id:

```go
	conversation := map[string]any{
		"sessionId": sessionID,
		"messages": []map[string]any{
			{"role": "user", "content": userMessage},
			{"role": "assistant", "content": fullResponse},
		},
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./domain/engine/ -run 'TestInteractive' -v && go build ./...`
Expected: PASS; build succeeds. (The `complete_session` constants in `ports.go`
are still referenced by `conversation.go` and are removed in Task 12.)

- [ ] **Step 5: Commit**

```bash
git add domain/engine/executor_interactive.go domain/engine/executor_interactive_test.go
git commit -m "feat: rework InteractiveExecutor for claude session capture"
```

---

### Task 12: Rework ConversationHandler for session resumption

**Files:**
- Modify: `domain/engine/conversation.go`
- Modify: `domain/engine/ports.go` (remove `complete_session` tool constants)
- Test: `domain/engine/conversation_test.go`

`HandleMessage` resumes via the stored session id (sending only the new user
message), detects completion via the sentinel block, and refreshes the stored
session id.

- [ ] **Step 1: Update existing tests and add the resumption tests**

This rework touches several existing tests in `conversation_test.go` because
the old `complete_session` tool behavior is asserted across many of them. The
file already provides `conversationTestSetup()`, `buildConversationHandler()`,
the `mockStreamingLLMProvider`, `cannedStream`, and `extractMessages()`.

**1a. Replace the `cannedToolStream` helper** (it builds a `complete_session`
ToolUse) with a sentinel-emitting helper:

```go
// cannedSentinelStream emits a response whose text contains a completion
// sentinel block carrying result, plus a Done chunk with a session id.
func cannedSentinelStream(prefix string, result map[string]any, sessionID string) func(ctx context.Context, req engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
	return func(_ context.Context, _ engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
		payload, _ := json.Marshal(result)
		body := prefix + engine.InteractiveResultSentinelOpen + string(payload) + engine.InteractiveResultSentinelClose
		ch := make(chan engine.LLMStreamChunk, 2)
		ch <- engine.LLMStreamChunk{Token: body}
		ch <- engine.LLMStreamChunk{Done: true, SessionID: sessionID}
		close(ch)
		return ch, nil
	}
}
```

**1b. The six session-completion tests** — `..._SessionCompletion_ExtractsOutput`,
`..._UpdatesInstanceVariables`, `..._TransitionsToQueued`,
`..._PublishesEndedEvent`, `..._NotifiesCapacityFreed`,
`..._PersistsConversation` — each calls `cannedToolStream(...)`. Change each call
to `cannedSentinelStream(...)`, e.g.:

```go
	// before:
	llmProvider.streamCallFn = cannedToolStream([]string{"Let me complete"}, toolInput, 10, 20)
	// after:
	llmProvider.streamCallFn = cannedSentinelStream("Let me complete\n", toolInput, "s-final")
```

Their assertions on `nodeExec.Output`, instance variables, the queued state, the
`interactive.ended` event, and capacity notification all stay valid.

**1c. Rewrite `TestConversationHandler_HandleMessage_SendsFullConversationToLLM`**
(rename it to `..._ResumesStoredSession`). The handler no longer replays history
or sends a tool. Replace the request assertions with:

```go
	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	req := reqs[0]

	assert.Equal(t, "You are a helpful assistant.", req.SystemPrompt)
	// Resumed turns send only the new user message via Prompt — no history, no tools.
	assert.Empty(t, req.Messages, "resumed turns must use Prompt, not Messages")
	assert.Equal(t, "New question", req.Prompt)
	assert.Empty(t, req.Tools, "claude-cli has no injectable tools")
	assert.Equal(t, "claude-sonnet-4-20250514", req.Model)
```

**1d. Delete two now-obsolete tests:**
- `TestConversationHandler_HandleMessage_JsonRoundTripHistory_ParsesMessages` —
  the handler no longer replays the message array to the LLM.
- `TestConversationHandler_HandleMessage_UnknownToolName_TreatsAsNormalResponse`
  — there is no tool-name completion gate anymore.

**1e. Update `TestConversationHandler_HandleMessage_NilConversationHistory_StartsFirstMessage`.**
Replace its `req.Messages` assertions with:

```go
	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Empty(t, reqs[0].Messages)
	assert.Equal(t, "Hello from scratch", reqs[0].Prompt)
	assert.Empty(t, reqs[0].SessionID, "no stored session id yet")
```

The later assertions in that test (messages appended to `LLMConversation` for
display) stay valid.

**1f. Add the resumption + sentinel tests:**

```go
func TestConversationHandler_HandleMessage_ResumesViaStoredSessionID(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	// Seed a stored claude session id on the existing node execution.
	nodeExecRepo.mu.Lock()
	nodeExecRepo.executions[0].LLMConversation["sessionId"] = "s-prev"
	nodeExecRepo.mu.Unlock()

	llmProvider.streamCallFn = cannedStream([]string{"resumed reply"}, 5, 10)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)
	require.NoError(t, handler.HandleMessage(context.Background(), instanceID, "next message"))

	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Equal(t, "s-prev", reqs[0].SessionID)
	assert.Equal(t, "next message", reqs[0].Prompt)
	assert.Empty(t, reqs[0].Messages)
}

func TestConversationHandler_HandleMessage_SentinelCompletesSession(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	llmProvider.streamCallFn = cannedSentinelStream("All set.\n",
		map[string]any{"picked": []any{"a", "b"}}, "s-final")

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)
	require.NoError(t, handler.HandleMessage(context.Background(), instanceID, "choose"))

	nodeExec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)
	assert.Equal(t, []any{"a", "b"}, nodeExec.Output["picked"])

	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	assert.Equal(t, instance.StateQueued, inst.State)
}

func TestConversationHandler_HandleMessage_NoSentinelIsNormalTurn(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	llmProvider.streamCallFn = cannedStream([]string{"just chatting"}, 5, 10)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)
	require.NoError(t, handler.HandleMessage(context.Background(), instanceID, "hi"))

	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	assert.Equal(t, instance.StateWaiting, inst.State, "no sentinel: session stays open")

	nodeExec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)
	messages := extractMessages(t, nodeExec.LLMConversation)
	assert.Equal(t, "assistant", messages[len(messages)-1]["role"])
}
```

The append/event tests (`AppendsUserMessage`, `PublishesUserMessageEvent`,
`PublishesTokenEvents`, `AppendsAssistantResponse`, `PublishesAssistantMessageEvent`),
the streaming-error tests, and all `EndSession` tests use `cannedStream` or no
provider and are unaffected.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./domain/engine/ -run TestConversationHandler -v`
Expected: FAIL — the new resumption/sentinel tests fail, and the edited tests
(1b/1c/1e) fail because the handler still replays `Messages` and detects
completion via the `complete_session` tool.

- [ ] **Step 3: Write minimal implementation**

In `domain/engine/conversation.go`:

1. Add a helper to read the stored session id:

```go
// conversationSessionID extracts the stored claude session id, if any.
func conversationSessionID(conv map[string]any) string {
	if conv == nil {
		return ""
	}
	id, _ := conv["sessionId"].(string)
	return id
}
```

2. Change `streamLLMResponse` to return the session id instead of a tool use:

```go
func (h *conversationHandler) streamLLMResponse(
	ctx context.Context,
	instanceID uuid.UUID,
	req LLMRequest,
) (response string, sessionID string, err error) {
	stream, err := h.llmProvider.StreamCall(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("conversation: stream call: %w", err)
	}

	var responseBuilder strings.Builder
	for chunk := range stream {
		if chunk.Error != nil {
			return "", "", fmt.Errorf("conversation: stream error: %w", chunk.Error)
		}
		if chunk.Token != "" {
			responseBuilder.WriteString(chunk.Token)
			h.eventBus.Publish(instanceID, Event{
				Type:      EventTypeInteractiveToken,
				Timestamp: time.Now(),
				Data:      map[string]any{"token": chunk.Token},
			})
		}
		if chunk.SessionID != "" {
			sessionID = chunk.SessionID
		}
		if chunk.Done {
			break
		}
	}
	return responseBuilder.String(), sessionID, nil
}
```

3. Rewrite `HandleMessage` steps 6–10. Replace the tool construction, the
   `llmMessages` history build, and the request with:

```go
	// 6. Build the request: resume the stored session, send only the new
	//    user message. WorkDir comes from the instance's seeded workspace.
	workDir, _ := inst.Variables["workspace"].(string)
	req := LLMRequest{
		SystemPrompt: nodeConfig.SystemPrompt,
		Prompt:       content,
		Model:        nodeConfig.Model,
		SessionID:    conversationSessionID(nodeExec.LLMConversation),
		WorkDir:      workDir,
	}

	// 7. Call the provider with streaming.
	fullResponse, newSessionID, err := h.streamLLMResponse(ctx, instanceID, req)
	if err != nil {
		return err
	}

	// 8. Detect completion via the result sentinel.
	sentinelOutput, completed, sentErr := extractSentinelResult(fullResponse)
	if sentErr != nil {
		return sentErr
	}

	// 9. Append the assistant message and refresh the stored session id.
	messages = append(messages, map[string]any{
		"role":    "assistant",
		"content": fullResponse,
	})

	if completed {
		return h.handleSessionCompletion(ctx, instanceID, nodeID, nodeExec, messages, newSessionID, sentinelOutput)
	}
	return h.handleNormalResponse(ctx, instanceID, nodeExec, messages, newSessionID, fullResponse)
```

   Note: the user message is still appended to `messages` at step 4 (unchanged)
   so history/display is preserved.

4. Update `handleSessionCompletion` and `handleNormalResponse` to persist the
   refreshed session id. Change their signatures to accept `sessionID string`
   and write the conversation map as:

```go
	nodeExec.LLMConversation = map[string]any{
		"sessionId": sessionID,
		"messages":  messages,
	}
```

   In `handleSessionCompletion` the assistant message is already appended by
   the caller (step 9 above) — remove its own step-1 append to avoid a
   duplicate. In `handleNormalResponse` likewise remove its step-1 append.

5. In `domain/engine/ports.go`, remove the now-unused constants
   `InteractiveCompletionToolName` and `InteractiveCompletionToolDescription` —
   the `complete_session` tool is fully replaced by the sentinel. This is the
   last task that references them, so the build stays green.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./domain/engine/ -count=1`
Expected: PASS — all engine tests including the reworked conversation tests.

- [ ] **Step 5: Commit**

```bash
git add domain/engine/conversation.go domain/engine/ports.go domain/engine/conversation_test.go
git commit -m "feat: rework ConversationHandler for claude session resumption"
```

---

## Part E — Config and wiring

### Task 13: Config fields for the claude-cli provider

**Files:**
- Modify: `domain/config/config.go`
- Modify: `adapter/config/loader.go`
- Test: `domain/config/config_test.go` or `adapter/config/loader_test.go`

- [ ] **Step 1: Write the failing test**

Add to the config loader test (`adapter/config/loader_test.go`) a case asserting
the new defaults load:

```go
func TestLoadClaudeCLIDefaults(t *testing.T) {
	t.Run("claude-cli config fields have defaults", func(t *testing.T) {
		dir := t.TempDir()
		cfg, err := Load(filepath.Join(dir, "config.yaml"))
		require.NoError(t, err)
		assert.Equal(t, 0, cfg.LLM.TimeoutSeconds) // default: no timeout
		assert.Equal(t, "", cfg.LLM.BinaryPath)    // default: auto-resolve
		assert.Equal(t, "", cfg.LLM.Model)         // default: provider default
	})
}
```

> Implementer note: match the existing `loader_test.go` import/helper style.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/config/ -run TestLoadClaudeCLIDefaults -v`
Expected: FAIL — `cfg.LLM.TimeoutSeconds` / `BinaryPath` / `Model` undefined.

- [ ] **Step 3: Write minimal implementation**

In `domain/config/config.go`, extend `LLMConfig`:

```go
// LLMConfig holds LLM provider settings.
type LLMConfig struct {
	Provider string
	APIKey   string
	// BinaryPath is the path to the `claude` binary (claude-cli provider).
	// Empty means auto-resolve from PATH.
	BinaryPath string
	// Model is the default model passed to the provider when a node does not
	// specify one.
	Model string
	// TimeoutSeconds bounds a single provider call. 0 means no timeout.
	TimeoutSeconds int
}
```

In `adapter/config/loader.go`, add defaults to the `defaults()` map:

```go
		"llm.binarypath":     "",
		"llm.model":          "",
		"llm.timeoutseconds": 0,
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./adapter/config/ ./domain/config/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add domain/config/config.go adapter/config/loader.go adapter/config/loader_test.go
git commit -m "feat: add claude-cli config fields (binary path, model, timeout)"
```

---

### Task 14: Wire the claude-cli provider and workspace manager

**Files:**
- Modify: `cmd/arbitron/main.go`

This is bootstrap wiring (no unit test; verified by build + manual run). The
provider is selected by `config.LLM.Provider`.

- [ ] **Step 1: Replace provider construction**

In `cmd/arbitron/main.go`, replace step 7 (`llmProvider := anthropic.New(cfg.LLM.APIKey)`) with provider selection:

```go
	// 7. Create the LLM provider based on config.
	var llmProvider engine.LLMProvider
	switch cfg.LLM.Provider {
	case "claude-cli":
		llmProvider = claudecli.New(
			cfg.LLM.BinaryPath,
			cfg.LLM.Model,
			time.Duration(cfg.LLM.TimeoutSeconds)*time.Second,
			nil, // production os/exec runner
		)
	default:
		llmProvider = anthropic.New(cfg.LLM.APIKey)
	}
```

Add the `claudecli` import (`github.com/arbitron/arbitron/adapter/claudecli`) and
ensure `time` is imported.

- [ ] **Step 2: Wire the workspace manager**

After the engine is created (step 11, `eng := engine.NewEngine(...)`), add:

```go
	// 11a. Wire the per-instance workspace manager.
	eng.SetWorkspaceManager(filesystem.NewWorkspaceManager(cfg.Workspace.Path))
```

- [ ] **Step 3: Verify it builds**

Run: `go build ./... && go vet ./...`
Expected: success.

- [ ] **Step 4: Full test suite**

Run: `go test ./... -count=1`
Expected: PASS across all packages.

- [ ] **Step 5: Commit**

```bash
git add cmd/arbitron/main.go
git commit -m "feat: wire claude-cli provider and workspace manager"
```

---

## Final Verification

- [ ] Run the full suite: `go test ./... -count=1` — all green.
- [ ] `go vet ./...` — clean.
- [ ] Set `llm.provider: "claude-cli"` in a scratch config and trigger a simple
      single `llm_call` workflow against a repo checkout to confirm the agent
      runs end-to-end (requires the `claude` CLI installed and authenticated).

---

## Self-Review Notes

- **Spec coverage:** Part 1 → Tasks 2–6; Part 2 → Task 1; Part 3 → Tasks 7–9;
  Part 4 → Tasks 10–12; Part 5 → Tasks 13–14. All spec sections mapped.
- **`Conversation` field:** `LLMResponse.Conversation` is left unset by the
  claude-cli adapter (optional; `LLMCallExecutor` tolerates a nil conversation).
- **`LLMRequest.Messages`:** unused by the claude-cli path after Task 12; kept
  on the port for future adapters, as the spec states.
