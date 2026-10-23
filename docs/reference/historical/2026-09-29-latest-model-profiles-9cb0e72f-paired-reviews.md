# Paired Reviews — Latest Model Profiles

Phase 2 runs only after every single-system box in `docs/reference/focus-areas.md`
is ticked. Each row joins two behaviours that meet all three conditions:

- they share persisted state, a wire contract, a flag or an ordering;
- different tasks or commits built them;
- each side passes in isolation while the combination is wrong.

One approval covers both directions. Trace the TRACE path, then show that BREAK
is impossible, either by a test that goes red or by a guard cited at `file:symbol`.
Area numbers refer to `focus-areas.md`.

**Budget:** 8 singles × 2 + 14 paired = 30 minimum passes ≤ 500 × 0.35 = 175.

## Paired Integration Reviews

| # | Paired Review | Seam | Risk | Status |
|---|---|---|---|---|
| 1 | Area 1 run pins ↔ Area 2 refresh advancement | SHARED: `profiles.model` and the `ProfileCache` generation written by `commitAdvanceTx` and `PublishCommitted` (P2T3, P3T5, P3T6), versus `instances.model_pins` written at trigger (P1T3) and read through `PinningResolver` (P1T4); ordering: `SnapshotProfiles` captures all aliases under one cache read lock, `Load` swaps the registry atomically, and `PublishCommitted` confirms the committed revision/model before refresh success · TRACE: trigger run R1 on X → refresh advances the alias to Y and publishes → R1 retries a node and resumes after restart → trigger R2 → argv or request body of both · BREAK: R1 executes Y after advancement or restart, or R2 executes X because publication was reported before the cache held Y | high | [x] |
| 2 | Area 3 interactive continuation ↔ Area 2 refresh advancement | SHARED: the pinned model of an interactive run versus the advanced profile row, reaching the harness through `PinningResolver.ForRun` and `InteractiveStreamSelector.StreamProviderFor` while tools and credentials stay live (P1T4 continuation versus P2T3/P3T6 advancement) · TRACE: start an interactive Claude or Codex run on X → advance and publish Y → `conversationHandler.HandleMessage` follow-up → resumed argv and recorded session model · BREAK: the follow-up turn runs Y, or loses the pin while re-resolving the live route | high | [x] |
| 3 | Area 3 interactive continuation ↔ Area 6 admin profile edit | SHARED: `profiles.allowed_tools`, `connection_ref` and the reloaded `ProfileCache` from `ProfileService.UpdatePatch` (P2T2/P2T3, `c05d7821`/`c64b6f4e`), consumed with persisted model pins by `conversation.go:HandleMessage` and per-turn `StreamProviderFor` (P1T4, `e95ef5b4`) · TRACE: start on model X with Read/Grep → MCP admin edits model to Y, narrows tools to Glob and changes to a compatible connection → resume → inspect argv/env or request and persisted turn; repeat with an explicit empty tool list · BREAK: the successful turn retains revoked tools or the old connection, falls back to global tools after clear, or adopts Y instead of pin X; model-only advancement cannot exercise these edits | high | [x] |
| 4 | Area 1 run pins ↔ Area 4 Claude/Codex evidence | SHARED: the `ResolvedBinding.Pinned` flag set by `domain/provider/pinning.go:PinningResolver.Resolve` (P1T3/P1T4) and read by `adapter/claudecli/model_probe.go:requiredExecutedModel` and `adapter/codex/provider.go:Provider.RunAgent` (P1T4 lane commits) · TRACE: pinned batch run on X through agent_call, llm_call and child-input on Claude and Codex → harness reports a fallback model → node result · BREAK: a pinned binding reaches an adapter with `Pinned` false or an empty model, so the fallback output is accepted as X | high | [x] |
| 5 | Area 1 run pins ↔ Area 5 pi/OpenCode/HTTP evidence | SHARED: the same `ResolvedBinding.Pinned` and `Model` consumed by `adapter/pi/model_probe.go:verifyExecutedModel`, `adapter/opencode/model_probe.go:checkModelExecution`, `adapter/anthropicapi/model_probe.go:messageIdentity.verify` and `adapter/openrouter/model_probe.go:verifyExecutedModel` (P1T3 pin production versus P1T4/P3T4 lane enforcement) · TRACE: pinned run on X through pi, OpenCode, Anthropic and an OpenAI-compatible connection → response or export names Y or another provider → node result · BREAK: the lane skips verification for a pinned binding (for example a factory path that rebuilds the binding without `Pinned`) and the run completes on Y | high | [x] |
| 6 | Area 3 interactive continuation ↔ Area 4 Claude/Codex evidence | SHARED: `ChatRequest.RequireExecutedModel` and `req.Model`, set in `domain/engine/conversation.go:HandleMessage` and `executor_interactive.go:Execute`, read by `adapter/claudecli/streamjson.go:pinnedModel` and `adapter/codex/interactive_provider.go:startTurn` (`VerifyModel`), which ignore `binding.Pinned` (P1T4 engine continuation versus separate P3T4 harness evidence commits) · TRACE: pinned interactive session on X → resumed turn where the stored session or the result reports Y → turn outcome and persisted NodeExecution · BREAK: a continuation path that leaves `RequireExecutedModel` false, or sets `req.Model` different from the binding, makes the stream accept Y | high | [x] |
| 7 | Area 3 interactive continuation ↔ Area 5 HTTP evidence | SHARED: `RequireExecutedModel` on chat-kind interactive streams, routed by `InteractiveStreamSelector.StreamProviderFor` to `ChatProviderFor` and checked in `anthropicapi` and `openrouter` `StreamChat` from `message_start` or frame model (P1T4 engine continuation versus P3T4 HTTP evidence commits) · TRACE: pinned chat-kind interactive run on an Anthropic or OpenAI-compatible connection → follow-up turn whose stream frames name Y → turn outcome · BREAK: the stream verifier reads identity only on non-streaming responses or after the turn is recorded, so a Y-served turn completes as X | high | [x] |
| 8 | Area 2 refresh advancement ↔ Area 6 admin profile edit | SHARED: the `profiles` row and its `revision`, written by the automatic CAS (`commitAdvanceTx`) and by admin `UpdateExpected`/`UpdateValidated`; plus the `EligibleCandidateSource` proof (`Newest`, `ValidateStart`, `RevalidateStart`) that admin creation consumes from the P3T6 `CandidateService` (P2T2/P2T3 admin writes versus P3T6 selection) · TRACE: admin tools-only `profile_update` reads revision n → refresh CAS advances to Y (n+1) → admin write lands → `profile_get` and next new-run model; separately, admin creates a following profile with no model → candidate chosen → executed model · BREAK: the admin write silently reverts Y or the CAS overwrites the admin edit; or a created following profile starts on a candidate not confirmed on its own route | high | [x] |
| 9 | Area 2 refresh discovery ↔ Area 4 Claude/Codex lanes | SHARED: the D3/D4 launch contract and executable spelling: the B1 probe router must use the same argv, flags, env and evidence readers as real execution (`requiredExecutedModel`, `rolloutVerifier`), and the Codex `models_cache.json` slug filter (P3T4 probe router remainder versus existing P1T4/P3T4 harness execution commits) · TRACE: catalog lists X → probe on a Claude subscription path and a Codex account path → evidence persisted with `NativeModel` → profile advances → pinned run on X through the real adapter · BREAK: the probe uses a different flag set (for example `--fallback-model` or a different home or env) and confirms X while real runs fail or silently fall back; or Codex probes a model absent from the account list | high | [x] |
| 10 | Area 2 refresh discovery ↔ Area 5 pi/OpenCode/HTTP lanes | SHARED: path configuration revision from `adapter/provider/path_inventory.go` and the native model spelling, including `provider/` prefixes, that the probe confirms and the lane later executes (`pi/args.go:buildArgs`, `opencode` injected config, connection base URL) (P3T3/P3T4 inventory and probe versus P1T4 lane execution) · TRACE: confirm a slash-bearing or aggregator model on pi, OpenCode and an OpenRouter connection → advance → pinned run → argv or request body and verifier result · BREAK: the probe confirms one spelling or provider and the run executes another, so every advanced run fails verification or a different provider serves it | high | [x] |
| 11 | Area 7 admin settings ↔ Area 2 refresh ingestion | SHARED: `domain/settings/service.go:Service.ReadInternal().CatalogURL`, validated at write as absolute HTTP(S) only, versus the fetch-time SSRF guard in `adapter/outbound/guarded_client.go` (P1T1/P1T2 persisted settings versus P3T2/P3T6 fetch pipeline) · TRACE: admin changes the URL from A to valid B, then to a loopback or private URL → next cycle's fetch → stored `SourceRef` and fetch error · BREAK: the cycle keeps fetching A after the edit, or the internal URL is fetched because only write-time validation was relied on, or the error echoes the URL's query token | high | [x] |
| 12 | Area 1 run pins ↔ Area 6 admin profile edit | SHARED: the explicit-version grammar `modelidentity.NewVersionResolver` used by the trigger snapshotter (P1T3/P3T1), versus profile validation that skips it for pinned mode (`selection.go:ValidateSelection`, P2T2) · TRACE: admin saves or seeds a pinned profile with `sonnet` or an Azure deployment → `profile_get` succeeds → trigger a workflow using it · BREAK: the profile is accepted at write and every new run of that workflow fails at trigger, with no admin-visible rejection | med | [x] |
| 13 | Area 2 refresh advancement ↔ Area 8 announcement delivery | SHARED: stable `Entry.CanonicalID` from `IdentityMapper.Map` (P3T1/P3T2/P3T8) keys lifetime event uniqueness in migration 022; and the frozen `AnnouncementPayload.Changes`, written in the same `CommitCycle` transaction from the `Advancement` records `commitAdvanceTx` returned (P3T5), then partitioned and sent (P4T1/P4T2) · TRACE: a refresh with two eligible following profiles, one losing its CAS to an admin edit → event row → drain → receiver card(s); also confirm the same DeepSeek model through deepseek, ollama-cloud and openrouter, edit tier policy and refresh again · BREAK: duplicate events/cards result from provider-specific or policy-dependent canonical IDs; or the card lists the conflicted profile as changed, omits the committed one, or is rebuilt from the current profile row instead of the frozen payload | med | [x] |
| 14 | Area 7 admin settings ↔ Area 8 announcement delivery | SHARED: the raw `teams_webhook_url` from `Service.ReadInternal`, snapshotted at discovery into `CycleCommit.WebhookConfigured` and re-read per part at drain (`AnnouncementService.deliver`) (P1T1/P1T2 settings versus P3T5/P4T2 event classification and delivery) · TRACE: discovery while the settings read faults, then while empty, then after an admin sets the URL through HTTP → event state → drain → receiver URL · BREAK: a read fault is treated as empty and suppresses forever; or the redacted placeholder reaches the sender; or an event discovered while empty is replayed after the URL is set | med | [x] |

