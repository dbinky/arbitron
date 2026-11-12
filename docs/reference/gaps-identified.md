# Gaps identified — LunaRoute provider

> **Rule:**
> - Each record is at most ten lines.
> - When an item is closed, move it under `## Closed` with a one-line note.
> - The open list is what a pass reads, so keep it short.
> - An unfed or inert production path is never "acceptable". Fix it, or escalate it under `Won't Fix` with the ruling that
>   covers it.

## Open

- **[ESCALATION] Area 1 / single:1/review2 — the remaining approval prerequisites are G12 and G13.**
  `adapter/mcp/tools_connection.go:connectionUpdateHandler` must distinguish an explicit empty key from omission and refuse it.
  `domain/provider/lunaroute_connection.go:PrepareLunaRouteCreate` / `PrepareLunaRouteUpdate` must reject an explicitly
  supplied `lr_` credential for a foreign provider, including resubmitting the existing key on switch-away.
  The box requires real MCP → SQLite tests proving both refusals leave metadata, revision and ciphertext unchanged.
  No external fixture or dependency is missing; permitting either behavior instead requires an explicit operator decision
  narrowing Area 1's promise. G1's omitted-key/concurrent-switch defect is fixed this pass; stop after this one fix.

- [ ] **G2 (Area 2, high): inverting `BackendExplicit` in the dynamic resolver turns no test red.**
  The mutation run kept `harden:adapter/provider/dynamic.go`: negating `strings.TrimSpace(p.Backend) != ""` at
  `adapter/provider/dynamic.go:89` and `:124` survived. No test drives a real `DynamicProfileResolver.Resolve` binding to
  the launch gate.
  Add root tests: an explicit-pi profile reaches the adapter, and a backend-less profile is refused, both via the real
  resolver rather than a hand-built `ResolvedBinding`.
- [ ] **G3 (Area 6): classifier boundaries are untested.**
  The `harden:domain/provider/lunaroute_classify.go` survivors sit at lines 118, 137, 141, 189, 217, 225, 229, 232, 234 and
  236. They cover:
  - the redaction pair sizing;
  - the 256-byte snippet boundary;
  - the Retry-After digit test;
  - transport-before-code precedence;
  - verified-code-before-status precedence.

  Add a table test per boundary that fails under each mutant.
- [ ] **G4 (Area 6): StreamChat may not classify SSE error frames.**
  The `harden:adapter/openrouter/provider.go` survivors are at lines 239–240: negating
  `p.classify != nil && len(frame.Choices) == 0 && frame.Usage == nil` survives, and so does the scanner buffer at line 209.
  A structured LunaRoute error frame mid-stream may produce empty content instead of a classified failure. Prove it is
  caught, or fix it.
- [ ] **G5 (Area 2): diagnosis branches are not discriminated.**
  The `harden:adapter/provider/lunaroute_agent.go` survivors are at lines 83 and 85, and the
  `harden:domain/provider/lunaroute_diagnosis.go` survivors at lines 17 and 23. Classified-probe-error, local-probe-error
  and nil-original paths are not told apart by any assertion. Add tests on `Checked`, `Observation`, the error text and
  `errors.Is` for each branch.
- [ ] **G6 (Area 7): a 3xx listing status may count as success.**
  `harden:adapter/probe/lunaroute.go` survives at line 76 (`resp.StatusCode >= 300`). Add a 300 response at the fake
  gateway: it must classify as a failure, never as a listing.
- [ ] **G7 (Area 3): loader and save boundaries are untested.**
  The survivors are:
  - `harden:adapter/config/capabilities.go` (`len(...) > 0` guards at lines 186, 200 and 220);
  - `harden:domain/provider/profile_service.go` (line 316, the mixed-route agent-backend axis);
  - `harden:domain/provider/lunaroute_validate.go` (line 135, the `MaxOutputTokens > 0` boundary);
  - `harden:domain/provider/lunaroute_capability.go` (line 109, sort order).

  Add tests for: empty `tasks`, `backends` and `efforts` maps; mixed routes on two backends; `MaxOutputTokens` equal to the
  limit; and sorted ids.
- [ ] **G8 (Area 3): vendor-prefix consistency is under-tested.**
  The survivors are `harden:domain/modelidentity/family.go` (negations at lines 89, 98 and 110) and
  `harden:domain/modelidentity/version.go` (line 192, the lunaroute case).
  Add cases where a vendor prefix contradicts the id, where a bedrock qualifier contradicts the prefix, and where an alias
  hint conflicts. Add an id that only the non-lunaroute grammar accepts.
- [ ] **G10 (Areas 4, 5): executed-identity safety checks are untested.**
  The survivors are `harden:adapter/codex/lunaroute.go` (line 97, `safeLunaRouteIdentity`) and
  `harden:adapter/pi/model_probe.go` (lines 60 and 62). Reported model strings with quotes, whitespace, control characters,
  `lr_` text or more than 256 bytes must be rejected as evidence. Add a case per clause.
