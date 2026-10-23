# Latest Model Profiles — Phase 3 Task 2: Guarded catalog fetch and deterministic normalization Implementation Plan

> **Alignment baseline (2026-10-19):** D1–D11 govern this task. D10 permits restricted Codex probes; D11 assigns feature-induced suite repairs to P4T4. Earlier execution evidence is historical, not a current dependency inventory or permission to skip unfinished work. The current follow-through sections supersede earlier interface sketches. The existing probe/router (`adapter/probe/model_usability.go`), discovery/selection/recovery (`domain/modelcatalog/{refresh,candidate_source,probe_schedule,loop}.go`) and root assembly (`cmd/arbitron/model_refresh.go:newModelRefresh`) are extension points. Implement unchecked local obligations without waiting for downstream integration acceptance; P3T7 owns cross-phase execution sinks and P4T3 owns receiver/release sinks. See the Phase 3 design’s current closure map.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this task. Track the checkboxes and use strict red/green/refactor TDD.

**Goal:** Guarded catalog fetch and deterministic normalization.

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

**Depends on:** P1T1 (`docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-1.md`) and P3T1 identity fields; raw ingestion is already implemented; normalization now consumes D2/P3T1/P3T8 contracts.

**Files:** Create `domain/modelcatalog/catalog.go`, `adapter/modelcatalog/fetcher.go`, `adapter/modelcatalog/normalizer.go`, tests/source fixtures and `adapter/outbound/guarded_client.go` with tests. Modify `adapter/notification/ssrf_guard.go` to share exported guard where practical without changing notification behavior.

## Interfaces (owned here unless explicitly consumed)

Produce `CatalogSlice{Provider string; Models []modelidentity.CatalogModel; Entries []modelidentity.Entry; Raw json.RawMessage; Status string}` and `CatalogSnapshot{FetchedAt time.Time; Slices []CatalogSlice}`. `CatalogFetcher.Fetch(context.Context,string) ([]byte,error)`; `CatalogNormalizer.Normalize(context.Context,string,[]byte) (CatalogSnapshot,error)` (source URL, raw bytes). Normalizer parses every model into P3T1 CatalogModel (`CatalogProvider`, exact NativeID, `canonical_model_id`, `release_date`, SourceRef) and passes it to IdentityMapper.Map; retain source-backed entries with unknown tier/order and keep raw source. No metadata can stop at a fixture or get dropped by the mapper API. `outbound.NewGuardedClient(timeout time.Duration,maxRedirects int) *http.Client`; cap body reads separately in fetcher.

## Data producers and composition-root wiring

P1T1 internal CatalogURL -> HTTP producer at configured URL, default models.dev -> raw JSON fixtures from actual upstream schema -> normalizer -> P3T5 store. Current primary source is `https://raw.githubusercontent.com/anomalyco/models.dev/dev/packages/core/src/schema.ts`; verify and record schema alongside representative multi-provider catalog response. P3T7 constructs guarded client/fetcher/normalizer and feeds non-empty fetched data through store/selection.

## Strict-TDD steps

- [x] Write the following named behavioral tests in the listed test files. Use real migrated SQLite for persistence and root assembly for wiring; introduce only I/O-leaf test substitutions.

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

- [x] Independent normalization tests: captured multi-provider fetch-to-entry test, unknown/optional metadata, Unicode/slash IDs, distinct configured-source provenance, captured release-date tie-breaking under an explicit test policy, deterministic ordering, raw ownership, malformed top-level/slice isolation, empty valid catalogs, dependency faults and cancellation.

> Decisions D1–D7 are resolved in `docs/designs/2026-09-29-latest-model-profiles-decisions.md`. Follow the concrete contracts in this task and implement its named prerequisites first; unchecked work remains unimplemented.

Unblocked test progress: added exact URL/method/body assertions, known-length and chunked 20MiB overflow, exact boundary, empty body, non-2xx, truncated body, timeout/cancellation, initial credentials/scheme/private-address rejection, resolved dial/DNS rebinding guard, redirect sink/limit checks and proxy-bypass prevention. Transport substitution is private to package tests and validates the load-bearing destination.

