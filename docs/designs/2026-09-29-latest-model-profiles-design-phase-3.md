# Latest Model Profiles — Design Phase 3: Catalog Refresh

> **Current alignment (2026-10-19):** D1–D11 and the current closure map in Phase 3 govern implementation. Historical completion/blocker records are not a present inventory. Existing runtime producers are extended in place; upstream producer completion never waits for downstream acceptance. D8 permits native Azure/Bedrock configured-ID pinned execution while rejecting following/usability claims.

Spec: `docs/specs/2026-09-29-latest-model-profiles-spec.md`
Depends on: Phase 1 settings/run pins; Phase 2 following profiles and atomic
`AdvanceModel` with confirmed cache publication.

## Goal

Every four hours, refresh an internal catalog, distinguish cataloged from
confirmed usable models on this installation's providers and Claude Code,
Codex, Pi and OpenCode paths, and advance eligible following profiles by that
successful refresh. Discovery and announcements must include usable models
with **no following profiles**. Failures preserve working profiles and run pins.

## Architecture

New `domain/modelcatalog` ports/orchestration, driven adapters under `adapter/`,
SQLite persistence, MCP tools and composition-root wiring. Use a cancellable
startup refresh and four-hour ticker, with single-flight protection shared by
scheduled and manual refreshes. No harness installation or upgrade is performed.

| Unit | Responsibility |
|---|---|
| `CatalogFetcher` | Read Phase 1 URL and download the catalog with bounded size/time and guarded redirects/DNS. Ingest all provider slices, not just followed providers. Reject malformed top-level shape without replacing prior data. |
| `CatalogNormalizer` | Parse source-backed identity/release metadata deterministically. Catalog identity, follow-provider identity, capability tier, immutable version and ordering evidence are separate fields. Preserve raw-source evidence. Unsupported mappings remain cataloged but ineligible. No mandatory LLM call or reserved Sonnet profile is required. |
| `ExecutionPathInventory` | Enumerate configured connection/chat paths and configured CLI paths using actual factory routing, profile configuration, deployment defaults and workflow node overrides. Include pinned profiles and providers/harnesses without following profiles. Produce stable path keys with execution kind, backend, connection/account configuration revision and endpoint/provider metadata; never put secrets in keys or output. |
| `UsabilityProbe` router | List candidates where supported, then obtain verified usability for the **actual path**, with matching credentials, binary, endpoint and configuration. A provider listing or installed binary alone does not prove that a model can be executed by a harness. Unsupported/failed checks report unknown. |
| Usability matching within router lanes | Translate native IDs to catalog identities with verified provider-specific mappings; retain the exact model spelling required by the execution adapter. Strip only a recognized routing prefix, never arbitrary text before `/`. Resolve deployment IDs and subscription aliases through evidence, not string guesses. |
| `CatalogStore` | Persist catalog entries/evidence, per-path current usability and historical first-confirmed usability, refresh status, model-level announcement events and durable advancement records. Model identity is stable across paths and refreshes; an unknown check does not erase historical discovery. |
| `FollowAdvancer` | From each following profile's expected revision/configuration, choose the newest confirmed usable specific version in the same provider/tier. Verify current version, candidate ordering, adapter compatibility and unchanged path-evidence revision before atomic CAS. Never change target, connection, backend, axes/tools or instance pins. |
| `RefreshService` | Fetch/normalize/probe/advance with per-slice/path isolation; publish cache changes, durably record results/events, then expose `RefreshReport` and invoke `Announcer`. A failed notification cannot undo advancement or mark model discovery failed. |
| `RefreshLoop` and admin MCP | Startup pass then every four hours. `model_catalog_list` exposes cataloged/confirmed-usable/unknown states, evidence and unmatched targets; `model_catalog_refresh` requests a manual pass through the same lock. Public tools authorize in the service. |

### Identity, tier and version contract

