# RALPH.md — Native Ralph Loop (seed)

This file is **workspace content**, not part of `workflow.json`. The harness copies
this seed set into the instance `{{workspace}}` before the first pass (or the first
`editor` pass creates them). The `editor` agent reads and rewrites these files each
iteration; the `assessor` reasons over their summarized contents.

## Constraints

- **Do NOT use sub-agents.** Work by hand, one component at a time.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **One focus area per iteration. Fix the most important issue, then stop.**
- **Do NOT invent new functionality.** Log perceived gaps in `gaps.md`; clearly out-of-scope items go under `Won't Fix`.

## Iteration Structure

1. **Read tracking** — Read `focus-areas.md`. Pick the next incomplete focus area (lowest number first).
2. **Read the code** — Read every file the focus area touches.
3. **Cross-reference** — Compare against the mission. Add issues to `gaps.md`.
4. **Fix the most important issue** — ONE issue per iteration.
5. **Write/verify tests** — Cover happy path, success, failure, error, and edge cases.
6. **Run tests** — Do not leave the workspace in a broken state.
7. **Assess the checklist**, then proceed to Wrap Up regardless of checklist state. **Do not go back to step 4.**

Most focus areas need multiple passes. That is normal and expected — do not try to
finish everything in one pass, and never mark an area complete just to end the loop.

## Definition of done

The loop is done when:

- Every item in `focus-areas.md` is satisfied, and
- `gaps.md` lists no blocking gap.

## Wrap Up

**Step A** — Did the focus area pass every item in the definition of done above?

**Step B** — If it passed: mark it complete in `focus-areas.md`. If it did not: do **NOT** update tracking.

**Step C** — Save your work. It is valuable regardless of checklist status.

**Step D** — Check both files:
1. Are ALL focus areas in `focus-areas.md` satisfied?
2. Are there ANY blocking gaps left in `gaps.md`?

Then output exactly one tag:
- All satisfied AND no blocking gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.**

The assessor reads that tag when it decides `done`, and the conditional routes to the
exit port on `{"done": true}`. The loop also exits unconditionally once the iteration
counter reaches `max_iterations` — but that backstop is a safety net, not the intended
way for a loop to end.