## No seam

All 28 combinations of the 8 areas were considered: 14 paired above and 14
unpaired below. The absorbed-row notes record former rows, not extra combinations.
No seam is deferred for budget.

### Inside one behaviour or same-task contract

- Area 1 ↔ Area 3: the shared `PinLookup` / `PinningResolver` contract was built by P1T3/P1T4; entry-specific batch and continuation paths stay separate. Release-versus-turn ordering remains Area 3 failure check 5.
- Area 4 ↔ Area 5: the lanes share only `claudecli.RunSpec`, built in the same P3T4/P1T4 slice; no independently built state handoff. Each lane owns its verifier.

### Genuinely unrelated

- Area 1 ↔ Area 7: run pinning reads no system setting.
- Area 1 ↔ Area 8: announcements never read or write run pins; frozen advancement payloads are covered by row 13.
- Area 3 ↔ Area 7: continuation reads no system setting.
- Area 3 ↔ Area 8: no shared state, contract, flag or ordering.
- Area 4 ↔ Area 6: admin model spelling reaches lane execution through pins (row 12); no direct profile-write/evidence state contract.
- Area 4 ↔ Area 7: no shared system setting.
- Area 4 ↔ Area 8: no shared state; proof paths are excluded from cards in Area 8 folded check 4.
- Area 5 ↔ Area 6: admin model spelling reaches lane execution through pins (row 12); no direct profile-write/evidence state contract.
- Area 5 ↔ Area 7: no shared system setting.
- Area 5 ↔ Area 8: no shared state.
- Area 6 ↔ Area 7: profile writes read no system setting.
- Area 6 ↔ Area 8: admin edits are not announced; row 13 traces only committed automatic changes.

### Struck internal row

- Former row 10 (old Areas 2 ↔ 7): classification and ordering feed the same refresh transaction; now internal to Area 2. Preserve policy-edit, dated-snapshot and unmatched-target checks in its lenses.

### Absorbed duplicate row

- Former row 15 (old Areas 7 ↔ 9): after the area merge it duplicates the Area 2 ↔ Area 8 pair. Row 13 now checks both canonical event uniqueness and frozen committed changes; one approval covers both directions.
