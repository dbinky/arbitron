# Latest Model Profiles — Phase 3 Task 3: Complete execution path inventory and revisions Implementation Plan

> **Alignment baseline (2026-10-19):** D1–D11 govern this task. D10 permits restricted Codex probes; D11 assigns feature-induced suite repairs to P4T4. Earlier execution evidence is historical, not a current dependency inventory or permission to skip unfinished work. The current follow-through sections supersede earlier interface sketches. The existing probe/router (`adapter/probe/model_usability.go`), discovery/selection/recovery (`domain/modelcatalog/{refresh,candidate_source,probe_schedule,loop}.go`) and root assembly (`cmd/arbitron/model_refresh.go:newModelRefresh`) are extension points. Implement unchecked local obligations without waiting for downstream integration acceptance; P3T7 owns cross-phase execution sinks and P4T3 owns receiver/release sinks. See the Phase 3 design’s current closure map.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this task. Track the checkboxes and use strict red/green/refactor TDD.

**Goal:** Complete execution path inventory and revisions.

**Architecture:** Domain ports own contracts; adapters produce real inputs; the shared `cmd/arbitron/main.go:initDomain` composition root connects them. Execution order follows the dependency graph, not numeric phase order.

**Tech Stack:** Go 1.25.5, SQLite/sqlc, MCP/HTTP; React/Vitest for dashboard boundaries.

**Spec:** `docs/specs/2026-09-29-latest-model-profiles-spec.md`; `docs/designs/2026-09-29-latest-model-profiles-design-phase-3.md`. Read the Phase 3 execution/coverage map and this task's upstream Interfaces before coding.

## Global constraints

- Refresh cadence is four hours; following provider/tier stays fixed and only eligible newer usable versions advance.
- New runs use published updates; post-migration active/retried/resumed runs retain specific starting versions. Pre-migration NULL-pin runs keep live resolution until completion (D1).
- Pinned profiles and last known working models survive discovery failure; no harness installation/update.
- Announcement webhook defaults empty; do not supply it during this work. Each newly usable canonical model gets at least one announcement when configured, including no-followers discoveries; rare duplicates after ambiguous sends are accepted (D7).
- No mandatory Claude/Sonnet normalizer. Unknown evidence is explicit, never fabricated success.

## Review focus

Required tests below cover missing production inputs, restart/concurrency, partial failure, authorization/secret handling and exact final sinks. A fake must reject missing load-bearing fields; unsupported external guarantees remain blockers.

## Dependencies and files

**Depends on:** P3T8 policy source/revision; P2T2 (`docs/plans/2026-09-29-latest-model-profiles-implementation-phase-2-task-2.md`), including P2T1; existing workflow registry/repository, connections and factory routes.

**Files:** Create `adapter/crypto/installation_secret.go`, `adapter/crypto/installation_secret_test.go`, `domain/modelcatalog/paths.go`, `domain/provider/execution_gate.go`, `adapter/provider/path_inventory.go`, tests, `adapter/sqlite/migrations/023_execution_revisions.{up,down}.sql`. Create `domain/instance/execution_snapshot.go`; modify `adapter/sqlite/instance_repo.go`, `adapter/sqlite/queries/instances.sql`, `domain/provider/connection.go`, `adapter/sqlite/queries/connections.sql` and affected generated conversions; modify `domain/provider/advance.go`, `domain/provider/connection_service.go`, `profile_service.go`, `domain/workflow/service.go`, `adapter/sqlite/connection_repo.go`, `cmd/arbitron/main.go`.

## Interfaces (owned here unless explicitly consumed)

