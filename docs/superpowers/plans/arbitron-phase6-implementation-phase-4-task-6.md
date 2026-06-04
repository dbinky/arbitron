# Phase 4, Task 6: Attribution — `instance_interventions` audit (migration 007), repo, attribution writes, `instance.intervened` event, REST + MCP exposure

## Goal (Source of truth: design §5 "Attribution")

When an admin acts on an instance they do **not** own, record the action so the owner and other admins can see who intervened. Owner self-actions are not noise-logged. Specifically:

- Migration `007_interventions.up.sql` adds an `instance_interventions` audit table: `id`, `instance_id`, `actor_user_id`, `action`, `detail` (nullable), `created_at`.
- The instance/engine service writes an intervention row whenever the **acting identity differs from the instance owner**, for every gated operation (pause/resume/cancel/set_priority/retry/rerun) and for admin interactive participation.
- Expose REST `GET /api/v1/instances/{id}/interventions` (visible to owner + admins, scoped via Task 2), fold the intervention list into the trace surface, and publish an `instance.intervened` event on the bus so live viewers see it. Admin interactive participation is attributed on the `interactive.message`/`interactive.ended` events (actor recorded).

## Dependencies

- Task 5 (intervention gate + `ctx` threaded through the intervention methods; `authorizeIntervention` returns the loaded instance carrying `OwnerID`).
- Task 4 (interactive gate in `loadAndValidate`).
- Task 2 (`canView` for scoping the intervention-list read).
- Phase 1 identity (`identity.FromContext` gives `actor_user_id`). Note migration numbering: Phase 1 owns `003_auth`; Phase 4 owns `007_interventions` (the design reserves 007 for this phase; 004–006 belong to the intervening Phase 6 sub-phases).

## Background (current state)

- Migrations live in `adapter/sqlite/migrations/NNN_name.{up,down}.sql`, applied in lexical order by `RunMigrations` (`migrate.go`), which parses the leading integer as the version. The repo currently ships `001_initial_schema` and `002_connections`; the design's higher numbers (003 auth, 007 interventions) land as those phases are implemented. The `instances` table is the FK target (`002_connections.up.sql` shows the `CREATE TABLE IF NOT EXISTS` + `strftime` timestamp idiom).
- `NotificationRepo` (`adapter/sqlite/notification_repo.go`) is the template for a hand-written-SQL repo (no sqlc): `db *sql.DB`, `ExecContext` inserts, `QueryContext` lists, `uuidToString`/`parseTime` from `convert.go`.
- `engine.EventBus.Publish(instanceID, Event)` (`ports.go:213`) is how live events reach the per-instance and system streams; `Event{Type, Timestamp, Data any}`.
- The intervention methods (post-Task 5) hold the loaded instance (`OwnerID`) and a `ctx` carrying the actor identity — exactly what an attribution write needs.
- `publishInteractiveMessage` (`conversation.go:77`) defines the `interactive.message` wire shape (role/content); the `interactive.ended` event is published in `completeSession` (`conversation.go:251`). These are the attribution points for admin participation.

## Files to Create/Modify

### Create: `adapter/sqlite/migrations/007_interventions.up.sql`

```sql
-- instance_interventions: audit trail of actions taken on an instance by an
-- actor who is NOT the instance owner (admin intervention). Owner self-actions
-- are intentionally not recorded. Visible to the owner and to admins.
CREATE TABLE IF NOT EXISTS instance_interventions (
    id            TEXT PRIMARY KEY,
    instance_id   TEXT NOT NULL,
    actor_user_id TEXT NOT NULL,
    action        TEXT NOT NULL,            -- pause|resume|cancel|set_priority|retry|rerun|interactive.message|interactive.ended
    detail        TEXT,                     -- nullable; e.g. new priority value, end reason
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    FOREIGN KEY (instance_id) REFERENCES instances(id)
);

CREATE INDEX IF NOT EXISTS idx_instance_interventions_instance
    ON instance_interventions (instance_id, created_at);
```

### Create: `adapter/sqlite/migrations/007_interventions.down.sql`

