# Mission: Seed the Ralph Review Plan

Working **unattended** in the worktree at `./work`, write three tracking files and
commit them. The user is unavailable — do not ask questions, make the calls yourself.

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
| `./work/docs/reference/focus-areas.md` | the **single-system** review plan (two approvals per area) |
| `./work/docs/reference/paired-reviews.md` | the **paired integration** review plan (one approval per row) |
| `./work/docs/reference/gaps-identified.md` | concrete known gaps, with a `Won't Fix` section |

Create `./work/docs/reference/` if it does not exist.

## focus-areas.md — the review plan

This is the part that makes the loop systematic rather than a grab-bag. Two tables.

### Table 1 — Single-System Reviews

Read the changed files and commits above and identify each distinct thing this
branch touched. Include one review area for each:

- each **domain concept** touched
- each **system** touched
- each **app** touched
- each **API** touched
- each **contract** touched

Name them concretely (`consent ledger`, `OneSignal channel adapter`) — never
generically (`backend`, `misc`).

Emit **one area per distinct thing the change set touched — there is no cap.**
Granularity is what makes a review specific enough to catch anything, so do not
merge two genuinely separate concerns to shorten the list. The paired table below
is seam-based, not all-pairs, so a longer list no longer multiplies the cost.

Each single-system area needs **2 independent approvals** before it is done — two
separate passes must each conclude it meets the bar, so the table has two status
columns.

### Table 2 — Paired Integration Reviews (seams only)

**Do NOT pair every area with every other.** Most pairs share no seam, and a review
of two things that never meet has nothing to inspect — it burns a pass to
manufacture an approval. Pair along real seams instead.

A **seam** exists between areas A and B when they actually meet in the code:

- one **produces** data the other **consumes**;
- they share a **table, queue/topic, contract, endpoint, or feature flag**;
- one **constructs, configures, or injects** the other;
- they are **adjacent hops** in the same runtime path.

For each seam you find, emit the pair in **both directions** — the reviewing
mindset genuinely differs by direction. For a pair (A, B):

> *I am reviewing the completeness, correctness, and spec alignment of **A** as it
> is impacted by updates in **B**. Both should meet a stringent bar for "done" and
> work together correctly.*

Because reverse pairs exist, each paired review needs only **1 approval**.

**Name the seam.** Every row must cite the concrete thing the two areas share —
the symbol, table, topic, endpoint, or flag. A pair you cannot name a seam for is
not a seam; drop it.

**Additionally pair every cross-cutting area with each area it gates.** Anything
that suppresses, authorizes, or short-circuits another path — a consent gate, an
auth check, a feature flag, a shared schema — has a seam with everything it
governs even when no data flows directly between them.

**Record what you deliberately did not pair.** End `focus-areas.md` with a short
`## No seam` list: each unpaired combination and a one-line reason. An omission
that is written down is a decision; an omission that is silent is a coverage gap
nobody can audit.

### The plan must fit the budget it will run under

**This is a hard constraint, not advice.** Compute the implied minimum pass count
as `2 × singles + paired`, then require:

> `2 × singles + ceil(paired ÷ 3)` **≤ `{{max_ralph_iterations}}` × 0.35**

Paired rows are charged at a third of a pass because up to four sharing an
area are reviewed together; singles are charged two passes each because their
two approvals are deliberately independent and cannot be batched.

The 0.35 is measured, not guessed: observed runs advance about **0.38 approval
boxes per pass**, because passes are also spent closing gaps, fixing regressions
and re-reviewing. Budgeting for the zero-rework ideal is what produced plans that
could not finish.

If your plan exceeds the budget, **do not shrink the area list** — granularity is
what makes a review specific enough to catch anything. Instead **prune the paired
table to the highest-risk seams** until it fits. A seam outranks another when it
crosses a trust boundary, carries persisted or user-visible state, gates another
path, or spans two components written in different phases. A seam between two
adapters that merely share a struct is the first to go.

Every pruned pair goes into `## No seam` marked **`DEFERRED (budget)`** with its
seam named and a one-line reason. Deferred is not the same as absent: a later
pass may promote one back if it turns out to matter. Silently dropping it is what
turns a scoped review into an incomplete one nobody can audit.

A plan that does not fit is rejected by `verify-ralph-seed` and the run stops, so
get this right the first time. State the arithmetic explicitly in RALPH.md:
singles, paired, implied minimum, the budget, and how many pairs you deferred.

### Order both tables by risk, highest first

