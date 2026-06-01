# Vendor-Abstraction MVP — Design Phase 1: Port Split, `agent_call`, claude-cli Both Ports, opencode Spike

**Date:** 2026-06-01 · **Status:** Design approved; pending implementation plan
**Spec:** `docs/specs/feature-pipeline-replacement-mvp.md`
**Phasing:** Risk-first thin vertical. This phase delivers an end-to-end Claude-subscription path with the intent/backend split in place and de-risks opencode before later phases depend on it.

## Overview

Phase 1 splits the single `engine.LLMProvider` port into two capability-scoped ports — `ChatProvider` (reasoning, no side effects) and `AgentProvider` (agentic file editing) — and rebinds the node kinds onto them. The existing `claude-cli` adapter is refactored into two thin adapters over the shared `CommandRunner`/stream-json machinery: a **tool-suppressed** chat adapter and a **tools-enabled** agent adapter. A new `agent_call` node kind carries all agency; `llm_call` becomes tool-suppressed and loses its silent agentic behavior (spec §3). A **slim profile registry** is introduced so nodes reference a backend by alias (`model_profile`) and capability validation (`agent_call`↔agent, `llm_call`↔chat) is real from this phase — even though every profile resolves to `claude-cli` until Phase 2. Finally, a contained **opencode headless spike** de-risks the spec's §9 critical-path item before Phase 3 builds the opencode `AgentProvider`.

This phase realizes acceptance criteria **#1** (local Claude run, no API keys) and **#6** (capability validation), and lays the port/contract foundation for the rest.

---

## 1. Port Split — `domain/engine/ports.go`

Replace the single `LLMProvider` interface (`Call` + `StreamCall`) with two interfaces split by capability (spec §4). The split is the corollary of intent-vs-backend: a chat-only backend must not be forced to no-op tool methods, and the `agent_call` capability contract needs a port it can require.

### `ChatProvider` — reasoning/chat, no side effects

```go
// ChatProvider performs reasoning/chat with no tool use and no file side
// effects. Backends: tool-suppressed claude -p (P1); Azure AI Foundry,
// OpenRouter (P3).
type ChatProvider interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    StreamChat(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error)
}

type ChatRequest struct {
    Model        string // resolved model-id (from the profile)
    SystemPrompt string
    Prompt       string
    MaxTokens    int
    Temperature  float64
    OutputSchema any
    WorkDir      string // claude -p needs a cwd even when tool-suppressed
    SessionID    string // retained for the deferred interactive backend
}

type ChatResponse struct {
    Output    any
    Model     string
    TokensIn  int
    TokensOut int
    SessionID string
}

type ChatChunk struct {
    Token     string
    Done      bool
    Error     error
    TokensIn  int
    TokensOut int
    SessionID string
}
```

`ChatRequest`/`ChatResponse`/`ChatChunk` are the current `LLMRequest`/`LLMResponse`/`LLMStreamChunk` renamed and trimmed of agent concerns. `WorkDir` stays because `claude -p` requires a working directory even in chat mode; `SessionID` stays for the deferred `interactive` backend (spec §9).

### `AgentProvider` — agentic editing (new)

```go
// AgentProvider runs an agentic mission that mutates files inside the
// instance workspace. The agentic loop (tool calls, edits, test runs)
// happens INSIDE the harness, not inside Arbitron. Backends: claude -p with
// tools (P1); opencode (P3).
type AgentProvider interface {
    RunAgent(ctx context.Context, mission AgentMission) (*AgentResult, error)
}

type AgentMission struct {
    Model     string   // resolved model-id (or provider/model for opencode)
    Prompt    string   // the mission instruction
    WorkDir   string   // = the instance {{workspace}}
    ToolScope []string // allowed-tools / permission scope
    SessionID string   // optional cross-step resume; see opencode spike (§5)
    Timeout   time.Duration
}

type AgentResult struct {
    Summary   string       // result/summary text
    Steps     []AgentStep  // step events (tool calls, edits) for the trace
    Model     string
    TokensIn  int          // coarse; subprocess accounting (spec §9)
    TokensOut int
    SessionID string
}

type AgentStep struct {
    Kind    string // "tool_call" | "edit" | "assistant" | "result"
    Summary string
}
```

`RunAgent` is non-streaming for MVP; step events are surfaced afterward in `AgentResult.Steps`. Token/cost accounting is coarse for a subprocess harness (accepted, spec §9).

**Hex placement:** both ports stay in `domain/engine` (driven ports the engine depends on). No domain type imports an adapter.

---

## 2. claude-cli Adapters — `adapter/claudecli`

The existing `Provider` is refactored into two adapters that share the `CommandRunner`, the stream-json parser (`streamjson.go`), and the streaming state machine (`streamstate.go`). The `procenv.BaseEnv()` allowlist (no secrets) is unchanged in this phase — the Claude subscription inherits the ambient login, so no key is injected (spec §6).