Produce `ExecutionPath{Key,Kind,Mode,Backend,ConnectionRef,RouteProvider,ConfigRevision string; Aliases []string; Binding provider.ResolvedBinding}` (Binding internal only). `PathSnapshot{Revision string; ExecutionRevision provider.ExecutionRevision; Paths []ExecutionPath}`; `ExecutionPathInventory` has `Snapshot(context.Context) (PathSnapshot,error)` and `PathsForProfile(context.Context,provider.Profile) (PathSnapshot,error)` to construct prospective default plus saved alias-override paths before a new profile exists. `instance.ExecutionSnapshot{InstanceID uuid.UUID; WorkflowSnapshot map[string]any}` and trusted `ExecutionSnapshotSource.ListExecutionSnapshots(context.Context) ([]instance.ExecutionSnapshot,error)` are defined here and implemented by InstanceRepo; include model-consuming nonterminal and retryable failed instances. `Connection.ConfigRevision int64` and atomic increments on every connection/key update are owned here, including repo conversion and query coverage. Existing `(*provider.ExecutionGate).WithCurrent(context.Context,string,func(context.Context) error) error` checks revision and holds an update read lease during callback; mutators take write lease. Add `ExecutionGate.WithMutation(context.Context,func(context.Context) error) error` and inject shared gate into connection/profile/workflow services. Automatic CAS uses adapter transaction helper within the existing read lease, so it must not recursively acquire the mutation lock. Persistent revision checks inside CAS protect against other server processes; external file/account edits are fingerprint-rechecked. `ExecutionPathInventory.WithBinding(context.Context,ExecutionPath,string) (provider.ResolvedBinding,error)` supplies candidate model for the same path. Produce `adapter/crypto.LoadOrCreateInstallationSecret(path string, required bool) ([]byte,error)` for keyed revision fingerprints: a cryptographically random 32-byte secret persisted at `cfg.Database.Path + ".model-state.key"`, mode 0600, alongside file-backed DB. Atomically create once under a bootstrap lock/temp+sync+atomic publication; concurrent processes must return the same stored secret. Migration 023 owns `model_state_meta.secret_initialized`; root reads this persisted flag to populate required, and marks it only after successful key publication. If required=true and file is missing, fail; always reject corrupt secret rather than rotate and invalidate encrypted attempts. DB/key backup must retain both; never rewrite an existing valid key. For in-memory test DB require explicit secret injection. initDomain preserves this existing independent model-state secret even on subscription-only installs where `connCrypto` is nil; existing modelStateCrypto bootstrap remains intact. Path fingerprints consume the raw secret internally through an explicit keyed-fingerprint closure, never public DTOs. Reuse standard crypto/rand and UUID/time producers; never include secret in logs/public DTOs. No secret or RevealKey closure in public catalog DTOs.

## Data producers and composition-root wiring

ConnectionStore.List/Get/Reveal supplies configured endpoints/provider/extras/key at lazy execution; ProfileService.ListInternal supplies kind/model/backend/tools/axes; actual deployment cfg and factory/interactive selector supply routes. `workflow.Registry.List` enumerates private/shared owner scopes, Repository.Load reads node override/default config; include unregistered filesystem workflows by extending existing local registry/backfill enumeration at initDomain, rather than declaring registry-only coverage complete. Include configured connections/harnesses with no following profiles, pinned profiles, and active Instance.WorkflowSnapshot routes. Add trusted `InstanceRepo.ListExecutionSnapshots(context.Context)` for nonterminal plus retryable failed runs; no public authorization bypass.

## Strict-TDD steps

- [x] Write the following named behavioral tests in the listed test files. Use real migrated SQLite for persistence and root assembly for wiring; introduce only I/O-leaf test substitutions.

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

- [x] Route/mode slice: add behavioral tests for no-profile deployment paths, batch/interactive identities and actual selector support, policy-independent coherent-profile VersionRequests, saved/active overrides, routing-provider versus follow-provider, required-source faults, incompatible node kinds and prospective/current revision separation. Add real-root empty-profile/default-Codex, private unregistered interactive workflow, durable stale-write rejection, opt-in selector and policy-outage tests.

> Decisions D1–D7 are resolved in `docs/designs/2026-09-29-latest-model-profiles-decisions.md`. Follow the concrete contracts in this task and implement its named prerequisites first; unchecked work remains unimplemented.

