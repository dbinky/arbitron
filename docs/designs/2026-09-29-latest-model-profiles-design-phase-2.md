# Latest Model Profiles — Design Phase 2: Following Profiles

> **Current alignment (2026-10-19):** D1–D11 and the current closure map in Phase 3 govern implementation. Historical completion/blocker records are not a present inventory. Existing runtime producers are extended in place; upstream producer completion never waits for downstream acceptance. D8 permits native Azure/Bedrock configured-ID pinned execution while rejecting following/usability claims.

Spec: `docs/specs/2026-09-29-latest-model-profiles-spec.md`
Depends on: Phase 1 settings and run pinning.

## Goal

Let an admin select a specific model version (**pinned**) or follow the latest
eligible version in a chosen **provider + capability tier** (**following**).
Automatic advancement changes only the current model of following profiles;
provider, tier and active-run pins remain stable.

## Architecture

Extend the existing profile vertical: `domain/provider`, SQLite `ProfileRepo`,
MCP profile tools, YAML seed configuration and the profile cache.

| Unit | Responsibility |
|---|---|
| `Profile` extension | New `SelectionMode` (`pinned` default or `following`), optional `FollowTarget` with provider/tier slugs, and persisted revision. `Model` remains the selected specific version, the last known working model for following profiles. `ResolvedBinding` retains its existing contract. |
| `ProfileService` validation/patching | Admin-only create/update; reject unknown mode, missing following target, target on pinned mode and absence of any resolvable starting version. Following creation may initialize from the Phase 3 eligible-candidate port. Normalize target slugs but preserve model-ID spelling. Keep capability validation; capability tier is distinct from `ContextWindow` and `Effort`. |
| New `AdvanceModel` | Accept alias, expected revision, expected model/target/execution configuration, and eligible candidate model and path-evidence revision. Return a committed change record; P2T1's existing journal schema and P2T3 helper are reused by P3T5 in the cycle transaction. Atomically update only `Model` and revision while the row is still following and every expected field matches. Validate candidate against existing capability rules. |
| Persistence | Migration for mode/target/revision; old rows default to pinned. Add atomic conditional update to the store/repository, not a read-then-unconditional `ProfileStore.Update`. Increment revision on all admin edits so target changes or ABA changes cannot evade CAS. |
| MCP tools | Extend `profile_create`, `profile_update`, `profile_get`, `profile_list` with selection mode and follow target, retaining omission/clear patch semantics. |
| YAML seeding | Extend `domain/config/config.go:ProfileConfig` and `adapter/config/profiles.go:ProfilesFromConfig` and apply the same validation in `cmd/arbitron/profile_seed.go:SeedProfiles`. |

At creation, an admin may supply a working specific-version starting model, or
choose provider/tier and let the service initialize from Phase 3's newest
confirmed eligible catalog candidate. Do not require a manually supplied model
name to create a following profile once that catalog producer is available.
Creating a profile does not guess a model or install/update a harness. If neither
an evidenced starting version nor an eligible catalog candidate exists, return
an explicit unavailable result without persisting a pretend working model.
Changing an existing profile to following retains its evidenced starting version.
Startup retains existing persisted profiles during catalog outages; only new
following creation or model/route/target edits need new usability validation.
Pinned fixed versions need not match a following-tier pattern.
Phase 3 checks target identity and current-version metadata before advancement;
unrecognized tier or uncertain identity leaves the model unchanged and is visible.
Existing mutable model strings such as `sonnet` require P1-VERSION before they
can be claimed to pin a specific version.

The follow provider denotes the catalog model provider selected by the admin;
the connection's routing provider and the agent backend are separate axes.
An aggregator connection may carry that provider's models. Do not equate
`Connection.Provider` with the follow provider or change the route to find a
replacement. Phase 3's verified identity mapping (P3-IDENTITY) determines whether
the chosen version belongs to the same provider and capability tier.

`AdvanceModel` is an internal service operation, unavailable as a user tool.
Add an explicit internal read path for background refresh; existing
`ProfileService.List` requires an authenticated identity and cannot simply be
called with `context.Background()` as a system actor. External operations keep
service-level authorization.

## Data flow

