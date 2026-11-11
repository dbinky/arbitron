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
  **phase-1 LunaRoute classifier** with operation context (not the generic
  `classifyStatus`), so callers can tell credentials / unavailable / busy / service failure
  / unknown apart.
- Enforce the phase-1 hosted endpoint and key rules within the probe, including
  CLI-created bindings (which bypass the profile resolver). Disable redirects
  for LunaRoute credential-bearing HTTP calls; the existing shared client has
  no redirect policy. Test via an injected transport responding at the canonical
  hosted URL, rather than weakening the production endpoint rule for localhost.
- **Does not** parse limits or capabilities from the listing (phase-1 decision:
  availability only).
- **Registered as:** the `lunaroute` arm of `ProbeFactory.ProbeFor`.

### 3.2 Connection model discovery service (domain/provider)
- **Job:** produce the per-connection discovery view.
- **Contract:** input = connection name (admin identity required, same rule as
  `ConnectionService`). Output = list of entries, each: model id, family (lab or
  `unresolved`), task kinds, compatible backends, context limit, output limit,
  supported efforts per task/backend, independent reasoning support and known setting conflicts — each
  capability field explicitly `known(value)` or `unknown`; plus a top-level note that
  availability does not imply
  compatibility. Return independent support/unsupported/unknown facts rather
  than treating an observed supported backend as a complete allowlist. Include
  evidence references for known facts. Errors are the phase-1 sentinels.
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
  explicitly; LunaRoute profiles name their agent backend and use pinned
  selection. Do not recommend or automatically select a model.
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
- **Implementation seam:** extend `adapter/openrouter/provider.go:Config` with
  an optional structured error-classification hook (status, bounded body,
  transport error, operation); inject the LunaRoute classifier in its factory
  arm. Apply it to `Chat` and `StreamChat`, including immediate and channel
  errors, body-read/decode failures and structured SSE error frames. Verified codes must be
  decoded from recorded operation-specific schemas and supplied to the classifier; a permanently
  empty Code input is inert wiring. The existing `statusError` only returns `ErrUpstreamStatus`;
  parsing
  its formatted error string is not an implementation. Redact echoed keys
  before snippets/logging; discard incomplete capped diagnostic bodies so partial credentials
  cannot survive an early truncation; avoid dumping arbitrary response bodies.
- Wrap actual chat invocations with the phase-1 pure launch check and validate
  request-level settings such as `MaxTokens` against known output limits.
  `buildBody` currently maps/floors effort through `ChatReasoningEffort`; for
  LunaRoute reject unsupported or lossy mappings and emit the validated setting
  exactly. Never send a request that knowingly exceeds a limit.
- **No new chat adapter:** reuse the existing request/response implementation.
  Non-LunaRoute hooks/behaviour remain unchanged. A pinned response with a
  different model remains an execution-evidence error. Under the LR-03 decision, a
  reviewed alias (`alias:` target) passes only when the reported model is in the
  reviewed vendor family; the check is never disabled to make it pass.

### 3.5 Agent post-failure diagnosis wrapper (adapter/provider)
- **Job:** turn opaque agent failures on LunaRoute into classified errors.
- **Contract:** decorates the `engine.AgentProvider` returned by
  `AgentProviderFor` when the binding's canonical provider is `lunaroute`.
  Before dispatch, run the phase-1 launch compatibility check; a known conflict
  returns directly without a probe or launch. Success is a pure pass-through
  (zero extra calls). After an opaque run failure, perform at most one bounded
  LunaRoute probe using the same binding. Report the probe's current observation
  separately: credentials rejected now, model missing now, service unavailable
  now, or reachable/model listed with cause unknown. A later probe is not proof
  of the original inference failure's cause; retain that uncertainty. Prefer
  verified structured inference errors when available and do not probe on local
  validation/reveal/config failures or caller cancellation.