- [ ] **G11 (Areas 3, 7): text survivors need a decision.**
  The survivors `harden:adapter/mcp/tools_connection_models.go`, `harden:adapter/mcp/lunaroute_backend_hint.go` and
  `harden:domain/provider/model_discovery.go` are string-concatenation mutants in LLM-facing text. Pin the load-bearing
  phrases in tests (unknown is not a guarantee; codex or pi only; pinned only), or record each mutant as equivalent with a
  reason.

- [ ] **G12 (Area 1): MCP accepts an explicit empty key on an existing LunaRoute connection.**
  `adapter/mcp/tools_connection.go:connectionUpdateHandler` turns `api_key: ""` into nil; the repo preserves the old key
  and increments `ConfigRevision`, violating failure-lens check 1's empty-key refusal/no-row-change requirement.
  Reproduced through `cmd/arbitron/lunaroute_connection_wiring_test.go:lunaRouteMCP` in a temporary test overlay:
  create lunaroute with `lr_original`, update same provider with `api_key: ""` → success and revision 1 → 2.
  The temporary `TestArea1EmptyKeyRefused` failed (exit 1); no failing test or overlay remains in the worktree.
- [ ] **G13 (Area 1, high): explicitly supplied LunaRoute keys can still be paired with a foreign provider.**
  `domain/provider/lunaroute_connection.go:PrepareLunaRouteCreate` / `PrepareLunaRouteUpdate` pass through foreign-provider
  keys unchecked. G1 now requires a nonempty replacement, but resubmitting `lr_original` on switch-away is accepted.
  Real-root temporary `TestArea1ForeignProviderRejectsLunaRouteKey` failed (exit 1) for both openrouter create with
  `lr_mispaired` and lunaroute → openrouter update with `lr_original`; persisted provider became openrouter.
  Refuse `lr_` keys at foreign-provider writes with static secret-free guidance; test create, update and switch-away sinks.

## Review evidence

- **Area 1 / single:1/review2 — failure-lens refinement (no approval).**
  Baseline: the initial focused provider/sqlite/mcp/cmd tests passed before edits; no pre-existing area failure observed.
  Check 1: key-shape/storage-error coverage passes, but real-root empty-key reproduction violates no-row-change (G12).
  Checks 2–3: `adapter/sqlite/connection_repo_lunaroute_test.go:TestConnectionRepo_LunaRouteConcurrentSwitchDoesNotLeakKeyToForeignProvider` catches a missing foreign-provider CAS; `TestConnectionRepo_SwitchAwayFromLunaRouteRequiresNewKey` covers nil/empty, success, seal/SQL failure, cancellation and deadline without partial writes. Both defect tests were red before the fix; G13 remains for explicit lr_ resubmission.
  Checks 4–5: `domain/provider/lunaroute_endpoint_test.go:TestNormalizeLunaRouteBaseURL_RejectsOtherDestinations` includes `%2F`; removing the whitespace scan makes `TestValidLunaRouteKey` fail for `lr_ab cd`; inverting the generated conditional-query error guard makes `TestConnectionRepo_LunaRoutePersistenceErrors/conditional_SQL` fail (both temporary mutations exit 1).
  Folded items: canonicalization and stored-root/secret-free response tests pass; new sentinel is consumed by production `adapter/mcp/tools_connection.go:connErrorResult`, whose wrapped-secret test passes. `rg -n 'PrepareLunaRouteUpdate|ErrLunaRouteDestinationKeyRequired' . --glob '*.go' --glob '!**/*_test.go'` confirms real stored.Provider input and error consumer.
  Verification (exit 0 in all four packages): `go test -timeout 30m ./domain/provider ./adapter/sqlite ./adapter/mcp ./cmd/arbitron -run '^Test(ValidLunaRouteKey|PrepareLunaRoute.*|NormalizeLunaRouteBaseURL.*|IsLunaRoute|Canonicalize.*|ConnectionRepo_.*|ConnectionLunaRoute.*|Connection(Create|Update|Get|List|Delete)_.*|ConnErrorResult_.*|LunaRouteConnection.*)$' -count=1`.
  Recommend: no approval for single:1/review2 — G12 accepts an explicit empty key and writes a revision; G13 accepts lr_ keys on foreign providers, both reproduced through real MCP/SQLite.

