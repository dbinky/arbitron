# Paired integration reviews — LunaRoute provider

A row exists only where two behaviours from `focus-areas.md` meet all three conditions:

- they share state, a contract or an ordering;
- different plan tasks built them;
- their combination can be wrong while each side passes its own tests.

Each row is one seam, and its single approval covers both directions. Area numbers refer to `focus-areas.md`.

## Paired Integration Reviews

| # | Paired Review | Seam | Risk | Status |
|---|---|---|---|---|
| 1 | Area 3 profile save ↔ Area 5 pi launch | SHARED: the one `capabilityRules` object (`adapter/config/capabilities.go:LoadCapabilityRules` → `ProfileService` rules and `Factory` `Deps.CapabilityRules` → `pi.Config.CapabilityRules`); its limits, reasoning and pi effort levels are validated at save (P1-T8/P1-T10) and emitted into models.json and `--thinking` (P4-T4/P4-T5) · TRACE: load an override file setting `glm-5.3` output to 4096 and pi efforts to `low`; save a pinned pi profile with effort `low` through MCP; run the node through the production factory against a fake pi binary; read the models.json and argv it received · BREAK: models.json carries a context/max-token/reasoning value other than the one save validated, or `--thinking` sends a level other than the validated effort (generic flooring or a `none`/`max` remap) | high | [x] |
| 2 | Area 3 profile save ↔ Area 6 chat | SHARED: persisted profile model/effort and the shared capability rules; `ProfileService.validateAxes` promises exact effective axes (P1-T8/P1-T10), while `DynamicProfileResolver.Resolve` and `lunaRouteChat` must preserve them into `openrouter.buildBody` (P2-T3/P2-T4) · TRACE: load an override giving `glm-5.3` chat efforts `[low, high]`; save each pinned chat profile via MCP; reload the real profile cache, resolve through the run pinning resolver and execute an llm_call against fake JSON and SSE endpoints; compare saved effective effort and model to the captured reasoning_effort and model · BREAK: save succeeds and chat succeeds but the resolver/emitter drops or floors the saved effort or sends a model different from the pin; direct request-setting name/MaxTokens preflight checks remain inside Area 6 | high | [ ] |
| 3 | Area 3 profile save ↔ Area 4 codex launch | SHARED: the codex effort allowlist and route identity, validated at save by `domain/provider/lunaroute_validate.go:lunaRouteExactEffort` with the table's codex effort fact, and emitted by `adapter/codex/lunaroute.go:lunaRouteEffortArgs` and `lunaRouteExecExtra` (`review_model`, base URL) (P1-T9 vs P4-T2) · TRACE: add a codex effort fact `[low, xhigh]` for `glm-5.3` in an override file; save pinned codex profiles with each effort; run each through the factory against a fake codex binary; compare argv to the saved effective axes · BREAK: save accepts an effort that codex emits differently or drops (for example `-c model_reasoning_effort` missing or remapped), or `review_model` names a model other than the saved pinned id, so codex silently runs a different setting or helper model than the profile promises | high | [ ] |
| 4 | Area 2 agent diagnosis ↔ Area 4 codex launch | SHARED: the local-failure contract (`domain/provider/lunaroute_errors.go:LocalLunaRouteError`, `ErrLunaRouteLocalFailure`, `SanitizeLunaRouteError`) that codex marks onto preparation failures and that `adapter/provider/lunaroute_agent.go:lunaRouteDiagnosable` reads to decide whether to probe (P2-T5 vs P4-T2/P4-T3) · TRACE: through the production factory, fail codex at each local stage (preflight, args, private-home mkdir/chmod, version probe, rollout read), then fail it opaquely after launch with stderr echoing the key; count probe HTTP hits and read the node error · BREAK: a local codex failure lacks the marker, so the wrapper probes and reports a misleading gateway observation for a local fault; or an opaque failure is marked local and never diagnosed | med | [ ] |
| 5 | Area 2 agent diagnosis ↔ Area 5 pi launch | SHARED: the same local-failure contract, marked by `adapter/pi/provider.go:localError` and `prepareLunaRoutePiLaunchWithFS` and read by `lunaRouteDiagnosable` (P2-T5 vs P4-T4/P4-T5) · TRACE: through the production factory, fail pi at preflight, models.json write, chmod, version probe and a failed turn with exit 0; count probe HTTP hits and read the node error · BREAK: a local pi failure (missing metadata, write failure) triggers a gateway probe and an observation that blames the gateway; or a failed turn is classified local and never diagnosed | med | [ ] |
| 6 | Area 2 agent diagnosis ↔ Area 7 discovery probe | SHARED: the `ProbeResult` contract (`Reachable`, `Models`, `ModelsUnsupported`, and whether `*LunaRouteFailure` is the error type) produced by `adapter/probe/lunaroute.go:lunaRouteProbe.Probe` and interpreted by `adapter/provider/lunaroute_agent.go:diagnose` (P2-T1 vs P2-T5) · TRACE: fail an agent run opaquely; have the fake gateway answer the follow-up `/v1/models` with 200 and the model, 200 without it, an empty list, a truncated body, a non-JSON body, 401 and 503; read each node error · BREAK: a truncated or malformed listing comes back as `Reachable` with partial models, so diagnosis falsely says the model is unavailable now; or a gateway verdict comes back as a plain error, not a `LunaRouteFailure`, so diagnosis drops a real "credentials rejected now" to "could not be checked" | med | [ ] |

