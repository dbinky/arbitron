# LunaRoute Provider — Design Phase 4: codex & pi backends

**Depends on:** Phase 1 (endpoint helper, guarded reveal, capability table,
profile-save rules incl. the temporary codex rejection), Phase 2 (diagnosis
wrapper), Phase 3 (pattern for per-backend LunaRoute branches).
**Approach (approved):** A — per-launch ephemeral provider definitions; no
persistent dotfiles; non-LunaRoute paths unchanged.

## 1. Purpose and scope

Enable tool-using agent work on LunaRoute through the two backends whose current
credential model cannot reach a custom endpoint:

- **codex** — today subscription-only end to end (`Factory.AgentProviderFor`
  ignores the provider; `adapter/codex/env.go:codexEnv` never reveals a key;
  `ValidatePinRoutes` erases the route provider for codex).
- **pi** — today a short credential allowlist with no base-URL or custom-provider
  support (`adapter/pi/env.go:providerDescriptors`, `piEnv`).

The spec's "no changes to existing subscription-based connections" is honoured:
every non-LunaRoute codex and pi path is byte-identical to today.

## 2. Existing architecture this phase extends

| Seam | Location | Use |
|---|---|---|
| codex dispatch | `adapter/provider/factory.go:Factory.AgentProviderFor` codex arm | already passes `Binding: b`; adapter gains a LunaRoute mode |
| codex env | `adapter/codex/env.go:codexEnv` | gains a keyed LunaRoute mode alongside the unchanged subscription env |
| codex argv | `adapter/codex/args.go:buildExecArgs` (already carries `-c` overrides for effort) | LunaRoute provider overrides appended via the same `-c` channel |
| codex save / trigger / live pin validation | `domain/provider/selection.go:ValidatePinRoutes`, `adapter/provider/pinned_route.go:validatePinnedRoute` | both codex carve-outs narrowed to non-LunaRoute routes |
| codex inventory binding | `adapter/provider/path_inventory.go:routeBinding` | enable guarded reveal for LunaRoute codex only; preserve non-LunaRoute no-reveal |
| codex backend health | `adapter/backendhealth` → `adapter/codex/readiness.go:Readiness` | unchanged; documented as not gating LunaRoute codex runs |
| pi descriptors / env | `adapter/pi/env.go:providerDescriptors`, `resolveDescriptor`, `piEnv` | new `lunaroute` descriptor kind (custom provider) |
| pi launch | `adapter/pi/provider.go:Provider.RunAgent`, `args.go:buildArgs` | per-launch config dir + model selection for the custom provider |
| Phase-1 temporary rule | compatibility check rule 7 (codex rejected) | removed and replaced by real validation |

## 3. Components

### 3.1 codex LunaRoute mode (adapter/codex)
- **Job:** run `codex exec` against LunaRoute's Responses endpoint with the
  connection's key, without touching `~/.codex`.
- **Mode selection:** canonical provider `lunaroute` → LunaRoute mode; anything
  else → existing subscription mode (unchanged, including ignoring the provider).
- **Env contract:** base env + exactly one credential variable carrying the
  guarded-revealed `lr_` key (fail closed on reveal error: no env, no launch).
  Preserve the existing non-LunaRoute `CODEX_HOME` forwarding. **LR-07 decided
  ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** a LunaRoute launch gets a per-launch private 0700 `CODEX_HOME` (no
  `auth.json`, no `config.toml`), removed on every exit path after the pinned
  rollout check reads it. Operator `CODEX_HOME`, `OPENAI_API_KEY` and
  `OPENAI_BASE_URL` are not forwarded. There are no shared auth/config writes.
- **Argv contract:** `-c` overrides that define a `lunaroute` model provider —
  base URL = endpoint helper's `/v1` form, wire API = Responses, credential =
  *name* of the env var (never the key) — and select it as the model provider;
  model passed as today. Effort overrides continue to ride the same `-c` channel
  only after phase-1 exact support validation (no generic flooring). All
  supporting model calls use the same connection and the selected compatible
  model. Decided overrides (LR-07): `model_provider="lunaroute"`,
  `model_providers.lunaroute.{name, base_url, env_key="LUNAROUTE_API_KEY", wire_api="responses"}`
  and `review_model="<id>"`. Compaction uses the main model. P4-T1 verifies on
  codex-cli 0.167.0: an ignored override is dropped, and a login requirement or
  off-connection traffic stops enablement.
