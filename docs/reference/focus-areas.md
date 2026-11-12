# Focus areas — LunaRoute provider

Each area is one **behaviour**, traced from its entry to its sink across every layer. Review 1 uses the wiring lens and
Review 2 the failure lens. Rows run highest risk first. Scope, the R7 release boundary and the test commands are in
`RALPH.md`.

The `harden:` ids are the mutation-run survivors from `.arbitron/pipeline/lunaroute-provider.survivors.json`. That file
lists each surviving mutant by file and line. Each area cites the ids it owns as proven test gaps to start from.

## Single-System Reviews

| # | Focus Area | Risk | Review 1 Status | Review 2 Status |
|---|---|---|---|---|
| 1 | A LunaRoute connection is written only with its own lr_ key and the hosted endpoint, and its key is never paired with a foreign provider | high | [ ] | [ ] |
| 2 | A LunaRoute agent route is re-checked on the actual call (pins, node backend overrides, R7 enablement) and refused before any reveal or launch | high | [ ] | [ ] |
| 3 | A LunaRoute profile saves only as a pinned, explicitly backed (codex or pi), table-compatible selection, and other providers keep the shipped behaviour | high | [ ] | [ ] |
| 4 | A LunaRoute model is accepted only under a reviewed fixed id whose real vendor is known; Claude-shaped aliases and unresolved families are refused wherever family or executed model is judged | high | [ ] | [ ] |
| 5 | A LunaRoute codex agent runs keyed against the Responses endpoint in a private, always-removed CODEX_HOME and never touches the operator's codex login | high | [ ] | [ ] |
| 6 | A LunaRoute pi agent runs from a private PI_CODING_AGENT_DIR whose models.json carries table-sourced limits and an env-referenced key | high | [ ] | [ ] |
| 7 | A LunaRoute chat node completes through /v1/chat/completions with exactly the validated settings, or fails with one classified, secret-free gateway outcome | high | [ ] | [ ] |
| 8 | connection_models (and CLI connection validate) lists a LunaRoute connection's live models with curated known/unknown facts, admin-only and key-free | med | [ ] | [ ] |
| 9 | An opaque LunaRoute agent failure is reported with at most one bounded gateway observation, keeping the original error and never retrying | med | [ ] | [ ] |
| 10 | Release readiness is claimed only for LR-01–04, LR-07 and LR-08 with existing evidence, and every author-facing surface lists chat, codex and pi only | low | [ ] | [ ] |

---

### Area 1 — A LunaRoute connection is written only with its own lr_ key and the hosted endpoint, and its key is never paired with a foreign provider

- **Promise:** `connection_create`/`connection_update` accept a LunaRoute connection only with a non-empty `lr_` key and the
  hosted root, normalized to `https://gw.lunaroute.com` before persistence. Switching to lunaroute without a new key is
  refused, including under a concurrent provider switch. Credentials from other providers are never forwarded, and no key
  is echoed.
- **Entry:** `adapter/mcp/tools_connection.go:connectionCreateHandler` and `connectionUpdateHandler`
- **Sink:** `adapter/sqlite/connection_repo.go:ConnectionRepo.Create` and `ConnectionRepo.Update`, which persist through
  `generated.UpdateConnectionMetadataIfProvider`, `UpdateConnectionMetadata` or `UpdateConnectionWithKey`; refusals go to
  `adapter/mcp/tools_connection.go:connErrorResult`.
- **Files:**
  - Changed: `domain/provider/lunaroute_connection.go`, `domain/provider/lunaroute_endpoint.go`,
    `domain/provider/lunaroute_errors.go`, `domain/provider/canonical.go`, `adapter/sqlite/connection_repo.go`,
    `adapter/sqlite/generated/connections.sql.go` (and its query source under `adapter/sqlite/`),
    `adapter/mcp/tools_connection.go`
  - Crossed: `domain/provider/connection_service.go` (`Create`, `UpdatePatch`), `domain/provider/connection.go`,
    `domain/provider/url_policy.go`, `domain/provider/authz.go`, `cmd/arbitron/main.go` (the `connStore` construction)
- **Folded in:**
  - `domain/provider/canonical.go:CanonicalLunaRoute`: one spelling, no aliases. Check that `Canonicalize("  LunaRoute ")`
    yields `lunaroute`.
  - Tool descriptions in `adapter/mcp/tools_connection.go`: `provider`, `base_url` and `api_key` mention lunaroute and the
    hosted root. Check that the text matches the rule actually enforced.
  - The LunaRoute echo in the `connection_get`/list path (`tools_connection.go`, around the `IsLunaRoute(providerID)`
    branch). Check that it shows the canonical provider and root and never shows the key.
- **Review 1 — wiring lens:**
  1. `ConnectionRepo.Create` calls `provider.PrepareLunaRouteCreate` before `Seal`. `ConnectionRepo.Update` calls
     `provider.PrepareLunaRouteUpdate` after `GetConnectionMeta`. The production `connStore` built in `cmd/arbitron/main.go`
     is this repo. Demonstrated by `cmd/arbitron/lunaroute_connection_wiring_test.go:TestLunaRouteConnectionWriteWiring`.
  2. `storedProvider` is fed by the real `GetConnectionMetaRow.Provider`, not `""`. Grep `PrepareLunaRouteUpdate` callers.
     Covered by `TestConnectionRepo_SwitchToLunaRouteRequiresNewKey`.
  3. For every accepted base-URL spelling, the persisted row (read back through a fresh `Get`, not the in-memory struct) has
     `Provider=lunaroute` and `BaseURL=LunaRouteHostedRoot`. See
     `adapter/sqlite/connection_repo_lunaroute_test.go:TestConnectionRepo_LunaRouteCreateNormalizesAndSeals`.
  4. Every write sentinel (`ErrLunaRouteKeyPrefix`, `ErrLunaRouteKeyRequired`, `ErrLunaRouteEndpoint`) reaches the MCP
     caller as actionable text through `connErrorResult`. Covered by `TestConnErrorResult_LunaRouteSentinels`.
  5. Grep for every production writer of the connections table (`CreateConnection`, `UpdateConnectionWithKey`,
     `UpdateConnectionMetadata`) outside `adapter/sqlite`, including CLI subcommands and seeds. Each must go through
     `ConnectionRepo`, or the rule is bypassable.
