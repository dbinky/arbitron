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
  Preserve the existing non-LunaRoute `CODEX_HOME` forwarding. For LunaRoute,
  explicitly override all provider/auth/model and helper selectors that could
  conflict with the connection. Whether forwarding ambient `CODEX_HOME` remains
  compatible with this isolation is part of LR-07; no shared auth/config writes
  are allowed. A per-launch private config home is acceptable only after its
  runtime contract is verified, with cleanup on every exit path.
- **Argv contract:** `-c` overrides that define a `lunaroute` model provider —
  base URL = endpoint helper's `/v1` form, wire API = Responses, credential =
  *name* of the env var (never the key) — and select it as the model provider;
  model passed as today. Effort overrides continue to ride the same `-c` channel
  only after phase-1 exact support validation (no generic flooring). All
  supporting model calls use the same connection and the selected compatible
  model. The exact supported overrides remain gated by LR-07.
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
  form, key referenced by env var name, the binding's model) into a private
  temporary directory (owner-only permissions) and points pi at that directory for
  this run only; the directory is removed after the run on every exit path.
  Reasoning is an independent supported/unsupported/unknown fact (supported → true, unsupported →
  false); neither missing efforts nor a nonempty effort list supplies that boolean. Unknown
  required reasoning blocks save and launch. Any required model limits/tool/reasoning metadata
  must come from the phase-1
  evidence-backed table, not invented defaults. Unknown required fields block
  this route until LR-02/LR-08 are resolved.
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
| `/v1` endpoint | codex argv, pi provider definition | Stored input: `adapter/sqlite/connection_repo.go:ConnectionRepo.Get` (`BaseURL`); phase-1 helper is new work. **UNRESOLVED DEPENDENCY LR-01:** authoritative hosted/dialect/auth contract. |
| `lr_` key | codex env, pi env | `adapter/sqlite/connection_repo.go:ConnectionRepo.Reveal`, wrapped by phase-1 guard; current codex inventory no-op is not a credential source. |
| Effort override | codex argv, pi argv | `domain/provider/provider.go:ResolvedBinding.Effort`; existing `adapter/codex/args.go:codexEffortArgs` from factory `Deps.CapabilityRules`, and `adapter/pi/args.go:thinkingLevel`. LunaRoute must validate exact supported emission, avoiding codex flooring. |
| Route requests for codex profiles | codex validation | `adapter/provider/path_inventory.go:ExecutionPathInventory.VersionRequests` (existing) |
| codex custom-provider override keys and wire-API value | codex argv | **UNRESOLVED DEPENDENCY LR-07: exact `-c model_providers.<id>.*` keys, Responses wire API, credential env-key field, auth independence, user/project-config precedence and all supporting-model routing need verification against the actual installed binary. Record version/effective config and complete a tool task before fixing the contract; there is no universally pinned binary in this repo.** |
| pi custom-provider definition format | pi provider definition | **UNRESOLVED DEPENDENCY LR-08: pi custom-provider format (API type, base URL, env-referenced key, required model metadata), exact provider/model argv and main/supporting-model config precedence for the actual installed version are unverified.** |
| pi config-directory relocation mechanism | pi env/argv | **UNRESOLVED DEPENDENCY LR-08: per-run config-directory relocation is not implemented or verified. If no supported mechanism exists, plans must escalate the blocked required pi outcome; do not write `~/.pi`, drop pi from scope or claim readiness.** |
| Tool-call support on Responses (codex) and chat-completions (pi) | both backends | **UNRESOLVED DEPENDENCY LR-02:** per-model/backend completed tool-task evidence absent. Record only tested combinations; authoritative limits/effort facts are separate inputs. |
| Temp-dir root / permissions / cleanup | pi adapter | **UNRESOLVED DEPENDENCY LR-08:** no per-launch pi directory producer exists in `adapter/pi/provider.go:Provider.RunAgent`. Implement using OS temp-directory input (`os.TempDir`), `os.MkdirTemp`, owner-only files and deferred `os.RemoveAll`, once the relocation contract is verified. |
| Required pi model metadata / supported settings | pi definition and launch check | Existing loaded container `domain/provider/capability.go:CapabilityRules` and `adapter/config/capabilities.go:LoadCapabilityRules` require the phase-1 section/read port, and new pi Config injection. **UNRESOLVED DEPENDENCY LR-02:** authoritative per-model facts; LR-08 determines required fields. |
| Mission prompt / workspace | both backends | `domain/engine/ports.go:AgentMission`, built by `domain/engine/executor_agent.go:AgentCallExecutor.Execute`; `WorkDir` stays the instance workspace even if config is temporary. |
| Binary / runner / timeout / response cap | both backends | `adapter/provider/factory.go:Deps` (`CodexBinaryPath`, `PiBinaryPath`, `Runner`, per-backend timeout, `MaxResponseBytes`) wired by `cmd/arbitron/main.go`. |
| Vendor / alias identity | launch/pin check | `domain/modelidentity/identity.go:families` plus phase-1 extensions; **UNRESOLVED DEPENDENCY LR-03** for verified LunaRoute aliases. |
| Original failure / current diagnostic | error wrapper | `adapter/codex/provider.go:Provider.RunAgent`, `adapter/pi/provider.go:Provider.RunAgent` errors; phase-2 probe adds a later observation, not proof of original cause. |

### Deferred to plans
- **Live verification tasks first:** one task per backend that settles its
  UNRESOLVED items against the actual installed binary version, before any
  argv/config contract is coded.
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
minimum code, then refactor while green. Use a version-recorded protocol
contract once LR-07/LR-08 are verified; mock launches do not close those items.

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
  credentials or shared config writes. LR-07/LR-08 close only with recorded
  evidence; LR-01–LR-03 still require their own authoritative inputs.

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
readiness. All LR-01–LR-08 items must have recorded evidence or remain visible
plan-stage tasks/escalations; do not remove an unresolved marker on a mock pass.

## Alignment contracts for release evidence

P4-T1 independently verifies codex/pi contracts before P4-T2/T4; P4-T3/T5 jointly enable and feed
their adapters; P4-T7 owns final acceptance. Version probes on LunaRoute use private configuration
with the credential removed, rather than inheriting subscription CODEX_HOME. Error/log safety and
local-failure markers apply to legacy unpinned calls as well. The release matrix executes the
proprietary/alias selection, validates actual output/tool traces and main/helper requests, and
runs the existing same-vendor safeguard rejection; saved profiles, model pins, empty observation
slices or skipped tests are insufficient. All eight LR leaves remain open until
authoritative/runtime artifacts exist and the owning tasks populate runtime sources.