Add a **Risk** column (`high` / `med` / `low`) to every row in both tables and
**emit the rows in descending risk order**. The refinement loop takes work in
that order, and it may be stopped by its iteration cap before finishing. Risk
ordering is what makes a truncated run useful: it will have reviewed the things
most likely to be broken instead of everything up to wherever it stopped.

Rank on consequence, not size: a silent data-corruption or auth-bypass path
outranks a cosmetic one however much code it touches.

### What every review actually checks

The tables say *what* to review; these are the criteria that decide whether a
review passes. The recurring failure is code that is present and green in
isolation but **never wired-and-fed** in production:

- For every new exported field / collaborator / config the branch added:
  `grep -rn <Symbol>` the whole repo — is it **constructed or populated outside
  `*_test.go`**? If it is only ever set in tests, the review fails.
- For every new component: is it assembled into the real composition root so the
  live path reaches it? Name the wiring site, or fail the review.
- Does any production call site pass `nil`/empty/hardcoded where a real producer
  should feed it?
- Do any tests pass only because a fake ignores a load-bearing argument, or
  because an "integration" test asserts an intermediate hop instead of the sink?
- Does the area have coverage across all five scenario categories (happy,
  success, failure, error, edge), and would at least one test **fail** if its
  production input were nil/empty?

Do the greps now and cite real findings — file:symbol — rather than writing
generic reminders.

## Table format

`./work/docs/reference/focus-areas.md`:

```markdown
## Single-System Reviews

| # | Focus Area | Risk | Review 1 Status | Review 2 Status |
|---|---|---|---|---|
| 1 | <area> | high | [ ] | [ ] |
```

`./work/docs/reference/paired-reviews.md`:

```markdown
## Paired Integration Reviews

| # | Paired Review | Seam | Risk | Status |
|---|---|---|---|---|
| 1 | <A> as impacted by <B> | <the shared symbol/table/flag> | high | [ ] |

## No seam

- `DEFERRED (budget)` — <pair> — <why it is lower risk for this branch>
```

Rows in BOTH files are ordered **highest risk first**. The `## No seam` list
lives at the end of `paired-reviews.md`.

Rows in both tables are ordered **highest risk first**.

## RALPH.md — MUST contain this section VERBATIM

**Transcribe the block below exactly — do not paraphrase, summarise, renumber or
invent your own.** Substitute only the bracketed placeholder.

**All three role blocks are mandatory.** `verify-ralph-seed` greps each one
separately for its Wrap Up, its self-assessment and its promise tags, and FAILS
THE RUN if any role is missing them — a file can otherwise carry a perfect
protocol for one role and silently omit the stop rule for another, which
surfaces sixty passes later as a timeout. Without them the refinement agent has
no rule for what *ends* a pass: it will
improvise a protocol that says what to do and never says when to stop, sprawl
across several review areas in one pass, and leave the loop unable to tell
progress from churn. It also carries the separation between the agent that fixes
and the agent that approves, which is what stops a mistake from being certified
by the same model that made it.

---8<--- copy from here ---8<---

## Role: single-review-1

*Refined by Sonnet 5, assessed by GPT-5.6-sol. The first independent look at an area.*

### Constraints
- **Do NOT use sub-agents.** Work by hand, one component at a time.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **One single-system area per iteration.** Fix the most important issue, then stop.
- **Do NOT invent new functionality.** Log gaps in `docs/reference/gaps-identified.md`; out-of-scope items go under `Won't Fix`.
- **The refiner never ticks a box; the approver never writes production code.** They run on different model families on purpose.

### Iteration Structure
1. **Read tracking** — `RALPH.md`, and the control file for this role.
2. **Read the code** — every file the assigned item covers.
3. **Cross-reference** — apply the wired-and-fed criteria below; record findings in `docs/reference/gaps-identified.md`.
4. **Fix the most important issue** — ONE issue, fixed well across all affected files.
5. **Write/verify tests** — happy, success, failure, error, edge. Strict TDD when adding behaviour.
6. **Run tests** — `[TEST COMMANDS]`. Compare against the recorded baseline; fix NEW failures only.
7. **Assess, then proceed to Wrap Up regardless of the outcome.** Do not go back to step 4.

### Wrap Up
**Step A** — Did the item meet every criterion?

**Step B** — Record a recommendation; tick NOTHING. The approver ticks Review 1 Status in `docs/reference/focus-areas.md`.

**Step C** — Commit and push. Your work is valuable regardless of review status.