- **Auth independence:** LunaRoute mode does not read or require
  `~/.codex/auth.json` or `$CODEX_HOME/auth.json`; prove this with absent and
  conflicting local subscription files. The global backend-health login verdict
  is informational and does not gate LunaRoute codex runs.
- **Wrapped** by the phase-2 diagnosis wrapper.

### 3.2 codex route validation (domain/provider)
- **Job:** validate codex + LunaRoute as LunaRoute, not as a subscription.
- **Contract:** the `ValidatePinRoutes` codex carve-out (which clears route
  provider and connection for codex) applies only when the route's canonical
  provider is not `lunaroute`. Narrow the matching provider-erasure branch in
  `adapter/provider/pinned_route.go:validatePinnedRoute` as well; otherwise
  trigger-valid LunaRoute models are rejected at real pinned launch.
- `adapter/provider/path_inventory.go:routeBinding` currently supplies a no-op
  reveal closure for **all** codex bindings. Allow guarded reveal for LunaRoute
  codex there, including inventory/probe-built routes; non-LunaRoute codex still
  never reveals a connection key. The factory already passes the binding.
- LunaRoute codex routes go through phase-1 grammar and compatibility validation
  at save, pin capture and real launch. Phase-1 rule 7 ("codex not yet supported")
  is removed in the same change as both carve-out fixes and real execution
  support; no intermediate accepted route may fall back to subscription mode.

### 3.3 pi LunaRoute custom provider (adapter/pi)
- **Job:** let pi call LunaRoute's OpenAI-compatible endpoint with the
  connection's key, without touching `~/.pi`.
- **Descriptor:** a `lunaroute` entry of a new *custom-provider* kind (key-based,
  credential env var named, plus "needs per-launch provider definition"). The
  descriptor-table invariant test is extended so a row cannot be both OAuth and
  custom.
