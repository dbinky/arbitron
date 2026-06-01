# Phase 1, Task 6: Wiring in `main.go` + Workflow JSON `model` → `model_profile` Migration

## Goal

Wire the new pieces together in `cmd/arbitron/main.go`: construct both claude-cli adapters (`ChatAdapter`, `AgentAdapter`), the `StaticProfileResolver` (from config profiles), register `NodeTypeAgentCall`, inject the `ChatProvider` into `LLMCallExecutor` (with the resolver) and the `AgentProvider` + resolver into `AgentCallExecutor`, and thread the `ResolverChecker` into `workflow_validate`. Migrate existing workflow JSON node configs from `model` to `model_profile`.

Source of truth: `docs/superpowers/specs/vendor-abstraction-design-phase-1.md` §6 and §8 item 6.

## Dependencies

- **Task 1** (ports), **Task 2** (`ChatAdapter`/`AgentAdapter`), **Task 3** (`StaticProfileResolver`, config `Profiles`, `ProfilesFromConfig`), **Task 4** (`AgentCallExecutor`, executor constructor signatures), **Task 5** (`workflow.Validate(wf, checker, profiles)`, `ResolverChecker`).
- This task is the integration point and runs after all code-bearing tasks.

## Background (current state, `cmd/arbitron/main.go`)

- Around line 196: a `switch cfg.LLM.Provider` builds a single `var llmProvider engine.ChatProvider` (post-Task-1) — `case "claude-cli"` constructs `claudecli.New(...)`; `default` builds the anthropic stub. `WorkspaceRoot` is resolved from `cfg.Workspace.Path`.
- Around line 226: `executors := map[workflow.NodeType]engine.NodeExecutor{ ... NodeTypeLLMCall: engine.NewLLMCallExecutor(fileReader, llmProvider), ... }`.
- Around line 296: `executors[workflow.NodeTypeInteractive] = engine.NewInteractiveExecutor(instanceRepo, nodeExecRepo, llmProvider, fileReader, bus, queueMgr)` and the conversation handler at ~301 also takes `llmProvider`.
- `workflow.Validate` is called wherever `workflow_validate` is served — the two production call sites are `adapter/mcp/tools_workflow.go` (line ~248) and `adapter/http/workflow_handler.go` (line ~114), both currently `workflow.Validate(wf, <fileReader>)`. Each needs the new third `ProfileChecker` arg (the `ResolverChecker`). (Confirm via `grep -rn "workflow.Validate(" .`.)
- Existing workflow JSON using `model`: `adapter/filesystem/testdata/all-node-types/workflow.json` (and any production workflow definitions under the workspace path — these live outside the repo at runtime, so the migration here covers in-repo testdata + a documented operator step).

## Files to Create/Modify

### Modify: `cmd/arbitron/main.go`

1. **Build the two claude-cli adapters** (replace the single `llmProvider` construction for the claude path). Keep the `cfg.LLM.Provider` switch, but for `claude-cli` construct both:
   ```go
   wsRoot, err := filepath.Abs(cfg.Workspace.Path) // existing
   // ...
   chatCfg := claudecli.Config{
       BinaryPath:       cfg.LLM.BinaryPath,
       DefaultModel:     cfg.LLM.Model,
       WorkspaceRoot:    wsRoot,
       Timeout:          time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
       MaxResponseBytes: cfg.LLM.MaxResponseBytes,
       // chat is tool-suppressed by construction: no AllowedTools, no SkipPermissions
   }
   chatProvider := claudecli.NewChatAdapter(chatCfg)

   agentCfg := chatCfg
   agentCfg.SkipPermissions = cfg.LLM.SkipPermissions
   agentCfg.AllowedTools = cfg.LLM.AllowedTools // full agent scope default
   agentProvider := claudecli.NewAgentAdapter(agentCfg)
   ```
   - `var chatProvider engine.ChatProvider` and `var agentProvider engine.AgentProvider`.
   - `default` (non-claude) branch: `chatProvider = anthropic.New(cfg.LLM.APIKey)`; for `agentProvider`, there is no non-claude agent backend in P1 — set it to a small nil-safe stub that returns an error from `RunAgent` (so a workflow using `agent_call` against a non-claude deploy fails loudly), or leave the claude `AgentAdapter` as the only agent backend and document that `agent_call` requires the claude-cli provider in P1. Prefer: construct the claude `AgentAdapter` regardless of provider in P1 (every agent profile resolves to claude-cli per design §4), so `agent_call` works on the local Claude path even when `cfg.LLM.Provider` defaults elsewhere — but only if `wsRoot` is valid. Keep it simple: in the `default` branch, still build the claude `AgentAdapter` for agents.

