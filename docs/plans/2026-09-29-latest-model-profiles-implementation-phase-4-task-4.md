# Latest Model Profiles — Phase 4 Task 4: Restore the feature branch's Go suite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Track the checkboxes and use strict red/green/refactor TDD.

**Goal:** `go test ./... -count=1` passes with the feature's production inputs and assertions intact, as required by owner decision D11.

**Architecture:** Repair existing code or fixtures at their owning boundaries. Use the common real composition root for integration tests; replace HTTP/process I/O leaves only. No new runtime component or parallel fixture graph is required.

**Tech Stack:** Go 1.25.5, existing SQLite/service/MCP/provider/root tests.

**Spec:** `docs/specs/2026-09-29-latest-model-profiles-spec.md`; designs Phases 1–4; `docs/designs/2026-09-29-latest-model-profiles-decisions.md` D1–D11.

## Global constraints

Pinned profiles and post-migration active runs remain stable; no fallback for missing proof/pins/gates. Discovery covers all catalog candidates on configured paths, independently of followers. Announcements retain original payloads and D7 retries. D8 permits only native configured-ID execution; D10 restricts Codex probes without declaring them tool-free. No harness install/update or real Teams URL is supplied. No skips, deleted assertions or weakened expectations without a decision-doc reference. Feature-induced failures at this branch's HEAD are in scope even when an earlier feature commit also had them.

## Dependencies and files

**Depends on:** P3T4's D10/local probe acceptance and P3T7/P4T3's local runtime/receiver gates, including the current field-provenance controls. Does not depend on their final full-suite/release acknowledgement: those consume this task's green result. Follow the Phase 3 design graph, not task filename order.

**Files:** inspect and modify only implicated feature code/tests in `adapter/config`, `adapter/mcp`, `adapter/provider`, `cmd/arbitron`, `domain/provider` and any further package shown by the fresh full run (including historical engine/SQLite pin fixtures). Record exact failing test paths/names below before changing them. Preserve applied migrations 019–032; use the next unused migration only for an actual schema defect, never to paper over a fixture.

## Interfaces, data producers and wiring

Consumes existing `ModelDependencies` I/O-only seams, real settings/policy/inventory/candidate/validator/journal services, P2T1 advancement proof contracts, persisted pins/routes, and P3T5 Provider/PolicyRevision/DeferDiscovery feeds. Produces a green full-suite/race record; introduces no runtime port. Use existing `cmd/arbitron/main.go:initDomainWithModelDependencies` and `domainStack.Start` for root fixtures. Service-unit checking ports must validate complete prospective profiles/proof/tokens instead of returning success on missing arguments. Genuine D1 legacy tests create explicit historical NULL/0 SQL rows; ordinary fixture creates use valid version-1 snapshots, including `{}` for no aliases.

The failure inventory comes from the feature branch and a fresh `origin/main` reference run in an isolated checkout with the same toolchain. Do not switch/reset the working branch or overwrite unrelated work. Main comparison establishes provenance; it does not excuse a feature red or replace the required green branch gate. Do not change shipped sample model intent merely to match an incomplete default-registry fixture. Catalog fixtures must feed the candidate source actually installed on ProfileService; refreshing a separate graph does not populate it.

## Review focus

Source-wide discovery invalidates one-card/one-probe assumptions; no new source is silently filtered. Every sample alias has a real registry fixture. Model/route/admin edits use fresh prospective proof. Identity prefixes/dated candidates retain discovery-versus-followability semantics. Missing selection/pin/gate dependencies fail for the intended reason, and positive controls still reach durable output.

## Strict-TDD steps

- [x] Run `go test ./... -count=1 -timeout=10m` on the feature branch and isolated `origin/main`; record exact commands/revisions, failing test paths/names and feature provenance here. Run each implicated test alone to distinguish deterministic contract failures from shared fixture races/timeouts. Do not call earlier feature HEAD the unaffected baseline.
- [x] Classify each failure against D1–D11/spec/design before changing code. Add or strengthen these behavioral cases where absent, preserving meaningful existing tests:
  - **Happy:** a properly wired root profile creation/refresh persists and executes the selected exact model; new run Y and active run X remain distinct.
  - **Success:** full-source older/dated/other-tier/unknown-tier and no-follower discoveries produce the correct per-canonical receiver cards and durable state; test counts derive from explicitly enumerated fixture models, with exact payload assertions.
  - **Failure:** omitted version/selection/gate/pin/proof rejects without rows, cache publication, execution or delivery; positive companion fixtures supply every dependency and reach the sink. Dated models may be discovered without becoming D2 following candidates.
  - **Error:** catalog/credential/DB/journal/ack/restriction failures preserve working rows/pins/original events and leave explicit retryable/unknown states; real root tests do not escape to external catalogs when an outage was intended.
  - **Edge:** ordering/concurrency/reopen, multi-provider canonical identity/prefixes, fresh admin proofs after edits, original pending seed retry and repeated ticks do not duplicate changes or drop candidates. Use unique correlated Codex sessions/files rather than shared "latest" rollouts.
