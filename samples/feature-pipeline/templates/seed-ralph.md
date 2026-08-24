# Mission: Seed the Ralph Review Plan

Working **unattended** in the worktree at `./work`, write three tracking files and
commit them. The user is unavailable — do not ask questions, make the calls yourself.

## Inputs

- Spec: `./work/{{spec_path}}`
- Designs: `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-*.md`
- Plans: `./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-*-task-*.md`
- **Test command(s)** (use these exact commands — already detected for this repo): `{{record-baseline.test_commands}}`
- **Branch-point test baseline** (failures that existed BEFORE this feature): `{{record-baseline.test_baseline}}`

## Files to write

| Path | What it is |
|---|---|
| `./work/RALPH.md` | the charter: goal, test commands, definition of done, links to spec/designs/plans, **and the loop protocol** |
| `./work/docs/reference/focus-areas.md` | the review plan — two tables, described below |
| `./work/docs/reference/gaps-identified.md` | concrete known gaps, with a `Won't Fix` section |

Create `./work/docs/reference/` if it does not exist.

## focus-areas.md — the review plan

The feature has just been implemented task by task. Each task was verified in
isolation; what has **not** been verified is whether the pieces are wired together
and behave correctly as a whole. That is what this plan reviews. Two tables.

### Table 1 — Single-System Reviews

Read the phase designs and plan tasks and identify each distinct thing this
feature built or changed. Include one review area for each:

- each **domain concept** introduced or changed
- each **system** touched
- each **app** touched
- each **API** touched
- each **contract** touched (ports, message schemas, DB shapes, config sections)

Name them concretely (`consent ledger`, `OneSignal channel adapter`) — never
generically (`backend`, `misc`). Prefer the component boundaries the phase designs
already drew; they are the architecture's own seams.

Emit **one area per distinct thing the change set touched — there is no cap.**
Granularity is what makes a review specific enough to catch anything, so do not
merge two genuinely separate concerns to shorten the list. The paired table below
is seam-based, not all-pairs, so a longer list no longer multiplies the cost.

Each single-system area needs **2 independent approvals** before it is done, so the
table has two status columns. Two separate passes must each conclude it meets the bar.

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

This table is where a task-by-task implementation is most likely to be wrong: each
task passed its own tests, but nothing yet checked that Phase 2's consent gate is
actually consulted by Phase 4's fan-out.

### The plan must fit the budget it will run under

**This is a hard constraint, not advice.** Compute the implied minimum pass count
as `2 × singles + paired`, then require:

> `2 × singles + paired` **≤ `{{max_ralph_iterations}}` × 0.35**

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

The tables say *what* to review; these criteria decide whether a review passes. The
recurring failure mode is code that is present and green in isolation but **never
wired-and-fed** in production. Do the greps now and cite real findings —
`file:symbol` — not generic reminders:

- For **every** new exported field / collaborator / config the feature added:
  `grep -rn <Symbol>` the whole repo — is it **constructed or populated outside
  `*_test.go`**? If it is only ever set in tests, the review fails.
- For **every** new component: is it assembled into the real composition root
  (server/DI wiring) so the live path reaches it? Name the wiring site or fail it.
- Does any production call site pass `nil`/empty/hardcoded where a real producer
  should feed it? (a scorer injected `nil`, an embedding left empty, a bucket
  hardcoded.) List each.
- Do any tests pass only because a **fake ignores a load-bearing argument**, or
  because an "integration" test asserts an intermediate hop instead of the sink?
- Does the area have coverage across all five scenario categories (happy, success,
  failure, error, edge), and would **at least one test fail if its production input
  were nil/empty**? Green over-mocked tests do not count.

## Table format

```markdown
## Single-System Reviews

| # | Focus Area | Risk | Review 1 Status | Review 2 Status |
|---|---|---|---|---|
| 1 | <area> | high | [ ] | [ ] |

## Paired Integration Reviews

| # | Paired Review | Seam | Risk | Status |
|---|---|---|---|---|
| 1 | <A> as impacted by <B> | <the shared symbol/table/flag> | high | [ ] |
```

Rows in both tables are ordered **highest risk first**.

## RALPH.md — MUST contain this section VERBATIM

**Transcribe the block below exactly — do not paraphrase, summarise, renumber or
invent your own.** Substitute only the bracketed placeholder.

Without it the refinement agent has no rule for what *ends* a pass: it will
improvise a protocol that says what to do and never says when to stop, sprawl
across several review areas in one pass, and leave the loop unable to tell
progress from churn. It also carries the separation between the agent that
fixes and the agent that approves, which is what stops a mistake from being
certified by the same model that made it.

---8<--- copy from here ---8<---

## Roles

Each iteration runs TWO agents, deliberately on different models:

- The **refinement pass** reviews one area, fixes the most important issue, records findings, and commits. It **never** ticks an approval box.
- The **approver** runs immediately afterwards, re-derives the evidence independently, and is the ONLY agent permitted to tick a box in `docs/reference/focus-areas.md`.

An agent that certifies its own change has performed no review. A mistake approved by its own author stops being visible and compounds into every later pass.

A script also runs `[TEST COMMANDS]` after each pass and diffs the failures against the recorded baseline. Any NEW failure, or a target that built at baseline and no longer builds, blocks every approval until it is fixed.

