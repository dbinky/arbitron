# Gaps Identified — Workflow Spawn Node

Concrete findings from the seeding sweep of
`64768283d2c32b64023801103c7aab2f5ea538d3..HEAD`. Each was found by grepping the
whole repo for the symbol and checking for a **production** (non-`*_test.go`,
non-`*.test.ts`) constructor or caller.

Tick a box only when the gap is closed in code with a test that would fail if the
production input were nil/empty. An unfed production path is a gap to **fix or
escalate** — never a tolerated no-op.

---

## Open

- [ ] **Confirm the awaited-spawn wait is invisible to the timer recovery sweep.**
  `executeWait` transitions the parent with `instances.UpdateState(ctx, id,
  StateWaiting)` and the comment claims `waiting_until` stays NULL so
  `RecoverTimers` cannot pick it up (`domain/engine/executor_spawn.go:159-163`).
  Verify against the actual `UpdateState` implementation and the
  `ListWaitingWithTimer` predicate in `adapter/sqlite/instance_repo.go` that a
  spawn-waiting parent is genuinely excluded — an untested claim in a comment is
  not a guarantee, and a false positive here would fail a parent that is waiting
  correctly.

- [x] **Verify the cascade produces an intervention record for every descendant,
  scoped to the storage-failure case only.** `cascadeCancel` routes through
  `cancelAuthorized`, which calls `recordIntervention`; the "one intervention
  row per cancelled run across a deep tree" claim and the "authorization
  failure partway down the tree does not silently orphan the rest of the
  traversal" claim were already covered (the latter fixed in a prior pass —
  see `TestCancelCascade_AuthorizationFailureOnAChildHaltsThatBranch` under
  Fixed). What remained unverified: `cascadeCancel`'s own `ListActiveChildren`
  failure (a storage error listing one branch,
  `TestCancelCascade_StorageFailureStillReportsSuccess`) only asserted the
  PARENT stays cancelled — it never asserted whether a SIBLING branch's
  intervention records are still written, i.e. whether one branch's storage
  error silently truncates unrelated branches' audit trail too. Added
  `TestCancelCascade_StorageFailureOnOneBranchDoesNotTruncateSiblingBranch`
  (`domain/engine/cancel_cascade_test.go`), which links a parent to two
  branches — `branchA` (whose OWN `ListActiveChildren` call fails) and
  `branchB` (with its own live grandchild) — and asserts all four instances
  (parent, branchA, branchB, grandchildB) end `StateCancelled` and all four
  get intervention records (`c.interventions.count() == 4`). Confirmed this is
  not a live bug: Go's call-stack semantics already scope the failure
  correctly (the failing recursive call for `branchA` simply returns after
  logging, which cannot affect the OUTER loop iterating over the parent's
  other children) — moving the test's `for` loop to `break` instead of
  `continue` on any error, or otherwise trying to construct a code path where
  branchA's failure reaches branchB, was not possible without restructuring
  the function into something no longer resembling straightforward recursion;
  this matches the shape of several other "review 2: independent
  re-verification found no live gap" entries below (areas 17, 18) where the
  fix is closing a testing gap rather than a code change. While re-deriving
  this branch's coverage, found a SEPARATE, more significant gap — the
  "already-terminal child still descends into active grandchildren" branch is
  entirely untested (logged above under Open, not fixed here to keep this pass
  to one issue). This does not close review 1 approval for area 22: the newly
  found gap above must close first.

- [ ] **A dismissed child stays counted in `ChildCount` but disappears from its
  parent's expanded row list, with no way to bring it back into view.**
  `ChildCountsByParents` (`adapter/sqlite/instance_repo.go:592-611`) has no
  concept of per-user dismissal — its `WHERE` clause is built purely from
  `childVisibilityClause` (owner/department/public), matching the deliberate
  domain/view split documented at `adapter/http/instance_handler.go:225`
  ("dismissed" is HTTP/view state, intentionally not part of the domain
  model). But `List`'s dismissed-set subtraction
  (`adapter/http/instance_handler.go:444-457`, `isDismissed && !includeDismissed`
  → `continue`) applies unconditionally, including when `filters.ParentScope ==
  ParentScopeChildren`. The dashboard's only child-fetch call,
  `useRunHierarchy.toggle` (`dashboard/src/hooks/useRunHierarchy.ts:66`,
  `api.instances.list({ parent: id })`), never passes `includeDismissed`, so it
  gets the default `false`. Net effect: a parent's expander can read
  "2 children" (from `ChildCount`, dismissal-blind) while expanding shows only
  1 row (the dismissed sibling silently subtracted) — and there is no
  dashboard affordance to pass `includeDismissed=true` for a single expanded
  level, so that child is permanently invisible via the hierarchy view once
  dismissed (e.g. by a "Dismiss All" on an unrelated top-level sweep that
  happened to catch it). Neither the spec nor any of the five phase designs
  mention dismissal at all, so there is no documented intended behaviour to
  wire against — this needs a product decision (e.g. "child fetches always
  imply `includeDismissed=true`", or "a dismissed child should also be
  subtracted from `ChildCount`") before it can be closed as a fix rather than
  a guess. Found while closing area 24 review 1; the fix, if any, belongs to
  area 27 (hook) or area 23/8 (count query), not area 24 itself — REST's own
  `List`/`ListEnriched` wiring for the `parent` selector is otherwise correct
  and this does not block area 24's own approval.

- [ ] **Three defensive branches in `OnChildTerminal` have no known reachable
  production caller and are untested.** Found while closing area 19 review 2
  (`domain/engine/spawn_wake.go`): `go tool cover` shows the `ctx == nil`
  guard (line 36-38), the `!child.State.IsTerminal()` "called too early" guard
  (line 49-51, doc comment: "the real terminal write will call again"), and the
  parent-`instances.Get` error branch (line 53-56) all at 0 hits. All four
  production call sites (`run()`'s completion, `failInstance`,
  `cancelAuthorized`, `resumeFromWebhookWait`) already write the child's
  terminal state before calling the wake and always pass a live context, so
  none of these three branches is reachable today — not a live bug, but an
  unverified defensive-code claim, the same shape of gap this loop has fixed
  repeatedly elsewhere (most recently the `SetQueued`-failure branch in this
  same review). Low priority relative to the `SetQueued` fix since none is
  reachable by a real caller; pick up if area 19 needs further passes or if a
  future caller starts invoking `OnChildTerminal` before its terminal write.

- [ ] **`EnrichedInstanceSummary.FailedNodeID` is declared twice — once
  promoted from the embedded `InstanceSummary` (tagged `failedNodeId,omitempty`),
  once again directly on `EnrichedInstanceSummary` itself (no tag) — so Go's
  shallower-field-wins rule silently drops the promoted, tagged field from ANY
  direct `encoding/json` marshal of the struct and marshals only the untagged
  one under the bare key `"FailedNodeID"`.** Found while closing area 25 review 1
  (`domain/instance/instance.go:177` vs `:208`), while confirming the sibling
  fields referenced by the `ChildCount`/MCP-field-names gap (now fixed, see
  Fixed section) really do share one untagged convention. Pre-existing —
  present unchanged at the branch point, `ChildCount` did not introduce it — so
  out of scope for this feature's own fix, but worth a decision: MCP's
  `MarshalResult(enriched)` (`adapter/mcp/tools_instance.go:139`) marshals
  `EnrichedInstanceSummary` directly, so an MCP client asking for the failed
  node ID would need to read `"FailedNodeID"`, not `"failedNodeId"` — inconsistent
  with every other tagged field on the same embedded type it hides. REST is
  unaffected (it maps through `enrichedSummaryDTO`, never marshals the domain
  struct directly). No test currently pins which key wins; a future rename of
  either declaration could silently flip client-visible behaviour.

---

## Fixed

- [x] **The `typeColors` accent map in `ArbitronNode` — including the new
  `spawn_workflow: '#2ecc71'` entry — had no test at all, for any node type.**
  Focus area 30 (dashboard graph accent for `spawn_workflow`), review 1.
  `dashboard/src/graph/nodes/ArbitronNode.tsx:8-17` is a plain
  `Record<string, string>` read at render (`typeColors[data.nodeType] ??
  '#888888'`) and applied to both the `typeBar` background and the
  `typeLabel` text color — but `ArbitronNode.test.tsx` only asserted handle
  geometry, never the accent. Confirmed by temporarily deleting the
  `spawn_workflow` entry: the entire dashboard suite (`npx vitest run`) stayed
  green, proving a typo'd or removed key for the type this feature introduced
  would silently degrade every `spawn_workflow` node in the graph editor to
  the generic grey fallback, with nothing to catch it. Added two tests: one
  asserting `nodeType: 'spawn_workflow'` renders the type bar/label in
  `rgb(46, 204, 113)` (`#2ecc71`) with the label text `'spawn workflow'`, and
  one asserting an unmapped node type falls back to `rgb(136, 136, 136)`
  (`#888888`) rather than crashing or rendering an unstyled bar — this also
  covers the pre-existing fallback branch, which was equally untested.
  Confirmed the first test fails for the right reason
  (`expected 'rgb(136, 136, 136)' to be 'rgb(46, 204, 113)'`) with the
  `spawn_workflow` entry temporarily removed, then confirmed it passes with
  the entry restored (`git diff --stat` on `ArbitronNode.tsx` is empty — no
  production change needed). Independently verified the review's second claim
  — "no editor-side port rendering is required" for the node's `success`/
  `failure` ports — by reading `useGraphLayout.ts:173-186`: edge labels are
  already derived generically from `e.label ?? e.output` for every node type
  (this is how the pre-existing `conditional` node's branch ports are
  labeled too), so `spawn_workflow`'s ports need no node-side handle
  special-casing; `ArbitronNode` exposes only the fixed top/bottom handles
  for every type (already covered by the existing handles test), and grepped
  the whole `dashboard/src/graph` tree for any per-node-type handle or port
  logic — found none, confirming the claim rather than taking it on faith.
  Also confirmed `nodeTypes` (which registers `ArbitronNode`) is wired into
  real routes, not just tests (`grep` found `WorkflowReview.tsx` and
  `ExecutionView.tsx` as the two production call sites, alongside the mock in
  `GraphView.loops.test.tsx`). Ran the full `go test ./...`,
  `npx tsc --noEmit`, and `npx vitest run`: no new failures beyond the
  recorded baseline (the one pre-existing `adapter/config` failure is
  unchanged; dashboard typecheck at 0 errors; vitest 478 passed, 0 failed, up
  from 476 with this pass's two new tests). This closes review 1 approval for
  area 30; review 2 remains open — did not attempt to prove the fallback
  color choice or bar/label pairing beyond what's tested here, left for
  review 2 to independently re-derive.

- [x] **`HierarchyRows` nested one `<tbody>` inside another every time a row
  was expanded, an HTML-invalid structure React itself flags as a hydration
  hazard.** Focus area 29 (dashboard hierarchy rendering), review 2.
  `dashboard/src/components/HierarchyRows.tsx` wrapped each row's own markup
  (`QueueTableRow` + optional detail/loading/error rows + a recursive
  `<HierarchyRows>` call for its expanded children) in a `<tbody key=
  {inst.id}>`. `QueueSection.tsx` never wrapped its `<HierarchyRows>` call in
  a `<tbody>` of its own, so the top level worked by accident (each root row's
  `<tbody>` sat directly under `<table>`, a valid position) — but the
  recursive call for an EXPANDED row's children placed the child's `<tbody>`
  INSIDE the parent's own `<tbody>`, which `isTagValidWithParent` in React's
  `validateDOMNesting` rejects (`tbody`'s only valid children are `tr`/
  `style`/`script`/`template`). Confirmed by spying on `console.error` while
  expanding a row in `HierarchyRows.test.tsx`'s harness: React logged "In
  HTML, `<tbody>` cannot be a child of `<tbody>`... This will cause a
  hydration error" on every expansion, and NO existing test (across
  `HierarchyRows.test.tsx`, `QueueTableRow.test.tsx`, `QueueSection.test.tsx`)
  ever spied on `console.error`, so this was invisible to the whole suite
  despite firing on nearly every hierarchy-expansion test that already
  existed. This is not merely a lint nit: browsers only auto-correct invalid
  table nesting when PARSING HTML text (network response or `innerHTML`); a
  tree built via the DOM API (which is what React does) is not reparsed or
  fixed up, so a real user's browser renders the exact malformed tree
  constructed here, with no guarantee every engine's anonymous
  table-box-generation degrades gracefully for a recursively-nested
  row-group, and the SPA would additionally be unhydratable if this
  component were ever rendered server-side. Added `'expanding a row to show
  its children does not nest one <tbody> inside another'`
  (`HierarchyRows.test.tsx`), which spies on `console.error` and asserts no
  call contains "cannot be a child of"; confirmed it fails for the right
  reason (the exact React nesting-violation message) against the pre-fix
  code, then confirmed it passes against the fix. Also updated the test
  harness itself to wrap `<HierarchyRows>` in a `<tbody>` (matching
  `QueueSection`'s real usage) rather than placing it bare under `<table>`,
  since a bare-`<tr>`-under-`<table>` structure is its own distinct nesting
  violation that would have made the fixed code look broken in the harness
  even though `QueueSection` no longer has that problem. Fixed by giving each
  row's own grouping a plain `Fragment` (keyed, no DOM node of its own)
  instead of a `<tbody>`, and moving the single owning `<tbody>` up to
  `QueueSection`'s call site — so the table now has exactly one `<tbody>`
  no matter how many hierarchy levels are expanded, matching a normal flat
  table's structure. Independently re-verified the rest of area 29's
  checklist from source rather than trusting review 1's write-up:
  `QueueTableRow`'s `aria-expanded`/`aria-label` toggle-state tracking,
  `stopPropagation`, `depth`-driven indent, and childless-row spacer are all
  still backed by the four tests review 1 added and are unaffected by this
  fix (`git diff` on `QueueTableRow.tsx` is empty this pass); `HierarchyRows`
  recursion/depth passthrough and `QueueSection`'s delegation to it for the
  detail row it replaced are still covered by the existing nesting/depth and
  detail-panel/refresh-invalidation tests; re-confirmed by grep that
  `HierarchyRows` is constructed only by itself (recursion) and
  `QueueSection`, `QueueTableRow` only by `HierarchyRows`, and `QueueSection`
  only by the real `QueueView.tsx` route (four call sites, unchanged from
  review 1). Ran the full `go test ./...`, `npx tsc --noEmit`, and `npx
  vitest run`: no new failures beyond the recorded baseline (the one
  pre-existing `adapter/config` failure is unchanged; dashboard typecheck at
  0 errors; vitest 476 passed, 0 failed, up from 475 with this pass's one new
  test). This closes review 2 approval for area 29 — both approvals for area
  29 are now recorded.

- [x] **`QueueTableRow`'s hierarchy-toggle `e.stopPropagation()` — the exact
  mechanism that keeps expanding/collapsing a row's children from ALSO
  navigating away to that row's detail view — was completely untested.**
  Focus area 29 (dashboard hierarchy rendering), review 1.
  `dashboard/src/components/QueueTableRow.tsx`'s `<tr onClick={() =>
  navigate(...)}>` wraps the whole row, so the toggle button added by this
  feature (`git diff` against the branch point shows the entire tree-cell
  block — toggle, spacer, `depth`/`hierarchyExpanded`/`onToggleHierarchy`
  props — was newly added, while `QueueTableRow.test.tsx` was left
  byte-for-byte unchanged) needs its own `stopPropagation()` or every
  expand/collapse click would also fire the row's navigate. Confirmed by
  grep that NO test file in the whole dashboard suite ever mocks
  `useNavigate` — the existing `'navigates to /instances/{id} on row click'`
  test called `fireEvent.click` but asserted nothing, so a regression
  deleting the `stopPropagation()` call, or one that broke any of
  `aria-expanded`, the toggle/spacer split, or the `depth`-driven indent, would
  ship undetected; none of those items had a *direct* `QueueTableRow`-level
  test, only indirect coverage through `HierarchyRows.test.tsx`'s
  real-hook harness (which never checks navigation was suppressed either).
  Added a `useNavigate` mock (`vi.hoisted` + `vi.mock('react-router-dom', ...)`)
  to `QueueTableRow.test.tsx` and four new tests: clicking the toggle calls
  `onToggleHierarchy` and never `navigate` (the stopPropagation proof),
  `aria-expanded`/label track `hierarchyExpanded`, a childless row renders the
  aligning spacer (`aria-hidden` span) instead of a toggle, and `depth`
  scales the tree-cell's `paddingLeft` (`depth * 16px`); also strengthened the
  pre-existing empty "navigates on row click" test to assert
  `navigateMock` was actually called with the right path, since the new
  stopPropagation test's contrast depends on row-click navigation being a
  proven, not assumed, baseline behaviour. Confirmed the stopPropagation
  test fails for the right reason (`navigateMock` called with
  `/instances/i1`) when the `e.stopPropagation()` line is temporarily
  removed, then confirmed it passes with the line restored (`git diff --stat`
  on `QueueTableRow.tsx` is empty — no production change needed).
  Independently verified the rest of area 29's checklist from source:
  `HierarchyRows`'s recursion/depth passthrough and `QueueSection`'s
  delegation to it for the detail row it used to render inline
  (`git diff` against the branch point shows the `sorted.map` +
  inline `<QueueTableRow>`/detail-`<tr>` block replaced by a single
  `<HierarchyRows rows={roots} ... renderDetail={...} />` call) are both
  already covered by existing tests (`HierarchyRows.test.tsx`'s nesting/depth
  cases, `QueueSection.test.tsx`'s detail-panel expand/collapse and
  refresh-signal invalidation cases); grepped all non-test JSX call sites and
  confirmed `HierarchyRows` is constructed only by itself (recursion) and
  `QueueSection`, `QueueTableRow` only by `HierarchyRows`, and `QueueSection`
  only by the real `QueueView.tsx` route (four call sites, each fed a real
  `refreshedAt` counter and real API-derived `instances`, not hardcoded data).
  Ran the full `go test ./...`, `npx tsc --noEmit`, and `npx vitest run`: no
  new failures beyond the recorded baseline (the one pre-existing
  `adapter/config` failure is unchanged; dashboard typecheck at 0 errors;
  vitest 475 passed, 0 failed, up from 471 with this pass's four new tests).
  This closes review 1 approval for area 29; review 2 remains open.

- [x] **`isOnCycle`'s "detects a cycle of any length" claim was proven only up
  to length 2.** Focus area 28 (dashboard section-aware top-level rule),
  review 2. `dashboard/src/components/topLevelRows.ts`'s `isOnCycle` walks the
  full parent chain via a `seen` set specifically to generalize beyond the
  original self-referential (length-1) special case — the fix's own commit
  message says "Generalized the guard... subsuming the single-node
  self-referential case" — but the only cycle tests that existed
  (`topLevelRows.test.ts`) covered length 1 (self-reference) and length 2
  (mutual pair). A regression that replaced the general walk with a
  direct/one-hop-back check (which would still satisfy both existing tests)
  would silently reintroduce the original bug for any 3-or-more-node cycle.
  Added `'a three-instance cycle does not erase any row'`, asserting
  `topLevelRows([r('a','b'), r('b','c'), r('c','a')])` keeps all three rows in
  order; confirmed it fails for the right reason (`expected [] to deeply equal
  ['a','b','c']`) against a naive length-2-only `isOnCycle` stand-in, then
  confirmed it passes against the real code (`git diff --stat` on
  `topLevelRows.ts` is empty — no production change needed). Independently
  re-verified the rest of area 28's checklist from source rather than trusting
  review 1's write-up: `topLevelRows` has exactly one production call site
  (`QueueSection.tsx:158`, grepped for all non-test references), fed by
  `instances` props (`active`/`pending`/`failed`/`completed`) that are real
  API-derived state slices in `QueueView.tsx`, not hardcoded or test-only
  data; `EnrichedInstanceSummary.parentInstanceId` (`dashboard/src/api/types.ts:66`)
  is the only field read, matching area 26's wire-format verification. Ran the
  full `go test ./...`, `npx tsc --noEmit`, and `npx vitest run`: no new
  failures beyond the recorded baseline (the one pre-existing `adapter/config`
  failure is unchanged; dashboard typecheck at 0 errors; vitest 471 passed, 0
  failed, up from 470 with this pass's one new test). This closes review 2
  approval for area 28 — both approvals for area 28 are now recorded.

- [x] **`topLevelRows` erased every row of a corrupt multi-node parent-link
  cycle, not just a self-referential one.** Focus area 28 (dashboard
  section-aware top-level rule), review 1.
  `dashboard/src/components/topLevelRows.ts`'s pre-fix implementation only
  special-cased a length-1 cycle (`r.parentInstanceId === r.id`, per the
  existing self-referential test) — but the domain layer already treats
  longer spawn-link cycles as a real corrupt-data possibility worth guarding
  against (`domain/engine/cancel_cascade_test.go`'s
  `TestCancelCascade_MutualLinkCycleHaltsTraversal`, using an `unfiltered`
  fake to simulate a hand-edited/corrupt link row), and a normal
  `Engine.Spawn` can never itself produce a cycle since a child instance is
  always newly created after its parent (so only DB corruption/manual editing
  can produce one). Since each row has exactly one `parentInstanceId`, a
  length-2+ cycle (A's parent is B, B's parent is A) has every member's
  IMMEDIATE parent present in the same section, so the naive per-row check
  suppressed both — unlike the guarded self-loop case, there is no member
  outside the cycle left to expand and reveal the rest, so the whole cycle
  silently vanished from the section with no way to bring it back into view.
  Added `'a mutual two-instance cycle does not erase either row'`
  (`dashboard/src/components/topLevelRows.test.ts`), asserting
  `topLevelRows([r('a','b'), r('b','a')])` keeps both rows; confirmed it
  fails for the right reason (`expected [] to deeply equal ['a', 'b']`)
  against the pre-fix code, then confirmed it passes against the fix.
  Generalized the guard: `isOnCycle(id)` walks the parent chain (through rows
  present in the section) from `id`, returning true only if it loops back to
  `id` itself (subsuming the single-node self-referential case, which now
  needs no special-casing) — a row whose chain instead escapes to an absent
  parent, or wanders into a DIFFERENT cycle that never revisits `id`, is
  still suppressed exactly as before. Manually traced (not re-tested, since
  the existing suite already exercises it) that a normal multi-level chain
  (parent → child → grandchild, no cycle) is unaffected: `isOnCycle` returns
  false for the grandchild once the walk reaches the parent's own empty
  `parentInstanceId`, so the base "immediate parent present" suppression rule
  still applies unchanged. Ran the full `go test ./...`,
  `npx tsc --noEmit`, and `npx vitest run`: no new failures beyond the
  recorded baseline (the one pre-existing `adapter/config` failure is
  unchanged; dashboard typecheck at 0 errors; vitest 470 passed, 0 failed, up
  from 469 with this pass's one new test). Independently confirmed
  `topLevelRows` has exactly one production call site
  (`QueueSection.tsx:158`, grepped for all non-test references), fed real
  `instances` prop data flowing from `QueueView`'s API-backed state, not a
  hardcoded or test-only value. This closes review 1 approval for area 28;
  review 2 remains open.

- [x] **A double-toggle fired before the first fetch settled broke the
  "one request per level" contract, and silently re-expanded instead of
  collapsing.** Focus area 27 (dashboard run-hierarchy state hook), review 2.
  `toggle` (`dashboard/src/hooks/useRunHierarchy.ts`) read the closed-over
  `expanded[id]`/`levels[id]` from the CURRENT RENDER's state to decide
  whether to collapse, skip (cache hit), or fetch. Two `toggle(id)` calls
  fired back to back — a real double-click on the row's expander, or any
  caller invoking it twice before React re-renders — both closed over the
  SAME pre-update snapshot (`expanded[id]` still `false`, `levels[id]` still
  `undefined`), so both slipped past the "already expanded" and "already
  fetching/cached" guards: two concurrent `api.instances.list` calls fired for
  one logical toggle, and the row ended up expanded (last write wins `true`)
  instead of collapsing back — a real double-click should behave like flipping
  a checkbox twice, not fire two requests and stay open. Added `'two toggles
  fired before the first fetch settles issue only one request'`
  (`dashboard/src/hooks/useRunHierarchy.test.ts`), which calls
  `toggle('p1')` twice inside one `act` before resolving the mocked fetch and
  asserts `listMock` was called exactly once and the row ends collapsed;
  confirmed it failed for the right reason against the pre-fix code
  (`expected "vi.fn()" to be called 1 times, but got 2 times`), then confirmed
  it passes against the fix. Fixed by mirroring `expanded`/`levels` into a
  `useRef` updated SYNCHRONOUSLY inside each functional `setState` call (not on
  the next render), and having `toggle` read the ref instead of the
  closed-over state for its collapse/cache-hit decisions — the second call now
  sees the first call's effect immediately and correctly collapses without
  fetching. Independently re-verified the rest of area 27's checklist left
  open by review 1: one-request-per-level lazy fetch (now also covered under
  the racy case above, plus the existing `'collapsing hides the children
  without refetching...'` and `'recursive expansion...'` tests for the
  non-racy case), per-row error confinement
  (`'confines an expansion failure to its own row'`, keyed by a `Record<string,
  Level>` so rows cannot cross-contaminate by construction), error levels not
  cached (`'a retry after a failed expansion refetches'`); confirmed
  `useRunHierarchy` is still constructed at exactly one production call site
  (`QueueSection.tsx:75`, grepped for all non-test references) and every
  member of its returned `RunHierarchy` interface — `isExpanded`, `childrenOf`,
  `isLoading`, `errorOf`, `toggle`, `invalidate` — is read by real production
  code (`HierarchyRows.tsx` reads the first five, `QueueSection.tsx` wires
  `invalidate` to `refreshSignal`), not merely exercised by a test. Ran the
  full `go test ./...`, `npx tsc --noEmit`, and `npx vitest run`: no new
  failures beyond the recorded baseline (the one pre-existing `adapter/config`
  failure is unchanged; dashboard typecheck at 0 errors; vitest 469 passed, 0
  failed, up from 468 with this pass's one new test). This closes review 2
  approval for area 27 — both approvals for area 27 are now recorded.

- [x] **`useRunHierarchy.invalidate` had no production caller.** Focus area 27
  (dashboard run-hierarchy state hook), review 1.
  `dashboard/src/hooks/useRunHierarchy.ts:78` exports `invalidate`, documented as
  "used on a full refetch", but the only reference outside the hook was
  `dashboard/src/hooks/useRunHierarchy.test.ts:161` — `QueueSection.tsx` called
  `useRunHierarchy()` and never called `invalidate`, so every expanded level
  stayed cached forever: a child that completed, failed, or was cancelled kept
  rendering its pre-refetch state until the user collapsed and re-expanded it by
  hand, and a newly spawned child never appeared under an already-open parent.
  Wired it to the top-level refetch that already exists: `QueueView.tsx` now
  tracks a `refreshedAt` counter, incremented only on `fetchAll`'s SUCCESS path
  (not on the in-place `NODE_PATCH_EVENTS` row patch, which doesn't touch
  `instances`' identity in a way that means "the list was truly refetched," and
  not on a failed fetch, which leaves the existing rows and thus their children
  still valid), and passes it to every `<QueueSection refreshSignal={...}>`.
  `QueueSection` runs `useEffect(() => invalidate(), [refreshSignal,
  invalidate])` — `invalidate`'s identity is stable (the hook's own
  `useCallback` has an empty dep array), so this fires exactly once per real
  bump, not once per render. Added
  `'a refreshSignal bump collapses an expanded row and drops its stale cached
  children'` (`dashboard/src/components/QueueSection.test.tsx`): expands a row,
  confirms the (mocked) child renders, bumps `refreshSignal` via `rerender`, and
  asserts the child is gone and the row is collapsed again; confirmed it fails
  for the right reason (stale `Child Run` cell still present) when the effect
  body is neutered to `if (false) invalidate()`, then confirmed it passes
  against the real code. Added a sibling test asserting an unchanged
  `refreshSignal` leaves an expanded row untouched, so the fix doesn't collapse
  rows on every unrelated re-render. Did not attempt a finer-grained "re-fetch
  only open levels while preserving expansion" — `invalidate`'s own contract
  (tested at `useRunHierarchy.test.ts:156`) is "drop cache AND collapse," and
  building a different, softer behavior would be inventing functionality no
  design document asked for; this pass wires the existing, already-specified
  behavior to a real caller. Ran `go test ./...`, `npx tsc --noEmit`, and
  `npx vitest run`: no new failures beyond the recorded baseline (the one
  pre-existing `adapter/config` failure is unchanged; dashboard typecheck at 0
  errors; vitest 468 passed, 0 failed, up from 466 with this pass's two new
  tests). This closes review 1 approval for area 27; review 2 remains open, and
  the rest of area 27's checklist (one-request-per-level lazy fetch, per-row
  error confinement, error levels not cached) was not independently
  re-verified this pass — left for review 2.