- Happy: `TestFetchNormalizeAllProviders` guarded fixture source supplies at least two providers, including no followers; their stored catalog membership survives.
- Success: documented optional metadata omitted remains cataloged/unknown rather than discarded or fabricated.
- Failure: private/loopback/redirect target, unsupported scheme/shape, foreign schema is rejected without replacing known catalog.
- Error: timeout, oversized/chunked body, non-2xx, invalid JSON and malformed one slice preserve prior data appropriately.
- Edge: empty valid catalog, Unicode IDs, slash IDs, equal release dates, redirect limit/DNS rebinding and cancellation.
- Input guard: empty body is malformed; normalizer fake validates URL/raw bytes and returns source-backed entries. P3T7 test sees fetched candidate at final execution sink.

Assertion sketch for the named happy path (variables are observed fixture outputs; substitute fixture-backed versions where shown):

```go
assert.Len(t, snapshot.Slices, 2)
assert.NotEmpty(t, snapshot.Slices[1].Raw)
```

- [x] Run the focused gate below before implementation. Confirm each new assertion fails for the intended absent behavior (a compile failure may establish a missing API, but rerun to observe behavioral red after the API exists).

Unblocked red evidence: the focused gate first failed for missing outbound/fetch APIs; after API stubs existed it failed behaviorally for absent guards, missing downloads and ignored failure/boundary cases. The CatalogFetcher domain-port compile check also failed before adding that port.

- [x] Independent normalization red: focused gate failed for the missing Normalizer/CatalogNormalizer/snapshot APIs. After adding API stubs, reran and observed behavioral failures for absent normalization, metadata/provenance, slice isolation, empty-catalog and dependency behavior before implementing.
- [x] Implement the exact contracts above in the listed files:

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

Unblocked implementation progress: added the domain CatalogFetcher port, production Fetcher with a private guarded client (30s timeout, 20MiB read cap, 3 redirects), and adapter/outbound's URL/redirect/connect-time guard. Existing notification behavior was left unchanged rather than substituting a client with different redirect/proxy behavior.

- [x] Independent normalization implementation: domain CatalogSlice/CatalogSnapshot/CatalogNormalizer, deterministic models.dev parser, real IdentityMapper consumption, retained Models/Entries/raw evidence, explicit invalid-slice status with no replacement rows, configured-source JSON Pointer references and injected clock. Captured the complete upstream schema and unchanged representative model rows with URL/hash provenance. No mandatory normalizer model or fabricated identity metadata.

Fetch timeout 30s, 20MiB maximum raw body, 3 redirects; guard each DNS/dial/redirect against private/local targets and credentials. Test client injection is explicit and test-only. Top-level malformed shape aborts without replacing prior catalog; malformed provider slice retains prior slice/inhibits advances but other valid slices proceed. Preserve all providers, including no followers; configurable URL must speak verified same schema or return explicit unsupported format. No LLM normalizer/mandatory Sonnet.

- [x] Run the focused gate again; require exit 0 and every named test green. Refactor only while those tests remain green; format Go and regenerate sqlc for query/schema changes. Run dependent root tests when an interface changes.

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

- [x] Independent normalization green: the specified focused gate exits 0, including all new normalization tests. `go test ./domain/modelcatalog ./domain/modelidentity -count=1` and `go test -race ./adapter/outbound ./adapter/modelcatalog ./adapter/notification -count=1` also exit 0. This is not the downstream persistence/root acceptance gate.

**Focused gate:** `go test ./adapter/outbound ./adapter/modelcatalog ./adapter/notification -run "Guard|Fetch|Normalize" -count=1`. Commands separated by semicolons here are separate gates: run each and inspect every exit status. For dashboard changes run `make ui` from the repository root so embedded assets match source.

- [x] Review diff for scope, production wiring and forbidden fake shortcuts. Commit this task with `git add <listed changed files>` and a focused `feat:` commit only after verification; do not stage unrelated changes.

- [x] Independent normalization review: reviewed spec compliance first (only the owned parser/contracts/source capture), anti-green-theater second (exact configured HTTP destination, non-empty parsed/mapped sinks, canonical/date/provenance fields and mutated inputs; no persistence/root claim), code quality third (neighboring adapter conventions, deterministic iteration, owned raw bytes, fail-closed dependency/errors). Reformatted and reran focused/domain/race gates after review; commit only this verified slice.

## Provenance / release gate (resolved 2026-10-01)

