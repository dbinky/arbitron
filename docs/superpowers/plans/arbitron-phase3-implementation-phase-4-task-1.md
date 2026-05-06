# Phase 3.4 Task 1: Notification Backend — Migration, Domain, Repository, API

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create the notifications database table, domain model, repository port, Postgres adapter, HTTP endpoints, and wire notification creation into the engine's state transitions.

**Architecture:** New `domain/notification/` package with the domain model and port. New Postgres adapter, sqlc queries, and migration. New HTTP handler. Engine creates notifications as a fire-and-forget side effect on state transitions. Notification events are published on the global event bus for cross-tab sync.

**Tech Stack:** Go, Postgres, sqlc, chi

---

### Task 1.1: Database migration

**Files:**
- Create: `adapter/postgres/migrations/008_create_notifications.up.sql`
- Create: `adapter/postgres/migrations/008_create_notifications.down.sql`

- [ ] **Step 1: Create up migration**

```sql
CREATE TYPE notification_type AS ENUM ('needs_interaction', 'failed', 'completed');

CREATE TABLE notifications (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id   UUID NOT NULL REFERENCES instances(id),
    type          notification_type NOT NULL,
    read          BOOLEAN NOT NULL DEFAULT false,
    dismissed     BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_notifications_active ON notifications (read, dismissed, created_at DESC);
CREATE INDEX idx_notifications_instance ON notifications (instance_id);
```

- [ ] **Step 2: Create down migration**

```sql
DROP TABLE IF EXISTS notifications;
DROP TYPE IF EXISTS notification_type;
```

- [ ] **Step 3: Commit**

```bash
git add adapter/postgres/migrations/008_create_notifications.*
git commit -m "feat: add notifications database migration"
```

---

### Task 1.2: Notification domain model and port

**Files:**
- Create: `domain/notification/notification.go`
- Create: `domain/notification/ports.go`

- [ ] **Step 1: Create the domain model**

Create `domain/notification/notification.go`:

```go
package notification

import (
	"time"

	"github.com/google/uuid"
)

type Type string

const (
	TypeNeedsInteraction Type = "needs_interaction"
	TypeFailed           Type = "failed"
	TypeCompleted        Type = "completed"
)

type Notification struct {
	ID           uuid.UUID
	InstanceID   uuid.UUID
	WorkflowName string
	Type         Type
	Read         bool
	Dismissed    bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
```

- [ ] **Step 2: Create the repository port**

Create `domain/notification/ports.go`:

```go
package notification

import (
	"context"

	"github.com/google/uuid"
)

type ListFilters struct {
	Dismissed *bool
	Limit     int
	Offset    int
}

type Repository interface {
	Create(ctx context.Context, n *Notification) error
	List(ctx context.Context, filters ListFilters) ([]Notification, error)
	CountUnread(ctx context.Context) (int, error)
	MarkAllRead(ctx context.Context) error
	Dismiss(ctx context.Context, id uuid.UUID) error
}
```

- [ ] **Step 3: Commit**

```bash
git add domain/notification/
git commit -m "feat: add notification domain model and repository port"
```

---

### Task 1.3: Postgres notification repository

**Files:**
- Create: `adapter/postgres/queries/notifications.sql`
- Create: `adapter/postgres/notification_repo.go`

- [ ] **Step 1: Create sqlc queries**

```sql
-- name: CreateNotification :one
INSERT INTO notifications (id, instance_id, type)
VALUES ($1, $2, $3)
RETURNING *;

-- name: ListNotifications :many
SELECT n.*, i.workflow_name
FROM notifications n
JOIN instances i ON i.id = n.instance_id
WHERE (sqlc.narg('dismissed')::boolean IS NULL OR n.dismissed = sqlc.narg('dismissed')::boolean)
ORDER BY n.created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountUnreadNotifications :one
SELECT COUNT(*) FROM notifications WHERE read = false AND dismissed = false;

-- name: MarkAllNotificationsRead :exec
UPDATE notifications SET read = true, updated_at = now() WHERE read = false;

-- name: DismissNotification :exec
UPDATE notifications SET dismissed = true, updated_at = now() WHERE id = $1;
```

