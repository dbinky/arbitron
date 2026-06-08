# Phase 2 — Task 2: Factory dispatches on `binding.Backend`

**Objective:** Make `Factory.AgentProviderFor` route by the **backend axis**
(`binding.Backend`), not by the provider: `claude-code` → the claude-cli agent
adapter (even when the binding carries a non-claude connection); `opencode` →
`adapter/opencode`; unset → the deployment default. `ChatProviderFor` is
unchanged (chat always routes by provider).

**Dependencies:**
- Task 1 (adds `ResolvedBinding.Backend` and the effective-backend computation
  that sets it before `AgentProviderFor` is called).

**Spec/design anchors:**
- `docs/specs/org-readiness-spec.md` — Phase 2 ("The agent backend is
  independent of the provider. Either backend can be pointed at the gateway or at
  a directly-configured provider.").
- `docs/superpowers/specs/org-readiness-design-phase-2.md` — "Factory routes on
  backend" and "Background (current behavior)" (today the factory dispatches on
  the *provider*; this task replaces that with backend dispatch).

---

## Files to create / modify

Modify:
- `adapter/provider/factory.go`:
  - `AgentProviderFor(b)` dispatches on `domprovider`-canonical `b.Backend`
    (lowercased/trimmed), NOT `b.Provider`:
    - `claude-code` → return a claude-cli agent adapter configured for this
      binding. **Task 3** builds the binding-aware adapter; in THIS task the arm
      returns `f.deps.ClaudeAgent` (the pre-built adapter) so backend routing
      lands first and Task 3 swaps in per-binding injection without re-touching
      the dispatch.
    - `opencode` → `opencode.New(...)` exactly as the current default arm.
    - `""` (unset) → the factory's deployment-default backend (a new
      `Deps.DefaultAgentBackend`, defaulting to `claude-code`); apply the same
      switch on the defaulted value.
    - any other value → a new `ErrUnknownBackend` (fail-closed). This is
      defense-in-depth; Task 1's validator already rejects bad backends at load,
      but the factory must not silently fall through.
  - Add `Deps.DefaultAgentBackend string` so the composition root names the
    fallback once (mirrors the executor default from Task 1; both read
    `cfg.LLM.DefaultAgentBackend`).
  - Add a shared backend-constant reference (reuse Task 1's
    `workflow.BackendClaudeCode` / `BackendOpencode`, or define adapter-local
    copies if the import direction is wrong — prefer referencing the domain
    constants to keep one vocabulary).
  - Keep the INVARIANT comment block accurate: update it to say the AGENT tier
    routes by **backend** (claude-code → claude-cli adapter; opencode →
    opencode), and that a claude-code binding may now carry a non-claude
    connection (endpoint+key injected in Task 3).
- `cmd/arbitron/main.go` (`NewFactory(Deps{...})` at `cmd/arbitron/main.go:493`)
  — set `DefaultAgentBackend: cfg.LLM.DefaultAgentBackend`.

Create:
- Add cases to `adapter/provider/factory_test.go` (or a new
  `factory_backend_test.go` in package `provider_test`).

---

## TDD plan (write tests first)

Use the existing `newTestFactory(t)` helper and `agentBinding(provider)` helper
in `factory_test.go`. Add a backend-aware binding helper:

```go
func agentBindingBackend(providerName, backend string) provider.ResolvedBinding {
    return provider.ResolvedBinding{
        Kind: provider.ProfileKindAgent, Provider: providerName,
        Backend: backend, Model: "m",
    }
}
```

Construct the factory with `DefaultAgentBackend: "claude-code"` in a new helper
variant so the unset-backend arm is exercised deterministically.

### Category: happy path — backend selects the adapter

