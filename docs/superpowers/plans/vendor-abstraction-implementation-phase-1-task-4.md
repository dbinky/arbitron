# Phase 1, Task 4: `agent_call` Node Kind + `AgentCallExecutor`; rebind `llm_call` to a chat profile

## Goal

Add the `agent_call` node kind (`NodeTypeAgentCall`) with its config fields, build the `AgentCallExecutor` (`domain/engine/executor_agent.go`) that resolves an agent profile and runs an `AgentProvider` mission, and rebind `LLMCallExecutor` so it resolves a chat profile and calls `ChatProvider` with no tools. Replace the bare `node.Config.Model` string with a `model_profile` alias on both kinds.

Source of truth: `docs/superpowers/specs/vendor-abstraction-design-phase-1.md` §3 and §4.

## Dependencies

- **Task 1** (`ChatProvider`/`AgentProvider`, `ChatRequest`, `AgentMission`/`AgentResult`/`AgentStep`).
- **Task 3** (`domain/provider.Profile`/`ProfileKind`/`ProfileResolver`, `ErrUnknownProfile`).

## Background (current state)

- `domain/workflow/workflow.go` — `NodeType` constants (`llm_call`, `script`, `tool_call`, `conditional`, `transform`, `timer_delay`, `webhook_wait`, `interactive`). `NodeConfig` has `TemplateFile`, `Model`, `OutputSchema`, `MaxTokens`, `Temperature`, `TimeoutSeconds`, `ToolScope` (NOT present today — add it), etc.
- `domain/engine/executor_llm.go` — `LLMCallExecutor{ fileReader FileReader; llmProvider ChatProvider }` (post-Task-1), reads `node.Config.TemplateFile`, resolves `{{variable}}` via `variable.Resolve`, sets `WorkDir` via `WorkDirFromVars(vars)`, builds `ChatRequest{ Model: node.Config.Model, ... }`, calls `.Chat`, then `processLLMOutput`.
- `domain/engine/conventions.go` — `ReservedVarWorkspace = "workspace"`, `WorkDirFromVars(vars)` returns `(path, seeded bool)`.
- `domain/engine/result.go` — `NodeResult` (Output, LLMConversation, LLMModel, LLMTokensIn, LLMTokensOut, ScriptStdout/Stderr/ExitCode, IsWait).
- `domain/engine/executor.go` — `NodeExecutor.Execute(ctx, node, vars, workflowName) (*NodeResult, error)`.

## Files to Create/Modify

### Modify: `domain/workflow/workflow.go`

- Add the node kind:
  ```go
  NodeTypeAgentCall NodeType = "agent_call"
  ```
- Add config fields to `NodeConfig` (additive; keep `omitempty`):
  ```go
  ModelProfile string   `json:"model_profile,omitempty"` // alias of a profile (chat for llm_call, agent for agent_call)
  ToolScope    []string `json:"toolScope,omitempty"`     // agent_call: allowed-tools list (default: full agent scope)
  ```
  - `templateFile`, `timeoutSeconds`, `outputSchema` already exist on `NodeConfig` and are reused by `agent_call` (mission template, extended timeout, structured result schema).
  - Retain the existing `Model string json:"model"` field for now (the validation + migration in Tasks 5/6 move workflows to `model_profile`); the executors below stop reading `Model` in favor of `ModelProfile`.

### Create: `domain/engine/executor_agent.go`

`AgentCallExecutor` implements `NodeExecutor`, mirroring `LLMCallExecutor`'s structure.

```go
type AgentCallExecutor struct {
    fileReader    FileReader
    agentProvider AgentProvider
    resolver      provider.ProfileResolver
}

func NewAgentCallExecutor(fr FileReader, ap AgentProvider, resolver provider.ProfileResolver) *AgentCallExecutor
```

- Note: this is the first `domain/engine` type to import `domain/provider`. That is hex-legal (domain→domain). Add the import.

`Execute` steps:
1. Read `node.Config.TemplateFile` via `fileReader.ReadFile(workflowName, ...)`; wrap read errors as `agent_call %q: read template: %w`.
2. Resolve `{{variable}}` substitutions: `prompt := variable.Resolve(string(templateBytes), vars)`.
3. Resolve the profile: `prof, err := e.resolver.Resolve(node.Config.ModelProfile)`; wrap errors as `agent_call %q: resolve profile: %w`. (Validation in Task 5 catches missing/unknown/kind-mismatch before any run; the executor still guards defensively.)
4. Determine `WorkDir`: `workDirStr, _ := WorkDirFromVars(vars)`. If not seeded (`ok == false`) treat as the empty-workspace edge case — the `AgentProvider` (claude-cli `AgentAdapter`) rejects an empty `WorkDir` via `ErrWorkDirRequired`, so propagate that error rather than silently running.
5. Build the mission:
   ```go
   mission := AgentMission{
       Model:     prof.Model,
       Prompt:    prompt,
       WorkDir:   workDirStr,
       ToolScope: node.Config.ToolScope,
       Timeout:   time.Duration(node.Config.TimeoutSeconds) * time.Second,
   }
   ```
   (`SessionID` left empty in P1 — single-shot mission per step; cross-step resume is a Phase-3 decision pending the opencode spike.)