- [ ] **Step 2: Regenerate sqlc**

```bash
cd adapter/postgres && sqlc generate
```

- [ ] **Step 3: Implement notification_repo.go**

```go
package postgres

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/adapter/postgres/generated"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type NotificationRepo struct {
	pool    *pgxpool.Pool
	queries *generated.Queries
}

func NewNotificationRepo(pool *pgxpool.Pool) *NotificationRepo {
	return &NotificationRepo{
		pool:    pool,
		queries: generated.New(pool),
	}
}

func (r *NotificationRepo) Create(ctx context.Context, n *notification.Notification) error {
	_, err := r.queries.CreateNotification(ctx, generated.CreateNotificationParams{
		ID:         toPgUUID(n.ID),
		InstanceID: toPgUUID(n.InstanceID),
		Type:       generated.NotificationType(n.Type),
	})
	if err != nil {
		return fmt.Errorf("creating notification: %w", err)
	}
	return nil
}

func (r *NotificationRepo) List(ctx context.Context, filters notification.ListFilters) ([]notification.Notification, error) {
	limit := filters.Limit
	if limit == 0 {
		limit = 20
	}

	rows, err := r.queries.ListNotifications(ctx, generated.ListNotificationsParams{
		Dismissed: toPgBoolPtr(filters.Dismissed),
		Limit:     int32(limit),
		Offset:    int32(filters.Offset),
	})
	if err != nil {
		return nil, fmt.Errorf("listing notifications: %w", err)
	}

	result := make([]notification.Notification, len(rows))
	for i, row := range rows {
		result[i] = notification.Notification{
			ID:           fromPgUUID(row.ID),
			InstanceID:   fromPgUUID(row.InstanceID),
			WorkflowName: row.WorkflowName,
			Type:         notification.Type(row.Type),
			Read:         row.Read,
			Dismissed:    row.Dismissed,
			CreatedAt:    fromPgTimestamptz(row.CreatedAt),
			UpdatedAt:    fromPgTimestamptz(row.UpdatedAt),
		}
	}
	return result, nil
}

func (r *NotificationRepo) CountUnread(ctx context.Context) (int, error) {
	count, err := r.queries.CountUnreadNotifications(ctx)
	if err != nil {
		return 0, fmt.Errorf("counting unread: %w", err)
	}
	return int(count), nil
}

func (r *NotificationRepo) MarkAllRead(ctx context.Context) error {
	return r.queries.MarkAllNotificationsRead(ctx)
}

func (r *NotificationRepo) Dismiss(ctx context.Context, id uuid.UUID) error {
	return r.queries.DismissNotification(ctx, toPgUUID(id))
}
```

Note: Helper functions like `toPgBoolPtr` may need to be added to `adapter/postgres/convert.go` if they don't exist.

- [ ] **Step 4: Run tests**

```bash
go test ./adapter/postgres/ -v
```

- [ ] **Step 5: Commit**

```bash
git add adapter/postgres/
git commit -m "feat: implement Postgres notification repository"
```

---

### Task 1.4: Notification HTTP handler

**Files:**
- Create: `adapter/http/notification_handler.go`
- Modify: `adapter/http/server.go`

- [ ] **Step 1: Create notification_handler.go**