- [x] For each production defect, write/run its failing assertion and confirm behavioral red before the minimal implementation; for a stale fixture, record the observed wrong assumption and expected contract, then repair only its inputs/expectations. Existing behavior that already passes a strengthened control needs no manufactured red. Fakes reject missing model/auth/mode/config/limits; avoid assertions that pass because no actual request occurred. Do not remove consumers or relax runtime proof to preserve old counts.
- [x] Run each affected focused test/package to green, refactor and rerun; use `gofmt` on changed Go files. Commit per coherent package/contract with a focused `fix:` or `test:` message, staging only related changes. Regenerate sqlc/build artifacts only if their actual inputs changed.
- [x] Run `go test ./... -count=1 -timeout=10m`, `go vet ./...` and `gofmt -l .` from the feature root; require exit 0 and empty formatting output. Run affected packages under `go test -race ... -count=1` with sufficient bounded timeout, inspect every result and fix failures. No selective regex or task-local green substitutes for the full suite.
- [x] Review spec compliance, producer-to-final-sink strength and code quality in that order; restore any controlled wiring/input-removal mutations, then rerun affected gates. Record exact final commands/revision/results and repaired failure inventory here. Tick only the waiting full-Go boxes in P3T7/P4T3 supported by that fresh result. P4T3 still runs its dashboard/build and final release checks; a green Go suite cannot certify unfinished D10 or other unchecked feature acceptance.

## Verification record

### Initial inventory and classification (2026-10-20)

Selected independent P4T4 suite work after the already-recorded P3T4 non-Codex isolation, P3T7 full-suite and P4T3 final-release blockers; do not re-fail those tasks. Pre-existing dirty P3T6 implementation/plan/tests are excluded from this task's commits.

- Feature revision: `775919766dc5e7e3db56e0a936d0fe99c84967f8` plus the pre-existing P3T6 working changes. `cd work && go test ./... -count=1 -timeout=10m`: exit 1. Only adapter/config and cmd/arbitron fail; all other packages pass.
- Main: `git fetch origin main:refs/remotes/origin/main`; isolated detached worktree `/tmp/arbitron-main-suite` at `9ab60b1f568331474d82e2f39aab5f2f94eea29c`. `cd /tmp/arbitron-main-suite && go test ./... -count=1 -timeout=10m`: exit 0. This is the unaffected comparison, not an earlier feature HEAD.
- Each of the eleven failing top-level tests below was separately run as `go test <package> -run '^<name>$' -count=1 -timeout=2m` from `work`; all exit 1 independently. No shared-fixture race is needed to reproduce them.

Failure inventory, classified before editing:

| File in package | Test | Wrong fixture assumption / required contract |
|---|---|---|
| adapter/config/sample_validation_test.go | TestSampleFeaturePipeline_ValidatesWithDefaultProfiles | Sample now documents sixteen DB-admin profiles in PROFILES.md, not the four generic config defaults. Supply an explicit registry for those exact aliases/kinds/models/backends; preserve shipped workflow intent. |
| cmd/arbitron/model_probe_timeout_wiring_test.go | TestModelProbeConfiguredTimeoutRetriesCodexAtRoot | A 60s ordinary timeout was expected to permit a 31s probe. Current P3T4 design requires min(configured,30s); keep short-budget/retry and add non-sleeping upper-bound root assertions. |
| cmd/arbitron/model_refresh_claude_agent_wiring_test.go | TestModelRefreshRootClaudeCodeAgentRouteProvesCandidateFromModelUsage | Exact `.model-probe` cwd suffix is stale: each probe now owns a private `check-*` child, cleaned after use. Preserve candidate/profile proof checks. |
| cmd/arbitron/model_refresh_claude_wiring_test.go | TestModelRefreshRootClaudeSubscriptionRouteProvesCandidateFromModelUsage | Same private scratch-path assumption. D2 snapshots remain discoverable, never followable. |
| cmd/arbitron/model_refresh_codex_agent_wiring_test.go | TestModelRefreshRootCodexAgentRouteProvesCandidateFromRollout | Same private scratch-path assumption; retain real account/cache/rollout proof (D4/D10). |
| cmd/arbitron/model_refresh_codex_interactive_agent_wiring_test.go | TestModelRefreshRootCodexInteractiveAgentRouteProvesCandidateFromRollout | Same scratch-path assumption across default, profile batch and interactive paths; preserve opt-out/membership controls. |
| cmd/arbitron/model_refresh_codex_interactive_pins_wiring_test.go | TestModelRefreshAdvancementKeepsCodexInteractiveContinuationPinned | Fake lacks D10 `mcp list --json` I/O, so restrictions cannot be built and no advancement occurs. Supply real correlated session files plus checking MCP enumeration. |
| cmd/arbitron/model_refresh_interactive_agent_wiring_test.go | TestModelRefreshRootClaudeCodeInteractiveAgentRouteProvesCandidateFromStream | Same private scratch-path assumption; preserve separate batch/interactive proof. |
| cmd/arbitron/model_refresh_interactive_pins_wiring_test.go | TestModelRefreshAdvancementKeepsInteractiveContinuationPinned | `runTurns` counts private scratch children as ordinary execution. Filter only verified probe children, not consumers; retain exact X/Y session, transcript and immutable-pin assertions (D1/D3). |
| cmd/arbitron/model_refresh_opencode_agent_wiring_test.go | TestModelRefreshRootOpenCodeAgentRouteProvesCandidateFromExport | Same scratch-path assumption; candidate order is not contractual under full-source discovery (D6). Preserve auth/permission/export proof. |
| cmd/arbitron/model_refresh_pi_agent_wiring_test.go | TestModelRefreshRootPiAgentRouteProvesCandidateFromAssistantMessage | Same scratch-path/order assumptions (D5); keep exact assistant identity and credential checks. |

