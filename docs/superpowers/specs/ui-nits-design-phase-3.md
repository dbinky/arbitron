# UI Nits — Design Phase 3: Per-User Instance Dismissal

Implements spec sections 3 ("Per-user queue dismissal") and 4 ("Show / Hide
dismissed") from `docs/specs/ui-nits-spec.md`. Backend + frontend.

## Goal

Let each user remove finished (Completed/Failed) items from their own queue view
without affecting anyone else or the instance itself, dismiss a whole category at
once, reveal/restore dismissed items via a global toggle, and have a retried or
re-run item resurface on its new outcome.

## Current state (this branch = `main`, pre–Phase 6)

- No authentication. Every request acts as the seeded **`local`** user
  (`users` row `00000000-0000-0000-0000-000000000001`, role `admin`), resolved
  via `GetDefaultUser()`. `adapter/http/server.go` has a `// TODO(phase-6)` where
  auth middleware will land.
- Instances carry `owner_id` (set to the default user in `engine.Trigger`).
  `notifications` have `read`/`dismissed` flags; **instances have no dismiss
  state**.
- Persistence is SQLite via **sqlc**: `adapter/sqlite/sqlc.yaml`,
  `queries/*.sql`, generated code in `adapter/sqlite/generated/`, migrations in
  `adapter/sqlite/migrations` (latest `002_connections`). Migration runner is
  `adapter/sqlite/migrate.go` (embedded, versioned).
- HTTP: chi router; handlers call repos / the `engine.Engine` directly (no
  service layer). JSON via `WriteJSON` / `WriteError` helpers. Instance list is
  `GET /api/v1/instances?state=&since=` in `instance_handler.go`.
- Frontend: `QueueView.tsx` groups Active / Failed / Completed via
  `QueueSection.tsx` + `InstanceRow.tsx`, fed by `api.instances.list(...)` in
  `dashboard/src/api/client.ts`. WebSocket events trigger debounced refetches.

## Acting-user seam

Dismissal is per user, but only the `local` user exists today. Introduce one
helper used by every dismissal endpoint:

```go
func currentUser(r *http.Request) uuid.UUID // returns GetDefaultUser() today
```

Phase 6 replaces its body with the authenticated user from request context. All
dismissal code depends only on this seam, so the per-user semantics are correct
now (single user) and compose later (many users).

## Backend

### Migration `003_instance_dismissals`

```sql
-- up
CREATE TABLE IF NOT EXISTS instance_dismissals (
    user_id      TEXT NOT NULL REFERENCES users(id),
    instance_id  TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    dismissed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    PRIMARY KEY (user_id, instance_id)
);
CREATE INDEX IF NOT EXISTS idx_instance_dismissals_user
    ON instance_dismissals (user_id);
-- down
DROP TABLE IF EXISTS instance_dismissals;
```

`PRIMARY KEY (user_id, instance_id)` makes dismiss idempotent (upsert/ignore);
`ON DELETE CASCADE` cleans up if an instance is ever deleted.

### sqlc queries (`queries/instance_dismissals.sql`)

- `DismissInstance :exec` — `INSERT ... ON CONFLICT(user_id, instance_id) DO
  NOTHING`.
- `RestoreInstance :exec` — `DELETE WHERE user_id = ? AND instance_id = ?`.
- `DismissInstancesByIDs` — applied per id from the service (or a batch insert),
  used by Dismiss-All over the ids currently in a category for the user.
- `ListDismissedInstanceIDs :many` — `SELECT instance_id WHERE user_id = ?`
  (used to mark/filter list results).
- `ClearInstanceDismissals :exec` — `DELETE WHERE instance_id = ?` (all users),
  called on retry.

Run `sqlc generate` after adding the query file.

### Domain port + repo

- `domain/instance/dismissal.go` (or extend `ports.go`):

```go
type DismissalRepository interface {
    Dismiss(ctx context.Context, userID, instanceID uuid.UUID) error
    Restore(ctx context.Context, userID, instanceID uuid.UUID) error
    DismissedIDs(ctx context.Context, userID uuid.UUID) (map[uuid.UUID]bool, error)
    ClearForInstance(ctx context.Context, instanceID uuid.UUID) error
}
```

- `adapter/sqlite/dismissal_repo.go` implements it over the generated queries,
  following `notification_repo.go` conventions (uuid↔string helpers, error
  wrapping, `ErrNotFound` where relevant — restore of a non-dismissed item is a
  no-op success).

### List endpoint: mark + include

`GET /api/v1/instances` (`instance_handler.go` `List`):

- Resolve `currentUser(r)`; load `DismissedIDs(user)`.
- Add query param `includeDismissed` (default `false`).
- Add `dismissed bool` to the instance summary JSON returned for items.
- Default (`includeDismissed=false`): omit dismissed items from the response.
- `includeDismissed=true`: return all items in the existing window, each marked
  with `dismissed`. Ordering is unchanged (existing sort), so the frontend can
  reinsert dismissed items in their original chronological position.
- Only **terminal** items (failed/completed) can be dismissed, so Active items
  are never affected by this logic.

### Mutation endpoints (chi routes under `/instances`)

- `POST /instances/{id}/dismiss` → `repo.Dismiss(currentUser, id)`. Guard:
  the instance must be in a terminal state (completed/failed); otherwise
  `WriteError(409, ...)`.
- `POST /instances/{id}/restore` → `repo.Restore(currentUser, id)`.
- `POST /instances/dismiss-all?state=failed|completed` → resolve the ids
  currently visible to the user in that category (reuse the List query with the
  state filter + window), then dismiss each. Validate `state ∈ {failed,
  completed}`.

All return `WriteJSON(200, {"status":"ok"})` on success and emit no global
WebSocket event (dismissal is per-user view state, not instance state). The
frontend updates optimistically / refetches its own list.

### Resurface on retry/rerun (`domain/engine/engine.go`)

- `Retry(id, ...)`: after transitioning the instance back to running, call
  `dismissals.ClearForInstance(ctx, id)` so the (now active) item leaves the
  dismissed set for everyone who had dismissed it and reappears in Active, then
  in its fresh terminal category. Wire a `DismissalRepository` into the engine
  (constructor + `cmd/arbitron/main.go` wiring).
- `Rerun(id)`: creates a new instance id via `Trigger`, which has no dismissal
  rows — inherently un-dismissed. No change needed beyond a comment.

## Frontend

- `api/client.ts`: add `dismiss(id)`, `restore(id)`, `dismissAll(state)`, and an
  `includeDismissed` option on `instances.list`; add `dismissed?: boolean` to the
  instance summary type in `api/types.ts`.
- `QueueView.tsx`: hold a `showDismissed` boolean. When false, render the list as
  today (server already omits dismissed). When true, refetch with
  `includeDismissed=true` and render dismissed rows in place (dimmed). Add the
  global **Show Dismissed / Hide Dismissed** toggle at the bottom of the view.
- `QueueSection.tsx`: for the **Failed** and **Completed** sections only, render a
  **[Dismiss All]** control in the section header (calls `dismissAll(state)` then
  refetches). Active section unchanged.
- `InstanceRow.tsx`: for terminal rows, render a **[x]** dismiss control that
  calls `dismiss(id)` and removes the row from view; when a row is shown because
  `showDismissed` is on, render it **dimmed** with a **↺ restore** control
  (calls `restore(id)`) in place of the [x].
- After any dismiss/restore/dismiss-all, refetch the relevant category (or update
  local state) so counts and ordering stay correct. WebSocket-driven refetches
  continue to respect the current `showDismissed` flag.

## Components & boundaries

| Unit | Responsibility | Depends on |
| --- | --- | --- |
| migration 003 | `instance_dismissals` table | users, instances |
| `dismissal_repo.go` | Persist/query dismissals | generated queries |
| `DismissalRepository` | Domain port | — |
| list endpoint changes | Mark/omit dismissed per user | repo, currentUser |
| dismiss/restore/dismiss-all handlers | Mutations | repo, currentUser |
| engine retry hook | Clear dismissals on retry | repo |
| `client.ts` / Queue components | Controls, toggle, dimming | API |

## Edge cases

- **Dismiss a non-terminal item:** rejected (409); UI only offers [x] on terminal
  rows anyway.
- **Restore something not dismissed:** no-op success (idempotent).
- **Dismiss All on an empty category:** no-op success.
- **Dismiss then the item is rerun:** rerun is a new id, so the old dismissed row
  stays dismissed (historical) and the new run shows normally — consistent with
  "new outcome is new information."
- **Dismiss then retry (same id):** `ClearForInstance` removes the dismissal; the
  item reappears (Active → terminal).
- **Concurrent dismiss of the same item:** PK + `DO NOTHING` makes it idempotent.

## Testing

- Repo tests (sqlite): dismiss is idempotent; restore removes; `DismissedIDs`
  returns the right set per user; `ClearForInstance` removes across users;
  cascade on instance delete.
- Handler tests: dismiss/restore/dismiss-all happy paths and guards (non-terminal
  → 409, bad `state` → 400); list omits dismissed by default and marks+includes
  with `includeDismissed=true`; results scoped to `currentUser`.
- Engine test: retry of a dismissed failed instance clears its dismissal; rerun
  produces an un-dismissed new instance.
- Frontend: toggle reveals/hides dismissed in correct order; [x]/[Dismiss
  All]/↺ call the right endpoints and update the view; Active rows have no [x].

## Out of scope

Multi-user visibility scoping (delivered by Phase 6; this design plugs into it
via the `currentUser` seam) and the deferred workflow-folder system.