**Step D** — One line: `Progress: <what advanced>` — the fix you landed, the gap you closed, the component you wired, or explicitly what did not advance and why. The progress assessor has no tools and sees only your response.

**Step E** — Check the tracking files, then output exactly one tag:
- All reviews approved AND no open gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.**

## Role: single-review-2

*Refined by GPT-5.6-sol, assessed by Opus 5. Review 1 already passed — this pass exists to find what the first reviewer MISSED, from a different model family.*

### Constraints
- **Do NOT use sub-agents.** Work by hand, one component at a time.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **One single-system area per iteration.** Fix the most important issue, then stop.
- **Do NOT invent new functionality.** Log gaps in `docs/reference/gaps-identified.md`; out-of-scope items go under `Won't Fix`.
- **The refiner never ticks a box; the approver never writes production code.** They run on different model families on purpose.

### Iteration Structure
1. **Read tracking** — `RALPH.md`, and the control file for this role.
2. **Read the code** — every file the assigned item covers.
3. **Cross-reference** — apply the wired-and-fed criteria below; record findings in `docs/reference/gaps-identified.md`.
4. **Fix the most important issue** — ONE issue, fixed well across all affected files.
5. **Write/verify tests** — happy, success, failure, error, edge. Strict TDD when adding behaviour.
6. **Run tests** — `[TEST COMMANDS]`. Compare against the recorded baseline; fix NEW failures only.
7. **Assess, then proceed to Wrap Up regardless of the outcome.** Do not go back to step 4.

### Wrap Up
**Step A** — Did the item meet every criterion?

**Step B** — Record a recommendation; tick NOTHING. The approver ticks Review 2 Status in `docs/reference/focus-areas.md`.

**Step C** — Commit and push. Your work is valuable regardless of review status.

**Step D** — One line: `Progress: <what advanced>` — the fix you landed, the gap you closed, the component you wired, or explicitly what did not advance and why. The progress assessor has no tools and sees only your response.

**Step E** — Check the tracking files, then output exactly one tag:
- All reviews approved AND no open gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.**

## Role: paired-review

*Refined by Opus 5, assessed by GPT-5.6-sol. Phase 2: runs only once every single-system box is ticked.*

### Constraints
- **Do NOT use sub-agents.** Work by hand, one component at a time.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **Up to FOUR paired rows sharing an already-loaded area per iteration.** Fix the most important issue, then stop.
- **Do NOT invent new functionality.** Log gaps in `docs/reference/gaps-identified.md`; out-of-scope items go under `Won't Fix`.
- **The refiner never ticks a box; the approver never writes production code.** They run on different model families on purpose.

### Iteration Structure
1. **Read tracking** — `RALPH.md`, and the control file for this role.
2. **Read the code** — every file the assigned item covers.
3. **Cross-reference** — apply the wired-and-fed criteria below; record findings in `docs/reference/gaps-identified.md`.
4. **Fix the most important issue** — ONE issue, fixed well across all affected files.
5. **Write/verify tests** — happy, success, failure, error, edge. Strict TDD when adding behaviour.
6. **Run tests** — `[TEST COMMANDS]`. Compare against the recorded baseline; fix NEW failures only.
7. **Assess, then proceed to Wrap Up regardless of the outcome.** Do not go back to step 4.

### Wrap Up
**Step A** — Did the item meet every criterion?

**Step B** — Record a recommendation per row; tick NOTHING. The approver ticks up to four rows in `docs/reference/paired-reviews.md`, each cited separately.

**Step C** — Commit and push. Your work is valuable regardless of review status.

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

- Every single-system review has **2 approvals**; every paired review has **1**.
- No unchecked items remain in `docs/reference/gaps-identified.md`.
- Every area is **wired-and-fed**, verified by grep rather than by a green isolated test.
- **No NEW test failures** beyond the recorded baseline, and nothing that built at baseline is broken.

Do NOT write a checklist box requiring the entire suite to be green. Do NOT let
`gaps-identified.md` record an inert/no-op path as "acceptable" — an unfed
production path is a gap to fix or an explicit escalation, never a tolerated no-op.

## Finish

```
git -C ./work add RALPH.md docs/reference/ && git -C ./work commit -m "chore: seed {{slug}} ralph review plan" && git -C ./work push --quiet -u origin HEAD
```

Report: the number of single-system areas, the number of paired reviews, the
implied minimum pass count (2×singles + paired), the number of seams found, and the count of deliberately-unpaired combinations, and confirm RALPH.md contains
the verbatim loop protocol.