No new external source policy is required for raw transport. Task completion requires implementing P3T1/P3T8, normalization and downstream persistence/root integration; there is no outstanding human identity-policy gate. Self-review ran in spec-compliance, sink-strength, then code-quality order; strengthened redirect tests assert actual received paths and reject unexpected dial targets. All implemented tests were rerun after review; no full-task completion is claimed.

The actual configured CatalogURL read by DiscoveryPipeline supplies both Fetch(ctx,url) and Normalize(ctx,url,raw). Normalizer sets SourceRef from that explicit source plus provider/model location and records FetchedAt using its injected `func() time.Time` (production time.Now); an admin URL must not silently claim the default source. IdentityMapper receives these references through CatalogModel. Add a two-distinct-source test whose raw model metadata is identical but recorded provenance differs, plus an equal-version test that would fail if release_date were dropped.

Retain the parsed CatalogModel rows in CatalogSlice.Models alongside mapped Entries; normalization fills both from the same source bytes. P3T5 persists these raw identity inputs for current and historical offerings. Add a test that optional canonical_model_id/release_date survive normalization and DB reopen, and that changing the policy reclassifies both the historical current model and the new candidate. Entries alone cannot reconstruct a missing source canonical_model_id.

## Current fetch/normalization sink

Extend existing Fetcher/Normalizer and `cmd/arbitron/model_refresh.go:newModelRefresh`; migration 022 and DiscoveryPipeline now exist. P3T7 owns a configured-source HTTP/transport leaf → Normalize → CatalogSlice.Models/Entries → ModelCatalogRepo reopen → selected new-run model test. Include a non-followable NativeID so unknown metadata cannot be filtered out before discovery. Observe red if source URL, release_date, canonical_model_id or Models is dropped, then implement any missing feed in this task's adapter and rerun its focused gate. No extra parser, store or normalizer profile is needed.

### Fetch/normalization sink completion (2026-10-18)

- Selection: P2T2's previously recorded cold-bootstrap blocker remains present: CandidateDeps still has no prospective pipeline/coordinator and reads only stored catalog. Do not re-fail it; complete P3T2's independent remaining ingestion acceptance. No bootstrap, scheduling, probe-universe or announcement work is included here.
- Red: the new `TestFetchNormalizeAllProvidersRoot` first fails compilation for the missing HTTP leaf seam; after adding the field, it fails behaviorally with no advancement because the root ignores it. Run the exact adapter-focused gate before implementation (existing controls pass). Preserve the existing production fetch/normalize/store pipeline rather than replace the fetcher in the new test.
- Minimal implementation: add transport-only constructors retaining the existing URL/redirect wrapper and all fetch bounds; feed `modelRefreshDeps.FetchTransport` into the actual root-built Fetcher. Nil keeps the existing guarded DNS/dial path and disabled proxies. No runtime flag, setting, alternate parser, persistence path, schema or UI change.
- Root sink: captured multi-provider schema-compatible bytes, with explicit synthetic new/Unicode rows, pass through real HTTP request construction, normalization, persisted policy, inventory/probes, SQLite transaction and cache. Remove X from the latest catalog and edit the policy so X/Y have equal numeric order: source-backed historical X and Y dates must select Y. Assert every provider's source rows/entries/raw metadata, unknown membership, durable profile, exact new-run request/output, and DB reopen. HTTP errors, foreign schema and empty responses retain the full catalog/history and working Y. No Claude connection or announcement webhook is supplied.
- Ordered review: spec first (only owned ingestion/seam/final-sink acceptance), anti-green-theater second (temporarily drop source URL, release_date, canonical_model_id and Models individually; each fails the new root's durable or selection sinks; dropping the root feed already reproduced behavioral red), quality third (reuse bounded fetcher/shared guards and neighboring root fixtures, owned HTTP response bytes, exact destination/auth/body checks, gofmt). Also remove the transport URL wrapper temporarily: the new guard test fails for private URLs, credentials and unsupported schemes. Restore every mutation before final verification.
- Green: exact focused gate; full adapter/outbound, adapter/modelcatalog and adapter/notification packages; full cmd/arbitron package; domain/modelcatalog and domain/modelidentity; SQLite `Catalog|Historical|Advance|ValidatedSelection`; race tests for all three adapter packages and root `FetchNormalize|ModelRefreshRoot|CatalogURL`; `git diff --check`. Source metadata mutations leave no production parser diff. P3T7's broader cold-start/ticker execution acceptance remains in that task, not a dependency of this completed ingestion slice.