- Preserve the original error identity (`errors.Is/As`) and only safe diagnostics;
  never promise a raw stderr snippet. Current adapters may sanitize away stderr
  on pinned runs, and `engine.AgentProvider` has no stderr result field. Adapters
  must redact at their own log/error sinks before the wrapper sees the error.
  A failing diagnosis never masks the original error; cancellation is preserved.
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
| Connection name | discovery service | Author-supplied tool argument. New `connection_models` handler must parse it using the same name-input pattern as `adapter/mcp/tools_connection.go:connectionGetHandler`; it is not a catalog-derived name. |
| Connection metadata (provider, base URL) | discovery service | `domain/provider/connection.go:ConnectionStore.Get` — production instance `adapter/sqlite.ConnectionRepo`, already on MCP deps as `adapter/mcp/deps.go:Deps.Connections` |
| Key for listing | LunaRoute probe | `domain/provider/connection.go:ConnectionStore.Reveal`, implemented by `adapter/sqlite/connection_repo.go:ConnectionRepo.Reveal`; phase-1 guard is applied inside the probe as well as by discovery binding construction. |
| Probe selector | discovery service, agent wrapper | `adapter/probe/factory.go:NewProbeFactory` — **today constructed only in `cmd/arbitron/connection.go` (CLI)**; must be constructed in the serve composition root (deferred item) |
| Available model ids | discovery service, agent wrapper | LunaRoute `GET /v1/models` via the new probe arm. **LR-04 decided 2026-11-10 ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** parse `{"data":[{"id":…}]}` reading only `data[].id`. Send Bearer only, with no `anthropic-version` or `X-LunaRoute-Client`. Error bodies are accepted bare or wrapped (`{"type":"error","error":{…}}`). The documented code table in `lunaroute_classify.go:lunaRouteVerifiedCodes` maps `INVALID_API_KEY`, `CONCURRENT_REQUEST_LIMIT_EXCEEDED`, the four service-failure codes, and `BYOK_SPEND_CAP_REACHED`/`MODEL_UNPRICED` → new non-retryable `ErrLunaRouteRequestRefused`. A body code beats the status mapping. There is no `MODEL_NOT_FOUND`/`*_busy`; availability comes from the re-list. P2-T8 confirms the `data` wrapper. |
| Family per model | discovery service | Existing prefix table `domain/modelidentity/identity.go:families`, extended by phase 1. **LR-03 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** reviewed ids and the D8-style alias exception; never inferred from listing membership. |
| Task kinds / backends / limits / efforts | discovery service | phase-1 capability table (`capabilities.default.yaml` → `LoadCapabilityRules`). **LR-02 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** doc-sourced static facts for the release set; task/backend support only from completed live runs; unknowns stay explicit. |
| HTTP client, timeout, response cap | probe, chat route | `cmd/arbitron/main.go:newOutboundHTTPClient` → factory `Deps.HTTPClient`; limits from `domain/config/config.go:LLMConfig` through serve wiring and `adapter/probe/factory.go:Deps`. New LunaRoute client clone disables redirects; the existing client does not. |
| Chat binding (model, base URL, reveal) | chat route | `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve`, with run model applied by `domain/provider/pinning.go:PinningResolver.Resolve`; fields defined in `domain/provider/provider.go:ResolvedBinding`. |
| Chat request settings / prompts | chat preflight and body | `domain/engine/ports.go:ChatRequest`, constructed by `domain/engine/executor_llm.go:LLMCallExecutor.Execute`; node settings originate in `domain/workflow/workflow.go:NodeConfig`. |
| Original agent failure | diagnosis wrapper | `domain/engine/ports.go:AgentProvider.RunAgent` error; concrete producers are each backend's `provider.go:Provider.RunAgent` (Claude: `agent_adapter.go:AgentAdapter.RunAgent`). There is no separate stderr port. |
| Caller identity / cancellation | admin gate, bounded diagnosis | Authenticated context from `adapter/http/auth_middleware.go:AuthMiddleware.Handler`, using `domain/identity/identity.go:WithIdentity/FromContext`; service gate is `domain/provider/authz.go:requireAdmin`. Context also flows through `AgentProvider.RunAgent`. |
| Hosted endpoint / auth contract | probe, chat | **LR-01 decided ([decisions](2026-11-10-lunaroute-provider-decisions.md)):** docs-verified root and `/v1` paths, with `Authorization: Bearer lr_…` on probe and chat. Phase-1 helper `lunaroute_endpoint.go` (P1-T1) is the producer. |

