# Live row updates in the Queue view

**Date:** 2026-06-30
**Branch:** `feat/queue-live-row-updates`
**Status:** Approved design

## Problem

The Queue view (`dashboard/src/routes/QueueView.tsx`) connects to the system
WebSocket (`/api/v1/ws`) but only reacts to *instance lifecycle* events
(`instance.created/started/completed/failed/cancelled/paused/resumed/retrying/recovered`).
While an instance is **running**, the only events emitted are node-level
(`node.started` / `node.completed` / `node.failed`) plus `variables.updated`,
and the Queue view filters all of those out (`STATE_CHANGE_EVENTS`,
QueueView.tsx:21). Between "started" and a terminal lifecycle event there are no
refetches, so an Active row's **Step** and **Status** cells sit frozen for the
entire duration of a run — a long pr-review looks dead in the queue even though
it is advancing node by node.

(The **Elapsed** cell is also frozen because it is computed client-side with no
per-second ticker — this is intentionally **out of scope**; see Non-goals.)

## Goal

Active rows reflect the current node **live** as the engine advances, without a
full-list refetch on every node transition.

## Approach: surgical in-place row patch

Patch the affected row's fields directly from the node event, rather than
triggering the existing debounced list refetch. The lifecycle-event refetch is
left untouched and remains the source of truth / reconciliation path.

### Why this over refetch-on-node-events

A refetch approach (just add `node.*` to `STATE_CHANGE_EVENTS`) is fewer lines
but re-issues the 7-call enriched list query on every node transition across all
running instances. The surgical patch is a local state update — no network, no
debounce — and keeps the list query reserved for genuine lifecycle changes.

### The one blocker it must clear

The system WS handler currently **drops** the instance id: `writeWSEvent`
serializes only `engine.Event{type, timestamp, data}` (system_stream_handler.go:88).
`sev.InstanceID` is used server-side for ownership filtering but never reaches
the client, so the frontend cannot map a node event to a row. The design adds it
to the **system** envelope only.

## Changes

### Backend

1. **Enrich `node.started` and `node.failed` event data** at their emit sites in
   `domain/engine/engine.go` (~line 804 for started, ~line 887 for failed). Add
   the friendly label and backend, computed with the **same fallback rules** the
   enricher uses (`enricher.go resolveNodeFields`), so a patched row is identical
   to what a refetch would produce (minus model/retries — see below):
   - `nodeLabel`: `node.Label` if non-empty, else `node.ID`
   - `backend`: `node.Config.Backend` if set (`!= BackendUnset`), else the
     engine's default agent backend

   `node.completed` is intentionally **not** enriched — a completed node is
   immediately superseded by the next `node.started` (or a terminal lifecycle
   event), so patching it would just be churn.

2. **Include `instanceId` in the system-WS envelope only.** In
   `SystemStreamHandler.HandleWebSocket`, write an envelope that carries
   `sev.InstanceID` alongside the event (e.g. `{instanceId, type, timestamp,
   data}`), instead of the bare event. The per-instance `StreamHandler` is left
   unchanged — its client already knows the instance. The new field is purely
   additive, so other `/api/v1/ws` consumers are unaffected.

### Frontend

3. **Patch rows on `node.started` and `node.failed`** in `QueueView`. On either
   event, find the row by `instanceId` in the `active` list and immutably patch:
   - `currentNodeId` ← `data.nodeId`
   - `friendlyStep` ← `data.nodeLabel`
   - `backend` ← `data.backend`

   No refetch, no debounce. If no row matches the `instanceId` (e.g. a
   brand-new run not yet in the list), **ignore** the event — the existing
   `instance.started` debounced refetch brings the run in one cycle later. This
   is an accepted, deliberate lag.

4. **Leave the lifecycle refetch path exactly as-is.** `STATE_CHANGE_EVENTS` and
   the debounced `fetchAll()` remain the reconciliation backstop and the source
   of truth for fields the node event does not carry.

5. **Extend `WebSocketEvent`** (`dashboard/src/api/types.ts`) with an optional
   `instanceId?: string`.

## Known, accepted gaps

- **Model** and **Retries** come from the node-exec aggregate, not the node
  event, so they do **not** patch live; they refresh on the next lifecycle-event
  refetch. Acceptable.
- A just-started run appears one debounce cycle late (via `instance.started`),
  not instantly. Acceptable (user-confirmed).
- **Elapsed** does not tick (no per-second timer). Out of scope (user-confirmed).

## Non-goals

- Per-second Elapsed ticker.
- Live Model / Retries patching.
- Any change to the per-instance stream or the Execution/trace view, which
  already updates live.

## Testing (TDD, three seams)

1. **Engine** (`domain/engine`): `node.started` and `node.failed` events carry
   `nodeLabel` and `backend` matching the enricher's fallback rules (label
   falls back to node id; backend falls back to default).
2. **System stream handler** (`adapter/http`): the JSON written to a system-WS
   client includes `instanceId`; the per-instance handler's payload is
   unchanged.
3. **QueueView** (frontend): a `node.started` / `node.failed` for a listed
   instance patches that row's `currentNodeId` / `friendlyStep` / `backend`
   **without** a refetch; an event for an unlisted instance is ignored; a
   lifecycle event (e.g. `instance.completed`) still triggers the debounced
   refetch.
