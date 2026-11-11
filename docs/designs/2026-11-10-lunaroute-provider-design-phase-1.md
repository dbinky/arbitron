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
- One base-URL normalization point (root vs `/v1` per dialect), enforced again
  before credential use.
- A runtime **model-family classifier** (single source of truth, fail-closed).
- A **LunaRoute model capability table** (curated, config-loaded) exposed as a
  domain read port with explicit `unknown`.
- **Profile-save and launch validation** for LunaRoute routes: a version grammar
  and a compatibility check over the effective route.
- LunaRoute **error sentinels + one classifier** used by phases 2–4.

Out of scope for this phase: any HTTP call, MCP tool, or agent/chat adapter change.
The pure rules are consumed by the production paths in phases 2–4; phase 1 alone
does not make LunaRoute executable.

The release uses existing workflow node types, connections and profiles. It adds
no subscription changes, recommendations/fallbacks, custom service destinations,
scheduling/priority inference, usage/billing reports, dedicated model-browser UI
or the spec's excluded inference modalities and LunaRoute MCP service.

### Spec ↔ idea-doc reconciliation (spec wins)
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
  root `https://gw.lunaroute.com` (accepting empty, trailing `/`, or a trailing
  `/v1`), or report it as a non-LunaRoute destination; derive `root` (Anthropic Messages
  dialect — the
  client appends `/v1/messages`) and `root + /v1` (OpenAI chat-completions,
  Responses and `/v1/models`).
- Reject other schemes, hosts, ports, userinfo, query/fragment and arbitrary
  paths. A general HTTPS/SSRF check does not enforce the hosted-only rule. Every
  credential sink in phases 2–4 calls this helper before use, including probes.
- **Why one place:** a doubled/missing `/v1` is the most likely failure mode.
  **LR-01 decided 2026-11-10 ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** the root, the four `/v1` paths and `lr_`
  auth are verified against LunaRoute's docs. Every sink sends
  `Authorization: Bearer`. The P1-T11 live test is a release-mode regression check.

### 3.3 Connection credential rule (domain/provider)
- **Job:** decide whether a LunaRoute connection write is acceptable.
- **Contract (key-aware sibling of `ValidateConnection`):** given the connection,
  the submitted key (present or absent), and the previously stored provider (on
  update), reject:
  - a missing/empty create key, or a submitted key without a nonempty suffix
    after the `lr_` prefix (BYOK-leak guard; `lr_` alone is not a credential);
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
- Reject missing reveal closures, empty keys and `lr_` without a suffix as well. Pair this with the
  endpoint guard before any credential is used.
- **Applied by:** `DynamicProfileResolver.Resolve`, `routeBinding`, and the
  connection-built bindings in CLI validation and phase-2 discovery. The current
  `routeBinding` deliberately disables reveal for codex; phase 4 must narrow that
  suppression for LunaRoute. Non-LunaRoute bindings are untouched.

### 3.5 Model-family classifier (domain/modelidentity)
- **Job:** the single runtime answer to "which vendor made this model?".
- **Contract:** route provider + model id → actual vendor lab, or explicit
  `unresolved` (route provider may be empty for a no-connection/sample input).
  Strip at most one recognised routing prefix and reject a
  contradictory vendor prefix. Recognised vendor spellings (including verified
  `claude-…-lr1` aliases) resolve to their actual vendor, never the gateway or
  a generic open-weight family. Opaque aliases require a reviewed exact-id to
  vendor mapping; never infer their vendor from availability or arbitrary text.
- **Built from:** the existing `families` table (lab, name prefix, lab aliases).
  This table currently limits recognition by catalog provider and has no
  LunaRoute entries; adding the new route and any exact alias mappings is
  explicit work. Preserve existing o-series and Bedrock-qualified recognition
  from `samples/sample_harness_test.go:gptModelRE` and `vendorQualifierRE`; the
  current `families` table alone does not cover these cases. A model listing
  alone cannot prove an alias target or vendor.
- **Consumers:** phase-1 compatibility check (§3.7), phase-2 discovery, and
  `samples` family checks (replacing the private `modelFamily`). Update fixtures
  to canonical lab names (`anthropic`, `openai`, `zhipuai`, etc.). Preserve
  `samples/family_test.go:judgePairingProblems` rejection of same-vendor pairings
  and unresolved families, including proprietary aliases across different
  gateways/backends. That pairing table is a sample-test input, not existing
  runtime doer/judge metadata. MCP guidance uses the same discovery family values
  when the author checks a pairing; profile/launch validation rejects unresolved
  LunaRoute families unconditionally.
