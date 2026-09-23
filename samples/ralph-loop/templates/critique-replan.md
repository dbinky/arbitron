# Mission: Re-plan an In-Flight Review Without Losing Earned Approvals

You are the **plan critic**, on a different model family from whoever wrote this
plan. This run was **resumed with `replan=true`**: the branch already carries a
review plan, passes have already earned approvals against it, and the operator
wants the remainder tightened to the current rules before the loop continues.

Working **unattended** in the worktree at `./work`. The user is unavailable — make
the calls yourself. You have tools: read the code, do not reason from prose alone.

## What you are judging

- **Branch:** `{{branch}}` (the run continues on it; every approval already
  earned is committed here)
- **Operator scope note** (may be empty): {{scope_note}}
- **Pass budget:** `2 × singles + paired` must be ≤ `{{max_ralph_iterations}}` × 0.35

Files: `./work/RALPH.md`, `./work/docs/reference/focus-areas.md`,
`./work/docs/reference/paired-reviews.md`, `./work/docs/reference/gaps-identified.md`.
The worktree is a shallow clone, so `git log` shows little; read the plan, then
the code it names.

## Why this pass exists

The plan's shape sets the cost of every pass that follows. Plans written before
2026-09-11 sliced areas by layer or by file (a handler, a contract, a repository,
a value object and a mapping of the same feature, each costing two passes) and
listed every seam twice, once per direction. One such plan needed 235 passes over
five days; the same branch under the rules below is about 15 areas and a dozen
seams. Two runs are mid-flight on plans of that old shape, with 54 and 131 passes
still ahead of them. You are converting the **remaining** work to the new shape
while keeping every approval that was genuinely earned.

## Step 1 — read the plan as it stands

Record, before you change anything: the number of single rows and which boxes are
ticked on each; the number of paired rows, whether they are directional
(`A as impacted by B`, with the reverse listed as its own row) or seam rows
(`A ↔ B`), and which are ticked; whether areas have `### Area N` detail blocks.
This snapshot goes into the record at the end.

## Step 2 — rebuild the single-system table as behaviours

A single-system area is a **behaviour**: one thing the branch promises, traced from
its entry point to its sink across every layer. Not a file, a class, a layer, a
package or a plan task.

**The merge test — two areas are ONE area when any of these holds:**

- Judging either as wired-and-fed requires reading the other. A repository cannot
  be judged "fed" without the handler that feeds it; a gate cannot be judged
  "consulted" without the path that consults it.
- They would be reviewed by reading the same files.
- One has no entry point or no sink of its own. A DTO, a mapping, a value object,
  a migration, a constant table, a wiring file: hops on a behaviour's path, not
  behaviours. A composition root is not an area; "constructed at the root at
  `file:symbol`" is a wiring-lens check in every behaviour it wires.

A change too small to trace on its own (a one-line list, a fixture, a config value,
a flag default, a docs page) is **folded in** as a named check of the behaviour it
serves. Split a behaviour only by sub-path (per image kind, per entry point, per
lane), never by layer. Most branches yield 6 to 15 behaviours; above 20 is layer
slicing until proven otherwise.

Every new row gets a `### Area N — <name>` block under a `## Area details` heading
after the table, with **Promise**, **Entry**, **Sink**, **Files**, **Folded in**,
**Prior approvals** (see step 3), **Review 1 — wiring lens** (3 to 7 numbered
checks: constructed at the composition root at `file:symbol`; fed by the real
producer; happy and success paths reach the sink; the test that demonstrates each),
**Review 2 — failure lens** (3 to 7 numbered checks: nil and empty inputs,
timeouts, partial failure and rollback, concurrency, ordering; the test that goes
red for each) and **Seed evidence** (the greps you ran now, `file:symbol`). Fill
them from the code. Numbered lists only: **never checkbox syntax outside the two
tables**, because the progress script counts `[x]` file-wide.

Keep five columns exactly: `| # | Focus Area | Risk | Review 1 Status | Review 2 Status |`.
Rows in descending risk order, renumbered sequentially.

## Step 3 — the in-flight approval rule

This is the part that differs from a fresh plan, and it is the part the operator
will audit. A ticked box on a new row is a claim that the whole behaviour passed
that review. So:

