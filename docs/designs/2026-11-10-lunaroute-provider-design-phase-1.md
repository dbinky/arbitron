# LunaRoute Provider — Design Phase 1: Domain foundation

**Spec:** `docs/specs/2026-11-10-lunaroute-provider-spec.md`
**Idea doc (context only):** `docs/ideas/lunaroute-provider.md`
**Approach (approved):** A — extend existing seams; no new LunaRoute adapter package.
**Phase set:** 1 Domain foundation · 2 Discovery & chat · 3 claude-code & opencode · 4 codex & pi.

## 1. Purpose and scope

Establish every domain-level rule the later phases depend on, so that adapters in
phases 2–4 only *consume* decisions and never make them:

- `lunaroute` as a canonical provider id.
- Connection-write rules: `lr_` key required, fixed hosted endpoint, no key reuse
  on provider switch — plus a launch-time guarded reveal (defence in depth).
- One base-URL normalization point (root vs `/v1` per dialect).
- A runtime **model-family classifier** (single source of truth, fail-closed).
- A **LunaRoute model capability table** (curated, config-loaded) exposed as a
  domain read port with explicit `unknown`.
- **Profile-save validation** for LunaRoute routes: a version grammar and a
  compatibility check.
- LunaRoute **error sentinels + one classifier** used by phases 2–4.

Out of scope for this phase: any HTTP call, MCP tool, or agent/chat adapter change.

### Spec ↔ idea-doc reconciliation (spec wins; recorded with the human)
- Codex and pi are **in scope** (idea: out / stretch) → phase 4.
- Per-connection model discovery via MCP is **in scope** (idea: non-goal) → phase 2.
- Proprietary models and aliases are **allowed**, with family = real vendor
  (idea: plain ids only).
- `LUNAROUTE-PROJECT-ID` header is **dropped** (spec non-goal: usage attribution).
- `-background` model ids are allowed only when the author names them explicitly;
  nothing derives priority automatically (spec non-goal).

## 2. Existing architecture this phase extends

| Seam | Location | How this phase uses it |
|---|---|---|
| Canonical vocabulary | `domain/provider/canonical.go:Canonicalize` | add `CanonicalLunaRoute` |
| Write-time connection rules | `domain/provider/connection.go:ValidateConnection`, called by `adapter/sqlite/connection_repo.go:ConnectionRepo.Create/Update` | add a key-aware sibling rule called at the same point |
| SSRF policy | `domain/provider/url_policy.go:ValidateConnectionURL` | still applies; LunaRoute rule is stricter (fixed host) |
| Binding producers (lazy reveal) | `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve`, `adapter/provider/path_inventory.go:routeBinding` | wrap `RevealKey` with the guarded reveal for LunaRoute |
| Version grammar | `domain/modelidentity/version.go:explicitVersion` | add a `lunaroute` case |
| Route-set validation on profile save | `domain/provider/selection.go:ValidateSelection` → `checkRouteVersions` → `ValidatePinRoutes` | add LunaRoute compatibility check over the same `VersionRequest`s |
| Lab/prefix knowledge | `domain/modelidentity/identity.go:families` | basis of the family classifier |
| Capability ruleset | `domain/provider/capability.go:CapabilityRules`, loaded by `adapter/config/capabilities.go:LoadCapabilityRules` from `capabilities.default.yaml` | carries the new LunaRoute model capability section |
| Test-only family rule | `samples/sample_harness_test.go:modelFamily` | replaced by the domain classifier (targeted improvement: one rule) |

## 3. Components

### 3.1 Canonical id
- **Job:** name the provider. `CanonicalLunaRoute = "lunaroute"`; no aliases.
- **Consumers:** every selector that switches on `Canonicalize` (chat factory,
  agent factory, probe factory, opencode/pi descriptor tables, the new rules).

### 3.2 LunaRoute endpoint helper (domain/provider)
- **Job:** the single place that knows the hosted root and how each dialect
  derives its URL.
- **Contract:** normalize a stored/submitted base URL to the canonical hosted
  root (accepting empty, trailing `/`, or a trailing `/v1`), or report it as a
  non-LunaRoute destination; derive `root` (Anthropic Messages dialect — the
  client appends `/v1/messages`) and `root + /v1` (OpenAI chat-completions,
  Responses and `/v1/models`).
- **Why one place:** a doubled/missing `/v1` is the most likely failure mode.

