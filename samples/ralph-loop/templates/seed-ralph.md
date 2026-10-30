# Mission: Seed the Ralph Review Plan

Working **unattended** in the worktree at `./work`, write four tracking files and
commit them. The user is unavailable — do not ask questions, make the calls yourself.

A separate **plan critic** on a different model family runs immediately after you.
It applies the merge test and the seam test below to your plan and merges or strikes
whatever fails them. Write the plan so that it has nothing to do.

## Inputs

- **Branch being refined:** `{{base_branch}}`  ·  work branch `{{branch}}`
- **Scope derivation method:** `{{derive-scope.scope_method}}` (against `{{derive-scope.default_branch}}`)
- **Files changed by this branch** ({{derive-scope.file_count}} total):
  {{derive-scope.changed_files}}
- **Commits on this branch:**
  {{derive-scope.commit_log}}
- **Extra scope note from the operator** (may be empty): {{derive-scope.scope_note}}
- **Test command(s)** — use these exact commands, do NOT invent one: `{{record-baseline.test_commands}}`
- **Branch-point test baseline** (failures that existed BEFORE this branch): `{{record-baseline.test_baseline}}`

> If `scope_method` is `two-dot` or `branch-only`, the file list may include work
> that is not this branch's. Say so in RALPH.md's scope section rather than
> silently reviewing someone else's changes.

## Files to write

| Path | What it is |
|---|---|
| `./work/RALPH.md` | the charter: goal, scope, test commands, definition of done, **and the loop protocol for all three roles** |
| `./work/docs/reference/focus-areas.md` | the **single-system** review plan: one table, then one detail block per area |
| `./work/docs/reference/paired-reviews.md` | the **paired integration** review plan: one row per seam, one approval per row |
| `./work/docs/reference/gaps-identified.md` | concrete known gaps, with a `Won't Fix` section |

Create `./work/docs/reference/` if it does not exist.

## What a review area IS

A single-system area is a **behaviour**: one thing this branch promises, traced
from the point where it starts to the point where its effect lands, across every
layer in between. Not a file, not a class, not a layer, not a package.

The wired-and-fed criteria that decide every review are path-tracing questions
(is it constructed at the composition root, is it fed real data, does the live
path reach the sink). A reviewer can only answer them by reading the whole path.
An area that is one layer of a path forces the reviewer to read the neighbouring
layers anyway, and the plan then charges again for each neighbour as its own area
and again for every seam between them. One earlier plan reviewed a single feature
as seven horizontal slices (handler, response contract, deck handler, repository,
value object, mapping, persistence) and needed 116 paired rows to stitch them back
together. Do not do that.

### How to find the behaviours

1. Read the commit log, the scope note, and any spec, design or plan documents the
   branch carries under `docs/`. Write down what the branch **promises**: each
   capability, guarantee, removal or migration it claims to deliver.
2. For each promise, find its **entry point** (a handler, a job, a CLI command, a
   boot step, a config key, a migration) and its **sink** (a table write, a queue
   publish, a response, a rendered artifact, a deleted row, a refused request).
   Read the code between them. That path is the candidate area.
3. Apply the **merge test** to every pair of candidates, then the **split test**
   to any candidate too large for one pass.

### The merge test — two candidates are ONE area when any of these holds

- Judging either one as wired-and-fed requires reading the other. A repository
  cannot be judged "fed" without the handler that feeds it; a gate cannot be judged
  "consulted" without the path that consults it.
- They would be reviewed by reading the same files.
- One of them has no entry point or no sink of its own. A DTO, a mapping, a value
  object, a migration, a constant table, a wiring file: these are hops on some
  behaviour's path, not behaviours.

A change too small to trace on its own — a one-line list edit, a fixture, a config
value, a flag default, a docs page — is **folded in** as a named check of the
behaviour it serves. It does not get an area, because an area costs two passes.

### The split test — split only by sub-path, never by layer

If a behaviour is too large for one reviewer to trace in one pass, split it by
**sub-path**: per image kind, per entry point, per tenant type, per lane. Never by
layer. "The render path for canonical images" and "the render path for chat
images" are two areas; "the render handler" and "the render repository" are not.

### Size

