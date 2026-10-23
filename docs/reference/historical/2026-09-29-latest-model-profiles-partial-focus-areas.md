# Focus Areas — Latest Model Profiles Review Plan

Single-system review plan for branch `dev-harness-and-model-management-2026-09-29-5e86d796`
(`22a953d..HEAD`). Each row is one **behaviour** traced from entry to sink, not a
layer or a file. Review 1 is the **wiring lens** (does the path exist and is it
fed?); review 2 is the **failure lens** (how does it fail?). Pass criteria live in
`RALPH.md`. Seams between areas live in `docs/reference/paired-reviews.md`.

Only the code this branch actually ships is reviewed here. The blocked phases
(run pinning, CAS advancement, identity, usability, refresh loop, announcements)
have no code; they are release-blocking escalations under `## Won't Fix` in
`docs/reference/gaps-identified.md`. A hop in an area below that ends at one of
those unbuilt consumers passes the wiring lens only if that escalation is recorded
and no production code substitutes a nil, empty or hardcoded producer for it.

**Budget:** 4 single-system areas × 2 + 0 paired seams = **8 minimum passes**;
500 × 0.35 = **175** available. 8 ≤ 175; no budget deferrals.

## Single-System Reviews

| # | Focus Area | Risk | Review 1 Status | Review 2 Status |
|---|---|---|---|---|
| 1 | every boot loads or creates the installation model-state key once and refuses to start when it is missing or corrupt | high | [x] | [x] |
| 2 | YAML seeds following profiles; profile_update preserves their selection and increments revision; stored profiles load into the resolver cache | high | [x] | [x] |
| 3 | an admin's catalog URL and Teams webhook edits persist, with the webhook defaulting empty and redacted on read | high | [x] | [x] |
| 4 | every connection metadata or credential edit bumps its stored config revision, and a recreated connection never reuses an old one | med | [x] | [x] |

---

### Area 1 — every boot loads or creates the installation model-state key once and refuses to start when it is missing or corrupt

- **Promise:** On every `serve` and `mcp` boot (subscription-only installs too), Arbitron loads the 32-byte key at `<db path>.model-state.key`, creates it once atomically under a cross-process lock on first boot, records `model_state_meta.secret_initialized=1` only after publication, and afterwards refuses to start rather than regenerate a missing or invalid-length key; same-length corruption must be assessed separately (P3T3 independent portion, commit 40026e8).
- **Entry:** `cmd/arbitron/main.go:initDomainWithModelStateSecret` (reached via `initDomain` from `runServe` and `newMCPCmd`).
- **Sink:** `adapter/crypto/installation_secret.go:LoadOrCreateInstallationSecret` (key file published by `publishInstallationSecret`) plus `generated.Queries.MarkModelStateSecretInitialized`; the resulting AES-GCM is held in `cmd/arbitron/main.go:domainStack.modelStateCrypto`.
- **Files:**
  1. Changed: `cmd/arbitron/model_state.go`, `cmd/arbitron/main.go` (`initDomain`, `initDomainWithModelStateSecret`, `domainStack.modelStateCrypto`), `adapter/crypto/installation_secret.go`, `adapter/crypto/installation_secret_unix.go`, `adapter/crypto/installation_secret_windows.go`, `adapter/crypto/aesgcm.go` (package comment), `adapter/sqlite/migrations/023_execution_revisions.up.sql` and `.down.sql` (the `model_state_meta` half), `adapter/sqlite/queries/model_state.sql`, `adapter/sqlite/generated/model_state.sql.go`.
  2. Tests: `cmd/arbitron/model_state_secret_wiring_test.go`, `adapter/crypto/installation_secret_test.go`.
  3. Unchanged, on the path: `adapter/crypto/masterkey.go` (`masterKeyLen`), `adapter/crypto/aesgcm.go:NewAESGCM`, `adapter/sqlite/migrate.go`, `install.sh` (DB paths near `arbitron_db_path` line ~266, systemd `ReadWritePaths` line ~1542), `docs/install/upgrading.md` (forward-only migration / backup box), `cmd/arbitron/connection.go` (opens the DB without initialising the key).
