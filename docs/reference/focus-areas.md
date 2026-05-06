# Focus Areas Review Tracking

This document tracks the systematic review of the Arbitron Phase 3 Dashboard for correctness, security, and robustness.

Each **single focus area** requires **2 full review passes** before being considered done.
Each **paired focus area** requires **1 full review pass** before being considered done.

A review pass is only checked when the checklist produces a passing result for that area.

**Progression order:** Complete all Pass 1 single area reviews first. Then do paired area reviews (which catch integration issues while the code is fresh). Then do Pass 2 single area reviews.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| 1 | Go Embed & Static Serving (`adapter/dashboard/` — embed directive, SPA fallback, cache headers, catch-all route wiring in `adapter/http/server.go`) | [ ] | [ ] | |
| 2 | System WebSocket & EventBus SubscribeAll (`adapter/eventbus/eventbus.go`, `adapter/http/system_stream_handler.go` — fan-out to all subscribers, system-wide WS endpoint, connection lifecycle) | [ ] | [ ] | |
| 3 | Instance List Since Filter (`domain/instance/instance.go` ListFilters, `adapter/postgres/` query changes, `adapter/http/instance_handler.go` — since param parsing, query integration) | [ ] | [ ] | |
| 4 | Node Position Domain & Persistence (`domain/workflow/workflow.go` NodePosition, `domain/workflow/ports.go` SaveNodePositions, `adapter/filesystem/workflow_repo.go` — position read/write, all-or-nothing rule, field preservation) | [ ] | [ ] | |
| 5 | Workflow Stats Endpoint (`domain/instance/instance.go` WorkflowStats, `adapter/postgres/instance_repo.go` Stats, `adapter/http/workflow_handler.go` Stats handler — aggregation, date range, edge cases) | [ ] | [ ] | |
| 6 | Notification Backend (`domain/notification/`, `adapter/postgres/notification_repo.go`, `adapter/http/notification_handler.go` — CRUD, pagination, mark-read, dismiss, migration) | [ ] | [ ] | |
| 7 | Engine Notification Wiring (`domain/engine/engine.go` — SetNotifications, fire-and-forget creation on completed/failed transitions, error logging) | [ ] | [ ] | |
| 8 | Dashboard Theme & App Shell (`dashboard/src/styles/theme.css`, `dashboard/src/App.tsx`, `dashboard/src/main.tsx` — CSS custom properties, React Router setup, route definitions) | [ ] | [ ] | |
| 9 | NavBar & Navigation (`dashboard/src/components/NavBar.tsx` — active link highlighting, breadcrumb integration, bell placeholder replaced with NotificationBell) | [ ] | [ ] | |
| 10 | API Client & Types (`dashboard/src/api/client.ts`, `dashboard/src/api/types.ts` — all endpoint methods, type definitions match backend contracts, error handling) | [ ] | [ ] | |
| 11 | WebSocket Hooks (`dashboard/src/hooks/useSystemWebSocket.ts`, `dashboard/src/hooks/useInstanceWebSocket.ts` — reconnection, cleanup, event parsing, callback refs) | [ ] | [ ] | |
| 12 | Queue View (`dashboard/src/routes/QueueView.tsx`, `dashboard/src/components/QueueSection.tsx`, `dashboard/src/components/InstanceRow.tsx`, `dashboard/src/components/StateBadge.tsx` — three sections, collapsible, sort, attention flags, live updates) | [ ] | [ ] | |
| 13 | React Flow Graph & Layout (`dashboard/src/graph/` — ArbitronNode component, nodeTypes registry, useGraphLayout hook, dagre config, all-or-nothing position rule, edge labels) | [ ] | [ ] | |
| 14 | Execution View (`dashboard/src/routes/ExecutionView.tsx`, `dashboard/src/components/ResizablePanel.tsx`, `dashboard/src/components/SidePanel.tsx` — graph + side panel, node click, live WS updates, breadcrumb, instance controls) | [ ] | [ ] | |
| 15 | Node Output & Variables Display (`dashboard/src/components/NodeOutput.tsx`, `dashboard/src/components/VariablesPanel.tsx` — script stdout/stderr, LLM conversation, error details, timing, key-value table) | [ ] | [ ] | |
| 16 | Workflow Review Panel (`dashboard/src/routes/WorkflowReview.tsx`, `dashboard/src/components/UsageStats.tsx`, `dashboard/src/components/TriggerModal.tsx` — draggable graph, position auto-save, stats display, trigger form, input schema) | [ ] | [ ] | |
| 17 | Workflows Catalog & Settings (`dashboard/src/routes/WorkflowsCatalog.tsx`, `dashboard/src/routes/Settings.tsx` — list/search, queue config editing, live queue status) | [ ] | [ ] | |
| 18 | Notification Bell & Popover (`dashboard/src/components/NotificationBell.tsx` — badge, popover, mark-read, dismiss, dismissed paging, cross-tab sync via WS) | [ ] | [ ] | |
| 19 | Interactive Callout Placeholder (`dashboard/src/graph/nodes/InteractiveCallout.tsx` — bubble styling, pulsing border, children prop, positioning) | [ ] | [ ] | |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| 20 | System WebSocket + Queue View (live update events flow from EventBus SubscribeAll through system WS to QueueView refetch — event types, reconnection, state transitions move instances between sections) | [ ] | |
| 21 | Instance WebSocket + Execution View (per-instance WS events update node states in graph, side panel output streaming, variable updates — event type handling, cleanup on navigate away) | [ ] | |
| 22 | API Client + Backend Endpoints (all client methods match server routes — paths, HTTP methods, query params, request/response JSON shapes, error codes) | [ ] | |
| 23 | React Flow Graph + Dagre Layout (useGraphLayout produces correct rfNodes/rfEdges, ArbitronNode renders correctly with layout positions, edge labels from workflow definition) | [ ] | |
| 24 | Position Persistence + Review Panel (drag-end saves positions via layout endpoint, auto-save on first view when needsAutoSave is true, positions round-trip through workflow.json) | [ ] | |
| 25 | Notification Backend + Bell Frontend (notification list endpoint returns workflowName, bell fetches unread count, mark-read on popover open, dismiss removes entry, cross-tab sync events) | [ ] | |
| 26 | Go Embed + Vite Build (dashboard dist/ copied to adapter/dashboard/dist/, SPA fallback serves index.html for client routes, API routes take precedence over catch-all) | [ ] | |
| 27 | Workflow Stats + UsageStats Component (stats endpoint returns correct aggregation, frontend date range picker maps to from/to params, success/failure rates computed correctly) | [ ] | |
| 28 | Engine Notifications + Notification Repo (fire-and-forget creation doesn't block workflow, completed/failed transitions create correct notification types, error logging on repo failure) | [ ] | |

---

## Review Guidance

### Single Area Reviews — What To Check

**Pass 1 (Correctness & Coverage):**
- All public functions have test coverage (happy path, failure, error, edge cases)
- The code does what the design doc says it should
- Error handling is thorough — no silent failures
- Module boundaries are clean (no leaking types across boundaries)
- React components have correct TypeScript types and handle loading/error states

**Pass 2 (Robustness & Extensibility):**
- Code aligns to the spirit of the design, not just surface conformance
- The component is ready to be extended by future work (Phase 4 interactive steps, Phase 5 MCP)
- Test assertions test real risks, not just happy-path ceremony
- Edge cases are handled (empty inputs, WebSocket disconnection, missing data, concurrent access)
- CSS uses theme variables consistently, no hardcoded colors

### Paired Area Reviews — What To Check

Paired reviews verify that two components integrate correctly:
- Data flows cleanly across the boundary (Go backend → REST API → React frontend)
- Error propagation works end-to-end
- No assumptions in one component that the other doesn't satisfy
- WebSocket event contracts match between publisher and subscriber
- API request/response shapes are consistent between client and server