- **Review 2 — failure lens:**
  1. These keys must each be refused with no row change and no key text in the error: `lr_` alone, `lr_` followed by a
     space or control character, `sk-…`, `sk-or-…`, empty, and a nil key on a switch to lunaroute. Covered by
     `TestConnectionLunaRoutePersistenceErrorsDoNotDiscloseKeys`.
  2. Concurrent switch: if the provider changes between the meta read and the write, `UpdateConnectionMetadataIfProvider`
     updates 0 rows, so the update fails through `missingOrChanged`. Covered by
     `TestConnectionRepo_LunaRouteMetadataUpdateIsGuardedAgainstConcurrentSwitch`, using the `afterMetaRead` hook.
  3. Switching a lunaroute connection **away** to another provider with `api_key` omitted keeps the sealed `lr_` key
     (`UpdateConnectionMetadata` path), so the next call sends it to the new host. Open gap G1: fix it or escalate it, and
     add the test that goes red today.
  4. Base-URL edges: `https://GW.LunaRoute.com/v1/` normalizes. These are refused: `:443`, userinfo, an escaped `%2F` path,
     `?q`, `#f`, `http://`, a different host, and `/v2`. Covered by `domain/provider/lunaroute_endpoint_test.go`.
  5. `harden:domain/provider/lunaroute_connection.go`: a mutant at line 18 (the whitespace/control scan) survives, so add a
     test that distinguishes a valid key from one with an embedded space. `harden:adapter/sqlite/generated/connections.sql.go`:
     a negation at line 204 survives. Check whether a query error on that path goes unnoticed.
- **Seed evidence:**
  - `adapter/sqlite/connection_repo.go:Create` calls `provider.PrepareLunaRouteCreate(c, apiKey)`.
  - `adapter/sqlite/connection_repo.go:Update` calls `provider.PrepareLunaRouteUpdate(c, apiKey, stored.Provider)`, and on
    the nil-key path calls `UpdateConnectionMetadataIfProvider` (with `Provider_2: stored.Provider`) for lunaroute and plain
    `UpdateConnectionMetadata` otherwise.
  - `domain/provider/lunaroute_connection.go:PrepareLunaRouteUpdate` returns early when the *new* provider is not
    lunaroute. That is the root of G1.
  - `adapter/mcp/tools_connection.go:connectionUpdateHandler` requires `provider` and treats `api_key` as optional.

### Area 2 — A LunaRoute agent route is re-checked on the actual call (pins, node backend overrides, R7 enablement) and refused before any reveal or launch

- **Promise:** The pure LunaRoute rule runs on the *effective* binding: after run pins, after node backend overrides, and
  after any change to the connection or capability table since the profile was saved.
  - Claude-code and opencode (R7) are refused.
  - So are backends chosen only by deployment defaults, and LunaRoute interactive sessions.
  - Every refusal happens before a credential is revealed or a subprocess starts, so the run never reaches a subscription
    adapter.
- **Entry:** `domain/engine/executor_agent.go:AgentCallExecutor.Execute`, plus workflow trigger →
  `adapter/provider/model_snapshot.go:ModelSnapshotter` and interactive start →
  `adapter/provider/interactive_stream_selector.go`
- **Sink:** a refusal from `adapter/provider/factory.go:Factory.AgentProviderFor`, from the preflight in
  `adapter/provider/lunaroute_agent.go:lunaRouteAgent.RunAgent`, from the snapshot, or from the interactive selector. Every
  one fires before the inner `RunAgent`.
- **Files:**
  - Changed: `adapter/provider/factory.go` (`AgentProviderFor`), `adapter/provider/lunaroute_agent.go` (`wrapLunaRouteAgent`,
    `RunAgent` preflight only), `adapter/provider/dynamic.go` (`DynamicProfileResolver.Resolve`),
    `adapter/provider/path_inventory.go` (`routeBinding`), `adapter/provider/pinned_route.go` (`validatePinnedRoute`),
    `adapter/provider/model_snapshot.go`, `adapter/provider/interactive_stream_selector.go`,
    `domain/engine/executor_agent.go`, `domain/provider/lunaroute_validate.go` (`CheckLunaRouteLaunch`,
    `checkLunaRouteBinding`, `CheckLunaRouteAdapterLaunch`, `lunaRouteEnabledBackends`),
    `domain/provider/lunaroute_reveal.go`, `domain/provider/provider.go` (`ResolvedBinding.BackendExplicit`)
  - Crossed: `domain/provider/pinning.go:PinningResolver.Resolve`, `cmd/arbitron/main.go` (`providerFactory`,
    `NewModelSnapshotter`)
- **Folded in:**
  - The R7 map `lunaRouteEnabledBackends`, which holds codex and pi only.
  - The `routeBinding` codex reveal narrowing (`path_inventory.go`): reveal stays suppressed for subscription codex and is
    enabled for LunaRoute codex.
  - The `validatePinnedRoute` codex carve-out narrowed to non-LunaRoute.
  - The dormant claude-code and opencode emitters, which must stay unreachable from production. Their survivors are listed
    under `Won't Fix` in `gaps-identified.md`, and are not refined while R7 holds: `harden:adapter/claudecli/streamjson.go`,
    `harden:adapter/claudecli/stream_agent_adapter.go`, `harden:adapter/opencode/config.go` and
    `harden:adapter/opencode/model_probe.go`.
- **Review 1 — wiring lens:**
  1. `BackendExplicit` must be set from `Profile.Backend` at all three production sites: both branches of
     `dynamic.go:Resolve` (around lines 89 and 124) and `path_inventory.go:routeBinding`. It must be ORed with the node
     override at `executor_agent.go:Execute` (around line 84) before the deployment default applies, and it must survive
     `PinningResolver.Resolve`. Inverting it at `dynamic.go:89/124` currently turns no test red
     (`harden:adapter/provider/dynamic.go`; gap G2).
  2. `AgentProviderFor` refuses lunaroute + claude-code/opencode *before* `GuardLunaRouteReveal` and `agentProviderFor`.
     Covered by `TestFactory_AgentProviderFor_DeferredLunaRouteBackends` and
     `cmd/arbitron/lunaroute_backend_provenance_wiring_test.go:TestLunaRouteDeferredNodeOverridesRefused`.
  3. Every LunaRoute agent provider is wrapped (`factory.go`, `wrapLunaRouteAgent`). The preflight runs
     `CheckLunaRouteLaunch` with the root `capabilityRules` from `Deps.CapabilityRules`, never a nil table, before
     `inner.RunAgent`. Covered by `TestLunaRouteAgent_LaunchRefusalBeforeLaunch`.
  4. `cmd/arbitron/main.go` constructs `NewModelSnapshotter(..., capabilityRules)`, and the snapshotter refuses a LunaRoute
     pin that the table now rejects. Covered by `TestSnapshotLunaRouteCapabilityPolicy`.
  5. The interactive selector refuses a LunaRoute agent before backend defaulting. Covered by
     `cmd/arbitron/lunaroute_interactive_wiring_test.go:TestLunaRouteInteractiveAgentRefusedAtRoot`.
  6. Grep production constructors of `codex.New`, `pi.New`, `claudecli.NewAgentAdapterForBinding` and `opencode.New`. Every
     one that can receive a LunaRoute binding must be reached only through `AgentProviderFor`; model refresh and usability
     probes must not bypass it.
