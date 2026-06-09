# Final Org Gaps — Phase 1 Design: Schedule "Run Now"

Implements **Capability A** of `docs/specs/final-org-gaps-spec.md`: fire a defined
schedule immediately, on demand, over REST + MCP, reusing the schedule's
variables and creator attribution, with caller-controlled collision behavior
(default: fire anyway; opt-in skip-when-busy).

## Context (existing code)

- `domain/schedule/service.go` — `Service` is the single authz+validation path
  shared by REST and MCP. It owns `Create/Get/List/Update/SetEnabled/Delete`,
  the `caller()` identity read, and `authorize()` (owner-or-admin). It has **no**
  firing dependencies today.
- `domain/schedule/evaluator.go` — `Evaluator.fireOne` already performs the fire
  core: resolve creator identity via `IdentityResolver.IdentityForUser`, inject
  it with `identity.WithIdentity(ctx, id)`, call
  `Trigger.TriggerWithDepartment(ctx, workflow, vars, "normal", dept)`. It then
  does cron-specific work: alerting on creator-gone / deleted-workflow, and
  `SetLastFired` to advance the cron position.
- `domain/schedule/evaluator.go` — `ActiveRunChecker.HasActiveRun(ctx, workflow,
  ownerID)` is the busy check used by the collision policy.
- `adapter/http/schedule_handler.go` and `adapter/mcp/tools_schedule.go` are dumb
  translators over `Service`.

## Design

### New collaborator: `Runner` (the shared fire core)

Extract the identity-resolve + trigger core into a small, testable collaborator
so both the cron loop and run-now share exactly one fire path.

```go
// domain/schedule/runner.go
type Runner struct {
    trigger Trigger
    idents  IdentityResolver
    active  ActiveRunChecker
}

func NewRunner(t Trigger, i IdentityResolver, a ActiveRunChecker) *Runner

// Fire resolves the schedule's creator, optionally enforces skip-when-busy,
// injects the creator identity, and triggers the workflow. It NEVER advances
// LastFiredAt (run-now is out of cadence). Returns the new instance id and
// fired=false when skipped because busy.
func (r *Runner) Fire(ctx context.Context, s Schedule, skipWhenBusy bool) (instanceID uuid.UUID, fired bool, err error)
```

`Fire` semantics:
1. If `skipWhenBusy` and `active.HasActiveRun(ctx, s.WorkflowName, s.CreatorID)`
   is true → return `(uuid.Nil, false, nil)` (skipped, not an error).
2. `id, err := idents.IdentityForUser(ctx, s.CreatorID)` → on error return a
   wrapped `ErrCreatorUnresolved` (maps to 409/conflict at the seam: the run
   cannot be attributed).
3. `fireCtx := identity.WithIdentity(ctx, id)`.
4. Inject run origin for Phase 3 (no-op until Phase 3 lands — see note):
   `fireCtx = instance.WithOrigin(fireCtx, instance.Origin{Source: instance.OriginRunNow, ScheduleID: &s.ID})`.
5. `instID, _, err := trigger.TriggerWithDepartment(fireCtx, s.WorkflowName, s.Variables, "normal", s.Department)`.
6. Return `(instID, true, err)`.

`Evaluator.fireOne` is refactored to call a shared internal helper for steps
2–3 + the trigger call, keeping its cron-only alerting and `SetLastFired`. The
goal is one identity-resolve+trigger implementation, not two.

> **Phase ordering note.** `instance.Origin` / `WithOrigin` are introduced in
> Phase 3. In Phase 1, step 4 is omitted (or stubbed) and added when Phase 3
> lands. Phase 1 does not depend on Phase 3.

### Service.RunNow

`Service` gains an **optional** firer dependency (nil when unwired, exactly like
the conditional MCP registration pattern):

```go
type Firer interface {
    Fire(ctx context.Context, s Schedule, skipWhenBusy bool) (uuid.UUID, bool, error)
}
func WithFirer(f Firer) ServiceOption

type RunNowInput struct{ SkipWhenBusy bool } // default false ⇒ fire anyway

type RunNowResult struct {
    InstanceID uuid.UUID
    Fired      bool // false ⇒ skipped because busy
}

func (s *Service) RunNow(ctx context.Context, id uuid.UUID, in RunNowInput) (RunNowResult, error)
```