### 3.3 Connection credential rule (domain/provider)
- **Job:** decide whether a LunaRoute connection write is acceptable.
- **Contract (key-aware sibling of `ValidateConnection`):** given the connection,
  the submitted key (present or absent), and the previously stored provider (on
  update), reject:
  - a submitted key without the `lr_` prefix (BYOK-leak guard);
  - a base URL that does not normalize to the hosted root (spec: hosted service
    only, no custom destinations);
  - an update that changes the provider *to* `lunaroute` without supplying a new
    key (a stored foreign key must never be reused).
- Returns typed sentinels (§3.8) the MCP layer maps to actionable text.
- **Normalization:** the stored base URL is rewritten to the canonical root
  before persistence so downstream adapters always see the root.
- **Called from:** `ConnectionRepo.Create/Update`, at the same point
  `ValidateConnection` is called today (the repo is the single seam every
  writer passes through).

### 3.4 Guarded reveal (domain/provider)
- **Job:** defence in depth at launch time.
- **Contract:** wrap a binding's lazy reveal so that, for a canonical
  `lunaroute` binding, a revealed key without the `lr_` prefix fails closed
  (typed credential error, no key material in the error) before any adapter can
  place it in env/config/headers.
- **Applied by:** both binding producers (`DynamicProfileResolver.Resolve`,
  `routeBinding`). Non-LunaRoute bindings are untouched.

### 3.5 Model-family classifier (domain/modelidentity)
- **Job:** the single runtime answer to "which vendor made this model?".
- **Contract:** model id (optionally provider-prefixed) → lab, or an explicit
  `unresolved`. Strips at most one routing prefix; recognises vendor aliases by
  their vendor name prefix (e.g. `claude-…-lr1` → anthropic), so a Claude alias on
  LunaRoute is classified as Anthropic, never as open-weight.
- **Built from:** the existing `families` table (lab, name prefix, lab aliases).
  Adding a new LunaRoute vendor (e.g. the Muse Spark Open lab) is a one-row table
  edit — deliberate and reviewable.
- **Consumers:** phase-1 compatibility check (§3.7), phase-2 discovery, and
  `samples` family test (replacing the private `modelFamily`).
- **Change-safety:** consumers depend only on lab-or-unresolved; table internals
  can change freely.

### 3.6 LunaRoute model capability table (config + domain read port)
- **Job:** the curated record of *known* per-model facts.
- **Content (per LunaRoute model id):** supported task kinds (chat / agent),
  compatible agent backends, context limit, output limit, supported effort
  levels. Any absent model or field is `unknown`.
- **Storage:** a new LunaRoute-models section in
  `adapter/config/capabilities.default.yaml`, parsed by `LoadCapabilityRules`
  (same embedded-default + operator-override mechanism as `model_overrides`).
- **Domain read port:** lookup by model id → per-field known/unknown values.
  Carried on the already-loaded and already-injected capability ruleset so no new
  root wiring path is invented.
- **Semantics:** the table is the *only* source of capability knowledge in this
  release (live `/v1/models` decides availability only — phase 2). Enforcement
  rejects only what the table marks incompatible; `unknown` is allowed and
  surfaced as unknown, except where §3.7 names a specific known conflict.

### 3.7 Profile-save validation for LunaRoute routes (domain)
Two units, both running inside `ValidateSelection` over the prospective route set
(`VersionRequest`s carrying model, route provider, connection ref, backend):

**(a) LunaRoute version grammar** — a `lunaroute` case in `explicitVersion`.
- Accepts plain catalog-id spellings that carry a numeric version
  (`glm-5.3`, `glm-5.3-flash`, `deepseek-4.1-flash`, `glm-5.3-background`, vendor
  aliases like `claude-sonnet-5-lr1`).
- Rejects floating names (`latest`, unversioned), any `/`-prefixed spelling, and
  empty. Reads no table and no catalog (preserves the resolver's
  "outage cannot stop a working run" property). Refusal names the accepted shape.

**(b) LunaRoute compatibility check** — new domain check, injected with the §3.6
read port, applied to every route whose canonical provider is `lunaroute`.
Rejects, each with its own typed, actionable error:
1. family `unresolved` (§3.5);
2. a profile kind (chat/agent) or agent backend the table marks incompatible;
3. a non-default context tier (Claude `200k`/`1m` tiers are known to conflict
   with gateway models);
4. a non-default effort the table does not list as supported for the model;
5. backend `claude-code` with unknown context or output limit (Claude Code would
   otherwise assume a Claude-sized window);