1. **`claude-code` backend → claude-cli agent adapter, claude-cli provider.**
   `agentBindingBackend("claude-cli", "claude-code")` →
   `assert.IsType((*claudecli.AgentAdapter)(nil), got)` (or the binding-aware
   type Task 3 introduces; in this task it is `f.deps.ClaudeAgent`'s type).
2. **`claude-code` backend WITH a non-claude connection → still claude-cli
   adapter.** `agentBindingBackend("litellm", "claude-code")` → claude-cli agent
   adapter. This is the core decoupling: provider `litellm` no longer forces
   opencode (contrast the old behavior where any non-claude provider → opencode).
3. **`opencode` backend → opencode adapter.**
   `agentBindingBackend("openai", "opencode")` →
   `assert.IsType((*opencode.Adapter)(nil), got)` (match the concrete opencode
   type used elsewhere in this test file).
4. **`opencode` backend WITH a claude-cli provider.**
   `agentBindingBackend("claude-cli", "opencode")` → opencode adapter. Proves
   backend, not provider, drives the choice. (Note: opencode will fail closed at
   RunAgent because `claude-cli` has no opencode mapping — that is the existing,
   correct behavior and is NOT this task's concern; assert only the type
   selected here.)

### Category: default (unset backend)

5. **unset backend → deployment default (claude-code).**
   `agentBindingBackend("litellm", "")` with `DefaultAgentBackend: "claude-code"`
   → claude-cli agent adapter.
6. **unset backend → opencode default.** Same binding, factory built with
   `DefaultAgentBackend: "opencode"` → opencode adapter. Proves the default is a
   single configured knob.

### Category: validation / error (fail-closed)

7. **unknown backend → `ErrUnknownBackend`.**
   `agentBindingBackend("litellm", "gpt-cli")` → `(nil, err)` with
   `errors.Is(err, adapterprovider.ErrUnknownBackend)`, message naming the bad
   value. Never returns a silently-wrong adapter.
8. **claude-code arm but `ClaudeAgent` nil → clear configuration error.** Build a
   factory with `ClaudeAgent: nil`, `DefaultAgentBackend: "claude-code"`; a
   `claude-code` binding → a non-nil error explaining the claude-cli agent
   adapter is not configured (mirror the existing nil-`ClaudeAgent` guard at
   `factory.go:172`).

### Category: boundary / canonicalization

9. **whitespace / case tolerance is consistent with Task 1's validator.** Decide
   ONE rule and assert it: the validator (Task 1) only accepts exact lowercase,
   so the factory should treat anything the validator rejected as unknown. Add a
   case `agentBindingBackend("openai", "OPENCODE")` → `ErrUnknownBackend` (the
   validator already blocked this at load; the factory stays strict). Keep the
   factory and validator vocabularies identical.

### Category: chat unchanged (regression guard)

10. **`ChatProviderFor` ignores `Backend`.** A chat binding with
    `Backend: "opencode"` and `Provider: "litellm"` still returns the openrouter
    chat adapter (backend has no effect on the chat tier). Confirms we did not
    accidentally widen chat dispatch.

---

## Implementation steps (after each red test)

1. Add `ErrUnknownBackend = errors.New("provider: unknown agent backend")` and
   `Deps.DefaultAgentBackend` → tests compile.
2. Rewrite `AgentProviderFor`:
   ```go
   backend := domprovider.NormalizeBackend(b.Backend) // trim+lower, or reuse Canonicalize-style helper
   if backend == "" { backend = f.deps.DefaultAgentBackend }
   switch backend {
   case workflow.BackendClaudeCode:
       if f.deps.ClaudeAgent == nil { return nil, errors.New(...) }
       return f.deps.ClaudeAgent, nil   // Task 3 swaps in a binding-aware adapter
   case workflow.BackendOpencode:
       return opencode.New(opencode.Config{Binding: b, ...}), nil
   default:
       return nil, fmt.Errorf("%w: %q", ErrUnknownBackend, b.Backend)
   }
   ```
   → tests 1–9 green.
3. Update the INVARIANT comment to describe backend dispatch → no behavior
   change; documentation correctness.
4. Set `DefaultAgentBackend: cfg.LLM.DefaultAgentBackend` in `main.go` →
   integration wiring.
5. Confirm `ChatProviderFor` untouched → test 10 green.

---

## Acceptance criteria (tied to spec Phase 2)

- "Either backend can be pointed at the gateway or at a directly-configured
  provider": a `claude-code` binding with a `litellm` connection selects the
  claude-cli adapter (test 2); an `opencode` binding with a non-claude provider
  selects opencode (test 3) — backend and provider are now orthogonal.
- "An agentic node that does not name a backend uses [the] default" (tests 5–6).
- Fail-closed: an unknown backend never silently runs (test 7).

---

## Security notes

- **Fail-closed routing.** The `default` arm returns `ErrUnknownBackend`, never a
  fallback adapter — a corrupted/unexpected backend value cannot silently pick a
  high-privilege agent path (design "Error handling / security").
- No secrets are handled here. The opencode arm still passes `Binding` (with its
  lazy `RevealKey`) into `opencode.New`, where the key is revealed per call in
  `opencodeEnv` (unchanged). The claude-code arm's key injection arrives in
  Task 3; this task must NOT add key handling.
- `OpencodeAllowAllTools` posture passes through unchanged for the opencode arm.
