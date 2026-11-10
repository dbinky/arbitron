# LunaRoute Provider — Design Phase 3: claude-code & opencode backends

**Depends on:** Phase 1 (endpoint helper, guarded reveal, capability table,
profile-save rules), Phase 2 (agent post-failure diagnosis wrapper, probe arm).
**Approach (approved):** A — extend each backend's existing seam.

## 1. Purpose and scope

Enable tool-using agent work on LunaRoute through the two backends that already
accept a per-call base URL and key:

- **claude-code** — Anthropic Messages dialect at the hosted root.
- **opencode** — OpenAI-compatible dialect at `<root>/v1`.

Supporting (background) calls stay inside the selected connection and on the
selected, compatible model. No new node types; selection is by profile only.

## 2. Existing architecture this phase extends

| Seam | Location | Use |
|---|---|---|
| claude-cli credential env | `adapter/claudecli/env.go:agentEnv` (via `procenv.CredentialEnv`) | unchanged: `ANTHROPIC_BASE_URL` + `ANTHROPIC_API_KEY` |
| claude-cli capability env | `adapter/claudecli/env.go:agentEnvWithRules` → `adapter/claudecli/shared.go:applyCapabilityEnv` | LunaRoute-specific env appended here; Claude context tiers never emitted for LunaRoute |
| Per-binding claude adapter | `adapter/claudecli/agent_adapter.go:NewAgentAdapterForBinding`, built in `Factory.AgentProviderFor` claude-code arm | unchanged construction |
| opencode descriptor table | `adapter/opencode/providers.go:providerDescriptors` | new `lunaroute` row |
| opencode config / argv / env | `adapter/opencode/config.go:buildConfig`, `args.go:buildArgs`/`modelLeaf`, `env.go:opencodeEnv` | consume the new row; config `baseURL` from the endpoint helper |
| Agent diagnosis wrapper | Phase 2, in `Factory.AgentProviderFor` | wraps both backends for LunaRoute bindings |

## 3. Components

### 3.1 claude-code LunaRoute environment (adapter/claudecli)
- **Job:** make Claude Code behave correctly against a non-Anthropic gateway.
- **Contract:** for a binding whose canonical provider is `lunaroute`, the agent
  env (after the unchanged credential skeleton) additionally carries:
  - the default Haiku model and the small/fast model set to **the binding's
    model** (background calls — titles, summaries, compaction — stay on a model
    LunaRoute serves and that the profile already validated);
  - gateway hint headers enabled;
  - max context tokens and max output tokens from the phase-1 capability table
    (phase-1 rule guarantees both are known for any saved claude-code LunaRoute
    profile; the emitter still fails closed if either is missing);
  - **no** Claude context-tier env (`200k`/`1m` emissions are suppressed for
    LunaRoute; phase-1 validation already rejects non-default tiers).
- **Base URL:** the endpoint helper's root form (Claude Code appends
  `/v1/messages`).
- **Credential:** stays `ANTHROPIC_API_KEY` (already wired, accepted by
  LunaRoute); revealed through the guarded reveal.
- **Non-LunaRoute bindings:** env byte-identical to today.
- **Change-safety:** one LunaRoute branch in the capability-env step; credential
  skeleton and its security tests untouched.

### 3.2 opencode LunaRoute descriptor (adapter/opencode)
- **Job:** describe LunaRoute to opencode's static provider config.
- **Contract (descriptor row):** opencode id `lunaroute`, credential env
  `LUNAROUTE_API_KEY`, npm package `@ai-sdk/openai-compatible`, provider block
  always emitted. Config block `options.baseURL` = endpoint helper's `/v1` form
  (re-checked by the existing SSRF backstop in `buildConfig`); `apiKey` references
  the env var (`{env:…}`), never the secret. Model argv `-m lunaroute/<id>`.
- **Explicitly not used:** `@lunaroute/opencode-extension`, interactive login, or
  any on-disk auth store.
- **Descriptor invariants:** the row passes `validateDescriptors` (not keyless,
  not aws).

### 3.3 Diagnosis wrapper coverage
- Both backends' providers are returned through the phase-2 wrapper for LunaRoute
  bindings, so opencode's bare `exit status 1` becomes a classified error.

## 4. Data flow

**claude-code:** `agent_call` node → resolver binding (guarded reveal) →
`AgentProviderFor` (claude-code arm, wrapped) → `NewAgentAdapterForBinding` →
`agentEnvWithRules`: credential env (`ANTHROPIC_BASE_URL=<root>`,
`ANTHROPIC_API_KEY=lr_…`) + LunaRoute env (Haiku/small-fast = model, hints,
limits from table) → `claude` subprocess → `<root>/v1/messages`.

**opencode:** `agent_call` → binding → `AgentProviderFor` (opencode arm, wrapped) →
descriptor `lunaroute` → env `LUNAROUTE_API_KEY=lr_…`; config provider block
`{npm: openai-compatible, baseURL: <root>/v1, apiKey: {env:LUNAROUTE_API_KEY}}`;
argv `-m lunaroute/<id>` → `opencode` subprocess → `<root>/v1/chat/completions`.