- **Review 2 — failure lens:**
  1. A profile saved on pi, run with a node override to claude-code or to a whitespace backend, must be refused. Covered by
     `TestLunaRouteDeferredNodeOverridesRefused` and `TestLunaRouteExplicitNodeOverrideChecked`.
  2. A legacy LunaRoute profile with no backend, relying on a deployment default of codex, must be refused. Covered by
     `TestLunaRouteLegacyDefaultBackendRefused`.
  3. Narrowing the capability table after save (an override file drops pi limits or efforts) must refuse the launch. The run
     must never proceed silently.
  4. If the connection's `base_url` is tampered in the DB, or its provider is switched after save, the launch must be
     refused through `NormalizeLunaRouteBaseURL` in `checkLunaRouteBinding` before reveal. Assert that the reveal counter
     stays at 0.
  5. Ordering: every refusal fires before `RevealKey` and before any runner call. A test must count both.
  6. A nil capability table must produce `ErrSelectionPortsMissing` and never a pass.
- **Seed evidence:**
  - `adapter/provider/factory.go:AgentProviderFor` checks
    `slices.Contains(LunaRouteBackends, backend) && !LunaRouteBackendEnabled(backend)`, then calls `GuardLunaRouteReveal`,
    then `wrapLunaRouteAgent(inner, b, f.deps.Probes, f.deps.CapabilityRules)`.
  - `domain/engine/executor_agent.go:84` sets
    `binding.BackendExplicit = binding.BackendExplicit || strings.TrimSpace(node.Config.Backend) != ""`.
  - `adapter/provider/path_inventory.go:596` disables reveal only when the backend is codex and the provider is not
    LunaRoute.
  - `adapter/provider/pinned_route.go:30` applies the subscription carve-out only to non-LunaRoute codex.
  - `cmd/arbitron/main.go:1436` calls `NewModelSnapshotter(..., capabilityRules)`.

### Area 3 — A LunaRoute profile saves only as a pinned, explicitly backed (codex or pi), table-compatible selection, and other providers keep the shipped behaviour

- **Promise:** `profile_create`/`profile_update` on a LunaRoute connection refuse with a typed, actionable `LunaRouteRefusal`
  for any of these:
  - following mode;
  - a missing backend, or claude-code/opencode;
  - an unreviewed id;
  - a table-incompatible task or backend;
  - a non-default context;
  - an effort without exact support;
  - unknown limits for pi;
  - mixed routes with different axes.

  Requested axes are echoed exactly, without native flooring. Non-LunaRoute profiles keep the #84 behaviour unchanged (R5).
- **Entry:** `adapter/mcp/tools_profile.go:profileCreateHandler` and `profileUpdateHandler`
- **Sink:** `domain/provider/profile_service.go:ProfileService.write`, which either persists or refuses; refusal text goes
  through the profile error result in `adapter/mcp/tools_profile.go` (the `LunaRouteRefusal` branch).
- **Files:**
  - Changed: `adapter/mcp/tools_profile.go`, `adapter/mcp/lunaroute_backend_hint.go`, `adapter/mcp/authoring_guidance.go`,
    `domain/provider/profile_service.go` (`revalidatePrepared` and the LunaRoute axes branch around lines 286–320),
    `domain/provider/selection.go` (`ValidateSelection`, `ValidatePinRoutes`, `checkRouteVersions`,
    `profileUsesLunaRoute`, `checkLunaRouteProfile`), `domain/provider/lunaroute_validate.go` (`CheckLunaRouteRoute`,
    `checkLunaRouteRoute`, `lunaRouteExactEffort`, `lunaRouteNeedsLimits`), `domain/provider/lunaroute_capability.go`,
    `domain/provider/capability.go`, `domain/provider/lunaroute_errors.go`, `adapter/config/capabilities.go`,
    `adapter/config/capabilities.default.yaml` (`lunaroute_models`)
  - Crossed: `adapter/provider/path_inventory.go` (`ExecutionPathInventory.VersionRequests`),
    `domain/modelcatalog/candidate_source.go`, `cmd/arbitron/main.go` (`LoadCapabilityRules` around line 810,
    `NewProfileService` around 994, `SetModelSelection` around 1431)
- **Folded in:**
  - The capability YAML loader (`capabilities.go:LoadCapabilityRules` → `WithLunaRouteModels`). Check that unknown stays
    distinct from known-empty, that unknown backends, non-positive limits and unknown request settings are rejected at
    load, that an override file replaces per-model entries, and that a missing override falls back to the embedded file.
  - The `ValidatePinRoutes` codex carve-out narrowed to non-LunaRoute (`selection.go:172`).
  - The following-mode refusal (`ErrLunaRoutePinnedOnly`).
  - The `LunaRouteBackendHint` and authoring-guidance text: it must say codex or pi only, pinned only, and explicit backend.
    The ids `harden:adapter/mcp/lunaroute_backend_hint.go` survive on string concatenation; either pin the text in a test
    or record them as equivalent.
- **Review 1 — wiring lens:**
  1. The `capabilityRules` from `LoadCapabilityRules` is the same object passed to `NewProfileService`, `Factory`,
     `NewModelDiscoveryService` and `NewModelSnapshotter`. Covered by
     `cmd/arbitron/lunaroute_capabilities_wiring_test.go:TestLunaRouteCapabilityTableWiring` and
     `TestLunaRouteEmbeddedOutputLimitWiring`.
  2. `ValidateSelection` and `ValidatePinRoutes` receive non-nil caps from `ProfileService` on create, update and
     `revalidatePrepared`. Grep every caller of `ValidateSelection(` and `ValidatePinRoutes(`.
  3. `profileUsesLunaRoute` reads the real routing provider from the candidate source that `SetModelSelection` injects. It
     must not use a test stub.
  4. A pinned `glm-5.3` pi profile saves and echoes its requested effort without flooring. Covered by
     `cmd/arbitron/lunaroute_profile_wiring_test.go:TestLunaRouteProfileSaveWiring` and
     `lunaroute_agent_wiring_test.go:TestLunaRouteAgentProfileSaveWiring`.
  5. A LunaRoute codex save keeps its route provider through `ValidatePinRoutes`. Covered by
     `domain/provider/lunaroute_codex_routes_test.go`.
  6. Seeded survivors: `harden:adapter/config/capabilities.go` (len guards at lines 186, 200, 220),
     `harden:domain/provider/profile_service.go` (line 316), `harden:domain/provider/lunaroute_validate.go` (line 135, the
     `MaxOutputTokens > 0` boundary) and `harden:domain/provider/lunaroute_capability.go` (line 109, sort order). Gap G7.
- **Review 2 — failure lens:**
  1. Following mode with an empty model must refuse with `ErrLunaRoutePinnedOnly` *before* catalog selection, with no
     missing-port error. Covered by `TestLunaRouteProfileScopeWiring`.
  2. These backend cases must hold: no backend gives `ErrLunaRouteBackendRequired`; claude-code or opencode gives
     `ErrLunaRouteBackendUnsupported` (`TestLunaRouteDeferredBackendsSaveWiring`); codex and pi are accepted.
  3. These axes must be refused: a non-default context; effort `none`/`max` on codex; any effort missing from the table for
     that axis; `glm-5.3` with effort `medium` on pi (absent from its levels).
  4. Pi with unknown limits or reasoning must refuse with `ErrLunaRouteLimitsUnknown`. A chat model absent from the table
     must be allowed as unknown, with no false refusal.
  5. Mixed LunaRoute and other routes whose effective axes differ must be refused (`profile_service.go:316`).
  6. R5: every LunaRoute branch must be gated by `profileUsesLunaRoute`. Non-LunaRoute profile tests in `domain/provider`
     and `adapter/mcp` must still pass with no LunaRoute-specific skips (grep for `t.Skip` added near LunaRoute).