```
admin MCP / YAML seed -> validate mode + target + starting model -> ProfileRepo
                     -> reload cache
Phase 3 eligible candidate + expected revision/config -> AdvanceModel
                     -> atomic CAS -> durable model + revision -> reload cache
new run -> Phase 1 profile snapshot -> pins
active run -> existing Phase 1 pins
```

Existing `ProfileService.recache` is best-effort. Automatic advancement must
instead expose cache-publication failure and retry it within the refresh before
reporting advancement as fully successful. Serialize publications so an older
concurrent reload cannot replace a newer snapshot. A successful four-hour refresh
must publish the selected model for new runs. Once a durable CAS succeeded,
record that change exactly once even if publication needs repair; do not invent
a second advancement on retry.

No reserved Sonnet normalizer profile is seeded or protected from deletion.
Phase 3 refresh must work on installations with other providers and harnesses;
a mandatory Claude subscription would undermine that outcome.

## Data Provenance

| Leaf input | Existing source or unresolved producer |
|---|---|
| Admin identity and write authorization | `domain/identity/identity.go:FromContext`, `Identity.IsAdmin`; `domain/provider/authz.go:requireAdmin`. |
| User model/kind/connection/backend/tools/axes | `domain/provider/provider.go:Profile`, `adapter/mcp/tools_profile.go:RegisterProfileTools`; YAML via `domain/config/config.go:ProfileConfig` and `adapter/config/profiles.go:ProfilesFromConfig`. |
| Mode/target/revision and patch intent | **PLANNED PRODUCER P2-PROFILES (P2T1–P2T3)**: new fields, parsing, migrations and `ProfilePatch` extensions have no existing producer. Admin/YAML inputs above are the boundaries to extend. |
| Existing profile snapshot/current model | `domain/provider/profile_store.go:ProfileStore.List`, `ProfileStore.Get`, implemented by `adapter/sqlite/profile_repo.go:ProfileRepo`; authenticated public reads in `domain/provider/profile_service.go:ProfileService.List`. Internal read authorization is P2-PROFILES. |
| Initial catalog candidate, candidate usability, target identity and current-model comparison | **RESOLVED P3-IDENTITY** (decisions D2) (Phase 3): stable provider/tier/version and path-ID mapping are not supplied by the current profile registry. **PLANNED PRODUCER P3-USABILITY (P3T4, decisions D3–D6)**: exact-model probes provide candidate path evidence; P3T6/P3T7 supply initialization and injection. |
| Atomic expected revision/config comparison | Existing `adapter/sqlite/profile_advance.go:ProfileRepo.CommitAdvance`, `commitAdvanceTx` and `domain/provider/advance_service.go:ProfileService.AdvanceModel` supply CAS+journal; P2T3 extends expected admin edits, candidate validation and direct publication recovery. Model-only comparison is insufficient. |
| Candidate capability validation | `domain/provider/profile_service.go:ProfileService.resolveAxes`, `domain/provider/capability.go:CapabilityRules.Validate`. |
| Cache publication | `domain/provider/profile_service.go:ProfileService.recache`, `domain/provider/authz.go:Reloader`, `adapter/provider/cache.go:ProfileCache.Load`, `Reload`; **PLANNED PRODUCER P2-PUBLISH (P2T3)**: confirmed, ordered automatic publication and repair do not exist. |
| New-run versus active-run model | Existing live source `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve`; **PLANNED PRODUCER P1-PINS (P1T3–P1T4)** and **P1-VERSION** from Phase 1 supply the new snapshot and immutable pins. |

## Integration and plan obligations

Implement P2-PROFILES/P2-PUBLISH in the existing service and adapters, regenerate
sqlc and extend sample configuration comments. Preserve the default pinned mode
for existing rows/configs. Share the exact selection/target/revision contracts
with Phase 3. Do not add unauthenticated public reads or bypass service validation
for automated writes.

## Error handling

Validation and authorization errors persist nothing. Pinned profiles reject
advancement. Any concurrent mode/target/model/connection/backend/axes/tool edit
causes CAS conflict and a skip for this cycle. Unverifiable target, current
version, or candidate usability preserves the last known working model. A
successful durable write with failed cache publication is a recoverable partial
failure, recorded separately and repaired before refresh success is reported.

## Testing — strict TDD

