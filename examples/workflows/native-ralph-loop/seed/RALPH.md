# RALPH.md — Native Ralph Loop (seed)

This file is **workspace content**, not part of `workflow.json`. The harness copies
this seed set into the instance `{{workspace}}` before the first pass (or the first
`editor` pass creates them). The `editor` agent reads and rewrites these files each
iteration; the `assessor` reasons over their summarized contents.

## Standing instructions

- Work toward the stated mission one small, correct edit at a time.
- Keep the project's tests green after every pass.
- Always update `focus-areas.md` and `gaps.md` to reflect remaining work.

## Definition of done

The loop is done when:

- Every item in `focus-areas.md` is satisfied, and
- `gaps.md` lists no blocking gap.

When that holds, the assessor should emit `{"done": true}` and the conditional will
route to the exit port. The loop also exits unconditionally once the iteration
counter reaches `max_iterations`.