- **Seed evidence:**
  - `adapter/config/capabilities.go:95` returns
    `provider.NewCapabilityRules(base.backends).WithLunaRouteModels(base.lunaroute)`.
  - `adapter/config/capabilities.go:245` allows only the `temperature` and `output_schema` request settings.
  - `cmd/arbitron/main.go:994` calls `provider.NewProfileService(profileRepo, capabilityRules, profileReloader)`.
  - `domain/provider/selection.go:239` makes `checkLunaRouteProfile` refuse non-pinned profiles and blank backends.
  - `capabilities.default.yaml` gives task/backend `supported` facts only to `glm-5.3` (codex, pi). `glm-5.3-flash` and
    `deepseek-4.1-flash` have limits, reasoning and pi efforts, but unknown task and backend facts.

### Area 4 — A LunaRoute model is accepted only under a reviewed fixed id whose real vendor is known; Claude-shaped aliases and unresolved families are refused wherever family or executed model is judged

- **Promise:** Family is the actual vendor (`glm-*` → zhipuai, `deepseek-*` → deepseek).
  - `claude-*-lrN` aliases never classify as anthropic and are refused (R3).
  - An unresolved family cannot pass save or launch.
  - The executed model reported by a run must equal the reviewed fixed id.
  - The sample doer/judge same-vendor safeguard keeps working through the domain classifier.
- **Entry:** `domain/modelidentity/family.go:ClassifyFamily` and the lunaroute case of
  `domain/modelidentity/version.go:explicitVersion`/`lunaRouteFixed`. These are reached from
  `domain/provider/lunaroute_validate.go:checkLunaRouteIdentity`, `domain/provider/model_discovery.go:Discover`,
  `version.go:LunaRouteExecutedModelAccepted` and `samples/family_test.go:judgePairingProblems`.
- **Sink:** `ErrLunaRouteModelSpelling` / `ErrLunaRouteFamilyUnresolved` refusals; the `family` field of the discovery
  view; execution-evidence failures in chat (`openrouter` `AcceptExecutedModel`), codex (`adapter/codex/rollout.go`) and pi
  (`adapter/pi/model_probe.go`); and sample pairing failures.
- **Files:**
  - Changed: `domain/modelidentity/family.go`, `domain/modelidentity/version.go` (`lunaRouteReviewedIDs`,
    `lunaRouteClaudeAlias`, `LunaRouteExecutedModelAccepted`), `domain/modelidentity/identity.go` (`families`),
    `samples/family_test.go`, `samples/sample_harness_test.go`
  - Crossed: `domain/provider/lunaroute_validate.go` (`checkLunaRouteIdentity`), `domain/provider/model_discovery.go`,
    `adapter/provider/lunaroute_chat.go`, `adapter/codex/rollout.go`, `adapter/pi/model_probe.go`,
    `docs/evidence/lunaroute-provider/model-identities.md`
- **Folded in:** the removal of the private sample `modelFamily` in favour of the domain classifier, and sample fixtures
  renamed to canonical lab names.
- **Review 1 — wiring lens:**
  1. `checkLunaRouteIdentity` is called with the real `modelidentity.NewVersionResolver()` and `ClassifyFamily`
     (`lunaroute_validate.go:102`). No production caller can inject another resolver.
  2. `lunaRouteReviewedIDs` must contain exactly `glm-5.3`, `glm-5.3-flash` and `deepseek-4.1-flash` (R3), with vendors
     `zhipuai`/`deepseek`. It must have no `claude-*-lr*` entry and no `deepseek-4.1-flash-1017`.
  3. `Discover` sets `Family` from `ClassifyFamily(route, id)` (`model_discovery.go:159`), never from listing membership.
  4. Executed-model acceptance is wired at `lunaroute_chat.go` (`AcceptExecutedModel`), `codex/rollout.go:231` and
     `pi/model_probe.go:47`. Fixed ids require exact equality. Covered by
     `cmd/arbitron/lunaroute_chat_wiring_test.go:TestLunaRouteChatWiringFixedEvidence` and
     `lunaroute_codex_wiring_test.go:TestLunaRouteCodexWiringRejectsRolloutDrift`.
  5. `samples/family_test.go` uses `modelidentity.ClassifyFamily` (lines 45 and 88), and grep finds no remaining private
     `modelFamily`.
- **Review 2 — failure lens:**
  1. These must classify unresolved and be refused at save: `claude-sonnet-5-lr1`, `claude-opus-lr29`,
     `CLAUDE-HAIKU-LR37` and `lunaroute/claude-opus-lr29`. Covered by
     `cmd/arbitron/lunaroute_fixed_identity_wiring_test.go:TestLunaRouteFixedIdentitySaveRefusals`.
  2. A contradictory vendor prefix (`openai/glm-5.3`, or a mismatched bedrock qualifier) must classify unresolved. The
     surviving negations `harden:domain/modelidentity/family.go` at lines 89, 98 and 110 mean prefix consistency is
     under-tested (gap G8).
  3. These must be refused: `GLM-5.3`, ` glm-5.3 `, an unreviewed `glm-5.3-background`, `latest`, and an id containing `/`.
     `harden:domain/modelidentity/version.go` (line 192) survives on the lunaroute case.
  4. A selected `glm-5.3` that reports `glm-5.3-flash`, or reports nothing, must fail the run. It must never pass.
  5. A same-vendor doer/judge pair, such as `glm-5.3` via lunaroute judged by a direct zhipuai model, must be reported by
     `judgePairingProblems`.
- **Seed evidence:**
  - `domain/modelidentity/version.go:87–94` lists the three reviewed ids, each with an evidence anchor.
  - `version.go:98` makes `lunaRouteFixed` require `!lunaRouteClaudeAlias(model)`.
  - `family.go:ClassifyFamily` returns `FamilyUnresolved` for `lunaRouteClaudeAlias` and for `lunaRouteAliasRE` matches.
  - `domain/provider/lunaroute_validate.go:102` makes the production call
    `checkLunaRouteIdentity(r.Model, modelidentity.NewVersionResolver(), modelidentity.ClassifyFamily)`.

### Area 5 — A LunaRoute codex agent runs keyed against the Responses endpoint in a private, always-removed CODEX_HOME and never touches the operator's codex login