### Deferred to plans
- **Composition-root wiring (serve path):** construct one `ProbeFactory` with the
  shared outbound HTTP client and inject it into (a) the discovery service and
  (b) the `Factory` deps for the agent wrapper; construct the discovery service
  and add it to `adapter/mcp/deps.go:Deps`; register `connection_models` in
  `adapter/mcp/register.go`. A root wiring test must prove the tool is reachable
  and non-nil-backed in the production server (not only in tests).
- **Factory deps:** add the probe selector to `adapter/provider` `Deps`; the
  agent wrapper is applied inside `AgentProviderFor` only for `lunaroute`.
- **Runtime policy:** wrap real chat/agent calls with the phase-1 compatibility
  rule; leave model-empty inventory construction lazy. Reject unsupported
  LunaRoute interactive agent routes in `InteractiveStreamSelector` before a
  subscription adapter is selected.
- **Chat error seam:** add the optional classification hook and LunaRoute
  redirect policy; defaults preserve other providers.
- **Discovery evidence:** parser fixtures follow the decided LR-01/LR-04 contracts.
  P2-T8 confirms them live. LR-02 static facts and LR-03 spellings come from P1-T11
  before acceptance profile creation.
- **Error mapping tables:** MCP connection/profile error results for every
  phase-1 sentinel.
- **Docs:** MCP authoring guidance (`adapter/mcp/authoring_guidance.go`) mentions
  `connection_models`, explicit model/backend choice, unknown capability
  semantics, and actual-vendor family checks for doer/judge pairings.

## 6. Integration seams

- `ProbeSelector` — new arm plus hosted/key guards in the probe; CLI
  `connection validate` gains support without assuming its binding is guarded.
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

For every change, write a failing test first, implement the minimum to pass,
then refactor while green. Use recorded protocol fixtures only after verification;
fixture success never flips a decided external contract to `verified`.

- **Happy-path:** `connection_models` against a transport-injected fake gateway returns
  every listed id with family and table-backed fields; a LunaRoute chat call
  against a fake `/v1/chat/completions` returns content.
- **Success:** probe sends Bearer `lr_…` to `<root>/v1/models` (exact path, no
  doubled `/v1`); chat posts to `<root>/v1/chat/completions` with no OpenRouter
  headers; CLI `connection validate` lists LunaRoute models; agent wrapper is a
  pure pass-through on success (fake probe asserts zero calls).
- **Failure:** 401 → credentials rejected; verified unknown-model code / missing
  model in successful listing → model unavailable; bare listing 404 → cause unknown;
  429 → busy; 503 → service failure; teapot/unknown status → cause unknown;
  non-admin discovery → forbidden; unknown connection → not found.
- **Error:** transport error / TLS failure → service failure; oversized body →
  bounded-read error; guarded reveal rejects non-`lr_` stored key before any
  request is sent (fake server asserts no request); agent wrapper with a failing
  diagnosis preserves original error identity and marks its later observation
  uncertain; cancellation makes no diagnosis call; malformed listing is a
  protocol/service failure, never a successful empty catalog.
- **Edge-case:** empty model list (reachable, zero models); model id present in
  listing but absent from table (all fields `unknown`); table entry for a model
  absent from listing (not shown — availability rules); unresolved-family model
  shown as `unresolved`; provider with `ModelsUnsupported`; echoed key redacted
  before truncation; redirect rejected; request MaxTokens above a known limit
  rejected before I/O; key never appears in results/logs/errors (log-capture assertion).
- **Tier 2 (live, gated by `ARBITRON_LUNAROUTE_E2E_KEY`, skipped otherwise):**
  through the real MCP tools — create connection, `connection_models`, create a
  chat profile, run a representative chat task to completion; invalid key and
  unknown model produce the distinct errors; confirms the `/v1/models` envelope
  and error bodies against the LR-04 decision and records observed chat support only.
  LR-02 static facts and LR-03 spellings come from P1-T11's doc source notes;
  discovery alone does not supply them.
  Missing credentials skip the test, leaving release acceptance outstanding.