The bound is the merge test, not a number. Most branches yield 6 to 15 behaviours;
a very large branch may reach 20. Above 20 you are almost certainly slicing by
layer or by file: go back and merge. The previous plan for a 636-file branch had
52 areas; under this rule it has about 15, and each pass reviews a whole path
instead of a fragment.

## focus-areas.md — the single-system plan

### The table

Five columns, exactly. The router and the counter parse this table, so keep the
shape; the detail lives in the blocks below it.

```markdown
## Single-System Reviews

| # | Focus Area | Risk | Review 1 Status | Review 2 Status |
|---|---|---|---|---|
| 1 | <behaviour, named by what it does> | high | [ ] | [ ] |
```

Rows are ordered **highest risk first** (`high` / `med` / `low`), ranked on
consequence, not size: a silent data-corruption, auth-bypass or irreversible-deletion
path outranks a cosmetic one however much code it touches. The loop takes work in
this order and may be stopped by its cap, so risk order is what makes a truncated
run useful.

Name areas by behaviour, concretely: `existing user with incomplete preferences is
gated to setup and back`, `a manifest-named asset is on disk and verified before the
lane serves`. Never `backend`, `misc`, or a file name.

### The detail blocks

Under the table, one block per area, headed `### Area N — <name>`, carrying:

- **Promise:** one sentence: what the branch says this does.
- **Entry:** `file:symbol` where the behaviour starts.
- **Sink:** `file:symbol` where its effect lands.
- **Files:** every file on the path, changed files first, then unchanged files the
  path crosses. A reviewer must be able to load the whole area from this list.
- **Folded in:** each small change reviewed as part of this behaviour, with its
  file and what to check.
- **Review 1 — wiring lens:** 3 to 7 numbered checks that decide whether the path
  **exists and is fed**: constructed at the composition root at `file:symbol`; fed
  by the real producer at `file:symbol`, not `nil`/empty/hardcoded; the happy and
  success paths reach the sink; the test that demonstrates each.
- **Review 2 — failure lens:** 3 to 7 numbered checks that decide **how it fails**:
  nil and empty inputs, timeouts, partial failure and rollback, concurrency, the
  ordering that must hold, and for each the test that goes red if it is wrong.
- **Seed evidence:** the greps you ran now and what they found (`file:symbol`).

The two lenses are the reason an area has two approvals. Review 1 and review 2 run
on different model families and ask **different questions**; that is what makes
the second approval independent instead of a repeat of the first.

**Use numbered lists in the blocks, never checkbox syntax.** `[ ]` and `[x]` are
counted file-wide by the progress script; a checkbox in a detail block corrupts the
approval arithmetic. The verifier rejects a plan with a checkbox outside a table.

## paired-reviews.md — the seam plan

### The seam test — a paired row exists ONLY when all three hold

1. The two behaviours **share** persisted state, a wire contract, a queue or topic,
   a flag, or an **ordering constraint** (X must happen before Y).
2. The two sides were built by **different tasks, phases or commits**. Independent
   authorship is where integration breaks.
3. The failure would be **silent**: each side is green in isolation, and only the
   combination is wrong.

An internal hop inside one behaviour is **not** a seam; it belongs in that area's
checks. Two areas that merely call each other in the same package are not a seam.
A cross-cutting gate (a flag, an auth or consent check) gets a paired row with a
governed behaviour only where the governed side could run **without** the gate and
nothing would go red; otherwise "the path consults the gate at `file:symbol`" is a
wiring-lens check in the governed area.

### One row per seam, both directions, one approval

Each seam is **one row**, named `A ↔ B`, and its single approval covers both
directions: what A assumes about B, and what B assumes about A. A reversed title is
not another risk and does not earn another row. **Never emit reverse rows.** The
previous format listed every seam twice and 92 of a plan's rows were mirror pairs.

### The table

Five columns, exactly. No `|` characters inside a cell.

```markdown
## Paired Integration Reviews

| # | Paired Review | Seam | Risk | Status |
|---|---|---|---|---|
| 1 | <A> ↔ <B> | SHARED: <the state, contract, flag or ordering> · TRACE: <entry, through the seam, to the observable result> · BREAK: <the failure that must be shown impossible or caught> | high | [ ] |
```