## No seam

All 28 final area pairs were considered: six paired above and 22 below. The former launch/diagnosis wrapper split
is now internal to Area 2; the former identity-only area is folded into Area 3 and its calling paths.
No paired row was struck; these are exclusions, not unapproved reverse rows.

### Inside one behaviour

- 1 ↔ 2: stored-route tampering and switched-provider handling are Area 2's resolver/pinned-route checks; the switched-away retained-key defect is G1 in Area 1. Do not assume the LunaRoute URL guard runs on a different provider.
- 1 ↔ 3: `selection.go:profileUsesLunaRoute` reads the current connection during save; follow-up route changes belong to actual-call checks.
- 1 ↔ 4: `codex/lunaroute.go:prepareLunaRouteLaunchWithFS` guards the credential and derives the hosted URL; G1 is write-side ownership.
- 1 ↔ 5: `pi/lunaroute.go:prepareLunaRoutePiLaunchWithFS` guards the credential and derives the hosted URL; G1 is write-side ownership.
- 1 ↔ 6: `provider/lunaroute_chat.go:lunaRouteChat` guards reveal and preflight before its HTTP sink; G1 is write-side ownership.
- 1 ↔ 7: `probe/lunaroute.go:Probe` guards reveal and endpoint on the listing path; G1 is write-side ownership.
- 2 ↔ 3: save and actual-call validation share `checkLunaRouteRoute`; policy drift refuses loudly. Identity and pins are included in Area 2's lenses.
- 2 ↔ 6: the chat path has its own preflight and no agent diagnosis wrapper; `lunaRouteChat` feeds `CheckLunaRouteRequest` inside Area 6.
- 3 ↔ 7: discovery facts/family and profile validation consume the same rules/classifier; Area 7 checks the real feed. A display/save incompatibility is a visible refusal, not a silent successful run.
- 3 ↔ 8: `TestProductionR7CompletionApplication` reads the real capability table and observation files; evidence-anchor and sample/guide checks are folded into Areas 3 and 8, not another paired pass.
- 4 ↔ 8: register application pointers and codex evidence artifacts are read together by Area 8's gate tests; no independent runtime state contract.
- 5 ↔ 8: Area 8 compares pi observation metadata with the actual table via `TestProductionR7CompletionApplication`; no separate handoff to a running adapter.
- 6 ↔ 7: both feed the pure `ClassifyLunaRoute`; Area 6 owns classification semantics and Area 7 verifies the HTTP inputs, not a shared state transition.

### Genuinely unrelated

- 1 ↔ 8: release evidence does not participate in connection writes; application pointers are checked inside Area 8.
- 2 ↔ 8: register readiness does not drive factory policy or diagnosis; runtime gate wiring belongs to Area 2.
- 4 ↔ 5: codex and pi have separate private directories and process-local credential environments.
- 4 ↔ 6: codex and chat do not exchange runtime state; exact-model acceptance is folded into each execution path.
- 4 ↔ 7: codex launches do not consume discovery listings; their post-failure probe contract is Area 2 ↔ Area 7, paired row 6.
- 5 ↔ 6: pi and chat do not exchange runtime state; exact-model acceptance is folded into each execution path.
- 5 ↔ 7: pi launches do not consume discovery listings; their post-failure probe contract is Area 2 ↔ Area 7, paired row 6.
- 6 ↔ 8: chat runtime does not consume the evidence register; Area 8 checks its evidence references.
- 7 ↔ 8: discovery runtime does not consume the evidence register; Area 8 checks its application pointers.