- **Change-safety:** consumers depend only on lab-or-unresolved; table internals
  can change freely.

### 3.6 LunaRoute model capability table (config + domain read port)
- **Job:** the curated record of *known* per-model facts.
- **Content (per LunaRoute model id):** supported task kinds (chat / agent),
  compatible agent backends, context limit, output limit, supported effort
  levels per task/backend, independent reasoning support required by custom backend metadata, and
  known request-setting conflicts (e.g. output
  token bounds). Each known fact carries an evidence reference. Any absent
  model or field is `unknown`; one successful task proves only that tested
  combination, not all tasks/backends, all efforts, or maximum token limits.
- **Storage:** a new LunaRoute-models section in
  `adapter/config/capabilities.default.yaml`, parsed by `LoadCapabilityRules`
  (same embedded-default + operator-override mechanism as `model_overrides`).
- **Domain read port:** lookup by model id → per-field known/unknown values.
  Request-setting facts use `temperature` / `output_schema` support facts; output bounds use the
  existing limit fields. Reasoning is a separate supported/unsupported/unknown fact, never
  inferred from effort levels. Carried on the already-loaded and already-injected capability
  ruleset so no new
  root wiring path is invented.
- **Semantics:** the table is the *only* source of capability knowledge in this
  release (live `/v1/models` decides availability only — phase 2). Enforcement
  rejects only what the table marks incompatible; `unknown` is allowed and
  surfaced as unknown, except for explicit settings and required backend
  metadata checks in §3.7. A positive observation does not make omitted
  backends incompatible, and unknown is never presented as proven support.

### 3.7 Profile-save validation for LunaRoute routes (domain)
Two units, both running inside `ValidateSelection` over the prospective route set
(`VersionRequest`s carrying model, route provider, connection ref, backend):

**(a) LunaRoute version grammar** — a `lunaroute` case in `explicitVersion`.
- Extend the existing fixed-version contract using reviewed LunaRoute
  spellings, including open-weight ids, proprietary ids, aliases and explicitly
  selected `-background` ids. Candidate examples from the idea doc are
  `glm-5.3`, `deepseek-4.1-flash` and `claude-sonnet-5-lr1`; these are not evidence
  of immutable alias targets. Do not accept arbitrary ids just because they
  contain a digit or reject every vendor-prefixed catalog id.
- Reject empty, floating or unverified fixed-version spellings with an
  actionable refusal. Reads no live catalog (preserves the resolver's
  "outage cannot stop a working run" property).
- **LR-03 decided 2026-11-10 ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** the reviewed fixed ids are `glm-5.3`,
  `glm-5.3-flash` and `deepseek-4.1-flash-1017`. The reviewed alias
  `claude-sonnet-5-lr1` (vendor `anthropic`, target `alias:claude-sonnet-5-lr1`)
  is a D8-style pinned-only exception: configured-ID continuity, no
  exact-version claim, execution evidence must report a model in the reviewed
  vendor family (a different family fails the run; drift inside it is recorded).
  Unreviewed aliases are refused. A numeric alias is never proof.

**(b) LunaRoute compatibility check** — new domain check, injected with the §3.6
read port, applied to every route whose canonical provider is `lunaroute`.
Rejects, each with its own typed, actionable error:
1. family `unresolved` (§3.5);
2. a profile kind (chat/agent) or agent backend the table marks incompatible;
3. a non-default context tier: this release provides no verified LunaRoute
   context-tier emission path (Claude-native `200k`/`1m` switches are not model
   token limits and must not be assumed valid for a gateway route);
4. an explicit non-default effort (including `none`) without known support for
   this model/task/backend, or without a backend path that emits it as requested;
   LunaRoute must not use the existing generic effort flooring to change it;
5. backend `claude-code` with unknown context or output limit (Claude Code would
   otherwise assume a Claude-sized window);
6. an agent profile without an explicitly selected backend; deployment defaults
   do not choose LunaRoute backends for the author;
7. backend `codex` — "LunaRoute on codex not yet supported" (removed in phase 4).
   Check the original route provider **before** `ValidatePinRoutes` erases it for
   codex; otherwise this rejection is bypassed. Other not-yet-enabled backend
   paths also fail closed until their enabling phase is complete.

Ordering: test temporary backend enablement on the original route first, then
grammar, then model compatibility. Pass the same rules through
`ProfileService.revalidatePrepared` as well as `ValidateSelection`. Route-aware effective-axis
reporting must preserve the validated requested LunaRoute setting; generic native-provider
flooring must not alter it. Refuse following mode before catalog selection, including when Model
is empty.