The Seam column is a **trace recipe with a stated failure**, not a symbol name.
The reviewer traces the named path and demonstrates that BREAK cannot happen, by a
test that goes red or by a cited guard; the approver ticks on that evidence. A row
whose BREAK you cannot state is not a seam. Drop it.

Rows are ordered **highest risk first**.

### `## No seam`

End `paired-reviews.md` with a `## No seam` section listing every combination you
considered and did not pair, one line each with the reason, under two headings:
**Inside one behaviour** (the hop is a check in area N) and **Genuinely unrelated**
(no shared state, contract, flag or ordering). An omission that is written down is
a decision; an omission that is silent is a coverage gap nobody can audit. If the
budget forces you to drop a real seam, list it here as `DEFERRED (budget)` with its
SHARED and BREAK stated, so a later pass can promote it.

## Seeded focus items

**Seed file:** `{{focus_seed}}`. If that value is empty, skip this section: the run
has no seed. If it names a file, this section is mandatory.

A seed is a survivors file from an earlier hardening stage: test gaps a mutation
run proved real. Its items must not be lost in the plan.

1. Read `./work/{{focus_seed}}`. Each entry under `focus_items` has an `id`.
2. Make every id a named area, or name it inside an existing area. Either way,
   write its **exact id string** in `./work/docs/reference/focus-areas.md`. The
   verifier greps for each id verbatim and fails the plan if one is missing.
3. Use each item's `mutants` (its surviving mutants) as **Review-1 evidence** for
   the area that holds it, so the wiring review starts from a proven gap rather
   than rediscovering it.