- [x] Independent portion: write connection revision, secret bootstrap/concurrent-process and real `initDomain` reload/fail-closed tests.

- Happy: `TestInventoryActualRuntimeRoutes` loads private/shared workflow overrides and produces the same routes as actual executors/selector.
- Success: connection and subscription paths exist without followers; pinned profiles and active snapshot-only overrides are included; Codex subscription does not reveal unrelated connection key.
- Failure: unsupported interactive route or incompatible profile path is unknown and inhibits affected advancement.
- Error: registry/repository/instance/fingerprint read fault reports incomplete inventory, never zero-path eligibility.
- Edge: node/profile/deployment precedence; archived/renamed/deleted workflow still in active snapshot; multi-user/local modes; credential/account/binary edits invalidate evidence and block stale CAS under the shared gate; subscription-only boot creates/reloads the installation secret, concurrent boot gets identical bytes, corrupt key or write failure fails explicitly.
- Input guard: P3T7 root test edits an actual connection/workflow then asserts stale candidate cannot reach durable profile; every fake inspects scope, owner, alias and configuration revision.

Assertion sketch for the named happy path (variables are observed fixture outputs; substitute fixture-backed versions where shown):

```go
assert.Contains(t, inventoriedBackends, nodeOverride)
assert.Error(t, staleRevisionCommitErr)
```

- [x] Run the focused gate below before implementation. Confirm each new assertion fails for the intended absent behavior (a compile failure may establish a missing API, but rerun to observe behavioral red after the API exists).

- [x] Route/mode slice red: focused gate first fails on missing Mode/VersionRequests APIs; after stubs, default paths are empty, modes collapse and routing requests fail. Real-root tests fail behaviorally for the same missing inputs. Additional kind-guard, spawn-preparer and missing-deployment-backend assertions each fail before their minimal fixes.
- [x] Independent portion: run the new tests red (missing APIs, then behavioral failures for zero revisions, absent metadata and missing root key bootstrap).

- [x] Implement the exact contracts above in the listed files:

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

- [x] Route/mode slice implementation: share route bindings and consumer enumeration across proof inventory and VersionRequests; preserve actual connection provider and coherent model, deployment/node/profile precedence, explicit launch mode and kind mismatches. WithBinding uses the actual factory/interactive selector; unsupported affected routes remain visible without blocking unrelated paths. Include the required deployment harness without profiles and prevent Codex from revealing an unrelated connection key. initDomain injects the exact execution selectors, real repositories/config/HMAC/harness inputs, and keeps the inventory as the shared gate's revision source.

Match runtime node > profile > deployment backend precedence for each supported chat/agent/interactive route. Execution snapshot revision includes monotonic DB connection credential/config revision, profile revisions, workflow content hash, policy revision, deployment cfg, binary fingerprint and account/config-file fingerprint; use keyed digest for sensitive material, never expose secrets. Migration 023 adds connection revisions; coordinate edits/seeds through one shared gate, and recheck external binary/account/file fingerprints immediately before CAS. Unknown/unreadable global inventory is not empty success. P3T7 constructs inventory/gate from real repos/config/factory inputs; connect admin mutation hooks. Probing uses prospective/default paths for unreferenced profiles too.

- [x] Independent portion: implement migration 023, atomic connection update increments/conversions, locked random-key publication, persisted initialization checks, explicit in-memory test injection and subscription-only root AES-GCM construction. The later inventory fingerprint consumer remains owned by this task, not claimed implemented by this independent slice. D7 removes any receiver-reconciliation dependency.

- [x] Run the focused gate again; require exit 0 and every named test green. Refactor only while those tests remain green; format Go and regenerate sqlc for query/schema changes. Run dependent root tests when an interface changes.

- [x] Route/mode slice green: focused inventory/revision/gate tests, dependent root inventory/secret/pin/version tests and inventory/revision/gate race tests pass. Full adapter/provider and domain/provider tests pass. No query/schema or applied-migration changes. Final downstream acceptance remains blocked above.

