# Workflow Auto-Arrangement — Product Specification

## Purpose

Help workflow authors and people monitoring runs trace execution paths without manually untangling diagrams.

Every newly created or updated workflow receives automatic top-to-bottom arrangement, followed by visual readability review and refinement. Workflow and instance views display the same arrangement for the same saved revision.

Readability takes priority over compactness and manual positioning. Long workflows may require substantial vertical scrolling.

This applies to parallel-group workflows as well: the containers, task cards and presentation connectors shown to authors and run viewers must be included in the reviewed arrangement. A different client-generated group layout does not satisfy review of the saved revision.

## Features

- **Arrange on every save.** Apply to all workflow creation and update paths. Replace manually adjusted node positions.
- **Save immediately.** Save the workflow before arrangement finishes. Show whether arrangement and review are in progress, complete, or need attention.
- **Present continuous downward flow.** Avoid wrapping sequential flow into columns. Place branches beside the flow and keep loop returns visually distinct.
- **Review the rendered result.** Every candidate layout receives visual readability review. Candidates that fail the readability criteria are refined and reviewed again.
- **Bound refinement.** Default to five total arrangement-and-review attempts per saved revision. Make this limit configurable through a configuration file.
- **Handle incomplete results.** If the limit is reached, retain the best available layout, identify unresolved readability issues, and offer retry. Arrangement or review failures never lose the saved workflow and are visibly reported with a retry option. If no new layout is available, retain the saved workflow's existing layout.
- **Respect saved revisions.** Apply results only to the revision reviewed. An older review cannot replace a newer revision's layout.
- **Preserve workflow behavior.** Arrangement changes presentation only. All execution behavior and connections remain intact.

## Outcomes and Acceptance Criteria

A successful layout meets these criteria:

- Nodes do not overlap.
- Connections do not pass through unrelated nodes.
- Node labels, branch labels, arrow directions, and connection endpoints remain readable at a practical viewing zoom.
- Each connection can be traced from source to destination. Lines do not merge or overlap in ways that conceal their destinations.
- Crossings are minimized. Unavoidable crossings remain distinguishable from junctions.
- Branches, rejoins, shortcuts, and loop returns are understandable without manually moving nodes.
- Workflow and instance views show the same arrangement for the same saved revision.

The current **feature-pipeline** is the complex acceptance example; **pr-review** is the simpler example. Both must show continuous downward progression, allowing vertical scrolling.

A result that exhausts its attempt limit without meeting the criteria is marked as needing attention, rather than presented as having passed review.

## Non-Goals

- Splitting workflows into phases or sub-workflows.
- Adding node creation or editing to the visual editor.
- Changing execution behavior, conditions, or connections.
- Preserving manual positions or adding layout opt-outs.
- Fitting an entire workflow onscreen.
- Guaranteeing zero crossings for every graph.
- Automatically rearranging existing workflows before their next save.
