# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_

## Fixed Previously

### LOOP-002: Missing depth assertions in "multiple loops sharing a head" test + no sibling-loops coverage
**Area**: Loop detection (`dashboard/src/graph/loops.ts`)
**Fixed**: Iteration 1
**Detail**: The "multiple loops sharing a head" test verified that `body(l1) ⊂ body(l2)` but never asserted `depth`. Since depth drives container nesting in the renderer, an incorrect depth value would cause visual mis-rendering without any failing tests. Added `expect(l1.depth).toBe(1)` and `expect(l2.depth).toBe(0)` to that test. Also added a new `describe` block for genuinely disjoint sibling loops (two sequential non-overlapping loops) to verify both get `depth === 0` — this topology was entirely untested._

<!--
Example of a fixed issue entry:

### LOOP-001: Nested loop body leaked across sibling loops
**Area**: Loop detection (`dashboard/src/graph/loops.ts`)
**Fixed**: Iteration 3
**Detail**: Body reachability traversed back-edges, so a nested loop's body absorbed nodes from a sibling loop via the cycle-closing edge. Stripped detected back-edges before building body adjacency. Added 2 tests: nested loops, two loops sharing a head.
-->

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_

<!--
Example of a won't-fix entry:

### FOLDER-W01: Enterprise workflow-folder system with group permissions
**Area**: Spec out-of-scope section
**Detail**: Folders that organize workflows and restrict run/view by security group depend on the merged Phase 6 identity model and a group concept Phase 6 does not introduce. Deferred to a post–Phase-6 spec; do not implement here.
-->