Write and run each expected failing test before implementation; implement the
minimum behavior, rerun to green and refactor without changing the contract.

- **Happy path:** create pinned and following profiles; following resolves its
  specific starting model and advances only within its provider/tier.
- **Success:** CAS advances and publishes a newer eligible model; new runs pin
  it and active runs retain their prior pins; switching mode works through patch;
  following creation initializes from the latest eligible catalog candidate
  when the admin supplies only provider/tier and routing configuration.
- **Failure:** invalid modes/targets, no available starting version, non-admin writes, unsupported
  capability axes and advancement of pinned profiles persist no changes.
- **Error:** CAS conflicts for target/routing changes with an unchanged model,
  ABA edits, repository errors, and failed reload/publication; repair is observable.
- **Edge cases:** old rows/configs migrate pinned, normalized target slugs retain
  exact model-ID spelling, unmatched targets stay put, partial patch omission
  differs from clear, multiple concurrent publications cannot regress cache,
  system refresh uses its internal read path without weakening public auth.

Use domain service tests, real migrated SQLite CAS tests, MCP handler tests and
seed/config tests; integrate with Phase 1 run pins and Phase 3 refresh.

## Implementation alignment (2026-09-29)

- P2T1: [2026-09-29-latest-model-profiles-implementation-phase-2-task-1](../plans/2026-09-29-latest-model-profiles-implementation-phase-2-task-1.md).
- P2T2: [2026-09-29-latest-model-profiles-implementation-phase-2-task-2](../plans/2026-09-29-latest-model-profiles-implementation-phase-2-task-2.md).
- P2T3: [2026-09-29-latest-model-profiles-implementation-phase-2-task-3](../plans/2026-09-29-latest-model-profiles-implementation-phase-2-task-3.md).

The execution/coverage map in `2026-09-29-latest-model-profiles-design-phase-3.md` is authoritative for cross-phase ordering. A planned producer closes task ownership; its implementation and final-sink tests remain required before release. Every task has named happy/success/failure/error/edge tests and a final-sink wiring obligation.

## Alignment clarification (2026-10-16)

Selection validation returns PreparedSelection, retaining EligibleCandidate model/canonical identity, prospective PathRevision and ExecutionRevision from both Newest and ValidateStart. CandidateService supplies route-only VersionRequests (real connection provider/effective backend) and read-only RevalidateStart. Admin and seed prepare outside mutation leases, then revalidate the same prospective profile under the lease and commit through P2T3 SelectionMutationStore with SQL policy/connection checks. Axes/tool edits affect path configuration and must be revalidated. A prospective digest is not the current installation digest used by automatic WithCurrent. Existing pinned explicit versions do not require catalog/tier-policy reads for run capture; arbitrary digit-containing aliases do not establish fixed versions. P2T1–P2T3/P3T3/P3T6/P3T7 own these contract extensions and final persisted/executed-model tests.

Direct advancement publication failures are also recoverable when no RefreshReport was ever created. P2T3 defines PublicationJournal and the service injection; P3T5 migration 022 supplies acknowledgement storage over existing original journal rows; P3T6/P3T7 wire recovery before success/serving. Recovery publishes/acknowledges rows and never repeats their CAS. Tests drive reopen -> new-run executed model with exactly one original advancement.

## Current validation and seed ownership

P2T2 keeps the existing service-backed SeedProfiles(ctx,store,seeder,candidates,onSkip) entry point and ProfileSeedStore's migration-030 original pending snapshot. Prepare outside leases, then validate/write the pending seed batch under the shared gate without recursive public Create; resume interrupted originals without overwriting existing rows. P3T6 supplies durable prospective discovery before the first seed, P3T7 injects it before lifecycle start. A successful first boot must not silently postpone configured following aliases to another boot. Pinned D8 native configured IDs are route-aware exceptions, not SpecificVersion evidence; absent route ports reject new/admin writes even when both were omitted.

P2T3 produces read-only ValidateAdvance(ctx,AdvanceRequest) error using the real configured capability rules and actual route-only version inputs. P3T6 calls it for automatic proposals, P3T7 injects the actual ProfileService, and a root refresh test rejects capability-incompatible Y while compatible Z reaches durable/new-run execution. Direct AdvanceModel validation alone does not cover automatic CommitCycle.