- **Per-launch provider definition:** before launch, the adapter writes a
  provider definition (OpenAI-compatible API, base URL = endpoint helper's `/v1`
  form, key referenced as `"$LUNAROUTE_API_KEY"` with `authHeader: true`, the
  binding's model; LR-08 decided) into a private
  temporary directory (owner-only permissions) and points pi at that directory for
  this run only; the directory is removed after the run on every exit path.
  Reasoning is an independent supported/unsupported/unknown fact (supported → true, unsupported →
  false); neither missing efforts nor a nonempty effort list supplies that boolean. Unknown
  required reasoning blocks save and launch. Any required model limits/tool/reasoning metadata
  must come from the phase-1
  evidence-backed table, not invented defaults (pi's own 128000/16384/false
  defaults are never relied on). Unknown required fields block this route for
  that model (LR-02/LR-08 decisions).
- **Env contract:** base env + the one credential var (guarded reveal) + the
  config-dir pointer. No key in argv or in the written file.
- **Model selection:** argv selects the LunaRoute custom provider and the
  binding's model; main and supporting calls may not inherit user/project
  config selecting a different provider or model. Verify override precedence
  and disable ambient auto-selection that conflicts with the route.
- **Policy wiring:** pi currently has no `CapabilityRules` field. Add a
  LunaRoute-only rules/read-port input via factory deps for launch validation
  and any required model metadata; maintain non-LunaRoute construction semantics.
  Keep `pi --thinking` mapping only when it exactly represents the validated
  effort; unknown or known-conflicting explicit settings are rejected.
- **Non-LunaRoute pi bindings:** byte-identical to today (no temp dir).
- **Wrapped** by the phase-2 diagnosis wrapper.

## 4. Data flow

**codex:** `agent_call` → binding (guarded reveal) → `AgentProviderFor` codex arm
(wrapped) → codex adapter: provider `lunaroute` → env `{cred var}=lr_…`; argv
`exec … -c <lunaroute provider overrides> -c <effort> --model <id>` → `codex`
subprocess → `<root>/v1/responses`.

**pi:** `agent_call` → binding → `AgentProviderFor` pi arm (wrapped) → pi adapter:
descriptor `lunaroute` (custom) → policy/endpoint/key validation → guarded reveal
→ create private temp config dir → write provider
definition → env `{cred var}=lr_…` + config-dir pointer → argv selects provider +
model → `pi` subprocess → `<root>/v1/chat/completions` → remove temp dir.

**Profile save (codex):** `ValidatePinRoutes` → canonical provider `lunaroute` →
no carve-out → LunaRoute grammar + compatibility check (resolved vendor, known
conflicts rejected, explicitly selected backend). Run pin validation retains the
LunaRoute provider too, and `routeBinding` supplies guarded reveal for this route.

## 5. Data Provenance

| Input | Consumer | Production producer |
|---|---|---|
| Binding (provider, base URL, model, reveal, axes, tools) | codex, pi adapters | `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve`, model from `domain/provider/pinning.go:PinningResolver.Resolve`; backend precedence from `domain/engine/executor_agent.go:AgentCallExecutor.Execute`. Inventory also produces bindings via `adapter/provider/path_inventory.go:routeBinding`, whose codex reveal suppression must change for LunaRoute. |
| `/v1` endpoint | codex argv, pi provider definition | Stored input: `adapter/sqlite/connection_repo.go:ConnectionRepo.Get` (`BaseURL`); phase-1 helper is new work. **LR-01 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** docs-verified root plus `/v1`, with Bearer auth. |
| `lr_` key | codex env, pi env | `adapter/sqlite/connection_repo.go:ConnectionRepo.Reveal`, wrapped by phase-1 guard; current codex inventory no-op is not a credential source. |
| Effort override | codex argv, pi argv | `domain/provider/provider.go:ResolvedBinding.Effort`; existing `adapter/codex/args.go:codexEffortArgs` from factory `Deps.CapabilityRules`, and `adapter/pi/args.go:thinkingLevel`. LunaRoute must validate exact supported emission, avoiding codex flooring. |
| Route requests for codex profiles | codex validation | `adapter/provider/path_inventory.go:ExecutionPathInventory.VersionRequests` (existing) |
| codex custom-provider override keys and wire-API value | codex argv | **LR-07 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** `-c model_provider="lunaroute"`, `model_providers.lunaroute.{name,base_url=<root>/v1,env_key="LUNAROUTE_API_KEY",wire_api="responses"}`, `review_model=<id>`, private `CODEX_HOME`, auth independent of the user's login. P4-T1 verifies on codex-cli 0.167.0. |
| pi custom-provider definition format | pi provider definition | **LR-08 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** `models.json` `providers.lunaroute = {baseUrl:<root>/v1, api:"openai-completions", apiKey:"$LUNAROUTE_API_KEY", authHeader:true, models:[{id,name,reasoning,input:["text"],contextWindow,maxTokens}]}`; argv `--model lunaroute/<id>`. Source: pi 1.5.1 installed docs (`models-configuration.md`). |
| pi config-directory relocation mechanism | pi env/argv | **LR-08 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** `PI_CODING_AGENT_DIR` relocates the whole agent dir (pi 1.5.1 `configuration.md`). A private dir is untrusted, so project `.pi` config does not load. If P4-T1 contradicts this, pi + LunaRoute stays refused and LR-08 escalates; `~/.pi` is never written and pi is never dropped. |
| Tool-call support on Responses (codex) and chat-completions (pi) | both backends | **LR-02 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** `unknown` until a P4-T1 completed tool task marks the tested model/backend `supported`. |
| Temp-dir root / permissions / cleanup | pi adapter | New producer `adapter/pi/lunaroute.go:prepareLunaRoutePiLaunch` (P4-T4, LR-08 decided): `os.MkdirTemp` under `os.TempDir`, 0700 dir, 0600 `models.json`, deferred `os.RemoveAll`. |
| Required pi model metadata / supported settings | pi definition and launch check | Existing loaded container `domain/provider/capability.go:CapabilityRules` and `adapter/config/capabilities.go:LoadCapabilityRules` require the phase-1 section/read port, and new pi Config injection. **LR-02/LR-08 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** required fields are `reasoning`, `contextWindow` and `maxTokens`, from doc-sourced LR-02 facts; unknown → refuse. |
| Mission prompt / workspace | both backends | `domain/engine/ports.go:AgentMission`, built by `domain/engine/executor_agent.go:AgentCallExecutor.Execute`; `WorkDir` stays the instance workspace even if config is temporary. |
| Binary / runner / timeout / response cap | both backends | `adapter/provider/factory.go:Deps` (`CodexBinaryPath`, `PiBinaryPath`, `Runner`, per-backend timeout, `MaxResponseBytes`) wired by `cmd/arbitron/main.go`. |
| Vendor / alias identity | launch/pin check | `domain/modelidentity/identity.go:families` plus phase-1 extensions; **LR-03 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** reviewed ids plus the D8-style alias exception. |
| Original failure / current diagnostic | error wrapper | `adapter/codex/provider.go:Provider.RunAgent`, `adapter/pi/provider.go:Provider.RunAgent` errors; phase-2 probe adds a later observation, not proof of original cause. |

### Deferred to plans
- **Live verification tasks first:** one task per backend verifies its decided
  LR-07/LR-08 contract against the actual installed binary version and applies
  the decisions doc's On-failure rule.
- **Wiring:** factory already passes binding, runner and timeouts; add LunaRoute
  capability rules/model metadata to pi Config, and implement codex inventory
  guarded reveal. Wrapper and shared rules originate in phases 1–2. A
  production-factory test must prove the LunaRoute mode is selected for codex and pi
  bindings built by the real
  resolver.
- **Validation change:** remove phase-1 rule 7 together with narrowing both
  `ValidatePinRoutes` and `validatePinnedRoute` carve-outs and enabling guarded
  inventory reveal/execution (single coherent change).
- **Data population:** codex and pi compatibility per model in the capability
  table from scoped completion evidence, with authoritative settings/limits
  collected separately. Do not mark untested models incompatible.
- **Docs:** example profiles for codex and pi on LunaRoute; note that the codex
  backend-health login verdict does not apply to LunaRoute runs.

## 6. Integration seams

- codex adapter — mode switch on canonical provider; subscription path untouched.
- `ValidatePinRoutes` + `validatePinnedRoute` — both codex carve-outs narrowed.
- `routeBinding` — LunaRoute codex reveals through the credential guard.
- pi descriptor table — new custom-provider kind; existing rows untouched.
- Factory — unchanged backend dispatch; wrapper from phase 2 and new pi
  capability metadata injection. Reject unimplemented LunaRoute interactive
  agent paths rather than routing through subscription streaming.

## 7. Error handling

- Reveal failure / non-`lr_` key → no env, no temp files, no launch.
- pi temp-dir creation or write failure → fail before launch; cleanup on every
  exit path (success, failure, panic-recovery, context cancel).
- Unknown/misconfigured pi descriptor → existing `ErrUnknownProvider` fail-closed;
  pi never falls back to `~/.pi` or ambient keys.
- codex LunaRoute mode never falls back to the subscription (and vice versa).
- Run failure → phase-2 diagnosis wrapper classification.
- Key never in argv, never in the written pi file, never logged (asserted).

## 8. Testing — STRICT TDD (all five categories required)

Write failing tests before each implementation change, pass them with the
minimum code, then refactor while green. Build to the decided LR-07/LR-08
contracts. Mock launches never flip those items to `verified`.

- **Happy-path:** production-factory codex LunaRoute binding → env carries one
  credential var, argv carries the provider overrides with `<root>/v1` and the env
  var *name*; pi LunaRoute binding → temp dir with a provider definition
  referencing `<root>/v1` and the env var name, env points pi at it.
- **Success:** codex subscription bindings (no connection, or non-LunaRoute
  connection) produce byte-identical env/argv to today; non-LunaRoute pi bindings
  unchanged with no temp dir; codex + LunaRoute profile now saves when the table
  does not mark codex incompatible, vendor is resolved and spelling verified;
  trigger/pinned launch retains LunaRoute grammar; inventory-derived codex
  binding reveals only its LunaRoute key; supported effort emitted exactly.
- **Failure:** codex + LunaRoute profile with a table-incompatible model rejected;
  codex + LunaRoute with unresolved family rejected; fake runner exit 1 →
  uncertain diagnostic via wrapper; pi temp dir removed after a failed run.
- **Error:** reveal error → no launch, no temp dir; temp-dir write failure → no
  launch; context cancellation mid-run → temp dir removed; guarded reveal rejects
  non-`lr_` stored key.
- **Edge-case:** temp dir permissions are owner-only; concurrent pi launches get
  distinct temp dirs; non-LunaRoute `CODEX_HOME` forwarding unchanged; LunaRoute
  runs with conflicting home/project config still use only selected routing;
  model ids with dots/`-background`; key absent from argv, written file and
  captured logs, even when upstream/CLI errors echo it; pi exit 0 with a failed
  turn is a failure, preserving existing `ErrTurnFailed` semantics.
- **Tier 2 (live, gated by `ARBITRON_LUNAROUTE_E2E_KEY`):** a representative
  tool-using agent task (create a file, run a command, verify the workspace
  result) completes successfully on codex and on pi with a compatible LunaRoute
  model each, using the real MCP connection/profile/workflow tools through
  completion. Prove the selected connection/model also handles supporting calls
  with credential-free request/effective-config evidence, and no subscription
  credentials or shared config writes. LR-07/LR-08 become `verified` only with
  recorded evidence; LR-01–LR-03 have their own decided sources.

## 9. Release acceptance across all phases

The release gate is one MCP-driven matrix: configure a named connection, inspect
available ids and known/unknown capabilities, explicitly select model/backend,
finish a representative chat task, and finish a tool-using task on **each** of
`claude-code`, `codex`, `opencode` and `pi`. Each tool task must create a file,
execute a command and verify the workspace result plus a successful terminal
turn (pi exit code alone is insufficient). Models may differ by backend. Include
open-weight and proprietary/alias selection with verified actual-vendor family
classification, same-vendor safeguard rejection and unresolved-family refusal.
Test invalid credentials, unavailable models, known incompatible settings/routes
and service failures through MCP; assert no credential disclosure and no silent
model/backend/provider substitution. Observe supporting calls for every backend.
No launch-only smoke test, skipped live suite or missing required backend meets
readiness. All LR-01–LR-08 items are decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)); the release gate requires each
to be `verified` with recorded evidence. A mock pass never changes a register status.

