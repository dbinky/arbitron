# RBAC Phase 2 — Task 5: Audit Log Infrastructure (migration 011, `domain/audit` port + service, SQLite adapter)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the shared audit-log infrastructure introduced in Phase 2 and reused by Phase 3: a deliberately small `audit_events` table (migration 011), a `domain/audit` port + service whose `Record` reads the actor from context and never fails the underlying operation (best-effort), and a hand-written SQLite `EventRepo`.

**Architecture:** A new bounded context `domain/audit` with an `EventStore` port and an `audit.Service` consumed by callers (Task 6 wires it into `Publish`/`SetVisibility`/the gate). `Record` is best-effort: a store failure is logged via an injected logger and surfaced to observability, but `Record` itself returns nil so audit is never a new failure mode on the write path. The SQLite adapter mirrors the hand-written `InterventionRepo` (no sqlc).

**Tech Stack:** Go, SQLite (hand-written SQL, no sqlc — mirrors `adapter/sqlite/intervention_repo.go`), `github.com/google/uuid`, `log/slog`, testify. Migration embedded by `adapter/sqlite/migrate.go`.

**Cross-phase dependency:** Reads the actor from `identity.FromContext` (exists today). No dependency on Phase 1's new fields. Independent of Tasks 1–4 and may proceed in parallel.

---

## Context you need

- Actor identity: `identity.FromContext(ctx) (identity.Identity, bool)` — `id.UserID` is the actor. When no identity is present (system action), record with a sentinel actor (`"system"`) rather than failing.
- Adapter template: `adapter/sqlite/intervention_repo.go` — hand-written SQL repo, uses `uuidToString`, `parseUUID`, `parseTime`, `ptrToNullString` from `adapter/sqlite/convert.go`. Test DB via `setupTestDB(t)`.
- Migration conventions: `adapter/sqlite/migrations/NNN_name.up.sql` + `.down.sql`, applied in version order. Phase 2 uses `010` (Task 1) then `011` here.
- The design's table shape (design doc §Audit Log):
  ```sql
  CREATE TABLE audit_events (
      id TEXT PRIMARY KEY, occurred_at TEXT NOT NULL, actor_id TEXT NOT NULL,
      action TEXT NOT NULL, target TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '{}'
  );
  ```

## Files

- Create: `adapter/sqlite/migrations/011_audit_log.up.sql`
- Create: `adapter/sqlite/migrations/011_audit_log.down.sql`
- Create: `domain/audit/audit.go` (`Event`, `EventStore` port, action constants)
- Create: `domain/audit/service.go` (`Service` with best-effort `Record`)
- Create: `domain/audit/service_test.go`
- Create: `adapter/sqlite/audit_repo.go` (`EventRepo` implementing `audit.EventStore`)
- Create: `adapter/sqlite/audit_repo_test.go`

---

## Steps

- [ ] **Step 1: Write the failing migration/repo round-trip test**

Create `adapter/sqlite/audit_repo_test.go`:

```go
package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditRepo_AppendThenList(t *testing.T) {
	db := setupTestDB(t)
	repo := NewAuditRepo(db.DB)
	ctx := context.Background()

	ev := audit.Event{
		ID:         uuid.New(),
		OccurredAt: time.Now().UTC(),
		ActorID:    "00000000-0000-0000-0000-000000000001",
		Action:     audit.ActionWorkflowPublish,
		Target:     "triage",
		Detail:     `{"to":"department","department":"engineering"}`,
	}
	require.NoError(t, repo.Append(ctx, ev))

	got, err := repo.ListByActor(ctx, "00000000-0000-0000-0000-000000000001", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, audit.ActionWorkflowPublish, got[0].Action)
	assert.Equal(t, "triage", got[0].Target)
	assert.Equal(t, `{"to":"department","department":"engineering"}`, got[0].Detail)
}

func TestAuditRepo_DefaultDetailIsEmptyObject(t *testing.T) {
	db := setupTestDB(t)
	repo := NewAuditRepo(db.DB)
	ctx := context.Background()

	ev := audit.Event{
		ID: uuid.New(), OccurredAt: time.Now().UTC(),
		ActorID: "actor-x", Action: audit.ActionAdminOverride, Target: "eng-wf",
		// Detail intentionally empty → adapter stores "{}".
	}
	require.NoError(t, repo.Append(ctx, ev))
	got, err := repo.ListByActor(ctx, "actor-x", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "{}", got[0].Detail)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./adapter/sqlite/ -run TestAuditRepo -v`
Expected: FAIL — `domain/audit` and `NewAuditRepo` do not exist.

- [ ] **Step 3: Create the domain port + event type**

Create `domain/audit/audit.go`:

```go
// Package audit defines the consequential-mutation audit trail: a small,
// append-only record of who changed what. Reads are never audited (spec
// §Audit). Introduced in RBAC Phase 2; reused by Phase 3 lead interventions.
package audit

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Action constants name the audited mutations. Kept as exported strings so the
// emitting call sites and the store agree exactly.
const (
	ActionWorkflowPublish          = "workflow.publish"
	ActionWorkflowVisibilityChange = "workflow.visibility_change"
	ActionAdminOverride            = "admin.override"
)

// Event is one audit record. Detail is opaque JSON describing the change
// (e.g. {"from":"department","to":"public","department":"engineering"}).
type Event struct {
	ID         uuid.UUID
	OccurredAt time.Time
	ActorID    string // identity.UserID string, or "system" when no identity
	Action     string
	Target     string // workflow name / instance id
	Detail     string // JSON; "" is normalized to "{}" by the store
}

// EventStore is the append-only persistence port for audit events.
type EventStore interface {
	Append(ctx context.Context, ev Event) error
	// ListByActor returns the most recent events for an actor (newest first),
	// capped at limit. Used by tests and any future audit-view surface.
	ListByActor(ctx context.Context, actorID string, limit int) ([]Event, error)
}
```

- [ ] **Step 4: Write the failing service test**

Create `domain/audit/service_test.go`:

```go
package audit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	events  []audit.Event
	failErr error
}

func (f *fakeStore) Append(_ context.Context, ev audit.Event) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.events = append(f.events, ev)
	return nil
}
func (f *fakeStore) ListByActor(_ context.Context, _ string, _ int) ([]audit.Event, error) {
	return f.events, nil
}

func TestService_Record_StampsActorFromContext(t *testing.T) {
	store := &fakeStore{}
	svc := audit.NewService(store)
	user := uuid.New()
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: user, Role: identity.RoleUser})

	svc.Record(ctx, audit.ActionWorkflowPublish, "triage", `{"to":"public"}`)

	require.Len(t, store.events, 1)
	assert.Equal(t, user.String(), store.events[0].ActorID)
	assert.Equal(t, audit.ActionWorkflowPublish, store.events[0].Action)
	assert.Equal(t, "triage", store.events[0].Target)
	assert.False(t, store.events[0].OccurredAt.IsZero())
	assert.NotEqual(t, uuid.Nil, store.events[0].ID)
}

func TestService_Record_NoIdentity_UsesSystemActor(t *testing.T) {
	store := &fakeStore{}
	svc := audit.NewService(store)
	svc.Record(context.Background(), audit.ActionAdminOverride, "x", "")
	require.Len(t, store.events, 1)
	assert.Equal(t, "system", store.events[0].ActorID)
}

func TestService_Record_StoreFailure_DoesNotPanicOrReturn(t *testing.T) {
	store := &fakeStore{failErr: errors.New("db down")}
	svc := audit.NewService(store)
	// Best-effort: Record must NOT propagate the failure (no return value).
	svc.Record(context.Background(), audit.ActionWorkflowPublish, "x", "")
	assert.Empty(t, store.events)
}

func TestService_Record_NilStore_IsNoop(t *testing.T) {
	var svc *audit.Service // nil-safe
	assert.NotPanics(t, func() {
		svc.Record(context.Background(), audit.ActionWorkflowPublish, "x", "")
	})
}
```

- [ ] **Step 5: Run to verify the service test fails**

Run: `go test ./domain/audit/ -run TestService_Record -v`
Expected: FAIL — `audit.NewService`/`Service.Record` undefined.

- [ ] **Step 6: Implement the service**

Create `domain/audit/service.go`:

```go
package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// systemActor is the actor id used when no identity is present (scheduler /
// webhook / startup actions).
const systemActor = "system"

// Service records consequential mutations best-effort. A store failure is
// logged and dropped — auditing must never turn a successful operation into a
// failed one (design doc §Audit Log).
type Service struct {
	store  EventStore
	logger *slog.Logger
	now    func() time.Time // injectable for tests; defaults to time.Now
}

// NewService builds an audit Service over store. A nil store yields a nil-safe
// no-op Service (local/unconfigured deployments still call Record harmlessly).
func NewService(store EventStore) *Service {
	return &Service{store: store, logger: slog.Default(), now: time.Now}
}

// SetLogger overrides the failure logger (optional).
func (s *Service) SetLogger(l *slog.Logger) {
	if s != nil && l != nil {
		s.logger = l
	}
}

// Record appends an audit event, reading the actor from ctx. It never returns
// an error: a nil Service, a nil store, or an Append failure are all swallowed
// (the latter logged at WARN). detail must be valid JSON or ""; "" becomes "{}".
func (s *Service) Record(ctx context.Context, action, target, detail string) {
	if s == nil || s.store == nil {
		return
	}
	actor := systemActor
	if id, ok := identity.FromContext(ctx); ok {
		actor = id.UserID.String()
	}
	ev := Event{
		ID:         uuid.New(),
		OccurredAt: s.now().UTC(),
		ActorID:    actor,
		Action:     action,
		Target:     target,
		Detail:     detail,
	}
	if err := s.store.Append(ctx, ev); err != nil {
		s.logger.Warn("audit append failed (operation already succeeded)",
			"action", action, "target", target, "error", err)
	}
}
```

