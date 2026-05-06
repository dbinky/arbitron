# Arbitron Phase 3 — Design Phase 4: Notifications & Interactive Step Callout

## Overview

Phase 4 adds the notification system (backend persistence, API, bell popover with cross-tab sync) and the interactive step callout UI shell (visual only — the conversational interface is a placeholder until product Phase 4).

---

## Database Migration

New migration `008_create_notifications`:

```sql
CREATE TYPE notification_type AS ENUM ('needs_interaction', 'failed', 'completed');

CREATE TABLE notifications (
    id            UUID PRIMARY KEY,
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

The `idx_notifications_active` index supports the popover query pattern: unread non-dismissed first, then dismissed paging. The `idx_notifications_instance` index supports looking up notifications for a specific instance.

---

## Domain Model

New `domain/notification/` package:

```go
type Type string

const (
    TypeNeedsInteraction Type = "needs_interaction"
    TypeFailed           Type = "failed"
    TypeCompleted        Type = "completed"
)

type Notification struct {
    ID         uuid.UUID
    InstanceID uuid.UUID
    Type       Type
    Read       bool
    Dismissed  bool
    CreatedAt  time.Time
    UpdatedAt  time.Time
}
```

### NotificationRepository Port

```go
type NotificationRepository interface {
    Create(ctx context.Context, n *Notification) error
    List(ctx context.Context, filters ListFilters) ([]Notification, error)
    CountUnread(ctx context.Context) (int, error)
    MarkAllRead(ctx context.Context) error
    Dismiss(ctx context.Context, id uuid.UUID) error
}