- **Promise:** `codex exec` gets six `-c` overrides: `model_provider`, the four `model_providers.lunaroute.*` keys (name,
  `base_url = https://gw.lunaroute.com/v1`, `env_key = LUNAROUTE_API_KEY`, `wire_api = responses`), and `review_model=<id>`.
  - The guarded `lr_` key is in the child environment only.
  - `CODEX_HOME` is a per-run private 0700 directory, and the operator's `CODEX_HOME`, `OPENAI_API_KEY` and
    `OPENAI_BASE_URL` are not forwarded.
  - The rollout is verified before the home is removed on every exit path.
  - The reported model is persisted as `model_evidence`.
  - Subscription codex is unchanged.
- **Entry:** `domain/engine/executor_agent.go:AgentCallExecutor.Execute` → `adapter/provider/factory.go:Factory.AgentProviderFor`
  (codex arm) → `adapter/codex/provider.go:Provider.RunAgent`
- **Sink:** codex subprocess argv and env (from `adapter/codex/lunaroute.go:lunaRouteExecExtra` and
  `prepareLunaRouteLaunch`); `AgentResult.ModelEvidence` persisted under `model_evidence` at
  `domain/engine/executor_agent.go:128`; the private home removed.
- **Files:**
  - Changed: `adapter/codex/lunaroute.go`, `adapter/codex/provider.go`, `adapter/codex/rollout.go`,
    `adapter/codex/parse.go`, `adapter/provider/factory.go` (codex arm)
  - Crossed: `adapter/codex/args.go`, `adapter/codex/env.go`, `adapter/codex/version.go`, `adapter/procenv`
    (`CredentialEnv`, `RemoveVar`), `domain/provider/lunaroute_reveal.go`, `domain/provider/lunaroute_errors.go`
    (`LocalLunaRouteError`, `SanitizeLunaRouteError`), `domain/engine/executor_agent.go`, `domain/engine/ports.go`
    (`ModelEvidence`), `cmd/arbitron/main.go` (`CodexBinaryPath`)
- **Folded in:**
  - The memoized version probe runs without the key (`ensureLunaRouteCompatible`).
  - The codex backend-health login verdict is informational for LunaRoute runs (check against `LunaRouteBackendHint`).
  - A LunaRoute codex usability probe returns `ErrModelEvidence` (`provider.go:318`).
- **Review 1 — wiring lens:**
  1. The codex arm receives the guarded binding (`GuardLunaRouteReveal` in `AgentProviderFor`) and
     `Deps.CapabilityRules`. `Provider.RunAgent` enters LunaRoute mode only on `IsLunaRoute(provider)`.
  2. In the subprocess argv the five provider overrides and `review_model` appear exactly once, and the base derives from
     the stored root through `LunaRouteOpenAIBaseURL`, so `/v1` appears exactly once. Argv contains no key. Covered by
     `adapter/codex/lunaroute_test.go:TestLunaRouteCodex_HappyPath` and the root fake binary in
     `cmd/arbitron/lunaroute_codex_wiring_test.go:TestLunaRouteCodexWiring`.
  3. The child env contains `LUNAROUTE_API_KEY=lr_…` and `CODEX_HOME=<private dir>` and none of the operator's
     `CODEX_HOME`, `OPENAI_API_KEY` or `OPENAI_BASE_URL`. Assert on the fake runner's env, not on a helper's return value.
  4. The rollout verifier (`verifier.lunaRouteAfterTurn`) reads the private home before `launch.cleanup` and fills
     `ModelEvidence`. The executor persists `model_evidence`, asserted at the node conversation sink.
  5. Subscription codex is byte-identical to the shipped path. Covered by `TestLunaRouteCodex_SubscriptionUnchanged`.
- **Review 2 — failure lens:**
  1. A reveal error or a non-`lr_` stored key must produce no launch and leave no home behind. Covered by
     `TestLunaRouteCodex_CredentialFailures`.
  2. A `mkdir` or `chmod` failure must clean up and return a `LocalLunaRouteError` marker, which prevents a diagnosis probe.
     Covered by `TestLunaRouteCodexWiringLocalFailureNeverProbes`.
  3. On cancel or timeout mid-run, the deferred cleanup must remove the home and the subprocess must be torn down.
  4. Effort `none` or `max` must be refused before I/O. `xhigh` must be emitted verbatim. Covered by
     `TestLunaRouteCodex_ExactEffort`.
  5. Rollout drift must fail: a different model, a missing final turn, or an unsafe identity string.
     `harden:adapter/codex/lunaroute.go` (line 97, `safeLunaRouteIdentity`) survives.
  6. Concurrent runs must get distinct homes, and the version probe env must never contain `LUNAROUTE_API_KEY`.
  7. Every returned error, after `SanitizeLunaRouteError`, must be free of `lr_` material, including errors that come from
     codex stderr.
- **Seed evidence:**
  - `adapter/codex/lunaroute.go:prepareLunaRouteLaunchWithFS` removes `codexHomeVar`, `OPENAI_API_KEY` and
    `OPENAI_BASE_URL`, then appends `CODEX_HOME=<home>`.
  - `adapter/codex/provider.go:181` calls `prepareLunaRouteLaunch` and runs `defer launch.cleanup()`.
  - `provider.go:276` sets `evidence, err = verifier.lunaRouteAfterTurn(...)`.
  - `domain/engine/executor_agent.go:128` sets `conversation["model_evidence"] = res.ModelEvidence`.

### Area 6 — A LunaRoute pi agent runs from a private PI_CODING_AGENT_DIR whose models.json carries table-sourced limits and an env-referenced key

- **Promise:** A pi launch on LunaRoute writes a private 0700 directory containing a 0600 `models.json`. That file declares
  the `lunaroute` provider with:
  - `baseUrl = https://gw.lunaroute.com/v1`;
  - `api = openai-completions`;
  - `apiKey = "$LUNAROUTE_API_KEY"`, which references the key and never contains it;
  - `authHeader = true`;
  - the model's context window, max tokens and reasoning flag from the capability table.

  The launch selects `--model lunaroute/<id>` with the validated `--thinking` level. It refuses unknown metadata before any
  reveal, removes the directory on every path, and persists the reported model.
- **Entry:** `domain/engine/executor_agent.go:AgentCallExecutor.Execute` → `Factory.AgentProviderFor` (pi arm) →
  `adapter/pi/provider.go:Provider.RunAgent`
- **Sink:** the pi subprocess (argv plus env `PI_CODING_AGENT_DIR`/`LUNAROUTE_API_KEY`) and the `models.json` written by
  `adapter/pi/lunaroute.go:prepareLunaRoutePiLaunchWithFS`; `model_evidence` persisted at `executor_agent.go:128`.
- **Files:**
  - Changed: `adapter/pi/lunaroute.go`, `adapter/pi/provider.go`, `adapter/pi/env.go`, `adapter/pi/args.go`
    (`thinkingLevel`), `adapter/pi/model_probe.go`, `adapter/provider/factory.go` (pi arm)
  - Crossed: `domain/provider/lunaroute_capability.go`, `domain/provider/lunaroute_validate.go`
    (`CheckLunaRouteAdapterLaunch`), `domain/provider/lunaroute_reveal.go`, `adapter/procenv`,
    `domain/engine/executor_agent.go`, `cmd/arbitron/main.go` (`PiBinaryPath`, `CapabilityRules` into the factory)
