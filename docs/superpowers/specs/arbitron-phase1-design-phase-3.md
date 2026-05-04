# Arbitron Phase 1 — Design Phase 3: API & Observability

## Overview

This design phase covers the REST API handlers, WebSocket streaming, structured logging, and OpenTelemetry tracing. The goal: users can interact with the system via HTTP, observe running workflows in real time, and operators have structured logs and traces for debugging.

---

## 1. REST API Handlers

All handlers live in `adapter/http/`. They are thin adapters that translate between HTTP and domain types. No business logic in handlers.

### Router Setup (`server.go`)

```go
func NewRouter(engine *engine.Engine, workflows workflow.WorkflowRepository, instances instance.InstanceRepository, snapshots instance.VariableSnapshotRepository) chi.Router {
    r := chi.NewRouter()

    r.Use(middleware.Recoverer)
    r.Use(requestLoggingMiddleware)

    r.Route("/api/v1", func(r chi.Router) {
        r.Get("/health", healthHandler)
        r.Get("/system/config", configHandler)

        r.Route("/workflows", func(r chi.Router) {
            r.Get("/", listWorkflows)
            r.Get("/{name}", getWorkflow)
            r.Post("/{name}/validate", validateWorkflow)
            r.Post("/{name}/trigger", triggerWorkflow)
        })

        r.Route("/instances", func(r chi.Router) {
            r.Get("/", listInstances)
            r.Get("/{id}", getInstance)
            r.Post("/{id}/pause", pauseInstance)
            r.Post("/{id}/resume", resumeInstance)
            r.Post("/{id}/cancel", cancelInstance)
            r.Post("/{id}/retry", retryInstance)
            r.Post("/{id}/rerun", rerunInstance)
            r.Get("/{id}/trace", getTrace)
            r.Get("/{id}/trace/{nodeId}", getNodeTrace)
            r.Get("/{id}/variables", getVariables)
            r.Get("/{id}/variables/history", getVariableHistory)
        })
    })

    return r
}
```

### Error Response Format

All error responses use a consistent JSON structure:

```go
type ErrorResponse struct {
    Error string `json:"error"`
    Code  string `json:"code"`
}
```

HTTP status mapping:
- 400 `BAD_REQUEST` — invalid JSON body, missing required fields, type mismatch
- 404 `NOT_FOUND` — workflow or instance doesn't exist
- 409 `CONFLICT` — invalid state transition (e.g. pause a completed instance)
- 500 `INTERNAL_ERROR` — unexpected server errors

A helper function standardizes error responses:

```go
func writeError(w http.ResponseWriter, status int, code string, message string)
```

### Workflow Handlers (`workflow_handler.go`)

**`GET /workflows`:**
- Call `WorkflowRepository.List()`
- Return 200 with JSON array of `{name, description, author}`

**`GET /workflows/{name}`:**
- Extract `name` from URL path
- Call `WorkflowRepository.Load(name)`
- Not found → 404
- Return 200 with the full parsed workflow definition as JSON

**`POST /workflows/{name}/validate`:**
- Extract `name` from URL path
- Call `WorkflowRepository.Load(name)` — not found → 404
- Call `workflow.Validate(wf)` with a `FileReader` for file reference checking
- Return 200 with `{valid: true/false, errors: [...]}`
- Note: always 200, even if invalid. The validation result IS the response.

**`POST /workflows/{name}/trigger`:**
- Extract `name` from URL path
- Parse body: `{variables: {...}, priority: "normal"}`
- Default priority to `normal` if omitted
- Call `Engine.Trigger(name, variables, priority)`
- Validation error → 400
- Not found → 404
- Return 201 with `{instanceId, state: "running"}`

### Instance Handlers (`instance_handler.go`)

**`GET /instances`:**
- Parse query params: `state` (optional), `workflowName` (optional), `limit` (default 50, max 200), `offset` (default 0)
- Call `InstanceRepository.List(filters)`
- Return 200 with JSON array of instance summaries

**`GET /instances/{id}`:**
- Parse UUID from path — invalid UUID → 400
- Call `InstanceRepository.Get(id)` — not found → 404
- Return 200 with full instance details: state, current node, variables, workflow name, timing, priority

**`POST /instances/{id}/pause`:**
- Parse UUID — invalid → 400
- Call `Engine.Pause(id)` — not found → 404, wrong state → 409
- Return 200 with `{state: "paused"}`

**`POST /instances/{id}/resume`:**
- Parse UUID — invalid → 400
- Call `Engine.Resume(id)` — not found → 404, wrong state → 409
- Return 200 with `{state: "running"}`

**`POST /instances/{id}/cancel`:**
- Parse UUID — invalid → 400
- Call `Engine.Cancel(id)` — not found → 404, wrong state → 409
- Return 200 with `{state: "cancelled"}`