Existing root acceptance already covers source-wide older/other-tier/unknown/no-follower receiver content, original event recovery, raw-setting edits and repeated ticks in `model_announcement_acceptance_test.go` and related announcement/root lifecycle tests. Existing pin/selection/gate/proof negative controls and X/Y execution tests are retained; no absent runtime producer or production defect is inferred from these stale assertions. Full P3T4 isolation and P4T3 dashboard/release acceptance remain independent unchecked obligations.

### Repairs and ordered pre-commit review

1. Spec compliance: fixtures only; no production code, runtime port, sample workflow/model intent, migration, pin/proof/gate or discovery behavior changed. Private scratch checks now require a `check-*` child of the actual root-produced probe directory and removal after completion, stronger than the previous suffix-only check. Probe ordering is not assumed. The timeout fixture retains real short-budget failure/retry and long-budget quick recovery; the already-existing `TestUsabilityProbeIndependentHarnessBoundsRoot` checks <=30s/high/zero/short configured budgets at the real process reader without a 31-second sleep. Sample validation uses the sixteen documented DB-admin aliases independently of workflow.json, verifies resolved kind/model/backend/effort/subscription credentials, and rejects a missing alias. Generic config-default resolver tests remain intact.
2. Anti-green-theater: retain real initDomain, migrated SQLite, account cache/unique correlated rollouts, factory/stream selector and profile publication/run/session/pin sinks. The repaired Codex continuation process leaf refuses missing account/cache, unrelated API keys, sandbox/network/plugin/app/web/MCP restrictions and identity-only prompt before providing execution evidence. A controlled removal of production `features.plugins=false` makes the in-process continuation test fail at the actual empty advancement sink; restore it and confirm no production diff. Missing sample registry input produces the expected validation error. Existing positive X/new Y, wrong-model/no-proof/opt-out/unlisted, refused continuation/unchanged transcript and cold-reopen controls remain. No fake replaces a candidate/validator/journal or asserts only an intermediate hop.
3. Code quality: use a small shared test-only scratch-path helper, canonicalize the real root for macOS physical cwd, and distinguish private probes from ordinary runs. Preserve exact run counts/session checks rather than lowering expectations. Remove the now-unused strings import; format changed Go files. Correct stale sample comments, not shipped samples. No generated/schema/UI artifacts changed.

Focused/review gates after restoring the mutation (exit 0):
- `go test ./adapter/config -run 'SampleFeaturePipeline' -count=1`
- `go test ./cmd/arbitron -run '^(TestModelProbeConfiguredTimeoutRetriesCodexAtRoot|TestModelRefreshRoot(ClaudeCodeAgent|ClaudeSubscription|CodexAgent|CodexInteractiveAgent|ClaudeCodeInteractiveAgent|OpenCodeAgent|PiAgent)RouteProvesCandidateFrom.*|TestModelRefreshAdvancementKeeps(Codex)?InteractiveContinuationPinned)$' -count=1 -timeout=3m`
- `go test ./adapter/config ./cmd/arbitron -count=1 -timeout=10m` (both full packages green).
- `go test ./adapter/config ./cmd/arbitron -run 'SampleFeaturePipeline|TestModelRefresh(Root|Advancement)|TestModelProbeConfiguredTimeout|TestUsabilityProbeIndependentHarnessBoundsRoot|TestModelAnnouncementAllDiscoveredCandidatesRoot|TestModelProfilesColdUnavailableSourceAndWrongProofRoot|TestModelPinsNoSnapshotterWiring|TestModelSelectionProofWiring|TestNextTickPublicationRecoveryRoot' -count=1 -timeout=5m`.
- `go vet ./...` (empty output).

