# LunaRoute Provider — Design Phase 2: Discovery & chat

**Depends on:** Phase 1 (canonical id, endpoint helper, guarded reveal, family
classifier, capability table, error classifier).
**Approach (approved):** A — extend existing seams; no new LunaRoute adapter package.

## 1. Purpose and scope

- An LLM working through MCP can list the models available through a chosen
  connection, each with family, task kinds, compatible backends and
  context/output limits, with unknowns marked explicitly.
- LunaRoute chat nodes work through the existing OpenAI-compatible chat adapter.
- Gateway failures surface as distinguishable, actionable errors.
- The agent-provider **post-failure diagnosis wrapper** is introduced here (used
  by phases 3–4).

## 2. Existing architecture this phase extends

| Seam | Location | Use |
|---|---|---|
| Probe port | `domain/provider/probe.go:ConnectionProbe`, `ProbeSelector`, `ProbeResult` | LunaRoute probe implements it |
| Probe dispatcher | `adapter/probe/factory.go:ProbeFactory.ProbeFor` | new `lunaroute` arm |
| Shared probe HTTP helpers | `adapter/probe/httpshared.go` (bounded read, scrub, scheme check) | reused by the LunaRoute probe |
| CLI validate | `cmd/arbitron/connection.go:runConnectionValidate` | gains LunaRoute support automatically |
| Connection reads (admin) | `domain/provider/connection_service.go:ConnectionService` | discovery service follows its admin rule |
| MCP registration | `adapter/mcp/register.go`, `adapter/mcp/tools_connection.go` | new `connection_models` tool; error mapping for phase-1 sentinels |
| Chat dispatch | `adapter/provider/factory.go:Factory.ChatProviderFor` | new `lunaroute` arm |
| OpenAI-compatible chat | `adapter/openrouter` (`request.go:newRequest` → `{BaseURL}/chat/completions`, Bearer) | reused with base `<root>/v1` |
| Agent dispatch | `adapter/provider/factory.go:Factory.AgentProviderFor` | wrapped for LunaRoute bindings |

## 3. Components

### 3.1 LunaRoute model probe (adapter/probe)
- **Job:** authenticate against the gateway and list available model ids.
- **Contract:** implements `ConnectionProbe`. Calls `GET <root>/v1/models` (URL
  from the phase-1 endpoint helper) using the guarded reveal; returns
  `ProbeResult{Reachable, Models}`. Non-2xx and transport errors are mapped by the
  **phase-1 LunaRoute classifier** (not the generic `classifyStatus`), so callers
  can tell credentials / unavailable / busy / service failure / unknown apart.
- **Does not** parse limits or capabilities from the listing (phase-1 decision:
  availability only).
- **Registered as:** the `lunaroute` arm of `ProbeFactory.ProbeFor`.

### 3.2 Connection model discovery service (domain/provider)
- **Job:** produce the per-connection discovery view.
- **Contract:** input = connection name (admin identity required, same rule as
  `ConnectionService`). Output = list of entries, each: model id, family (lab or
  `unresolved`), task kinds, compatible backends, context limit, output limit,
  supported efforts — each capability field explicitly `known(value)` or
  `unknown`; plus a top-level note that availability does not imply
  compatibility. Errors are the phase-1 sentinels.
- **Collaborators (ports only):** `ConnectionStore` (metadata Get + Reveal, via a
  binding built with the guarded reveal), `ProbeSelector`, family classifier,
  capability table read port.
- **Provider-agnostic:** works for any provider with a probe; non-LunaRoute
  providers return ids with family from the classifier and all table fields
  `unknown` (only LunaRoute is curated in this release). Providers whose probe
  reports `ModelsUnsupported` yield an explicit "listing not supported" result.
- **Change-safety:** the MCP tool depends only on the service's output contract.

### 3.3 MCP tool `connection_models` (adapter/mcp)
- **Job:** expose §3.2 to the LLM.
- **Contract:** one required argument (connection name); returns the discovery
  view as JSON; never returns key material. Description tells the LLM that
  `unknown` fields are not guarantees and that it must choose model and backend
  explicitly.