`RunNow` flow:
1. `c, err := caller(ctx)` — absence ⇒ `ErrForbidden`.
2. `sched, err := s.repo.Get(ctx, id)` — not found ⇒ `ErrNotFound`.
3. `if !authorize(c, *sched) { return ErrForbidden }` — **owner-or-admin**, the
   same gate as Get/Update/Delete. (Authorization is over *managing the
   schedule*; the run itself is attributed to the schedule's creator.)
4. `if s.firer == nil { return ErrRunNowUnavailable }` — defensive; the surface
   is registered only when wired.
5. `instID, fired, err := s.firer.Fire(ctx, *sched, in.SkipWhenBusy)`.
6. Return `RunNowResult{instID, fired}`.

New sentinels: `ErrRunNowUnavailable` (500-class/unavailable), `ErrCreatorUnresolved`
(409). Reuse existing `ErrForbidden` (403) and `ErrNotFound` (404).

### REST adapter

`POST /schedules/{id}/run` on `ScheduleHandler`:
- Body: `{"skipWhenBusy": false}` (optional; default false).
- Decode → `svc.RunNow(ctx, id, in)` → map errors via the existing handler error
  mapping (add `ErrCreatorUnresolved`→409, `ErrRunNowUnavailable`→503).
- Success body: `{"instanceId": "...", "fired": true}` (HTTP 202 Accepted when
  fired; 200 with `"fired": false` when skipped-busy).

### MCP adapter

`schedule_run_now` tool in `adapter/mcp/tools_schedule.go`, registered inside
`RegisterScheduleTools` **only when** `srv.deps.ScheduleService` exposes a wired
firer (gate added alongside the existing `ScheduleService == nil` guard):
- Args: `id` (required, UUID), `skipWhenBusy` (optional bool, default false).
- Handler: `ParseUUID` → `RunNow` → `scheduleResultError` mapping (extended for
  the two new sentinels) → `MarshalResult({instanceId, fired})`.

### Composition root wiring

Where the `Evaluator` is constructed (it already has `Trigger`,
`IdentityResolver`, `ActiveRunChecker`), build a `Runner` from the same three
collaborators and pass it to `schedule.NewService(..., WithFirer(runner))`. The
REST route and MCP tool are added unconditionally over the now-firer-wired
service (or gated on wiring, consistent with the existing pattern).

## Data flow

```
REST POST /schedules/{id}/run        MCP schedule_run_now
        │                                   │
        └──────────► Service.RunNow ◄────────┘
                         │ caller() + repo.Get + authorize(owner-or-admin)
                         ▼
                     Runner.Fire(schedule, skipWhenBusy)
                         │ [skipWhenBusy && HasActiveRun] ─► skipped (fired=false)
                         │ IdentityForUser(creator) ─► WithIdentity(ctx)
                         ▼
                 Engine.TriggerWithDepartment(creatorCtx, wf, vars, "normal", dept)
                         ▼
                     new instance (attributed to creator)
```

## Error handling

| Condition | Sentinel | REST | MCP |
|---|---|---|---|
| No request identity | `ErrForbidden` | 403 | forbidden |
| Caller not owner/admin | `ErrForbidden` | 403 | forbidden |
| Schedule missing | `ErrNotFound` | 404 | not found |
| Creator identity unresolvable | `ErrCreatorUnresolved` | 409 | conflict |
| Firer not wired | `ErrRunNowUnavailable` | 503 | failed |
| Skipped (busy + skipWhenBusy) | — (not an error) | 200 `fired:false` | `{fired:false}` |
| Trigger failure (missing input, capacity) | wrapped trigger err | 400/500 | failed |

## Testing (TDD — 5 scenario categories)

- **Happy path:** owner runs an enabled schedule → instance created, attributed
  to creator, with the schedule's variables and department; `fired=true`;
  `LastFiredAt` unchanged.
- **Input validation:** bad UUID; missing id; `skipWhenBusy` non-bool.
- **Authorization:** non-owner non-admin → 403; admin runs another user's
  schedule → success; no identity → 403.
- **Collision:** `skipWhenBusy=true` with an active run → `fired=false`, no
  instance; `skipWhenBusy=false` (default) with an active run → new instance
  created anyway.
- **Failure modes:** creator deleted → `ErrCreatorUnresolved`; deleted workflow
  → trigger error surfaced (no position side effects); firer unwired →
  `ErrRunNowUnavailable`.
- **REST/MCP parity:** the same scenarios exercised through both adapters return
  equivalent results (they share `Service.RunNow`).
- **Refactor safety:** existing `Evaluator` tests (skip-when-busy, creator-gone,
  deleted-workflow, position advance) stay green after the shared-core
  extraction.

## Out of scope (this phase)

- Any dashboard control (run-now is API/MCP only).
- Instance origin stamping/persistence (Phase 3; `Runner.Fire` leaves a clearly
  marked seam for it).
