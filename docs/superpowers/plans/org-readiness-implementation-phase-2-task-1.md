# Phase 2 — Task 1: The Backend axis through the domain

**Objective:** Introduce a `Backend` selection axis (`""` / `claude-code` /
`opencode`) that flows node → profile → deployment-default, is validated at
workflow load, and lands on `ResolvedBinding.Backend` with the effective value
computed (precedence **node > profile > deployment default**) in the agent
executor.

**Dependencies:** None (first task of Phase 2). Subsequent tasks consume
`ResolvedBinding.Backend`:
- Task 2 (factory dispatch on `binding.Backend`)
- Task 3 (claude-cli injection reads the same binding)

**Spec/design anchors:**
- `docs/specs/org-readiness-spec.md` — Phase 2 ("Agent backend is selectable per
  node"; "A deployment default applies when a node does not choose"; "The agent
  backend is independent of the provider").
- `docs/superpowers/specs/org-readiness-design-phase-2.md` — "The backend axis
  (node → profile → binding)" and "Error handling / security" (fail-closed on
  bad backend).

---

## Files to create / modify

Modify:
- `domain/workflow/workflow.go` — add `Backend string` to `NodeConfig`.
- `domain/workflow/validation.go` — validate `NodeConfig.Backend` against the
  allowed set in `validateNodeConfigs` (agent_call / interactive_agent only).
- `domain/config/config.go` — add `Backend string` to `ProfileConfig`; add a
  deployment default field to `LLMConfig` (`DefaultAgentBackend string`).
- `domain/provider/provider.go` — add `Backend string` to `Profile` and to
  `ResolvedBinding`.
- `adapter/provider/static.go` — `StaticProfileResolver.Resolve` copies
  `Profile.Backend` → `ResolvedBinding.Backend` on BOTH the no-connection
  (subscription) arm and the connection-backed arm.
- `adapter/config/*.go` (the `ProfilesFromConfig` mapper, current call site at
  `cmd/arbitron/main.go:460`) — map `ProfileConfig.Backend` →
  `provider.Profile.Backend`.
- `domain/engine/executor_agent.go` — compute the effective backend
  (node > profile > default) after `resolver.Resolve`, set it on the binding
  before `selector.AgentProviderFor(binding)`. The executor needs the deployment
  default; add it as a field on `AgentCallExecutor` (constructor parameter).
- `cmd/arbitron/main.go` — load `llm.defaultagentbackend` (default
  `claude-code`) and pass it to `NewAgentCallExecutor` (call site at
  `cmd/arbitron/main.go:515`).
- Config loader (koanf binding for `llm.defaultagentbackend`) — wherever
  `LLMConfig` is populated and defaulted; floor an empty value to `claude-code`.

Create:
- `domain/workflow/validation_backend_test.go` (or add cases to the existing
  validation test file) — backend-validation scenarios.
- `domain/engine/executor_agent_backend_test.go` — effective-backend precedence
  scenarios.
- Backend-copy cases added to `adapter/provider/static_test.go`.

---

## Shared constants

Add a single source of truth for the allowed backend values so validation,
the executor default, and Task 2's factory all agree on the vocabulary. Place in
`domain/workflow` (the validation owner) and re-export / reference from the
engine:

```go
// domain/workflow/workflow.go
const (
    BackendUnset     = ""            // inherit (node→profile→default)
    BackendClaudeCode = "claude-code"
    BackendOpencode   = "opencode"
)
```

The deployment-default floor (`claude-code`) lives in the config loader, not the
domain, so the domain stays free of a deployment policy.

---

## TDD plan (write tests first, in order)

### Category: validation (workflow load, fail-closed)

`domain/workflow/validation_backend_test.go`:

1. **happy — empty backend valid.** An `agent_call` node with
   `Config.Backend == ""` produces no `INVALID_AGENT_BACKEND` error.
2. **happy — `claude-code` valid.** `Config.Backend == "claude-code"` → no
   error.
3. **happy — `opencode` valid.** `Config.Backend == "opencode"` → no error.
4. **validation — unknown backend rejected.** `Config.Backend == "gpt-cli"` →
   exactly one `ValidationError{Code: "INVALID_AGENT_BACKEND", NodeID: <id>}`
   with a message naming the node and the bad value. (Fail-closed: design
   "Error handling / security".)
5. **boundary — case sensitivity.** `Config.Backend == "Claude-Code"` is
   rejected (the allowed set is exact-match lowercase; document that authors use
   the canonical spelling). Asserts we do not silently accept a near-miss.