**Launch validation:** phases 2–4 run this pure compatibility rule on the actual
resolved model, effective backend (after any node override), context and effort
before HTTP/subprocess I/O. Retain whether the backend was explicitly selected
(`ResolvedBinding.BackendExplicit`, produced from Profile.Backend by both binding producers, ORed
with a node override before deployment defaulting); an effective default cannot masquerade as
author intent. This covers profile seeds, unchanged profile edits,
connection changes, run pins and workflows added after profile save. No online
probe is required before a working run. Factory inventory uses model-empty
bindings in `pathBuilder.add`; construction must remain lazy and must not reveal
credentials or require per-model capabilities for those metadata-only bindings.
Validate real calls at `Chat`/`StreamChat`/`RunAgent`. Until a LunaRoute agent
interactive path is explicitly implemented, `InteractiveStreamSelector` rejects
it; it must not reach a subscription stream adapter.

**Following mode:** this design adds connection discovery, not a LunaRoute slice
in the global following catalog. Explicitly keep LunaRoute selections pinned-only
and refuse following mode with an actionable message; do not depend on an assumed
permanent absence from models.dev or let a later catalog refresh auto-select one.

### 3.8 LunaRoute error sentinels + classifier (domain/provider)
- **Job:** one mapping from gateway outcomes to meaning, shared by phases 2–4.
- **Sentinels:** credentials rejected · model unavailable · service busy ·
  service failure · cause unknown; plus write-time sentinels (non-`lr_` key,
  non-hosted endpoint, key required on provider switch) and profile-save
  sentinels (unresolved family, incompatible kind/backend, unsupported axis,
  unknown limits for claude-code, backend not yet supported, backend required).
- **Classifier contract:** (operation, HTTP status, verified gateway error code
  if any, transport error, redacted diagnostic text) → one sentinel, carrying a scrubbed
  bounded snippet; never key material.
  Mapping: 401 or a verified invalid-key code → credentials; a verified
  unknown-model code or an authoritative successful listing missing the selected
  model → model unavailable; 429 → service busy; 5xx/transport → service failure.
  A bare 404 from `/models` means a failed listing, not an unavailable selected
  model; unrecognised 403/404/400 causes remain unknown. Endpoint/operation
  context is therefore also an input. Verified incompatibility codes map to the
  compatibility sentinel; do not guess codes from prose.
- Control scrubbing is not secret redaction. Redact the revealed key and auth
  material before truncation in all errors/snippets. Existing
  `adapter/procenv/httpx.go:StatusSnippet` only caps/scrubs controls; it cannot
  ensure this if an upstream body echoes a key. Preserve error identity through
  `errors.Is/As` without exposing raw secret-bearing error strings.
- Pure function; no I/O.

## 4. Data flow

**Connection write (MCP `connection_create`/`connection_update`):**
MCP handler → `ConnectionService` (admin gate, unchanged) → `ConnectionRepo` →
`ValidateConnection` (existing) → **LunaRoute credential rule** (§3.3: prefix,
endpoint normalize, provider-switch) → `ValidateConnectionURL` (existing SSRF) →
seal + persist (existing). The plaintext key is inspected only for its prefix/suffix shape and
never logged or echoed.

**Profile write (MCP `profile_create`/`profile_update`):**
`ProfileService.write` → `ValidateSelection` → route requests from the candidate
source → original-route enablement check → `ValidatePinRoutes` →
`explicitVersion` (**LunaRoute grammar**) →
**LunaRoute compatibility check** (family classifier + capability table) →
`validateAxes` (existing) → persist.

**Launch (any LunaRoute binding):**
binding producer → **guarded reveal** wraps `RevealKey` → actual-call compatibility
and hosted-endpoint checks → adapter (phases 2–4) calls reveal → invalid key fails
closed before use. Metadata-only factory construction performs no I/O.

## 5. Data Provenance

