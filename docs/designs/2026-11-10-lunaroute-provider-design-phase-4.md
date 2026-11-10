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
| codex dispatch | `adapter/provider/factory.go:AgentProviderFor` codex arm | passes the binding; codex adapter selects mode |
| codex env | `adapter/codex/env.go:codexEnv` | gains a keyed LunaRoute mode alongside the unchanged subscription env |
| codex argv | `adapter/codex/args.go:buildExecArgs` (already carries `-c` overrides for effort) | LunaRoute provider overrides appended via the same `-c` channel |
| codex route validation | `domain/provider/selection.go:ValidatePinRoutes` codex carve-out | narrowed to non-LunaRoute routes |
| codex backend health | `adapter/backendhealth` → `adapter/codex/readiness.go:Readiness` | unchanged; documented as not gating LunaRoute codex runs |
| pi descriptors / env | `adapter/pi/env.go:providerDescriptors`, `resolveDescriptor`, `piEnv` | new `lunaroute` descriptor kind (custom provider) |
| pi launch | `adapter/pi/provider.go:Provider.RunAgent`, `args.go:buildArgs` | per-launch config dir + model selection for the custom provider |
| Phase-1 temporary rule | compatibility check rule 6 (codex rejected) | removed and replaced by real validation |

## 3. Components

### 3.1 codex LunaRoute mode (adapter/codex)
- **Job:** run `codex exec` against LunaRoute's Responses endpoint with the
  connection's key, without touching `~/.codex`.
- **Mode selection:** canonical provider `lunaroute` → LunaRoute mode; anything
  else → existing subscription mode (unchanged, including ignoring the provider).
- **Env contract:** base env + exactly one credential variable carrying the
  guarded-revealed `lr_` key (fail closed on reveal error: no env, no launch).
  `CODEX_HOME` forwarding is unchanged.
- **Argv contract:** `-c` overrides that define a `lunaroute` model provider —
  base URL = endpoint helper's `/v1` form, wire API = Responses, credential =
  *name* of the env var (never the key) — and select it as the model provider;
  model passed as today. Effort overrides continue to ride the same `-c` channel.
- **Auth independence:** LunaRoute mode does not read or require
  `~/.codex/auth.json`; the global backend-health login verdict is informational
  and does not gate LunaRoute codex runs.
- **Wrapped** by the phase-2 diagnosis wrapper.

### 3.2 codex route validation (domain/provider)
- **Job:** validate codex + LunaRoute as LunaRoute, not as a subscription.
- **Contract:** the `ValidatePinRoutes` codex carve-out (which clears route
  provider and connection for codex) applies only when the route's canonical
  provider is not `lunaroute`. LunaRoute codex routes go through the phase-1
  LunaRoute grammar and compatibility check like every other backend. Phase-1
  rule 6 ("codex not yet supported") is deleted in the same change.

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
- **Env contract:** base env + the one credential var (guarded reveal) + the
  config-dir pointer. No key in argv or in the written file.
- **Model selection:** argv selects the LunaRoute custom provider and the
  binding's model.
- **Non-LunaRoute pi bindings:** byte-identical to today (no temp dir).
- **Wrapped** by the phase-2 diagnosis wrapper.

## 4. Data flow

**codex:** `agent_call` → binding (guarded reveal) → `AgentProviderFor` codex arm
(wrapped) → codex adapter: provider `lunaroute` → env `{cred var}=lr_…`; argv
`exec … -c <lunaroute provider overrides> -c <effort> --model <id>` → `codex`
subprocess → `<root>/v1/responses`.

**pi:** `agent_call` → binding → `AgentProviderFor` pi arm (wrapped) → pi adapter:
descriptor `lunaroute` (custom) → create private temp config dir → write provider
definition → env `{cred var}=lr_…` + config-dir pointer → argv selects provider +
model → `pi` subprocess → `<root>/v1/chat/completions` → remove temp dir.

**Profile save (codex):** `ValidatePinRoutes` → canonical provider `lunaroute` →
no carve-out → LunaRoute grammar + compatibility check (table must not mark codex
incompatible).

## 5. Data Provenance

