# Dashboard UX Improvements — Spec

## Overview

This spec covers a focused set of dashboard usability improvements that make
running and reviewing workflows easier to read and easier to manage. It
addresses four pain points:

1. **Loop semantics are hard to read** in the workflow graph — backflow edges
   cut confusingly across the diagram and loop bodies drift out of alignment.
2. **The graph always opens fully zoomed out**, so step titles are too small to
   read, and the viewport does not track the step that is currently running.
3. **The queue list is shared and ever-growing** — every user sees every item
   and has no way to clear finished work out of their own view.
4. **There is no way to bring dismissed items back** once they have been cleared.

Each improvement is described below in terms of the experience it delivers, not
how it is built. A short "System impact" note at the end records where these
changes reach beyond the browser so downstream planning is not surprised.

### Out of scope (deferred)

An **enterprise workflow-folder system** — folders that organize workflows and
restrict who can run or view them, tied to security groups — was considered
alongside these items and **deliberately deferred**. It depends on the
multi-user identity model introduced in Phase 6 (real users, roles, and
server-side visibility enforcement) and, beyond that, on a *security-group*
concept that Phase 6 does not introduce. Building folder permissions before
Phase 6 is merged would mean coding against an unmerged, moving foundation, and
the group concept it needs does not yet exist. It will be specified separately
once Phase 6 is merged, designed coherently with that identity model and the
existing private-workspace / shared-directory structure.

---

## 1. Clearer loop rendering

### Problem

In the workflow graph, a loop is expressed as an edge that returns to an earlier
step. Today every edge is drawn the same way, so a loop's return edge runs
straight back up the diagram and slices across the steps between, and the steps
inside the loop are not kept aligned with the main flow. The result reads as
visual noise rather than as "this section repeats."

### Outcome

A loop reads at a glance as a self-contained, repeating section.

- The steps that make up a loop are visually **grouped together inside a labeled
  container**, kept aligned in a single column. The path that enters the loop
  and the path that exits it cross the container boundary cleanly.
- The **return edge** (the edge that loops back) is drawn as a **visually
  distinct curve** — styled differently from normal forward edges and routed to
  the side rather than cutting across the steps in between.
- The container carries a short **"Loop until …" label** describing the loop's
  exit intent — for example, "Loop until refinement complete" or "Loop until
  tests pass." The phrase is authored on the loop's return edge (reusing the
  edge label that already exists in a workflow definition). When no phrase is
  provided, the container is simply labeled **"Loop."**
- **Nested loops** are shown as nested containers.

### Success criteria

- A workflow containing a loop displays that loop's steps grouped in a labeled
  container, aligned in one column.
- The return edge is visually distinguishable from forward edges and does not
  cross over the steps between the loop's end and its start.
- The container label shows the author-provided "Loop until …" phrase, or
  "Loop" when none is provided.
- A workflow with a loop inside a loop renders as nested containers.

---

## 2. Readable default zoom with follow-the-current-step

### Problem

The graph opens fitted so the entire workflow is visible, which on any
non-trivial workflow shrinks step titles below readability. When an instance is
running, the viewport does not move to show which step is executing, so the user
has to hunt for the active step and zoom in manually every time.

### Outcome

The graph opens at a comfortable, readable zoom and, while an instance runs,
keeps the active step in view automatically.

- **Default zoom** opens at a level where step titles are comfortably readable
  (roughly two to three times the apparent size of the current fit-to-fit view),
  rather than fitting the whole graph.
- **While an instance is running**, the viewport **smoothly animates to center
  the currently executing step** — both when the view is first opened and each
  time execution advances to a new step.
- The user stays in control: **manually panning or zooming pauses the automatic
  follow**, and a clearly labeled **Recenter / Follow control** lets them
  re-engage automatic following at any time.
- **When there is no active step** (a completed or failed instance, or a static
  workflow preview), the view opens centered on the most relevant step at the
  default zoom:
  - completed instance → centered on the final step,
  - failed instance → centered on the failed step,
  - static workflow preview → centered on the entry step.

### Success criteria

- Opening a workflow graph shows readable step titles without manual zooming.
- Opening a running instance centers the active step; as execution advances, the
  viewport animates to keep the new active step centered.
