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

- A `Factory` type that maps a `ResolvedBinding` to a concrete backend, keeping
  `main.go` thin and the selection table-testable. Its methods satisfy the
  `engine.ChatProviderSelector` / `engine.AgentProviderSelector` ports defined above, so
  the executors depend on the domain interface and never import this adapter:
  ```go
  // Factory holds the shared construction inputs and selects a concrete backend
  // per resolved binding. It implements engine.ChatProviderSelector and
  // engine.AgentProviderSelector.
  type Factory struct{ deps Deps }

  func NewFactory(deps Deps) *Factory
  var _ engine.ChatProviderSelector  = (*Factory)(nil)
  var _ engine.AgentProviderSelector = (*Factory)(nil)

  // ChatProviderFor returns the concrete engine.ChatProvider for a chat-kind
  // binding, selected by binding.Provider. An empty Provider (subscription
  // Claude) returns the claude-cli ChatAdapter. Unknown providers error.
  func (f *Factory) ChatProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error)

  // AgentProviderFor returns the concrete engine.AgentProvider for an
  // agent-kind binding: a non-empty/non-Claude provider → adapter/opencode;
  // empty/Claude → the claude-cli AgentAdapter.
  func (f *Factory) AgentProviderFor(b provider.ResolvedBinding) (engine.AgentProvider, error)
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

### Provider-selection seam (reconciles the per-node selection with the Phase-1 executor)

In Phase 1 the `LLMCallExecutor`/`AgentCallExecutor` each hold **one** concrete
`ChatProvider`/`AgentProvider` (the only backend then is claude-cli) plus the
`ProfileResolver`, and they call `resolver.Resolve(alias)` only to read the model.
From Phase 3 the concrete backend must be chosen **per node** from the resolved
`binding.Provider` (openrouter / azure / claude-cli / opencode). To keep the executor
adapter-free (it must not import `adapter/openrouter` etc.), introduce a tiny
**domain selector port** that the executor holds **in place of** the single raw
provider field — the factory below is its adapter implementation:

```go
// domain/engine — selector ports the executors depend on. The composition root
// supplies an implementation (adapter/provider factory). Keeps domain/engine free
// of concrete adapter imports while allowing per-node backend selection.
type ChatProviderSelector interface {
    ChatProviderFor(b provider.ResolvedBinding) (ChatProvider, error)
}
type AgentProviderSelector interface {
    AgentProviderFor(b provider.ResolvedBinding) (AgentProvider, error)
}
```

- **Executor change (do this in Task 5, the integration task):** replace the
  `LLMCallExecutor`'s `chatProvider ChatProvider` field with a `chat ChatProviderSelector`
  (and `AgentCallExecutor`'s `agentProvider AgentProvider` with an `AgentProviderSelector`).
  In `Execute`, after `binding := resolver.Resolve(alias)`, call
  `cp, err := e.chat.ChatProviderFor(binding)` and then `cp.Chat(ctx, req)` (symmetric for
  agent). The resolver call and the rest of the executor flow are otherwise unchanged.
  This is the one executor-signature change Phase 3 makes; it supersedes the Phase-1
  "executor holds a single provider" shape and realizes Phase 2 Task 3's promise that the
  executor "still hands the resolver an alias and receives a backend" — the backend now
  comes via the selector rather than a hardwired field.
- The `adapter/provider` factory below implements both selector ports (its
  `ChatProviderFor`/`AgentProviderFor` are the methods). In Phase 1/2-only builds (before
  this task lands) a trivial selector that always returns the single claude-cli adapter is
  equivalent; this task generalizes it to the binding-driven factory.

### Modify: `cmd/arbitron/main.go`

- Where the chat/agent providers are constructed today (after Phase 1's port split the
  `switch cfg.LLM.Provider` builds `engine.ChatProvider`/`engine.AgentProvider`, and
  Phase 2 already constructs the widened `ProfileResolver` over the `ConnectionStore`),
  replace the Phase-1/2 per-kind default selection with the factory: construct
  `factory := provideradapter.NewFactory(deps)` and inject it into the executors as the
  `engine.ChatProviderSelector` / `engine.AgentProviderSelector` (the executors now hold a
  selector, not a raw provider — see the seam above), so `llm_call` resolves an alias to a
  binding and obtains the concrete `ChatProvider` via `factory.ChatProviderFor(binding)`
  and `agent_call` obtains its `AgentProvider` via `factory.AgentProviderFor(binding)` per
  the node's profile alias. This is **additive** to the Phase-1 (Task 6) and
  Phase-2 (Task 5) `main.go` wiring — it does not remove the resolver, the connection
  store, the validation-checker threading, or the claude-cli adapter construction those
  tasks established; it only swaps the static claude-cli-only provider selection for the
  binding-driven factory.
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

- **Happy path** — a `Factory` whose binding has `provider == "openrouter"` →
  `ChatProviderFor` returns an `*openrouter.Provider`; `provider == "azure-openai"` →
  `*azurefoundry.Provider`; an empty provider → the claude-cli `ChatAdapter`; an agent
  binding with a non-Claude provider → `AgentProviderFor` returns `*opencode.Provider`.
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