```sql
DROP INDEX IF EXISTS idx_instance_interventions_instance;
DROP TABLE IF EXISTS instance_interventions;
```

### Create: domain type + port — `domain/instance/intervention.go` (or extend `instance.go` + `ports.go`)

```go
// Intervention records a non-owner action on an instance for attribution.
type Intervention struct {
	ID          uuid.UUID
	InstanceID  uuid.UUID
	ActorUserID uuid.UUID
	Action      string
	Detail      *string
	CreatedAt   time.Time
}

// InterventionRepository persists and queries the intervention audit trail.
type InterventionRepository interface {
	Create(ctx context.Context, iv *Intervention) error
	ListByInstance(ctx context.Context, instanceID uuid.UUID) ([]Intervention, error)
}
```

Add `Action` constants (`ActionPause = "pause"`, etc.) so the engine and the interactive handler reference the same strings.

### Create: `adapter/sqlite/intervention_repo.go`

Hand-written-SQL repo modeled on `NotificationRepo`:

```go
type InterventionRepo struct{ db *sql.DB }

func NewInterventionRepo(db *sql.DB) *InterventionRepo { return &InterventionRepo{db: db} }

func (r *InterventionRepo) Create(ctx context.Context, iv *instance.Intervention) error {
	const q = `INSERT INTO instance_interventions (id, instance_id, actor_user_id, action, detail) VALUES (?, ?, ?, ?, ?)`
	_, err := r.db.ExecContext(ctx, q,
		uuidToString(iv.ID), uuidToString(iv.InstanceID), uuidToString(iv.ActorUserID),
		iv.Action, ptrToNullString(iv.Detail))
	if err != nil {
		return fmt.Errorf("creating intervention: %w", err)
	}
	return nil
}

func (r *InterventionRepo) ListByInstance(ctx context.Context, instanceID uuid.UUID) ([]instance.Intervention, error) {
	const q = `SELECT id, instance_id, actor_user_id, action, detail, created_at
	           FROM instance_interventions WHERE instance_id = ? ORDER BY created_at ASC`
	// scan rows; nullStringToPtr(detail); parseTime(created_at) — mirror NotificationRepo.List
}
```

Wire `NewInterventionRepo(db)` in `cmd/arbitron/main.go` and inject it into the engine via a setter (mirror `SetNotifications`).

### Modify: `domain/engine/engine.go` — record attribution on intervention

Add the repo as an engine field with `SetInterventions(repo instance.InterventionRepository)` (nil-safe: no record when unset, like `emitNotification`). Add a helper that records + publishes only when the actor differs from the owner:

```go
// recordIntervention writes an audit row and publishes instance.intervened when
// the acting identity is NOT the instance owner. Owner self-actions are skipped.
func (e *Engine) recordIntervention(ctx context.Context, inst *instance.Instance, action string, detail *string) {
	id, ok := identity.FromContext(ctx)
	if !ok || id.UserID == inst.OwnerID {
		return // owner self-action (or no identity): not logged
	}
	if e.interventions != nil {
		iv := &instance.Intervention{
			ID: uuid.New(), InstanceID: inst.ID, ActorUserID: id.UserID,
			Action: action, Detail: detail, CreatedAt: time.Now(),
		}
		if err := e.interventions.Create(ctx, iv); err != nil {
			slog.Error("failed to record intervention", "instanceID", inst.ID, "error", err)
		}
	}
	e.eventBus.Publish(inst.ID, Event{
		Type:      "instance.intervened",
		Timestamp: time.Now(),
		Data:      map[string]any{"actorUserId": id.UserID.String(), "action": action},
	})
}
```

Call `recordIntervention` at the **end of the successful path** of each gated method (after the state transition committed), using the `inst` returned by `authorizeIntervention`. For `SetPriority` pass `detail = &priority`; for `Rerun` record on the **source** instance with `detail = &newID` so the owner sees their instance was rerun by an admin (the new instance is owned by the actor and needs no attribution). Recording after success avoids logging a refused/failed action.