- A new row's **Review 1** box is `[x]` only if **every** old row merged into it had
  Review 1 ticked. Same for Review 2. One unticked constituent reopens the box.
- An old row that becomes a **folded-in** check of an already-approved behaviour
  was never reviewed. It must have **another review path**: a seam row whose TRACE
  runs through it, or a check in a behaviour whose box is still open. If neither
  exists, the behaviour's boxes reopen. Never keep a tick on a row whose
  unreviewed part has no review path.
- When a box reopens, the block's **Prior approvals** line names the constituents
  that were approved before consolidation and the one(s) that were not, so the
  next pass concentrates on the unreviewed part and the approver can reuse the
  evidence that still stands. Reopening is not erasure.
- Old approvals were "first look" and "second look" rather than wiring and
  failure lenses. Treat a constituent whose both boxes were ticked as having
  satisfied both lenses; treat a constituent with only Review 1 ticked as
  satisfying the wiring lens only.
- A new seam row is `[x]` only if every old paired row merged into it, both
  directions, was ticked. Otherwise `[ ]`.

Never tick a box that was not earned, and never untick one except by this rule.

## Step 4 — rebuild the paired table as seams

**The seam test — a row exists ONLY when all three hold:**

1. The two behaviours share persisted state, a wire contract, a queue or topic, a
   flag, or an ordering constraint.
2. The two sides were built by different tasks, phases or commits.
3. The failure would be silent: each side green in isolation, only the combination
   wrong.

Strike rows that are now internal hops of one behaviour (their check moves into
that area's lens), rows between behaviours that merely call each other in the
same package, and rows whose BREAK cannot be stated. Merge every directional pair
`A as impacted by B` / `B as impacted by A` into **one** row `A ↔ B` whose Seam cell
states what each side assumes about the other. Add any real seam the old plan
missed. Five columns exactly, no `|` inside a cell:

```markdown
| # | Paired Review | Seam | Risk | Status |
|---|---|---|---|---|
| 1 | <A> ↔ <B> | SHARED: <the state, contract, flag or ordering> · TRACE: <entry, through the seam, to the observable result> · BREAK: <the failure that must be shown impossible or caught> | high | [ ] |
```

End `paired-reviews.md` with `## No seam`: every struck or unpaired combination,
one line each with the reason, under **Inside one behaviour** and **Genuinely
unrelated**; `DEFERRED (budget)` for a real seam dropped only for budget.

## Step 5 — budget, risk order, cross-references

Recompute `2 × singles + paired` and rewrite the budget arithmetic in RALPH.md and
in the header of `focus-areas.md`. Both tables in descending risk order. The gap
ledger cites old area numbers: **do not rewrite its entries**. Add one short note
at the top of `gaps-identified.md`, `Plan consolidated <date>: old area N → new
area M, …`, covering every old row, and put the same map in the record below.
Leave every gap item's state exactly as it is.

## What you must NOT do

- Never touch production code or tests. This pass edits the four tracking files only.
- Never delete or reword the three `## Role:` blocks in RALPH.md, never remove the
  definition of done, never remove the test-baseline references. The verifier
  greps the role blocks and the next scripts read the rest.
- Never leave a directional paired row, a reverse row, checkbox syntax outside the
  two tables, a sixth column, or a `|` inside a cell. The verifier rejects each.
- Never merge two behaviours that fail the merge test just to shrink the list.

## Record what you did

Add a `## Plan critique` section to `RALPH.md` (append if one exists) containing:

- `replan <date>: areas A → B, seams C → D, ticked boxes E → F, implied passes G → H, budget I`
- the old-to-new map: one line per old single row, `old N → new M (kept R1/R2 | reopened: why)`,
  and one line per old paired row or mirror pair, `old N,M → new K | struck: why`
- one line per added seam
- `No changes needed` for any step that found nothing

Keep it factual and short; it is read once by a human and grepped by a script.

## Finish

```
git -C ./work add RALPH.md docs/reference/ && git -C ./work commit -m "chore: seed critique — replan of the {{slug}} ralph review plan" && git -C ./work push --quiet -u origin HEAD
```

Then report: areas before and after, seams before and after, ticked boxes before
and after with every reopened box named and justified, the implied minimum passes
and the budget. Do not pad the report.
