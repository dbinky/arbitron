# Paired Reviews — Workflow Auto-Arrangement Seams (branch over `345f751`)

Phase 2 of the loop. This table runs only after every single-system box in
`docs/reference/focus-areas.md` is ticked. Area numbers refer to that file.

Each row is one seam between two behaviours that meets all three conditions:

- they share persisted state, a wire contract, a lock or an ordering;
- they were built by different tasks or commits;
- a broken combination would be silent, because each side is green on its own.

One approval covers **both directions**. The reviewer follows TRACE and shows that
BREAK cannot happen, either with a test that goes red or with a guard cited at
`file:symbol`. Real-browser row 5 (and row 7 when using the real renderer) requires `ARBITRON_REQUIRE_BROWSER=1`. A skipped
browser test is not evidence.

## Paired Integration Reviews

| # | Paired Review | Seam | Risk | Status |
|---|---|---|---|---|
| 1 | Area 1 peer-crash settled reads ↔ Area 2 rename relocation | SHARED: the relocation-intent journal and `CommitSource.Settle`, which raw reads reach through `FileReaderAdapter.settled` (§6.18 settled-raw-read row, commit 5fd8afd) and which the rename skips only for its own intent through `withOwnRelocation` (§6.18 active-rename row, P2T3 move) · TRACE: process A starts `Service.Rename(x→y)` and is killed after `BeginRelocation` and the stamped copy, before `EndRelocation` → process B serves `GET` raw `workflow.json` for x and for y, then `GET /workflows/y/layout` → `FileReaderAdapter.withExisting` → `settled` → `WithMutations` → `Settle` with an external context → `RelocationIntents` → `ErrMutationPending` → HTTP 503 → registry-authority recovery → bytes · BREAK: a surviving raw read returns either half of the move (staged y bytes or the about-to-vanish x bytes) because the read context is treated as owning the intent, or because the raw-read path settles only tree commits and ignores relocation intents; or the subsequent `CommitSource.CommittedRequests` recovery sweep decides the endpoint by folder existence instead of `Registry.Resolve` (the raw read itself must return pending, never expand to the second endpoint lock) | high | [x] |
| 2 | Area 1 peer-crash settlement ↔ Area 4 retry lock set | SHARED: the destination mutation lock and the read settlement that runs inside it; `QueryService.RetryWorkflow` holds `WithMutations(targets)` and then `AuthorizeLayoutRetry` reloads through the settling `WorkflowRepo.Load` (P3T8 retry vs P2T7 settlement) · TRACE: kill a writer between the private-swap renames, leaving a prepared token T2 over committed T1 → `POST /workflows/{name}/layout/retry` with expectedRevision T2, then with T1 → `LayoutRetryTargets` → `WithMutations` → `AuthorizeLayoutRetry` → `Load` → `Settle` (roll forward or restore under the held lock) → `checkExpected` → `Requester.Retry` or refusal → `layout_revisions.job_generation` · BREAK: the retry accepts the killed writer's never-committed token T2 and bumps a generation for an invisible revision; or settlement inside the already-held lock re-acquires the same destination lock and deadlocks until timeout; or the settled outcome (T1 restored) is compared after the bump instead of before | high | [x] |
| 3 | Area 1 peer settling reads ↔ Area 3 boot seed claim | SHARED: the prepared seed journal entry and registry claim, which `CommitSource.settlePrepared` → `releaseSeedClaim` compensates, under the shared destination lock (P2T4 seeder vs P2T7/P2T9 settled reads) · TRACE: process A is seeding sample S, prepared entry recorded and `Registry.Register` done, tree not yet visible → process B (same workspace) serves a raw read or `GET` for S → `withExisting` → `settled` → `WithMutations(S)` → `Settle` → `settlePrepared` → decision on the live entry; repeat with A killed at the same point · BREAK: B treats A's still-live seed as abandoned and releases its registry claim (A then publishes an unregistered or duplicate sample), or B's settlement rolls A's prepared tree forward before proof; with A killed, B must release exactly that claim and nothing else | high | [x] |
| 4 | Area 2 rename relocation ↔ Area 4 historical retry | SHARED: the committed relocated envelope in the revision journal and the relocated `layout_revisions` destination; `QueryService.committedEnvelope` and `historicalDestination` consume what `renameRevision` and `TransferOwnership` produce (P3T8 vs P2T3/P2T7) · TRACE: run an instance on revision A of x → rename x→y (and separately transfer, and delete) → `POST /instances/{id}/layout/retry` → `AuthorizeInstanceLayoutRetry` → `committedEnvelope(A)` → `historicalDestination` → lock snapshot plus relocated destination → `Requester.RequestArrangement` or `Retry` → row destination and Manifest → worker cache publication · BREAK: the retry rebuilds the row at the old locator despite committed relocation evidence, rewrites the snapshot bytes to the new locator, adopts a prepared (uncommitted) relocation, or re-authorises against the current workflow's visibility instead of the snapshot | high | [x] |
| 5 | Area 5 stdio transport ↔ Area 6 shipped renderer | SHARED: the process's stdout. Area 5 guarantees stdout carries only JSON-RPC (P2T9.8, commit cfe19ac). Area 6's `BrowserRenderer` launches a Chromium child through chromedp's ExecAllocator in the same process (P3T7) · TRACE: `arbitron mcp` in local mode with a real browser configured → `workflow_create` → worker → `BrowserRenderer.ensureBrowser` → `chromedp.NewExecAllocator` → Chromium and its subprocesses run → assert a passed ordinary-sample SQLite result with rendered evidence to prove lazy browser launch occurred → every stdout line decoded as a JSON-RPC frame (the `runStdioSession` decoder) through EOF · BREAK: Chromium's or chromedp's output is inherited onto protocol stdout (the existing stdio protocol test runs with no browser, so it would stay green), corrupting the MCP stream as soon as an arrangement job renders | high | [x] |
| 6 | Area 3 boot seed resume ↔ Area 4 missing-row retry | SHARED: `layout_revisions` immutable acknowledgement fields, especially `requested_at`, consumed by `Requester.AcknowledgedArrangement` and `RevisionStamper.EnsureStamped`; producer/consumer were built by P3T8 retry (1b378f1) and P2T4 seed resume (315da30) · TRACE: commit a sample but fail `ledger.Record`, let enqueue remove the journal, then exercise an authorised workflow retry with the row present and with the row missing → `QueryService.retry` → `Store.Retry` or `requestArrangement` → resume seeding with the original exact publication → inspect ledger, saved bytes, token and stored acknowledgement · BREAK: the missing-row branch recreates the same revision with `RequestedAt: q.now()` instead of the captured `Presentation.SavedAt`; retry succeeds but later exact seed resume rejects the row and never records the ledger. The existing-row retry must preserve all acknowledgement fields; do not weaken `EnsureStamped` equality to make the combination pass | med | [x] |
| 7 | Area 4 authorised layout reads ↔ Area 6 rendered arrangement worker | SHARED: group ownership captured from the same SavedRevision by two consumers. The worker builds it from the row's `Manifest` (`coordinator.go:153`, commits f3b37cc/44390f5). `Reader.View` builds it from the authorised workflow file or frozen instance snapshot (`reader.go:188`, commit b59fd27). Both share the `ErrInvalidGroups` classification · TRACE: save group workflow G at revision R1 → worker fails R1 at rendering, retaining the ordinary-box best → save R2 with a reordered task list → `GET /instances/{id}/layout` for an instance frozen on R1 and `GET /workflows/G/layout` → compare `View.Groups` with the ownership the R1 renderer received (`RenderRequest.WorkflowGroups`; group v1 fails before Review is called) and with R2's; repeat with an invalid-groups saved revision and Retry · BREAK: the instance view of R1 shows R2's ownership (or the workflow view shows R1's after R2), so the API pairs geometry with ownership from another revision (the present dashboard still uses client ownership, as escalated in W1); or Reader shows an invalid-groups revision as pending or legacy while the worker stored failed at arranging (or the reverse); or the failed row's retained ordinary-box best is drawn without the group fallback and labelled reviewed | med | [x] |