6. `res, err := e.agentProvider.RunAgent(ctx, mission)`; wrap as `agent_call %q: run agent: %w`.
7. Output: when `node.Config.OutputSchema != nil`, parse `res.Summary` into the schema-shaped output (reuse the `processLLMOutput`/`parseOrWrapText` JSON logic — factor a shared helper or call the existing one with a `ChatResponse`-like wrapper; simplest: add a small `processAgentOutput(res *AgentResult, schema any) any` that mirrors `processLLMOutput`). With no schema, output is the `Summary` text (wrapped via `parseOrWrapText`).
8. Return:
   ```go
   &NodeResult{
       Output:       output,
       LLMModel:     res.Model,
       LLMTokensIn:  res.TokensIn,
       LLMTokensOut: res.TokensOut,
   }
   ```
   - `AgentResult.Steps`/`Summary` are recorded on the `NodeExecution` for the trace. If `NodeResult` has no step-carrying field today, record steps by serializing them into `LLMConversation` (an `any`) under a small map (e.g. `{"steps": [...], "summary": ...}`), keeping the executor adapter-free. (Do not widen `NodeResult` beyond what the engine reads unless necessary; prefer reusing `LLMConversation` for the agent trace payload in P1.)

### Modify: `domain/engine/executor_llm.go`

- Replace `Model: node.Config.Model` with profile resolution:
  - Add a `resolver provider.ProfileResolver` field + constructor param: `NewLLMCallExecutor(fr FileReader, provider ChatProvider, resolver provider.ProfileResolver)`.
  - In `Execute`, resolve `prof, err := e.resolver.Resolve(node.Config.ModelProfile)` (wrap as `llm_call %q: resolve profile`), and set `req.Model = prof.Model`.
  - The request stays tool-suppressed by construction (the `ChatProvider`/`ChatAdapter` never has tools) — no tool fields on `ChatRequest`. This is the §3 fix: `llm_call` can no longer edit files.
  - Everything else (`processLLMOutput`, `WorkDirFromVars`) unchanged.

### Note: `interactive` and the conversation handler

- `interactive` is untouched this phase (design §3). It keeps reading `node.Config.Model` and calling `ChatProvider.StreamChat`. Do NOT rebind it to a profile in this task; leave its `Model` usage as-is. (Its provider-agnostic rework is deferred.)

## Testing (strict TDD — five scenario categories)

Use fakes: a `fakeChatProvider`/`fakeAgentProvider` and a `fakeResolver` (in-memory map) in the engine test package. Per design §7.

### `AgentCallExecutor`
- **Happy path:** template read + `{{variable}}` substitution feeds `mission.Prompt`; profile resolves to an agent profile → `mission.Model`; `{{workspace}}` seeds `mission.WorkDir`; fake `RunAgent` returns a result → `NodeResult.Output` is the summary (no schema), tokens + model propagate; steps recorded on the trace payload.
- **Success:** with `OutputSchema` set, `RunAgent`'s JSON summary is parsed into structured output; `ToolScope` and `timeoutSeconds` from config thread into the mission.
- **Failure:** `fileReader.ReadFile` error → wrapped `agent_call %q: read template` error, no provider call.
- **Error:** `RunAgent` returns an error → wrapped `agent_call %q: run agent` error propagated; resolver miss (`ErrUnknownProfile`) → wrapped `resolve profile` error.
- **Edge case:** `{{workspace}}` not seeded → empty `WorkDir` propagated and provider rejects it (assert the error surfaces, not a silent run); empty `AgentResult.Steps` yields a valid `NodeResult`; non-JSON summary with no schema is wrapped via `parseOrWrapText`.

### `LLMCallExecutor` rebind
- **Happy path:** resolves a chat profile → `ChatRequest.Model = prof.Model`; calls `ChatProvider.Chat`; returns structured/text output as before.
- **Success:** no tool fields are set on the request (compile-time: `ChatRequest` has none); structured vs. text output both handled by `processLLMOutput`.
- **Failure:** template read error → wrapped error, no provider call.
- **Error:** `ChatProvider.Chat` error → wrapped `provider call` error; resolver miss → wrapped `resolve profile` error.
- **Edge case:** empty `OutputSchema` with non-JSON text → `{"response": text}` wrap preserved.

Constraint: executors live in `domain/engine`; they import `domain/provider`, `domain/variable`, `domain/workflow` only — never an adapter. All provider/resolver dependencies are fakes in tests.

## Acceptance Criteria

- `workflow.NodeTypeAgentCall` exists and `NodeConfig` carries `model_profile` + `toolScope` (reusing existing `templateFile`/`timeoutSeconds`/`outputSchema`).
- `AgentCallExecutor` reads a template, resolves an agent profile, runs an `AgentProvider` mission seeded from `{{workspace}}`, and records steps/summary/tokens.
- `LLMCallExecutor` resolves a chat profile for its model and calls `ChatProvider` with no tools (loses file-editing ability).
- `interactive` behavior is unchanged.
- `go build ./...` and `go test ./domain/engine/... ./domain/workflow/...` pass.