- Manually panning or zooming stops the automatic follow; using the
  Recenter / Follow control resumes it and re-centers the active step.
- Opening a completed instance centers the final step, a failed instance centers
  the failed step, and a static preview centers the entry step — each at the
  default zoom.

---

## 3. Per-user queue dismissal

### Problem

The dashboard queue shows all instances to everyone, and finished work never
clears out. A user cannot tidy their own view; completed and failed runs pile up
indefinitely with no way to set them aside.

### Outcome

Each user can curate their own queue view by dismissing finished items, without
affecting anyone else's view or the underlying instance.

- The list each user sees is **scoped to what that user is permitted to see**
  (in single-user/local operation that is everything; this naturally narrows to
  per-user visibility under multi-user operation).
- Each **Completed** and **Failed** item has a **dismiss control ("[x]")** that
  removes it from that user's list. Dismissal is **per user** — it changes only
  the dismissing user's view and never deletes or alters the instance itself.
- Dismissal applies **only to terminal items** (Completed and Failed). Active,
  queued, waiting, and paused items cannot be dismissed.
- Each of the **Completed** and **Failed** categories has a **[Dismiss All]**
  control at its top that dismisses every item currently listed in that category
  for that user.
- **Resurface on a new outcome:** retrying or re-running a dismissed item clears
  its dismissal. While it is running again it appears in the Active section, and
  when it reaches a fresh terminal state it reappears (no longer dismissed) in
  Completed or Failed. The rationale is that a new result is new information the
  user should see.

### Success criteria

- A user can dismiss an individual completed or failed item and it disappears
  from their list only.
- Another user's view of the same instance is unaffected by the first user's
  dismissal.
- Active, queued, waiting, and paused items offer no dismiss control.
- [Dismiss All] on a category clears every currently listed item in that
  category for the acting user.
- Retrying or re-running a dismissed item causes it to reappear (active, then in
  its new terminal category) for that user.

---

## 4. Show / Hide dismissed

### Problem

Once items are dismissed there is no way to review or recover them — dismissal
would otherwise be a one-way door.

### Outcome

Dismissed items can be revealed, restored individually, and hidden again at will.

- A single **toggle at the bottom of the dashboard** controls visibility of
  dismissed items across both the Completed and Failed categories.
- Activating **[Show Dismissed]** brings the dismissed items back into the list
  **in their original chronological position**, interleaved with non-dismissed
  items, and the toggle's label changes to **[Hide Dismissed]**.
- While dismissed items are shown, they are **visually muted** so they are
  distinguishable from non-dismissed items, and each carries a **restore
  control ("↺")** — replacing its dismiss control — that un-dismisses that item
  for the user.
- Activating **[Hide Dismissed]** hides the dismissed items again.

### Success criteria

- The toggle starts as [Show Dismissed]; activating it reveals all dismissed
  items in their correct chronological order and relabels itself
  [Hide Dismissed].
- Shown dismissed items are visually distinct from non-dismissed items.
- Restoring a shown dismissed item returns it to the user's normal (un-dismissed)
  list.
- Activating [Hide Dismissed] removes dismissed items from view again.
- The toggle reflects the acting user's own dismissals only.

---

## System impact

Most of this work is in the dashboard, but two items reach into the backend.
Recorded here at the level of outcomes, not design:

- **Loop rendering (#1) and zoom/follow (#2)** are dashboard-only. They change
  how the existing workflow graph is laid out and how the viewport behaves; they
  require no new stored data and no API changes. The "Loop until …" phrase
  reuses the loop edge's existing label.
- **Per-user dismissal (#3, #4)** requires the system to **remember, per user,
  which instances that user has dismissed**, and to expose this through the
  dashboard's data interface: dismissing an instance, dismissing all in a
  category, restoring an instance, and listing instances with the option to
  include dismissed ones (with dismissed items marked as such). This state is
  per user and independent of the instance's own lifecycle, except that a
  retry/re-run clears the dismissal as described in section 3.

These per-user behaviors are defined so they work correctly with a single user
today and remain correct when multiple users with scoped visibility are present.