## 5. Data Provenance

| Input | Consumer | Production producer |
|---|---|---|
| Binding (provider, base URL, model, backend, reveal) | both adapters | `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve` (guarded reveal from phase 1) |
| Hosted root / `/v1` URL | both adapters | phase-1 endpoint helper over the stored (normalized) `Connection.BaseURL` |
| `lr_` key | both adapters | `ConnectionStore.Reveal` via guarded reveal |
| Background (Haiku / small-fast) model | claude-code env | the binding's own `Model` (`domain/provider.Profile.Model` → `ResolvedBinding.Model`) — no separate source |
| Max context / max output tokens | claude-code env | phase-1 capability table (`capabilities.default.yaml` → `LoadCapabilityRules` → `adapter/claudecli/provider.go:Config.CapabilityRules`, set in `cmd/arbitron/main.go` on the claude agent Config the factory receives as `Deps.ClaudeAgentConfig`). **UNRESOLVED DEPENDENCY: values unpopulated until the Tier-2 live run; until then claude-code + LunaRoute profiles cannot be saved (phase-1 rule).** |
| Exact Claude Code env var names for Haiku/small-fast/hint/limits | claude-code env | LunaRoute's Claude Code docs as recorded in `docs/ideas/lunaroute-provider.md` §2. **UNRESOLVED DEPENDENCY: verify the names are honoured by the Claude Code version this repo pins (live run).** |
| Tool-call support on Messages and chat-completions dialects | both backends | **UNRESOLVED DEPENDENCY: idea-doc open question 1; must be proven per model in the Tier-2 live run and recorded as the table's backend compatibility.** |
| opencode `-m` prefix form for a custom openai-compatible provider | opencode argv | **UNRESOLVED DEPENDENCY: `lunaroute/<id>` assumed; verify against the pinned opencode version in the live run (known opaque-failure trap).** |
| Capability rules object | claude adapter | `cmd/arbitron/main.go` `capabilityRules` → `claudecli.Config.CapabilityRules` → `agentEnvWithRules` (existing) |

### Deferred to plans
- **Wiring:** none new at the composition root beyond phase 2 (the factory already
  receives `CapabilityRules`; the wrapper's probe selector is wired in phase 2). A
  test must prove the production factory emits the LunaRoute env for a
  claude-code LunaRoute binding (not only a direct adapter unit test).
- **Data population:** capability-table limits and backend compatibility from the
  live run.
- **Samples/docs:** documented example profiles (connection + chat + claude-code +
  opencode on LunaRoute) in the provider docs / PROFILES guidance — documentation
  only; no bundled sample workflow is changed to depend on LunaRoute.

## 6. Integration seams

- claude-cli capability-env step — single LunaRoute branch.
- opencode descriptor table — single row; config/argv/env builders unchanged.
- Factory agent arm — returns wrapped providers for LunaRoute only.

## 7. Error handling

- Missing limit at emit time → fail closed before launch (should be unreachable
  given phase-1 validation; defended anyway).
- Unknown or misconfigured descriptor → existing `ErrUnknownProvider` fail-closed.
- Launch/run failure → phase-2 diagnosis wrapper classifies.
- Key appears only in the child env: never argv, never opencode config file,
  never logs (asserted).
- No substitution of model (background model is the selected model), backend or
  provider.

## 8. Testing — STRICT TDD (all five categories required)

- **Happy-path:** production-factory-built claude-code LunaRoute binding yields env
  with root base URL, `lr_` key, Haiku/small-fast = model, hint flag, both limits;
  opencode LunaRoute binding yields config block with `<root>/v1`, env-referenced
  key, and `-m lunaroute/<id>`.
- **Success:** non-LunaRoute claude-code and opencode bindings produce
  byte-identical env/config/argv to today (golden tests); wrapper passes success
  through.
- **Failure:** claude-code LunaRoute binding with a missing limit fails before
  launch; Claude `200k`/`1m` tier on a LunaRoute binding emits nothing (and is
  rejected at save by phase 1); fake runner exit 1 → classified error via wrapper.
- **Error:** reveal failure → no env produced, no launch; guarded reveal with a
  non-`lr_` stored key → credential error, no launch; SSRF backstop rejects a
  tampered non-hosted base URL in opencode config.
- **Edge-case:** model ids with dots/dashes/`-background`; vendor-alias model on
  claude-code (family check already passed at save); base URL stored with
  trailing slash; key never present in argv, config JSON, or captured logs.
- **Tier 2 (live, gated):** a representative tool-using agent task (create a file,
  run a command, verify the workspace result) finishes successfully on claude-code
  and on opencode with a compatible LunaRoute model each; confirms env var names,
  tool-call support, and opencode `-m` form, closing this phase's UNRESOLVED items.