| Input | Consumer | Production producer |
|---|---|---|
| Provider string and base URL on a connection | credential rule, selectors, endpoint helper | MCP `connection_create/update` args → `adapter/mcp/tools_connection.go:connectionCreateHandler`/`connectionUpdateHandler` → `domain/provider/connection.go:Connection.Provider/BaseURL` |
| Submitted plaintext key | credential rule | `adapter/mcp/tools_connection.go:connectionCreateHandler/connectionUpdateHandler` → `domain/provider/connection_service.go:ConnectionService.Create/Update/UpdatePatch` → `adapter/sqlite/connection_repo.go:ConnectionRepo.Create/Update` `apiKey` parameter |
| Previously stored provider (update) | credential rule | `adapter/sqlite/connection_repo.go:ConnectionRepo.Update` existing pre-check read `queries.GetConnectionMeta` (`adapter/sqlite/generated/connections.sql.go:GetConnectionMetaRow.Provider`) — already returned, currently discarded |
| Revealed key at launch | guarded reveal | `provider.ConnectionStore.Reveal` via the closures in `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve` and `adapter/provider/path_inventory.go:routeBinding` |
| Route requests (model, route provider, connection ref, backend) | grammar + compatibility check | `adapter/provider/path_inventory.go:ExecutionPathInventory.VersionRequests` via `domain/modelcatalog/candidate_source.go:CandidateService.VersionRequests`, injected by `cmd/arbitron/main.go` (`profileSvc.SetModelSelection(...)`) |
| Profile kind / explicit backend / context tier / effort | compatibility check | `domain/provider/provider.go:Profile`, parsed by `adapter/mcp/tools_profile.go:profileCreateHandler/profileUpdateHandler`, then `domain/provider/profile_service.go:ProfileService.write`. |
| Lab/prefix knowledge | family classifier | `domain/modelidentity/identity.go:families` plus existing spelling evidence `samples/sample_harness_test.go:gptModelRE/vendorQualifierRE` and cases in `samples/family_test.go:TestModelFamily`; extend explicitly for LunaRoute, with LunaRoute vendor/alias facts fixed by the LR-03 decision. |
| Per-model capabilities and request-setting conflicts | compatibility check; phases 2–4 | Existing container/loader: `domain/provider/capability.go:CapabilityRules`, `adapter/config/capabilities.go:LoadCapabilityRules`, injected by `cmd/arbitron/main.go` into `ProfileService` and `Factory`. These currently have backend axes only; the new LunaRoute section/read port must be implemented. **LR-02 decided 2026-11-10 ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** the release set is `glm-5.3`, `glm-5.3-flash`, `deepseek-4.1-flash-1017` and `claude-sonnet-5-lr1`. Static facts (limits, reasoning, efforts, settings) come only from LunaRoute's model docs, collected by P1-T11 into the embedded default. Task/backend `supported` comes only from completed live runs (P2-T8, P3-T5, P4-T1). There are no placeholders: an unknown limit makes a model unusable on claude-code, opencode and pi. |
| Vendor and alias identity / fixed-version spellings | family classifier, version grammar | Existing prefix evidence: `domain/modelidentity/identity.go:families`; current grammars: `domain/modelidentity/version.go:explicitVersion`. **LR-03 decided 2026-11-10 ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** `version.go:lunaRouteReviewedIDs` ships the release ids with vendors from `identity.go:families` (`zhipuai`, `deepseek`, `anthropic`). `claude-sonnet-5-lr1` is a reviewed alias under the D8-style exception. `/v1/models` availability never proves vendor. |
| Hosted root, dialect URL paths and accepted credential headers | endpoint helper; phases 2–4 sinks | **LR-01 decided 2026-11-10 ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** `https://gw.lunaroute.com`, `/v1/{models,chat/completions,messages,responses}`, `lr_` keys only, Bearer on every sink. Docs-verified (`docs.lunaroute.com/api/`, `/api/models/`, `/getting-started/authentication/`). New producer: `domain/provider/lunaroute_endpoint.go` (P1-T1). |
| Existing doer/judge pairs and model inputs | safeguard regression tests | `samples/family_test.go:judgePairings`, `judgePairingProblems`, `familyOfNode`; model values from `samples/sample_harness_test.go:profileTable`. These are sample tests; no runtime pair-role field is assumed. |
| Actual launch model / backend / axes | runtime compatibility check | `domain/provider/pinning.go:PinningResolver.Resolve` supplies pinned model over `DynamicProfileResolver.Resolve`; `domain/engine/executor_agent.go:AgentCallExecutor.Execute` applies effective backend; `domain/provider/provider.go:ResolvedBinding` carries axes. |
| Classifier status, code, transport error and operation | shared classifier | HTTP response/error from `adapter/openrouter/provider.go:Provider.Chat/StreamChat` and phase-2 probe (new consumer); response body decoding follows the LR-04 decision (documented code table, bare or wrapped error envelope). Local validation supplies known conflicts directly. |