### Modify: `domain/engine/conversation.go` — attribute admin interactive participation

In `loadAndValidate` (or its callers `HandleMessage`/`EndSession`), after the ownership gate passes, if `identity.FromContext(ctx).UserID != inst.OwnerID` (admin), record an intervention and tag the published `interactive.message`/`interactive.ended` events with `actorUserId`. Thread the actor into `publishInteractiveMessage`/`completeSession`:

- `HandleMessage`: on the user-message publish, add `"actorUserId"` to the event data when the actor is not the owner, and write an `interactive.message` intervention row.
- `EndSession`/`completeSession`: add `"actorUserId"` to the `interactive.ended` event and write an `interactive.ended` intervention row when the actor is not the owner.

The conversation handler needs the `InterventionRepository` injected (constructor param or setter). Keep it nil-safe.

### Modify: REST — `GET /api/v1/instances/{id}/interventions` + fold into trace

- Add an engine service read `GetInterventions(ctx, id) ([]instance.Intervention, error)` that authorizes via `authorizeView` (Task 2): owner + admins only; non-viewer → `ErrNotFound`.
- Add a handler `InstanceHandler.GetInterventions` returning a JSON list (`id, instanceId, actorUserId, action, detail, createdAt`), mapping `ErrNotFound` → 404.
- Register the route in `server.go` inside the `/instances` group: `r.Get("/{id}/interventions", instHandler.GetInterventions)`.
- Fold into trace: extend `GetTrace`'s response (or add the interventions array alongside the node-execution array) so the trace view shows interventions inline. Minimal: include an `interventions` field in the trace response payload; the engine `GetTrace` already authorizes, so reuse it to also load interventions.

### Modify: MCP — `trace_interventions` read tool

Add the `trace_interventions` read tool (this is the FINAL name — it lives in `adapter/mcp/tools_trace.go` alongside `trace_get`/`trace_get_node`, consistent with the design folding interventions into the trace surface; do NOT name it `instance_interventions`) calling `srv.deps.Engine.GetInterventions(ctx, id)` — scoped identically (owner + admins). Add `GetInterventions` to the MCP `EngineService` interface in `deps.go`. (No collision with existing tools: current tools are `trace_get`/`trace_get_node`; `trace_interventions` is new.)

## Testing (strict TDD — five scenario categories)

Engine attribution unit (`domain/engine/engine_intervention_attr_test.go`) with a fake `InterventionRepository` recording created rows + a fake event bus:

- **Happy path:** admin pauses user A's instance → exactly **one** intervention row `{actor=admin, action="pause", instance=A}` and one `instance.intervened` event published.
- **Validation:** `SetPriority` by an admin records `detail = "high"` (the new priority); `Rerun` by an admin records on the source instance with `detail = newID`.
- **Error/failure:** intervention repo `Create` errors → the operation still **succeeds** (attribution is best-effort, logged not fatal); assert the state transition committed and the error was logged, not returned.
- **Boundary:** owner acts on **own** instance → **zero** intervention rows and no `instance.intervened` event (self-actions not logged); repo `Create` never called.
- **Isolation/security:** the intervention list is visible to the owner and to admins but **not** to an unrelated non-admin user: `GetInterventions` returns rows for owner/admin and `ErrNotFound` for a third user (existence not leaked). Add a SQLite repo test (`adapter/sqlite/intervention_repo_test.go`): round-trip create→list ordered by `created_at`, nullable `detail` preserved, FK to a real instance. Add a migration test asserting `007` applies and the table/index exist. Add REST/MCP tests: `GET /instances/{id}/interventions` returns the admin's pause for the owner (200) and 404 for a stranger; the MCP tool mirrors it.

### Cross-cutting MCP-parity assertion (design §3 / §6)

Add an integration test (alongside the MCP suite) asserting that for a given non-admin identity, an MCP `instance_list` call returns the **same scoped set** as the REST `GET /api/v1/instances` endpoint — proving both surfaces inherit the domain-service scoping from Tasks 1–2 rather than re-implementing it.