```go
package http

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/notification"
)

type NotificationHandler struct {
	repo notification.Repository
}

func NewNotificationHandler(repo notification.Repository) *NotificationHandler {
	return &NotificationHandler{repo: repo}
}

type notificationResponse struct {
	ID           string `json:"id"`
	InstanceID   string `json:"instanceId"`
	WorkflowName string `json:"workflowName"`
	Type         string `json:"type"`
	Read         bool   `json:"read"`
	Dismissed    bool   `json:"dismissed"`
	CreatedAt    string `json:"createdAt"`
}

func toNotificationResponse(n *notification.Notification) notificationResponse {
	return notificationResponse{
		ID:           n.ID.String(),
		InstanceID:   n.InstanceID.String(),
		WorkflowName: n.WorkflowName,
		Type:         string(n.Type),
		Read:         n.Read,
		Dismissed:    n.Dismissed,
		CreatedAt:    n.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func (h *NotificationHandler) List(w http.ResponseWriter, r *http.Request) {
	filters := notification.ListFilters{Limit: 20}

	if d := r.URL.Query().Get("dismissed"); d != "" {
		val := d == "true"
		filters.Dismissed = &val
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			if parsed > 100 {
				parsed = 100
			}
			filters.Limit = parsed
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			filters.Offset = parsed
		}
	}

	notifications, err := h.repo.List(r.Context(), filters)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list notifications")
		return
	}

	resp := make([]notificationResponse, len(notifications))
	for i, n := range notifications {
		resp[i] = toNotificationResponse(&n)
	}
	WriteJSON(w, http.StatusOK, resp)
}

func (h *NotificationHandler) UnreadCount(w http.ResponseWriter, r *http.Request) {
	count, err := h.repo.CountUnread(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to count unread")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]int{"count": count})
}

func (h *NotificationHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	if err := h.repo.MarkAllRead(r.Context()); err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to mark read")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *NotificationHandler) Dismiss(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid notification ID")
		return
	}

	if err := h.repo.Dismiss(r.Context(), id); err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to dismiss")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "dismissed"})
}
```

- [ ] **Step 2: Register routes in server.go**

Add `NotificationHandler` to `NewRouter` params and register routes:

```go
r.Route("/notifications", func(r chi.Router) {
	r.Get("/", notifHandler.List)
	r.Get("/unread-count", notifHandler.UnreadCount)
	r.Post("/mark-read", notifHandler.MarkRead)
	r.Post("/{id}/dismiss", notifHandler.Dismiss)
})
```

- [ ] **Step 3: Run tests**

```bash
go test ./adapter/http/ -v
```

- [ ] **Step 4: Commit**

```bash
git add adapter/http/notification_handler.go adapter/http/server.go
git commit -m "feat: add notification HTTP endpoints"
```

---

### Task 1.5: Wire notifications into engine

**Files:**
- Modify: `domain/engine/engine.go`
- Modify: `cmd/arbitron/main.go`

- [ ] **Step 1: Add NotificationRepository to Engine**

Add to the Engine struct:
```go
notifications notification.Repository
```

Add a setter method:
```go
func (e *Engine) SetNotifications(repo notification.Repository) {
	e.notifications = repo
}
```

- [ ] **Step 2: Create notifications on state transitions**

In the engine's `run` method (or wherever `SetFailed` and `SetCompleted` are called), add notification creation after the state transition succeeds. Use fire-and-forget pattern:

After `SetCompleted`:
```go
if e.notifications != nil {
	n := &notification.Notification{
		ID:         uuid.New(),
		InstanceID: instanceID,
		Type:       notification.TypeCompleted,
	}
	if err := e.notifications.Create(context.Background(), n); err != nil {
		slog.Error("failed to create completion notification", "instanceID", instanceID, "error", err)
	} else {
		e.eventBus.Publish(instanceID, Event{
			Type:      "notification.created",
			Timestamp: time.Now(),
			Data:      map[string]any{"type": "completed", "instanceId": instanceID.String()},
		})
	}
}
```

After `SetFailed`:
```go
if e.notifications != nil {
	n := &notification.Notification{
		ID:         uuid.New(),
		InstanceID: instanceID,
		Type:       notification.TypeFailed,
	}
	if err := e.notifications.Create(context.Background(), n); err != nil {
		slog.Error("failed to create failure notification", "instanceID", instanceID, "error", err)
	} else {
		e.eventBus.Publish(instanceID, Event{
			Type:      "notification.created",
			Timestamp: time.Now(),
			Data:      map[string]any{"type": "failed", "instanceId": instanceID.String()},
		})
	}
}
```

- [ ] **Step 3: Wire in main.go**

In `cmd/arbitron/main.go`:

```go
notificationRepo := postgres.NewNotificationRepo(pool)
eng.SetNotifications(notificationRepo)
```

Create the notification handler and pass to `NewRouter`:

```go
notifHandler := adapthttp.NewNotificationHandler(notificationRepo)
```

- [ ] **Step 4: Run all tests**

```bash
go test ./... -count=1
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add domain/engine/ cmd/arbitron/main.go
git commit -m "feat: wire notification creation into engine state transitions"
```