- [x] Independent portion: focused secret/revision gates and race tests pass; regenerate affected sqlc queries/schema types and run all dependent root/provider/SQLite/workflow tests.

**Focused gate:** `go test ./domain/provider ./adapter/provider ./adapter/sqlite ./domain/workflow -run "Inventory|ExecutionRevision|ExecutionGate" -count=1`. Commands separated by semicolons here are separate gates: run each and inspect every exit status. For dashboard changes run `make ui` from the repository root so embedded assets match source.

- [x] Review diff for scope, production wiring and forbidden fake shortcuts. Commit this task with `git add <listed changed files>` and a focused `feat:` commit only after verification; do not stage unrelated changes.

- [x] Route/mode slice review: (1) spec first: only inventory/route/mode contracts and existing root composition; no new operator flag, policy grammar, refresh, or guessed SQL proof. Added missing incompatible-kind/default-input guards. (2) anti-green-theater second: root config supplies a nonempty Codex backend with zero profiles; actual private filesystem input invalidates a gate-guarded durable profile update; actual opt-in map changes supported modes; policy outage still yields exact model/backend requests. Unit ports cannot replace routing provider with follow target or reload the coherent profile. (3) quality third: reuse the factory/selector, shared binding/enumeration and engine mismatch sentinel, owned nested binding data, deterministic requests and mode constants; gofmt and rerun focused/root/race gates before committing this slice only.

- [x] Independent portion: complete ordered spec-compliance, anti-green-theater and code-quality reviews; commit the verified independent slice only.

## Execution evidence (independent portion only)

- Red: `go test ./adapter/crypto ./cmd/arbitron -run 'InstallationSecret|ExecutionRevision' -count=1` and the specified focused gate first failed on missing APIs. With API shapes present, they failed behaviorally on missing key bootstrap/metadata and zero persisted revisions. No inventory/gate behavior is claimed by these tests.
- Green: both focused gates above pass. `go test -race ./adapter/crypto ./adapter/sqlite ./cmd/arbitron -run 'InstallationSecret|ExecutionRevision' -count=1` passes. Full dependent packages also pass: `go test ./adapter/crypto ./domain/provider ./adapter/provider ./adapter/sqlite ./domain/workflow ./cmd/arbitron -count=1`.
- Regenerated connection/model-state queries with sqlc v1.31.1 in an isolated generation config; copied only owned generated queries and schema types. Whole-project generation encounters the existing hand-maintained `sqlc.slice` query in `instances.sql`; no unrelated generated queries were overwritten. Crypto test binaries cross-compile for Linux and Windows; OS runtime verification was on macOS.
- Review stage 1 (spec): only migration 023 connection revisions and independent model-state bootstrap; no invented identity, eligibility, inventory completeness, flags, webhook, or harness changes. Required full-task dependency implementations and final CAS sink remain pending above.
- Review stage 2 (anti-green-theater): real `initDomain` subscription boot writes key and initialized flag; reopening authenticates ciphertext with that same key; missing required key fails without regeneration. Real root admin mutation reaches durable connection revision and rotated credential, verified after restart. SQLite concurrent updates reject lost increments; separate processes converge on the exact persisted key. Migration rollback/reapply preserves credentials and initializes legacy revisions. Missing root wiring or empty/hardcoded key input breaks these assertions.
- Review stage 3 (quality): reuse AES-GCM, crypto/rand and sqlc; stable OS-released bootstrap lease, 0600 temporary key, file sync and atomic durable publication, bounded secret reads and no key material in errors/logs. Format Go and check diff. Future P3T3 inventory fingerprints consume this secret through explicit root wiring; do not introduce a receiver-reconciliation consumer after D7.

## Provenance / release gate (resolved 2026-10-01)

Full-task P2T2/P3T1 implementations and durable advancement/root sink dependencies remain pending; this is not a completed inventory or ExecutionGate implementation.

## Revision producers and lock discipline