- Maps phase-1 sentinels to stable, actionable messages (credential rejected,
  service unavailable/busy, cause unknown, forbidden, not found).
- Also: `connErrorResult` gains mappings for the phase-1 connection-write
  sentinels (non-`lr_` key, non-hosted endpoint, key required on provider switch);
  profile tools gain mappings for the phase-1 profile-save sentinels.
- Tool descriptions for `connection_create` list `lunaroute` among provider ids.

### 3.4 Chat route (adapter/provider factory)
- **Job:** route `lunaroute` chat bindings.
- **Contract:** `ChatProviderFor` arm builds the existing `adapter/openrouter`
  provider with the binding's base URL replaced by the endpoint helper's `/v1`
  form; no OpenRouter-specific headers are sent; non-2xx responses from a
  LunaRoute binding are classified with the phase-1 classifier (so chat errors
  say "credentials rejected" / "model unavailable" / … rather than a generic
  upstream status).
- **No new chat adapter:** request/response contract is identical.

### 3.5 Agent post-failure diagnosis wrapper (adapter/provider)
- **Job:** turn opaque agent failures on LunaRoute into classified errors.
- **Contract:** decorates the `engine.AgentProvider` returned by
  `AgentProviderFor` when the binding's canonical provider is `lunaroute`.
  Success path is a pure pass-through (zero extra calls). On a failed run it
  performs one LunaRoute probe with the same binding and wraps the original error
  with exactly one of: credentials rejected · model `X` not in catalog · service
  unavailable/busy · "agent failed; LunaRoute reachable and model listed — cause
  unknown" — always preserving the original error and its stderr snippet. A
  failing diagnosis never masks the original error.
- Applies to whichever backends phases 3–4 enable; in this phase it is unit-tested
  against fake agent providers.
- **No substitution / no retry** on another model, backend or provider.

## 4. Data flow

**Discovery:** MCP `connection_models(name)` → discovery service (admin gate) →
`ConnectionStore.Get` → binding (guarded reveal) → `ProbeSelector.ProbeFor` →
LunaRoute probe `GET <root>/v1/models` → ids → for each id: family classifier +
capability table → view → JSON.

**Chat:** `llm_call` node → profile resolver → binding (guarded reveal) →
`ChatProviderFor` (`lunaroute` arm) → `adapter/openrouter` POST
`<root>/v1/chat/completions` (Bearer `lr_…`) → response, or classified error.

**Agent failure:** factory-built agent provider fails → wrapper → LunaRoute probe →
classified, wrapped error → engine node failure (no fallback).

## 5. Data Provenance

| Input | Consumer | Production producer |
|---|---|---|
| Connection name | discovery service | MCP `connection_models` argument (new tool, §3.3) |
| Connection metadata (provider, base URL) | discovery service | `domain/provider.ConnectionStore.Get` — production instance `adapter/sqlite.ConnectionRepo`, already on MCP deps as `adapter/mcp/deps.go:Deps.Connections` |
| Key for listing | LunaRoute probe | `ConnectionStore.Reveal` wrapped by the phase-1 guarded reveal |
| Probe selector | discovery service, agent wrapper | `adapter/probe/factory.go:NewProbeFactory` — **today constructed only in `cmd/arbitron/connection.go` (CLI)**; must be constructed in the serve composition root (deferred item) |
| Available model ids | discovery service, agent wrapper | LunaRoute `GET /v1/models` via the new probe arm. **UNRESOLVED DEPENDENCY: the exact response envelope (assumed OpenAI-style `data[].id`) and the gateway's error-code bodies (assumed `INVALID_API_KEY` / `SERVICE_UNAVAILABLE` / `*_busy`, per the idea doc) are unverified offline; confirm in the Tier-2 live run before relying on code-level mapping beyond HTTP status.** |
| Family per model | discovery service | phase-1 family classifier over `domain/modelidentity/identity.go:families` |
| Task kinds / backends / limits / efforts | discovery service | phase-1 capability table (`capabilities.default.yaml` → `LoadCapabilityRules`). **UNRESOLVED DEPENDENCY (inherited from phase 1): values not yet populated; all read `unknown` until the live run fills them.** |
| HTTP client, timeout, response cap | probe, chat route | `cmd/arbitron/main.go:newOutboundHTTPClient` → `httpClient`, already in the factory deps; probe `Deps.HTTPClient/Timeout/MaxResponseBytes` |
| Chat binding (model, base URL, reveal) | chat route | `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve` |