6. **boundary — backend on a non-agent node is ignored.** An `llm_call` node
   with `Config.Backend == "opencode"` produces NO backend error (backend
   applies only to agent execution). Pick: validate backend ONLY for
   `NodeTypeAgentCall` and `NodeTypeInteractiveAgent`. Add a case proving an
   `llm_call` with a bogus backend is not flagged (so chat authors are not
   blocked by an irrelevant field).

### Category: resolver copy (adapter)

Add to `adapter/provider/static_test.go`:

7. **happy — subscription arm copies backend.** A profile with
   `ConnectionRef == ""` and `Backend == "claude-code"` resolves to a binding
   with `Backend == "claude-code"` (and still `Provider == "claude-cli"`).
8. **happy — connection arm copies backend.** A profile with a real
   `ConnectionRef` and `Backend == "opencode"` resolves to a binding with
   `Backend == "opencode"` (Provider taken from the connection as today).
9. **boundary — empty backend stays empty.** A profile with no backend resolves
   to `ResolvedBinding{Backend: ""}` (no implicit default injected at the
   resolver layer; the default is the executor's job).

### Category: effective-backend precedence (engine)

`domain/engine/executor_agent_backend_test.go` — use the existing
`staticAgentSelector` / fake resolver test seams (`helpers_test.go`,
`executor_agent_test.go`). Capture the binding the selector receives.

10. **happy — node overrides profile and default.** node.Backend =
    `opencode`, profile/binding.Backend = `claude-code`, executor default =
    `claude-code` → selector receives `binding.Backend == "opencode"`.
11. **happy — profile overrides default.** node.Backend = `""`,
    binding.Backend = `opencode`, default = `claude-code` → selector receives
    `opencode`.
12. **happy — default applies.** node.Backend = `""`, binding.Backend = `""`,
    default = `claude-code` → selector receives `claude-code`.
13. **boundary — all empty + empty default.** If the executor default is also
    `""` the binding's effective backend is `""` (Task 2's factory then applies
    its own deployment default). Document this as the single fallback seam so the
    default is configured in exactly one place (main.go floor). Assert the
    executor does NOT invent `claude-code` itself — keep the policy in the
    composition root.
14. **integration — precedence does not disturb other binding fields.** Model,
    Provider, AllowedTools, RevealKey on the binding are unchanged after the
    effective-backend computation (only `Backend` is mutated).

---

## Implementation steps (after each red test)

1. Add `Backend` to `NodeConfig` (+ `Backend*` constants) → tests 1–6 compile.
2. Add `validateAgentBackend` to `validateNodeConfigs` (switch arm for
   `NodeTypeAgentCall` / `NodeTypeInteractiveAgent`): reject any non-empty value
   not in `{claude-code, opencode}` with `INVALID_AGENT_BACKEND` → tests 1–6
   green.
3. Add `Backend` to `domain/provider.Profile` and `ResolvedBinding`.
4. Add the copy to `StaticProfileResolver.Resolve` on both arms → tests 7–9.
5. Add `Backend` to `domain/config.ProfileConfig`; map it in `ProfilesFromConfig`.
6. Add `LLMConfig.DefaultAgentBackend`; in the config loader, default an empty
   value to `claude-code` (floor); bind `llm.defaultagentbackend`.
7. Add a `defaultAgentBackend string` field + constructor param to
   `AgentCallExecutor`; compute `effective := firstNonEmpty(node.Config.Backend,
   binding.Backend, e.defaultAgentBackend)` and set `binding.Backend = effective`
   before `selector.AgentProviderFor(binding)` → tests 10–14.
8. Thread the default from `cfg.LLM.DefaultAgentBackend` into
   `NewAgentCallExecutor` at `cmd/arbitron/main.go:515`.

---

## Acceptance criteria (tied to spec Phase 2)

- "Agent backend is selectable per node": a node's `Config.Backend` reaches the
  binding (tests 10–11).
- "A deployment default applies when a node does not choose": empty node +
  empty profile → deployment default `claude-code` (test 12); default is
  configured headlessly via `llm.defaultagentbackend`.
- "The agent backend is independent of the provider": backend is carried on a
  separate field; precedence computation never reads/writes `binding.Provider`
  (test 14).
- Unknown backend → workflow_validate fails before any run (tests 4–5;
  fail-closed per design).

---

## Security notes

- **Fail-closed at load.** An unknown backend is a hard `ValidationError`
  (severity error), gating `workflow_validate` — never a silent fallback that
  could mis-route an agent (design "Error handling / security").
- The backend axis carries no secrets; it only selects which adapter Task 2
  dispatches to. No key/endpoint handling in this task.
- The deployment default lives in exactly one place (config loader floor +
  executor field) so a misconfiguration cannot silently change which backend
  runs across the deployment.
