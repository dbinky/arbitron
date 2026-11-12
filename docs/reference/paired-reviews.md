# Paired integration reviews — LunaRoute provider

A row exists only where two behaviours from `focus-areas.md` meet all three conditions:

- they share state, a contract or an ordering;
- different plan tasks built them;
- their combination can be wrong while each side passes its own tests.

Each row is one seam, and its single approval covers both directions. Area numbers refer to `focus-areas.md`.

## Paired Integration Reviews

| # | Paired Review | Seam | Risk | Status |
|---|---|---|---|---|
| 1 | Area 3 profile save ↔ Area 6 pi launch | SHARED: the one `capabilityRules` object (`adapter/config/capabilities.go:LoadCapabilityRules` → `ProfileService` rules and `Factory` `Deps.CapabilityRules` → `pi.Config.CapabilityRules`); its limits, reasoning and pi effort levels are validated at save (P1-T8/P1-T10) and emitted into models.json and `--thinking` (P4-T4/P4-T5) · TRACE: load an override file setting `glm-5.3` output to 4096 and pi efforts to `low`; save a pinned pi profile with effort `low` through MCP; run the node through the production factory against a fake pi binary; read the models.json and argv it received · BREAK: models.json carries a context/max-token/reasoning value other than the one save validated, or `--thinking` sends a level other than the validated effort (generic flooring or a `none`/`max` remap), or the factory sees a different or nil table so a saved profile is refused only at launch | high | [ ] |
| 2 | Area 3 profile save ↔ Area 7 chat | SHARED: request-setting and limit facts in `lunaroute_models` (setting names fixed by the loader at `capabilities.go`) and the names, `MaxTokens` and effort that `adapter/provider/lunaroute_chat.go` Preflight reports for the body `adapter/openrouter` actually builds (P1-T8 vs P2-T3/P2-T4) · TRACE: mark `glm-5.3` `output_schema: unsupported` and output limit 1000 in an override file; run an `llm_call` with an output schema, then with `MaxTokens` 2000, then with a chat effort; capture the request at a fake `/v1/chat/completions` · BREAK: the body sends `response_format`, `max_tokens` above the limit, or a reasoning effort that Preflight did not report under the loader's setting name, so a known conflict reaches the gateway; or `buildBody` floors the effort to a different value than the one validated | high | [ ] |
| 3 | Area 9 agent diagnosis ↔ Area 5 codex launch | SHARED: the local-failure contract (`domain/provider/lunaroute_errors.go:LocalLunaRouteError`, `ErrLunaRouteLocalFailure`, `SanitizeLunaRouteError`) that codex marks onto preparation failures and that `adapter/provider/lunaroute_agent.go:lunaRouteDiagnosable` reads to decide whether to probe (P2-T5 vs P4-T2/P4-T3) · TRACE: through the production factory, fail codex at each local stage (preflight, args, private-home mkdir/chmod, version probe, rollout read), then fail it opaquely after launch with stderr echoing the key; count probe HTTP hits and read the node error · BREAK: a local codex failure lacks the marker, so the wrapper probes and reports a misleading gateway observation for a local fault; or an opaque failure is marked local and never diagnosed; or key text in a codex error survives into `LunaRouteDiagnosis.Error()` | med | [ ] |
| 4 | Area 9 agent diagnosis ↔ Area 6 pi launch | SHARED: the same local-failure contract, marked by `adapter/pi/provider.go:localError` and `prepareLunaRoutePiLaunchWithFS` and read by `lunaRouteDiagnosable` (P2-T5 vs P4-T4/P4-T5) · TRACE: through the production factory, fail pi at preflight, models.json write, chmod, version probe and a failed turn with exit 0; count probe HTTP hits and read the node error · BREAK: a local pi failure (missing metadata, write failure) triggers a gateway probe and an observation that blames the gateway; or a failed turn is classified local and never diagnosed; or `claudecli.SanitizeProcessError` output still carries key text into the diagnosis | med | [ ] |
| 5 | Area 9 agent diagnosis ↔ Area 8 discovery probe | SHARED: the `ProbeResult` contract (`Reachable`, `Models`, `ModelsUnsupported`, and whether `*LunaRouteFailure` is the error type) produced by `adapter/probe/lunaroute.go:lunaRouteProbe.Probe` and interpreted by `adapter/provider/lunaroute_agent.go:diagnose` (P2-T1 vs P2-T5) · TRACE: fail an agent run opaquely; have the fake gateway answer the follow-up `/v1/models` with 200 and the model, 200 without it, an empty list, a truncated body, a non-JSON body, 401 and 503; read each node error · BREAK: a truncated or malformed listing comes back as `Reachable` with partial models, so diagnosis falsely says the model is unavailable now; or a gateway verdict comes back as a plain error, not a `LunaRouteFailure`, so diagnosis drops a real "credentials rejected now" to "could not be checked" | med | [ ] |
| 6 | Area 3 profile save ↔ Area 5 codex launch | SHARED: the codex effort allowlist and route identity, validated at save by `domain/provider/lunaroute_validate.go:lunaRouteExactEffort` with the table's codex effort fact, and emitted by `adapter/codex/lunaroute.go:lunaRouteEffortArgs` and `lunaRouteExecExtra` (`review_model`, base URL) (P1-T9 vs P4-T2) · TRACE: add a codex effort fact `[low, xhigh]` for `glm-5.3` in an override file; save pinned codex profiles with each effort; run each through the factory against a fake codex binary; compare argv to the saved effective axes · BREAK: save accepts an effort that codex emits differently or drops (for example `-c model_reasoning_effort` missing or remapped), or `review_model` names a model other than the saved pinned id, so codex silently runs a different setting or helper model than the profile promises | med | [ ] |