P3T8's persisted policy revision joins the execution snapshot revision so a policy edit between probe/selection and CAS conflicts. `ExecutionGate.WithRead(context.Context,func(context.Context) error) error` holds the shared read lease without an expected revision for P1T3 snapshot creation; `WithCurrent` also recomputes/checks the inventory fingerprint before its callback. Fingerprint callbacks are constructed at P3T7 from real DB revisions, loaded workflow content, deployment config, resolved executable, account/config files (including Codex models_cache and rollout root) and PolicySource, using the P3T3 secret. Unreadable required inputs yield incomplete/error, never a fixed empty digest. Version recognition itself does not depend on the gate.

Selection/fetch/probes happen outside mutation write leases. P2T2 performs candidate initialization before taking a write lease, then rechecks expected revision/path fingerprint and performs the small validated persistence operation under the lease; do not hold a write lease while invoking a candidate refresh that takes a read lease. Seed validation follows the same rule. WithCurrent callbacks use `commitAdvanceTx` directly, never public mutation methods. Workflow rename/delete/update and connection/profile create/update/delete/seed hooks all use the shared gate; root tests exercise actual admin services, not synthetic revision setters. For multiple processes, P2T3/P3T5 also validate the captured DB/policy revisions inside the commit transaction. File-backed workflow hashes are external fingerprint inputs, re-read immediately before commit; do not claim SQL validates a field with no DB producer; a stale or unreadable fingerprint prevents advancement.

## Transaction revision token

Populate P2T1's `provider.AdvanceRequest.ExecutionRevision` (in `domain/provider/advance.go`) using the existing execution_gate.go type `{ID string; PolicyRevision int64; Connections map[string]int64; WorkflowHashes map[string]string}`. Inventory produces an immutable copy of this token alongside PathSnapshot.Revision (the ID/digest); `PathSnapshot.ExecutionRevision` is never synthesized by a consumer. CandidateService feeds this token into prepared proposals; populate the same P2T1 EligibleCandidate.ExecutionRevision. P2T3 commitAdvanceTx and P3T5 CommitCycle validate token policy/connection revisions within the SQL transaction and each expected profile's complete CAS fields. The cycle validates shared persistent revisions once at transaction entry; earlier successful proposals in the same cycle cannot invalidate later proposals by their own profile revision increments. External workflow/binary/account files are fingerprint-rechecked under WithCurrent immediately before transaction entry. Profile revisions are compared per Expected Profile, not bypassed because a global token matches. Nil/empty ID, omitted required connection revisions or a policy mismatch blocks advancement.

## Keyed fingerprint composition producer

The current `cmd/arbitron/model_state.go:initModelStateCrypto` returns only Crypto; it does not expose a fingerprint key. This task adds `initModelState(ctx,db,databasePath,injectedSecret) (provider.Crypto,func([]byte) string,error)` using the same validated persisted key read/bootstrap, with HMAC-SHA256 and domain-separated fingerprint input. Preserve initModelStateCrypto as a compatibility wrapper if tests call it. main.go stores/injects the fingerprint closure into inventory; never export raw key bytes or assume Crypto.Seal is a deterministic fingerprint. Add tests proving the same DB/key after restart gives the same non-empty fingerprint, different config input changes it, and subscription-only actual root inventory consumes it. Add model_state.go and model_state_secret_wiring_test.go to this task's modification list; existing key corruption/missing-required-file behavior remains fail-closed.

Keep two revision scopes distinct: ExecutionPath.ConfigRevision identifies actual routing/auth/backend/axes/tool/binary/account configuration and excludes the candidate Model and membership-only policy edits; UsabilityEvidence is keyed by that configuration plus model identity. PathSnapshot.Revision/ExecutionRevision additionally includes expected profile/policy/workflow state for safe selection/CAS. Advancing only Profile.Model/Revision or editing a tier pattern must not force a second probe of an already confirmed model on an otherwise unchanged execution path. Add tests for model-only/profile-revision and policy-only edits retaining confirmation, while rotated credentials or changed launch configuration require new proof.

## All-scope filesystem input producer