6. backend `codex` — "LunaRoute on codex not yet supported" (removed in phase 4;
   prevents `ValidatePinRoutes`' codex carve-out from silently treating a
   LunaRoute profile as a subscription run).

Ordering: compatibility runs after grammar so a mis-spelled model gets the
grammar message first.

**Following mode:** LunaRoute has no models.dev catalog slice, so a following
profile finds no candidate and is rejected by the existing
`ErrNoEligibleCandidate` path. LunaRoute profiles are pinned-only in practice;
documented, no new mechanism.

### 3.8 LunaRoute error sentinels + classifier (domain/provider)
- **Job:** one mapping from gateway outcomes to meaning, shared by phases 2–4.
- **Sentinels:** credentials rejected · model unavailable · service busy ·
  service failure · cause unknown; plus write-time sentinels (non-`lr_` key,
  non-hosted endpoint, key required on provider switch) and profile-save
  sentinels (unresolved family, incompatible kind/backend, unsupported axis,
  unknown limits for claude-code, backend not yet supported).
- **Classifier contract:** (HTTP status, gateway error code if any, transport
  error) → one sentinel, carrying a scrubbed bounded snippet; never key material.
  Mapping: 401/invalid-key → credentials; 404/unknown-model → model unavailable;
  429 busy → service busy; 5xx/503/transport → service failure; anything else →
  cause unknown.
- Pure function; no I/O.

## 4. Data flow

**Connection write (MCP `connection_create`/`connection_update`):**
MCP handler → `ConnectionService` (admin gate, unchanged) → `ConnectionRepo` →
`ValidateConnection` (existing) → **LunaRoute credential rule** (§3.3: prefix,
endpoint normalize, provider-switch) → `ValidateConnectionURL` (existing SSRF) →
seal + persist (existing). The plaintext key is inspected only for its prefix and
never logged or echoed.

**Profile write (MCP `profile_create`/`profile_update`):**
`ProfileService.write` → `ValidateSelection` → route requests from the candidate
source → `ValidatePinRoutes` → `explicitVersion` (**LunaRoute grammar**) →
**LunaRoute compatibility check** (family classifier + capability table) →
`validateAxes` (existing) → persist.

**Launch (any LunaRoute binding):**
binding producer → **guarded reveal** wraps `RevealKey` → adapter (phases 2–4)
calls reveal → non-`lr_` key fails closed before use.

## 5. Data Provenance