## No seam

Each of the 45 area pairs was considered. Six are paired above, and the other 39 are listed here.

### Inside one behaviour

- 1 ↔ 2: a connection switched or tampered after save is refused at launch through `NormalizeLunaRouteBaseURL` in
  `checkLunaRouteBinding` (Area 2, Review 2 check 4).
- 1 ↔ 3: save reads the connection's canonical provider through `profileUsesLunaRoute` (Area 3, Review 1 check 3). A later
  switch is Area 2's job.
- 1 ↔ 5, 1 ↔ 6, 1 ↔ 7, 1 ↔ 8: every credential sink re-derives its URL from the stored root through
  `LunaRouteOpenAIBaseURL` and reveals through `GuardLunaRouteReveal`, which fails closed loudly on a foreign key. These
  are wiring checks in Areas 5–8. The silent case, a key kept on a switch away from lunaroute, is a write-side defect: gap
  G1 in Area 1.
- 2 ↔ 3: save and launch apply the same `checkLunaRouteRoute`, once to a `Profile` and once to a `ResolvedBinding`. Their
  consistency is Area 2's review (Review 1 check 3, Review 2 check 3), and divergence fails loudly at run time.
- 2 ↔ 4: the launch refusal for an identity runs through `checkLunaRouteIdentity` (Area 4, Review 1 check 1).
- 2 ↔ 5, 2 ↔ 6: the factory R7 gate and wrapper preflight run before the codex and pi adapters, and each adapter calls
  `CheckLunaRouteAdapterLaunch` itself. Which constructors are reachable is Area 2, Review 1 check 6.
- 2 ↔ 7: chat has no agent wrapper; its launch check is `Preflight` → `CheckLunaRouteRequest` in Area 7, Review 1 check 3.
- 2 ↔ 9: the wrapper's preflight-before-run ordering is Area 2, Review 1 check 3. That a preflight refusal is never
  diagnosed is Area 9, Review 2 check 1. Both are in one file, on different sub-paths.
- 3 ↔ 4: save calls the identity check directly; a direct call between areas is not a seam (Area 4, Review 1 check 1).
- 3 ↔ 8: discovery reads the same root `capabilityRules` (Area 8, Review 1 check 4). A display-versus-save mismatch fails
  loudly when save refuses.
- 3 ↔ 10: whether the capability table's evidence references resolve is Area 10, Review 1 checks 3–4.
- 4 ↔ 5, 4 ↔ 6, 4 ↔ 7: the executed-model acceptance call sites are Area 4, Review 1 check 4. A mismatch fails the run
  loudly with `ErrModelEvidence`, and fixed ids require exact equality, so nothing is accepted silently.
- 4 ↔ 8: the discovery family field is `ClassifyFamily` (Area 4, Review 1 check 3).
- 4 ↔ 10: whether the `lunaRouteReviewedIDs` evidence anchors resolve is Area 10, Review 1 check 3.

### Genuinely unrelated

- 1 ↔ 4: identity rules never read connection rows.
- 1 ↔ 9: diagnosis uses the run's binding, not a connection write.
- 1 ↔ 10: the register does not reference write rules beyond `applied_to` pointers, which Area 10 checks.
- 2 ↔ 8: discovery is not a launch and has no backend, pin or override.
- 2 ↔ 10: no shared state.
- 3 ↔ 9: diagnosis reads no profile or table state.
- 4 ↔ 9: diagnosis never judges family or identity.
- 5 ↔ 6: separate adapters with separate private dirs and separate child envs. They share only the env var name
  `LUNAROUTE_API_KEY`, which is process-local.
- 5 ↔ 7, 6 ↔ 7: chat and agents share no state; each consumes the classifier, a pure function, independently.
- 5 ↔ 8, 6 ↔ 8: launches do not consume listings. The probe runs after a launch failure only through Area 9, which is
  paired in rows 3, 4 and 5.
- 5 ↔ 10, 6 ↔ 10, 7 ↔ 10, 8 ↔ 10, 9 ↔ 10: the register points at their symbols, which Area 10 checks. No runtime state is
  shared.
- 7 ↔ 8: both call `ClassifyLunaRoute`, a pure function. Its semantics are Area 7's; Area 8 checks only its feed.
- 7 ↔ 9: the chat path has no diagnosis wrapper.