A follow **provider + capability tier** is stable across releases; capability
tier is neither `ContextWindow`, reasoning `Effort`, nor pricing tiers. The
[models.dev source schema](https://github.com/anomalyco/models.dev/blob/dev/packages/core/src/schema.ts)
has model IDs, release metadata and optional family/canonical identity metadata;
it does not supply Arbitron's follow-tier contract or a ready-made sortable
version key. Decisions D2 supplies the provider-specific full-match pattern table and numeric
ordering policy. P3T8 supplies its persisted admin-editable source; P3T1 consumes
that source plus parsed release_date/canonical_model_id from P3T2. An LLM inventing a tier or version key from
a name is not a producer. Adding admin target slugs to a prompt is not evidence
that a new model belongs to those tiers.

Within the same target, compare captured numeric version tuples first and use
release_date only to break equal numeric versions. Different numeric versions
remain orderable without dates; equal versions with missing/equal dates cannot
advance. Unmatched patterns remain cataloged but never followable; string-ID ordering is
not a release chronology. If the current model cannot be mapped (for example a
mutable `sonnet` alias), keep it and expose the unresolved comparison. Do not
advance merely because the old ID is absent from the latest catalog, and never
downgrade when a newer current version disappears or becomes unavailable.

### Actual execution-path contract

`provider.EffectiveBackend` describes write-time capability validation, not the
full runtime route. `AgentCallExecutor.Execute` applies node > profile >
deployment backend precedence. Interactive paths use
`adapter/provider/interactive_stream_selector.go:InteractiveStreamSelector.StreamProviderFor`
and must be evaluated according to its actual supported routes. Chat uses
`Factory.ChatProviderFor`; agent execution uses `Factory.AgentProviderFor`.
Codex ignores the connection/provider and authenticates through its subscription
environment; a connection list cannot confirm its models. Pi/OpenCode require
their own provider/configuration/env mappings. A connection-backed Claude Code
path still needs harness evidence, not just `ConnectionProbe.Models`.

Probe all discoverable catalog candidates on configured paths independently of
advancement. “Cataloged” means stored source-backed membership; “confirmed
usable” means verified on at least one configured path, even if tier/order
metadata is still unknown. A Claude smoke check restricted to candidates newer than a following
profile would miss models with no followers and is insufficient. Bound every
check, disable tools for smoke calls except for D10's restricted Codex contract, use an isolated temporary work directory,
keep credentials in the existing injection seams, and require a verified result
for the requested specific model without silent fallback. Persist confirmation for an unchanged path/configuration identity, without
time expiry (D3–D6). Probe a candidate once on that identity; failed/unknown
checks retry later, and configuration/credential/account/binary changes create
a new revision needing confirmation. Recheck the path/configuration revision at advancement so a
connection, credential, account or binary change cannot reuse an earlier green
probe. P3-PATHS must supply this revision and coordination with those updates.
`backendhealth.Checker.Backends` currently reports only Codex;
absence of another harness's health entry is not a ready verdict.

A single profile model field must remain valid for all supported configured
execution paths that consume that alias, including node overrides. If different
paths require incompatible model strings, or a relevant path is unknown, leave
the profile unchanged and surface the conflict. Plans must complete P3-PATHS and
P3-IDENTITY rather than write one path's native ID into every consumer blindly.

## Data flow and Phase 4 handoff

```
startup / four-hour tick / manual trigger -> single-flight refresh
  -> URL -> all provider slices -> normalized catalog/evidence
  -> configured path inventory -> lists + confirmed usability -> matching
  -> eligible following profiles -> atomic CAS + advancement journal -> cache publication
  -> durable announcement events -> RefreshReport -> Announcer
new run -> Phase 1 coherent profile snapshot -> current specific model pins
```

`RefreshReport` carries refresh ID/time, per-slice/path status, newly confirmed
usable model identities with provider/tier (tier may be unknown for catalog-only
entries), all confirmed paths and exact committed profile changes (alias,
from/to IDs, canonical destination identity and revision). Phase 4 groups by that
canonical identity, not by mismatched native-ID strings.

Durably capture the first usable observation, announcement eligibility and
original payload even with no advancements. A CAS and its advancement journal
must commit together through the repository transaction seam; retries recover
the actual change rather than reconstruct it from a later profile snapshot.
Snapshot the Phase 1 internal webhook setting when classifying first discovery:
configured -> pending event; empty -> suppressed event; read error -> preserve
an `awaiting_setting` event without pretending the setting was empty.
Pending events survive restarts and the interim no-op announcer. They are read
from storage on retry, not solely from a future cycle's `newly usable` delta.
There is no first-refresh baseline flag. First discoveries with a configured
webhook are eligible on the first successful refresh as on later ones.
Event states shared with Phase 4 are `awaiting_setting`, `pending`, `in_flight`,
`delivery_unknown`, `announced`, `suppressed`. Claims and delivery transitions
belong to Phase 4; discovery never overwrites them. If classification initially
failed, retain the observation and original payload and retry the setting read;
classify on the first successful read without dropping that observation.

## Spec coverage across phases

| Spec requirement/outcome | Delivery contract |
|---|---|
| Four-hour catalog refresh, including Claude Code/Codex/Pi/OpenCode; cataloged versus usable | Phase 3 fetch, inventory, evidence-backed probes and scheduled/manual loop. |
| Admin creates a specific-version or provider/tier-following profile | Phase 2 mode/target surfaces and initialization; Phase 3 eligible-candidate producer. |
| Eligible following profiles use newer usable models by the next successful refresh | Phase 3 selection/CAS; Phase 2 confirmed cache publication; Phase 1 new-run snapshot. |
| Pinned profiles and active runs stay stable; refresh failure/no eligible replacement retains working model | Phase 2 pinned guard; Phase 3 conservative eligibility/failure isolation; Phase 1 immutable pins across all execution/continuation paths. |
| Editable Teams webhook starts empty; each newly usable model gets at least one announcement with changed profiles, if any | Phase 1 persisted empty setting; Phase 3 installation-wide durable events/journal; Phase 4 sender and durable at-least-once retries (resolved D7), including accepted duplicates after ambiguity. |
| No harness install/update, automatic provider/tier change, active-run model change or supplied webhook | Explicit constraints in Phases 1–4; CAS updates model/revision only and never run pins. |

## Data Provenance

| Leaf input | Existing source or unresolved producer |
|---|---|
| Catalog URL and raw announcement setting | **PLANNED PRODUCER P1-SETTINGS (P1T1–P1T2)**: Phase 1 persisted service/internal reader. Existing admin boundary sources are listed there. |
| Raw provider/model JSON | External `https://models.dev/api.json` (or admin-supplied URL); upstream `packages/core/src/schema.ts:Provider`, `Model`. Existing `adapter/modelcatalog/fetcher.go:Fetcher.Fetch` supplies guarded raw HTTP; P3T2 supplies the missing parser/normalizer and captured schema-compatible metadata. |
| Catalog provider/tier/specific version/order and current-model equivalence | **RESOLVED P3-IDENTITY** (decisions D2): no repository producer defines these mappings or sortable version evidence; upstream family/release/canonical metadata does not by itself establish Arbitron tier membership or alias/deployment mapping. Shares immutable alias evidence with P1-VERSION. |
| Profiles/current models | `domain/provider/profile_store.go:ProfileStore.List`, `Get`; new targets/revisions/internal read path are **PLANNED PRODUCER P2-PROFILES (P2T1–P2T3)**. |
| Connection endpoint/provider/extras and credentials | `domain/provider/connection.go:ConnectionStore.List`, `Get`, `Reveal`, implemented by `adapter/sqlite/connection_repo.go:ConnectionRepo`; `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve` supplies lazy key injection for profiles. |
| Runtime backend/default/overrides | `domain/workflow/workflow.go:NodeConfig.Backend`, `ModelProfile`; `domain/config/config.go:LLMConfig.DefaultAgentBackend`; `domain/engine/executor_agent.go:AgentCallExecutor.Execute`; `adapter/provider/factory.go:Factory.ChatProviderFor`, `AgentProviderFor`; interactive selector named above. Existing `adapter/provider/path_inventory.go:ExecutionPathInventory.Snapshot`, `PathsForProfile` and `adapter/filesystem/workflow_repo.go:WorkflowRepo.LoadManifests` supply global inventory inputs. P3T3 extends mode/default-harness and policy-independent route requests; P3T7 completes refreshed eligibility/root sinks. |
| Binary/environment/config for probes | Existing `adapter/provider/factory.go:Deps`; `adapter/codex/env.go:codexEnv`, `adapter/pi/env.go:piEnv`, `adapter/opencode/env.go:opencodeEnv`, `adapter/opencode/config.go:buildConfig`, `adapter/claudecli/chat_adapter.go:ChatAdapter.spec`. These are package-private seams to reuse/extend within their adapters, not callable domain APIs. |
| Connection-listed IDs | `adapter/probe/factory.go:ProbeFactory.ProbeFor` -> `domain/provider/probe.go:ConnectionProbe.Probe`, `ProbeResult.Models`, `ModelsUnsupported`; actual adapters in `adapter/probe/`. These list provider IDs only. Claude CLI probe runs `--version` and returns `ModelsUnsupported`, not model IDs. |
| Codex accepted model IDs | **PLANNED PRODUCER P3-CODEX (P3T4)**: D4 requires the actual account `models_cache.json` `.models[].slug` filter and a correlated session rollout reader proving the probe turn_context.model. P3T4 implements both using the same resolved home/environment as real execution; neither app-server model/list nor request echo is a substitute. |
| OpenCode accepted model IDs | **PLANNED PRODUCER P3-OPENCODE (P3T4)**: D6 requires real injected run configuration plus bounded `opencode export <sessionID>` and matching assistant providerID/modelID. Listings are optional diagnostics, never a prerequisite or exact-model proof. |
| Pi accepted model IDs | **PLANNED PRODUCER P3-PI (P3T4)**: D5 requires real stdin/print/json/no-session launch and assistant message_end provider/model matching; Pi catalog/list membership is explicitly not required. |
| Confirmed executable models on every path, including Claude subscriptions | **RESOLVED P3-USABILITY** (decisions D3–D6): model-specific confirmation/smoke adapters and supported-listing guarantees are absent. `adapter/claudecli/chat_adapter.go:ChatAdapter.Chat` is a real bounded/tool-suppressed call seam, but echoes `req.Model` and is not proof of an immutable resolved alias or absence of fallback. |
| Harness health | `adapter/backendhealth/checker.go:Checker.Backends`, `adapter/backendhealth/backendhealth.go:CodexEntry`, `adapter/codex/readiness.go:Readiness` provide Codex health only; logged-out detection has an explicit unverified assumption in source. Other readiness evidence and confirmation belong to P3-USABILITY; never infer usable models from this health check alone. |
| Guarded outbound connections | `adapter/notification/ssrf_guard.go:ssrfGuardedDialContext` and `adapter/notification/teams_notifier.go:NewTeamsNotifier` are existing patterns. Cross-package reusable fetch client/redirect policy must be built under P3-CATALOG, since the dial helper is package-private. |
| Clock/cancellation | Standard-library `time:Now`, `time:NewTicker`, `context:WithCancel`; loop integration follows `domain/schedule/evaluator.go:Evaluator`. |
| Historical usability, durable changes/events and RefreshReport | **PLANNED PRODUCER P3-STATE (P3T5)**: new catalog store/transactions/report producers do not exist. Implement before Phase 4; do not label a future report as an existing source. |

## Integration and plan obligations

Implement each flagged producer or escalate missing evidence; do not hide a
missing source under “derived,” “and/or,” or fixture-only CLI output. Wire a
server-side `ProbeFactory`, catalog store/services/loop and P4T2's concrete durable `Announcer` in
`cmd/arbitron/model_refresh.go:newModelRefresh`, before Start. Explicitly staged tests may defer durable pending events; a deferred/no-op announcer is never the production release default.
Register admin tools in MCP. Define schema/size/time/evidence-expiry limits,
transactional advancement journal and report contracts. Extend Phase 2's CAS
repository seam for that journal. Four-hour eligibility must not depend on a
Sonnet subscription, a manual normalizer profile edit or a harness upgrade.

## Error handling

Fetch failure retains catalog/profiles; slice validation failure retains that
slice and inhibits its advances while other slices proceed. Probe/list parse
failure, missing mapping, missing required Codex cache or not-ready harness means unknown and prohibits
advancement through that path. Optional Pi/OpenCode listing failure does not
replace or block a successful exact-model probe under D5/D6. Record failed checks and
retry later. Missing current-version evidence never triggers a blind replacement.
CAS conflict skips that profile; cache-publication failure is recorded/repaired
before refresh success. Persistence failure never yields a falsely successful
report. Single-flight prevents overlapping cycles. Announcements have their own
status and retry even when subsequent discovery fails. The loop continues after
all failures; active-run pins are never touched.

## Testing — strict TDD

Write failing behavioral tests first, confirm the intended failure, implement to
green, then refactor. External-source verification is a separate prerequisite;
fake executables cannot prove that an assumed command exists.

- **Happy path:** a newer same-provider/tier version becomes confirmed usable;
  that refresh advances/publishes the profile, new runs use it and active runs
  retain pins.
- **Success:** each of Claude Code, Codex, Pi and OpenCode uses its real path;
  connection-backed and subscription cases; usable models without followers
  are discovered and generate a durable event with no profile changes.
- **Failure:** unavailable/unknown candidates, wrong provider/tier, incompatible
  capabilities/native IDs and pinned profiles never advance; incomplete source
  metadata stays visible without invented tier/version data.
- **Error:** fetch timeout/SSRF/redirect/oversize, malformed listing/output,
  credentials rejected, harness readiness fault, CAS conflict, store/journal
  failure, reload failure and concurrent refresh request.
- **Edge cases:** first refresh with/without webhook; no following profiles;
  partial slice failure; model on multiple paths; provider prefixes containing
  `/`; Azure deployment/Bedrock/native identifiers; node override/deployment
  default; unchanged confirmation reuse/credential revision change; unknown or absent current
  model, equal dates/versions, no downgrade; restart recovers pending events and
  committed changes after a crash before report emission.

Use domain fakes, verified source/CLI fixtures, isolated fake executables,
SQLite transaction tests and integration tests across Phases 1–4. Include an
installation with no Anthropic/Claude normalizer path.

## Implementation alignment (2026-09-29)

- P3T1: [2026-09-29-latest-model-profiles-implementation-phase-3-task-1](../plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-1.md).
- P3T2: [2026-09-29-latest-model-profiles-implementation-phase-3-task-2](../plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-2.md).
- P3T3: [2026-09-29-latest-model-profiles-implementation-phase-3-task-3](../plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-3.md).
- P3T4: [2026-09-29-latest-model-profiles-implementation-phase-3-task-4](../plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-4.md).
- P3T5: [2026-09-29-latest-model-profiles-implementation-phase-3-task-5](../plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-5.md).
- P3T6: [2026-09-29-latest-model-profiles-implementation-phase-3-task-6](../plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-6.md).
- P3T7: [2026-09-29-latest-model-profiles-implementation-phase-3-task-7](../plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-7.md).
- P3T8: [editable pattern policy](../plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-8.md).

The execution/coverage map in `2026-09-29-latest-model-profiles-design-phase-3.md` is authoritative for cross-phase ordering. A planned producer closes task ownership; its implementation and final-sink tests remain required before release. Every task has named happy/success/failure/error/edge tests and a final-sink wiring obligation.

### Task graph, migration ownership and requirement coverage

The executable dependency order is acyclic and follows producer ownership:

1. P1T1 -> P1T2; independently P2T1 -> P3T8 -> P3T1 -> P2T2.
2. P2T2 + P3T8 -> P3T3 -> P2T3; P3T1 + P1T1 -> P3T2.
3. P3T1 + P3T3 -> P3T4; P3T1 + P3T3 -> P1T3.
4. P1T3 + P3T4 -> P1T4. P2T3 + P3T1–P3T4 -> P3T5 -> P3T6.
5. P3T2 + P3T5 + P1T1 -> P4T1; P3T5 + P3T6 + P4T1 -> P4T2.
6. P1T2 + P1T4 + P2T2–P2T3 + P3T1–P3T6 + P3T8 + P4T1–P4T2 -> P3T7 production root/local sinks. Staged Phase 3 tests can defer delivery, but final root tests require the concrete service.
7. P3T7 local runtime/sink gates + P4T1–P4T2 -> P4T3 local receiver/sink gates.
8. P3T4 D10 local acceptance + P3T7/P4T3 local gates -> P4T4 suite repair -> P3T7 full-suite acknowledgement and P4T3 final release verification. Those final acknowledgements are acceptance gates, not prerequisites for P4T4. D11 forbids treating feature-branch reds as an accepted baseline.

P2T2 defines/consumes selection ports, while P3T6 supplies the candidate runtime;
P3T7 performs production injection before seed/create. Earlier tasks test ports
with checking fakes and do not depend on later root integration to implement
contracts. Root tests land when the downstream runtime exists. P3T8 defines a
narrow MutationGate port, later satisfied by P3T3 without importing provider.
DiscoveryPipeline collects data without calling profile mutation/candidate
service recursively. Network work occurs before mutation leases; commit leases
cover only snapshot/revalidation/persistence/publication.

| Requirement / producer | Owning tasks | Real composition / final test sink |
|---|---|---|
| Persisted URL/empty webhook, admin boundaries/internal raw reads | P1T1–P1T2 | existing initDomain/httpDeps/mcpDeps -> SQLite reopen -> real fetch URL/receiver destination |
| NULL legacy versus immutable new pins, child/rerun snapshots | P1T3–P1T4 | trigger/create before enqueue -> all five consumers -> exact provider/harness/continuation outputs |
| Selection fields, sparse patches, YAML/admin validation | P2T1–P2T2 | P3T7 injects version/candidate ports before seed; persisted catalog -> seeded/public profile -> executed model |
| CAS+journal, expected admin mutations, confirmed cache publication | P2T3 + P3T5–P3T6 | shared transaction helper + MarkPublished -> new-run model; old-run pin unchanged |
| Admin-editable D2 full-match rules and revision | P3T8 | SQLite seed/admin MCP -> real PolicySource -> mapper/selection -> durable profile/new execution |
| Identity and numeric order/date tie-break/version recognition | P3T1–P3T2 | parsed release_date/canonical_model_id -> mapper -> historical catalog -> selected model |
| Guarded source fetch and deterministic normalization | P3T2 | configured URL -> fetched bytes -> parsed entries -> persistence/new execution |
| All-scope/no-followers/active-snapshot inventory and revisions | P3T3 | real repos/config/factory/filesystem/secret -> mutation gate -> transactional stale-evidence rejection |
| Exact probes, Codex account cache/rollout, OpenCode export | P3T4 + P1T4 | real launcher/env/filesystem readers -> persisted confirmation + actual pinned execution/session |
| Durable catalog/history/events/reports and original changes | P3T5 | same DB CAS+journal/events -> restart recovery of original payload, publication and evidence |
| Cold-seed/candidate initialization, four-hour refresh and admin tools | P3T6–P3T7 | shared pipeline/coordinator -> real startup/ticker/manual -> executed selected model |
| Guarded synchronous card/parts and 2xx classification | P4T1 | raw internal setting + frozen payload -> non-deduplicating receiver |
| Durable claims/backoff/ambiguous retry/part acknowledgement | P4T2–P4T3 | startup/same four-hour loop (including discovery failure) -> receiver union + announced state |
| D10 restricted Codex probes and independent resource bounds | P3T4; P3T7/P4T3 sinks | actual batch/interactive argv/config/account -> correlated proof -> durable evidence/new-run execution/card |
| Feature-induced suite regressions and final green gate | P4T4; P4T3 final acceptance | unchanged production root -> complete Go/race gates; never disable discovery or exact-proof requirements to repair fixtures |

Migrations 019 settings, 020 pins, 021 profiles/journal, 023 revisions/model_state_meta,
024 connection_revision_floor and 025 identity policy already exist in this checkout.
P1T3 preserves 020 and owns new 031 pin-route exceptions; P3T5 extends existing 022 catalog/events. RunMigrations checks
each applied version, so test adding these gaps to an existing DB with 024
recorded as well as a fresh install. Do not edit applied 019/020/021/023/024/025 SQL.
P3T8 preserves existing 025 identity policy; if event schema 022 has already shipped when
P4T2 runs, extend existing 026 retry/part bookkeeping with a new migration if necessary. Never reuse
024 for attempts. Regenerate every affected handwritten/generated conversion.
The existing P3T3 model-state secret stays for keyed fingerprints; D7 eliminates
receiver reconciliation and encrypted original-destination attempt requirements.

### Remaining dependency provenance (resolved 2026-10-01)

| ID | Consumer / missing input | Decision/evidence still needed |
|---|---|---|
| P1-LEGACY | P1T3–P1T4: starting immutable versions for pre-pin instances | **RESOLVED 2026-10-01** (D1: pre-migration runs have a NULL pin record and keep live resolution until they finish) — see `2026-09-29-latest-model-profiles-decisions.md` |
| P3-IDENTITY | P3T8/P3T1, P2T2 and P3T6: follow-tier membership/order and supported explicit-version recognition | **RESOLVED 2026-10-01** (D2: curated full-match pattern table per catalog provider/tier; numeric version order, release_date tie-break) — see `2026-09-29-latest-model-profiles-decisions.md` |
| P3-USABILITY-CLAUDE | P3T4 / run pin enforcement: definitive exact-model Claude Code execution evidence | **RESOLVED 2026-10-01** (D3: probe with --model X, no --fallback-model; usable iff X is a key of modelUsage) — see `2026-09-29-latest-model-profiles-decisions.md` |
| P3-USABILITY-CODEX | P3T4: definitive exact-model Codex execution evidence | **RESOLVED 2026-10-01** (D4: account model list filter + probe; usable iff the rollout turn_context.model is X) — see `2026-09-29-latest-model-profiles-decisions.md` |
| P3-USABILITY-PI | P3T4: definitive exact-model Pi execution evidence | **RESOLVED 2026-10-01** (D5: probe in the real launch mode; usable iff the assistant message provider/model match) — see `2026-09-29-latest-model-profiles-decisions.md` |
| P3-USABILITY-OPENCODE | P3T4: definitive exact-model OpenCode execution evidence | **RESOLVED 2026-10-01** (D6: probe, then opencode export; usable iff assistant providerID/modelID match) — see `2026-09-29-latest-model-profiles-decisions.md` |
| P4-DELIVERY | P4T1–P4T3: ambiguous-send retry policy (receiver deduplication not required) | **RESOLVED 2026-10-01** (D7: at-least-once; 2xx marks delivered; retry ambiguous sends; rare duplicates accepted) — see `2026-09-29-latest-model-profiles-decisions.md` |

A smoke success cannot establish that every later agent/interactive turn remains
on that version. P1T4/P3T4 must enforce the verified strict-model contract at
actual execution and continuation. [Claude Code model configuration](https://code.claude.com/docs/en/model-config)
documents fallback behaviors, so request-model echoing is particularly insufficient.
All seven policy decisions were resolved on 2026-10-01; P3T8 additionally closes the editable-policy producer task gap. `2026-09-29-latest-model-profiles-decisions.md` is the authority and wins over
any conflicting text in the plans. The rest of this rule still holds: no task may
mark a contract met by a synthetic fixture, silently disable support, or convert
unknown evidence to success. All other design dependency IDs now name concrete producer tasks.

### Shared contract closure

P3T8 supplies PatternRule/IdentityPolicy/PolicySource; P3T1 owns CatalogModel/Entry,
VersionRequest and IdentityMapper.Map(ctx,CatalogModel). P3T2 parses optional
release_date/canonical_model_id into that input; P3T5 persists them and historical
current metadata. P3T3 supplies PathSnapshot with a provider.ExecutionRevision
token, extending AdvanceRequest/EligibleCandidate; P2T3/P3T5 verify its persistent
policy/connection revisions in the same transaction, and the gate verifies
external fingerprints. P3T6 extends DiscoveryPipeline.Collect(ctx,prospective) returning CycleCommit
without profile mutation or premature event commit. P3T7 constructs all real
sources and injects them before seed, recovery and scheduler start. P1T4 depends
on P3T4 shared exact-model parsers for actual execution/continuations. P4T1/P4T2
share AnnouncementPart/partition plan and durable part acknowledgements.

Every leaf above is owned by a task or an existing file:symbol. Unsupported
opaque aliases/deployments have an implemented error contract, not an assumed
mapping source. D1 and D7 are explicit owner-approved exceptions, reflected in
the spec and tests. No additional human decision is hidden behind a fixture or
missing collaborator.

The shared settings.InternalReader port is added by P3T6 and implemented by the
existing settings.Service (P1T1); loop Now/NewTicker and SetAnnouncer are owned by
P3T6 and injected by P3T7/P4T3. Multipart lease renewal, persisted full Parts and
confirmed indexes are owned by P4T2, produced from P4T1's deterministic partitioner.

P3T5 also owns PublicCatalogView/ProjectCatalog; P3T6's authenticated List returns
that safe projection, while CatalogStore.Snapshot supplies trusted internal data.
P3T3 explicitly extends the existing model_state.go bootstrap to produce a keyed
fingerprint closure; Crypto alone was not a fingerprint-source producer.

### Alignment review (2026-10-01)

This worktree has September 29 filenames for the spec/design/plan hierarchy;
the requested October 1 design/plan filenames are absent. This review updates
the existing hierarchy and follows the October 1 decision record. Checked the
actual initDomain/httpDeps/mcpDeps, model-state bootstrap, profile/cache/store,
instance repository/migration runner and harness launch/session seams. Also
checked the [current upstream models.dev schema](https://raw.githubusercontent.com/anomalyco/models.dev/dev/packages/core/src/schema.ts):
release_date and canonical_model_id must be explicitly parsed and fed, not
inferred from an ID-only mapper. Implementation tasks still record source
versions/fixtures and execute their own red/green tests.

Reviewed all 18 tasks for happy/success/failure/error/edge coverage, explicit
producers and production wiring. Document references and task dependency graph
are checked for missing references/cycles; code implementation remains future
work except the previously recorded completed slices. All newly added contracts
have owning tasks and final-sink tests; no producer hole is accepted as a no-op.

## Alignment closure (2026-10-16)

The earlier execution evidence remains historical; unchecked tasks are remaining review obligations, not evidence that every listed file is absent. Existing pin, identity policy, inventory/gate/HMAC and atomic advancement implementations are extended in place. P3T5 uses the existing four-argument commitAdvanceTx(ctx,tx,normalizedExpected,request); cache SnapshotProfiles is the existing alias-keyed map API. WorkflowScopeSource includes both ListScopes and LoadManifests. ExecutionGate is the existing concrete provider type, injected by pointer.

P2T1 extends the candidate port with proof-returning ValidateStart, route-only VersionRequests and read-only RevalidateStart; P2T1 owns PreparedSelection; P2T2 owns prepare-outside-lease orchestration; P2T3 owns validated SQL selection writes. Existing ExecutionRevision lives in domain/provider/execution_gate.go and is reused. P3T3 supplies actual batch/interactive Mode, policy-independent VersionRequests, and prospective PathsForProfile revisions, distinct from Snapshot's current revisions. P3T6 recomputes prospective proof under the caller's mutation lease without network/recursive locks; automatic selection uses Snapshot + WithCurrent. P3T7 supplies these ports to the actual seeded/admin/trigger paths before any serving/recovery. No new cyclic prerequisite is introduced: early service tasks use checking ports, and the final runtime integration lands in P3T7. P1T4's runtime/snapshot inventory extension follows P3T3; no P3T6 dependency is added to trigger pin capture.

CatalogSlice.Models is populated by P3T2 from the source, persisted historically by P3T5, and re-mapped at one policy revision by P3T6 for both current and candidate comparisons. P3T6 produces CycleCommit.UnmatchedTargets, persisted/projected by P3T5 and exposed through P3T7 admin tools. Execution inventory includes the deployment-default harness even on an empty installation and distinguishes interactive launch support. P3T5 stores per-path/config ProbeCursor; P3T6 feeds fair bounded discovery and commits completed evidence on degraded budget exhaustion so repeated failures cannot starve new models. P3T7 tests each producer through durable usability/profile/new-run sinks, including restart and policy edits.

P4T2 completion APIs take explicit clock time, persist the P4T1 deterministic full part plan at first claim and retain ownership until all parts finish or a retryable failure occurs. P4T1 honors any received 2xx independently of its body and excludes internal proof paths from cards. P4T3 tests clock/backoff, destination changes between parts and secret-free receiver output through the common build/start lifecycle. These are owned implementation changes, not accepted no-ops or missing human decisions.

Publication recovery includes direct advancements without a report: P2T3 PublicationJournal is implemented by P3T5 ModelCatalogRepo using migration 022's model_publication_acks table. P3T6 replays only publication/acknowledgement, P3T7 injects the shared port and tests the executed-model sink. Settings URL-read failure aborts collection; a separate post-collection eligibility-read failure freezes awaiting_setting observations, so it cannot be confused with an empty webhook.

## Current closure map and remaining implementation work (2026-10-18)

This section replaces historical absence claims and competing interface sketches above. All leaves below have an existing producer or an owning task; none is a waiver of its unchecked implementation/tests. Existing assembly is cmd/arbitron/model_refresh.go:newModelRefresh, with live lanes in adapter/probe/model_usability.go and interactive_usability.go, DiscoveryPipeline/RefreshService in domain/modelcatalog/refresh.go, CandidateService in candidate_source.go, coordinator/loop in loop.go and authorized AdminService in admin.go. Extend those.

| Contract / production input | Producer → consumer / wiring | Required final sink |
|---|---|---|
| Captured workflow and coherent profile models | P1T3 ModelSnapshotter.SnapshotModels(ctx,aliases,wf) and existing WorkflowVersionRequestSource.VersionRequestsForWorkflow → instance create; P1T4 root | Trigger ignores unrelated saved workflows, exact pins/output or D8 configured ID survives restart |
| D8 opaque exception, without invented identity evidence | P1T3 ModelPinSnapshot.OpaqueRoutes → Instance.ModelPinRoutes (new 031) → PinSet.OpaqueRoutes → P1T4 ResolvedBinding.OpaquePinRoute/factory/selector | Native pinned Azure/Bedrock executes; strict-to-opaque edit, following and probes reject |
| Existing all-route identity/config inputs | P3T3 inventory/global VersionRequests/PathsForProfile, actual launchers/config files/HMAC → selection/commit; workflow-scoped method only for capture | Config edit rejects stale SQL advancement; rollout growth/model-only/policy-only change retains confirmation |
| Complete native candidate universe | P3T2 CatalogSlice.Models/Entries → P3T6 probe_schedule full-source queue on every compatible path, including follower paths → existing P3T4 lanes | Older/another-tier/unknown-tier model reaches durable usable event and P4T3 receiver |
| Durable cold prospective confirmation | P3T6 Collect(ctx,*Profile) (CycleCommit,error) + shared Coordinator.Do → CommitCycle(no advances)/MarkPublished → CandidateService | Empty first boot seeds catalog-selected aliases and executes them without restart; proof persists |
| Replacement capability validation | P2T3 ProfileService.ValidateAdvance → mandatory P3T6 AdvanceValidator → automatic proposal → existing CAS | Incompatible Y skipped; compatible Z persists/publishes/executes |
| Publication recovery on every cycle | Existing P3T5 journal/report rows → P3T6 Recover inside coordinated Refresh → publisher/ack | Next actual tick/manual call repairs failed direct no-report and cycle publication, even on fetch failure |
| Shared clock, lifecycle and guarded I/O | P3T7 ModelDependencies → existing Now/NewTicker, real HTTP transports/CommandRunner, build/Start/Close used by serve/MCP | 4h tick and shutdown tests reach output/receiver plus durable ack/state; no direct method substitutes |
| Durable receiver input | Existing P4T1 sender/partitioner and P4T2 repo/service constructed at newModelRefresh → mandatory P3T6 Announcer before P3T7 Start | P4T3 complete original receiver/announced/part union with active X/new Y execution |

The producer order remains P2T1 → P3T8/P3T1 → P2T2 port orchestration → P3T3 → P2T3 and P3T4/P1T3 → P1T4; P3T5 types/storage can follow those local contracts; P3T6 consumes them; P3T7 owns root acceptance; P4T1/P4T2 local sender/store contracts precede final P4T3. P1T3 consumes P2T2 route-validation semantics through already-defined ports, not the later candidate runtime. P3T4 durable refresh/restart acceptance is owned downstream by P3T6/P3T7 (D9). No upstream task waits on the root acceptance test as its implementation prerequisite.

Preserve current APIs where already implemented: internal RefreshService.Refresh, authorized AdminService.List/RefreshNow, RefreshCoordinator.Refresh/Close plus new Do, RefreshLoop.Run(ctx) with Now/NewTicker seams. Collect gains the prospective parameter and returns existing CycleCommit, not a parallel DiscoveryInputs DTO. CandidateDeps gains Pipeline/Coordinator/Gate/Mapper and loses uncoordinated direct-probe production; read-only revalidation uses persisted proof and a single current policy. P3T7's ModelDependencies fields are FetchTransport/ProviderTransport/AnnouncementTransport http.RoundTripper, Runner claudecli.CommandRunner, Now func() time.Time, NewTicker func(time.Duration) (<-chan time.Time,func()), ModelStateSecret []byte. Production defaults use real guarded clients/launcher/clock; test seams replace I/O leaves only.

Applied migrations now include 019–030, including 027 revision floor, 028 policy snapshot exclusion, 029 key verifier and 030 seed recovery. P1T3 reserves new 031 for pin-route metadata; no task reuses 027 or rewrites an applied migration. Existing 022/026 state producers are preserved. Every changed field has persistence/reopen coverage and a root input-removal control. Historical failing fixtures induced by pin/gate requirements are repaired with valid dependencies under P3T7/P4T3; full release checks are not waived.

P2T2 owns ValidatePinRoutes(ctx,Profile,[]VersionRequest,VersionResolver) (string,error), shared with P1T3. It returns a canonical native opaque family only for a valid pinned-only D8 route set; normal requests still use SpecificVersion. P1T4 verifies that family at the final native chat/stream selector, never exemptions for an agent backend. All-route following eligibility keeps the full source candidate set available: per-cycle probe budgets may limit work, but final selection cannot permanently truncate to three models or hide a fourth usable candidate behind three unavailable newer ones. P3T6 tests that case and declares budget-incomplete scans degraded.

## Current decision and field closure (2026-10-19)

D10 replaces only Codex's blanket tool-free requirement: retain read-only sandboxing, no network, disabled inherited MCP/plugins and an identity-only prompt on the real account/rollout path. P3T4 owns verified flag/config recipes, batch/interactive/resume builders and local tests; P3T7 feeds those builders through the actual factory/selector. Source verification and implementation remain required, but no further human tool-policy decision is missing. Other harness restrictions and exact identity proof remain unchanged. Per-check time is at most 30s (a shorter configured timeout is allowed); per-probe stdout, rollout and export reads are independently capped at 1MiB even when ordinary executions allow more. P3T4 owns these limits and P3T7 tests their root feed.

The current checkout also provides contracts omitted from earlier sketches; preserve these producers rather than invent parallel fields:

| Field / contract | Producer → consumer | Task / acceptance |
|---|---|---|
| `AdvanceRequest.Evidence []provider.PathEvidenceRef` (`PathKey,ConfigRevision`) | P3T6 selector's actual all-consumer inventory → P2T3/P3T5 SQL evidence validation | P2T1 records the shared shape; missing/wrong refs reject without CAS/journal; real new-run sink in P3T7 |
| `AdvanceStore.ReplayAdvance(ctx,request) (Advancement,bool,error)` | existing `adapter/sqlite/profile_advance.go:ProfileRepo.ReplayAdvance` → direct service publication repair | P2T3 preserves replay before new-write validation; unavailable discovery cannot duplicate or prevent publication of an original commit |
| `UsabilityEvidence.Provider string` | existing `domain/modelcatalog/candidates.go:AdvanceSelector` sets the catalog offering's provider from checked Entry, never the routing provider → SQLite `catalog_provider` → `confirmingEntry` → frozen card | P3T4 DTO, P3T6 feed, P3T5 migration/reopen and P4T3 receiver test; an unconfirmed historical aggregator offering must not name a different confirmed offering's provider/tier |
| `CycleCommit.PolicyRevision int64` | P3T6 captured classification/inventory revision → existing `adapter/sqlite/model_catalog_repo.go:checkCyclePolicyTx` | P3T5 rejects stale classifications even with no advancements; P3T7 policy-race sink |
| `CycleCommit.DeferDiscovery []string` | existing `domain/modelcatalog/refresh.go:RefreshService` drops stale proposals → P3T5 transaction's observation classification | P3T6/P3T5 preserve evidence while postponing only affected first events until a cycle can classify actual changes; unrelated/no-follower models still announce; P4T3 receiver/retry sink |
| `modelRefresh.seedPending func(context.Context)` | existing `cmd/arbitron/main.go:initDomainWithModelDependencies` closes over real `SeedProfiles`/ProfileRepo/ProfileService and migration-030 pending originals → `model_refresh.go:startScheduled` after coordinated cycles | P2T2 recovery contract; P3T7 actual next-tick seed/execution test, including source-down first boot and no second boot |

Migration 031 pin-route metadata and migration 032 evidence-provider provenance now exist. Preserve applied 019–032, including existing 022/026; no plan may rewrite them or reuse a recorded number. A newly discovered schema defect needs the next unused migration after inspecting the checkout. Migration 032's empty provider default denotes pre-provenance evidence; do not backfill an assumed offering from today's catalog or turn unknown paths into confirmation. Existing frozen events remain immutable. P3T5 owns upgrade/reopen controls; P4T4 repairs stale fixtures against these existing contracts.