type ListFilters struct {
    Dismissed *bool
    Limit     int
    Offset    int
}
```

### Postgres Adapter

New file `adapter/postgres/notification_repo.go` implementing `NotificationRepository`. New sqlc queries in `adapter/postgres/queries/notifications.sql`.

---

## Notification Creation

Notifications are created automatically by the engine as a side effect of state transitions. The engine gains a `NotificationRepository` dependency (injected at construction).

### Trigger Points

- **Instance transitions to `failed`** — engine creates a `TypeFailed` notification with the instance ID
- **Instance transitions to `completed`** — engine creates a `TypeCompleted` notification with the instance ID
- **Instance reaches an interactive node** — engine creates a `TypeNeedsInteraction` notification. The interactive node type does not exist yet (product Phase 4), but the hook point is wired: when a future node executor signals "waiting for interaction," the engine creates the notification. For now, this path is untriggered but tested.

Notification creation is fire-and-forget — a failure to create a notification does not fail the workflow. Log the error and continue.

---

## API Endpoints

New handler file: `adapter/http/notification_handler.go`.

### `GET /api/v1/notifications`

Query parameters:
- `dismissed` (boolean, default: false) — filter by dismissed state
- `limit` (int, default: 20, max: 100) — page size
- `offset` (int, default: 0) — pagination offset

Returns notifications ordered by `created_at DESC`. Response:

```json
[
  {
    "id": "...",
    "instanceId": "...",
    "type": "failed",
    "read": false,
    "dismissed": false,
    "createdAt": "2026-05-06T12:00:00Z"
  }
]
```

### `GET /api/v1/notifications/unread-count`

Returns:
```json
{"count": 3}
```

### `POST /api/v1/notifications/mark-read`

Marks all unread notifications as read. No request body. Returns:
```json
{"status": "ok"}
```

### `POST /api/v1/notifications/{id}/dismiss`

Dismisses a single notification. Returns:
```json
{"status": "dismissed"}
```

---

## Cross-Tab Sync

### Event Bus Extension

The system-wide WebSocket endpoint (`GET /api/v1/ws` from Phase 1) already broadcasts instance state changes. It now also broadcasts notification events:

- `notification.created` — when a new notification is created (includes the notification object)
- `notification.read` — when notifications are marked as read
- `notification.dismissed` — when a notification is dismissed (includes the notification ID)

The engine publishes these events on the global event bus channel when notification state changes. All connected WebSocket clients receive them.

### Frontend Handling

Every open tab maintains a WebSocket connection to `GET /api/v1/ws`. When a notification event arrives:
- `notification.created`: add to the popover list, increment badge count
- `notification.read`: clear badge, update red dots on visible notifications
- `notification.dismissed`: remove from active list (or mark as dismissed if the dismissed section is showing)

This keeps all tabs in sync without polling.

---

## Bell Popover Component

### Bell Icon

Outline-style SVG bell in the nav bar. A red badge with the unread count overlays the icon. Badge is hidden when count is zero.

On mount, fetch `GET /api/v1/notifications/unread-count` to initialize the badge. Subsequently, WebSocket events keep it current.

### Popover Behavior

Clicking the bell opens a scrollable popover anchored to the bell icon.

**Active notifications** (non-dismissed) load on open via `GET /api/v1/notifications?dismissed=false&limit=20`.

Each notification entry shows:
- Type icon (warning for failed, checkmark for completed, chat bubble for needs_interaction)
- Workflow name and instance short ID
- Relative timestamp ("2h ago", "1d ago")
- [x] dismiss button on hover/focus

Clicking a notification navigates to `/instances/:instanceId`.

**Mark as read:** Opening the popover fires `POST /api/v1/notifications/mark-read`. Badge clears, red dots on unread entries disappear.

**Dismiss:** Clicking [x] fires `POST /api/v1/notifications/:id/dismiss`. The entry animates out of the list.

### Dismissed Section

At the bottom of the active notification list: a "Show dismissed" link.

Clicking it fetches `GET /api/v1/notifications?dismissed=true&limit=20&offset=0` and appends the results below the link.

Below the dismissed notifications: a "Load more" link. Clicking it fetches the next page (`offset += 20`) and appends. This is manual paging — no infinite scroll.

---

## Interactive Step Callout

A custom React Flow overlay component that renders when a node is in a "waiting for interaction" state.

### Visual Design

- A comic-book-style speech bubble: rounded rectangle with a triangular tail pointing at the target node
- The bubble is semi-transparent with a border matching the node's accent color
- Positioned above or to the side of the node, using React Flow's coordinate system to stay anchored as the graph scrolls or zooms

### Placeholder Content

Inside the bubble: a placeholder terminal component styled as a monospace text area. Displays a static message: "Interactive session — available in a future release." A subtle pulsing border indicates the node is waiting.

### Phase 4 Integration Point

The callout component accepts a `children` prop for its content area. In this phase, the placeholder is passed in. In product Phase 4, a real terminal/conversational interface component replaces it. No structural changes to the callout needed — just a different child component.

---

## Wiring

### main.go Changes

- Create `NotificationRepo` (Postgres adapter)
- Pass it to the engine constructor
- Pass it to the notification HTTP handler
- Register notification routes on the chi router

### Engine Changes

- Add `NotificationRepository` as a constructor dependency
- After `SetFailed`: create a failed notification
- After `SetCompleted`: create a completed notification
- Future: after entering interactive wait state, create a needs_interaction notification

---

## Tests

### Frontend

- Bell icon: badge shows correct count, hidden when zero
- Popover: loads notifications on open, displays entries correctly
- Mark read: opening popover clears badge and red dots
- Dismiss: clicking [x] removes entry, fires API call
- Show dismissed: loads dismissed notifications, "Load more" pages correctly
- Cross-tab: WebSocket events update badge and popover in real time
- Interactive callout: renders bubble anchored to node, shows placeholder content

### Backend

- Notification creation: engine creates notifications on state transitions
- Notification creation is fire-and-forget: does not fail the workflow
- `GET /api/v1/notifications`: correct filtering by dismissed, pagination, ordering
- `GET /api/v1/notifications/unread-count`: correct count
- `POST /api/v1/notifications/mark-read`: all unread marked, returns correct response
- `POST /api/v1/notifications/:id/dismiss`: notification marked dismissed, 404 for unknown ID
- WebSocket broadcast: notification events published on global channel
- Migration: creates table, indexes, enum type correctly
