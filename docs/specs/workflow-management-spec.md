# Workflow Management — Product Specification

## Purpose

Give arbitron a robust, MCP-driven capability for admins (and the single user in
local mode) to manage the full lifecycle of workflows — rename, delete, unpublish,
duplicate, edit in place, archive, and transfer ownership — safely and
consistently. The capability eliminates the identity and visibility dead-ends that
currently make published workflows effectively unmanageable.

## Features

- **Rename.** An admin can change a workflow's name. Renaming is seamless: anything
  that already references the workflow internally (in-flight instances, schedules)
  keeps working, and past run history is unaffected. The name behaves as a label on
  the workflow, not as its permanent identity.
- **Delete.** An admin can remove any workflow, including shared/published ones
  (today these are read-only and cannot be removed). Deletion is *guarded*: it is
  refused while the workflow has actively running instances (the admin cancels or
  lets them drain first). Past run history and traces are always preserved and
  remain viewable after deletion.
- **Unpublish.** An admin can return a shared/public workflow to private.
- **Duplicate.** An admin can clone any workflow into a new private workflow — not
  only the existing shared-to-private copy.
- **Edit in place.** An admin can modify a shared/published workflow directly,
  without maintaining a separate private copy and re-publishing. Changes apply to
  future runs; instances already in flight continue on the version they started
  with.
- **Archive / disable.** An admin can reversibly deactivate a workflow so it can no
  longer be triggered, without deleting it or its history. Archiving can be undone.
- **Transfer ownership.** An admin can reassign a workflow's owner to another user.
- **Global name uniqueness.** Every workflow name is unique across the whole
  system. Rename, create, publish, and duplicate reject a name that is already in
  use, with a clear error.
- **Admin-gated.** All of the above operations require an admin; in local mode the
  single user is treated as the admin. Non-admin users can still create and use
  workflows but cannot perform these management operations.
- **Audit trail.** Every management operation is recorded — who performed it, which
  operation, the target workflow, when, and the before/after name where relevant —
  and the record is queryable.
- **All via MCP.** Every management operation and the audit query are available
  through the arbitron MCP server.

## Outcomes

- Admins can manage the entire workflow catalog through MCP with no read-only or
  identity dead-ends — the "a published workflow can't be renamed or deleted" trap
  is gone.
- A workflow has one unambiguous name everywhere it appears — listings, lookups,
  and triggers — and renaming or editing it never silently breaks an in-flight run
  or loses history.
- Destructive actions are guarded and accountable: running work is never clobbered,
  history always survives, and every change is attributable through the audit
  trail.
- The current class of confusion — a private and a shared workflow sharing a name,
  plus a stale internally-stored name diverging from the name used to look it up —
  becomes impossible.

## Non-Goals

- No management dashboard or UI in this iteration — management is delivered through
  MCP only. A UI may follow later.
- No change to how workflows are authored or validated, or to how instances
  execute.
- No management delegation beyond admin / local-mode user — there is no per-owner or
  per-department management role in this iteration.
- The capability is not responsible for updating external, name-based callers (for
  example, a GitHub Action that triggers a workflow by name) when a workflow is
  renamed; it cannot reach outside the system.
- No bulk automatic migration of pre-existing ambiguous names. Uniqueness is
  enforced going forward, and any existing name collisions are reconciled as part
  of rollout rather than by an automated migration feature.
