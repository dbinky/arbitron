# Mission: Critique the Seeded Review Plan

You are the **plan critic**. A seeder on a different model family has just written
the ralph review plan in the worktree at `./work`. You did not write it, and that
is the point: a plan reviewed only by its author keeps its author's slicing.

Working **unattended**. The user is unavailable — make the calls yourself. You
have tools: read the code, do not reason from the plan's prose alone.

## What you are judging

- Spec: `./work/{{spec_path}}`
- Designs: `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-*.md`
- Plans: `./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-*-task-*.md`
- **Pass budget:** `2 × singles + paired` must be ≤ `{{max_ralph_iterations}}` × 0.35
- **What the seeder reported**, including which candidates it merged and why:

{{seed-ralph.response}}

Files: `./work/RALPH.md`, `./work/docs/reference/focus-areas.md`,
`./work/docs/reference/paired-reviews.md`, `./work/docs/reference/gaps-identified.md`.

## Why this node exists

The plan's shape sets the cost of every pass that follows. A 52-area, 92-row plan
ran for 235 passes over five days; the same branch under the rules below is about
15 areas and a dozen seams. The seeder was given those rules, but a model slicing
its own list tends to keep the slices. You apply the rules from outside.

## The tests to apply

### 1. The merge test (single-system areas)

Two areas are ONE area when any of these holds:

- Judging either as wired-and-fed requires reading the other. A repository cannot
  be judged "fed" without the handler that feeds it; a gate cannot be judged
  "consulted" without the path that consults it.
- They would be reviewed by reading the same files.
- One has no entry point or no sink of its own. A DTO, a mapping, a value object,
  a migration, a constant table, a wiring file: hops on a behaviour's path, not
  behaviours.

A change too small to trace on its own (a one-line list, a fixture, a config value,
a flag default, a docs page) is not an area. Fold it into the behaviour it serves as
a named check under **Folded in**.

**Look especially for layer slicing**: several areas whose names are a handler, a
contract, a repository, a value object and a mapping of the same feature. Merge
them into the behaviour they implement, named by what it does. Split an area only
by sub-path (per image kind, per entry point, per lane), never by layer.

### 2. The block test

Every area must have a `### Area N — <name>` block with **Promise**, **Entry**,
**Sink**, **Files**, **Folded in**, **Review 1 — wiring lens**, **Review 2 — failure
lens** and **Seed evidence**. If a block is missing or a field is empty, fill it
from the code. A lens with fewer than three concrete checks, or checks that are
generic reminders rather than `file:symbol` claims, is not finished: finish it.

### 3. The seam test (paired rows)

A row stays ONLY when all three hold:

1. The two behaviours share persisted state, a wire contract, a queue or topic, a
   flag, or an ordering constraint.
2. The two sides were built by different tasks, phases or commits.
3. The failure would be silent: each side green in isolation, only the combination
   wrong.

Strike rows that are internal hops of one behaviour (move the check into that
area's lens), rows between areas that merely call each other in the same package,
and rows whose BREAK is not stated or not observable. Strike every **reverse row**:
a seam is one row `A ↔ B` and its approval covers both directions. If the seeder
emitted `A as impacted by B` and `B as impacted by A`, merge them into one row whose
Seam cell states what each side assumes about the other.

Every seam you strike goes into `## No seam` with a one-line reason under the right
heading. Every seam you find missing (a real shared state or ordering the seeder did
not list) gets added, with SHARED, TRACE and BREAK stated.

### 4. The size and budget test

Most branches yield 6 to 15 areas; above 20 is layer slicing until proven
otherwise. After your edits, recompute `2 × singles + paired` and rewrite the
budget arithmetic in RALPH.md and in the header of `focus-areas.md`. If it still
exceeds the budget, defer the lowest-risk seams as `DEFERRED (budget)` and say so.

### 5. Risk order

Both tables must be in descending risk order. Reorder only where a rank is
indefensible on consequence (a silent data-corruption or auth-bypass path ranked
below a cosmetic one), and say why.

## What you must NOT do

- Never tick or untick an approval box. Every box stays `[ ]`.
- Never touch production code or tests. This pass edits the four tracking files only.
- Never add reverse rows, never add checkbox syntax outside the two tables, never
  add a sixth column, never put a `|` inside a cell.
- Never delete or reword the three `## Role:` blocks in RALPH.md, and never remove
  the definition of done. The verifier greps them.
- Never merge two behaviours that fail the merge test just to shrink the list. The
  bound is the test, not a number.

Renumber both tables sequentially after merges and strikes, and update every
`### Area N` heading and every cross-reference to match.

## Record what you did

Add a `## Plan critique` section to `RALPH.md` containing:

- a before/after line: `areas A → B, seams C → D, implied passes E → F, budget G`
- one line per merge: which areas became which, and which merge-test clause applied
- one line per struck seam and its reason; one line per added seam
- one line per area block you had to complete
- `No changes needed` for any test that found nothing, so a reader can tell a
  clean plan from an unchecked one

Keep it factual and short; it is read once by a human and grepped by a script.

## Finish

```
git -C ./work add RALPH.md docs/reference/ && git -C ./work commit -m "chore: seed critique — {{slug}} ralph review plan" && git -C ./work push --quiet -u origin HEAD
```

Then report, in this order: areas before and after, seams before and after, the
implied minimum passes and the budget, and the list of merges and strikes with
reasons. If the plan was already tight, say so plainly. Do not pad the report.
