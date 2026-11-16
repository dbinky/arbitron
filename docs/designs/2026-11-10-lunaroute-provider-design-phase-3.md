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
- **Credential (LR-01 decided, [decisions](2026-11-10-lunaroute-provider-decisions.md)):** `ANTHROPIC_AUTH_TOKEN=<lr_ key>` (Claude
  Code sends `Authorization: Bearer`) through the existing `procenv.CredentialEnv`
  skeleton with the key var switched for LunaRoute. `ANTHROPIC_API_KEY` is stripped
  and never set. Reveal goes through the guard.
- **Precedence (LR-05 decided):** every LunaRoute claude-code launch passes
  `--setting-sources ""` (the mechanism `model_probe.go` already uses), so user,
  project and local `settings.json` cannot override the env contract. `CLAUDE.md`
  memory still loads. P3-T5 verifies main and helper routing on Claude Code
  2.1.322. A selector the binary ignores is dropped, and an unroutable helper
  stops enablement.
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
  has no custom model definitions or small-model field. **LR-06 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):**
  the per-run `OPENCODE_CONFIG_CONTENT` overlay (D12-verified deep merge over all
  inherited layers) carries `provider.lunaroute.models.<id> = {name, tool_call: true,
  limit: {context, output}}` from LR-02, `model` and `small_model` =
  `lunaroute/<id>`, and `enabled_providers: ["lunaroute"]`. The run uses `--pure`.
  Unknown limits are refused before reveal and at save. Non-default effort is
  rejected. P3-T5 verifies on opencode 1.19.5: an ignored key is dropped, and
  unproven `small_model` routing stops enablement.
- Preserve existing tool permission translation and fail-closed tool scope.
  Sanitize backend errors/logs before wrapper diagnosis for pinned and legacy unpinned LunaRoute
  calls; raw stderr is not safe. Local validation/reveal/config/version failures carry a local
  marker so diagnosis does not probe them.

### 3.3 Diagnosis wrapper coverage
- Both backends' providers are returned through the phase-2 wrapper for LunaRoute
  bindings. A post-failure listing supplies a current observation; it does not
  prove why the earlier inference failed. Preserve the cause-unknown outcome
  when no verified inference error identifies the cause.

## 4. Data flow

**claude-code:** `agent_call` node → resolver binding (guarded reveal) →
`AgentProviderFor` (claude-code arm, wrapped) → `NewAgentAdapterForBinding` →
`agentEnvWithRules`: credential env (`ANTHROPIC_BASE_URL=<root>`,
`ANTHROPIC_AUTH_TOKEN=lr_…`, `ANTHROPIC_API_KEY` stripped) + LunaRoute env (all six
selectors = model, hints, limits from table) + `--setting-sources ""` → `claude`
subprocess → `<root>/v1/messages` with Bearer auth.

**opencode:** `agent_call` → binding → `AgentProviderFor` (opencode arm, wrapped) →
descriptor `lunaroute` → env `LUNAROUTE_API_KEY=lr_…`; config provider block
`{npm: openai-compatible, baseURL: <root>/v1, apiKey: {env:LUNAROUTE_API_KEY}}`;
argv `-m lunaroute/<id>` → `opencode` subprocess → `<root>/v1/chat/completions`.

## 5. Data Provenance