Define trusted `WorkflowScopeSource` with existing `ListScopes(context.Context) ([]workflow.Scope,error)` and `LoadManifests(context.Context,workflow.Scope) ([]modelcatalog.WorkflowManifest,error)` in paths.go and implement it as `adapter/filesystem/workflow_repo.go:WorkflowRepo.ListScopes`: enumerate configured cfg.Workspace.Path/shared and actual users/<owner UUID> directories, then combine with registry scopes, using the existing WorkflowRepo.LoadManifests for parsed definitions and raw bytes together. P3T7 injects this concrete source into inventory. Missing empty roots are valid; malformed/unreadable owner directories or unreadable manifests report incomplete inventory, not zero consumers. This finds a workflow absent from the registry and a private owner absent from the current public caller's catalog; no background service impersonates that caller. Add actual root filesystem tests for both cases, asserting an unsupported/stale override prevents its alias from advancing. Modify workflow_repo.go and its tests as part of this task. Preserve service authorization for every public workflow operation.

Already implemented independent files (installation_secret bootstrap, connection revisions, migrations 023/024 and their recorded tests) are preserved. Remaining work adds inventory/gates/fingerprint closure/scope source and corresponding consumers; do not recreate the applied migration or duplicate the existing key producer.

## Prospective revision and execution-mode contracts

Snapshot returns the current installation digest/token; PathsForProfile returns the digest/token of the world with the proposed profile overlaid and only that alias's routes. These revisions intentionally differ before create/edit. Automatic advancement uses Snapshot + WithCurrent. Admin/seed preparation uses PathsForProfile, then P3T6 RevalidateStart recomputes PathsForProfile under the existing mutation lease and compares its prospective digest/token and each required path/config against the prepared proof. Never pass a prospective digest to WithCurrent, which reads the unmodified installation. Persistent policy/connection checks still occur in P2T3's validated SQL transaction. Add success tests showing a fresh create/route edit commits despite the prospective digest differing from the saved digest, plus failure/error tests for intervening edits and unreadable proof inputs.

Mode is `batch` or `interactive`, produced from each actual workflow node type; unreferenced/default routes use batch. Include Mode in path identity and configuration and verify supported interactive routes against InteractiveStreamSelector, rather than infer support from a batch factory. Both modes for one alias must participate in advancement. Unsupported interactive routes remain explicit affected-path unknown; do not globally stop unrelated providers. P3T4 probes each supported launch mode and P1T4 tests actual continuation enforcement. Add inventory/root tests where batch Codex succeeds but an unsupported interactive route prevents that alias's advancement.

The inventory also includes a deployment-default harness path even with no profiles/workflows/connections. P3T7 feeds DefaultAgentBackend and the real configured launcher/factory settings; do not use a profile list as the only producer of subscription paths. Add `TestInventoryDefaultHarnessWithoutProfiles` and a P3T7 no-profile default-Codex probe -> durable usable-event sink. An installed binary alone is never usability evidence.

Reuse existing WorkflowScopeSource.LoadManifests and WorkflowManifest raw bytes; do not introduce a ListScopes-only competing interface. Existing inventory, filesystem source, ExecutionGate, HMAC bootstrap and mutations are extension points in this checkout, not fresh parallel implementations. Preserve migrations 020/023/024/025 and all existing pin/policy/advancement tests.

For P1T3 and P2T2 fixed-version validation, implement the route-only `VersionRequests(context.Context,provider.Profile) ([]modelidentity.VersionRequest,error)` inventory method named by P1T3. Reuse connection/backend/node precedence and all-scope route enumeration, with the supplied coherent profile overlay; it neither loads policy/catalog nor probes or takes a gate lock. P3T6 delegates EligibleCandidateSource.VersionRequests to it. Required route/source errors propagate; legitimate unsupported routes fail explicitly. Test missing/policy-fault catalog does not affect explicit-model requests, while missing connection metadata does. This keeps pin capture during a policy outage possible without weakening following eligibility.