### `ChatAdapter` (implements `ChatProvider`)

- Invokes `claude -p --print --verbose --output-format stream-json` **tool-suppressed**: emit an empty allowed-tools set (`--allowedTools ""`) and **omit** `--dangerously-skip-permissions`. The agent has no tools, so it behaves as a plain completion.
- `Chat` parses the terminal `result` event → `ChatResponse`. `StreamChat` maps `assistant` events → token chunks, the `result` event → the terminal `Done` chunk (reusing today's logic).
- Validates `WorkDir` resolves under the workspace root, as today.

### `AgentAdapter` (implements `AgentProvider`)

- Invokes `claude -p ... --dangerously-skip-permissions` with `ToolScope` → `--allowedTools`; `WorkDir` = `{{workspace}}`; PATH-augmented env so the agent finds `git`/`gh`.
- `RunAgent` runs to completion; `assistant`/`user`(tool-result) events fold into `AgentResult.Steps`; the `result` event yields `Summary` + token counts + `SessionID`.

### Shared seam

Both adapters depend on the existing `CommandRunner` interface (`Run(ctx, RunSpec)`), so both stay unit-testable with the canned-stream-json fake. The `argsPolicy`/`buildArgs` helper gains a `SuppressTools bool` (chat) vs. `SkipPermissions bool` (agent) distinction; arg-threading is exercised by table tests.

---

## 3. `agent_call` Node Kind + Executor

### Node kind — `domain/workflow/workflow.go`

Add `NodeTypeAgentCall NodeType = "agent_call"` alongside the existing kinds.

`agent_call` config fields:

- `model_profile` — alias of an **agent**-kind profile (§4)
- `templateFile` — mission template (relative path, `{{variable}}` substitution), as `llm_call`
- `toolScope` — optional allowed-tools list (default: full agent scope)
- `timeoutSeconds` — optional extended timeout (agents run longer than chat)
- `outputSchema` — optional structured result schema

Agent-specific configuration lives on **this** node kind, not on `llm_call` (spec §3).

### `AgentCallExecutor` — `domain/engine/executor_agent.go`

A new `NodeExecutor` mirroring `LLMCallExecutor`'s structure:

1. Read `templateFile` via `FileReader`; resolve `{{variable}}` substitutions.
2. Resolve `model_profile` → agent profile → `AgentMission.Model` (§4).
3. Set `WorkDir` from the `{{workspace}}` reserved variable; `ToolScope`/`Timeout` from config.
4. Call `AgentProvider.RunAgent`.
5. Record `AgentResult.Steps`/`Summary`/token counts on the `NodeExecution`; output is the parsed `outputSchema` result (or `Summary` text when no schema).

Registered in `cmd/arbitron/main.go` as `executors[workflow.NodeTypeAgentCall] = engine.NewAgentCallExecutor(fileReader, agentProvider, ...)`.

### `llm_call` rebind

`LLMCallExecutor` now depends on `ChatProvider` instead of `LLMProvider`; it calls `Chat`. **Behavior change:** `llm_call` no longer has tools and cannot edit files. Workflows that relied on the silently-agentic `llm_call` (e.g. the current pr-review workflow) migrate those nodes to `agent_call`. This is the explicit fix for the §3 leaky abstraction. `interactive` is **untouched** in this phase: it keeps its current claude-cli streaming path (its provider-agnostic rework to a Messages-API backend is deferred per spec §4/§9).

---

## 4. Slim Profile Registry — `domain/provider`

A new small domain package introduces the profile concept *minus* connections (which arrive in Phase 2), so the alias indirection and capability contract exist from Phase 1 with no node rework later.

```go
// domain/provider
type ProfileKind string

const (
    ProfileKindChat  ProfileKind = "chat"
    ProfileKindAgent ProfileKind = "agent"
)

// Profile is the slim P1 shape. Phase 2 adds ConnectionRef + default params.
type Profile struct {
    Alias string
    Kind  ProfileKind
    Model string // model-id passed to the resolved backend
}

// ProfileResolver maps a node's alias to a Profile.
type ProfileResolver interface {
    Resolve(alias string) (Profile, error)
}
```

- **Registry source (P1):** a flat, server-level registry loaded via koanf (`domain/config` + `adapter/config`), e.g. a `profiles` block: `{ alias, kind, model }`. **Not** stored in `workflow.json`. A flat global registry (no named profile-sets); a config swap repoints aliases.
- **Adapter:** `adapter/provider` holds a `StaticProfileResolver` over the loaded registry. In P1 every profile resolves to `claude-cli`: `kind: chat` → the `ChatAdapter`, `kind: agent` → the `AgentAdapter`. The engine wires the two claude-cli adapters and the resolver selects which one a node uses by `kind`.
- **Node reference:** `llm_call` and `agent_call` carry `model_profile: <alias>`. The bare `node.Config.Model` string is **removed** in favor of the alias (one-time migration of existing workflows).

### Capability validation — `domain/workflow/validation.go`

Extend `validateNodeConfigs()`:

- `llm_call` requires `model_profile` referencing a profile whose `kind == chat`.
- `agent_call` requires `model_profile` referencing a profile whose `kind == agent`.
- A kind mismatch (e.g. `agent_call` → a chat profile) **fails `workflow_validate` with a clear error, before any run** (acceptance #6). This prevents the silent failure where a tool-less model narrates *"I would edit the file"* and the workflow proceeds (spec §3).

Validation needs the profile registry. It is threaded into validation the same way the existing file `checker` is (validation already takes injected checkers), so the domain stays adapter-free.

---

## 5. opencode Headless Spike (de-risk task)

A contained, throwaway spike run **before** the `AgentProvider` contract is frozen, to settle the spec §9 critical-path risk (opencode headless session-resume; upstream #2404, #13851). It is not production code; it produces a written finding that sets the opencode adapter shape built in Phase 3.

**Procedure:**

1. In a temporary `{{workspace}}` with a seed file, run `opencode run --model <provider/model> "<edit instruction>"` headless (no TTY), with a provider key injected as an environment variable into the subprocess.
2. Confirm: the file is actually mutated, the process exits 0, and nothing blocks on an interactive prompt.
3. Probe **cross-step resume**: a second `opencode` step that depends on the first session's context (via `opencode serve`/session id), to see whether headless resume is reliable.

**Pass bar:** step 2 succeeds (real mutation, clean exit, no hang). **Resume decision:** if step 3 is unreliable, the `AgentProvider` contract for opencode is **single-shot mission per step** — no `SessionID` threading; each agent node is one self-contained `opencode run`. The claude-cli `AgentProvider` is unaffected either way. The finding is recorded in the Phase 3 design notes.

---

## 6. Config & Wiring — `domain/config`, `adapter/config`, `cmd/arbitron/main.go`

- `domain/config`: add a `Profiles` registry block (`[]{ alias, kind, model }`); the existing `LLMConfig` (binary path, default model, timeout, tool scope, max response bytes) is retained for the claude-cli backends. No API key is required for the Claude subscription path.
- `adapter/config/loader.go`: load the `profiles` block (koanf, env-overridable per existing precedence).
- `cmd/arbitron/main.go`: construct the two claude-cli adapters (`ChatAdapter`, `AgentAdapter`), the `StaticProfileResolver`, and register `NodeTypeAgentCall`. `LLMCallExecutor` is constructed with the `ChatProvider`; `AgentCallExecutor` with the `AgentProvider` and the resolver.

---

## 7. Testing Strategy (strict TDD, 5 scenario categories)

Every unit covers happy path, success, failure, error, and edge case.

- **claude-cli `ChatAdapter`** — tool-suppression arg-threading (`--allowedTools ""`, no skip-permissions); result parse; streaming chunks; non-zero exit; binary-not-found; timeout/context-cancel; malformed stream-json; empty output.
- **claude-cli `AgentAdapter`** — `--dangerously-skip-permissions` + `--allowedTools` threading; `WorkDir` under workspace root; `result` → `AgentResult` with steps + tokens + session id; failure/error/edge cases as above; `SessionID` resume arg threaded when set.
- **`AgentCallExecutor`** — template read + substitution; profile resolve → mission; workspace WorkDir seeding; output (schema vs. summary); provider error propagation; missing `{{workspace}}` edge case.
- **`LLMCallExecutor` rebind** — calls `ChatProvider.Chat`; no tools requested; structured vs. text output; provider error.
- **`StaticProfileResolver`** — alias hit; unknown alias error; chat vs. agent kind selection; duplicate-alias edge case.
- **Validation** — `agent_call`→agent OK; `llm_call`→chat OK; `agent_call`→chat fails with clear error; missing `model_profile` fails; unknown alias fails.
- **opencode spike** — not unit-tested (throwaway); the finding is captured as a Phase 3 design note, not a test.

## 8. Sequencing (each built test-first)

1. Port split (rename `LLMProvider` → `ChatProvider`, add `AgentProvider`) — additive/rename, unblocks the rest.
2. claude-cli `ChatAdapter` + `AgentAdapter` over the shared runner.
3. `domain/provider` profile registry + `StaticProfileResolver` + config loading.
4. `agent_call` kind + `AgentCallExecutor`; rebind `LLMCallExecutor` to `ChatProvider`.
5. Capability validation.
6. Wiring in `main.go`; migrate existing workflow `model`→`model_profile`.
7. opencode spike (parallelizable; informs Phase 3, not blocking Phase 1 completion).

## 9. Acceptance Mapping

- **#1 Local Claude run** — `llm_call` via tool-suppressed `claude -p`, `agent_call` via `claude -p` with tools, no API keys.
- **#6 Capability validation** — `agent_call` bound to a chat profile fails `workflow_validate`.
- Foundation for #2/#3/#4/#5/#7 (delivered in later phases).