- **Folded in:**
  1. `docs/install/upgrading.md` backup table lists only `arbitron.db`; the key file must be backed up with it (open gap G1).
  2. `adapter/crypto/aesgcm.go` package comment rewrite — check it makes no claim the code does not keep.
  3. Migration 023 down drops `model_state_meta`; check a down/up cycle with the key file present boots cleanly.
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/model_state.go:initModelStateCrypto` runs after migrations and before adapters in `cmd/arbitron/main.go:initDomainWithModelStateSecret`; both `runServe` and the `mcp` command reach it through `initDomain`, and no production caller passes a non-nil `injectedSecret` (grep `initDomainWithModelStateSecret(`).
  2. `cmd/arbitron/model_state.go:initModelStateCrypto` feeds `required` from the persisted flag `adapter/sqlite/queries/model_state.sql:ModelStateSecretInitialized`, not a constant, and the flag is written only after `NewAESGCM` succeeds. Test: `TestExecutionRevisionSecretBootFailureDoesNotMarkInitialized`.
  3. `cmd/arbitron/model_state.go:initModelStateCrypto` derives the key path from the real `cfg.Database.Path`, and on every install layout in `install.sh` (local `~/.arbitron/data`, shared `/var/lib/arbitron`, Windows `%LOCALAPPDATA%`) the service user can create a sibling file there.
  4. `domainStack.modelStateCrypto` has no reader (grep `modelStateCrypto`). Confirm escalation W3 in gaps `## Won't Fix`, and that nothing substitutes `connCrypto` or nil where a model-state consumer will attach.
  5. Happy and success paths reach the file and the flag: `TestExecutionRevisionSubscriptionBootSecretReload` (a subscription-only boot, then a reopen with identical bytes) and `TestInstallationSecretCreateReload`.
- **Review 2 — failure lens:**
  1. Missing key with `secret_initialized=1` fails boot with the restore message and never regenerates (`TestInstallationSecretMissingRequired`); `adapter/crypto/installation_secret.go:readInstallationSecret` rejects short or long keys without printing bytes (`TestInstallationSecretCorruptAndWriteFailure`). It checks length only: a changed 32-byte key still boots. Do not count the size tests as proof of arbitrary corruption detection; assess this against P3T3's corrupt-key requirement and record any missing integrity guarantee as a gap.
  2. Crash windows: key published but flag not marked → the next boot reuses the file (not yet required); temp write → `Sync` → rename → directory `Sync` ordering in `adapter/crypto/installation_secret_unix.go:publishInstallationSecret`; the Windows `MoveFileEx(MOVEFILE_WRITE_THROUGH)` path gives the same guarantee. Name the test that goes red if publication happens before the data is durable.
  3. Concurrency: `adapter/crypto/installation_secret.go:LoadOrCreateInstallationSecret` must return identical bytes to `serve` and `mcp` booting together on one DB. Check flock on `<key>.lock` (unix) and `LockFileEx` (windows); `TestInstallationSecretConcurrentProcesses` uses real subprocesses — confirm it fails if the lock is removed.
  4. Permissions: temp and final file are 0600 on unix; on Windows `Chmod` is a no-op, so check the ACL the shared-install data directory gives the key.
  5. `:memory:` requires an explicit secret (`TestExecutionRevisionMemoryRequiresExplicitSecret`); confirm no shipped config or `install.sh` path can select `:memory:` in production.
  6. Restore scenario: a DB backup restored without its key refuses boot. Close G1 so operators are told to back up both files, and check the error message names the file path.
- **Seed evidence:**
  1. `grep -rn modelStateCrypto --include=*.go` → `cmd/arbitron/main.go:177` (field), `:417` (construction), `:1245` (assignment); no reader.
  2. `grep -rn LoadOrCreateInstallationSecret` → only `cmd/arbitron/model_state.go:initModelStateCrypto`.
  3. `docs/install/upgrading.md` "Forward-only database migrations" box lists only `arbitron.db` paths.
  4. `go test ./...` on HEAD: only baseline failure `adapter/config > TestSampleFeaturePipeline_ValidatesWithDefaultProfiles`.

---

### Area 2 — YAML seeds following profiles; profile_update preserves their selection and increments revision; stored profiles load into the resolver cache

- **Promise:** A profile can be `pinned` (default for every old row and config) or `following` a provider + tier. Target slugs are normalised and the exact model spelling is preserved. Persistence owns a revision that starts at 1 and increments on every write. Every stored profile still maps through `rowToProfile` into the `ProfileCache` that all runs resolve through (P2T1, commit 1db798f).
- **Entry:**
  1. YAML at boot: `adapter/config/profiles.go:ProfilesFromConfig`, then `cmd/arbitron/profile_seed.go:SeedProfiles` (called from `initDomainWithModelStateSecret`).
  2. Admin edit of existing fields: `adapter/mcp/tools_profile.go:profileUpdateHandler` / `patchFromArgs` → `domain/provider/profile_service.go:ProfileService.UpdatePatch`. MCP cannot supply selection mode or target; those domain patch fields are currently exercised only by direct callers/tests (W6).
- **Sink:**
  1. Written to the `profiles` columns `selection_mode`, `follow_provider`, `follow_tier`, `revision` by `adapter/sqlite/profile_repo.go:ProfileRepo.Create` / `Update`.
  2. Read back through `ProfileRepo.List`, `adapter/provider/cache.go:ProfileCache.Load` and `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve`.
- **Files:**
  1. Changed: `domain/provider/provider.go`, `domain/provider/selection.go`, `domain/provider/profile_patch.go`, `domain/provider/profile_store.go`, `domain/provider/advance.go`, `domain/config/config.go`, `adapter/config/profiles.go`, `adapter/sqlite/profile_repo.go`, `adapter/sqlite/queries/profiles.sql`, `adapter/sqlite/generated/profiles.sql.go`, `adapter/sqlite/generated/models.go`, `adapter/sqlite/migrations/021_following_profiles.up.sql` and `.down.sql`, `adapter/provider/cache.go`.
  2. Tests: `adapter/sqlite/profile_following_test.go`, `adapter/provider/profile_following_test.go`, `domain/provider/profile_following_test.go`, `adapter/config/profile_following_test.go`, `adapter/config/profiles_test.go`, `domain/config/profile_following_test.go`, `cmd/arbitron/profile_following_wiring_test.go`.
  3. Unchanged, on the path: `cmd/arbitron/profile_seed.go:SeedProfiles`, `cmd/arbitron/main.go` (the profile seed / `profileCache.Load` block around lines 716–770), `domain/provider/profile_service.go` (`write`, `UpdatePatch`, `recache`), `adapter/mcp/tools_profile.go` (`profile_update` patch builder, `profile_get` / `profile_list` DTOs), `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve`.
- **Folded in:**
  1. `domain/provider/advance.go`: `AdvanceRequest`, `Advancement`, `AdvanceStore`, `EligibleCandidateSource`, `ErrAdvanceConflict` are declared with no implementation. The `profile_advancement_journal` table (migration 021) has no writer. Check that nothing constructs a nil or fake implementation in production (escalation W5).
  2. `adapter/provider/cache.go`: `Clone` on `Load`, `get`, `fixedProfileStore` and `NewProfileCacheFromProfiles` — this is on every run's resolution path.
  3. `adapter/config/profiles.go`: `AllowedTools` is now `slices.Clone`d.
  4. Revised contract comments in `domain/provider/profile_store.go:ProfileStore` — check they match the SQL.
- **Review 1 — wiring lens:**
  1. YAML `selection_mode` / `follow_target` reach the row through `domain/config/config.go:ProfileConfig` / `FollowTargetConfig` (the koanf tags; `FollowTargetConfig` fields have none, so confirm lowercase `provider:` / `tier:` bind), then `ProfilesFromConfig`, `SeedProfiles` and `ProfileRepo.Create`. Test: `TestFollowingProfileRootRoundTrip` asserts at `ds.profileSvc.Get` across a real `initDomain` reopen.
  2. `profile_update` keeps mode and target when MCP omits them: `ProfilePatch.apply` → `NormalizeSelection` → `ProfileRepo.Update`, which does `revision = revision + 1`. `adapter/mcp/tools_profile.go:patchFromArgs` never sets `SelectionMode` / `FollowTarget`. The repo-only `adapter/sqlite/profile_following_test.go:TestFollowingProfileRevisionABAAndConcurrentEdits` proves SQL increments, not MCP preservation. Require an actual `profile_update` call on a YAML-seeded following row and assert persisted selection plus revision and the reloaded cache.
  3. `adapter/sqlite/profile_repo.go:ProfileRepo.Get` and `ProfileRepo.List` map the new columns through `rowToProfile`, then `adapter/provider/cache.go:ProfileCache.Load` → `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve`. Test: `TestFollowingProfileCacheReloadFromSQLite`.
  4. On an existing install, following selection can be neither configured nor inspected through MCP (ordinary profile fields remain visible). `SeedProfiles` returns early when the table is non-empty, and the MCP profile tools neither accept nor return the fields. Confirm escalation W6 is recorded and that nothing claims otherwise (sample config comments, docs).
  5. The unfed ports in "Folded in" item 1 are recorded as escalation W5, with no production stand-in.
- **Review 2 — failure lens:**
  1. `cmd/arbitron/profile_seed.go:SeedProfiles` on first boot with a malformed selection in YAML (unknown mode, target on pinned, non-slug target): `ProfileRepo.Create` returns `ErrInvalidSelection`, and `SeedProfiles` treats that as fatal. Boot aborts after a partial seed, and the next boot skips seeding because the table is non-empty. This is open gap G3; the fix must make the red test pass.
  2. A single stored row that `adapter/sqlite/profile_repo.go:rowToProfile` rejects (non-slug target written outside the repo, `revision < 1`) fails `ProfileRepo.List`. That aborts `profileCache.Load` at boot and leaves a stale cache on `Reload`. Confirm the migration 021 CHECK rejects every shape the domain rejects except slug form, and name the test that shows the chosen fail-closed behaviour.
  3. Migration 021 on a populated DB: old rows read back `pinned` / `''` / `''` / `1`. The CHECK attached to the `revision` column references sibling columns; show it is enforced on `UPDATE`. The down migration drops the columns and the journal (`TestFollowingProfilesMigrationOldRowsAndRollback`).
  4. `adapter/sqlite/queries/profiles.sql:UpdateProfile` increments atomically; `adapter/sqlite/profile_following_test.go:TestFollowingProfileRevisionABAAndConcurrentEdits` exercises concurrent repo `Update`, not service `UpdatePatch`. Trace `domain/provider/profile_service.go:UpdatePatch` read/merge/write and distinguish revision increments from preservation of concurrent sparse edits. `CreateProfile` restarts at 1 after delete; an identical recreation also matches the whole `advance.go:AdvanceRequest.Expected`. Do not assert the unimplemented CAS makes this safe: record the lifecycle assumption with W5 for P2T3, and log any present persistence-contract violation.
  5. `domain/provider/selection.go:Profile.Clone` and `adapter/provider/cache.go:ProfileCache.get` copy `AllowedTools` and `FollowTarget`; changing a returned cached profile must not mutate the snapshot (`TestFollowingProfileCacheCopiesNestedFields`). `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve` returns a binding without `FollowTarget`; check its returned `AllowedTools` separately.
  6. Patch edge cases: `**FollowTarget` pointer-to-nil clears while nil omits; model spelling such as `Vendor/Version-X` is not lowercased; `profile_update` with a now-invalid selection returns a validation error, not an internal error, and writes nothing.
  7. Baseline: `adapter/config > TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` fails at the branch point with `UNKNOWN_PROFILE` for sample aliases. Confirm its failure text is unchanged and unrelated to `ProfilesFromConfig`.
- **Seed evidence:**
  1. `grep -rn "SelectionMode\|FollowTarget" adapter/mcp` → no matches; MCP profile tools do not carry the fields.
  2. `grep -rn "AdvanceStore\|EligibleCandidateSource\|CommitAdvance"` (non-test) → only declarations in `domain/provider/advance.go`.
  3. `cmd/arbitron/profile_seed.go:SeedProfiles`: `if n > 0 { return 0, 0, nil }` and `return seeded, skipped, cerr` on any non-`ErrProfileExists` Create error. `main.go` returns `seeding profiles: %w` from `initDomain`.
  4. `adapter/sqlite/profile_repo.go:rowToProfile` now returns `provider.NormalizeSelection(...)` for every row.

---

### Area 3 — an admin's catalog URL and Teams webhook edits persist, with the webhook defaulting empty and redacted on read

- **Promise:** An admin reads and edits two settings through HTTP `GET` / `PUT /api/v1/system/settings`, MCP `system_settings_get` / `system_settings_update` and the dashboard "Model Announcements" card:
  1. `model_catalog.url`, defaulting to `https://models.dev/api.json`.
  2. `model_announcements.teams_webhook_url`, defaulting to empty.

  Values persist in SQLite across restart. An omitted field is retained and an empty webhook clears it. Public reads keep the catalog URL visible and return `**REDACTED**` for a configured webhook (empty when unset). The service exposes the raw webhook only through trusted `ReadInternal`. The implemented catalog transport uses an SSRF-guarded client (30 s, 3 redirects, 20 MiB), but it has no production caller; review it as folded infrastructure and retain W1/W2, not as a completed fetch path. (P1T1, P1T2, raw-fetch portion of P3T2; commits 3854785, 308a47c, 52212c2.)
- **Entry:** `adapter/http/server.go:SystemHandler.UpdateSettings` / `GetSettings`; `adapter/mcp/tools_system.go:systemSettingsHandler`; `dashboard/src/routes/Settings.tsx:ModelAnnouncementSettings`.
- **Sink:**
  1. The `system_settings` rows, written by `adapter/sqlite/settings_repo.go:SettingsRepo.Update`.
  2. The redacted response built by `domain/settings/service.go:publicValues`.
  3. Pending consumer boundary: `Service.ReadInternal` has no production caller. `adapter/modelcatalog/fetcher.go:Fetcher.Fetch` can perform a guarded outbound GET, but no composition-root path reaches it (W1/W2); this is not a live sink.
- **Files:**
  1. Changed: `domain/settings/settings.go`, `domain/settings/service.go`, `adapter/sqlite/settings_repo.go`, `adapter/sqlite/queries/settings.sql`, `adapter/sqlite/generated/settings.sql.go`, `adapter/sqlite/migrations/019_system_settings.up.sql` and `.down.sql`, `adapter/http/server.go`, `adapter/mcp/tools_system.go`, `adapter/mcp/deps.go`, `cmd/arbitron/main.go` (`systemSettings` construction, `domainStack.SystemSettings`, `mcpDeps`, `httpDeps`, `runServe` router assembly), `dashboard/src/api/client.ts`, `dashboard/src/routes/Settings.tsx`, `dashboard/src/routes/Settings.module.css`, `adapter/dashboard/dist/*`, `domain/modelcatalog/catalog.go`, `adapter/modelcatalog/fetcher.go`, `adapter/outbound/guarded_client.go`.
  2. Tests: `domain/settings/service_test.go`, `adapter/sqlite/settings_repo_test.go`, `adapter/http/system_settings_test.go`, `adapter/mcp/tools_system_settings_test.go`, `adapter/mcp/register_test.go`, `cmd/arbitron/model_settings_wiring_test.go`, `dashboard/src/api/client.test.ts`, `dashboard/src/routes/Settings.test.tsx`, `adapter/modelcatalog/fetcher_test.go`, `adapter/outbound/guarded_client_test.go`.
  3. Unchanged, on the path: `domain/config/teams.go` (`ValidateTeamsWebhookURL`, `RedactWebhookURL`, `RedactedSecret`), `adapter/http/errors.go:WriteError`, `domain/identity/identity.go`, the authenticated route group and `RequestLoggingMiddleware` in `adapter/http`, and `adapter/notification/ssrf_guard.go` (the existing separate guard, for comparison).
- **Folded in:**
  1. `cmd/arbitron/main.go:domainStack.httpDeps` refactor: `runServe` now builds `RouterDeps` from `httpDeps()` plus `DashboardHandler` / `MCPHandler`. Every route in the server depends on this refactor keeping all fields.
  2. `dashboard/src/api/client.ts:request` now reads `body.error ?? body.message`. That is correct for `adapter/http/errors.go:ErrorResponse` (`error` carries the message) and changes every dashboard error string.
  3. `adapter/dashboard/dist/*` was rebuilt; check it matches source via `make ui`.
  4. `adapter/mcp/register_test.go` expected-tool count.
  5. `adapter/modelcatalog/fetcher.go:NewFetcher` / `Fetch`, `domain/modelcatalog/catalog.go:CatalogFetcher` and `adapter/outbound/guarded_client.go:NewGuardedClient`: no production entry point; fold under the catalog setting they are intended to consume. Check construction, dial/redirect guards and bounds here, without implementing blocked P3T7.
- **Review 1 — wiring lens:**
  1. `settingsdomain.NewService(adaptsqlite.NewSettingsRepo(db))` in `cmd/arbitron/main.go:initDomainWithModelStateSecret` reaches `domainStack.SystemSettings`, `mcpDeps()` and `httpDeps()`. `runServe` and the `mcp` command both use these projections. Test: `TestModelSettingsWiringRoundTrip` (HTTP write → MCP read → reopen → `ReadInternal`).
  2. The refactor dropped nothing: diff the `adapthttp.RouterDeps{...}` literal in `runServe` at `22a953d` against `cmd/arbitron/main.go:domainStack.httpDeps` plus the two post-assignments.
  3. `dashboard/src/routes/Settings.tsx:ModelAnnouncementSettings` sends camelCase `catalogUrl` / `teamsWebhookUrl` through `api.system.updateSettings` → `PUT /api/v1/system/settings`, matching the keys `SystemHandler.UpdateSettings` accepts (`Settings.test.tsx`, `client.test.ts`).
  4. On a fresh DB, migration 019 seeds both defaults with `INSERT OR IGNORE` and the `ReadSystemSettings` JOIN returns one row (`TestSettingsPersistRestart`).
  5. The raw webhook service boundary is `domain/settings/service.go:Service.ReadInternal`, which has no production caller; `publicValues` intentionally exposes the catalog URL. `NewFetcher` is never constructed outside tests. Confirm escalations W1 and W2 are recorded, and that no production code fetches a hardcoded models.dev URL or reads `system_settings` around the service.
  6. The routes sit inside the authenticated group, and both surfaces delegate authorisation to `domain/settings/service.go:requireAdmin`.
- **Review 2 — failure lens:**
  1. The raw webhook never leaves. Check the HTTP and MCP get/update responses, the error paths (`writeSystemSettingsError`, `systemSettingsError`, and the 5xx `detail` log in `WriteError`) and request logging. Name a test that goes red if the raw URL appears in any response or log line.
  2. `adapter/sqlite/settings_repo.go:SettingsRepo.Update` writes only supplied keys in one transaction: an omitted key is retained; an empty webhook clears it; `**REDACTED**`, JSON `null`, non-strings and unknown keys are rejected with no write. Two concurrent independent patches lose neither field (`TestSettingsPatchOmissionAndClear`). The dashboard Save omits the webhook when its input is blank.
  3. `adapter/http/server.go:writeSystemSettingsError` and `adapter/mcp/tools_system.go:systemSettingsError` turn a store read error into a generic 500 or MCP error, never an "empty / not configured" answer. `ReadInternal` propagates the error (`TestSettingsReadAndWriteFaults`, `TestSettingsServiceReadAndWriteFaults`), and the dashboard shows it.
  4. `adapter/http/server.go:SystemHandler.UpdateSettings` rejects missing/non-admin identity with HTTP 401/403 before parsing. `adapter/mcp/tools_system.go:systemSettingsHandler` parses arguments first, then `domain/settings/service.go:Service.Update` rejects unauthorized writes with MCP tool errors, not HTTP status codes. Assert neither surface reaches the store without admin identity, including malformed unauthorized requests; `Service.Get` gates reads too.
  5. `adapter/outbound/guarded_client.go:NewGuardedClient`, `guardedTransport.RoundTrip`, `validateURL` and `GuardedDialContext` must cover:
     - a dial-time `CheckDialAddress` on the resolved IP (DNS rebinding);
     - re-validated redirects, capped at 3;
     - proxies disabled;
     - userinfo, `localhost` and `.local` rejected.

     Probe `64:ff9b::/96` (NAT64), `2002::/16` (6to4) and IPv4-mapped v6 (`TestGuardClientRejectsInternalDial`, `TestGuardRedirectSink`).
  6. `adapter/modelcatalog/fetcher.go:Fetcher.Fetch` bounds: known-length and chunked bodies over 20 MiB, the exact boundary, an empty body, non-2xx, and timeout or cancellation (`TestFetchBoundsAndFailures`, `TestFetchExactSizeLimit`, `TestFetchTimeoutAndCancellation`).
  7. Validation asymmetry: `Service.Update` accepts `http://169.254.169.254/…` or `http://localhost/…` as a catalog URL (parse-only check), while the webhook validator rejects such hosts when saved. Confirm that fetch-time rejection is the documented design (phase 1: "fetch-time SSRF enforcement in Phase 3"). If it is not sufficient, log a gap.
- **Seed evidence:**
  1. `grep -rn ReadInternal --include=*.go` (non-test) → only the definition `domain/settings/service.go:87`.
  2. `grep -rn "NewFetcher\|NewGuardedClient"` (non-test) → `adapter/modelcatalog/fetcher.go:NewFetcher` only; `cmd/arbitron/main.go` constructs neither.
  3. `adapter/http/errors.go:WriteError` encodes `ErrorResponse{Error: msg, Code: code}` and logs `detail` for status ≥ 500. The settings handlers pass only static strings.
  4. `domain/config/teams.go:RedactWebhookURL` returns `**REDACTED**` for a non-empty webhook and an empty string otherwise, and `Service.Update` rejects exactly that constant.

---

### Area 4 — every connection metadata or credential edit bumps its stored config revision, and a recreated connection never reuses an old one

- **Promise:** Each stored connection carries `config_revision`, starting at 1. Every metadata or API-key update atomically increments it, and it is read back as `provider.Connection.ConfigRevision`, the monotonic signal future path-revision checks use to refuse stale usability evidence after a credential change (P3T3 independent portion, commit 40026e8).
- **Entry:** MCP `connection_update` in `adapter/mcp/tools_connection.go`, then `domain/provider/connection_service.go:ConnectionService.UpdatePatch` / `Update`.
- **Sink:** The `connections.config_revision` column, written by `generated.UpdateConnectionMetadata` / `UpdateConnectionWithKey` in `adapter/sqlite/connection_repo.go:ConnectionRepo.Update`. It is read back by `ConnectionRepo.Get` / `List`.
- **Files:**
  1. Changed: `domain/provider/connection.go`, `adapter/sqlite/connection_repo.go`, `adapter/sqlite/queries/connections.sql`, `adapter/sqlite/generated/connections.sql.go`, `adapter/sqlite/generated/models.go`, `adapter/sqlite/migrations/023_execution_revisions.up.sql` and `.down.sql` (the `connections` half).
  2. Tests: `adapter/sqlite/connection_execution_revision_test.go`, `cmd/arbitron/model_state_secret_wiring_test.go:TestExecutionRevisionRootConnectionMutationPersists`.
  3. Unchanged, on the path: `adapter/mcp/tools_connection.go` (`connection_create` / `update` / `delete`), `domain/provider/connection_service.go`, `adapter/sqlite/connection_repo.go` (`Create`, `Delete`), `cmd/arbitron/main.go` (`connStore` construction around line 698).
- **Folded in:**
  1. The `ConfigRevision` field is placed first in `provider.Connection`. Check that no positional struct literal elsewhere was silently re-mapped (`grep -rn "provider.Connection{"`).
- **Review 1 — wiring lens:**
  1. `adapter/sqlite/queries/connections.sql:UpdateConnectionMetadata` and `UpdateConnectionWithKey` increment `config_revision`, `CreateConnection` relies on the column default of 1, and `Get` / `List` map it (`TestConnectionExecutionRevision`).
  2. Trace `adapter/mcp/tools_connection.go:RegisterConnectionTools` / `connectionUpdateHandler` → `domain/provider/connection_service.go:ConnectionService.UpdatePatch` → `adapter/sqlite/connection_repo.go:ConnectionRepo.Update` through `cmd/arbitron/main.go:domainStack.mcpDeps`. `cmd/arbitron/model_state_secret_wiring_test.go:TestExecutionRevisionRootConnectionMutationPersists` calls the service directly; it does not prove MCP wiring. Require a tool-dispatch assertion at the stored revision and credential sink.
  3. `adapter/sqlite/connection_repo.go:ConnectionRepo.Update` must be the revision-aware path for every other connection edit: grep the queries and repo for `UPDATE connections`, and check any key re-seal or master-key rotation path.
  4. `ConfigRevision` has no production reader. Confirm escalation W4 is recorded and that no code treats a zero or absent revision as "unchanged".
- **Review 2 — failure lens:**
  1. `adapter/sqlite/connection_repo.go:ConnectionRepo.Delete` followed by `Create` with the same name but a different key yields `config_revision = 1` again. The only credential-change signal repeats, so a future path revision cannot tell the two keys apart. This is open gap G2; name the test that goes red. Exercise the real service path too: `domain/provider/connection_service.go:ConnectionService.Delete` rejects referenced connections, so use an unreferenced name or explicitly remove references first.
  2. `adapter/sqlite/queries/connections.sql:UpdateConnectionMetadata` and `UpdateConnectionWithKey` each increment under concurrent edits; `TestConnectionExecutionRevisionConcurrentUpdates` currently covers only nil-key metadata updates. Check mixed metadata/credential edits at the durable row.
  3. `adapter/sqlite/connection_repo.go:ConnectionRepo.Update` rejects invalid URLs, `provider.ValidateConnection` failures and `Crypto.Seal` failures before its UPDATE; assert both revision and encrypted credential remain unchanged. `TestConnectionExecutionRevision` covers URL rejection only, so identify coverage for the other two failures.
  4. `ConnectionRepo.Update` checks existence with `GetConnectionMeta`, then runs an `:exec` UPDATE outside a transaction. A concurrent delete in between reports success with zero rows. Decide whether that matters for the revision contract.
  5. Migration 023 sets existing rows to 1 and the down migration drops the column (`TestConnectionExecutionRevisionMigrationRoundTrip`).
- **Seed evidence:**
  1. `grep -rn ConfigRevision --include=*.go` (non-test, non-generated) → `domain/provider/connection.go:14` and `adapter/sqlite/connection_repo.go` `Get` / `List` only.
  2. `adapter/sqlite/queries/connections.sql:CreateConnection` does not set `config_revision`, and migration 023 defaults it to 1, so a recreated name restarts at 1.