| Input | Consumer | Production producer |
|---|---|---|
| Binding (provider, base URL, model, backend, reveal, axes, tools) | both adapters | `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve`; run pins from `domain/provider/pinning.go:PinningResolver.Resolve`, backend precedence from `domain/engine/executor_agent.go:AgentCallExecutor.Execute`; shape in `domain/provider/provider.go:ResolvedBinding`. |
| Hosted root / `/v1` URL | both adapters | Stored input from `adapter/sqlite/connection_repo.go:ConnectionRepo.Get` (`BaseURL`); helper is phase-1 work. **LR-01 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** docs-verified root plus `/v1`, Bearer auth. |
| `lr_` key | both adapters | `adapter/sqlite/connection_repo.go:ConnectionRepo.Reveal`, through phase-1 guard; no ambient credential source. |
| Background (Haiku / small-fast) model | claude-code env | the binding's own `Model` (`domain/provider.Profile.Model` → `ResolvedBinding.Model`) — no separate source |
| Max context / max output tokens | claude-code env | phase-1 capability table (`capabilities.default.yaml` → `LoadCapabilityRules` → `adapter/claudecli/provider.go:Config.CapabilityRules`, set in `cmd/arbitron/main.go` on the claude agent Config the factory receives as `Deps.ClaudeAgentConfig`). **LR-02 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** doc-sourced limits for the release set, loaded in the embedded default by P1-T8/P1-T11. A short live run never sets maxima. Unknown limits keep claude-code (and opencode) refused. |
| Exact Claude Code env var names for Haiku/small-fast/hint/limits | claude-code env | LunaRoute's Claude Code docs as recorded in `docs/ideas/lunaroute-provider.md` §2. **LR-05 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** six selectors set to the model, `CLAUDE_CODE_GATEWAY_HINT_HEADERS=1`, `CLAUDE_CODE_MAX_{CONTEXT,OUTPUT}_TOKENS`, `ANTHROPIC_AUTH_TOKEN`, and `--setting-sources ""` isolation. P3-T5 verifies on 2.1.322 (main plus helper request evidence). |
| Tool-call support on Messages and chat-completions dialects | both backends | **LR-02 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** backend support is `unknown` until a P3-T5 completed tool task marks the tested model/backend `supported`; nothing is generalised to other catalog models. |
| OpenCode custom model registration, model argv and supporting-model selection | opencode config/argv | Existing container/builders: `adapter/opencode/config.go:providerBlock`, `buildConfig`, `adapter/opencode/args.go:buildArgs`, `modelLeaf`. **LR-06 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** `models.<id>` with `tool_call`/`limit`, `model`/`small_model`, `enabled_providers`, `--pure`, `-m lunaroute/<id>`. P3-T5 verifies on 1.19.5. |
| Capability rules object | claude adapter | `cmd/arbitron/main.go` `capabilityRules` → `claudecli.Config.CapabilityRules` → `agentEnvWithRules` (existing) |
| Mission prompt / workspace / tool scope | both subprocesses | `domain/engine/ports.go:AgentMission`, built by `domain/engine/executor_agent.go:AgentCallExecutor.Execute`; instance workspace is `AgentMission.WorkDir`. |
| Binary / runner / timeout / response cap / tool posture | backend adapters | `adapter/provider/factory.go:Deps` (`ClaudeAgentConfig`, `OpencodeBinaryPath`, `Runner`, `OpencodeTimeout`, `MaxResponseBytes`, `OpencodeAllowAllTools`), populated in `cmd/arbitron/main.go`; `adapter/claudecli/provider.go:Config`. |
| Family / alias classification | launch check | `domain/modelidentity/identity.go:families` extended in phase 1; **LR-03 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** reviewed ids plus the D8-style alias exception. |

### Deferred to plans
- **Wiring:** none new at the composition root beyond phase 2 (the factory already
  receives `CapabilityRules`; the wrapper's probe selector is wired in phase 2). A
  test must prove the production factory emits the LunaRoute env for a
  claude-code LunaRoute binding (not only a direct adapter unit test).
- **Decided contracts first:** LR-05/LR-06 contracts are fixed by the decisions
  doc. P3-T5 verifies them and applies their On-failure rules. LR-02 doc-sourced
  limits are loaded before claude-code/opencode profile creation. Per-backend
  completion observations then mark only the tested support facts `supported`.
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
green. Verify production wiring as well as pure builders. Mocks never flip
LR-01–LR-03/LR-05/LR-06 to `verified`.

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
  alone does not verify LR-05/LR-06. Include a proprietary/alias selection in the
  overall acceptance matrix and show same-vendor doer/judge rejection through
  the shared classifier. Missing credentials leave release acceptance pending.

## Alignment contracts for plan execution

P3-T5 has independent evidence preparation before the P3-T1/T2 emitters, then production
verification after P3-T3; there is no evidence-through-unimplemented-adapter cycle. LR-05/LR-06
contracts are decided; verification failures follow the decisions doc's On-failure rules (drop an
ignored key, or stop enablement). An ignored custom-model/helper key is not accepted as
harmless. If OpenCode requires model metadata, its Config consumes the shared capability rules
through Factory and its root test checks the emitted values. The Claude terminal modelUsage check
must reject nonselected helper models on LunaRoute while preserving existing non-LunaRoute
behavior. Sink tests cover both backends' failed-run diagnosis, not only successful env emission.

## Plan-owned observation and reported-identity producers

P2-T9 produces `test/lunaroute/capture_test.go` and its isolated TLS observation proxy/wrappers; P3-T5/P4-T1 supply forced, correlated helper operations and P4-T7 observes current real MCP runs. No capture changes the production endpoint/credential policy. Missing traffic or response-model evidence fails verification. `engine.AgentResult.ModelEvidence` (P2-T5) is populated from each backend's verified records by P3-T1/T2/P4-T2/T4 and persisted by AgentCallExecutor in `LLMConversation.model_evidence`; configured Model and pins do not substitute for reported identities or alias drift. Each record carries a typed `kind` persisted with it: `reported_execution` (pi `message_end`, Claude `modelUsage`) is an identity the executed call itself reported, while `routing_intent` (Codex `turn_context`; the OpenCode export until its response provenance is established) is only the harness's recorded selection. A missing or unknown kind is never treated as execution, and each consumer chooses which kind satisfies its own contract; gateway response evidence is collected separately.

Independent contract observations precede enablement; final register `verified` status also requires production application and follows it. Therefore an enabling task never depends on a register label requiring that same task to have completed. Each live collection task owns its scoped capabilities.default.yaml support-fact update; source notes and skipped runs cannot populate support.