**`POST /instances/{id}/retry`:**
- Parse UUID — invalid → 400
- Parse optional body: `{variables: {...}}`
- Call `Engine.Retry(id, variables)` — not found → 404, wrong state → 409
- Return 200 with `{state: "running"}`

**`POST /instances/{id}/rerun`:**
- Parse UUID — invalid → 400
- Call `Engine.Rerun(id)` — not found → 404, wrong state → 409
- Return 201 with `{instanceId: <new ID>, state: "running"}`

**`GET /instances/{id}/trace`:**
- Parse UUID — invalid → 400
- Call `NodeExecutionRepository.ListByInstance(id)` — instance not found → 404
- Return 200 with ordered array of node execution records

**`GET /instances/{id}/trace/{nodeId}`:**
- Parse UUID and nodeId
- Call `NodeExecutionRepository.GetByInstanceAndNode(id, nodeId)` — not found → 404
- Return 200 with single node execution record

**`GET /instances/{id}/variables`:**
- Parse UUID — invalid → 400
- Call `InstanceRepository.Get(id)` — not found → 404
- Return 200 with just the variables JSONB field

**`GET /instances/{id}/variables/history`:**
- Parse UUID — invalid → 400
- Call `SnapshotRepository.ListByInstance(id)` — instance not found → 404
- Return 200 with ordered array of snapshots

---

## 2. WebSocket Streaming

**`adapter/http/stream_handler.go`** handles the `/api/v1/instances/{id}/ws` endpoint.

### Event Bus (`domain/engine/ports.go`)

```go
type EventBus interface {
    Publish(instanceID uuid.UUID, event Event)
    Subscribe(instanceID uuid.UUID) (<-chan Event, func())  // returns channel and unsubscribe func
}

type Event struct {
    Type      string    `json:"type"`
    Timestamp time.Time `json:"timestamp"`
    Data      any       `json:"data"`
}
```

Event types:
- `node_started` — data: `{nodeId, nodeType, label}`
- `node_completed` — data: `{nodeId, nodeType, output, durationMs}`
- `node_failed` — data: `{nodeId, nodeType, error, attempt}`
- `variable_updated` — data: `{variables}` (full variable state)
- `state_changed` — data: `{previousState, newState}`
- `llm_token` — data: `{nodeId, token}`

### In-Process Event Bus Implementation (`adapter/eventbus/eventbus.go`)

```go
type InProcessEventBus struct {
    mu          sync.RWMutex
    subscribers map[uuid.UUID][]chan Event
}
```

- `Publish`: fan out to all subscriber channels for the instance. Non-blocking send — if a channel's buffer is full, drop the event (slow consumers don't block the engine).
- `Subscribe`: create a buffered channel (buffer size 256), add to the subscriber list, return the channel and an unsubscribe function.
- Unsubscribe function: remove the channel from the list, close it.

### WebSocket Handler

Upgrade path: `GET /api/v1/instances/{id}/ws`

1. Parse instance UUID from path
2. Get instance from repository — not found → 404 (before upgrade)
3. Upgrade to WebSocket via `nhooyr.io/websocket`
4. If instance is in a terminal state (completed, failed, cancelled):
   - Send one `state_changed` event with the current state
   - Close the WebSocket
   - Return
5. Subscribe to the event bus for this instance ID
6. Enter a read/write loop:
   - Read from the event channel, JSON-serialize each event, send as a WebSocket text message
   - Monitor for WebSocket close or context cancellation
   - On close/cancel: unsubscribe from event bus, close WebSocket
7. If the instance completes/fails/cancels while connected: the engine publishes a `state_changed` event, the handler sends it, then closes the WebSocket

### LLM Token Streaming

When the engine uses `LLMProvider.StreamCall()`:
1. The provider returns a channel of streaming chunks
2. The engine reads from this channel in the execution loop
3. For each chunk: publish an `llm_token` event to the event bus AND accumulate into the full response buffer
4. When streaming completes: the accumulated buffer becomes the node's output

This means WebSocket subscribers see individual tokens in real time, and the final node execution record has the complete response.

---

## 3. Structured Logging

**`adapter/observability/logging.go`:**

```go
func SetupLogging(cfg config.LoggingConfig) (*slog.Logger, error)
```

1. Parse the log level string into `slog.Level`
2. Create the log file's parent directories if needed
3. Open the log file for append
4. Create two handlers:
   - `slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: level})`
   - `slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})`
5. Wrap in a `MultiHandler` that fans out `Handle()` calls to both
6. Set as the default logger via `slog.SetDefault()`
7. Return the logger (also used for explicit injection where needed)

**`MultiHandler`** implements `slog.Handler`:
- `Enabled`: returns true if ANY child handler is enabled for the level
- `Handle`: calls `Handle` on all children
- `WithAttrs`: returns a new MultiHandler with attrs propagated to all children
- `WithGroup`: returns a new MultiHandler with group propagated to all children