## Route/mode verification evidence (2026-10-16)

- Required focused gate: `go test ./domain/provider ./adapter/provider ./adapter/sqlite ./domain/workflow -run 'Inventory|ExecutionRevision|ExecutionGate' -count=1`.
- Dependent root gate: `go test ./cmd/arbitron -run 'PathInventory|ExecutionRevision|InstallationSecret|ModelPins|Version' -count=1`.
- Race gate: `go test -race ./domain/provider ./adapter/provider ./adapter/sqlite ./domain/workflow ./cmd/arbitron -run 'Inventory|ExecutionRevision|ExecutionGate' -count=1`.
- Broader dependent run passes adapter/provider, domain/provider and domain/modelcatalog; cmd/arbitron still fails TestRetryResumeRerunAndSpawnPins because its old `vendor/model-4-1` fixture is rejected by the already-shipped fixed-version grammar. Reproduced that same failure against untouched HEAD in an isolated temporary worktree, then removed it. Do not weaken production version validation or expand this task to unrelated pin fixtures.
- Earlier blocked P1T3/P1T4 and P2T2/P2T3 work was not re-failed. P3T1/P3T2 final refresh acceptance also remains dependent on P3T5–P3T7; this pass implements only P3T3's independent producer work. Overall checkboxes stay open for the missing full selection/refresh sinks, not for an invented nil/empty production fallback.

## Current route and revision consumers

Keep both existing ports: global `VersionRequests(ctx,profile)` for prospective admin/following validation, and `VersionRequestsForWorkflow(ctx,profile,wf)` for trigger-only capture. NewModelSnapshotter consumes `provider.WorkflowVersionRequestSource`; nil wf is invalid. The latter reads the exact trigger workflow and coherent Profile only, without unrelated saved/active manifests or policy. Snapshot/PathsForProfile still include all real consumers for following advancement. P1T3 produces the D8 exception only from actual effective native routes, never Connection.Provider when a subscription backend ignores it.

P3T7 injects existing inventory/HMAC/mutation gates; P3T6 consumes current versus prospective revision tokens. Write red/green tests for trigger route isolation alongside global saved/active override constraints, missing inputs, stale policy/connection/account fingerprints and empty default-harness installations; verify durable stale-write rejection and unchanged/changed ConfigRevision behavior. Do not recreate already present inventory/gate/secret producers or wait on final discovery acceptance to finish local route tests.

### Effective harness configuration fingerprint producer

Extend `adapter/provider/harness_fingerprint.go:harnessInputs` and existing HarnessFileFingerprinter tests. Derive files/environment from the same resolved launch home and config builders as the real harness: Codex HOME/CODEX_HOME `auth.json`, `models_cache.json` and effective `config.toml`; Claude configured settings/plugin/MCP inputs; Pi/OpenCode effective provider/auth/settings/config inputs used by their actual launch builders. Account-only file lists are insufficient. Hash effective non-secret configuration and keyed sensitive bytes; a relevant present-but-unreadable input is unknown/error, never empty success. A legitimately absent optional config is a distinct stable input. Ignore growing rollout/session output in the static ConfigRevision; those files supply per-turn proof, not configuration identity. Model-only/policy-only edits retain confirmation.

- [x] Write red `TestHarnessEffectiveConfigInvalidatesProof` and a root CODEX_HOME/config.toml edit test, with known initial confirmation. Changing effective config must change path ConfigRevision and reject the old candidate at CAS; unchanged files/restart retain proof; unreadable config fails closed; writing an unrelated rollout must not invalidate it. Repeat the effective-input contract for the other harness builders. Feed real files through newModelRefresh/initDomain inventory, then assert stale row/journal/new-run sinks; rerun the inventory/gate and race gates green after implementation.

### Effective configuration completion (2026-10-18)