- [x] **`ListInstancesParams.parent`'s own doc comment documents three wire
  values (omit, `'roots'`, an instance UUID), matching
  `domain/instance/spawn_link.go`'s `ParseParentScope`, but only "omit" and "a
  UUID" were ever exercised by a client-level test.** Focus area 26 (dashboard
  API client & hierarchy types), review 2. Confirmed by grep that no dashboard
  production code ever calls `api.instances.list({ parent: 'roots' })` — the
  dashboard fetches unfiltered by state (`QueueView.tsx`) and computes
  top-level rows client-side via `topLevelRows.ts` instead of asking the
  server for the `roots` scope (a deliberate design already covered by the "No
  seam" rationale for 24/25 × 27/28/29), so this is not a live bug, just an
  untested corner of the documented client vocabulary. Added `'list sends the
  literal roots scope verbatim'` (`dashboard/src/api/client.test.ts`),
  asserting `list({ parent: 'roots' })` produces a URL containing
  `parent=roots`; confirmed it fails for the right reason (`expected:
  "parent=roots", actual: "/api/v1/instances"`) when the truthy check is
  temporarily changed to exclude `'roots'`, then confirmed it passes against
  the real code (`git diff --stat` on `client.ts` is empty — no production
  change needed). Independently re-verified the rest of area 26's checklist
  from source rather than trusting review 1's write-up: `enrichedSummaryDTO`'s
  `parentInstanceId`/`childCount` JSON tags (`adapter/http/instance_handler.go:188,258`)
  still match `types.ts` exactly; real production callers of the hierarchy
  fields are confirmed by grep (`HierarchyRows.tsx:58` reads `childCount`,
  `topLevelRows.ts:24` reads `parentInstanceId`, `useRunHierarchy.ts:68` calls
  `list({ parent: id })` with a real instance id) — not merely present in
  types with no reader. Ran the full `go test ./...`, `npx tsc --noEmit`, and
  `npx vitest run`: no new failures beyond the recorded baseline (the one
  pre-existing `adapter/config` failure is unchanged; dashboard typecheck at 0
  errors; vitest 466 passed, 0 failed, up from 465 with this pass's one new
  test). This closes review 2 approval for area 26 — both approvals for area
  26 are now recorded.

- [x] **`EnrichedInstanceSummary.parentInstanceId`/`.childCount`'s wire-format
  field names were proven only by TS-constructed object literals, never by an
  actual JSON round trip.** Focus area 26 (dashboard API client & hierarchy
  types), review 1. `dashboard/src/api/client.ts`'s `list()` is a pure
  passthrough (`return request(...)`, itself just `res.json()` cast to `T`
  with no runtime validation), so a field-name mismatch between
  `dashboard/src/api/types.ts` and the server's actual JSON keys (the same
  shape of gap already found and fixed for MCP's `ChildCount`/`FailedNodeID`
  in area 25) would compile fine and only surface as `undefined` at runtime —
  TypeScript cannot catch it because the cast is unchecked. Confirmed by
  reading every existing test that touches these fields
  (`client.test.ts`, `HierarchyRows.test.tsx`, `useRunHierarchy.test.ts`,
  `topLevelRows.test.ts`): all of them build fixtures through a typed `row()`
  helper or a direct TS object literal, which would still compile even if
  someone renamed the field in `types.ts` (every call site would just get
  renamed too) — none of them prove the CLIENT decodes the SERVER's actual
  string keys (`parentInstanceId`, `childCount`, confirmed matching
  `adapter/http/instance_handler.go`'s `enrichedSummaryDTO` JSON tags by
  source comparison) rather than some other convention. Added `'list surfaces
  parentInstanceId and childCount from a raw wire-format response verbatim'`
  (`dashboard/src/api/client.test.ts`), which mocks `fetch` to return a raw,
  hand-typed JSON object using literal string keys (not the `row()` helper)
  and asserts `api.instances.list()` surfaces `parentInstanceId`/`childCount`
  from it; confirmed it fails for the right reason (`expected: "p1", actual:
  undefined`) when the mock's key is temporarily typo'd to
  `parentInstanceID`, then confirmed it passes with the correct key restored.
  Independently verified the rest of area 26's checklist from source: `parent`
  is appended as `?parent=` only when truthy (`client.ts:99`, covered by the
  existing `'list narrows by parent...'` test); the SAME `roots`/UUID wire
  vocabulary is documented consistently in `ListInstancesParams.parent`'s
  comment and `domain/instance/spawn_link.go`'s `ParseParentScope`. Ran the
  full `go test ./...`, `npx tsc --noEmit`, and `npx vitest run`: no new
  failures beyond the recorded baseline (the one pre-existing `adapter/config`
  failure is unchanged; dashboard typecheck at 0 errors; vitest 465 passed, 0
  failed, up from 464 with this pass's one new test). This closes review 1
  approval for area 26; review 2 remains open.

- [x] **MCP `instance_list`'s `enriched == nil` → `[]` normalisation
  (`adapter/mcp/tools_instance.go:136-138`) had a test with the right name and
  the right assertion, but the wrong sink — it didn't actually exercise the
  handler's own guard.** Focus area 25 (MCP `instance_list` parent selector),
  review 2. `go tool cover` on the `adapter/mcp` suite showed
  `instanceListHandler` at 95%, with lines 136-138 (the `if enriched == nil {
  enriched = []instance.EnrichedInstanceSummary{} }` block) at 0 hits, despite
  an existing test titled `"nil repo result produces JSON array not null"`
  (`tools_instance_test.go`) that asserts exactly the behaviour this guard is
  for. Root cause: that test sets the *repo's* `listFn` to return `nil, nil`
  and uses the DEFAULT `stubEngine{}` (no `listEnrichedFn`), whose own
  `ListEnriched` delegates through `ListInstances` and rebuilds the result
  with `make([]instance.EnrichedInstanceSummary, len(summaries))` — which is a
  non-nil empty slice even when `len(summaries) == 0`. So `enriched` returned
  to the handler was never actually `nil` in that test; the assertion passed
  because the STUB, not the handler, already produced `"[]"`. Confirmed by
  temporarily deleting the handler's own normalisation block entirely: the
  full `adapter/mcp` package (all pre-existing tests, this one included) still
  passed, proving the code path this test claimed to cover has no test that
  can fail if it regresses — the same "asserts an intermediate hop instead of
  the sink" shape RALPH.md calls out by name. A regression that returned a raw
  `nil` from a real caller of `Engine.ListEnriched` — plausible, since
  `Engine.ListEnriched` delegates to `e.enricher.Enrich` when an enricher is
  wired, and nothing here pins that path's own nil-handling either — would
  have shipped an MCP client a literal JSON `null` instead of `[]`, breaking
  any client that iterates the result without a null-check. Added a sibling
  subtest, `"nil Engine.ListEnriched result produces JSON array not null"`,
  that sets `listEnrichedFn` directly to return `nil, nil`, bypassing the
  stub's own reconstruction and forcing the handler's guard to be what turns
  it into `"[]"`; confirmed it fails for the right reason (`actual: "null"`)
  with the guard temporarily removed, then confirmed it passes with the real
  code restored. `go tool cover` afterward shows `instanceListHandler` at
  100%. Independently re-verified the rest of area 25's checklist from source
  rather than trusting review 1's write-up: the `parent` string arg and
  description are unchanged and present (`tools_instance.go:28-30`); the SAME
  `instance.ParseParentScope` decoder is still the only decode path (grepped);
  a malformed selector still returns a tool error
  (`TestMCPInstanceList_MalformedParentIsAnError`); happy
  (`_RootsOnly`, `_ChildrenOfAParent`), success (`_NoParentArgDoesNoNarrowing`,
  `_ParentComposesWithStateArg`), failure (`_MalformedParentIsAnError`), and
  edge (`_UnknownParentReturnsEmpty`) coverage all still present and green in
  `tools_instance_parent_test.go`; `TestMCPRestParity_InstanceListParentFilter`
  still proves MCP/REST parity for the `roots` scope; composition-root wiring
  re-confirmed at BOTH real call sites (`cmd/arbitron/main.go:1178` HTTP-mounted
  MCP server and `main.go:1320` stdio transport), both built from the same
  `ds.mcpDeps()` carrying the real `ds.eng`, not a stub. Ran the full
  `go test ./...`, `npx tsc --noEmit`, and `npx vitest run` (vitest unaffected,
  not re-run for this Go-only change but already green from step 2 of this
  pass): no new failures beyond the recorded baseline (the one pre-existing
  `adapter/config` failure is unchanged; dashboard typecheck at 0 errors;
  vitest 464 passed, 0 failed as of this pass's pre-check). This closes review
  2 approval for area 25 — both approvals for area 25 are now recorded.

- [x] **`ChildCount`'s MCP field name was undocumented-by-test and the
  REST/MCP divergence unexamined.** Focus area 25 (MCP `instance_list` parent
  selector), review 1. `adapter/mcp/tools_instance.go:139` returns
  `MarshalResult(enriched)` over `[]instance.EnrichedInstanceSummary` directly.
  `EnrichedInstanceSummary.ChildCount` (`domain/instance/instance.go:213`) has
  no JSON tag, so it marshals as `"ChildCount"` while REST's
  `enrichedSummaryDTO` (`adapter/http/instance_handler.go:258`) emits
  `"childCount"`. Read the rest of `EnrichedInstanceSummary`
  (`RunName`, `Submitter`, `FriendlyStep`, `Model`, `Backend`, `RetryCount`,
  `QueuePosition`, `FailedNodeID`) and confirmed via `git diff` against the
  branch point that NONE of the struct's own fields carry JSON tags — this is
  a pre-existing, deliberate convention (`parity_test.go`'s own comment: "MCP
  marshals the domain struct with PascalCase keys; REST maps to a ... DTO"),
  not a new asymmetry `ChildCount` introduced. So the divergence is consistent,
  not a bug — but no test proved `ChildCount` actually reaches the MCP surface
  at all, under any key. Extended `TestInstanceList_ReturnsEnrichedFields`
  (`adapter/mcp/tools_instance_test.go`) to set `ChildCount: 3` on the stub
  enriched row, decode the MCP response body, and assert
  `rows[0]["ChildCount"] == 3` (a real decode, not a substring match); confirmed
  it fails for the right reason (`expected: 3, actual: 0`) with `ChildCount: 3`
  temporarily removed from the fixture, then confirmed it passes with it
  restored. Independently re-verified the rest of area 25's checklist from
  source: the `parent` string arg and its description are present
  (`tools_instance.go:28-30`); the SAME `instance.ParseParentScope` decoder
  REST uses is called (grepped both call sites); a malformed selector returns a
  tool error, covered for three malformed inputs
  (`TestMCPInstanceList_MalformedParentIsAnError`); the parent selector itself
  has happy (`_RootsOnly`, `_ChildrenOfAParent`), success
  (`_NoParentArgDoesNoNarrowing`, `_ParentComposesWithStateArg`), failure/error
  (`_MalformedParentIsAnError`), and edge (`_UnknownParentReturnsEmpty`)
  coverage in `tools_instance_parent_test.go`, and its fake
  (`parityInstanceRepo` in `parity_test.go`) genuinely honours `ParentScope`/
  `ParentInstanceID` rather than ignoring them; composition-root wiring
  confirmed real — `cmd/arbitron/main.go:1178` constructs
  `adaptmcp.NewServerWithDeps("arbitron", "0.1.0", ds.mcpDeps())` then
  `RegisterAllTools(mcpSrv)` → `RegisterInstanceTools`, and `mcpDeps()`
  (`main.go:244-246`) passes the real `ds.eng`, not a stub. While confirming
  the untagged-fields claim, found a separate, pre-existing gap — a duplicate
  `FailedNodeID` declaration that shadows the embedded, tagged one for direct
  JSON marshaling — logged above under Open rather than fixed here (out of
  scope: present unchanged since the branch point, not introduced by
  `ChildCount`). Ran the full `go test ./...`, `npx tsc --noEmit`, and
  `npx vitest run`: no new failures beyond the recorded baseline (the one
  pre-existing `adapter/config` failure is unchanged; dashboard typecheck at 0
  errors; vitest 464 passed, 0 failed). This closes review 1 approval for area
  25; review 2 remains open.

- [x] **`400 INVALID_PARENT`'s specific error CODE was never pinned by a test —
  only the HTTP status.** Focus area 24 (REST list parent selector & hierarchy
  DTO), review 2. `adapter/http/instance_handler.go:411-414` documents and
  implements a specific error code (`WriteError(w, http.StatusBadRequest,
  "INVALID_PARENT", perr.Error())`) so a caller can distinguish a malformed
  `parent` selector from any other `400` on the endpoint — but
  `TestInstanceListREST_MalformedParentIsRejected`
  (`adapter/http/instance_list_parent_test.go`, added when review 1 closed)
  only asserted `w.Code == http.StatusBadRequest`, never decoded the JSON body
  to check the `code` field. Confirmed by hand: changing the code string to
  `"BAD_REQUEST"` left every existing test in the package green. A regression
  that changed or genericized the code (the same shape of gap fixed repeatedly
  elsewhere in this loop — a documented, specific claim proven only by its
  coarser side effect) would ship undetected. Updated the test to decode
  `adapthttp.ErrorResponse` from the response body and assert
  `body.Code == "INVALID_PARENT"` for all four malformed inputs; confirmed it
  fails for the right reason (`expected: "INVALID_PARENT", actual:
  "BAD_REQUEST"`) against the neutered code, then confirmed it passes against
  the real code. Independently re-verified the rest of area 24's checklist
  from source rather than trusting review 1's write-up: composition-root
  wiring re-confirmed (`cmd/arbitron/main.go:1188`, `Service: ds.eng` — the
  real `*engine.Engine` — passed to `adapthttp.NewRouterFromDeps`, which
  constructs `NewInstanceHandler` at `adapter/http/server.go:203` with all four
  real repository/service deps, no `nil`/stub); re-traced the
  narrowing-never-widening security contract end to end — `Engine.ListEnriched`
  (`domain/engine/engine.go:544-557`) always overwrites
  `OwnerID`/`Departments`/`IncludePublic` from the service-set
  `listConstraint`, never from caller input, while leaving
  `filters.ParentScope`/`.ParentInstanceID` (set only by the handler) untouched
  — and `InstanceRepo.List` (`adapter/sqlite/instance_repo.go:204-215`) ANDs
  the parent-scope clause onto the visibility disjunction rather than OR-ing it
  in, so a caller cannot use `parent=` to see a run outside their own
  visibility scope; `summaryDTO.ParentInstanceID`/`enrichedSummaryDTO.ChildCount`
  mappings and their `omitempty` JSON tags are exercised by
  `TestInstanceListREST_ChildrenOfAParent`/`_RootRowOmitsParentInstanceID`/
  `_ChildCountIsSurfaced`; `List` is still the sole caller of `ListEnriched`
  (unchanged since review 1). `go tool cover` over the full `adapter/http`
  suite shows `List` at 98.1% and `toEnrichedSummaryDTO`/`toEnrichedResponse`
  at 100%, matching review 1's own figures with no regression. Ran the full
  `go test ./...`, `npx tsc --noEmit`, and `npx vitest run`: no new failures
  beyond the recorded baseline (the one pre-existing `adapter/config` failure
  is unchanged; dashboard typecheck at 0 errors; vitest 464 passed, 0 failed,
  unaffected by this Go-only change). This closes review 2 approval for area
  24 — both approvals for area 24 are now recorded.

- [x] **Area 23 (batched child counts on enriched rows), review 2: independent
  re-verification found no live gap.** Re-derived every checklist item from
  source rather than trusting review 1's write-up. `go tool cover` (running
  `TestEnricherChildCount`-prefixed tests) confirms `batchChildCounts` and
  `SetSpawnLinks` are both at 100% statement coverage. Confirmed `Enrich` is
  the ONLY production call site of `batchChildCounts`/the enricher's
  `ChildVisibility` construction (grepped for `.Enrich(` across the whole
  repo — `domain/engine/engine.go:572` is the sole non-test caller, invoked
  from `ListEnriched` with the identical `ctx` that already authorized the
  page via its own `listConstraint(ctx)` call), so the "same listConstraint
  that authorized the page" claim holds by construction — both calls derive
  from the same immutable `ctx` within one request, not from a value threaded
  between them, so they cannot diverge. Confirmed `countingSpawnLinks`
  (`domain/engine/enricher_child_count_test.go`) genuinely honours the
  `ChildVisibility` argument rather than ignoring it (already noted by review
  1, re-checked here), so the non-disclosure test
  (`TestEnricherChildCount_InvisibleChildrenReportZero`) is unfailable-if-broken
  by construction. Re-verified the composition-root wiring
  (`cmd/arbitron/main.go:798`,
  `instanceEnricher.SetSpawnLinks(instanceRepo)`) independently by temporarily
  commenting out that line and re-running review 1's own
  `TestEnricherChildCountE2E_ParentReportsRealChildCountThroughCompositionRoot`
  (`cmd/arbitron/enricher_child_count_e2e_test.go`): confirmed it fails for the
  right reason (`ChildCount` 0 instead of 1) with the wiring removed, then
  confirmed it passes again once restored (`main.go` has no net diff after
  this pass — `git diff --stat` is empty). Confirmed all five scenario
  categories are present and each is a genuine (not over-mocked) test: happy
  (`TestEnricherChildCount_StampsCountsOnRows`), success/batching
  (`_OneBatchedCallPerPage`, `_PassesTheCallersServiceSetVisibility`,
  `_AdminVisibilityIsUnconstrained`, `_InvisibleChildrenReportZero`),
  failure/degradation (`_QueryFailureDegradesTheRowNotThePage`), and edge
  (`_NoRepositoryWiredReportsZero`, `_EmptyPageTouchesNoRepository`,
  `_NoIdentitySkipsCounting` — the last two proving the repository is never
  touched without a real page or a real identity, matching the "never default
  to unconstrained" contract). Ran the full `go test ./...`,
  `npx tsc --noEmit`, and `npx vitest run`: no new failures beyond the
  recorded baseline (the one pre-existing `adapter/config` failure is
  unchanged; dashboard typecheck at 0 errors; vitest 464 passed, 0 failed).
  This closes review 2 approval for area 23 — both approvals for area 23 are
  now recorded.

- [x] **`instanceEnricher.SetSpawnLinks(instanceRepo)`'s composition-root wiring
  had no test proving it through the real engine, unlike the identical seam
  already proven for `eng.SetSpawnLinks` (area 16 review 2) and
  `queueMgr.SetChildTerminalHook` (area 20).** Focus area 23 (batched child
  counts on enriched rows), review 1. `domain/engine/enricher.go:105`
  documents and implements a load-bearing no-op: `if e.spawnLinks == nil ...
  return map[uuid.UUID]int{}` — every row would report `ChildCount` 0 and no
  hierarchy expander would render. Every existing test in
  `domain/engine/enricher_child_count_test.go` drives `batchChildCounts`
  through a `countingSpawnLinks` fake the test itself constructs and injects
  via `en.SetSpawnLinks(links)` — none of them prove `cmd/arbitron/main.go`
  actually calls `instanceEnricher.SetSpawnLinks(instanceRepo)` before a real
  list request reaches it, the same shape of gap this loop has fixed
  repeatedly for other composition-root seams. Added
  `TestEnricherChildCountE2E_ParentReportsRealChildCountThroughCompositionRoot`
  (`cmd/arbitron/enricher_child_count_e2e_test.go`), which boots the real
  composition root via `initDomain`, triggers a real `fire_and_forget` spawn
  parent, waits for its real linked child to be persisted, then reads the
  parent back through `Engine.ListEnriched` (the sink a dashboard request
  would hit) and asserts `ChildCount == 1`; confirmed it fails for the right
  reason (`ChildCount` 0) when `instanceEnricher.SetSpawnLinks(instanceRepo)`
  is temporarily removed from `main.go`, then confirmed it passes again once
  restored (`main.go` has no net changes this pass). Independently reviewed
  the rest of area 23's checklist from source and found it already solid:
  `batchChildCounts` is at 100% statement coverage
  (`domain/engine/enricher_child_count_test.go` covers happy — counts
  stamped on rows; success — one batched call per page, visibility passed is
  the SAME service-set scope, admin unconstrained; failure/error — a query
  failure degrades only the row, never the page; edge — no repo wired, empty
  page touches no repository, no identity skips counting rather than
  defaulting to unconstrained); the `countingSpawnLinks` fake honours the
  `ChildVisibility` argument rather than ignoring it, so
  `TestEnricherChildCount_InvisibleChildrenReportZero` is unfailable-if-broken
  by construction, not by luck. Ran the full `go test ./...`,
  `npx tsc --noEmit`, and `npx vitest run`: no new failures beyond the
  recorded baseline (the one pre-existing `adapter/config` failure is
  unchanged; dashboard typecheck and vitest both clean). This closes review 1
  approval for area 23; review 2 remains open.

- [x] **A denied child cancellation in the cascade produced no assertion that
  the audit trail stayed accurate.** Focus area 22 (cancel cascade), review 2.
  `TestCancelCascade_AuthorizationFailureOnAChildHaltsThatBranch`
  (`domain/engine/cancel_cascade_test.go`) already asserted the parent ends
  `StateCancelled`, the child the actor cannot authorize (department-lead
  check fails) is left `StateRunning`, and the grandchild past it is untouched
  — but it never checked how many intervention records existed. Since
  `cancelAuthorized` only calls `e.recordIntervention` on its success path
  (`domain/engine/engine.go`), a denied child correctly produces none, so this
  was safe in the real code — but nothing pinned it, and `recordIntervention`
  being invoked before rather than after the authorization check, or on the
  failure path by mistake, would have shipped a false audit record ("this
  child was cancelled by X") for a child that was in fact left running,
  silently corrupting the intervention trail an operator relies on to
  understand what a cascade actually touched. Added
  `assert.Equal(t, 1, c.interventions.count(), ...)` to the existing test,
  asserting only the parent's own cancellation produced a record. Confirmed
  it fails for the right reason (`expected: 1, actual: 2`) when a bogus
  `e.recordIntervention(ctx, &instance.Instance{ID: childID},
  instance.ActionCancel, nil)` call is temporarily injected into
  `cascadeCancel`'s real-failure branch (`cancel_cascade.go:73-81`, the branch
  reached when `cancelAuthorized` returns neither `nil` nor
  `ErrCannotCancel`), then confirmed it passes again once the injected call is
  reverted (`cancel_cascade.go` has no net changes this pass). Independently
  re-verified the rest of area 22's checklist and found no other live gap:
  `cascadeCancel` is at 100% branch coverage per `go tool cover -func`; the
  `fakeSpawnLinks` test double's `ListActiveChildren` matches the real SQL
  adapter's `state IN ('queued','running','waiting','paused')` filter
  (`adapter/sqlite/instance_repo.go:649-674`); the owner-inheritance chain
  (`TriggerWithDepartment` → `identity.WithIdentity`, `engine.go:395-480`)
  confirmed as the reason cascade authorization by ownership always succeeds
  while department-leadership can fail partway, since department is derived
  per-child rather than inherited; `OnChildTerminal`
  (`domain/engine/spawn_wake.go`) confirmed independent of `e.spawnLinks`: and
  `cancelAuthorized`'s two uncovered `UpdateState`-failure branches confirmed
  via `git show <branch-point>:domain/engine/engine.go` to be byte-identical
  pre-existing code merely extracted into a new function name, not a gap
  introduced by this feature, so correctly left alone. Ran the full
  `go test ./...`, `npx tsc --noEmit`, and `npx vitest run`: no new failures
  beyond the recorded baseline (the one pre-existing `adapter/config` failure
  is unchanged; dashboard typecheck and vitest both clean). This closes review
  2 approval for area 22.

- [x] **`cascadeCancel`'s "already-terminal child still descends into its own
  active grandchildren" branch was completely uncovered.** Focus area 22
  (cancel cascade), review 1 (closes the last blocker deferred from the two
  fixes below). `domain/engine/cancel_cascade.go:67-71` — when
  `cancelAuthorized` on a child returns `ErrCannotCancel` (already terminal),
  the code deliberately still calls `e.cascadeCancel(ctx, childID, visited)`
  rather than `continue`-ing past it, with the comment "still descend, since a
  terminal child may have active grandchildren of its own." `go tool cover`
  over the full `TestCancelCascade`-prefixed suite showed this exact statement
  at 0 hits, and deleting the recursive call entirely (replacing the branch
  with a bare `continue`) still passed the entire suite. No existing fixture
  linked an active child under an already-terminal instance. Consequence: a
  regression that "simplified" this branch down to match every other failure
  branch's `continue` would ship undetected, silently orphaning any active
  descendant of an already-completed/failed/cancelled spawn-linked node —
  plausible in practice, since a fire-and-forget child can go terminal (e.g.
  complete) while its own fire-and-forget grandchild is still running. Added
  `TestCancelCascade_AlreadyTerminalChildStillDescendsIntoActiveGrandchild`
  (`domain/engine/cancel_cascade_test.go`), reusing the `unfiltered` fake
  technique the visited-guard tests already established (needed here too:
  the fake's own terminal-state filter would otherwise drop the terminal
  child from `ListActiveChildren`'s result before the loop ever reached
  `cancelAuthorized` on it, making the branch untestable through the fake at
  all) to link parent→child(`StateCompleted`)→grandchild(`StateRunning`);
  asserts the child is left untouched, the grandchild ends `StateCancelled`,
  and the grandchild gets its own intervention record via
  `interventions.ListByInstance`. Confirmed it fails for the right reason
  (grandchild stays `StateRunning`, zero intervention records) when the
  recursive `e.cascadeCancel(ctx, childID, visited)` call is temporarily
  deleted (bare `continue` left in its place), then confirmed it passes
  against the real code. With this fix, every remaining item on area 22's
  checklist is now backed by a passing, meaningfully-failing test: the
  visited-set guard and the department-scoped authorization-failure branch
  (both fixed below), storage-failure partial-failure handling and its
  sibling-branch audit-trail completeness (below), `ErrCannotCancel`-as-success
  and both-spawn-modes-cascaded (pre-existing), parent-transitions-first
  ordering (pre-existing), and the nil-`spawnLinks` no-op (pre-existing) —
  plus the composition-root wiring (`eng.SetSpawnLinks(instanceRepo)`) already
  proven end-to-end by area 16's
  `TestSpawnWaitE2E_CancellingParentCascadesToActiveChildThroughCompositionRoot`.
  Ran the full `go test ./...`, `npx tsc --noEmit`, and `npx vitest run`: no
  new failures beyond the recorded baseline (the one pre-existing
  `adapter/config` failure is unchanged; dashboard typecheck and vitest both
  clean). This closes review 1 approval for area 22; review 2 remains open.

- [x] **`cascadeCancel`'s visited-set cycle guard (`domain/engine/cancel_cascade.go:61-63`,
  `if visited[childID] { continue }`) was never actually exercised by either
  test that claimed to prove it.** Focus area 22 (cancel cascade), review 1
  (continued from the authorization-branch fix below, deliberately deferred to
  keep that pass to one issue). `TestCancelCascade_SelfReferentialLinkHaltsTraversal`
  and `_MutualLinkCycleHaltsTraversal` both passed, but instrumenting the
  already-visited branch showed it was hit ZERO times by either: `cancelAuthorized`
  transitions each instance to `StateCancelled` (terminal) BEFORE the recursive
  `cascadeCancel` call that would re-list it, and the fake's `ListActiveChildren`
  filters out terminal instances — so a cycle "halted" because the terminal
  filter had already dropped the node from the list, not because the `visited`
  check ever fired. The map was PROVEN REDUNDANT by these two tests, not proven
  load-bearing. Added an `unfiltered` field to `fakeSpawnLinks`
  (`domain/engine/cancel_cascade_test.go`) that skips the terminal-state filter
  in `ListActiveChildren`, simulating a corrupt/hand-edited link row that keeps
  reporting an already-cancelled node as active, and two new tests —
  `TestCancelCascade_VisitedGuardHaltsSelfReferentialCycleWithoutStateCoincidence`
  and `_MutualCycleWithoutStateCoincidence` — that reuse the self-referential
  and mutual-cycle fixtures with `unfiltered: true` and assert the exact
  intervention count (1 and 2 respectively), which only holds if `cancelAuthorized`
  is invoked exactly once per node; a second invocation (the guard failing to
  halt) would still return `ErrCannotCancel` and keep recursing without ever
  failing a looser "ends cancelled" assertion, so the exact count is what makes
  the test meaningful. Confirmed both fail for the right reason — an unbounded
  recursion that blows the goroutine stack rather than returning — when the
  `visited[childID]` check is temporarily replaced with `false && visited[childID]`,
  then confirmed both pass against the real code. This closes the guard
  half of area 22's checklist; review 1 approval is still not recorded this
  pass because the sibling gap below (intervention records for a storage-failure
  sibling branch) remains open.

- [x] **`cascadeCancel` recursed into a child's descendants even when that child
  itself failed cancellation for a REAL reason (authorization denied, not-found,
  storage error) — not just the benign already-terminal case.** Focus area 22
  (cancel cascade), review 1. `domain/engine/cancel_cascade.go`'s pre-fix loop
  called `e.cascadeCancel(ctx, childID, visited)` unconditionally after
  `cancelAuthorized(ctx, childID)`, regardless of WHY it failed. The design's own
  authorization claim ("children inherit the parent's owner, so an identity
  authorized to intervene on the parent is authorized on every descendant") is
  true for OwnerID but not for Department: `canIntervene` also grants department
  LEADS, and a spawned child's `Department` is derived from that child's OWN
  workflow publication (`resolvePublication`), not inherited from its parent —
  so a department lead authorized on the parent can be genuinely UNauthorized on
  a child scoped to a different department. `go tool cover` confirmed the
  non-`ErrCannotCancel` branch (the `slog.Warn` at the old lines 67-70) was at 0
  hits — no existing test drove `cancelAuthorized` to fail for any reason other
  than the parent's own uncancellable state or a `ListActiveChildren` storage
  error at the TOP of the cascade. Consequence: a department-lead actor
  cancelling a parent whose child is scoped to a department they don't lead
  would see that child correctly left running (authorization denied) — but the
  cascade would still recurse into the unauthorized child's OWN children via the
  same ctx identity, cancelling a grandchild whose department the actor DOES
  lead. Result: a live, running node (the child) sandwiched between a cancelled
  parent and a cancelled grandchild, with no operator-visible error beyond a
  `slog.Warn` log line — exactly the "systematic authorization failure would be
  invisible" shape already flagged in this file's Open section. Added
  `cascadeFixture.runInDepartment` (`domain/engine/cancel_cascade_test.go`) and
  `TestCancelCascade_AuthorizationFailureOnAChildHaltsThatBranch`, which drives a
  department-lead actor (leads "eng", member of "eng"+"finance") through a
  parent("eng")→child("finance")→grandchild("eng") chain and asserts the
  authorization-denied child AND the otherwise-reachable grandchild both stay
  `StateRunning`; confirmed it fails for the right reason (grandchild wrongly
  reaches `StateCancelled`) against the pre-fix code, then confirmed it passes
  after changing `cascadeCancel` to only recurse past a failed child when the
  failure is `ErrCannotCancel` (already terminal — genuinely safe to still check
  its own children), and to `continue` without descending for any other error.
  Ran `go test ./domain/engine/...` (full `TestCancelCascade` suite plus the
  whole package), `go test ./...`, `npx tsc --noEmit`, and `npx vitest run`: no
  new failures beyond the recorded baseline (the one pre-existing
  `adapter/config` failure is unchanged; dashboard typecheck and vitest both
  clean). While independently re-deriving the rest of area 22's checklist from
  source, found the visited-set guard is currently proven redundant rather than
  load-bearing by its own two "cycle" tests (logged separately above under
  Open, not fixed here to keep this pass to one issue) — so review 1 approval
  for area 22 is **not** recorded this pass; the checklist item "visited-set
  guard" is not yet genuinely verified.

- [x] **`SpawnWaitReconciler.Reconcile`'s "one failure doesn't abandon the rest"
  guarantee was proven only for a `Get` failure, never for the sibling
  `SetQueued` failure.** Focus area 21 (startup spawn-wait reconciler),
  review 2. `domain/engine/spawn_reconciler.go:83-87` documents and
  implements a best-effort posture: log a `SetQueued` failure and `continue`
  to the next candidate rather than aborting the sweep — but the only
  existing test that exercises this branch,
  `TestSpawnWaitReconciler_SetQueuedFailureIsLoggedNotFatal`, drives exactly
  ONE candidate through it, so it cannot distinguish `continue` from a
  regression that aborted the whole loop the moment any one write failed
  (e.g. an early `return nil`). The sibling
  `TestSpawnWaitReconciler_OneFailureDoesNotAbandonTheRest` proves the
  loop-continues shape, but only for the OTHER failure mode one line above
  (`instances.Get` on a missing parent) — the same shape of gap this loop has
  fixed repeatedly elsewhere (areas 19, 20): a shared best-effort contract
  proven at one call site/failure-mode and silently assumed at a sibling one.
  A regression here would silently strand every candidate queued after the
  first uncooperative one on a real restart, each waiting until the NEXT
  reconcile pass instead of being requeued immediately. Added a
  `setQueuedErrFor map[uuid.UUID]error` field to `mockQueueRepo`
  (`domain/engine/engine_test.go`, checked before the existing global
  `setQueuedErr`) so a test can fail `SetQueued` for one specific candidate
  while leaving a later one free to succeed, and added
  `TestSpawnWaitReconciler_SetQueuedFailureForOneCandidateDoesNotAbandonTheRest`
  (`domain/engine/spawn_reconciler_test.go`), asserting the failing
  candidate's parent stays `waiting`, a later candidate is still `queued`,
  and `NotifyCapacityFreed` still fires once for the one that succeeded;
  confirmed it fails for the right reason (later candidate stuck `waiting`,
  zero notifies) when the `continue` after the `SetQueued` error check is
  temporarily replaced with `return nil`, then confirmed it passes against
  the real code. Independently re-verified the rest of area 21's checklist
  from source rather than trusting review 1's write-up: re-read
  `cmd/arbitron/main.go:1032-1059` and confirmed the reconciler is still
  constructed only at the real composition root, still run after
  `RecoverCrashedInstances`, and its own error is still logged, not fatal;
  confirmed `SpawnWaitCandidate.ChildState` is populated by
  `ListWaitingOnTerminalChild`'s SQL (`adapter/sqlite/instance_repo.go:725-733`,
  `WHERE ... c.state IN ('completed','failed','cancelled')`) but genuinely
  unused inside `Reconcile` itself — not a gap, since the terminal-filtering
  already happens at the query layer and terminal states never revert;
  `go tool cover` over the `TestSpawnWaitReconciler`-prefixed suite shows
  `Reconcile` at 95.7% (the sole remaining gap, the defensive `ctx == nil`
  guard, has no known production caller and was already logged/left as a
  residual, lower-priority item when review 1 closed — the identical shape
  already accepted for `OnChildTerminal`'s own such guard in area 19). Ran
  the full `go test ./...`, `npx tsc --noEmit`, and `npx vitest run` suites:
  no new failures beyond the recorded baseline (`domain/engine` passes; the
  one pre-existing `adapter/config` failure is unchanged; dashboard
  typecheck and vitest are both clean). This closes review 2 approval for
  area 21 — both approvals for area 21 are now recorded.

- [x] **`SpawnWaitReconciler.Reconcile`'s own `SetQueued` re-queue failure
  branch was untested.** Focus area 21 (startup spawn-wait reconciler),
  review 1. `domain/engine/spawn_reconciler.go:83-87` documents a best-effort
  posture for each candidate — matching `Engine.OnChildTerminal`'s own
  "best-effort/void" contract (area 19) — logging a `SetQueued` failure and
  continuing to the next candidate rather than aborting the sweep. `go tool
  cover` over the full `TestSpawnWaitReconciler`-prefixed suite showed this
  branch (lines 83-86) at 0 hits: the existing
  `TestSpawnWaitReconciler_OneFailureDoesNotAbandonTheRest` exercises a
  *different* failure mode (an unreadable parent via `instances.Get`), never a
  `SetQueued` write failure. The same shape of gap fixed repeatedly elsewhere
  in this loop (areas 19, 20): a shared best-effort contract proven at one
  call site (`OnChildTerminal`'s own `SetQueued` failure test,
  `spawn_wake_test.go`) and silently assumed at a sibling one one file away. A
  regression that let a `SetQueued` error propagate up through `Reconcile`
  (turning a per-candidate best-effort failure into a sweep-aborting one,
  stranding every subsequent candidate) or that called `NotifyCapacityFreed`
  regardless of the error (misleading the queue into thinking a parent that
  was never actually re-queued had capacity freed) would have shipped
  undetected. Added a `queueRepo *mockQueueRepo` field to `reconcilerFixture`
  (`domain/engine/spawn_reconciler_test.go`) so the test can reach the same
  `setQueuedErr` injection point `spawn_wake_test.go` already uses, and added
  `TestSpawnWaitReconciler_SetQueuedFailureIsLoggedNotFatal`, asserting
  `Reconcile` returns no error, the parent stays `StateWaiting` (not desynced
  into `StateQueued` on a failed write), and `NotifyCapacityFreed` is never
  called; confirmed it fails for the right reason (`NotifyCapacityFreed`
  called once despite the injected error) when the error check is temporarily
  replaced with a discarded `_ =`, then confirmed it passes against the real
  code. `go tool cover` afterward shows `Reconcile` at 95.7% (the one
  remaining uncovered branch, the defensive `ctx == nil` guard at lines
  57-59, has no known production caller — `main.go:1057` always passes a live
  `context.Background()` — the same low-priority, unreachable-by-production
  shape already logged and left unfixed for `OnChildTerminal`'s identical
  guard when area 19 was closed). Independently re-verified the rest of area
  21's checklist from source: `NewSpawnWaitReconciler` is constructed only at
  the real composition root (`cmd/arbitron/main.go:1056`, grepped for all
  non-test call sites) with all four collaborators real, non-nil values (the
  same `instanceRepo` value satisfies both the `SpawnWaitRepository` and
  `InstanceRepository` parameters); `SetQueued` is called once per candidate
  while `NotifyCapacityFreed` is called at most once per `Reconcile` call
  (only when `requeued > 0`), confirmed intentional by
  `TestSpawnWaitReconciler_DuplicateCandidatesForSameParentAreHandledOnce`'s
  own assertion ("one notify per Reconcile call, not one per duplicate
  candidate"); the query error is *returned* (not swallowed) so
  `main.go:1057-1058` can log it without failing startup
  (`TestSpawnWaitReconciler_QueryFailureIsReturnedNotPanicked`); and the
  must-run-after-`RecoverCrashedInstances` ordering is proven both against
  fakes (`TestSpawnWaitReconciler_ResumesParentOfACrashedChildAfterRecovery`)
  and end-to-end through the real composition root at a real restart
  (`cmd/arbitron/spawn_wait_e2e_test.go`'s
  `TestSpawnWaitE2E_RestartReconcilesParentAfterCrash`, added when area 16
  review 1 closed). The five scenario categories (happy, success, failure,
  error, edge) are all present and now include a genuine best-effort-write
  failure rather than only a load failure. This closes review 1 approval for
  area 21; review 2 remains open.

- [x] **`OnTimerFired`'s own webhook-timeout `notifyChildTerminal` call had no
  test that proves the wired hook actually fires.** Focus area 20
  (queue-manager child-terminal hook), review 2. Found while closing area 20
  review 1 (`domain/queue/manager.go:186-226`). `go tool cover` showed the
  `m.notifyChildTerminal(ctx, instanceID)` statement at line 222 already at 1
  hit — but only because the call executed with `m.childTerminal == nil` in
  every existing `TestOnTimerFired_WebhookTimeout_*` test
  (`domain/queue/manager_test.go`); none of them called
  `m.SetChildTerminalHook(...)` first and asserted the hook was invoked with
  the right instance ID, the way `TestRecoverWebhooks_TimedOut_WakesChildTerminalHook`
  already did for the startup-recovery sibling path. Same shape of gap as the
  `RecoverWebhooks` fix below: a shared correctness guarantee proven at one
  call site and silently assumed at a sibling one — no known live bug (the
  call is present and unconditional on the success path), but the LIVE path
  (the one every real webhook timeout goes through) deserved its own dedicated
  hook-invocation test independent of the startup-recovery one, not just
  statement coverage that happened to pass through a nil hook. Added
  `TestOnTimerFired_WebhookTimeout_InvokesChildTerminalHook`
  (`domain/queue/manager_test.go`), wiring `m.SetChildTerminalHook(...)` before
  driving `m.OnTimerFired(ctx, id, queue.TimerTypeWebhook)` and asserting the
  hook fires exactly once with the timed-out instance's own ID, mirroring
  `TestManager_SetChildTerminalHook_IsInvoked`'s assertion shape; confirmed it
  fails for the right reason (hook never called, zero-value UUID) when the
  `m.notifyChildTerminal(ctx, instanceID)` call is temporarily wrapped in
  `if false { ... }`, then confirmed it passes against the real code.
  Independently re-verified the rest of area 20's checklist from source:
  `notifyChildTerminal` is nil-safe (`TestManager_ChildTerminalHook_IsNilSafe`
  and the un-hooked webhook-timeout tests, which never panic), `domain/queue`
  has no `arbitron/domain/engine` import (grepped the package directly), and
  the hook is wired at the real composition root
  (`cmd/arbitron/main.go:815`, `queueMgr.SetChildTerminalHook(eng.OnChildTerminal)`,
  already exercised end-to-end by area 16's `TestSpawnWaitE2E_*` suite). This
  closes review 2 approval for area 20 — both approvals for area 20 are now
  recorded.

- [x] **`RecoverWebhooks` failed a timed-out webhook child during startup
  recovery without calling the child-terminal wake, unlike its live
  equivalent.** Focus area 20 (queue-manager child-terminal hook), review 1.
  Originally found while reviewing area 19 (child-terminal wake).
  `domain/queue/manager.go:186-226`'s `OnTimerFired` (the LIVE webhook-timeout
  path) does `SetFailed` then `m.notifyChildTerminal(ctx, instanceID)` with a
  comment noting this is "the one terminal transition that happens outside
  domain/engine." But `domain/queue/recovery.go:47-92`'s `RecoverWebhooks` (the
  STARTUP recovery path, called after `spawnReconciler.Reconcile` in
  `cmd/arbitron/main.go` — the reconciler has already run by the time this
  failure happens) did the IDENTICAL `SetFailed` for a webhook instance whose
  timeout expired during downtime, but never called `notifyChildTerminal` —
  the same shape of gap this loop has fixed repeatedly for other
  terminal-transition call sites (areas 19, 16): a shared correctness guarantee
  proven at one call site and silently assumed at a sibling one. Consequence: a
  `wait_for_child` parent whose child was mid-webhook-wait at the moment of a
  crash, and whose webhook timeout had already passed during the downtime
  window, would never have been woken — not by the reconciler (which ran
  before this failure), not by this path (which didn't call the hook) — until
  the NEXT process restart. Added
  `TestRecoverWebhooks_TimedOut_WakesChildTerminalHook`
  (`domain/queue/recovery_test.go`), which wires a `SetChildTerminalHook` on
  the manager, drives `RecoverWebhooks` over a timed-out webhook instance, and
  asserts the hook fires with that instance's ID; confirmed it fails for the
  right reason (`[]uuid.UUID(nil)`, hook never invoked) against the
  pre-fix code, then confirmed it passes after adding
  `m.notifyChildTerminal(ctx, inst.ID)` to `RecoverWebhooks` right after the
  `SetFailed` succeeds (only on the success path — a `SetFailed` failure still
  logs and `continue`s without waking, matching `OnTimerFired`'s own
  error-propagation shape). Confirmed the rest of area 20's checklist from
  source: `SetChildTerminalHook`/`notifyChildTerminal` are nil-safe
  (`TestManager_ChildTerminalHook_IsNilSafe`), `domain/queue` has no
  `domain/engine` import (grepped the package), and the hook is wired at the
  real composition root (`cmd/arbitron/main.go:815`,
  `queueMgr.SetChildTerminalHook(eng.OnChildTerminal)`, already verified
  end-to-end by area 16's `TestSpawnWaitE2E_*` suite). Left one new,
  lower-priority gap for review 2 (logged above under Open): the LIVE
  `OnTimerFired` webhook-timeout path's own `notifyChildTerminal` call has
  statement coverage but no test that wires a hook and asserts it fires — not
  a live bug (the call is unconditional on the success path) but the same
  shape of unverified-call-site gap just fixed here for its startup-recovery
  sibling. This closes review 1 approval for area 20; review 2 remains open.

- [x] **`OnChildTerminal`'s own `queueRepo.SetQueued` failure branch — the
  function's documented "best-effort" contract — was untested.** Focus area 19
  (child-terminal wake), review 2. `domain/engine/spawn_wake.go`'s own
  docstring states the wake "is deliberately VOID and best-effort. A missed
  wake is recovered by `SpawnWaitReconciler` at the next startup; a wake that
  fails must never fail the terminal transition that triggered it." `go tool
  cover` over the full `TestOnChildTerminal`/`TestRunCompletion`/
  `TestResumeFromWebhookWaitTimeout`/`TestCancelCascade` suite showed
  `spawn_wake.go` at only 77.3% statement coverage, with the `SetQueued` error
  branch (lines 68-74: log-and-return, no `NotifyCapacityFreed`) at 0 hits —
  the existing `mockQueueRepo` (`domain/engine/engine_test.go`) had no way to
  inject a `SetQueued` error at all. A regression that let a `SetQueued` error
  propagate up through `failInstance`/`cancelAuthorized`/the run loop's
  completion path (turning a best-effort wake failure into a fatal one for the
  terminal transition that triggered it), or that called
  `NotifyCapacityFreed` regardless of the error (misleading the queue into
  thinking a parent that was never actually re-queued had capacity freed up),
  would have shipped undetected. Added a `setQueuedErr` field to
  `mockQueueRepo` and `TestOnChildTerminal_SetQueuedFailureIsSurfacedNotFatal`
  (`domain/engine/spawn_wake_test.go`), asserting the call does not panic, the
  parent stays `StateWaiting` (not desynced into `StateQueued` on a failed
  write), and `NotifyCapacityFreed` is never called; confirmed it fails for the
  right reason (`NotifyCapacityFreed` called once despite the injected error)
  when the error check is temporarily replaced with a discarded `_ =`, then
  confirmed it passes against the real code. Independently re-verified the
  rest of area 19's checklist from source rather than trusting review 1's
  write-up: re-derived the three-part match's three conjuncts each from their
  own dedicated tests; confirmed via `git diff` against the branch point that
  exactly three run-loop `SetFailed` call sites (panic recovery, workflow-parse
  failure, empty-current-node failure) were converted to `failInstance`, and
  that `OnChildTerminal` calls were added at exactly four sites — the run-loop
  completion path, `failInstance`, `cancelAuthorized` (newly extracted from
  `Cancel` by this feature), and `resumeFromWebhookWait` (review 1's fix) — all
  four are exercised by a real call through the public API rooted at
  `Engine.Trigger`/`Engine.Cancel`/`Engine.Resume`, not just direct
  `OnChildTerminal` calls: `cmd/arbitron/spawn_wait_e2e_test.go`'s
  `TestSpawnWaitE2E_CompletedChildRoutesParentThroughSuccess` (run-loop
  completion), `_FailedChildRoutesParentThroughFailure` (a node failure driving
  `failInstance` through `handleNodeError`), and
  `_CancelledChildRoutesParentThroughFailure` (`ds.eng.Cancel` on the CHILD
  directly, proving `cancelAuthorized`'s own call site, not just the cascade's)
  all drive through the real composition root; `resumeFromWebhookWait`'s own
  call site is proven through `Engine.Resume` in
  `TestResumeFromWebhookWaitTimeout_InvokesTheChildTerminalWake`. Confirmed
  `RecoverCrashedInstances` (`engine.go:1664`, unchanged by this feature per
  `git diff`) deliberately does NOT call the wake — it runs before
  `SpawnWaitReconciler.Reconcile` in the startup order (area 16/21), which is
  the substitute wake for exactly this path, so its own waiting parents are
  picked up by `ListWaitingOnTerminalChild` instead. Left the two remaining
  untested defensive branches (`ctx == nil`, and the `!child.State.IsTerminal()`
  "called too early" guard, and the parent-`Get`-error branch) unfixed: no
  known production caller can reach them today (all four call sites already
  write the terminal state before calling the wake), and the `ctx == nil`
  branch mirrors the same never-tested defensive pattern already established
  pre-feature in `Trigger`/`TriggerWithDepartment` (noted when closing area 13);
  logged as a residual, lower-priority coverage gap rather than fixed in this
  pass to keep the pass to one issue. This closes review 2 approval for area
  19 — both approvals for area 19 are now recorded.

- [x] **`Engine.Resume`'s webhook-timeout-expired-while-paused branch failed an
  instance without calling the child-terminal wake — a fourth terminal-transition
  call site the area 19 checklist's "three engine call sites" claim missed.**
  Focus area 19 (child-terminal wake), review 1. This pre-dates the spawn
  feature (identical at the branch point,
  `64768283d2c32b64023801103c7aab2f5ea538d3:domain/engine/engine.go:1314`) — the
  feature converted the run loop's three internal `SetFailed` call sites to go
  through the new `failInstance` helper (confirmed via `git diff` against the
  branch point: panic recovery, workflow-parse failure, and empty-current-node
  failure, all now `e.failInstance(...)`, each of which calls
  `e.OnChildTerminal`) and added the call directly in `cancelAuthorized`, but
  never touched `resumeFromWebhookWait`'s own `e.instances.SetFailed` at
  `engine.go:1358` (reached via `Engine.Resume`, itself reachable in production
  from both `adapter/mcp/tools_instance.go:218` and
  `adapter/http/instance_handler.go:512`) — the exact shape of gap this loop
  keeps finding: a shared correctness guarantee proven at some call sites and
  silently assumed at a sibling one three call sites away. Consequence: a
  `wait_for_child` child instance that is paused mid-webhook-wait and resumed
  after its timeout has already expired is failed via this path, but its
  waiting parent is never re-queued — it hangs until the next full-process
  restart's `SpawnWaitReconciler` pass instead of resuming immediately, same as
  every other terminal-transition gap this loop has fixed for this feature.
  Added `TestResumeFromWebhookWaitTimeout_InvokesTheChildTerminalWake`
  (`domain/engine/spawn_wake_test.go`), reusing the `wakeFixture` scaffold
  with a `StatePaused` child carrying an expired `WebhookToken`/`WaitingUntil`,
  driven through the real `Engine.Resume(bgAdmin(), ...)` entry point (not
  `OnChildTerminal` called directly, unlike every other test in this file, so
  it proves the wiring at the actual call site); confirmed it fails for the
  right reason (parent stays `waiting` instead of `queued`) before the fix,
  then passes after adding `e.OnChildTerminal(context.Background(), instanceID)`
  to `resumeFromWebhookWait` right after the `SetFailed` succeeds, mirroring
  the `failInstance`/`cancelAuthorized` pattern. Independently re-verified the
  rest of area 19's checklist: the three-part match
  (waiting ∧ `CurrentNodeID`==`ParentNodeID` ∧ mode==wait_for_child) and its
  best-effort/void posture are unchanged and covered by the existing
  `spawn_wake_test.go` suite; also closed two previously-open gaps this same
  review touches — confirmed `instances.Get` and `queueRepo.SetQueued`
  (`adapter/sqlite/instance_repo.go:132`, `adapter/sqlite/queue_repo.go:76`)
  read no context value at all (no identity/tenant/tracing lookup), so
  `OnChildTerminal`'s `context.Background()` calls drop nothing load-bearing;
  and confirmed `TestOnChildTerminal_NodeMismatchDoesNotWake` genuinely
  exercises the real match logic — `mockInstanceRepo.Get`
  (`domain/engine/engine_test.go:124`) returns instances verbatim by ID with no
  `ParentNodeID`-based filtering, so the node-mismatch guard is proven in
  `OnChildTerminal` itself, not pre-filtered away by the fake. While reviewing
  this area, found a related but out-of-scope gap in `domain/queue/recovery.go`
  (area 20's file, not area 19's) — logged separately under Open rather than
  fixed here, to keep this pass to one issue. This closes review 1 approval for
  area 19; review 2 remains open.

- [x] **Area 18 (awaited-spawn resumption short-circuit), review 2: independent
  re-verification found no live gap.** Re-derived every checklist item from
  source rather than trusting review 1's write-up. `go tool cover` over
  `TestSpawnResume`-prefixed tests shows `resumeFromLink` at 100% statement
  coverage. Confirmed the LINK-not-node-exec-status key by reading
  `FindChildBySpawnNode` (`adapter/sqlite/instance_repo.go:693-712`) and its
  own suite, including `TestFindChildBySpawnNode_MultipleChildrenReturnsTheNewest`
  — which specifically defeats same-second `created_at` collisions with a
  `rowid DESC` tie-break and documents that dropping the tie-break "would
  silently make the executor's resumption short-circuit key on the WRONG
  child" — and confirmed the fake used in the executor's own suite,
  `fakeSpawnWaits`, honours all three key parts (parent, node, mode) rather
  than collapsing them, so the mis-wake/wrong-mode guards in
  `TestSpawnResume_SecondSpawnNodeMatchedIndependently` and
  `_FireAndForgetLinkDoesNotSatisfyAwaitedLookup` are unfailable-if-broken by
  construction, not by luck. Confirmed the still-active path (no re-spawn, no
  re-transition) is exercised for all four non-terminal `instance.State`
  values (`TestSpawnResume_ActiveChildReturnsToWaitingWithoutSpawning`) and
  terminal routing — including cancelled-routes-to-failure — is proven both
  against fakes and end-to-end through the real composition root
  (`cmd/arbitron/spawn_wait_e2e_test.go`'s
  `TestSpawnWaitE2E_CompletedChildRoutesParentThroughSuccess`,
  `_FailedChildRoutesParentThroughFailure`,
  `_CancelledChildRoutesParentThroughFailure`). Confirmed review 1's
  `childState`-across-all-outcomes fix
  (`TestSpawnResume_ChildStateReflectsExactLinkStateAcrossAllOutcomes`) is
  exhaustive over all seven `instance.State` values, matching
  `domain/instance/instance.go`'s complete enumeration — no state was left
  untested. Verified `TestSpawnResume_BackEdgeRevisitRoutesFromPreviousChild`
  is not a redundant duplicate of the Completed-routes-success case despite
  identical assertions: per `AGENTS.md:88`, the resumption short-circuit
  genuinely cannot distinguish a wake re-entry from a back-edge revisit — they
  are the identical code path by design — so the test correctly pins that
  documented limitation as tested behaviour rather than fabricating a
  distinct mechanism that does not exist. Confirmed
  `SpawnWorkflowExecutor`/`resumeFromLink` has no separate construction site
  outside the composition root already verified in area 16. No code change
  was needed; this closes review 2 approval for area 18 — both approvals for
  area 18 are now recorded.

- [x] **`resumeFromLink`'s "childState in the output" claim was pinned for only
  one of seven possible link states.** Focus area 18 (awaited-spawn resumption
  short-circuit), review 1. `domain/engine/executor_spawn.go:183-184` writes
  `output["childState"] = string(link.ChildState)` unconditionally, BEFORE the
  terminal/still-active branch — so every resumption, terminal or not, must
  carry the child's exact state in its output. But
  `TestSpawnResume_ResultCarriesChildOutcome`
  (`domain/engine/executor_spawn_resume_test.go`) only asserted this for a
  `Completed` child; the sibling routing tests
  (`TestSpawnResume_FailedChildRoutesFailure`,
  `_CancelledChildRoutesFailure`) asserted only `OutputPort`, and the
  still-active edge-case test
  (`TestSpawnResume_ActiveChildReturnsToWaitingWithoutSpawning`) never
  inspected `res.Output` at all — the same shape of gap fixed repeatedly
  elsewhere in this loop: a claim proven at one call site and silently assumed
  at every sibling one. A regression that moved the `childState` write inside
  the terminal branch, or hard-coded it only for the success path, would have
  shipped undetected — leaving the trace silently missing "how the child
  ended" for a failed/cancelled/still-waiting resumption, exactly the
  self-explanatory-trace guarantee the field's own comment promises. Added
  `TestSpawnResume_ChildStateReflectsExactLinkStateAcrossAllOutcomes`, which
  drives `resumeWith` across all seven `instance.State` values and asserts
  `out["childState"]` equals the exact state given every time; confirmed it
  fails for the right reason (`childState` absent from every case) when the
  `output["childState"] = ...` line is temporarily removed, then confirmed it
  passes against the real code. Independently reviewed the rest of area 18's
  checklist from source: `executeWait` keys resumption on
  `FindChildBySpawnNode`'s link, never on node-execution status
  (`TestSpawnResume_BackEdgeRevisitRoutesFromPreviousChild` pins this as
  tested behaviour, not an assumption); the still-active branch correctly
  covers all four non-terminal states with no re-spawn and no re-transition
  (`TestSpawnResume_ActiveChildReturnsToWaitingWithoutSpawning`); terminal
  routing sends `Completed` to success and both `Failed` and `Cancelled` to
  the SAME failure port, matching the spec's no-separate-cancellation-path
  requirement, and is proven at the sink by the real composition root in
  `cmd/arbitron/spawn_wait_e2e_test.go` (`TestSpawnWaitE2E_*RoutesParentThrough*`),
  not just through fakes; the link key includes both node id and mode
  (`TestSpawnResume_SecondSpawnNodeMatchedIndependently`,
  `_FireAndForgetLinkDoesNotSatisfyAwaitedLookup`); `instance.ChildLink{...}`
  is constructed only by the real `InstanceRepo.FindChildBySpawnNode`
  (`adapter/sqlite/instance_repo.go:711`), grepped for all non-test call
  sites; and the "attempt=1 written twice" shape (once by `executeWait` on
  suspend, once by `executeWithRetry` on a terminal resume) is inherited,
  pre-existing behaviour shared by every other wait-style executor
  (`WebhookWaitExecutor` does the identical thing), not a gap this feature
  introduced. This closes review 1 approval for area 18; review 2 remains
  open.

- [x] **Area 17 (spawn executor — wait-for-child suspension), review 2: independent
  re-verification found no live gap.** Re-derived every checklist item from
  source rather than trusting review 1's write-up. Confirmed via `go tool
  cover` (running the full `TestSpawn`-prefixed suite) that `Execute`,
  `executeFireAndForget`, `executeWait`, `resumeFromLink`, `prepareAndSpawn`,
  `spawnOutput`, and `assertSpawnBranchPorts` are all at 100% statement
  coverage (a `TestSpawnWait`-only run shows `executeWait` at 95.5% because the
  `link != nil` short-circuit belongs to area 18's own tests, not this area's).
  Re-confirmed the ordering invariant (enqueue → node-exec record →
  `UpdateState(waiting)` → `NotifyCapacityFreed`) is proven twice: once as a
  synchronous call-order assertion against fakes
  (`TestSpawnWait_OrderingInvariant_SpawnThenRecordThenWaitingThenNotify`) and
  once end-to-end against the REAL `queue.Manager` with `perusercap: 1` — a
  config under which the test would hang if the ordering broke
  (`cmd/arbitron/spawn_wait_e2e_test.go`'s `bootWaitStack`/
  `TestSpawnWaitE2E_CompletedChildRoutesParentThroughSuccess` and siblings).
  Confirmed `assertSpawnBranchPorts` scopes edges by `edge.Source == nodeID`
  (no cross-node false positives) and is checked BEFORE `prepareAndSpawn`
  (`TestSpawnWait_MissingFailurePort_FailsBeforeCreatingChild`,
  `_MissingSuccessPort_...`, `_ParentLoadForBranchPortCheck_...`, each
  asserting zero preparer calls / zero spawned children). Confirmed
  `NewSpawnWorkflowExecutor` is constructed only at the real composition root
  (`cmd/arbitron/main.go:894`, grepped for all non-test call sites). Confirmed
  `queue.Manager.NotifyCapacityFreed`'s own `_ context.Context` parameter (it
  fires its evaluation via `context.Background()` in a goroutine) is
  deliberate fire-and-forget behavior, not a dropped-argument bug, and the
  test double (`mockQueueManager`) matches this shape rather than silently
  diverging from it. No code change was needed; this closes review 2 approval
  for area 17 — both approvals for area 17 are now recorded.

- [x] **`executeWait`'s own `instances.Get` failure (the load before
  `assertSpawnBranchPorts`) was untested.** Focus area 17 (spawn executor —
  wait-for-child suspension), review 1. `domain/engine/executor_spawn.go:117-120`
  loads the parent to check its snapshot's branch ports BEFORE any child is
  created — a distinct call site from `prepareAndSpawn`'s own `instances.Get`
  (already covered by `TestSpawnExecutor_ParentInstanceLoadFailure_Errors` for
  the fire-and-forget path). `go tool cover` over `TestSpawnWait`-prefixed
  tests showed this branch (lines 118-120) at 0% hits: no existing test in
  `executor_spawn_wait_test.go` set `f.instances.getErr`. Added
  `TestSpawnWait_ParentLoadForBranchPortCheck_FailsBeforeCreatingChild`
  (`domain/engine/executor_spawn_wait_test.go`), asserting the node errors, no
  child is spawned, and the preparer is never called (the load failure must
  precede any LLM cost); confirmed it fails for the right reason (nil-pointer
  panic dereferencing `parent.WorkflowSnapshot`) when the error check is
  temporarily replaced with `_ = err; if false { ... }`, then confirmed it
  passes against the real code. `go tool cover` afterward shows the branch at
  1 hit and `executeWait`'s own-scope statements at 95.5% (the remaining
  uncovered branch, the `link != nil` resumption early-return, belongs to area
  18, not this area). Also re-confirmed the rest of area 17's checklist: the
  full first-execution path (happy/success), both missing-port failures, the
  preparation and waiting-transition failures, and the node-exec-write failure
  (fixed below) together cover all five scenario categories; the ordering
  invariant is proven against a REAL `queue.Manager` (not a mock) by
  `cmd/arbitron/spawn_wait_e2e_test.go`'s `bootWaitStack` fixture; and
  `SpawnWorkflowExecutor` is constructed only at the real composition root
  (`cmd/arbitron/main.go:894`), confirmed by grepping for
  `NewSpawnWorkflowExecutor` outside `*_test.go`. This closes review 1
  approval for area 17; review 2 remains open.

- [x] **`nodeExecs.Create`'s failure inside `executeWait` had no test, unlike
  the identical failure mode already pinned for the sibling
  `WebhookWaitExecutor`.** Focus area 17 (spawn executor — wait-for-child
  suspension); review 1 remains open (see the `executeWait` `instances.Get` gap
  above). `domain/engine/executor_spawn.go:146-157` writes the wait path's own
  node-execution record — deliberately, since `executeWithRetry` skips one on
  the wait path — as part of the load-bearing ordering this focus area exists
  to check: `enqueue → node-exec record → UpdateState(waiting) →
  NotifyCapacityFreed`. `go tool cover` over `TestSpawnWait`-prefixed tests
  showed the `Create` error branch (lines 155-157) at 0% hits, while
  `TestWebhookWaitExecutor_NodeExecCreateFails_ReturnsError`
  (`executor_webhook_test.go`) already pins the identical shape one call away
  in the sibling wait executor — the same gap already fixed repeatedly in this
  loop: a behavior proven at one call site and silently assumed at a sibling
  one. A regression that swallowed this error (e.g. logging instead of
  returning) would leave the child running with the parent stuck in
  `StateRunning` forever, since neither `UpdateState(waiting)` nor
  `NotifyCapacityFreed` would ever run afterward — undetected by any existing
  test. Added
  `TestSpawnWait_NodeExecutionRecordWriteFails_ChildSurvivesAndParentNotSuspended`
  (`domain/engine/executor_spawn_wait_test.go`), asserting the child is not
  rolled back and the parent remains `StateRunning`; confirmed it fails for the
  right reason (no error returned) when the `Create` error check is temporarily
  replaced with a discarded `_ =`, then confirmed it passes against the real
  code. Also confirmed via the same coverage run that the whole-of-instance
  per-user-capacity deadlock guard this focus area names ("ordering invariant")
  is already proven against the REAL `queue.Manager` — not just a mock — by
  `cmd/arbitron/spawn_wait_e2e_test.go`'s `bootWaitStack` fixture, which sets
  `perusercap: 1` specifically so the test would hang if the ordering broke;
  this was not a gap.

- [x] **`eng.SetSpawnLinks(instanceRepo)`'s composition-root wiring for the
  cancel cascade was verified only by reading `main.go`, never by a real
  composition-root test.** Focus area 16 review 2. `cascadeCancel`
  (`domain/engine/cancel_cascade.go:47-49`) documents and implements a
  load-bearing no-op: `if e.spawnLinks == nil { return }` — cancelling a parent
  silently leaves every active child running, exactly the shape of gap this
  loop has fixed repeatedly for other composition-root seams (the reconciler
  ordering fixed in review 1, above). Every existing `cascadeCancel` test
  (`domain/engine/cancel_cascade_test.go`) drives the cascade through a fake
  `SpawnLinkRepository` that the test itself constructs and injects — none of
  them prove `cmd/arbitron/main.go` actually calls `eng.SetSpawnLinks(instanceRepo)`
  before a real cancel reaches it. A regression that dropped, reordered past a
  point where `instanceRepo` isn't yet constructed, or conditionally skipped
  that one call in `main.go` would have shipped undetected: every child of a
  cancelled run would keep executing with no operator-visible error. Added
  `TestSpawnWaitE2E_CancellingParentCascadesToActiveChildThroughCompositionRoot`
  (`cmd/arbitron/spawn_wait_e2e_test.go`), which boots the real composition
  root via `initDomain`, triggers a `wait_for_child` parent so it suspends in
  `StateWaiting` (a cancellable state) with a still-`StateRunning` child, calls
  `ds.eng.Cancel` on the PARENT, and asserts the child is cascaded into
  `StateCancelled` too. Confirmed it fails for the right reason (child never
  reaches `StateCancelled`, 40s timeout) when `eng.SetSpawnLinks(instanceRepo)`
  is temporarily commented out in `main.go`, then confirmed it passes against
  the real code. Independently re-verified area 16's other six checklist items
  by re-reading `main.go` and re-deriving each claim from source rather than
  trusting review 1's write-up: `instanceEnricher.SetSpawnLinks` (:798),
  `queueMgr.SetChildTerminalHook(eng.OnChildTerminal)` (:815), the single
  `identityResolver` value (a stateless struct of interfaces/maps, so
  pass-by-value to three call sites is equivalent to sharing one instance)
  (:880, :900, :911, :924), `NewChildInputPreparer` fed real `providerFactory`/
  `profileResolver` (:893), `executors[workflow.NodeTypeSpawnWorkflow]`
  assigned after `engine.NewEngine` onto the SAME map the engine holds by
  reference (confirmed the map is built at :744 and passed to `NewEngine` at
  :757, so the later assignment at :894 is visible to the engine — the
  identical pattern already used for `timer_delay`/`webhook_wait`), and the
  reconciler ordering re-confirmed passing via
  `TestSpawnWaitE2E_RestartReconcilesParentAfterCrash`. This closes review 2
  approval for area 16; both approvals for area 16 are now recorded.

- [x] **The startup ordering "`spawnReconciler.Reconcile` runs AFTER
  `RecoverCrashedInstances`" was proven only through fakes, never through the
  real composition root.** Focus area 16 review 1. `cmd/arbitron/main.go`
  reads all six of area 16's checklist items correctly on inspection:
  `instanceEnricher.SetSpawnLinks(instanceRepo)` (:798), `eng.SetSpawnLinks(instanceRepo)`
  (:841), `queueMgr.SetChildTerminalHook(eng.OnChildTerminal)` (:815), a single
  `identityResolver` value constructed once (:880) and passed to the spawn
  executor (:900), the schedule evaluator (:911), and the schedule runner
  (:924), `NewChildInputPreparer` constructed and fed into the spawn executor
  (:893-903), and `executors[workflow.NodeTypeSpawnWorkflow]` assigned AFTER
  `engine.NewEngine` via the shared-map-reference pattern also used for
  `timer_delay`/`webhook_wait` (:894, confirmed exercised end-to-end by
  `TestSpawnE2E_FireAndForgetCreatesLinkedChildThroughCompositionRoot`,
  `cmd/arbitron/spawn_e2e_test.go`). The one claim with no composition-root
  test at all was the ordering comment at :1047 ("ORDERING IS SIGNIFICANT:
  this runs AFTER RecoverCrashedInstances... so their parents resume through
  the failure port in this same startup pass instead of hanging until the
  next restart") — `domain/engine/spawn_reconciler_test.go`'s
  `TestSpawnWaitReconciler_ResumesParentOfACrashedChildAfterRecovery` proves
  the ordering DEPENDENCY (call `RecoverCrashedInstances` then `Reconcile` on
  a shared fixture) but never proves `main.go` itself calls them in that
  order at a real restart — a regression that reordered, dropped, or
  conditionally skipped the reconciler call in `main.go` (the exact shape of
  every prior composition-root gap this loop has fixed) would have shipped
  undetected. Added `TestSpawnWaitE2E_RestartReconcilesParentAfterCrash`
  (`cmd/arbitron/spawn_wait_e2e_test.go`), which boots the real composition
  root via `initDomain`, triggers a `wait_for_child` parent, captures its
  child mid-flight (still `StateRunning`, asleep), severs the database
  out from under the live process to simulate a hard crash (no graceful
  `Shutdown`/drain), then calls `initDomain` a SECOND time against the SAME
  database file — the real restart path — and asserts the parent, which
  nothing else in the system would ever wake, resumes through the failure
  branch in this second boot. Confirmed it fails for the right reason (parent
  never reaches `StateCompleted`, 40s timeout) when the reconciler call in
  `main.go` is temporarily wrapped in `if false { ... }`, then confirmed it
  passes against the real code. This closes review 1 approval for area 16;
  review 2 remains open.

- [x] **`Engine.Spawn`'s inherited archived-workflow refusal was never exercised
  through `Spawn` itself.** Focus area 15 review 2. `Engine.Spawn`
  (`domain/engine/engine_spawn.go:52-69`) is deliberately thin: it derives a
  context carrying the link and delegates entirely to `TriggerWithDepartment`,
  whose docstring states every one of its behaviours — including refusing an
  archived workflow (`engine.go:330-348`) — is "inherited rather than
  reimplemented" so no owner-override refactor of the trigger path is needed.
  Design Phase 3's own testing strategy lists "the child workflow is archived
  → node fails" as a required **failure**-category case for the spawn path.
  But every existing archived-refusal test
  (`TestTrigger_Archived_Refused`/`_NotArchived_Succeeds`/
  `_NotArchivedResolver_NeverBlocks`/`_IsArchivedError_FailsClosed`,
  `domain/engine/engine_archived_trigger_test.go`) drives the check through
  `Trigger`, never through `Spawn` — the exact shape of gap already fixed
  repeatedly in this loop (areas 12, 13, 14): a shared helper's behavior
  proven at one call site and silently assumed at a sibling one that reaches
  it one line away through identical delegation. Confirmed today's code has
  no live bug — `Spawn` inherits the check for free by construction — but an
  untested inheritance claim is exactly the gap this loop exists to close,
  and a future refactor that gave `Spawn` its own bypass of
  `TriggerWithDepartment`, or reordered the archive check past instance
  creation, would ship undetected. Added
  `TestEngineSpawn_ArchivedChildWorkflow_Refused`
  (`domain/engine/engine_spawn_test.go`), reusing the existing
  `spawnerFixture`/`archiveResolver` fixtures, asserting `Spawn` returns
  `workflow.ErrArchived` and creates no instance for an archived child
  workflow; confirmed it fails for the right reason (`nil` error) when the
  archived-check branch in `TriggerWithDepartment` is temporarily neutered
  (`if false && e.resolver != nil`), then confirmed it passes against the
  real code. Independently re-verified the rest of area 15's checklist:
  `go tool cover` over `TestSpawn`-prefixed tests shows `Execute`,
  `executeFireAndForget`, `prepareAndSpawn`, `spawnOutput`, and
  `assertSpawnBranchPorts` at 100% statement coverage (the wait-mode branch
  of `Execute` is covered by area 17/18's own `TestSpawnWait_*` suite, not
  this scope); every design-phase-3 testing-strategy item (happy path, owner
  inheritance + departments, priority inheritance, guidance read+substitute,
  no-child-on-preparation-failure, invisible/missing child workflow,
  identity-resolution failure with no fallback, instance-load failure, enqueue
  failure surfaced, zero-declared-inputs edge case, unrecognized-snapshot-mode
  edge case, department-less owner, retry-creates-second-child, and
  grandchild-links-to-immediate-parent) is present and passing in
  `executor_spawn_test.go`; the executor is constructed only at the real
  composition root (`cmd/arbitron/main.go:894-903`) with all eight
  collaborators real, non-nil, non-hardcoded values; and `ownerFromContext`'s
  use for the parent's OWN guidance-template read (as opposed to the
  reconstructed owner identity used for `ChildInputs`/`Spawn`) is the correct,
  pre-existing pattern shared by every other file-reading executor
  (`executor_llm.go`, `executor_agent.go`, `executor_interactive.go`), not a
  gap. This closes review 2 approval for area 15; both approvals for area 15
  are now recorded.

- [x] **`fakeChildSpawner.ChildInputs` ignored `ctx`, so no test proved the
  executor passes the reconstructed OWNER identity — not the ambient caller
  identity — into `ChildInputs`.** Focus area 15 review 1.
  `domain/engine/executor_spawn.go:219-223` (`prepareAndSpawn`) builds
  `childCtx := identity.WithIdentity(ctx, ownerIdentity)` and calls
  `e.spawner.ChildInputs(childCtx, ...)` — and production's
  `Engine.ChildInputs` (`domain/engine/engine_spawn.go:24-37`) genuinely
  depends on that context: it derives the owner via `triggerOwner(ctx, ...)`
  and gates the department-visibility read via
  `e.sharedVisibilityFilter(ctx)`, exactly like the trigger path. But
  `executor_spawn_test.go`'s `fakeChildSpawner.ChildInputs` had signature
  `ChildInputs(_ context.Context, workflowName string)` — the identity
  parameter was discarded outright — so a regression that passed the raw
  incoming `ctx` (the caller's own identity, e.g. an intruder admin triggering
  the parent) instead of `childCtx` to `spawner.ChildInputs` would have shipped
  undetected: `TestSpawnExecutor_ChildOwnerIsParentOwnerNotAmbientIdentity`
  only inspects the identity that reaches `Spawn`, never the one that reaches
  `ChildInputs`, and no other test in the suite calls `ChildInputs` through the
  executor with a distinguishable ambient identity. The consequence of such a
  regression: a spawn node could silently read a DIFFERENT owner's
  department-scoped child-workflow visibility than the one it will actually
  spawn under, an existence-leak/authorization-narrowing bug in the same shape
  already fixed repeatedly in this loop (areas 12, 13, 14) — a shared contract
  proven at one call site (`Spawn`) and silently assumed at a sibling one
  (`ChildInputs`) despite both being fed by the SAME `childCtx` value one line
  apart. Extended `fakeChildSpawner` with an `inputsIdentity
  []identity.Identity` field, recording `identity.FromContext(ctx)` on every
  `ChildInputs` call (`domain/engine/executor_spawn_test.go`), and added
  `TestSpawnExecutor_ChildInputsCallCarriesOwnerIdentityNotAmbient`, which runs
  the executor with an intruder identity in the ambient context and asserts
  `ChildInputs` was called with the PARENT OWNER's identity, not the
  intruder's; confirmed it fails for the right reason (asserts the intruder's
  id where the owner's was expected) when `childCtx` is temporarily replaced
  with the raw `ctx` at the `ChildInputs` call site, then confirmed it passes
  against the real code. Reviewed the rest of area 15's checklist
  independently: `Execute`'s mode re-check against the snapshot is fully
  covered (`TestSpawnExecutor_UnrecognizedSnapshotMode_Fails`,
  `_EmptySnapshotMode_Fails`); `prepareAndSpawn`'s ordering (parent load →
  identity → child inputs → guidance read+`variable.Resolve` → prepare →
  spawn) is enforced by Go's own sequential control flow and pinned end-to-end
  by the existing failure/error-category tests (a failure at any stage leaves
  `spawner.spawned` empty); `go tool cover` shows
  `executeFireAndForget`/`prepareAndSpawn`/`spawnOutput` at 100% statement
  coverage; and the executor is constructed only at the real composition root
  (`cmd/arbitron/main.go:894-903`) with no `nil`/hardcoded collaborator. This
  closes review 1 approval for area 15; review 2 remains open.

- [x] **`sharedVisibilityFilter`'s error-propagation branch was only pinned
  through `Trigger`, not through `ChildInputs`.** Focus area 14 review 2.
  `sharedVisibilityFilter`'s docstring and review 1's fix establish that
  `Trigger`/`TriggerWithDepartment` and `ChildInputs` must share the identical
  gate so "a spawn node can never reach a workflow the same caller could not
  trigger directly" — and area 13's prior passes already pinned the
  not-found/CanSeePublication branches for `ChildInputs` via
  `TestEngineChildInputs_DepartmentWorkflow_HiddenFromOutsider`/
  `_AllowedForMember`. But the `PublicationOf` **error**-propagation branch
  (`domain/engine/engine.go:290-292`, `return false, err`) was pinned only via
  `TestEngine_Trigger_PublicationLookupError_FailsClosed`, which drives it
  through `Trigger`. `ChildInputs` calls its OWN `e.sharedVisibilityFilter(ctx)`
  at `domain/engine/engine_spawn.go:32` — same shared logic, a genuinely
  distinct call site — and this is exactly the shape of gap already fixed
  repeatedly in this loop (areas 12, 13): a shared helper's behavior proven at
  one call site and silently assumed at a sibling one. A regression that
  swallowed the `PublicationOf` error only along the `ChildInputs` path (e.g. a
  future refactor that built ChildInputs its own ad hoc filter instead of
  reusing `sharedVisibilityFilter`, or dropped the error check on just that
  call site) would let a spawn node's child-workflow lookup silently treat a
  lookup outage as "unpublished ⇒ open" instead of failing closed — undetected
  by any existing test, since none drove a `PublicationOf` error through
  `ChildInputs`. Added `TestEngineChildInputs_PublicationLookupError_FailsClosed`
  (`domain/engine/engine_spawn_test.go`), mirroring
  `TestEngine_Trigger_PublicationLookupError_FailsClosed`'s fixture shape but
  calling `ChildInputs` instead of `Trigger`; confirmed it fails for the right
  reason (no error returned) when the error branch is temporarily changed to
  `return true, nil`, then confirmed it passes against the real code. Also
  independently re-verified the rest of area 14's checklist by reading
  `TriggerWithDepartment` end to end again: `WithSpawnLink` has exactly one
  production caller (`Engine.Spawn`, `domain/engine/engine_spawn.go:63`), which
  rejects an invalid link BEFORE ever writing it to context, so the
  incomplete-link-ignored branch inside `TriggerWithDepartment` is a genuine
  defensive guard (tested via `TestTrigger_PartialSpawnLinkIsNotStamped`) rather
  than a load-bearing production path; confirmed via `go tool cover` that the
  entire link-stamping block (`engine.go:448-457`) is fully exercised; traced
  that the run-loop context (`runCtx` in `Engine.run`, derived fresh from
  `context.Background()`) never carries a parent's `Origin` or `SpawnLink`
  forward into a child's own node executions, so nested spawns cannot leak a
  grandparent's link (confirmed by `TestEngineSpawn_GrandchildLinksToItsOwnParent`
  plus direct code reading); and confirmed the single struct literal claim
  still holds (`inst := &instance.Instance{...}` then one `Create` call, no
  interim mutation). This closes review 2 approval for area 14; both approvals
  for area 14 are now recorded.

- [x] **`sharedVisibilityFilter`'s own error and not-found branches were never
  exercised through the real closure.** Focus area 14 review 1.
  `domain/engine/engine.go:282-298` is a pure code-move extraction of logic
  that predates this feature (confirmed by diffing against
  `64768283d2c32b64023801103c7aab2f5ea538d3:domain/engine/engine.go`, which
  has the identical inline closure inside `TriggerWithDepartment`) — but the
  extraction now makes this closure shared by `ChildInputs`, a NEW caller this
  feature adds. Confirmed via `go test -coverprofile` over the whole
  `domain/engine` suite that two of its three branches had 0 hits: the
  `pubs.PublicationOf` error path (`return false, err`) and the not-found path
  (`return true, nil // unpublished shared folder ⇒ open`) — every existing
  test that exercises gating at all (`TestEngine_Trigger_DepartmentWorkflow_*`)
  registers a `Publication` for the workflow name it triggers, so only the
  `CanSeePublication` branch was ever reached. `workflow.LoadVisibleFiltered`'s
  own suite tests a filter-returns-error contract generically with a fake
  filter function, which is a different thing from the engine's actual
  `PublicationOf`-backed closure ever being driven down either path. Traced the
  consequence of each: `LoadVisibleFiltered` (`domain/workflow/resolve.go`)
  propagates a filter error immediately (fail-closed, correct) and treats a
  `false` result as "absent, try next scope" (never leaks existence) — so
  today's behavior is correct, but a regression that swallowed the
  `PublicationOf` error (fail-open) or inverted the not-found branch (hiding
  every unpublished shared workflow, including newly-spawnable children) would
  have shipped undetected on both the trigger AND the spawn path. Extended
  `fakePubLookup` (`domain/engine/engine_trigger_visibility_test.go`) with an
  `errs map[string]error` field, and added
  `TestEngine_Trigger_UnpublishedSharedWorkflow_VisibleWithIdentity` (a shared
  workflow with a configured publisher lookup but no publication row must
  remain visible) and `TestEngine_Trigger_PublicationLookupError_FailsClosed`
  (a lookup error must propagate, not be swallowed); confirmed each fails for
  the right reason when its branch is temporarily neutered (not-found forced
  to `false`; error forced to `true, nil`), then confirmed both pass against
  the real code. Also reviewed the rest of area 14's checklist by reading
  `TriggerWithDepartment` end to end: the spawn link is read from
  `instance.SpawnLinkFromContext` and stamped into the SAME `Instance` struct
  literal as `TriggerSource`/`ScheduleID` (lines 441-492, one literal, no
  follow-up write — confirmed by `TestTrigger_StampsSpawnLinkAtConstruction`
  and `TestTrigger_SpawnLinkAndOriginCoexist`); an incomplete link
  (`SpawnLink.Valid()` false) is ignored wholesale, never partially persisted
  (`TestTrigger_PartialSpawnLinkIsNotStamped`); and the four pre-existing
  `TestEngine_Trigger_DepartmentWorkflow_*`/`_SystemTrigger_*` tests already
  exercise the extracted helper through its one pre-existing caller
  (`TriggerWithDepartment`), while area 13's prior pass added the equivalent
  coverage for the new caller (`ChildInputs`) reusing the same scope-aware
  fixtures — so the "unchanged for existing callers" claim is now backed by
  passing tests on both callers, not just a code-identity argument. This
  closes review 1 approval for area 14; review 2 remains open.

- [x] **`Engine.ChildInputs`'s "not-found on invisible" claim was only tested
  against a scope-agnostic mock.** Focus area 13 review 2.
  `domain/engine/engine_spawn.go:32` calls `workflow.LoadVisibleFiltered` with
  `e.sharedVisibilityFilter(ctx)` — the exact same department-visibility gate
  `TriggerWithDepartment` uses, documented as shared so "a spawn node can never
  reach a workflow the same caller could not trigger directly." But the only
  existing coverage, `TestEngineChildInputs_ScopedToContextIdentity`
  (`domain/engine/engine_spawn_test.go`), explicitly notes in its own comment
  that "the mock workflow repo is scope-agnostic, so this asserts the CALL
  SHAPE" — it cannot fail if the gate were dropped or bypassed, since the mock
  `Load` ignores the `Scope` argument entirely and a private-scope hit always
  succeeds before shared scope (and its filter) is ever consulted. This is the
  same shape of gap already fixed three times in this loop (area 12's
  Subject/Email derefs, area 9's fail-closed carve-out, area 8's zero-scope
  branch): a shared helper's behavior is proven at one call site
  (`TestEngine_Trigger_DepartmentWorkflow_HiddenFromOutsider`,
  `domain/engine/engine_trigger_visibility_test.go`) and silently assumed at a
  sibling one. A regression that passed `nil` instead of
  `e.sharedVisibilityFilter(ctx)` into `ChildInputs`'s `LoadVisibleFiltered`
  call would have let a spawn node resolve a department-restricted child
  workflow from outside that department — undetected by any existing test.
  Added `TestEngineChildInputs_DepartmentWorkflow_HiddenFromOutsider` and
  `TestEngineChildInputs_DepartmentWorkflow_AllowedForMember`
  (`domain/engine/engine_spawn_test.go`), reusing the existing scope-aware
  `newTriggerHarness`/`fakePubLookup`/`ctxWithDept` fixtures from
  `engine_trigger_scope_test.go`/`engine_trigger_visibility_test.go` — an
  outsider gets `workflow.ErrNotFound` on a department-gated shared workflow,
  a department member does not. Confirmed both fail for the right reason (no
  error returned) when the filter argument is temporarily replaced with `nil`
  in `engine_spawn.go`, then confirmed both pass against the real code.
  Reviewed the rest of area 13 independently: `Engine.Spawn`'s link-validity
  guard and thin-delegation shape are unchanged since review 1 and still
  covered (`TestEngineSpawn_RejectsInvalidLinkBeforeCreating` and siblings);
  `ChildSpawner`/`SpawnRequest` are still constructed only at the composition
  root (`cmd/arbitron/main.go:894`, passing the concrete `*Engine` to
  `NewSpawnWorkflowExecutor`); and the five-category bar (happy, success,
  failure, error, edge) is now met for both methods with a genuine
  invisibility case rather than a call-shape stand-in. This closes review 2
  approval for area 13; both approvals for area 13 are now recorded.

- [x] **`Engine.ChildInputs`'s default-user-unavailable fail-closed path was
  untested.** Focus area 13 review 1. `domain/engine/engine_spawn.go:28-31`
  calls the same `triggerOwner` helper `TriggerWithDepartment` uses, and
  correctly returns an error rather than proceeding with a nil owner when a
  system caller (no context identity) hits an unresolvable default user — but
  this is `ChildInputs`'s OWN call to `triggerOwner`, and `go tool cover`
  showed its `!ok` branch (lines 29-31) at 0% while the identical branch in
  `Trigger`/`TriggerWithDepartment` is pinned by
  `TestTrigger_DefaultUserUnavailable_FailsClosed`
  (`engine_trigger_scope_test.go:183`) — the same shape of gap already fixed
  twice in this loop (area 12's Subject/Email derefs, area 8's zero-scope
  branch): a shared helper's behavior is proven at one call site and silently
  assumed at a sibling one. A regression that dropped or inverted `ChildInputs`'s
  own `!ok` check would have let a nil-owner query fall through to
  `workflow.LoadVisibleFiltered` with `uuid.Nil`, silently reading the "private
  to nobody" scope instead of failing the node — undetected by any existing
  test, since the only production caller (`prepareAndSpawn`) always injects a
  resolved identity before calling, so this is a latent defensive-branch gap
  today, not a live bug, but the fail-closed contract is `ChildInputs`'s own
  and deserves its own pin independent of `Trigger`'s. Added
  `TestEngineChildInputs_DefaultUserUnavailable_FailsClosed`
  (`domain/engine/engine_spawn_test.go`), which sets `f.userRepo.defaultUserErr`
  and calls `ChildInputs` with a bare `context.Background()`, asserting the
  "failed to resolve owner" error; confirmed it fails for the right reason (no
  error returned) when the `!ok` check is temporarily replaced with a
  discarded `_`, then confirmed it passes against the real code. Also reviewed
  the rest of area 13 against the wired-and-fed criteria: `ChildSpawner` and
  `SpawnRequest` are constructed only at the composition root
  (`cmd/arbitron/main.go:894-903`, `eng` — the concrete `*Engine` — passed to
  `NewSpawnWorkflowExecutor`), `Spawn`'s link-validity-before-creation guard
  and thin-delegation shape are unchanged and already covered
  (`TestEngineSpawn_RejectsInvalidLinkBeforeCreating` and siblings), and the
  five-category bar (happy, success, failure, error, edge) is now met for both
  methods — `ChildInputs`'s "not-found on invisible" half of the checklist
  inherits `workflow.LoadVisibleFiltered`'s own pre-existing coverage in
  `domain/workflow/resolve_test.go` (confirmed via `git show` against the
  branch point that `LoadVisibleFiltered` predates this feature, so its own
  test suite is not this feature's job to extend). The remaining uncovered
  line (`if ctx == nil` in both `Spawn` and `ChildInputs`) mirrors an existing,
  never-tested defensive pattern already present in `Trigger`/
  `TriggerWithDepartment` pre-dating this feature — not a gap this feature
  introduced. This closes review 1 approval for area 13; review 2 remains
  open.

- [x] **`userIdentityResolver.IdentityForUser`'s Subject/Email → `LedDepartments`
  plumbing was untested.** Focus area 12 review 2.
  `cmd/arbitron/main.go:1384-1391` duplicates `AuthMiddleware.enrich`'s
  Subject/Email pointer-deref extraction (`adapter/http/auth_middleware.go:315-322`)
  rather than sharing it — the type's own docstring calls this out as
  deliberate, so "a scheduled fire, a spawned child run, and the same user's
  interactive request all resolve to identical identities" — but every
  existing `userIdentityResolver` test (`TestUserIdentityResolver_LocalDefaultUser`,
  `_CarriesStoredDepartments`, `_UnknownUserErrors`,
  `_ListDepartmentsErrorPropagates`) leaves `Subject`/`Email` nil on the stub
  user, so the two `if u.Subject != nil` / `if u.Email != nil` branches were
  only ever exercised on their false arm. The request-path copy of this exact
  logic is thoroughly pinned (`TestAuthMiddleware_Enrich_SubjectLead_Match` and
  siblings in `adapter/http/auth_middleware_test.go`), but the resolver's own
  copy had no equivalent — a regression that dropped or swapped the two
  derefs in `main.go` (breaking the documented cross-path identity-parity
  guarantee for every scheduled fire and spawned child) would have shipped
  undetected. Added
  `TestUserIdentityResolver_SubjectAndEmailDriveLedDepartments`
  (`cmd/arbitron/spawn_wiring_test.go`), which sets a matching `Subject`/`Email`
  against a `GroupConfig.Leads` entry and asserts `LedDepartments` comes back
  populated; confirmed it fails for the right reason (empty `LedDepartments`)
  when the two derefs are temporarily blanked out in `main.go`, then confirmed
  it passes against the real code. Also reviewed the rest of area 12's
  checklist independently: the twin-port relationship still holds (the
  compile-time assertion plus the real call sites at
  `schedule.NewEvaluator`/`schedule.NewRunner` in `cmd/arbitron/main.go:907-926`
  both require `userIdentityResolver` to satisfy `schedule.IdentityResolver`,
  which only compiles if the twin stays structurally identical), the
  schedule→neutral rename and the single shared composition-root value
  (`cmd/arbitron/main.go:880`) still hold, and `groups` (`groupCfg`) is fed
  from real parsed config (`cmd/arbitron/main.go:354`), the same value passed
  to both auth middlewares — not a hardcoded or test-only value. This closes
  review 2 approval for area 12; both approvals for area 12 are now recorded.

- [x] **`userIdentityResolver.IdentityForUser`'s `ListDepartments` failure path
  was untested.** Focus area 12 review 1. `cmd/arbitron/main.go:1375-1383`
  calls `r.users.GetByID` then `r.users.ListDepartments`, propagating an error
  from either — but `cmd/arbitron/spawn_wiring_test.go`'s `stubUserRepo.
  ListDepartments` had no way to return an error, so only the `GetByID` failure
  path (`TestUserIdentityResolver_UnknownUserErrors`) was exercised; a
  regression that silently swallowed a `ListDepartments` error (e.g. `depts,
  _ := ...`) and built a departmentless identity from a real user's DB row
  would have shipped undetected — the scheduler and spawn executor (the two
  production callers, `domain/schedule/runner.go:79` and
  `domain/engine/executor_spawn.go:215`) both already propagate whatever error
  the resolver returns, so this was purely a gap in the resolver's own test
  coverage, not a live bug. Also confirmed the other three parts of area 12's
  checklist already hold: `IdentityResolver` is a structurally identical twin
  of `schedule.IdentityResolver` (same method signature, redeclared per the
  codebase's established cross-domain-edge practice); the type was already
  renamed from schedule-specific to `userIdentityResolver` and ONE value is
  constructed at `cmd/arbitron/main.go:880` and passed to the spawn executor
  (`:900`), the schedule evaluator (`:911`), and the schedule run-now runner
  (`:924`); and `TestUserIdentityResolver_LocalDefaultUser` already pins Phase
  3's local-mode open question (`docs/designs/...-phase-3.md:214-217`) — the
  local-mode default user resolves cleanly through the same `BuildIdentity`
  the request path's `AuthMiddleware.enrich` uses, without needing a distinct
  resolution path. Added
  `TestUserIdentityResolver_ListDepartmentsErrorPropagates`
  (`cmd/arbitron/spawn_wiring_test.go`), extending `stubUserRepo` with a
  `deptErr` field; confirmed it fails for the right reason (no error returned)
  when the `ListDepartments` error check is temporarily replaced with `depts,
  _ := ...`, then confirmed it passes against the real code. This closes
  review 1 approval for area 12; review 2 remains open.

- [x] **A present-but-null OPTIONAL child input was untested (distinct from the
  already-tested absent case).** Focus area 11 review 2.
  `domain/engine/child_input_contract.go`'s docstring enumerates exactly three
  things `validateChildInputs` rejects — nil map, missing required, present-but-
  null REQUIRED — implying a present-but-null OPTIONAL input is accepted and
  passed through unchanged, but only the absent-optional case had a test
  (`TestValidateChildInputs_AbsentOptionalIsFine`); the present-null-optional
  shape (the model explicitly emitting `"dryRun": null` rather than omitting
  the key) was unexercised. Traced the downstream consequence in
  `Engine.TriggerWithDepartment` (`domain/engine/engine.go:377-393`): a
  present-nil variable and an absent variable take the identical branch
  (`!provided || val == nil`), so the child's own `Default` applies either way
  — the two code paths (validateChildInputs's accept-and-pass-through, then
  TriggerWithDepartment's present-nil-equals-absent) combine correctly today,
  but nothing pinned it. Added
  `TestValidateChildInputs_NullOptionalPassesThroughUnchanged`
  (`domain/engine/child_input_contract_test.go`), asserting an optional input
  present with an explicit `nil` satisfies the contract and the key survives
  unchanged in the produced map; confirmed it fails for the right reason
  (reports `dryRun` as a missing required input) when the `!f.Required`
  short-circuit is temporarily removed from the required-fields loop, then
  confirmed it passes against the real code. Reviewed the rest of area 11
  against the wired-and-fed criteria: `childInputSchema`/`validateChildInputs`
  are still called only from `Prepare` (confirmed unchanged since review 1),
  `childInputPrompt`'s stability and `structuredOutputInstruction`'s JSON
  marshaling (which sorts map keys, so the schema's property ordering was
  never actually an instability risk despite living in a `map[string]any`) were
  already covered by area 10's passes, and the five-category bar (happy,
  success, failure, error, edge) was otherwise already met by the existing
  suite. This closes review 2 approval for area 11.

- [x] **`validateChildInputs`'s "no type coercion" invariant was untested.**
  Focus area 11 review 1. `domain/engine/child_input_contract.go:84-86`
  documents that a produced value is never type-checked or coerced against its
  declared `InputField.Type` — "the trigger path is the single place input
  semantics are enforced" — but traced `Engine.TriggerWithDepartment`
  (`domain/engine/engine.go:375-405`) and confirmed the trigger path *also*
  never checks `InputField.Type` against the supplied value; it only enforces
  presence (`Required`) and the reserved-name guard via `SetInput`. So a
  declared type is pure prompt guidance, never validated anywhere in the
  engine, for spawn-produced values or ordinary manual triggers alike — this is
  pre-existing behaviour the feature does not change, not a gap to fix. Also
  reviewed `childInputSchema`/`childInputPrompt`/`validateChildInputs` as a unit
  against the wired-and-fed criteria: all three are called only from `Prepare`
  (`domain/engine/child_input_preparer.go:54-55,89`), which area 10 already
  confirmed is constructed at the real composition root
  (`NewChildInputPreparer` in `cmd/arbitron/main.go`) and fed real
  `ChildInputRequest` values built from `node.Config`/`vars.Snapshot()` in
  `executor_spawn.go`, never hardcoded; and confirmed the five-category bar
  (happy, success, failure, error, edge) was already met by the existing
  `child_input_contract_test.go` suite except for this one invariant. Added
  `TestValidateChildInputs_TypeMismatchIsNeitherRejectedNorCoerced`
  (`domain/engine/child_input_contract_test.go`), which produces a required
  `number` input as a string and asserts both that the contract is satisfied
  and that the value is passed through unchanged — pinning the documented
  no-coercion behaviour so a future change cannot silently start rejecting or
  mutating type-mismatched values without this test failing first. This closes
  review 1 approval for area 11; review 2 remains open.

- [x] **`LLMChildInputPreparer.Prepare` never set `WorkDir` on its preparation
  `ChatRequest`.** Focus area 10 review 2. Compared `Prepare`
  (`domain/engine/child_input_preparer.go`) against its sibling
  `LLMCallExecutor.Execute` (`domain/engine/executor_llm.go`), which sets
  `WorkDir: workDirStr` via the existing `WorkDirFromVars(vars)` helper
  (`domain/engine/conventions.go`) — `Prepare`'s `ChatRequest` literal built the
  same kind of tool-suppressed reasoning call but omitted `WorkDir` entirely,
  so it always defaulted to `""`. Confirmed the production consequence by
  reading `adapter/claudecli/chat_adapter.go` and `adapter/claudecli/provider.go`:
  both `ChatAdapter.Chat` and `Provider.Chat` call `validateWorkDir(req.WorkDir)`
  before doing anything else and hard-fail with `ErrWorkDirRequired` on an
  empty `WorkDir` — a deliberate PR #8 security guard against a subprocess
  inheriting the engine process's own cwd (which holds `arbitron.db`,
  `config.yaml`, and other secrets) — and confirmed via
  `TestFactory_ChatProviderFor_EmptyIsClaudeCLI`
  (`adapter/provider/factory_test.go`) that claude-cli is the default backend
  when a profile sets no `Backend`. So every spawn node with a claude-cli-backed
  `model_profile` and at least one declared child input would fail at the
  preparation call in production, on every run, with no way to configure
  around it — a "wired-and-fed" gap (a production call site building a request
  literal with a load-bearing field left at its zero value) that no existing
  test caught, because `mockChatProvider` never inspects `WorkDir`. Fixed by
  threading the parent's workspace end to end: added `WorkDir string` to
  `ChildInputRequest` (`domain/engine/ports.go`), set `WorkDir: req.WorkDir` on
  the `ChatRequest` in `Prepare` (`domain/engine/child_input_preparer.go`), and
  populated it in `prepareAndSpawn` via the same `WorkDirFromVars(vars)` helper
  `LLMCallExecutor` already uses (`domain/engine/executor_spawn.go`) — no new
  functionality, only a missing wire-up between two already-existing pieces.
  Added `TestChildInputPreparer_WorkDirReachesChatRequest`
  (`domain/engine/child_input_preparer_test.go`), pinning that a `WorkDir` on
  the incoming request reaches the outgoing `ChatRequest`, and
  `TestSpawnExecutor_ParentWorkspaceReachesPreparerAsWorkDir`
  (`domain/engine/executor_spawn_test.go`), pinning that a workspace set via the
  reserved `workspace` variable on the executor's `VariableStore` reaches the
  preparer's `ChildInputRequest.WorkDir`; confirmed both fail (unknown field /
  undefined field) against the pre-fix code and pass against the fix. This
  closes review 2 approval for area 10.

- [x] **`childInputPrompt`'s sorted-key stability invariant was untested.**
  Focus area 10 review 1. `domain/engine/child_input_preparer.go:148-150`
  documents and implements a load-bearing invariant: both the declared-input
  listing and the parent-context listing must render in SORTED key order, "so
  the prompt is stable across runs" for provider-side caching and quiet trace
  diffs — but no test asserted the order, and Go's randomized map iteration
  means a regression that dropped the `sort.Strings`/`sort.Slice` calls would
  produce a merely *flaky* prompt rather than a reliably-failing test, so it
  could ship and pass CI by chance for a long time. Reviewed the full
  `LLMChildInputPreparer`/`Prepare` implementation against Design Phase 2's
  spec line by line (resolve → assertBindingKind → select → chat → parse
  ordering, `structuredOutputInstruction` reuse, zero-input short-circuit,
  `parseChildInputResponse`'s code-fence/prose/non-object handling,
  `boundedExcerpt`, the fail-closed posture-guard-before-selection ordering)
  and confirmed every item in the design's five-category testing strategy is
  already covered, all real production wiring is fed from
  `cmd/arbitron/main.go` (`providerFactory`, `profileResolver`, both real
  adapters) and `executor_spawn.go` (`ChildInputRequest` built from
  `node.Config`/`vars.Snapshot()`, never hardcoded), and every adapter treats
  `ChatRequest.MaxTokens == 0` as "provider default" rather than "zero
  tokens" — this was the one invariant in the design with zero test coverage.
  Added `TestChildInputPreparer_PromptFieldsAndContextAreSortedForStability`
  (`domain/engine/child_input_preparer_test.go`), which declares inputs and
  parent-context keys in deliberately unsorted order and asserts their
  appearance order in the rendered prompt is alphabetical; confirmed it fails
  for the right reason (declaration order, not sorted) when the `sort.Slice`
  call is temporarily removed, then confirmed it passes against the real
  code. This closes review 1 approval for area 10.

- [x] **`ChildInputRequest.ProfileAlias` may reach the resolver empty.**
  Focus area 9 review 1. `validateModelProfiles` deliberately allows a spawn
  node with **no** `model_profile` (`domain/workflow/validation.go:183-190`),
  on the grounds that a zero-input child never makes a preparation call. But
  `SpawnWorkflowExecutor.prepareAndSpawn` passes `node.Config.ModelProfile`
  unconditionally, and `LLMChildInputPreparer.Prepare` only skips the call
  when `len(req.Inputs) == 0`. Traced `provider.ProfileResolver.Resolve(ctx,
  "")` for both the production resolver (`adapter/provider/dynamic.go:73`)
  and its cache lookup (`adapter/provider/cache.go:92`): a miss on alias `""`
  behaves identically to a miss on any other unknown alias — `ProfileCache.get`
  is a plain map lookup keyed by `Profile.Alias`, and no code path ever
  registers a profile under the empty alias — so `Resolve(ctx, "")` returns
  `provider.ErrUnknownProfile` wrapped with `""`, not a silently-resolved
  default binding. The author-time carve-out does fail closed at run time, as
  the validation comment claims. Added
  `TestChildInputPreparer_EmptyProfileAliasWithDeclaredInputs_FailsClosed`
  (`domain/engine/child_input_preparer_errors_test.go`), which asserts a child
  that declares inputs on a node with an empty `ProfileAlias` gets
  `provider.ErrUnknownProfile` naming the node, the provider is never called,
  and no partial map is returned — pinning the fail-closed behaviour so a
  future resolver change cannot silently widen this into an unguarded path.
  This closes review 1 approval for area 9.

- [x] **`childVisibilityClause`'s `IncludePublic` disjunct was never exercised in
  isolation.** Focus area 8 review 2. `adapter/sqlite/instance_repo.go:568-570`
  adds `c.visibility = 'public'` to the disjunction when `vis.IncludePublic` is
  set, but the only existing test that sets `IncludePublic`
  (`TestChildCounts_ExcludesInvisibleChildren`) also sets an `OwnerID` that
  already admits the one counted child via the `c.owner_id = ?` arm — so the
  `IncludePublic` arm itself was never the reason a child was counted.
  Confirmed by hand: replacing `if vis.IncludePublic` with `if false &&
  vis.IncludePublic` left every test in `adapter/sqlite` green. A regression
  that dropped or broke the disjunct (the same shape as the already-fixed
  zero-scope branch) would have shipped silently, under-counting children and
  hiding a hierarchy expander for a caller who should see a public child.
  Added `TestChildCounts_IncludePublicAloneCountsPublicChildren`
  (`adapter/sqlite/instance_spawn_queries_test.go`), which creates a public
  child owned by a stranger with neither `OwnerID` nor `Departments` matching
  and asserts it is counted purely under `IncludePublic`; confirmed it fails
  for the right reason against the neutered branch, then passes against the
  real code. Also reviewed the other three link-addressed queries
  (`ListActiveChildren`, `FindChildBySpawnNode`, `ListWaitingOnTerminalChild`)
  and the `ParentScope` clause in `List` against the five-category bar (happy,
  success, failure, error, edge) — all four already have dedicated coverage
  for every state/mode/tie-break/storage-failure case, and all four have a
  confirmed production caller (`cancel_cascade.go`, `executor_spawn.go`,
  `enricher.go`, `spawn_reconciler.go`) fed real data, not a fake or
  hardcoded value; `ParentScope`/`ParentInstanceID` are fed from the shared
  `instance.ParseParentScope` decoder at both the REST and MCP call sites.
  This closes review 2 approval for area 8.

- [x] **`childVisibilityClause`'s zero-scope fail-closed branch was untested.**
  Focus area 8 review 1. `adapter/sqlite/instance_repo.go:579-581` documents and
  implements a load-bearing security guard: when a caller's `ChildVisibility` is
  not `Unconstrained` and carries no `OwnerID`, `Departments`, or
  `IncludePublic` (the zero value), the disjunction is empty and the clause
  must render `0=1` — count nothing — rather than falling through to an
  unfiltered (fail-open) count. No test ever constructed a totally zero-value
  `instance.ChildVisibility{}`: the existing invisible-children coverage
  (`TestChildCounts_AllChildrenInvisibleIsAbsent`,
  `TestChildCounts_ExcludesInvisibleChildren`) always sets `OwnerID`, which
  takes the `"c.owner_id = ?"` disjunct arm, never the empty-`ors` arm this
  gap is about. A regression flipping `0=1` to `1=1` (or dropping the guard)
  would have shipped silently and leaked child-existence counts to a caller
  with no authorized scope at all. Added
  `TestChildCounts_ZeroScopeVisibilityFailsClosed`
  (`adapter/sqlite/instance_spawn_queries_test.go`), which creates a *public*
  child (visible under `IncludePublic` or an owner match, neither set here)
  and asserts it is absent from the count under `instance.ChildVisibility{}`;
  confirmed it fails for the right reason when the branch is temporarily
  changed to `1=1`, then confirmed it passes against the real code. Also
  traced the only production caller (`InstanceEnricher.batchChildCounts`,
  `domain/engine/enricher.go:104-111`) and confirmed it already short-circuits
  before ever constructing a zero-scope `ChildVisibility` (no identity on the
  context skips the call entirely) — so this was a latent, not live, gap, but
  the fail-closed contract is documented as the query layer's own invariant
  and deserves its own coverage independent of today's one caller. This is
  the "edge"/security-invariant category area 8 was missing — happy, success,
  failure, and error were already covered for all four link-addressed queries
  and the `ParentScope` clause via the existing extensive suite
  (`instance_spawn_queries_test.go`, `instance_spawn_wait_test.go`,
  `instance_repo_parent_scope_test.go`). Closes review 1 approval for area 8.

- [x] **The generated-query staleness bug that hit `trigger_source`/`schedule_id`/
  `workflow_id` recurred for migration 018's spawn-link columns.** Focus area 7
  review 2. This exact codebase already has a documented failure mode
  (`adapter/sqlite/instance_repo_stale_queries_test.go`'s pre-existing
  provenance tests): four hand-frozen `sqlc`-generated query functions in
  `adapter/sqlite/generated/*.sql.go` (`ListRunningInstances`, `NextEligible`,
  `ListWaitingWithTimer`, `ListWaitingWithWebhook`) each embed a literal SELECT
  column list and a matching `Scan(...)` call, and neither is regenerated in
  this environment (no `sqlc` binary — `which sqlc` exits 127) when a
  migration adds columns. Migration `018_instance_spawn_link` added
  `parent_instance_id`/`parent_node_id`/`spawn_mode`, but none of the four
  queries' SELECT lists or Scan calls were updated for them, and
  `generated.Instance` (`adapter/sqlite/generated/models.go`) had no fields to
  hold them either — so every instance returned by these four paths silently
  reported a zero-value spawn link (`IsRoot() == true`, `SpawnMode == ""`)
  regardless of what was actually stored, the identical silent-drop shape
  already characterized for the three provenance fields. Traced all four call
  sites (`Engine.RecoverCrashedInstances`, `Manager.Evaluate` via
  `queueRepo.NextEligible`, `queue.RecoverTimers`/`RecoverWebhooks`) and
  confirmed none has a LIVE bug today — each either re-fetches the instance
  fresh via `Get` before touching spawn-link fields, or only reads fields the
  missing columns don't affect — but left uncorrected this is a landmine for
  the very next caller that reads `.ParentInstanceID`/`.SpawnMode` off one of
  these four result sets. Fixed by adding the three `sql.NullString` fields to
  `generated.Instance`, appending `parent_instance_id, parent_node_id,
  spawn_mode` to the SELECT column list and `&i.ParentInstanceID,
  &i.ParentNodeID, &i.SpawnMode` to the Scan call in all four generated
  functions, and populating the domain conversion in `rowToInstance`
  (`adapter/sqlite/instance_repo.go`) so any caller of the shared helper gets
  the correct value rather than silently dropping it. Added four failing-first
  characterization tests in `adapter/sqlite/instance_repo_stale_queries_test.go`
  (`TestListRunning_PreservesSpawnLink`, `TestNextEligible_PreservesSpawnLink`,
  `TestListWaitingWithTimer_PreservesSpawnLink`,
  `TestListWaitingWithWebhook_PreservesSpawnLink`), each creating a real
  parent+child pair via the production `InstanceRepo`/`QueueRepo` and driving
  the real repo method, confirmed all four failed for the right reason before
  the fix and passed after, with no regression in the pre-existing provenance
  or spawn-link suites. This is the "error"/latent-defect category the area
  was missing (the round-trip, partial-link, root, and pre-existing-row cases
  were already covered by review 1) — closes review 2 approval for area 7.

- [x] **The partial-link guard in `InstanceRepo.Create` was undocumented by any
  test.** Focus area 7 review 1. `adapter/sqlite/instance_repo.go:80-88`
  documents and implements a load-bearing guard — a caller supplying only SOME
  of `ParentInstanceID`/`ParentNodeID`/`SpawnMode` must have all three written
  as NULL, never a half-written link with a dangling `parent_instance_id` and
  no `parent_node_id` (which the wake and resumption short-circuit could never
  match, stranding the parent forever) — but no test exercised any of the three
  partial-input shapes. Added
  `TestInstanceRepo_PartialSpawnLinkNeverWritten`
  (`adapter/sqlite/instance_spawn_link_test.go`), table-driven over "parent id
  without node id", "node id without parent id", and "parent+node id without a
  valid mode"; confirmed by hand that removing the guard (writing each
  NullString unconditionally) makes two of the three subtests fail, proving the
  test is load-bearing rather than vacuous. This is the "failure" category the
  area was missing — happy/success/error/edge were already covered by the
  round-trip, same-statement, not-found, and root/pre-existing-row tests. This
  closes review 1 approval for area 7.

- [x] **`ListWaitingOnTerminalChild`'s retry-produces-duplicate-candidates case
  was untested.** Focus area 5 review 2. The reconciler's query JOINs every
  terminal child of a waiting parent's current node
  (`adapter/sqlite/instance_repo.go:725-733`), with no analogous tie-break to
  `FindChildBySpawnNode`'s `ORDER BY ... LIMIT 1` — by design, since it answers
  "is there a terminal child" rather than "which child answers", but that
  means a node-level retry that leaves more than one terminal child for the
  same (parent, node) key (the exact scenario the tie-break test above
  exercises) surfaces as TWO `SpawnWaitCandidate` rows naming the same parent,
  and no test verified either the SQL layer produced this shape or that
  `SpawnWaitReconciler.Reconcile` tolerates it. Confirmed by hand that it is
  harmless — the reconciler re-verifies `parent.State == waiting` against the
  live row before each `SetQueued`, so the second candidate for an
  already-requeued parent is skipped — but an untested "confirmed by hand" is
  exactly the gap this loop exists to close. Added
  `TestListWaitingOnTerminalChild_RetryLeavesOneRowPerTerminalChild`
  (`adapter/sqlite/instance_spawn_wait_test.go`) pinning the two-row shape, and
  `TestSpawnWaitReconciler_DuplicateCandidatesForSameParentAreHandledOnce`
  (`domain/engine/spawn_reconciler_test.go`) asserting the parent is queued
  and `NotifyCapacityFreed` fires exactly once despite the duplicate. Also
  reviewed and confirmed as correct-by-design (not a gap): `SpawnWaitCandidate.
  ChildState`, populated by the SQL adapter, has no production reader —
  `SpawnWaitReconciler.Reconcile` only uses it to identify *that* a child is
  terminal (already filtered by the WHERE clause), never *how*, because the
  routing decision is deliberately deferred to `resumeFromLink`'s fresher
  `FindChildBySpawnNode` read at resumption. This closes the "edge" category
  gap for area 5 — review 2 approval.

- [x] **`FindChildBySpawnNode`'s newest-child tie-break was untested.**
  Focus area 5 review 1. `adapter/sqlite/instance_repo.go:690-697` documents
  `ORDER BY created_at DESC, rowid DESC LIMIT 1` as deliberate: a node-level
  retry can leave more than one child row for the same (parent, node, mode)
  key, and the newest must answer. But `created_at` is `strftime('%Y-%m-%dT
  %H:%M:%SZ')` — second resolution (migration `001_initial_schema.up.sql:8`)
  — so two children created back-to-back in a retry (or in any fast test)
  land on an identical timestamp; `rowid DESC` was the ONLY thing breaking the
  tie, and no test exercised the multiple-children case at all. A regression
  that dropped or reversed the tie-break would have shipped silently and made
  the executor's resumption short-circuit (`FindChildBySpawnNode` is consulted
  by area 18) key on the wrong child. Added
  `TestFindChildBySpawnNode_MultipleChildrenReturnsTheNewest`
  (`adapter/sqlite/instance_spawn_wait_test.go`), which creates a stale child
  then a newest child under the same key and asserts the newest one answers;
  confirmed it fails (returns the stale child) when the ordering is reversed
  to `ASC, rowid ASC`, then confirmed the real ordering passes it. This closes
  the "edge" category gap for area 5 — review 1 approval.

- [x] **`assertBindingKind`'s unmapped-type no-op was only verified indirectly.**
  Focus area 3 requires verifying that an UNMAPPED node type really would no-op
  `assertBindingKind`, but the existing coverage
  (`TestExpectedKindForNodeType_OtherMappingsUnchanged`) only called
  `expectedKindForNodeType` directly, never `assertBindingKind` itself, so a
  future change that made the wrapper ignore `ok` would have gone undetected.
  Added `TestAssertBindingKind_UnmappedNodeType_NoOp`
  (`domain/engine/kind_guard_spawn_test.go`), which calls `assertBindingKind`
  with an unmapped type (`workflow.NodeTypeScript`) across chat, agent, and a
  bogus `ProfileKind`, asserting `nil` every time — this is the review-2
  approval for area 3.

- [x] **`instance.Instance.IsRoot()` had no production caller.**
  `domain/instance/instance.go:159` documents itself as "the ONE place the null
  link means root convention is expressed, so read paths do not each re-derive
  it" — but no production code called it. `domain/engine/spawn_wake.go:43`
  (`Engine.OnChildTerminal`) independently re-derived the exact same convention
  with a two-field nil check (`child.ParentInstanceID == nil ||
  child.ParentNodeID == nil`) guarded by the comment "a root run has nobody to
  wake" — literally `IsRoot()`'s definition, inline. The two-field check is
  safe to collapse to `child.IsRoot()` because `ParentInstanceID` and
  `ParentNodeID` are never set independently: `engine.go`'s trigger-path
  stamping only writes either both or neither (gated on `SpawnLink.Valid()`),
  and `adapter/sqlite`'s create/scan path persists and reads the three link
  columns as a single NULL-together unit. Replaced the inline check with
  `child.IsRoot()`, giving the method its first production caller; existing
  coverage (`TestOnChildTerminal_RootRunIsANoOp`,
  `TestInstanceNullLinksAreRoots`, `TestInstanceWithLinkIsNotRoot`) still
  passes unchanged. The other two re-derivations named in the original
  finding — the `ParentScopeRoots` SQL clause and the dashboard's
  `topLevelRows` — are a different layer (a SQL predicate and a TypeScript
  predicate over a JSON DTO) that cannot call a Go method, so they are not
  candidates for this fix; `IsRoot()`'s scope is Go in-process read paths over
  an already-loaded `Instance`.

## Won't Fix

Out of scope for this feature. Recorded so the omission is a decision, not an
oversight. **None of these is an unfed production path** — each is either
explicitly a spec non-goal or pre-existing behaviour this change set did not
touch.

- **Recursion / runaway limits on spawning.** The spec lists recursion limits and
  loop prevention as explicit non-goals, and `AGENTS.md:85` records that the
  engine has no general runaway guard. A workflow that spawns itself grows
  instance count geometrically. Phase 3's design records this as a known, accepted
  risk. Out of scope.
- **Lifting the awaited-spawn loop-body restriction.** Phase 4's design states the
  short-circuit cannot distinguish re-entry-after-wait from a back-edge revisit,
  and explicitly does not attempt to lift the restriction — for either awaited
  spawn nodes or interactive nodes, which carry the same restriction for the same
  reason. Documented in `AGENTS.md` instead.
- **Duplicate child on fire-and-forget node retry.** A node-level retry of a
  fire-and-forget spawn re-invokes `Execute` and creates a second child. Phase 3
  records this as intended and pinned by test, since the spec states
  fire-and-forget child outcomes do not affect the parent. (The awaited path is
  idempotent via the short-circuit.)
- **Atomicity of the child's terminal write and the parent's wake.** Phase 4
  defers this as a durability refinement: `SpawnWaitReconciler` makes
  non-atomicity recoverable rather than fatal.
- **A child left running when the parent fails at the spawn node.** Deliberate:
  the child may already be executing side effects, and cancelling it from a
  failing parent would layer a second failure on the first. Consistent with the
  engine's existing choice to retain workspaces on failure.
- **Hierarchy on the single-instance detail view.** Phase 5 covers the list view
  only.
- **Editor-side UI for spawn nodes** (node palette, per-type port rendering,
  configuration form). `dashboard/src/graph` has no editor for any node type;
  `conditional` has had named ports far longer and still renders through
  `ArbitronNode`'s single source handle. Spawn nodes are authored in
  `workflow.json` and policed by Phase 1 validation.
- **Server-side depth limit on hierarchy expansion.** Expansion is user-driven,
  one level per request; depth costs requests, not an unbounded query.
- **Pre-existing baseline failure**
  `adapter/config.TestSampleFeaturePipeline_ValidatesWithDefaultProfiles`. Present
  at the branch point; not this feature's job. Must not block the loop, and must
  not be "fixed" by weakening the sample-pipeline validation this feature extends.