New components in this phase are proposed work, not existing producers. The
external leaves LR-01–LR-03 are decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)); live evidence verifies them, and
each decision's On-failure rule applies if it contradicts them. Fixtures alone never
flip a register item to `verified`.

### Deferred to plans
- **Composition-root wiring:** inject the capability table read port into the
  profile-save path (`ValidateSelection` is a free function today; `ProfileService`
  already holds `CapabilityRules` and must pass the port through).
- **Repo change:** `ConnectionRepo.Update` must keep (rather than discard) the
  `GetConnectionMetaRow` its existence pre-check already reads, and pass the
  stored provider to the credential rule; the rule must run after that read.
  Keep provider/key validation coherent with the update so concurrent provider
  switches cannot reuse a foreign key.
- **Binding producers:** apply the guarded reveal in *both*
  `DynamicProfileResolver.Resolve` and `routeBinding`; a root wiring test must
  prove a LunaRoute binding from each producer is guarded.
- **Schema:** none (no new columns; base URL is normalized in place).
- **Config schema:** the YAML shape of the LunaRoute-models section and its
  loader validation (unknown backend names, non-positive known limits rejected
  at load). Use per-model override replacement for the new section; preserve
  `unknown` separately from known empty/unsupported values and define independent
  task/backend facts rather than interpreting omissions as denial.
- **Data population:** the LR-02/LR-03 decisions fix the release set. P1-T11
  collects doc-sourced static facts and confirms the id spellings. P1-T7/P1-T8
  write them to production sources. Scoped Tier-2 completion observations add
  task/backend support. None of these are outputs invented by tests.
- **Samples test:** switch `samples/family_test.go` to the domain classifier and
  delete the private `modelFamily` in `samples/sample_harness_test.go`.

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
  "model `X` family is unresolved; establish vendor evidence before adding a mapping",
  "claude-code on LunaRoute needs known context/output limits for `X`").
- Fail-closed everywhere: an unresolved family, an unknown-limit claude-code
  route, a non-`lr_` reveal, or a not-yet-enabled codex route never proceed.
- No error string ever contains key material; tests assert this.
- No substitution: validation never rewrites the model, backend or provider
  (only the base URL is normalized, to the same destination).

## 8. Testing — STRICT TDD (all five categories required)

Every unit is written test-first; each category below must have failing tests
before the code that satisfies it, followed by green tests and refactoring.

- **Happy-path:** valid `lr_…` key + empty/hosted base URL creates a connection whose
  stored base URL is the canonical root; `glm-5.3` agent profile on opencode with
  a table entry saves; a Claude alias classifies as anthropic.
- **Success:** endpoint helper derives root and `/v1` correctly for every
  accepted input spelling; grammar accepts each documented spelling incl.
  `-background`; guarded reveal passes an `lr_` key through unchanged;
  samples family test passes using canonical vendor names; same-vendor aliases
  across backends still fail the existing doer/judge checks.
- **Failure:** non-`lr_` key (e.g. `sk-…`, `sk-or-…`) rejected on create and on
  update; non-hosted base URL rejected; provider switch to `lunaroute` without a
  new key rejected; unresolved family rejected; table-incompatible backend
  rejected; non-default context tier rejected; claude-code with unknown limits
  rejected; codex + LunaRoute rejected before the subscription carve-out;
  explicit effort conflicts rejected without flooring; absent backend rejected.
- **Error:** guarded reveal with a stored non-`lr_` key fails closed with a
  credential sentinel; reveal error propagates without partial values; classifier
  maps each status/transport case to the right sentinel; no error text contains
  the key, including echoed upstream keys and raw transport errors.
- **Edge-case:** base URL `https://gw.lunaroute.com/`, `/v1`, `/v1/`, mixed case
  host; key exactly `lr_` rejected; whitespace around provider; `glm-5.3` vs
  `GLM-5.3`; `/`-prefixed model; `latest`; model absent from table (unknown, not
  rejected unless a named conflict applies); conflicting vendor prefixes; opaque
  aliases; model-empty inventory construction does no I/O; update with extras
  omitted preserves the existing merge behaviour; concurrent provider switches
  cannot retain a foreign credential.
- **Tier 2 (live, gated — see phase 2/3/4 docs):** collect observations and
  verification evidence for the decided LR items in §5 (`2026-11-10-lunaroute-provider-decisions.md`). Validate metadata and
  limits before saving the claude-code acceptance profile; a passing short task
  alone does not populate limits. Missing live credentials skip local tests but
  leave release readiness blocked until the complete acceptance set passes.