## Alignment contracts for release evidence

P4-T1 independently verifies codex/pi contracts before P4-T2/T4; P4-T3/T5 jointly enable and feed
their adapters; P4-T7 owns final acceptance. Version probes on LunaRoute use private configuration
with the credential removed, rather than inheriting subscription CODEX_HOME. Error/log safety and
local-failure markers apply to legacy unpinned calls as well. The release matrix executes the
proprietary/alias selection, validates actual output/tool traces and main/helper requests, and
runs the existing same-vendor safeguard rejection; saved profiles, model pins, empty observation
slices or skipped tests are insufficient. All eight LR items are decided
(`2026-11-10-lunaroute-provider-decisions.md`); each stays `decided` (not release-ready) until its
verification artifacts exist and the owning tasks have applied it to runtime sources.

## Plan-owned observation and reported-identity producers

P2-T9 produces `test/lunaroute/capture_test.go` and its isolated TLS observation proxy/wrappers; P3-T5/P4-T1 supply forced, correlated helper operations and P4-T7 observes current real MCP runs. No capture changes the production endpoint/credential policy. Missing traffic or response-model evidence fails verification. `engine.AgentResult.ModelEvidence` (P2-T5) is populated from each backend's verified records by P3-T1/T2/P4-T2/T4 and persisted by AgentCallExecutor in `LLMConversation.model_evidence`; configured Model and pins do not substitute for reported identities or alias drift. Each record carries a typed `kind` persisted with it: `reported_execution` (pi `message_end`, Claude `modelUsage`) is an identity the executed call itself reported, while `routing_intent` (Codex `turn_context`; the OpenCode export until its response provenance is established) is only the harness's recorded selection. A missing or unknown kind is never treated as execution, and each consumer chooses which kind satisfies its own contract; gateway response evidence is collected separately.

Independent contract observations precede enablement; final register `verified` status also requires production application and follows it. Therefore an enabling task never depends on a register label requiring that same task to have completed. Each live collection task owns its scoped capabilities.default.yaml support-fact update; source notes and skipped runs cannot populate support.
