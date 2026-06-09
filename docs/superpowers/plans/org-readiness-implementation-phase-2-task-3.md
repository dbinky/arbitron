# Phase 2 — Task 3: claude-cli endpoint/key injection at call time

**Objective:** Let the claude-cli agent backend target the org gateway (or any
configured provider) by injecting `ANTHROPIC_BASE_URL` (+ `ANTHROPIC_API_KEY`
via `RevealKey`) into the subprocess env **per call** from the
`ResolvedBinding`. An empty connection leaves the subscription path unchanged
(no base-url, no key). The key is revealed per call only — never logged, never on
the static allowlist; the model flag comes from the binding.

**Dependencies:**
- Task 1 (`ResolvedBinding.Backend`, effective-backend on the binding).
- Task 2 (factory's `claude-code` arm — this task makes that arm hand the
  binding to a binding-aware claude agent adapter instead of the static
  `Deps.ClaudeAgent`).

**Spec/design anchors:**
- `docs/specs/org-readiness-spec.md` — Phase 2 ("Any node can use the model
  gateway … Neither agent backend requires a personal vendor subscription"; "a
  node can be configured to reach a directly-configured provider instead of the
  gateway and still run").
- `docs/superpowers/specs/org-readiness-design-phase-2.md` — "Claude-cli
  endpoint injection (call-time)" and "Credential isolation".

---

## Background (what makes this non-trivial)

Today the claude-cli `AgentAdapter` is constructed once
(`cmd/arbitron/main.go:405`, pre-built into `Deps.ClaudeAgent`) and builds its
subprocess env from `procenv.BaseEnv()` inside `spec()`
(`adapter/claudecli/agent_adapter.go:52`). It has no access to a per-node
binding, so it cannot point at a custom endpoint/key. This task threads the
binding to the claude-cli agent run so the env is built per call.

opencode already does exactly this pattern in `adapter/opencode/env.go`
(`opencodeEnv(ctx, binding)`): reveal the key at call time, append `*_BASE_URL`
+ `*_API_KEY` to `procenv.BaseEnv()`, return nil-with-error on any failure so a
partial env never launches. Mirror that contract for claude-cli.

---

## Files to create / modify

Modify:
- `adapter/procenv/procenv.go` — add `ANTHROPIC_BASE_URL` to `baseAllowlist`
  (non-secret endpoint var). Do **NOT** add `ANTHROPIC_API_KEY` — the key is
  injected per call only.
- `adapter/claudecli/agent_adapter.go`:
  - `AgentAdapter` gains an optional per-binding field
    (`binding provider.ResolvedBinding`, zero-value = subscription path).
  - `NewAgentAdapter(cfg)` stays (subscription/back-compat); add
    `NewAgentAdapterForBinding(cfg, binding)` (or a `WithBinding(binding)`
    shallow-copy constructor) the factory calls per binding.
  - `spec(mission)` builds `Env` via a new helper `agentEnv(ctx, binding)`
    instead of the bare `procenv.BaseEnv()`. Because env construction now needs
    `ctx` (RevealKey) and can error, refactor `RunAgent` to build the spec/env
    after `validateWorkDir` and before `collectResult`, propagating an env-build
    error (no partial-env launch).
- `adapter/claudecli/shared.go` (or a new `adapter/claudecli/env.go`) — add the
  `agentEnv` helper that mirrors `opencode.opencodeEnv`'s safety contract:
  - start from `procenv.BaseEnv()`;
  - if `binding.BaseURL != ""` → append `ANTHROPIC_BASE_URL=<baseURL>`;
  - if `binding.RevealKey != nil` → reveal; on a non-empty key append
    `ANTHROPIC_API_KEY=<key>`; on RevealKey error return `(nil, err)` (no
    partial env);
  - empty `BaseURL` AND empty/absent key → return `procenv.BaseEnv()` unchanged
    (subscription path);
  - reject a base-name collision defensively (reuse the opencode
    `rejectBaseNames` idea — but note `ANTHROPIC_BASE_URL` is now ALLOWED in the
    base set, so the collision guard must target only the KEY var, which is not
    allowlisted; keep the guard scoped to `ANTHROPIC_API_KEY`).
- `adapter/provider/factory.go` — the `claude-code` arm (from Task 2) builds a
  binding-aware claude agent adapter. The factory needs the claude `Config`
  (binary, workspace root, timeout, allowed-tools, skip-permissions,
  max-response-bytes) so it can construct a per-binding adapter; add a
  `Deps.ClaudeAgentConfig claudecli.Config` (the agent cfg already built at
  `cmd/arbitron/main.go:401-403`) and construct
  `claudecli.NewAgentAdapterForBinding(deps.ClaudeAgentConfig, b)`. Keep
  `Deps.ClaudeAgent` for the empty-binding/subscription fast path OR drop it in
  favor of always-construct-from-config (decide: prefer always-construct so the
  injection path is uniform; the subscription path is just an empty binding).
- `cmd/arbitron/main.go` — pass the agent `claudecli.Config` into `Deps`
  (`ClaudeAgentConfig: agentCfg`) at `cmd/arbitron/main.go:493`.

Model flag: `mission.Model` is already set from `binding.Model` in the executor
(`executor_agent.go:84`) and flows to `--model` via `buildArgs`
(`claude.go:78-84`). No change needed beyond confirming a test asserts it.

Create:
- `adapter/claudecli/env_test.go` (or `agent_adapter_env_test.go`) — env
  injection scenarios using a fake `CommandRunner` that captures `RunSpec.Env`.

---

## TDD plan (write tests first)

Use a capturing `CommandRunner` (see `factory_test.go::noopRunner` for the seam,
and the opencode env tests `adapter/opencode/env_test.go` for the RevealKey /
"secret not logged" patterns). The runner records the `RunSpec` it receives and
returns a single canned `result` stream-json line so `RunAgent` succeeds.

### Category: happy path — gateway injection

1. **base_url + key present → env carries both.** Binding
   `{BaseURL: "https://litellm.internal:4000", RevealKey: ->"sk-test", Model:
   "gpt-4o"}`. After `RunAgent`, the captured `RunSpec.Env` contains
   `ANTHROPIC_BASE_URL=https://litellm.internal:4000` AND
   `ANTHROPIC_API_KEY=sk-test`.
2. **model flag from binding.** Same run → captured `RunSpec.Args` contains
   `--model gpt-4o` (the value came from `mission.Model` = `binding.Model`).
3. **base_url-only (keyless connection) → base url, no key var.** Binding
   `{BaseURL: ..., RevealKey: ->""}` → env has `ANTHROPIC_BASE_URL`, has NO
   `ANTHROPIC_API_KEY=`.

### Category: subscription path unchanged

4. **empty connection → subscription env.** Binding `{BaseURL: "", RevealKey:
   nil}` (or RevealKey -> "") → env has NO `ANTHROPIC_BASE_URL` and NO
   `ANTHROPIC_API_KEY`; it equals `procenv.BaseEnv()`. Proves the subscription
   path is byte-for-byte unchanged (regression guard for existing deployments).
5. **back-compat constructor.** `NewAgentAdapter(cfg)` (no binding) still
   produces the subscription env (zero-value binding), so the existing
   `Deps.ClaudeAgent` / streaming / interactive call sites are unaffected.

### Category: error (fail-closed, no partial env)

6. **RevealKey error aborts the run.** Binding `{BaseURL: ..., RevealKey:
   ->error}` → `RunAgent` returns the wrapped error and the runner is NEVER
   invoked (no subprocess launched with a partial env). Mirror
   `opencode.env_test.go::TestOpencodeEnv_BedrockRevealError`.
7. **key-var collision guard.** A defensive unit test on `agentEnv`: it never
   emits two `ANTHROPIC_API_KEY=` entries and the key var is not in
   `procenv.IsBaseName` (so it cannot be shadowed/duplicated by the base env).

### Category: security — key isolation

8. **key never appears in args or logs.** Run with key `sk-secret-123`; assert
   the secret substring is absent from `RunSpec.Args` and from any captured
   `slog` output (install a test `slog` handler writing to a buffer, like
   `factory_test.go` already imports `log/slog` + `bytes`). The key appears ONLY
   in `RunSpec.Env`. Mirror
   `adapter/opencode/env_test.go::TestOpencodeEnv_BedrockSecretsNotLogged`.
9. **key not on the static allowlist.** Unit-assert
   `procenv.IsBaseName("ANTHROPIC_API_KEY") == false` AND
   `procenv.IsBaseName("ANTHROPIC_BASE_URL") == true` (base url allowlisted so a
   subprocess may also inherit an operator-set one; the key is per-call only).

### Category: factory wiring (integration)

10. **factory claude-code arm injects the binding.** Build the factory with a
    capturing runner in `Deps.ClaudeAgentConfig.Runner`; call
    `AgentProviderFor(agentBindingBackend("litellm", "claude-code"))` with a
    base_url+key binding, then `RunAgent` → captured env carries
    `ANTHROPIC_BASE_URL` + `ANTHROPIC_API_KEY`. Proves Task 2's arm now hands the
    binding through (closes the loop from Task 2's placeholder).

### Category: procenv

11. **allowlist passthrough.** With `ANTHROPIC_BASE_URL` set in the parent
    process env, `procenv.BaseEnv()` now includes it (previously stripped); add
    a case to `adapter/procenv/procenv_test.go`. Confirm `ANTHROPIC_API_KEY` set
    in the parent is STILL stripped (negative case — must remain stripped).

---

## Implementation steps (after each red test)

1. `procenv`: add `ANTHROPIC_BASE_URL` to `baseAllowlist` → tests 9, 11.
2. `agentEnv(ctx, binding)` helper in `adapter/claudecli` → tests 1,3,4,6,7,8.
3. Refactor `AgentAdapter` to hold a binding + build env via `agentEnv` in
   `RunAgent` (after `validateWorkDir`, propagating env error before launch);
   add `NewAgentAdapterForBinding` → tests 2,5.
4. Factory `claude-code` arm builds `NewAgentAdapterForBinding(
   deps.ClaudeAgentConfig, b)`; add `Deps.ClaudeAgentConfig`; wire in main.go →
   test 10.
5. Confirm `--model` already flows from binding via the executor → test 2.

---

## Acceptance criteria (tied to spec Phase 2)

- "An agentic node configured to use Claude Code completes a task with its model
  calls routed through the gateway": claude-code backend + gateway connection →
  env carries `ANTHROPIC_BASE_URL` + key + correct `--model` (tests 1,2,10).
- "Neither agent backend requires a personal vendor subscription": the
  connection-backed path injects a key, no `claude /login` required.
- "A node can be configured to reach a directly-configured provider instead of
  the gateway and still run": same injection mechanism with a non-gateway
  `BaseURL`.
- Subscription path preserved for local/dev (test 4).

---

## Security notes

- **Per-call reveal only.** The key is fetched via `binding.RevealKey(ctx)` at
  launch time and placed ONLY in that subprocess's `RunSpec.Env`; it is never
  cached on the adapter, never an argv, never logged (tests 6,8). Matches the
  opencode credential-isolation contract.
- **Key never on the allowlist.** `ANTHROPIC_API_KEY` is deliberately absent
  from `procenv.baseAllowlist` (test 9), so a key the engine itself started with
  cannot leak into agent subprocesses; only the per-call injected value is
  present. `ANTHROPIC_BASE_URL` (non-secret endpoint) is allowlisted.
- **No partial-env launch.** A RevealKey failure returns `(nil, err)` and the
  subprocess is never started (test 6) — fail-closed, identical to
  `opencodeEnv`.
- **Stderr/redaction unchanged.** The runner's `decorateWithStderr` already caps
  and summarizes stderr to avoid leaking secrets; this task adds no new log of
  the env.