- **Area 1 / single:1/review1 — approved independently against refinement 3f59d42.**
  Check 1: `cmd/arbitron/main.go:initDomain` builds SQLite/AES-GCM → ConnectionService → `domainStack.mcpDeps`; `adapter/sqlite/connection_repo.go:Create` prepares before Seal, and `ConnectionRepo.Update` prepares after metadata read; `cmd/arbitron/lunaroute_connection_wiring_test.go:TestLunaRouteConnectionWriteWiring` reaches stored rows and revealed keys through real MCP.
  Check 2: `adapter/sqlite/connection_repo.go:Update` is the sole production `PrepareLunaRouteUpdate` caller and passes `stored.Provider`; `adapter/sqlite/connection_repo_lunaroute_test.go:TestConnectionRepo_SwitchToLunaRouteRequiresNewKey` verifies the foreign-provider source and refusal/success sinks.
  Check 3: `adapter/sqlite/connection_repo_lunaroute_test.go:TestConnectionRepo_LunaRouteAcceptedSpellingsPersistHostedRoot` passes all nine spelling classes for Create and Update, asserting canonical provider/root through fresh Get reads.
  Check 4: `adapter/mcp/tools_connection.go:connErrorResult` maps all three write sentinels to actionable guidance; `adapter/mcp/tools_connection_lunaroute_test.go:TestConnErrorResult_LunaRouteSentinels` proves wrapped errors cannot echo submitted secrets.
  Check 5: repository-wide production Go/SQL searches found no connection-table writers outside `adapter/sqlite/connection_repo.go:Create` and `ConnectionRepo.Update` calling generated queries; `cmd/arbitron/connection.go:newConnectionCmd` exposes validation only, and seeds add no connection writer.
  Folded items: `domain/provider/canonical.go:Canonicalize` trims/lowercases without a LunaRoute alias; `adapter/mcp/tools_connection.go:RegisterConnectionTools` documents the provider/root/lr_ key; `metaOf`, `connectionGetHandler` and `connectionListHandler` emit stored metadata without a key field. A temporary test overlay exercised both reads through the real root successfully.
  Happy/success/failure/error/edge coverage uses real SQLite/AES-GCM in `adapter/sqlite/connection_repo_lunaroute_test.go:TestConnectionRepo_LunaRouteCreateNormalizesAndSeals`, `TestConnectionRepo_LunaRouteUpdateRules`, `TestConnectionRepo_LunaRouteCreateRejectsForeignKeyAndEndpoint`, `TestConnectionRepo_LunaRoutePersistenceErrors` and the spelling table; fault fakes assert their key/SQL arguments.
  Verification: `go test -timeout 30m ./...` exited 0 (52 packages passed); uncached focused tests in sqlite/mcp/provider/cmd also exited 0. A temporary production overlay replacing `stored.Provider` with empty made `TestConnectionRepo_LunaRouteUpdateRules` fail (exit 1), proving that input is load-bearing; repository source stayed intact.
  Failure-lens gaps G1 and G9 remain open: `domain/provider/lunaroute_connection.go:PrepareLunaRouteUpdate` still permits switching away while retaining a key; key-shape/query-error mutation coverage belongs to Review 2 and does not withhold this wiring approval.

## Closed

- **G1 (Area 1) closed:** `domain/provider/lunaroute_connection.go:PrepareLunaRouteUpdate` requires a replacement on switch-away; `adapter/sqlite/connection_repo.go:Update` guards every retained-key write; red/green repo and real-root MCP tests prove refusal and concurrent-switch safety.
- **G9 (Area 1) closed:** embedded-space coverage added to key-shape/create/update tests; temporary whitespace and generated-query guard mutations both fail, with `TestConnectionRepo_LunaRoutePersistenceErrors` exercising meta-read and conditional-SQL errors.

## Won't Fix

- **Dormant claude-code and opencode LunaRoute emitters. Escalated and decided by operator ruling R7 (2026-11-12).**
  `adapter/claudecli/lunaroute_env.go` and the LunaRoute branches of `adapter/opencode/*` stay in the tree but are refused
  at `domain/provider/lunaroute_validate.go:lunaRouteEnabledBackends`, and at `Factory.AgentProviderFor` and the
  interactive selector. Their emission is not refined. Area 2 verifies only that they are unreachable. Re-enabling one
  takes a gate change plus a live operator pass (LR-05/LR-06 are `deferred`).
  The survivors left unrefined are `harden:adapter/claudecli/streamjson.go`, `harden:adapter/claudecli/stream_agent_adapter.go`,
  `harden:adapter/opencode/config.go` and `harden:adapter/opencode/model_probe.go`.
- **Live verification and the full MCP acceptance matrix.** Ruling R6 makes these operator-run, and R7 descopes them:
  P4-T7's matrix, P2-T8's manual acceptance, the P3-T5 harness, and the forced-helper collectors from P2-T9 step 3 and
  P4-T1 steps 2–3. Agents hold no `lr_` key, so a skipped live test is expected and never counts as readiness.
- **Codex warns "Model metadata for glm-5.3 not found".** R7 records this as a known, non-fatal degradation.
- **Repository content outside the LunaRoute scope.** The squashed single-commit branch makes the whole repo look changed;
  see `RALPH.md` → Scope. Layout, dashboard, spawn, queue, teams and other features are not this branch's work.