### Deferred to plans
- **Composition-root wiring (serve path):** construct one `ProbeFactory` with the
  shared outbound HTTP client and inject it into (a) the discovery service and
  (b) the `Factory` deps for the agent wrapper; construct the discovery service
  and add it to `adapter/mcp/deps.go:Deps`; register `connection_models` in
  `adapter/mcp/register.go`. A root wiring test must prove the tool is reachable
  and non-nil-backed in the production server (not only in tests).
- **Factory deps:** add the probe selector to `adapter/provider` `Deps`; the
  agent wrapper is applied inside `AgentProviderFor` only for `lunaroute`.
- **Error mapping tables:** MCP connection/profile error results for every
  phase-1 sentinel.
- **Docs:** MCP authoring guidance (`adapter/mcp/authoring_guidance.go`) mentions
  `connection_models` and that LunaRoute model choice is explicit.

## 6. Integration seams

- `ProbeSelector` — new arm only; CLI `connection validate` benefits unchanged.
- `ChatProviderFor` — new arm; openrouter adapter reused.
- `AgentProviderFor` — return value decorated for LunaRoute only; other backends'
  behaviour byte-identical.
- MCP — one new tool; existing tools gain error mappings only.

## 7. Error handling

- One classifier (phase 1) used by probe, chat and agent wrapper → identical
  wording across surfaces.
- Distinct outcomes: credentials rejected · model unavailable · service busy ·
  service failure · cause unknown (explicitly labelled as uncertain, with a
  scrubbed snippet).
- Discovery on a non-admin identity → forbidden; missing connection → not found.
- Diagnosis failures never replace the original agent error.
- No key material in any error, log line or tool result (asserted in tests).
- No fallback or substitution anywhere.

## 8. Testing — STRICT TDD (all five categories required)

- **Happy-path:** `connection_models` against an `httptest` fake gateway returns
  every listed id with family and table-backed fields; a LunaRoute chat call
  against a fake `/v1/chat/completions` returns content.
- **Success:** probe sends Bearer `lr_…` to `<root>/v1/models` (exact path, no
  doubled `/v1`); chat posts to `<root>/v1/chat/completions` with no OpenRouter
  headers; CLI `connection validate` lists LunaRoute models; agent wrapper is a
  pure pass-through on success (fake probe asserts zero calls).
- **Failure:** 401 → credentials rejected; 404 / model absent → model unavailable;
  429 → busy; 503 → service failure; teapot/unknown status → cause unknown;
  non-admin discovery → forbidden; unknown connection → not found.
- **Error:** transport error / TLS failure → service failure; oversized body →
  bounded-read error; guarded reveal rejects non-`lr_` stored key before any
  request is sent (fake server asserts no request); agent wrapper with a failing
  diagnosis returns the original error intact.
- **Edge-case:** empty model list (reachable, zero models); model id present in
  listing but absent from table (all fields `unknown`); table entry for a model
  absent from listing (not shown — availability rules); unresolved-family model
  shown as `unresolved`; provider with `ModelsUnsupported`; key never appears in
  results/logs/errors (log-capture assertion).
- **Tier 2 (live, gated by `ARBITRON_LUNAROUTE_E2E_KEY`, skipped otherwise):**
  through the real MCP tools — create connection, `connection_models`, create a
  chat profile, run a representative chat task to completion; invalid key and
  unknown model produce the distinct errors; confirms the `/v1/models` envelope
  and error bodies (closing this phase's UNRESOLVED items) and records the
  capability-table values for phase 1.