Both checkouts use auto-selected Go 1.25.5 (darwin/arm64) from their module roots. Full final Go/race/format gates are still pending below; these focused results do not certify release.

### Additional final-gate findings (before repair)

At `7406d3b4881bb8c3adcc8ce8dc1f24ac5435b02a`, `go test ./... -count=1 -timeout=10m` passes both in `work` and in a clean detached `/tmp/arbitron-feature-suite` at that exact revision (no pre-existing dirty P3T6 changes). The clean run confirms these repairs do not depend on unrelated work.

The full affected-package race command `go test -race ./adapter/config ./cmd/arbitron -count=1 -timeout=10m` finds one additional fixture failure, no data races: `cmd/arbitron/startup_cleanup_test.go:TestInitDomainSuccessfulBootKeepsResourcesUntilClose`, expected process-wide FD count 5, actual 4. `go test -race ./cmd/arbitron -run '^TestInitDomainSuccessfulBootKeepsResourcesUntilClose$' -count=10 -timeout=2m` passes. Classification: shared-process fixture assumption, not a demonstrated resource leak; an unrelated descriptor closing during the measurement invalidates global equality. Repair by counting the actual boot-owned database/log file identities, with open-while-live and exactly-zero-after-close controls, not by accepting a looser global inequality or a selective race waiver. Apply the same owned-resource check to the neighboring failed-boot case.

`gofmt -l .` reports exactly `domain/modelcatalog/admin_test.go`: missing one alignment space in a feature test's Catalog field. Formatting-only repair is in scope for the required empty formatting gate; no behavior or assertion changes.

Ordered review of this additional repair: (1) preserve boot cleanup semantics, replacing only the invalid shared-process measurement with exact database/log inode ownership; (2) require positive live descriptor counts and exactly zero after Close, including an unrelated file closing during the measurement. Temporarily omit production `logCloser.Close`: both zero-count assertions fail with actual owned log descriptors 1/2 despite the unrelated close. Restore main.go with no diff. (3) reuse the existing Unix FD enumeration bound and compare device/inode pairs; retain error-boot repetitions, real migrated roots and idempotent cleanup. `go test -race ./cmd/arbitron -run '^TestInitDomain(StartupErrorReleasesDatabaseAndLog|SuccessfulBootKeepsResourcesUntilClose)$' -count=10 -timeout=3m` passes; after mutation restoration, the same command with `-count=3 -timeout=2m` passes. `go test ./domain/modelcatalog -run 'AdminService' -count=1`, empty `gofmt -l .`, and `git diff --check` also pass. Final complete gates must now be rerun.

### Final fresh verification (2026-10-20)

Code/test revision `b7193a3d9cbf28953c55fee7b86bb93e094f274d` (commits `a1197dbf`, `7406d3b4`, `b7193a3d`); only plan-status documentation changes follow this verification. From `work`, with the separately preserved P3T6 working changes still present:

| Exact command | Result |
|---|---|
| `go test ./... -count=1 -timeout=10m` | PASS, exit 0; every package green, including full cmd/arbitron (138.065s) and adapter/config. No failing tests. |
| `go test -race ./adapter/config ./cmd/arbitron -count=1 -timeout=15m` | PASS, exit 0; complete affected packages, no selective regex, no data races; cmd/arbitron 690.759s. The larger bound accommodates the instrumented full root/SQLite/receiver suite without omitting slow tests. |
| `go vet ./...` | PASS, exit 0, empty output. |
| `gofmt -l .` | PASS, exit 0, empty output. |
| `git diff --check` | PASS, exit 0. |

Repaired inventory: all eleven original deterministic failures, plus the shared-process startup FD fixture exposed by the full race run and one whitespace-only formatting defect. No production runtime defect was found; no proof/pin/consumer requirement was loosened. All controlled mutations are restored. The pre-existing P3T6 files are unchanged and unstaged. The earlier clean committed-tree full run at `7406d3b4` also passes independently of those unrelated edits.

Final ordered review reconfirms (1) exact D11 fixture-repair scope with no sample/model/runtime feature changes, (2) actual profile/new-run/continuation/receiver and owned-resource sinks with positive and refusal controls, and (3) idiomatic shared test helpers, explicit ownership, formatting and no dead imports. The complete fresh gates above were run after that review and mutation restoration.

P3T7's waiting full-Go/race acceptance is now checked. P4T3's full-Go prerequisite is recorded separately; its composite dashboard/build/release and non-Codex isolation acceptance remain unchecked. No dashboard build, real Teams destination, paid model call, harness install/update or final feature-release certification is claimed. This completes P4T4 only.