4. Seeded areas count against the pass budget (the verifier's CHECK 2) like any
   other area. Merge seeded items into existing behaviours where the merge test
   allows it, rather than giving each its own area.

## The plan must fit the budget it will run under

**This is a hard constraint, not advice.** A paired row costs a whole pass: the
last plan's 96 rows took 66 passes, and batching happened in 7 of them. Singles
cost two passes each and cannot be batched. Require:

> `2 × singles + paired` **≤ `{{max_ralph_iterations}}` × 0.35**

The 0.35 is measured: passes are also spent closing gaps, fixing regressions and
re-reviewing, so the real cost is roughly 2.6 times the minimum. State the
arithmetic in RALPH.md: singles, paired, implied minimum, budget. If a plan built
under the merge test and the seam test still does not fit, defer the lowest-risk
seams first, then split nothing and merge more; never pad, never pair layers.

## What every review actually checks

The tables say *what* to review; these criteria decide whether a review passes.
The recurring failure is code that is present and green in isolation but **never
wired-and-fed** in production:

- For every new exported field / collaborator / config the branch added:
  `grep -rn <Symbol>` the whole repo — is it **constructed or populated outside
  `*_test.go`**? If it is only ever set in tests, the review fails.
- For every new component: is it assembled into the real composition root so the
  live path reaches it? Name the wiring site, or fail the review.
- Does any production call site pass `nil`/empty/hardcoded where a real producer
  should feed it?
- Do any tests pass only because a fake ignores a load-bearing argument, or
  because an "integration" test asserts an intermediate hop instead of the sink?
- Does the area have coverage across all five scenario categories (happy, success,
  failure, error, edge), and would at least one test **fail** if its production
  input were nil/empty?

Do the greps now and put real findings — `file:symbol` — into each area's
**Seed evidence** rather than writing generic reminders.

## RALPH.md — MUST contain this section VERBATIM

**Transcribe the block below exactly — do not paraphrase, summarise, renumber or
invent your own.** Substitute only the bracketed placeholder.

**All three role blocks are mandatory.** `verify-ralph-seed` greps each one
separately for its Wrap Up, its self-assessment and its promise tags, and FAILS
THE RUN if any role is missing them — a file can otherwise carry a perfect
protocol for one role and silently omit the stop rule for another, which
surfaces sixty passes later as a timeout. Without them the refinement agent has
no rule for what *ends* a pass: it will improvise a protocol that says what to do
and never says when to stop, sprawl across several review areas in one pass, and
leave the loop unable to tell progress from churn. It also carries the separation
between the agent that fixes and the agent that approves, which is what stops a
mistake from being certified by the same model that made it.

The blocks name roles and lenses, never models. Which model fills a role is the
workflow's business and changes without the plan changing.

---8<--- copy from here ---8<---

## Role: single-review-1

*The WIRING lens: does the path exist and is it fed? Refined and assessed by two
different model families. The first independent look at an area.*

### Constraints
- **Do NOT use sub-agents.** Work by hand, one component at a time.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **One single-system area per iteration.** Fix the most important issue, then stop.
- **Do NOT invent new functionality.** Log gaps in `docs/reference/gaps-identified.md`; out-of-scope items go under `Won't Fix`.
- **The refiner never ticks a box; the approver never writes production code.** They run on different model families on purpose.

### Iteration Structure
1. **Read tracking** — `RALPH.md`, the assigned row and its `### Area N` block in `docs/reference/focus-areas.md`, and the OPEN items in `docs/reference/gaps-identified.md` (grep for `[ ]`; do not read the closed history).
2. **Read the code** — every file in the area's **Files** list, entry to sink.
3. **Cross-reference** — work the area's **Review 1 — wiring lens** checks with real greps; record findings in `docs/reference/gaps-identified.md`.
4. **Fix the most important issue** — ONE issue, fixed well across all affected files.
5. **Write/verify tests** — happy, success, failure, error, edge. Strict TDD when adding behaviour.
6. **Run targeted tests** — the tests covering what you changed, using `[TEST COMMANDS]`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
7. **Assess, then proceed to Wrap Up regardless of the outcome.** Do not go back to step 4.

### Wrap Up
**Step A** — Does the area meet every wiring-lens check?

**Step B** — Record a recommendation; tick NOTHING. The approver ticks Review 1 Status in `docs/reference/focus-areas.md`.

**Step C** — Commit, and never push: the workflow pushes the pass once the tamper gate has ruled on it. Your work is valuable regardless of review status.

**Step D** — One line: `Progress: <what advanced>` — the fix you landed, the gap you closed, the component you wired, or explicitly what did not advance and why. The progress assessor has no tools and sees only your response.

**Step E** — Check the tracking files, then output exactly one tag:
- All reviews approved AND no open gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.**

## Role: single-review-2

*The FAILURE lens: how does it fail? Review 1 already passed on the wiring lens.
This pass asks a different question, on a different model family — that
difference is the entire reason two approvals exist.*

### Constraints
- **Do NOT use sub-agents.** Work by hand, one component at a time.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **One single-system area per iteration.** Fix the most important issue, then stop.
- **Do NOT invent new functionality.** Log gaps in `docs/reference/gaps-identified.md`; out-of-scope items go under `Won't Fix`.
- **The refiner never ticks a box; the approver never writes production code.** They run on different model families on purpose.

### Iteration Structure
1. **Read tracking** — `RALPH.md`, the assigned row and its `### Area N` block in `docs/reference/focus-areas.md`, and the OPEN items in `docs/reference/gaps-identified.md` (grep for `[ ]`; do not read the closed history).
2. **Read the code** — every file in the area's **Files** list, entry to sink.
3. **Cross-reference** — work the area's **Review 2 — failure lens** checks: nil and empty inputs, timeouts, partial failure and rollback, concurrency, ordering; record findings in `docs/reference/gaps-identified.md`.
4. **Fix the most important issue** — ONE issue, fixed well across all affected files.
5. **Write/verify tests** — happy, success, failure, error, edge. Strict TDD when adding behaviour.
6. **Run targeted tests** — the tests covering what you changed, using `[TEST COMMANDS]`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
7. **Assess, then proceed to Wrap Up regardless of the outcome.** Do not go back to step 4.

### Wrap Up
**Step A** — Does the area meet every failure-lens check?

**Step B** — Record a recommendation; tick NOTHING. The approver ticks Review 2 Status in `docs/reference/focus-areas.md`.

**Step C** — Commit, and never push: the workflow pushes the pass once the tamper gate has ruled on it. Your work is valuable regardless of review status.

**Step D** — One line: `Progress: <what advanced>` — the fix you landed, the gap you closed, the component you wired, or explicitly what did not advance and why. The progress assessor has no tools and sees only your response.

**Step E** — Check the tracking files, then output exactly one tag:
- All reviews approved AND no open gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.**

## Role: paired-review

*Phase 2: runs only once every single-system box is ticked. One row is one seam,
reviewed in BOTH directions in the same pass. Refined and assessed by two
different model families.*

### Constraints
- **Do NOT use sub-agents.** Work by hand, one component at a time.
- **One seam per iteration, both directions; batch further rows only when they share the context you have already loaded.** Fix the most important issue, then stop.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **Do NOT invent new functionality.** Log gaps in `docs/reference/gaps-identified.md`; out-of-scope items go under `Won't Fix`.
- **The refiner never ticks a box; the approver never writes production code.** They run on different model families on purpose.

### Iteration Structure
1. **Read tracking** — `RALPH.md`, the assigned row in `docs/reference/paired-reviews.md`, the `### Area` blocks of both behaviours it joins, and the OPEN items in `docs/reference/gaps-identified.md` (grep for `[ ]`).
2. **Read the code** — both sides of the seam and the seam itself, following the row's TRACE.
3. **Cross-reference** — demonstrate that the row's BREAK cannot happen: a test that goes red, or a cited guard at `file:symbol`; check what each side assumes about the other; record findings in `docs/reference/gaps-identified.md`.
4. **Fix the most important issue** — ONE issue, fixed well across all affected files.
5. **Write/verify tests** — happy, success, failure, error, edge. Strict TDD when adding behaviour.
6. **Run targeted tests** — the tests covering what you changed, using `[TEST COMMANDS]`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
7. **Assess, then proceed to Wrap Up regardless of the outcome.** Do not go back to step 4.

### Wrap Up
**Step A** — Is the seam sound in both directions, with its BREAK demonstrated impossible or caught?

**Step B** — Record a recommendation per row; tick NOTHING. The approver ticks each row it can verify in `docs/reference/paired-reviews.md`, each cited separately.

**Step C** — Commit, and never push: the workflow pushes the pass once the tamper gate has ruled on it. Your work is valuable regardless of review status.

**Step D** — One line: `Progress: <what advanced>` — the fix you landed, the gap you closed, the component you wired, or explicitly what did not advance and why. The progress assessor has no tools and sees only your response.

**Step E** — Check the tracking files, then output exactly one tag:
- All reviews approved AND no open gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.**

---8<--- copy to here ---8<---

Substitute `[TEST COMMANDS]` with the exact commands from the Inputs above.
Everything else is copied unchanged — the `<promise>` tags especially, because
the workflow verifies them and the assessor reads them.

## Definition of done — put this in RALPH.md too

The repo may already be red on arrival. Pre-existing failures are NOT this
branch's job and must NOT block the loop. "Done" means ALL of:

- Every single-system review has **2 approvals**, one per lens; every paired review has **1**, covering both directions.
- No unchecked items remain in `docs/reference/gaps-identified.md`.
- Every area is **wired-and-fed**, verified by grep rather than by a green isolated test.
- Every paired row's **BREAK** has been shown impossible or caught.
- **No NEW test failures** beyond the recorded baseline, and nothing that built at baseline is broken.

Do NOT write a checklist box requiring the entire suite to be green. Do NOT let
`gaps-identified.md` record an inert/no-op path as "acceptable" — an unfed
production path is a gap to fix or an explicit escalation, never a tolerated no-op.

## gaps-identified.md — keep it readable

Every pass reads the OPEN items of this file. One earlier ledger grew to 1.4 MB
and 18,000 lines because every pass appended a multi-page record, and it stopped
fitting in any reviewer's context. Seed it with the real gaps you found (one
`- [ ]` item each, a few lines, `file:symbol`), a `## Won't Fix` section, and this
rule at the top: *records are at most ten lines; closed items are moved under
`## Closed` with a one-line note; the open list is what a pass reads.*

## Finish

```
git -C ./work add RALPH.md docs/reference/ && git -C ./work commit -m "chore: seed {{slug}} ralph review plan" && git -C ./work push --quiet -u origin HEAD
```

Report: the number of single-system areas, the number of paired seams, the
implied minimum pass count (2×singles + paired), the budget, the count of
combinations recorded under `## No seam`, and confirm RALPH.md contains the
verbatim loop protocol. Say which candidate areas you merged and why, so the
critic can check your merge decisions instead of redoing them.