## No seam

All 21 pairs of the 7 current areas are accounted for: 7 paired rows and 14
exclusions below. Collapsed candidate seams are recorded separately and are not
additional pairs.

**Inside one behaviour**

- Area 1 ↔ Area 5: stdio is an alternative raw-crash entry point; `TestLayoutMCPStdio_SharedPeerCrash` already belongs to Area 1's settlement checks.
- Area 1 ↔ Area 7: group validation finishes before the private swap/shared overwrite; exact saved input is Area 7's sink and the crash protocol is Area 1's failure path.
- Area 2 ↔ Area 3: a same-name peer move is refused by `seedRevision` → `EnsureStamped` → `CommitSource.Settle` before a fresh claim; the explicit pending/collision case is Area 3's failure lens, not a silent independent handoff. Startup order within one process alone is not a cross-process guarantee.
- Area 2 ↔ Area 5: the stdio rename peer crash is an alternative entry covered by Area 2's `TestLayoutMCPStdio_RenamePeerCrash` check.
- Area 2 ↔ Area 7: rename group validation is `precheckCopiedGroups` before the endpoint lock set; final authority/settlement remains inside `withIdentityMutation`, covered by Area 7's corrected lock-boundary checks.
- Area 3 ↔ Area 5: boot seeding logs through the already-installed logger; Area 5's whole-subprocess stdout check includes boot, while Area 3 owns seed persistence.
- Area 3 ↔ Area 6: seeded samples are Area 6's acceptance inputs; renderer construction and sample selection do not create a second seed protocol.
- Area 3 ↔ Area 7: the seed's pre-lock child-contract gate is the seed entry in Area 7; registry/ledger compensation belongs to Area 3.
- Area 4 ↔ Area 5: stdio identity/denial and retry framing are Area 5's real-subprocess checks, while the query's authorised generation path is covered by Area 4.
- Area 4 ↔ Area 7: saved JSON equality is Area 7's sink; authorised workflow/snapshot ownership is Area 4's read path. Worker-versus-reader revision agreement is paired row 7.
- Area 5 ↔ Area 7: MCP create/update is an authoring entry in Area 7; the credential-free transport's principal is already checked end to end in Area 5.
- Area 6 ↔ Area 7: Area 7 proves saved bytes equal the row's captured Manifest; Area 6 proves the worker consumes that immutable row. No separate unvalidated transform exists between these sinks/entries.

**Genuinely unrelated**

- Area 1 ↔ Area 6: rendering consumes the immutable job manifest and touches no workflow-tree intent; peer settlement is not part of browser capture.
- Area 2 ↔ Area 6: browser capture uses immutable input and has no relocation state. Destination-fenced cache publication was approved in the archived loop and is unchanged on this branch; historical reconstruction is paired row 4.

**Collapsed inside one behaviour**

- Former row 5 (seeded Areas 6 ↔ 9): STRUCK — `renderJobDocument` → `parseJob` → `buildSurfaceModel` was already the group worker's own render/review path, now Area 6 Review 1 check 11; its ordinary/group compatibility controls are retained there.
- Seeded Areas 4 ↔ 7: merged into Area 4; shared `queryDefinition`, authorisation and invalid-group retry status are internal checks, not a paired row.

DEFERRED (budget): none. 2 × 7 + 7 = 21 ≤ 175.
