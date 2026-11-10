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
| opencode config / argv / env | `adapter/opencode/config.go:buildConfig`, `args.go:buildArgs`/`modelLeaf`, `env.go:opencodeEnv` | consume the new row; explicitly derive `/v1` with the endpoint helper before config emission |
| Agent diagnosis wrapper | Phase 2, in `Factory.AgentProviderFor` | wraps both backends for LunaRoute bindings |

## 3. Components

### 3.1 claude-code LunaRoute environment (adapter/claudecli)
- **Job:** make Claude Code behave correctly against a non-Anthropic gateway.
- **Contract:** for a binding whose canonical provider is `lunaroute`, the agent
  env (after the unchanged credential skeleton) additionally carries:
  - main and every supporting-model selector set to **the binding's model**
    (including Haiku and small/fast selectors where honoured); verified config
    must prevent background titles, summaries and compaction from inheriting a
    subscription, ambient provider or incompatible helper model;
  - gateway hint headers enabled;
  - max context tokens and max output tokens from the phase-1 capability table
    (phase-1 rule guarantees both are known for any saved claude-code LunaRoute
    profile; launch revalidation also covers changed routes/pins/config, and
    the emitter still fails closed if either is missing);
  - **no** Claude context-tier env (`200k`/`1m` emissions are suppressed for
    LunaRoute; phase-1 validation already rejects non-default tiers).
- **Base URL:** the endpoint helper's root form (Claude Code appends
  `/v1/messages`).
- **Credential:** proposed `ANTHROPIC_API_KEY` path reuses the existing skeleton,
  with acceptance by LunaRoute still gated by **UNRESOLVED DEPENDENCY LR-01**;
  reveal through the guard. Verify effective harness config, not just presence
  of env variables. LR-05 gates claims that all supporting calls honour them.
- **Non-LunaRoute bindings:** env byte-identical to today.
- **Change-safety:** LunaRoute branch in the capability-env step; reuse the
  credential skeleton and retain/extend its security tests. Before launch apply
  phase-1 compatibility validation and exact effort emission; suppress generic
  flooring. Non-default LunaRoute settings require evidence for this backend.

### 3.2 opencode LunaRoute descriptor (adapter/opencode)
- **Job:** describe LunaRoute to opencode's static provider config.
- **Contract (descriptor row):** opencode id `lunaroute`, credential env
  `LUNAROUTE_API_KEY`, npm package `@ai-sdk/openai-compatible`, provider block
  always emitted. Config block `options.baseURL` = endpoint helper's `/v1` form
  (phase-1 helper enforces the hosted destination; the existing SSRF backstop
  alone only enforces general URL safety); `apiKey` references
  the env var (`{env:…}`), never the secret. Model argv `-m lunaroute/<id>`.
- **Explicitly not used:** `@lunaroute/opencode-extension`, interactive login, or
  any on-disk auth store.
- **Descriptor invariants:** the row passes `validateDescriptors` (not keyless,
  not aws).
- **Required config work:** the current builder copies `binding.BaseURL`
  verbatim, so a descriptor row alone would emit the stored root without `/v1`.
  Add the LunaRoute URL derivation explicitly. The current `providerBlock` also
  has no custom model definitions or small-model field. **UNRESOLVED DEPENDENCY
  LR-06:** verify whether the actual OpenCode version requires explicit custom
  model registration/metadata and how its supporting/small-model calls are
  selected; implement those fields if required. All such calls must use this
  connection and the selected compatible model. Do not assume a row controls
  them. If no supported effort emission exists, reject non-default effort.
- Preserve existing tool permission translation and fail-closed tool scope.
  Sanitize backend errors/logs before wrapper diagnosis; raw stderr is not safe.

### 3.3 Diagnosis wrapper coverage
- Both backends' providers are returned through the phase-2 wrapper for LunaRoute
  bindings. A post-failure listing supplies a current observation; it does not
  prove why the earlier inference failed. Preserve the cause-unknown outcome
  when no verified inference error identifies the cause.

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
| Binding (provider, base URL, model, backend, reveal, axes, tools) | both adapters | `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve`; run pins from `domain/provider/pinning.go:PinningResolver.Resolve`, backend precedence from `domain/engine/executor_agent.go:AgentCallExecutor.Execute`; shape in `domain/provider/provider.go:ResolvedBinding`. |
| Hosted root / `/v1` URL | both adapters | Stored input from `adapter/sqlite/connection_repo.go:ConnectionRepo.Get` (`BaseURL`); helper is phase-1 work. **UNRESOLVED DEPENDENCY LR-01:** authoritative hosted/dialect contract. |
| `lr_` key | both adapters | `adapter/sqlite/connection_repo.go:ConnectionRepo.Reveal`, through phase-1 guard; no ambient credential source. |
| Background (Haiku / small-fast) model | claude-code env | the binding's own `Model` (`domain/provider.Profile.Model` → `ResolvedBinding.Model`) — no separate source |
| Max context / max output tokens | claude-code env | phase-1 capability table (`capabilities.default.yaml` → `LoadCapabilityRules` → `adapter/claudecli/provider.go:Config.CapabilityRules`, set in `cmd/arbitron/main.go` on the claude agent Config the factory receives as `Deps.ClaudeAgentConfig`). **UNRESOLVED DEPENDENCY LR-02: authoritative limit values/evidence absent; a short live run does not establish maximum limits. Collect before saving the claude-code acceptance profile, or it remains rejected by phase 1.** |
| Exact Claude Code env var names for Haiku/small-fast/hint/limits | claude-code env | LunaRoute's Claude Code docs as recorded in `docs/ideas/lunaroute-provider.md` §2. **UNRESOLVED DEPENDENCY LR-05: verify these names and all main/supporting-model overrides against the binary actually executed and its effective user/workspace config; record version and completed-run evidence that helper calls stay on the chosen connection. The repo supports installed binaries, not one universally pinned version.** |
| Tool-call support on Messages and chat-completions dialects | both backends | **UNRESOLVED DEPENDENCY LR-02:** per-model/dialect/backend tool-task completion observations absent; record tested combinations without generalising to every catalog model. |
| OpenCode custom model registration, model argv and supporting-model selection | opencode config/argv | Existing container/builders: `adapter/opencode/config.go:providerBlock`, `buildConfig`, `adapter/opencode/args.go:buildArgs`, `modelLeaf`. **UNRESOLVED DEPENDENCY LR-06:** `lunaroute/<id>` and helper routing need verification against the actual binary; no existing custom-model/helper producer is assumed. |
| Capability rules object | claude adapter | `cmd/arbitron/main.go` `capabilityRules` → `claudecli.Config.CapabilityRules` → `agentEnvWithRules` (existing) |
| Mission prompt / workspace / tool scope | both subprocesses | `domain/engine/ports.go:AgentMission`, built by `domain/engine/executor_agent.go:AgentCallExecutor.Execute`; instance workspace is `AgentMission.WorkDir`. |
| Binary / runner / timeout / response cap / tool posture | backend adapters | `adapter/provider/factory.go:Deps` (`ClaudeAgentConfig`, `OpencodeBinaryPath`, `Runner`, `OpencodeTimeout`, `MaxResponseBytes`, `OpencodeAllowAllTools`), populated in `cmd/arbitron/main.go`; `adapter/claudecli/provider.go:Config`. |
| Family / alias classification | launch check | `domain/modelidentity/identity.go:families` extended in phase 1; **UNRESOLVED DEPENDENCY LR-03** for verified LunaRoute aliases. |