- **Folded in:**
  - The pi usability probe refuses LunaRoute (`provider.go:301`).
  - The `localError` marker covers every local preparation failure.
- **Review 1 — wiring lens:**
  1. The pi arm passes `Deps.CapabilityRules`, fed by the root `capabilityRules`. `models.json` limits equal the table's.
     Covered by `cmd/arbitron/lunaroute_pi_wiring_test.go:TestLunaRoutePiWiring` and `TestLunaRoutePiRootChild`. Read the
     file the fake binary saw, not the returned bytes.
  2. `models.json` contains `$LUNAROUTE_API_KEY` literally and never the key. The real key appears only in the child env.
  3. Argv carries `--model lunaroute/<id>`. `--thinking` maps the validated effort exactly (`none` → `off`) and never floors
     it. Check that `args.go:thinkingLevel` has no lossy branch for LunaRoute.
  4. `ModelEvidence` is parsed from `pi_message_end` and persisted on the node.
  5. Non-LunaRoute pi is unchanged. Covered by `TestLunaRoutePi_NonLunaRouteUnchanged`.
- **Review 2 — failure lens:**
  1. Unknown limits or reasoning must refuse before reveal and before the directory is created. Covered by
     `TestLunaRoutePiWiringMissingMetadata` and `TestLunaRoutePi_RefusalsBeforeIO`.
  2. A `write`/`chmod` failure must remove the directory and return a local marker. Covered by
     `TestLunaRoutePi_CleanupAndSafeErrors`.
  3. A context cancelled during preparation must leave no directory. `ctx.Err()` is checked before `mkdirTemp`, and the
     cancel must also be checked during the run.
  4. A pi exit 0 with a failed turn must fail the run, preserving `ErrTurnFailed`.
  5. Concurrent runs must use distinct directories. Covered by `TestLunaRoutePi_ConcurrentDistinctDirs`.
  6. An ambient `PI_CODING_AGENT_DIR`, `LUNAROUTE_API_KEY` or other provider keys in the server env must not reach the child.
     Check what base env `CredentialEnv` starts from.
  7. `harden:adapter/pi/model_probe.go` (lines 60 and 62, identity safety) survives.
- **Seed evidence:**
  - `adapter/pi/lunaroute.go:lunaRouteModelsJSON` refuses when `!m.ContextTokens.Known() || !m.OutputTokens.Known()` or the
    reasoning fact is unknown.
  - `prepareLunaRoutePiLaunchWithFS` writes `models.json` with mode `0600` and sets `PI_CODING_AGENT_DIR=<dir>`.
  - `adapter/pi/provider.go:168` calls `prepareLunaRoutePiLaunch(ctx, p.binding, p.rules)` and runs
    `defer launch.cleanup()`.

### Area 7 — A LunaRoute chat node completes through /v1/chat/completions with exactly the validated settings, or fails with one classified, secret-free gateway outcome

- **Promise:** An `llm_call` on a LunaRoute profile posts to `https://gw.lunaroute.com/v1/chat/completions` with Bearer
  `lr_…`.
  - It sends no OpenRouter headers and does not follow redirects.
  - `MaxTokens`, effort and request settings are checked against the table before I/O.
  - A different executed model fails the run.
  - Every gateway failure maps to exactly one outcome: credentials rejected, model unavailable, busy (with Retry-After),
    service failure, request refused (not retryable), or cause unknown. No key or raw gateway text appears in the error.
- **Entry:** `domain/engine/executor_llm.go:LLMCallExecutor.Execute` → `adapter/provider/factory.go:Factory.ChatProviderFor`
  (lunaroute arm)
- **Sink:** the HTTP POST from `adapter/openrouter` (`Chat`/`StreamChat`) and the node response, or the
  `domain/provider/lunaroute_classify.go:LunaRouteFailure` node error
- **Files:**
  - Changed: `adapter/provider/lunaroute_chat.go`, `adapter/provider/factory.go` (`ChatProviderFor`),
    `adapter/openrouter/provider.go` (`Config.ClassifyError`, `Preflight`, `AcceptExecutedModel`, SSE error frames),
    `adapter/openrouter/request.go`, `adapter/openrouter/response.go`, `domain/provider/lunaroute_classify.go`,
    `domain/provider/lunaroute_errors.go`, `adapter/procenv/httpx.go` (`NoRedirectClient`)
  - Crossed: `adapter/provider/dynamic.go` (`Resolve`), `domain/provider/pinning.go`, `domain/engine/executor_llm.go`,
    `domain/engine/ports.go` (`ChatRequest`), `domain/provider/lunaroute_validate.go` (`CheckLunaRouteRequest`),
    `cmd/arbitron/main.go` (`HTTPClient`)
- **Folded in:** the LR-04/R4 code table (`lunaRouteDocumentedCodes`), `NormalizeLunaRouteRetryAfter`, `RedactLunaRoute`
  and `LunaRouteErrorCode`. This area owns the classifier's semantics; Areas 8 and 9 only check that they feed it.
- **Review 1 — wiring lens:**
  1. The production `LLMCallExecutor` reaches the lunaroute arm with a guarded binding, and the URL contains `/v1` exactly
     once. Covered by `cmd/arbitron/lunaroute_chat_wiring_test.go:TestLunaRouteChatWiring`.
  2. `ClassifyError` receives the real status, body, transport error, `Retry-After` header and
     `LunaRouteErrorCode(body)`. The code must not be permanently empty. Covered by
     `TestLunaRouteGatewayRejectsMutatedInputs` and `TestLunaRouteChat_ClassifiedErrors`.
  3. `Preflight` runs `CheckLunaRouteRequest` with the root `capabilityRules`, `req.MaxTokens` and the setting names
     actually sent, before any I/O. Covered by `TestLunaRouteChat_PreflightRefusesBeforeIO` and
     `TestLunaRouteChat_RequestSettings`.
  4. No `HTTP-Referer` or `X-Title` header is sent, and redirects are not followed. Covered by
     `TestLunaRouteChat_RedirectNotFollowed`.
  5. Requested axes are never floored. Covered by `TestLunaRouteChatWiringEffectiveAxesNeverFloor` and
     `TestLunaRouteChatWiringMissingEffortFactsRefuse`.
- **Review 2 — failure lens:**
  1. Each status and code must map to its outcome:
     - 401 `INVALID_API_KEY` or `MISSING_LUNAROUTE_HEADER` → credentials rejected.
     - 404 `INFERENCE_MODEL_NOT_FOUND` → model unavailable.
     - 429 `CONCURRENT_REQUEST_LIMIT_EXCEEDED` → busy, with Retry-After.
     - 429 `BYOK_SPEND_CAP_REACHED` or `MODEL_UNPRICED` → request refused, not busy.
     - 503 → service failure.
     - Any other status → cause unknown.
  2. A structured SSE error frame with no choices must be classified, never returned as empty content.
     `harden:adapter/openrouter/provider.go` survives at lines 239–240 (gap G4).
  3. A body read cut short must be discarded rather than used as a snippet. An `lr_` key or Bearer token echoed in the body
     must never appear in the error. Covered by `TestLunaRouteGatewayBodyReadError`.
  4. A cancelled transport must give cause unknown; a TLS or dial failure must give service failure.
     `harden:domain/provider/lunaroute_classify.go` survives at lines 118, 137, 141, 189, 217, 225, 229, 232, 234 and 236:
     redaction sizing, snippet boundary, Retry-After digits, transport precedence and code-over-status (gap G3).
  5. A `MaxTokens` above the known output limit, an unsupported setting, or an effort without exact chat support must be
     refused before the request is sent. Assert that the fake server got zero requests.
