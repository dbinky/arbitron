# Phase 3, Task 5: Resolver Provider→Adapter Wiring in `main.go` + End-to-End Smoke

## Goal

Wire the resolution chain to the concrete Phase-3 adapters in
`cmd/arbitron/main.go`, so a node's resolved `Connection.Provider` selects the right
backend at construction time:

- `openrouter` / `litellm` → `adapter/openrouter` (task 1) `ChatProvider`.
- `azure-openai` / `azure-foundry` → `adapter/azurefoundry` (task 2) `ChatProvider`.
- empty connection (subscription Claude) → the Phase-1 claude-cli `ChatAdapter`.
- agent provider on a non-Claude/open-weight model → `adapter/opencode` (task 4)
  `AgentProvider`.

This is the assembly task. **No new validation code is written** — but capability
validation now becomes *load-bearing*: a chat-only adapter genuinely cannot edit, so an
`agent_call` whose profile resolves to a native chat adapter is impossible by
construction (the profile is `kind: chat` and already fails `workflow_validate` from
Phase 1). Add an end-to-end smoke test per configuration.

**This is task 5 of 5. It depends on tasks 1, 2, and 4** (the three concrete adapters)
and on task 3 transitively (via task 4). It is the last task and should land after the
others.

### Dependencies consumed (do NOT redefine — import them)

- Phase 1 claude-cli `ChatAdapter` / `AgentAdapter` and the slim profile registry.
- Phase 2 `domain/provider.ProfileResolver` → `ResolvedBinding` (the resolution chain
  `alias → profile → connection → backend + lazy RevealKey`) and the
  `ConnectionStore`. The provider→adapter mapping in this task is the concrete
  realization of Phase 2 §3 step 3 ("`Connection.Provider` selects the concrete
  backend").
- Tasks 1/2/4 adapter constructors (`openrouter.New`, `azurefoundry.New`,
  `opencode.New`) and their `Config` shapes (binding, injectable HTTP client / runner).

### Hex placement

Wiring lives in `cmd/arbitron/main.go` (composition root). The mapping function may
live in a small `adapter/provider` factory (driven adapter) consuming the
`ResolvedBinding`; the domain resolver returns a binding and never imports a concrete
adapter — the composition root / factory does the provider→adapter selection.

## Files to Create/Modify

### Create: `adapter/provider/factory.go`

- A factory that maps a `ResolvedBinding` to a concrete backend, keeping `main.go` thin
  and the selection table-testable:
  ```go
  // ChatProviderFor returns the concrete engine.ChatProvider for a chat-kind
  // binding, selected by binding.Provider. An empty Provider (subscription
  // Claude) returns the claude-cli ChatAdapter. Unknown providers error.
  func ChatProviderFor(b provider.ResolvedBinding, deps Deps) (engine.ChatProvider, error)

  // AgentProviderFor returns the concrete engine.AgentProvider for an
  // agent-kind binding: a non-empty/non-Claude provider → adapter/opencode;
  // empty/Claude → the claude-cli AgentAdapter.
  func AgentProviderFor(b provider.ResolvedBinding, deps Deps) (engine.AgentProvider, error)
  ```
  - `Deps` carries the shared construction inputs (claude-cli adapters or their config,
    an injectable `*http.Client` for the HTTP adapters, the shared `CommandRunner`,
    workspace root).
  - Mapping:
    - `"openrouter"`, `"litellm"` → `openrouter.New(...)`.
    - `"azure-openai"`, `"azure-foundry"` → `azurefoundry.New(...)`.
    - `""` (empty connection) → claude-cli `ChatAdapter` (chat) / `AgentAdapter` (agent).
    - non-empty/non-Claude provider for an agent binding → `opencode.New(...)`.
    - anything else → `ErrUnknownProvider`.
- `var ErrUnknownProvider = errors.New("provider: no adapter for provider")`.

### Modify: `cmd/arbitron/main.go`

- Where the LLM provider is constructed today (the `switch cfg.LLM.Provider` around
  the `engine.LLMProvider` wiring), construct the widened Phase-2 `ProfileResolver`
  and pass the factory (or a resolver→adapter selection closure) into the executors,
  so `llm_call` receives the resolved `ChatProvider` and `agent_call` receives the
  resolved `AgentProvider` per the node's profile alias.
- Keep the empty-connection path defaulting to the claude-cli `ChatAdapter`/`AgentAdapter`
  so a Claude-only deployment with zero connections still runs (Phase 2 no-key path).
- Inject the production `*http.Client` into the HTTP adapters and the production
  `CommandRunner` into the opencode adapter (nil → defaults).
- No changes to `domain/workflow/validation.go` — the capability rule from Phase 1 is
  already load-bearing; document in a comment that an `agent_call` resolving to a chat
  adapter is impossible by construction.

## Testing

Strict TDD. Factory selection is table-tested in `adapter/provider`; end-to-end smoke
per configuration uses the adapters' own test seams (httptest / fake transport for the
HTTP adapters, the `CommandRunner` fake for opencode) — NO live network and NO real
binaries. All FIVE scenario categories:

- **Happy path** — `provider == "openrouter"` → `ChatProviderFor` returns an
  `*openrouter.Provider`; `provider == "azure-openai"` → `*azurefoundry.Provider`; an
  empty provider → the claude-cli `ChatAdapter`; an agent binding with a non-Claude
  provider → `*opencode.Provider`.
- **Success (variants)** — `litellm` maps to the openrouter adapter; `azure-foundry`
  maps to the azurefoundry adapter; an end-to-end smoke per configuration: a fake-backed
  `llm_call` runs through each chat adapter and an `agent_call` runs through opencode,
  proving the alias→profile→connection→adapter chain produces a runnable node.
- **Failure** — an `agent_call` whose alias resolves to a `kind: chat` profile is
  rejected by the existing Phase-1 validation BEFORE any run (assert the
  `workflow_validate` error; no adapter is even constructed) — capability validation
  proven load-bearing with no new code.
- **Error** — unknown provider string → `ErrUnknownProvider` from the factory; a
  required dependency missing (e.g. no claude-cli config for the empty-provider path) →
  a clear construction error.
- **Edge case** — empty connection + agent kind → claude-cli `AgentAdapter` (not
  opencode); a provider string with different casing/whitespace is handled per the
  documented normalization rule (or rejected) — assert the documented behavior.
- **Security assertion** — a smoke test that captures logs while resolving and running a
  connection-backed node asserts the revealed key never appears in any log line (the
  delivery half of acceptance #7, end to end through the factory + adapter).

## Acceptance Criteria

- `Connection.Provider` selects the concrete adapter: `openrouter`/`litellm` →
  `adapter/openrouter`; `azure-openai`/`azure-foundry` → `adapter/azurefoundry`; empty →
  claude-cli `ChatAdapter`; non-Claude agent → `adapter/opencode`.
- Switching a node's profile from Claude to Azure/OpenRouter is a single
  profile-reference change and the node runs (acceptance #2).
- `agent_call` runs via claude-cli (Phase 1) and via opencode on a non-Claude model
  (acceptance #3), proving the opencode path + env-injected credential.
- Capability validation is load-bearing with NO new validation code: an `agent_call`
  resolving to a chat adapter is impossible by construction and fails
  `workflow_validate`.
- Unknown providers yield `ErrUnknownProvider`.
- An end-to-end smoke test passes per configuration using each adapter's test seam (no
  live network, no real binaries).
- The revealed key never appears in logs end to end (acceptance #7 delivery half).
- All existing tests continue to pass.