| Input | Consumer | Production producer |
|---|---|---|
| Provider string on a connection | credential rule, selectors | MCP `connection_create/update` args → `adapter/mcp/tools_connection.go:connectionCreateHandler`/`connectionUpdateHandler` → `provider.Connection.Provider` |
| Submitted plaintext key | credential rule | same MCP handlers → `ConnectionService.Create/Update/UpdatePatch` → `ConnectionRepo.Create/Update` `apiKey` parameter |
| Previously stored provider (update) | credential rule | `adapter/sqlite/connection_repo.go:ConnectionRepo.Update` existing pre-check read `queries.GetConnectionMeta` (`adapter/sqlite/generated/connections.sql.go:GetConnectionMetaRow.Provider`) — already returned, currently discarded |
| Base URL | endpoint helper | MCP args → `provider.Connection.BaseURL` |
| Revealed key at launch | guarded reveal | `provider.ConnectionStore.Reveal` via the closures in `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve` and `adapter/provider/path_inventory.go:routeBinding` |
| Route requests (model, route provider, connection ref, backend) | grammar + compatibility check | `adapter/provider/path_inventory.go:ExecutionPathInventory.VersionRequests` via `domain/modelcatalog/candidate_source.go:CandidateService.VersionRequests`, injected by `cmd/arbitron/main.go` (`profileSvc.SetModelSelection(...)`) |
| Profile kind / context tier / effort | compatibility check | `domain/provider.Profile` from MCP profile tools → `ProfileService.write` |
| Lab/prefix knowledge | family classifier | `domain/modelidentity/identity.go:families` (extended with LunaRoute vendors as needed) |
| Per-model capabilities (kinds, backends, limits, efforts) | compatibility check; phases 2–3 | `adapter/config/capabilities.default.yaml` → `adapter/config/capabilities.go:LoadCapabilityRules` → `cmd/arbitron/main.go` `capabilityRules` → already injected into `ProfileService` and `Factory` deps. **UNRESOLVED DEPENDENCY: the actual values (which LunaRoute models support tool calls / which backends / their context and output limits) are not known offline; they must be populated from the live acceptance run (see Testing, Tier 2). Until populated, every LunaRoute field reads `unknown` — which means claude-code + LunaRoute is rejected by rule 3.7(b)5.** |
| Families for LunaRoute-only vendors (e.g. Muse Spark Open's lab) | family classifier | **UNRESOLVED DEPENDENCY: the vendor/lab of non-GLM/DeepSeek LunaRoute models and the exact alias spellings LunaRoute serves are unverified; populate from the live `/v1/models` listing. Until then such models classify `unresolved` and are rejected at profile save (fail-closed, by design).** |

### Deferred to plans
- **Composition-root wiring:** inject the capability table read port into the
  profile-save path (`ValidateSelection` is a free function today; `ProfileService`
  already holds `CapabilityRules` and must pass the port through).
- **Repo change:** `ConnectionRepo.Update` must keep (rather than discard) the
  `GetConnectionMetaRow` its existence pre-check already reads, and pass the
  stored provider to the credential rule; the rule must run after that read.
- **Binding producers:** apply the guarded reveal in *both*
  `DynamicProfileResolver.Resolve` and `routeBinding`; a root wiring test must
  prove a LunaRoute binding from each producer is guarded.
- **Schema:** none (no new columns; base URL is normalized in place).
- **Config schema:** the YAML shape of the LunaRoute-models section and its
  loader validation (unknown backend names, negative limits rejected at load).
- **Data population:** capability-table values and family rows for LunaRoute
  vendors, from the Tier-2 live run.
- **Samples test:** switch `samples/family_test.go` to the domain classifier and
  delete the private `modelFamily`.

## 6. Integration seams

- `Canonicalize` — every downstream selector gets `lunaroute` for free.
- `ConnectionRepo.Create/Update` — single write seam (no change to
  `ConnectionService` or MCP handler logic beyond error mapping in phase 2).
- `ValidateSelection` — single profile-save seam for both pinned and following.
- `CapabilityRules` — single loaded config object; the LunaRoute table rides it.
- Binding producers — single launch seam for the guarded reveal.

## 7. Error handling

- All rejections are **typed sentinels** (§3.8), matchable with `errors.Is`, each
  carrying an actionable message naming what to change (e.g. "LunaRoute keys
  start with `lr_`", "LunaRoute connections must use the hosted endpoint",
  "model `X` family is unresolved; add its vendor to the family table",
  "claude-code on LunaRoute needs known context/output limits for `X`").
- Fail-closed everywhere: an unresolved family, an unknown-limit claude-code
  route, a non-`lr_` reveal, or a codex route never proceed.
- No error string ever contains key material; tests assert this.
- No substitution: validation never rewrites the model, backend or provider
  (only the base URL is normalized, to the same destination).

## 8. Testing — STRICT TDD (all five categories required)

Every unit is written test-first; each category below must have tests before the
code that satisfies it.

- **Happy-path:** `lr_` key + empty/hosted base URL creates a connection whose
  stored base URL is the canonical root; `glm-5.3` agent profile on opencode with
  a table entry saves; a Claude alias classifies as anthropic.
- **Success:** endpoint helper derives root and `/v1` correctly for every
  accepted input spelling; grammar accepts each documented spelling incl.
  `-background`; guarded reveal passes an `lr_` key through unchanged;
  samples family test passes using the domain classifier.
- **Failure:** non-`lr_` key (e.g. `sk-…`, `sk-or-…`) rejected on create and on
  update; non-hosted base URL rejected; provider switch to `lunaroute` without a
  new key rejected; unresolved family rejected; table-incompatible backend
  rejected; non-default context tier rejected; claude-code with unknown limits
  rejected; codex + LunaRoute rejected.
- **Error:** guarded reveal with a stored non-`lr_` key fails closed with a
  credential sentinel; reveal error propagates without partial values; classifier
  maps each status/transport case to the right sentinel; no error text contains
  the key.
- **Edge-case:** base URL `https://gw.lunaroute.com/`, `/v1`, `/v1/`, mixed case
  host; key exactly `lr_`; whitespace around provider; `glm-5.3` vs
  `GLM-5.3`; `/`-prefixed model; `latest`; model absent from table (unknown, not
  rejected unless a named conflict applies); update with extras omitted preserves
  the existing merge behaviour.
- **Tier 2 (live, gated — see phase 2/3/4 docs):** populates the table and family
  rows this phase leaves UNRESOLVED.