- **Seed evidence:**
  - `adapter/provider/lunaroute_chat.go:lunaRouteChat` builds `openrouter.New` with
    `HTTPClient: procenv.NoRedirectClient(...)`, a `ClassifyError` that calls `ClassifyLunaRoute(...)` with
    `Code: LunaRouteErrorCode(LunaRouteChat, body)`, `AcceptExecutedModel: modelidentity.LunaRouteExecutedModelAccepted`,
    and a `Preflight` that calls `CheckLunaRouteRequest(caps, launch, req.MaxTokens, settings)`.
  - Grep finds `ClassifyError`, `Preflight` and `AcceptExecutedModel` set only in `lunaroute_chat.go` in production.

### Area 8 — connection_models (and CLI connection validate) lists a LunaRoute connection's live models with curated known/unknown facts, admin-only and key-free

- **Promise:** The admin-only `connection_models` tool lists a connection's models. For each listed id it returns:
  - its family (vendor or `unresolved`);
  - task and backend facts;
  - context and output limits;
  - efforts and reasoning, each either known with an evidence reference or explicitly `unknown`;
  - a note that availability does not imply compatibility.

  The listing comes from `GET <root>/v1/models` with Bearer `lr_…`, with no redirects. Failures map to stable messages and
  never include key material. `arbitron connection validate` lists through the same probe arm with a guarded binding.
- **Entry:** `adapter/mcp/tools_connection_models.go:connectionModelsHandler` and `cmd/arbitron/connection.go:runConnectionValidate`
- **Sink:** the HTTP GET in `adapter/probe/lunaroute.go:lunaRouteProbe.Probe`, then either the
  `domain/provider/model_discovery.go:DiscoveryView` JSON or CLI output, or the `discoveryErrorResult` text
- **Files:**
  - Changed: `adapter/mcp/tools_connection_models.go`, `adapter/mcp/register.go`, `adapter/mcp/deps.go`
    (`Deps.ModelDiscovery`), `domain/provider/model_discovery.go`, `adapter/probe/lunaroute.go`,
    `adapter/probe/factory.go` (lunaroute arm), `cmd/arbitron/connection.go`, `cmd/arbitron/main.go` (`probeFactory`
    around line 1030, `NewModelDiscoveryService` around 1078, `ModelDiscovery` into MCP deps around 462)
  - Crossed: `adapter/probe/httpshared.go`, `adapter/procenv/httpx.go`, `domain/provider/authz.go`,
    `domain/provider/connection_service.go`, `domain/provider/lunaroute_reveal.go`,
    `domain/provider/lunaroute_classify.go`
- **Folded in:**
  - `DiscoveryNote` and `connectionModelsDescription` text.
  - Non-LunaRoute providers are all-unknown.
  - `ModelsUnsupported` produces "listing not supported".
  - The ids `harden:domain/provider/model_discovery.go` and `harden:adapter/mcp/tools_connection_models.go` are
    string-concatenation survivors. Pin the text in a test or record them as equivalent.
- **Review 1 — wiring lens:**
  1. The serve root constructs one `ProbeFactory` with the shared outbound client and injects it into
     `NewModelDiscoveryService` and `Factory` `Deps.Probes`. `Deps.ModelDiscovery` is non-nil, and the tool is registered.
     Covered by `cmd/arbitron/lunaroute_discovery_wiring_test.go:TestLunaRouteDiscoveryWiring`.
  2. The admin gate runs before any store read or probe. Covered by `TestConnectionModels_AuthBeforeProbe`. The binding
     comes from `ConnectionStore.Get` and is wrapped by `GuardLunaRouteReveal`. Covered by
     `TestDiscover_GuardAppliesToDiscoveryBinding`.
  3. The probe sends Bearer to exactly `https://gw.lunaroute.com/v1/models` through `NoRedirectClient` and feeds the
     classifier `Code`, `RetryAfter` and `Secrets: key`. Covered by `TestLunaRouteProbe_ListsModels` and
     `TestLunaRouteProbe_VerifiedCodeFromHTTP`.
  4. View facts come from the root `capabilityRules`, embedded defaults included, and family comes from `ClassifyFamily`.
     Covered by `TestLunaRouteDiscoveryWiringEmbeddedDefaults`.
  5. CLI validate builds its binding through `GuardLunaRouteReveal` (`connection.go:127`). Covered by
     `cmd/arbitron/lunaroute_cli_validate_test.go:TestConnectionValidate_LunaRouteProductionWiring` and
     `lunaroute_cli_binding_test.go:TestBindingFromConnection_GuardsLunaRoute`.
- **Review 2 — failure lens:**
  1. A non-admin caller must get forbidden with zero probe calls. An unknown connection must get not found.
  2. A malformed or oversized listing must be a service failure and never an empty catalog. An empty `data` array must be
     reachable with zero models. Covered by `TestLunaRouteProbe_EmptyListIsReachable`.
  3. A 3xx status must never count as success: `harden:adapter/probe/lunaroute.go` (line 76, the `>= 300` boundary)
     survives (gap G6). Redirects must be refused.
  4. An unclassified store or probe error must produce generic text and a log line that names only the connection. Covered
     by `TestConnectionModels_LogsNeverEchoUnclassifiedErrors`.
  5. A table model absent from the listing must not be shown. A listed model absent from the table must have every field
     `unknown`.
- **Seed evidence:**
  - `cmd/arbitron/main.go:1030` sets `probeFactory := adaptprobe.NewProbeFactory(...)`, which is passed as `Probes:` into
    `NewFactory` and into `provider.NewModelDiscoveryService(connStore, probeFactory, capabilityRules)` at `main.go:1078`.
  - `adapter/probe/lunaroute.go:50` calls `GuardLunaRouteReveal(b).RevealKey(ctx)`, and line 26 sets
    `procenv.NoRedirectClient(d.HTTPClient)`.

### Area 9 — An opaque LunaRoute agent failure is reported with at most one bounded gateway observation, keeping the original error and never retrying

- **Promise:** When a wrapped LunaRoute agent run fails opaquely, the wrapper makes one probe of `/v1/models` with the same
  guarded binding, bounded to 20 s.
  - It reports the result as a current observation, never as proof of the cause: credentials rejected now, model missing
    now, service failure now, or listed with cause unknown.
  - The original error identity is preserved.
  - It never probes on success, cancellation, local preparation failures or already-classified failures.
  - It never retries or substitutes.