- [ ] **Step 7: Run to verify the service tests pass**

Run: `go test ./domain/audit/ -v`
Expected: PASS.

- [ ] **Step 8: Create migration 011**

Create `adapter/sqlite/migrations/011_audit_log.up.sql`:

```sql
-- audit_events: append-only record of consequential mutations (workflow
-- publish/visibility changes, admin overrides, and — in Phase 3 — lead
-- interventions). Reads are never audited (spec §Audit). Deliberately small.
CREATE TABLE IF NOT EXISTS audit_events (
    id          TEXT PRIMARY KEY,
    occurred_at TEXT NOT NULL,
    actor_id    TEXT NOT NULL,
    action      TEXT NOT NULL,   -- workflow.publish | workflow.visibility_change | admin.override
    target      TEXT NOT NULL,   -- workflow name / instance id
    detail      TEXT NOT NULL DEFAULT '{}'  -- JSON: {from,to,department,...}
);

CREATE INDEX IF NOT EXISTS idx_audit_occurred ON audit_events(occurred_at);
CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_events(actor_id);
```

Create `adapter/sqlite/migrations/011_audit_log.down.sql`:

```sql
DROP INDEX IF EXISTS idx_audit_actor;
DROP INDEX IF EXISTS idx_audit_occurred;
DROP TABLE IF EXISTS audit_events;
```

- [ ] **Step 9: Implement the SQLite repo**

Create `adapter/sqlite/audit_repo.go` (model on `intervention_repo.go`):

```go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/domain/audit"
)

// Compile-time: satisfies the audit event store port.
var _ audit.EventStore = (*AuditRepo)(nil)

// AuditRepo implements audit.EventStore over SQLite. Hand-written SQL (no sqlc),
// modeled on InterventionRepo.
type AuditRepo struct {
	db *sql.DB
}

// NewAuditRepo creates an AuditRepo using the given database connection.
func NewAuditRepo(db *sql.DB) *AuditRepo {
	return &AuditRepo{db: db}
}

func (r *AuditRepo) Append(ctx context.Context, ev audit.Event) error {
	detail := ev.Detail
	if detail == "" {
		detail = "{}"
	}
	const q = `INSERT INTO audit_events (id, occurred_at, actor_id, action, target, detail)
	           VALUES (?, ?, ?, ?, ?, ?)`
	_, err := r.db.ExecContext(ctx, q,
		uuidToString(ev.ID), timeToString(ev.OccurredAt), ev.ActorID, ev.Action, ev.Target, detail)
	if err != nil {
		return fmt.Errorf("appending audit event: %w", err)
	}
	return nil
}

func (r *AuditRepo) ListByActor(ctx context.Context, actorID string, limit int) ([]audit.Event, error) {
	const q = `SELECT id, occurred_at, actor_id, action, target, detail
	           FROM audit_events WHERE actor_id = ? ORDER BY occurred_at DESC LIMIT ?`
	rows, err := r.db.QueryContext(ctx, q, actorID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing audit events: %w", err)
	}
	defer rows.Close()

	out := make([]audit.Event, 0)
	for rows.Next() {
		var idStr, occurredStr, actor, action, target, detail string
		if err := rows.Scan(&idStr, &occurredStr, &actor, &action, &target, &detail); err != nil {
			return nil, fmt.Errorf("scanning audit row: %w", err)
		}
		id, err := parseUUID(idStr)
		if err != nil {
			return nil, fmt.Errorf("parsing audit id: %w", err)
		}
		occurred, err := parseTime(occurredStr)
		if err != nil {
			return nil, fmt.Errorf("parsing audit occurred_at: %w", err)
		}
		out = append(out, audit.Event{
			ID: id, OccurredAt: occurred, ActorID: actor,
			Action: action, Target: target, Detail: detail,
		})
	}
	return out, rows.Err()
}
```

Verify `timeToString` exists in `adapter/sqlite/convert.go` (it is referenced by `timeToNullString`). If only an unexported `timeToString` exists, use it; if not, format with `ev.OccurredAt.UTC().Format(time.RFC3339)` and add the `time` import.

- [ ] **Step 10: Run the adapter tests**

Run: `go test ./adapter/sqlite/ -run TestAuditRepo -v`
Expected: PASS.

- [ ] **Step 11: Run the full suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 12: Commit**

```bash
git add adapter/sqlite/migrations/011_audit_log.up.sql \
        adapter/sqlite/migrations/011_audit_log.down.sql \
        domain/audit/audit.go domain/audit/service.go domain/audit/service_test.go \
        adapter/sqlite/audit_repo.go adapter/sqlite/audit_repo_test.go
git commit -m "feat(rbac): audit log infrastructure (migration 011, domain/audit, EventRepo)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Dependencies

- **Depends on:** nothing. Independent of Tasks 1–4; may run in parallel.
- **Blocks:** Task 6 (audit event emission wires this `Service` into the workflow service).