- Selection: P2T2's previously recorded cold-bootstrap blocker is still P3T6's prospective pipeline/coordinator producer; it was not re-failed. P3T3's existing inventory/HMAC/gates and root injection make this remaining producer independently implementable. Per D9/current alignment, downstream cold-creation, scheduled-refresh and receiver acceptance do not block this local inventory task.
- Red: new file-content tests fail for every newly enumerated configuration file, including optional-file creation, same-size/same-mtime edits and unreadability. Relocated CODEX_HOME/CLAUDE_CONFIG_DIR tests fail too. The root test first exposed a fixture prerequisite (a subscription path without followers is not yet independently discovered by P3T6); use the actual pinned-to-following admin transition and refresh to collect genuine confirmation instead. With that prerequisite satisfied, config edits and unreadability incorrectly persist Y, append a journal row, publish Y and execute/pin Y. Unchanged/restart and rollout controls pass before implementation.
- Implementation: extend the already root-constructed HarnessFileFingerprinter, not a second producer. Include Codex effective config.toml alongside auth/model-cache; Claude user settings, installed-plugin configuration record and global MCP configuration; Pi provider/models/settings/MCP configuration; OpenCode global JSON/JSONC config alongside auth. Follow only home overrides actually forwarded by the launch env (CODEX_HOME and CLAUDE_CONFIG_DIR); do not honor ambient Pi/XDG overrides stripped by BaseEnv. Stream-hash account/config contents instead of falling back to size/mtime for larger files; reject nonregular/unreadable inputs, distinguish absent optional files, and exclude session/rollout output. Existing inventory keyed hashing feeds both ConfigRevision and the shared gate without exposing contents.
- Spec-compliance review FIRST: this closes the existing global static-config fingerprint contract only, with no new setting/flag, launcher posture, discovery source or migration. Remove an unused global settings.local.json entry (that is a project-local scope, not a global launcher input). Existing all-scope inventory, actual modes/routes, filesystem/repository error controls, current/prospective tokens, policy/model-only revision separation and secret bootstrap tests remain in place. The original task checklist is now closed for its owned producers/local acceptance, not for P3T6/P3T7/P4T3's unchecked runtime/release work.
- Anti-green-theater SECOND: the root test uses real initDomain, configured Codex home, actual candidate/probe/refresh/SQLite and reopen, gate-guarded AdvanceModel, publication cache and triggered agent execution. The fixture rejects an omitted/wrong launch home, API credentials and missing probe sandbox; real rollout parsing supplies exact confirmation. Assert original durable proof after restart, unchanged ConfigRevision across model-only edits, no second probe, authoritative model/revision, journal count, new-run executed argv and stored pins. Temporarily omit only config.toml from production enumeration: the strengthened test fails at row/revision, journal, execution and pins; restore it before final gates.
- Code-quality THIRD: reuse the existing file helper, root HMAC closure, binary/home rules, Codex evidence fixture and root boot/reopen helpers. Keep deterministic explicit input lists and streamed content hashes, reject nonregular inputs before reading, simplify test table fields and wrap comments. No generated/schema/UI changes.

Fresh verification (all exit 0):

- `go test ./domain/provider ./adapter/provider ./adapter/sqlite ./domain/workflow -run 'Inventory|ExecutionRevision|ExecutionGate' -count=1`
- `go test ./adapter/provider ./cmd/arbitron -run 'HarnessFingerprint|HarnessEffectiveConfig|PathInventory|InstallationSecret|ModelPins|Version' -count=1`
- `go test -race -p 1 ./domain/provider ./adapter/provider ./adapter/sqlite ./domain/workflow ./cmd/arbitron -run 'Inventory|ExecutionRevision|ExecutionGate|HarnessEffectiveConfig|HarnessFingerprint' -count=1`
- `go test ./domain/modelcatalog -count=1`
- `go test ./domain/modelcatalog ./adapter/crypto ./adapter/filesystem ./domain/workflow -count=1` (final dependent/full workflow gate)
- `go test ./cmd/arbitron -count=1`
- `git diff --check`

The focused workflow package regex currently matches no tests; the final full workflow package gate also passes its existing mutation-gate tests. No full-release completion is claimed.