### Deferred to plans
- **Wiring:** none new at the composition root beyond phase 2 (the factory already
  receives `CapabilityRules`; the wrapper's probe selector is wired in phase 2). A
  test must prove the production factory emits the LunaRoute env for a
  claude-code LunaRoute binding (not only a direct adapter unit test).
- **Evidence first:** settle LR-05/LR-06 before fixing env/config/argv contracts;
  fetch authoritative LR-02 limits before claude-code profile creation, then
  record per-backend completion observations in the table. Retain unresolved
  markers for any fact the live run cannot prove.
- **Samples/docs:** documented example profiles (connection + chat + claude-code +
  opencode on LunaRoute) in the provider docs / PROFILES guidance — documentation
  only; no bundled sample workflow is changed to depend on LunaRoute.

## 6. Integration seams

- claude-cli capability-env step — single LunaRoute branch.
- opencode descriptor table plus config/argv branches — URL derivation, any
  required model metadata and supporting-model selection; existing rows unchanged.
- Factory agent arm — returns wrapped providers for LunaRoute only.

## 7. Error handling

- Missing limit at emit time → fail closed before launch (should be unreachable
  given launch validation; defended anyway).
- Unknown or misconfigured descriptor → existing `ErrUnknownProvider` fail-closed.
- Launch/run failure → phase-2 diagnosis wrapper classifies.
- Key appears only in the child env: never argv, never opencode config file,
  never logs (asserted).
- No substitution of model (background model is the selected model), backend or
  provider.

## 8. Testing — STRICT TDD (all five categories required)

Write each failing test before implementation, make it pass, then refactor while
green. Verify production wiring as well as pure builders; mocks cannot close
LR-01–LR-03/LR-05/LR-06.

- **Happy-path:** production-factory-built claude-code LunaRoute binding yields env
  with root base URL, `lr_` key, Haiku/small-fast = model, hint flag, both limits;
  opencode LunaRoute binding yields config block with `<root>/v1`, env-referenced
  key, and `-m lunaroute/<id>`.
- **Success:** non-LunaRoute claude-code and opencode bindings produce
  byte-identical env/config/argv to today (golden tests); wrapper passes success
  through.
- **Failure:** claude-code LunaRoute binding with a missing limit fails before
  launch; Claude `200k`/`1m` tier on a LunaRoute binding emits nothing (and is
  rejected at save and launch by phase 1); fake runner exit 1 → uncertain
  diagnostic via wrapper; a node backend override to a known incompatible
  backend is refused before launch; effort never silently floors.
- **Error:** reveal failure → no env produced, no launch; guarded reveal with a
  non-`lr_` stored key → credential error, no launch; SSRF backstop rejects a
  tampered non-hosted base URL in opencode config via the phase-1 endpoint guard
  (general SSRF alone would accept many other public HTTPS hosts); no request.
  Echoed credentials in stderr or inference errors are redacted before logging.
- **Edge-case:** model ids with dots/dashes/`-background`; vendor-alias model on
  claude-code (family check already passed at save); base URL stored with
  trailing slash; key never present in argv, config JSON, or captured logs.
- **Tier 2 (live, gated by `ARBITRON_LUNAROUTE_E2E_KEY`):** a representative tool-using
  agent task (create a file,
  run a command, verify the workspace result) finishes successfully on claude-code
  and on opencode with a compatible LunaRoute model each; confirms env var names,
  tool-call support for the tested combinations and OpenCode custom model form.
  Inspect effective config and capture credential-free request/model evidence
  for main and supporting calls on both backends; one successful main request
  alone does not close LR-05/LR-06. Include a proprietary/alias selection in the
  overall acceptance matrix and show same-vendor doer/judge rejection through
  the shared classifier. Missing credentials leave release acceptance pending.
