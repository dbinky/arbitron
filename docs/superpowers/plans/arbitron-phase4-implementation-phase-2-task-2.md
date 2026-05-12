# Phase 2, Task 2: History Hydration & REST Endpoint

## Goal

Send the full conversation history over WebSocket on connection (for instances in interactive wait), add the REST endpoint for ending sessions, and modify the instance detail response to expose interactive state.

## Prerequisites

- Task 1 complete (WebSocket bidirectional messaging works)

## Files to Modify

### Modify: `adapter/http/stream_handler.go`

**History hydration on connection:**

After the WebSocket connection is established and the event bus subscription is set up, before entering the main event loop:

1. Load the instance
2. Check if instance is in `waiting` state
3. If waiting, load the node execution record for the current node
4. Check if the current node type is `interactive` (from workflow snapshot)
5. If interactive: read `llm_conversation` from the node execution record
6. Send an `interactive.history` event over the WebSocket:
   ```json
   {
     "type": "interactive.history",
     "timestamp": "...",
     "data": {
       "nodeId": "<current_node_id>",
       "messages": [...]
     }
   }
   ```
7. Then enter the normal event loop

If the instance is not in interactive wait, skip this step entirely (no history event sent).

### Create or Modify: `adapter/http/interactive_handler.go`

**New handler for the REST endpoint:**

`POST /api/v1/instances/{id}/interact/end`

1. Parse instance ID from URL path
2. Call `conversationHandler.EndSession(ctx, instanceID)`
3. On success: load the node execution record to get the output, return `200 OK` with `{"output": ...}`
4. On "not found" error: return `404`
5. On "invalid state" error: return `409` with descriptive message

### Modify: `adapter/http/server.go` (router)

- Register the new route: `POST /api/v1/instances/{id}/interact/end`
- Pass ConversationHandler to the interactive handler

### Modify: `adapter/http/instance_handler.go`

**Instance detail response enhancement:**

In the `GET /api/v1/instances/{id}` handler response:

1. After loading the instance, check if state is `waiting`
2. If waiting, load the workflow snapshot, find the current node, check if its type is `interactive`
3. Add two fields to the JSON response:
   - `interactiveNodeId` — the current node ID if it's an interactive node, otherwise null
   - `interactiveActive` — `true` if in interactive wait state, otherwise `false`

These are derived fields — no database changes needed.

## Testing

- **History hydration test:** Connect WebSocket to an instance in interactive wait state with existing conversation. Verify `interactive.history` event is the first event received, contains correct nodeId and full message array.

- **No history for non-interactive:** Connect WebSocket to a running (non-interactive) instance. Verify no `interactive.history` event is sent.

- **REST end session test:**
  - POST to `/instances/{id}/interact/end` on a waiting interactive instance → 200 with output
  - POST to `/instances/{id}/interact/end` on a running instance → 409
  - POST to `/instances/nonexistent/interact/end` → 404

- **Instance detail test:**
  - GET instance in interactive wait → response has `interactiveNodeId: "review-design"`, `interactiveActive: true`
  - GET instance in normal running state → response has `interactiveNodeId: null`, `interactiveActive: false`

## Acceptance Criteria

- WebSocket sends conversation history immediately on connection for interactive wait instances
- REST endpoint allows ending sessions with proper status codes
- Instance detail response includes interactive state fields
- All existing instance detail consumers continue to work (new fields are additive)