## Constraints

- **Do NOT use sub-agents.** Review and fix by hand, one component at a time.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **One single-system area per iteration. Fix the most important issue, then stop.**
- **Paired integration reviews may be batched**: up to FOUR unchecked seams that share an already-loaded area may be reviewed in one pass. They carry one approval each and are usually inspection, not repair. Each seam must be examined and reported separately.
- **Do NOT invent new functionality.** Log perceived gaps in `docs/reference/gaps-identified.md`. Clearly out-of-scope items go under `Won't Fix`.
- **Approvals are independent.** Never record two approvals for a single-system area in one pass — the second approval exists to catch what the first missed.
- **Never tick a box for work you performed yourself.**

## Iteration Structure (refinement pass)

1. **Read tracking** — Read `docs/reference/focus-areas.md`. Pick the HIGHEST-RISK review with an unchecked status, reading the Risk column across BOTH tables; break ties by table (single-system first), then number. The loop may be stopped by its cap before the plan is finished, so risk order is what makes a truncated run useful.
2. **Read the code** — Read every file the review covers.
3. **Cross-reference** — Apply the wired-and-fed criteria in RALPH.md. Record findings in `docs/reference/gaps-identified.md`.
4. **Fix the most important issue** — ONE issue per iteration, fixed well across all affected files.
5. **Write/verify tests** — happy path, success, failure, error, edge. Strict TDD when adding behaviour.
6. **Run tests** — Run `[TEST COMMANDS]`. Compare against the recorded baseline; fix NEW failures only.
7. **Assess the review**, then proceed to Wrap Up regardless of the outcome. **Do not go back to step 4.**

Most review areas need multiple passes. That is normal and expected — do not try
to finish everything in one pass, and never inflate a recommendation to end the loop sooner.

## Wrap Up (refinement pass)

**Step A** — Did the review area meet every criterion for approval?

**Step B** — Record a **recommendation**; do NOT tick any box. One line, exactly one of: `Recommend: approve area <N> review <1|2> — <the evidence>` or `Recommend: no approval for area <N> — <what is still missing>`.

**Step C** — Commit and push. Your work is valuable regardless of review status.

**Step D** — State what this pass advanced, as a single line: `Progress: <what advanced>` — the fix you landed, the gap you closed, the component you wired, or explicitly "no fix landed" and why. The progress assessor has no tools and sees only your response; Step E reports what REMAINS, so without this line progress is indistinguishable from churn.

**Step E** — Check both files:
1. Does every single-system review have BOTH approvals, and every paired review its one approval?
2. Are there ANY unchecked `[ ]` items in `docs/reference/gaps-identified.md`?

Then output exactly one tag:
- All reviews approved AND no open gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.**

## Approval (approver only)

**Step A** — Re-derive the evidence yourself: read `docs/reference/focus-areas.md`, then `git log --oneline -5` and `git show --stat HEAD`, then the diff. Run the wired-and-fed greps yourself. Do not accept the pass's claims as evidence.

**Step B** — If the deterministic test check reported ANY new failure versus the baseline, or a new build break, tick NOTHING and log it as an unchecked gap. Otherwise tick **at most ONE** box for a single-system area, or up to FOUR paired rows that this pass genuinely reviewed — each cited separately.

**Step C** — Commit and push, whether or not you approved.

**Step D** — State the outcome as a single line: `Approved: area <N> review <1|2> — <the evidence>` or `Withheld: area <N> — <what is missing>`.

Withholding approval is a successful outcome. The loop has its own backstops for
forward motion; the approver exists so that a ticked box means something.

---8<--- copy to here ---8<---

Substitute `[TEST COMMANDS]` with the exact commands from the Inputs above.
Everything else is copied unchanged — the `<promise>` tags especially, because the
workflow verifies them and the assessor reads them.

## Definition of done — put this in RALPH.md too

The repo may already be red on arrival. Pre-existing failures are NOT this
feature's job and MUST NOT block the loop. "Done" means ALL of:

- Every single-system review has **2 approvals**; every paired review has **1**.
- No unchecked items remain in `docs/reference/gaps-identified.md`.
- Every plan task is genuinely implemented — **wired-and-fed, not merely present**:
  constructed at the real composition root AND fed real data from a producer that
  exists in the code (NOT `nil`/empty/hardcoded, NOT only set in tests).
- The phase designs are satisfied in spirit.
- **No NEW test failures** beyond the recorded branch-point baseline — i.e. the
  current set of failing tests is a subset of the baseline.
- **No new build/compile break.** A suite that built at the baseline must still build.

Do NOT write a checklist box that requires the entire suite to be green. Do NOT let
`docs/reference/gaps-identified.md` record an inert/no-op path (e.g. "empty set treated as
maximally varied — acceptable") as *acceptable*: an unfed production path is a GAP
to fix or an explicit escalation, never a tolerated no-op.

## Finish

```
git -C ./work add RALPH.md docs/reference/ && git -C ./work commit -m "chore: seed {{slug}} ralph review plan" && git -C ./work push --quiet -u origin HEAD
```

Report: the number of single-system areas, the number of paired reviews, the
implied minimum pass count (2×singles + paired), the number of seams found, and the count of deliberately-unpaired combinations, and confirm RALPH.md contains the
verbatim loop protocol.
