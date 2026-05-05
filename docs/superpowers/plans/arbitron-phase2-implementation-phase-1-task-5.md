# Phase 1, Task 5: API Changes (Priority Endpoint, Trigger 201/202, System Config Update, Queue-Ordered Listing)

## Objective
Add the priority mutation endpoint, modify the trigger endpoint to return 201/202 based on queue state, add the system config queue endpoint, and update instance listing to support queue ordering.

## Dependencies
- Phase 1, Task 1 (domain model)
- Phase 1, Task 3 (Queue Manager)
- Phase 1, Task 4 (Engine with new Trigger signature)

## Files to Create/Modify
- `adapter/http/instance_handler.go` — add priority endpoint handler, update trigger response, update instance response with `queuedAt`
- `adapter/http/system_handler.go` — add queue config GET/PUT handlers (or modify existing system config handler)
- `adapter/http/router.go` — register new routes
- `adapter/http/instance_handler_test.go` — tests for new/modified endpoints
- `adapter/http/system_handler_test.go` — tests for config endpoint

## Implementation Steps

1. **Add `queuedAt` to instanceResponse** in `adapter/http/instance_handler.go`:
   ```go
   type instanceResponse struct {
       // ... existing fields ...
       QueuedAt *string `json:"queuedAt,omitempty"`
   }
   ```
   Update `toInstanceResponse` to format `inst.QueuedAt` as ISO 8601 string.

2. **Modify trigger handler** to use new Trigger signature:
   ```go
   id, started, err := h.engine.Trigger(name, req.Variables, req.Priority)
   ```
   Return `http.StatusCreated` (201) when `started == true`, `http.StatusAccepted` (202) when `started == false`. Response body always includes the instance.

3. **Update InstanceService interface** to add priority mutation:
   ```go
   type InstanceService interface {
       Pause(id uuid.UUID) error
       Resume(id uuid.UUID) error
       Cancel(id uuid.UUID) error
       Retry(id uuid.UUID, variableEdits map[string]any) error
       Rerun(id uuid.UUID) (uuid.UUID, error)
       SetPriority(id uuid.UUID, priority string) error  // new
   }
   ```

4. **Implement SetPriority in Engine**:
   ```go
   func (e *Engine) SetPriority(id uuid.UUID, priority string) error {
       ctx := context.Background()
       inst, err := e.instances.Get(ctx, id)
       if err != nil { return err }
       if inst.State != instance.StateQueued && inst.State != instance.StateWaiting {
           return instance.ErrNotQueued // only queued/waiting instances can change priority
       }
       return e.instances.UpdatePriority(ctx, id, instance.Priority(priority))
   }
   ```
   Add `UpdatePriority(ctx, id, priority)` to `InstanceRepository` interface and implement in postgres adapter.

5. **Add priority endpoint handler**:
   ```go
   func (h *InstanceHandler) SetPriority(w http.ResponseWriter, r *http.Request) {
       id, err := uuid.Parse(chi.URLParam(r, "id"))
       // parse request body: {"priority": "high|normal|low"}
       // validate priority value
       // call h.service.SetPriority(id, priority)
       // 200 on success, 409 if not queued/waiting, 400 for invalid priority
   }
   ```

6. **Add system config queue endpoint** — create or extend `adapter/http/system_handler.go`:
   ```go
   type SystemHandler struct {
       config       *config.Config
       queueManager QueueManagerForAPI  // interface with UpdateConfig + Evaluate
       queueRepo    instance.QueueRepository
   }

   // GET /system/config — returns full config including queue section with counts
   func (h *SystemHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
       running, _ := h.queueRepo.CountRunning(r.Context())
       queued, _ := h.queueRepo.CountQueued(r.Context())
       waiting, _ := h.queueRepo.CountWaiting(r.Context())
       resp := map[string]any{
           // ... existing config fields ...
           "queue": map[string]any{
               "globalCap":    h.config.Queue.GlobalCap,
               "perUserCap":   h.config.Queue.PerUserCap,
               "runningCount": running,
               "queuedCount":  queued,
               "waitingCount": waiting,
           },
       }
       // write JSON response
   }

   // PUT /system/config/queue — update caps at runtime
   func (h *SystemHandler) UpdateQueueConfig(w http.ResponseWriter, r *http.Request) {
       // parse body: {"globalCap": int, "perUserCap": int}
       // update config in memory
       // if cap was raised, trigger queue evaluation
       // persist to config file
       // return 200 with updated config
   }
   ```

7. **Update instance listing** — when `state=queued` filter is applied, order by priority (high first) then `queued_at` ASC. Modify the sqlc `ListInstances` query or add a new `ListQueued` query:
   ```sql
   -- name: ListInstancesQueued :many
   SELECT * FROM instances
   WHERE state = 'queued'
   ORDER BY
     CASE priority WHEN 'high' THEN 1 WHEN 'normal' THEN 2 WHEN 'low' THEN 3 END,
     queued_at ASC
   LIMIT $1 OFFSET $2;
   ```

8. **Register routes** in `adapter/http/router.go`:
   ```go
   r.Post("/instances/{id}/priority", instanceHandler.SetPriority)
   r.Get("/system/config", systemHandler.GetConfig)
   r.Put("/system/config/queue", systemHandler.UpdateQueueConfig)
   ```

## Tests
- `adapter/http/instance_handler_test.go`:
  - `TestTrigger_Returns201_WhenStartedImmediately`
  - `TestTrigger_Returns202_WhenQueued`
  - `TestSetPriority_Success_QueuedInstance`
  - `TestSetPriority_409_RunningInstance`
  - `TestSetPriority_400_InvalidPriority`
  - `TestListInstances_QueueOrder` — queued instances returned in priority+FIFO order
  - `TestGetInstance_IncludesQueuedAt`
- `adapter/http/system_handler_test.go`:
  - `TestGetConfig_IncludesQueueSection`
  - `TestUpdateQueueConfig_Success`
  - `TestUpdateQueueConfig_RaisedCap_TriggersEvaluation`

## Acceptance Criteria
- `POST /instances/{id}/priority` returns 200 for queued/waiting instances, 409 otherwise
- `POST /workflows/{name}/trigger` returns 201 when started, 202 when queued
- `GET /system/config` includes queue section with caps and counts
- `PUT /system/config/queue` updates caps and triggers evaluation when raised
- `GET /instances?state=queued` returns instances in queue order
- Instance response includes `queuedAt` field
- All API tests pass