### Request Logging Middleware

A chi middleware that logs every HTTP request:

```go
func RequestLoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler
```

Logs: method, path, status code, duration, request ID (generated UUID per request). Log level: info for 2xx/3xx, warn for 4xx, error for 5xx.

### Engine Logging

The engine and executors log at key points using `slog.With()` for structured fields:
- Instance triggered: info, with instance_id, workflow_name
- Node started: debug, with instance_id, node_id, node_type
- Node completed: info, with instance_id, node_id, duration_ms
- Node failed: warn, with instance_id, node_id, error
- Instance completed: info, with instance_id, total_duration_ms
- Instance failed: warn, with instance_id, failed_node_id, error
- Retry attempt: info, with instance_id, node_id, attempt, max_retries
- Crash recovery: warn, with instance_id, recovered node

---

## 4. OpenTelemetry Tracing

**`adapter/observability/tracing.go`:**

```go
func SetupTracing(cfg config.TracingConfig) (shutdown func(context.Context) error, error)
```

1. If `cfg.Enabled` is false, return a no-op shutdown
2. Create a resource with service attributes:
   - `service.name`: `arbitron`
   - `service.version`: from build info
3. Create the OTLP file exporter (writes to `cfg.ExportDir`)
4. Create a `BatchSpanProcessor` wrapping the exporter
5. Create a `TracerProvider` with the resource and processor
6. Set as global tracer provider via `otel.SetTracerProvider()`
7. Set up the text map propagator for context propagation
8. Return a shutdown function that flushes the batch processor and shuts down the provider

### Engine Tracing

The engine creates spans using `otel.Tracer("arbitron.engine")`:

**Instance span (root):**
- Created when `Run()` starts
- Name: `workflow.execute`
- Attributes: `workflow.name`, `instance.id`, `instance.priority`
- Ended when instance reaches a terminal state or is paused

**Node span (child of instance span):**
- Created when a node starts executing
- Name: `node.execute`
- Attributes: `node.id`, `node.type`, `node.label`
- On success: set status OK, add output size as attribute
- On failure: set status Error, record the error on the span
- On retry: add span events for each retry attempt with attempt number and error
- Ended when node execution completes (including all retries)

### HTTP Tracing

Use the `otelhttp` instrumentation middleware on the chi router to automatically create spans for each HTTP request. This gives request-level traces that can be correlated with engine traces when a trigger request starts a workflow.

---

## 5. Testing Strategy

**HTTP handler tests:**

Use `httptest.NewRecorder()` with mock domain services.

Workflow handlers:
- List workflows: returns JSON array, handles empty workspace
- Get workflow: returns full definition, 404 for missing
- Validate: returns valid=true for good workflow, valid=false with errors for bad, 404 for missing
- Trigger: returns 201 with instance ID, 400 for bad input, 404 for missing workflow

Instance handlers:
- List: returns filtered results, respects limit/offset, handles empty results
- Get: returns full details, 404 for missing, 400 for invalid UUID
- Pause/Resume/Cancel: correct state transitions return 200, wrong state returns 409, missing returns 404
- Retry: 200 with variable edits, 200 without edits, 409 for non-failed
- Rerun: 201 with new instance ID, 409 for non-terminal
- Trace: returns ordered records, 404 for missing instance
- Variables: returns current state, history returns ordered snapshots

Error response format:
- All error responses have consistent `{error, code}` JSON structure
- Status codes match documented mapping (400, 404, 409, 500)

**WebSocket tests:**
- Connect to running instance: receives events as they're published
- Connect to completed instance: receives state event then closes
- Multiple clients: all receive same events
- Client disconnect: unsubscribe is called, no resource leak
- Slow client: events dropped rather than blocking engine
- Invalid instance ID: 404 before upgrade

**Event bus tests:**
- Publish with no subscribers: no error
- Publish with one subscriber: event received
- Publish with multiple subscribers: all receive event
- Subscribe/unsubscribe lifecycle: no events after unsubscribe
- Channel closed after unsubscribe
- Buffer overflow: events dropped, no deadlock

**Logging tests:**
- JSON handler produces valid JSON with expected fields
- Text handler produces readable output
- Log level filtering: debug messages not shown at info level
- MultiHandler fans out to both handlers
- Request logging middleware: logs method, path, status, duration

**Tracing tests:**
- Instance execution produces root span with correct attributes
- Node execution produces child spans
- Failed node sets error status on span
- Retry attempts recorded as span events
- Shutdown flushes pending spans
- Disabled tracing: no spans created, no errors

**Integration tests:**
- Full round-trip: HTTP trigger → engine runs → query trace via HTTP → verify results
- WebSocket: trigger via HTTP, connect WebSocket, verify events stream in real time, verify close on completion
- Logging: verify log file contains expected entries after a workflow run