| Input | Consumer | Production producer |
|---|---|---|
| Binding (provider, base URL, model, reveal) | codex, pi adapters | `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve` (guarded reveal, phase 1) |
| `/v1` endpoint | codex argv, pi provider definition | phase-1 endpoint helper over stored `Connection.BaseURL` |
| `lr_` key | codex env, pi env | `ConnectionStore.Reveal` via guarded reveal |
| Effort override | codex argv | existing `adapter/codex/args.go` effort path from factory `Deps.CapabilityRules` |
| Route requests for codex profiles | codex validation | `adapter/provider/path_inventory.go:ExecutionPathInventory.VersionRequests` (existing) |
| codex custom-provider override keys and wire-API value | codex argv | **UNRESOLVED DEPENDENCY: the exact `-c model_providers.<id>.*` keys, the Responses wire-API value, and the env-key field name for the codex version this repo pins are unverified offline; verify as the first task of the codex sub-phase (live run) before the argv contract is fixed.** |
| pi custom-provider definition format | pi provider definition | **UNRESOLVED DEPENDENCY: pi's custom-provider file format (OpenAI-compatible API type, base URL, env-referenced key, model list) for the pinned pi version is unverified.** |
| pi config-directory relocation mechanism | pi env/argv | **UNRESOLVED DEPENDENCY: which env var or flag points pi at an alternate config directory for one run is unknown to this repo (the current adapter never relocates it). If none exists, this design's pi path is blocked and must return to the human — it must not fall back to writing `~/.pi`.** |
| Tool-call support on Responses (codex) and chat-completions (pi) | both backends | **UNRESOLVED DEPENDENCY: proven per model in the Tier-2 live run and recorded as the capability table's backend compatibility.** |
| Temp-dir root | pi adapter | OS temp dir (process environment); no new config |

### Deferred to plans
- **Live verification tasks first:** one task per backend that settles its
  UNRESOLVED items against the pinned binary version, before any argv/config
  contract is coded.
- **Wiring:** none new at the composition root (factory already passes binding,
  runner, timeouts; wrapper wired in phase 2). A production-factory test must
  prove the LunaRoute mode is selected for codex and pi bindings built by the real
  resolver.
- **Validation change:** remove phase-1 rule 6 together with narrowing the
  `ValidatePinRoutes` carve-out (single change, so there is never a window where
  codex + LunaRoute is accepted as a subscription).
- **Data population:** codex and pi compatibility per model in the capability
  table, from the live run.
- **Docs:** example profiles for codex and pi on LunaRoute; note that the codex
  backend-health login verdict does not apply to LunaRoute runs.

## 6. Integration seams

- codex adapter — mode switch on canonical provider; subscription path untouched.
- `ValidatePinRoutes` — carve-out narrowed by one condition.
- pi descriptor table — new custom-provider kind; existing rows untouched.
- Factory — unchanged dispatch; wrapper from phase 2.

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

- **Happy-path:** production-factory codex LunaRoute binding → env carries one
  credential var, argv carries the provider overrides with `<root>/v1` and the env
  var *name*; pi LunaRoute binding → temp dir with a provider definition
  referencing `<root>/v1` and the env var name, env points pi at it.
- **Success:** codex subscription bindings (no connection, or non-LunaRoute
  connection) produce byte-identical env/argv to today; non-LunaRoute pi bindings
  unchanged with no temp dir; codex + LunaRoute profile now saves when the table
  does not mark codex incompatible; effort override still emitted.
- **Failure:** codex + LunaRoute profile with a table-incompatible model rejected;
  codex + LunaRoute with unresolved family rejected; fake runner exit 1 →
  classified error via wrapper; pi temp dir removed after a failed run.
- **Error:** reveal error → no launch, no temp dir; temp-dir write failure → no
  launch; context cancellation mid-run → temp dir removed; guarded reveal rejects
  non-`lr_` stored key.
- **Edge-case:** temp dir permissions are owner-only; concurrent pi launches get
  distinct temp dirs; `CODEX_HOME` set in parent still forwarded in both modes;
  model ids with dots/`-background`; key absent from argv, written file and
  captured logs.
- **Tier 2 (live, gated by `ARBITRON_LUNAROUTE_E2E_KEY`):** a representative
  tool-using agent task (create a file, run a command, verify the workspace
  result) completes successfully on codex and on pi with a compatible LunaRoute
  model each; settles every UNRESOLVED item above. Together with phases 2–3 this
  completes the spec's readiness set (configure → discover → select → chat → four
  agent backends → distinct errors → alias family preserved).