- **Entry:** `adapter/provider/lunaroute_agent.go:lunaRouteAgent.RunAgent` (inner error branch)
- **Sink:** `domain/provider/lunaroute_diagnosis.go:LunaRouteDiagnosis` returned to `AgentCallExecutor`, which fails the node
- **Files:**
  - Changed: `adapter/provider/lunaroute_agent.go` (`diagnose`, `lunaRouteDiagnosable`, `lunaRouteLocalErrors`),
    `domain/provider/lunaroute_diagnosis.go`, `domain/provider/lunaroute_errors.go` (`LocalLunaRouteError`,
    `SanitizeLunaRouteError`, `ErrLunaRouteLocalFailure`)
  - Crossed: `adapter/provider/factory.go` (`Deps.Probes`), `adapter/probe/lunaroute.go`, `cmd/arbitron/main.go`
    (`Probes: probeFactory`), `domain/engine/executor_agent.go`
- **Folded in:** the nil-probes path, which must report "could not be checked" and must not panic.
- **Review 1 — wiring lens:**
  1. `Factory` `Deps.Probes` is the serve-root `probeFactory`, not nil. A failed codex run hits the probe's HTTP sink exactly
     once. Covered by `cmd/arbitron/lunaroute_codex_wiring_test.go:TestLunaRouteCodexWiringOpaqueFailure` and
     `TestLunaRouteAgent_PostFailureHTTPSink`.
  2. A successful run makes zero probe calls. Covered by `TestLunaRouteAgent_SuccessIsPassThrough`.
  3. The probe gets the same guarded binding the run used, under `lunaRouteDiagnosisTimeout`.
  4. `errors.Is(diag, original)` holds through `Unwrap`, and so does `errors.Is(diag, observation)`.
- **Review 2 — failure lens:**
  1. A cancelled context, any `lunaRouteLocalErrors` member, or an existing `*LunaRouteFailure` must produce no probe.
     Covered by `TestLunaRouteAgent_NoDiagnosisForLocalOrCancelled` and `TestLunaRouteCodexWiringLocalFailureNeverProbes`.
  2. A probe error that is not a `LunaRouteFailure` must leave `Checked=false` and report cause unknown, never a verdict.
     `harden:adapter/provider/lunaroute_agent.go` (lines 83 and 85) and `harden:domain/provider/lunaroute_diagnosis.go`
     (lines 17 and 23) survive, so the branches are not discriminated (gap G5).
  3. A listed model must give "credential accepted, model listed, cause unknown" and never "credentials rejected".
  4. A key inside the original error text must be redacted in `LunaRouteDiagnosis.Error()`.
  5. The probe timeout must bound the diagnosis without hanging the node. Nil probes must report unchecked. Covered by
     `TestLunaRouteAgent_NilProbesIsUncheckedNotPanic`.
- **Seed evidence:**
  - `adapter/provider/lunaroute_agent.go:diagnose` switches on these cases: a probe error that is a `LunaRouteFailure`;
    any other probe error; `!Reachable` or `ModelsUnsupported`; the model being listed; the default.
  - `lunaRouteLocalErrors` includes `ErrLunaRouteLocalFailure`, `ErrLunaRouteStoredCredential` and
    `ErrInvalidSelection`.

### Area 10 — Release readiness is claimed only for LR-01–04, LR-07 and LR-08 with existing evidence, and every author-facing surface lists chat, codex and pi only

- **Promise:** The evidence register gates readiness on exactly LR-01–LR-04, LR-07 and LR-08 being `verified`, each with
  existing evidence and real `applied_to` symbols. LR-05 and LR-06 are `deferred` (R7). Evidence references in the
  capability table and the reviewed-id table resolve. The guide and MCP text list chat, codex and pi only.
- **Entry:** `docs/evidence/lunaroute-provider/register.yaml`, read by `test/lunaroute/register_test.go`
- **Sink:** the gate tests (`TestReleaseGateRejectsPending`, `TestVerifiedEvidenceReferences`,
  `TestRegisterDeferredScope`) and the published text in `docs/setup/lunaroute.md`, `adapter/mcp/authoring_guidance.go`
  and `adapter/mcp/lunaroute_backend_hint.go`
- **Files:**
  - Changed: `docs/evidence/lunaroute-provider/register.yaml`, `docs/evidence/lunaroute-provider/*.md`, `observations/`,
    `sources/`, `test/lunaroute/register_test.go`, `test/lunaroute/evidence_helpers_test.go`,
    `test/lunaroute/docs_examples_test.go`, `docs/setup/lunaroute.md`
  - Crossed: `adapter/config/capabilities.default.yaml` (evidence refs), `domain/modelidentity/version.go`
    (`lunaRouteReviewedIDs` evidence anchors), `adapter/mcp/authoring_guidance.go`, `adapter/mcp/lunaroute_backend_hint.go`
- **Folded in:** observation redaction (`TestObservationBoundaryRedaction`, `TestObservationPrivateSink`) and the R7
  descoping of the live MCP matrix (P4-T7) and the P3-T5 harness.
- **Review 1 — wiring lens:**
  1. The register holds exactly LR-01 to LR-08. LR-05 and LR-06 are `deferred` and excluded from the gate. Covered by
     `TestRegisterDeferredScope`.
  2. Every `applied_to` `file:symbol` resolves to a live symbol (grep each one). A stale pointer is a gap.
  3. Every `evidence:` path in `capabilities.default.yaml` and every `Evidence` anchor in `lunaRouteReviewedIDs` resolves to
     an existing file and heading. Find or add the test that goes red on a broken one.
  4. Every task/backend `support: supported` fact cites an operator observation under `observations/`, never a docs page
     (LR-02 rule).
  5. Guide examples parse and use only chat, codex and pi. Covered by `test/lunaroute/docs_examples_test.go`.
- **Review 2 — failure lens:**
  1. A `pending`, `escalated` or `deferred` item, or a skipped live test, can never yield readiness. Covered by
     `TestReleaseGateRejectsPending`.
  2. A missing evidence file or a wrong anchor must fail a test. Covered by `TestRegisterRejectsBadEvidence`.
  3. Observation files must contain no `lr_` keys, Bearer values or prompts. Covered by `TestObservationBoundaryRedaction`.
  4. Text that presents claude-code or opencode as supported for LunaRoute, in the guide or the MCP text, must turn a test
     red. Covered by `TestLunaRouteBackendHint_RejectsMissingAndContradictorySurfaces` and
     `TestAuthoringGuidance_ModelDiscoveryAndExplicitSelection`.
- **Seed evidence:**
  - `register.yaml` marks LR-01, 02, 03, 04, 07 and 08 `verified` and LR-05/06 `deferred`, with empty `applied_to`.
  - LR-02 `applied_to` lists `adapter/config/capabilities.default.yaml:lunaroute_models`.
  - The `capabilities.default.yaml` facts for `glm-5.3` cite `observations/operator-manual-agent-tasks-2026-11-12.md`.
