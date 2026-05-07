# Arbitron Phase 3 — Design Phase 1: Frontend Foundation, Navigation & Queue View

## Overview

Phase 1 establishes the frontend project, wires it into the Go binary, and delivers the first usable surface: the Queue View home page with live-updating instance sections.

---

## Project Structure

A new `dashboard/` directory at the repo root:

```
dashboard/
  package.json
  tsconfig.json
  vite.config.ts
  index.html
  src/
    main.tsx              # React entry point
    App.tsx               # Root component with router
    routes/
      QueueView.tsx       # Home page
    components/
      NavBar.tsx          # Top navigation bar
      InstanceRow.tsx     # Single instance row in queue sections
      QueueSection.tsx    # Collapsible section (Active/Failed/Completed)
      StateBadge.tsx      # Color-coded state badge
    hooks/
      useWebSocket.ts     # WebSocket connection and event handling
      useInstances.ts     # Instance data fetching and live updates
    api/
      client.ts           # REST API client (fetch-based)
    styles/
      theme.css           # CSS custom properties for dark theme
```

Vite builds to `dashboard/dist/`.

---

## Go Embed & Static Serving

A new `adapter/dashboard/` package:

- Uses `//go:embed dist/*` to bundle the built frontend assets into the Go binary
- Exports a `Handler() http.Handler` function that serves static files from the embedded filesystem
- The chi router registers this handler as a catch-all after all `/api/v1/*` routes: API routes take precedence, everything else serves `index.html` for client-side routing
- Static assets (JS, CSS, images) served with long-lived cache headers (content-hashed filenames from Vite)

During development, the Vite dev server runs on its own port with a proxy to the Go API for hot reload. The embed path only applies to production builds.

---

## Dark Theme

A single dark theme using CSS custom properties. No light mode, no theme toggle.

Color palette:
- Background: dark navy (`#1a1a2e`, `#16162a`, `#1e1e38`)
- Borders: muted (`#2a2a4a`)
- Text: light gray (`#e0e0e0`, `#ccc`, `#888`, `#666`)
- Accent: indigo (`#7c8cf5`) for active nav, links
- State colors: blue (running), green (completed), red (failed), orange (waiting), gray (queued/pending), yellow (high priority)

---

## Navigation Bar

Top nav component on every page:

- **Arbitron** wordmark on the left, links to `/`
- **Queue** link to `/`
- **Workflows** link to `/workflows`
- **Settings** link to `/settings`
- Active page gets a bottom border highlight and accent color
- **Notification bell** on the right: outline SVG icon. In Phase 1, the bell is visible but non-functional (no badge, no popover). Phase 4 wires it up.

---

## Queue View

Route: `/`. The home page. Three collapsible sections in order: Active, Failed, Completed.

### Active Instances Section

- Header: "Active Instances" with count badge
- Shows instances in `running`, `waiting`, `queued`, or `paused` states
- No time limit — all active instances shown
- Independent filter dropdown (by state within the active set) and sort controls (by priority, elapsed time, workflow name)

### Failed Instances Section

- Header: "Failed Instances" with count badge, "last 5 days" label
- Shows instances in `failed` state from the last 5 days
- Time column shows relative time since failure (e.g., "2h ago", "1d ago")
- Independent filter and sort controls

### Completed Instances Section

- Header: "Completed Instances" with count badge, "last 5 days" label
- Shows instances in `completed` state from the last 5 days
- Time column shows relative time since completion
- Independent filter and sort controls

### Instance Row

Each row displays:
- Workflow name
- State badge (color-coded)
- Current node ID (or "—" if not yet started)
- Priority (color-coded for high)
- Time (elapsed for active, relative for terminal states)
- Owner ("local")
- Attention flag (red warning indicator) when the instance has failed or is waiting on an interactive step

Clicking a row navigates to `/instances/:id`.

### Collapsible Sections

Each section header is clickable to collapse/expand. Collapsed state shows just the header with count badge. Expand/collapse state can be local (not persisted across page loads).

---

## Data Fetching

### Initial Load

The queue view fetches instances via the existing REST API:
- `GET /api/v1/instances?state=running&limit=500` (and waiting, queued, paused)
- `GET /api/v1/instances?state=failed&limit=500`
- `GET /api/v1/instances?state=completed&limit=500`

The failed and completed queries need server-side date filtering. This requires adding a `since` query parameter to the existing list endpoint: `GET /api/v1/instances?state=failed&since=2026-05-01T00:00:00Z`.

### Live Updates

A new system-wide WebSocket endpoint: `GET /api/v1/ws`. Unlike the existing per-instance WebSocket (`/instances/:id/ws`), this broadcasts all instance state change events across the system. The queue view connects once and receives events like:

```json
{"type": "instance.state_changed", "data": {"id": "...", "state": "completed", ...}}
```

On receiving an event, the frontend updates the instance's position across sections (e.g., moves from Active to Completed) without refetching.

### Backend Changes

- Add `since` query parameter support to `GET /api/v1/instances` (filters by `created_at >= since`)
- Add `GET /api/v1/ws` system-wide WebSocket endpoint. The existing `EventBus` interface gains a `SubscribeAll() (<-chan Event, func())` method that receives events published for any instance. The engine already publishes events per-instance; the `InProcessEventBus` adapter adds fan-out to all-subscribers in its `Publish` method. The system-wide WebSocket handler subscribes via `SubscribeAll()` and broadcasts to all connected clients.

---

## Client-Side Routing

React Router with these routes (stubs for future phases):

- `/` — Queue View (Phase 1)
- `/instances/:id` — Execution View (Phase 2)
- `/workflows` — Workflows Catalog (Phase 3)
- `/workflows/:name` — Workflow Review Panel (Phase 3)
- `/settings` — Settings Page (Phase 3)

Phases 2–4 render placeholder "coming soon" components until implemented.

---

## Tests

### Frontend

- QueueSection component: renders instances, collapses/expands, shows correct count badge
- InstanceRow component: renders all fields, shows attention flag for failed/interactive states
- StateBadge component: correct color per state
- WebSocket hook: handles connection, reconnection, event parsing
- API client: correct URL construction, query parameter handling

### Backend

- System-wide WebSocket endpoint: connects, receives broadcast events, handles multiple subscribers
- Instance list with `since` parameter: filters correctly, returns empty for future dates