2. **Build the resolver** from config profiles:
   ```go
   profiles := configadapter.ProfilesFromConfig(cfg.Profiles) // Task 3 seam
   resolver, err := provideradapter.NewStaticProfileResolver(profiles)
   if err != nil { /* close db/log, return wrapped error */ }
   ```
   - Import the `adapter/provider` package (alias e.g. `provideradapter`) and the `ProfilesFromConfig` helper from `adapter/config` (or `adapter/provider`, per Task 3's placement).

3. **Update the executor map** (the `llm_call` constructor gained a resolver param in Task 4):
   ```go
   workflow.NodeTypeLLMCall: engine.NewLLMCallExecutor(fileReader, chatProvider, resolver),
   ```
   and register the agent executor:
   ```go
   executors[workflow.NodeTypeAgentCall] = engine.NewAgentCallExecutor(fileReader, agentProvider, resolver)
   ```

4. **Interactive + conversation handler** still take a `ChatProvider`; pass `chatProvider`:
   ```go
   executors[workflow.NodeTypeInteractive] = engine.NewInteractiveExecutor(
       instanceRepo, nodeExecRepo, chatProvider, fileReader, bus, queueMgr)
   conversationHandler := engine.NewConversationHandler(
       instanceRepo, nodeExecRepo, chatProvider, bus, queueMgr)
   ```

5. **Thread the profile checker into validation.** Build `checker := provideradapter.NewResolverChecker(resolver)` (Task 5) and pass it to every `workflow.Validate(wf, fileChecker, checker)` call site. If validation is invoked from an HTTP handler or service constructed in `main.go`, pass the `ResolverChecker` (and the existing file checker) down through that constructor. Update all `workflow.Validate(` callers found by grep — including `adapter/http` and any service layer.

### Modify: `adapter/filesystem/testdata/all-node-types/workflow.json`

- For each `llm_call`/`agent_call`/`interactive` node config, replace the `"model": "<id>"` field with `"model_profile": "<alias>"` referencing a profile that the test config registers (e.g. `default-chat` for `llm_call`, `default-agent` for `agent_call`). If this testdata feeds a validation test, ensure the test's profile registry includes those aliases with matching kinds.
  - Note: `interactive` still reads `node.Config.Model` in P1 (unchanged per Task 4). If this testdata's interactive node relies on `model`, leave its `model` field intact OR add a `model_profile` only for `llm_call`/`agent_call`. Do not break the interactive node's existing parse.

### Migration note (operator-facing, in-repo doc only)

- Add a short note to the design/plan trail (or a `MIGRATION` line in the PR description — do not create a new doc file) that production workflow JSON under the workspace must rename `model` → `model_profile` on `llm_call` nodes and convert agentic `llm_call` nodes to `agent_call` (design §3: the current pr-review workflow's silently-agentic `llm_call` nodes move to `agent_call`). The in-repo testdata change above is the executable part; production workflows live outside the repo and are migrated by the operator following this note.

## Testing (strict TDD — five scenario categories)

`main.go` wiring is integration-level; the unit-testable seams (adapters, resolver, executors, validation) are covered by Tasks 2–5. For this task:

- **Happy path:** an end-to-end (or `BuildServer`/`run`-constructor) test builds the dependency graph with a test config containing `default-chat`/`default-agent` profiles and asserts the executor map contains `NodeTypeLLMCall` and `NodeTypeAgentCall`, and that `Validate` is called with a non-nil profile checker.
- **Success:** a workflow whose `llm_call` references `default-chat` and `agent_call` references `default-agent` passes `workflow_validate` through the wired `ResolverChecker`.
- **Failure:** the migrated testdata workflow with an `agent_call` pointed at a chat profile fails validation through the real wiring (`PROFILE_KIND_MISMATCH`).
- **Error:** `NewStaticProfileResolver` failing on a duplicate-alias config aborts startup with a wrapped error (assert `BuildServer`/constructor returns an error, db/log closed).
- **Edge case:** a config with no `profiles:` block falls back to the `default-chat`/`default-agent` defaults (Task 3) and the server still constructs; a workflow with only non-LLM nodes validates with the profile checker wired.

Constraint: prefer testing the existing server-build/`run` constructor (the one returning `(*something, error)` used by `main`) rather than `main()` itself. Adapters are wired with the production `os/exec` runner in `main`, but the build-graph test can use the default config + temp workspace.

## Acceptance Criteria

- `main.go` constructs `ChatAdapter` (tool-suppressed) and `AgentAdapter` (tools), a `StaticProfileResolver` from `cfg.Profiles`, and registers `NodeTypeAgentCall`.
- `LLMCallExecutor` receives `chatProvider` + resolver; `AgentCallExecutor` receives `agentProvider` + resolver; interactive/conversation receive `chatProvider`.
- Every `workflow.Validate` call site passes the `ResolverChecker`.
- In-repo testdata workflow migrated `model` → `model_profile`; operator migration note recorded in the PR.
- `go build ./...` and `go test ./...` pass; acceptance #1 (local Claude run, no API keys) and #6 (capability validation) hold through the wired path.
