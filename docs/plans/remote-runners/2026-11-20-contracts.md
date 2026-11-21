# Remote Runners — Shared Plan Contracts (source of truth for names)

Every task file under `docs/plans/remote-runners/` uses the names, signatures,
file paths, migration numbers and event/audit vocabulary below. A task that needs
a symbol defined by another task copies the signature into its own
**Interfaces → Consumes** block (tasks must stand alone), but it never renames it.

Spec: `docs/specs/2026-11-19-remote-runners-spec.md`. Designs:
`docs/designs/2026-11-19-remote-runners-design-phase-{1..5}.md`.

## 0. Codebase facts verified for these plans (2026-11-20)

- Module `github.com/arbitron/arbitron`, Go 1.25.5, testify (`assert`/`require`).
  Go gate: `go test ./...`. Dashboard gate (from `dashboard/`): `npx tsc --noEmit` and `npx vitest run`; build `make ui`.
- Hexagonal: `domain/*` never imports `adapter/*`; `cmd/arbitron` is the composition root.
- SQLite: `adapter/sqlite`, hand-written SQL repos (pattern `adapter/sqlite/audit_repo.go`), migrations embedded from
  `adapter/sqlite/migrations/NNN_name.{up,down}.sql`, applied by `RunMigrations` in `adapter/sqlite/migrate.go`.
  **Current head is `039_profile_preflight_notification`.** Test helper `setupTestDB(t)` in `adapter/sqlite/integration_test.go`
  (returns `testDB{DB *sql.DB}` with migrations applied, `:memory:`). Converters `uuidToString`, `timeToString` in `adapter/sqlite/convert.go`.
  `Open` sets `SetMaxOpenConns(1)` — a `BEGIN IMMEDIATE`-style transaction is a single writer; use `db.BeginTx`.
- Config: `domain/config/config.go:Config`; defaults map in `adapter/config/loader.go` (keys are lower-case dotted, e.g. `"queue.globalcap": 20` at :75); `Load(configPath)` at :394.
- Engine run loop: `domain/engine/engine.go:Engine.executeWithRetry` (:1507). It builds `instance.NodeExecution` and calls `nodeResultToExecution` (:1594); it currently ignores the `nodeExecutions.Create` error.
- Context helpers: `engine.ContextWithInstanceID` / `engine.InstanceIDFromContext` (`domain/engine/executor_timer.go:21`), `engine.OwnerFromContext` (`domain/engine/runcontext.go:31`).
- Ports: `domain/engine/ports.go` — `ChatProvider{Chat, StreamChat}`, `AgentProvider{RunAgent}`, `ChatProviderSelector.ChatProviderFor(provider.ResolvedBinding)`, `AgentProviderSelector.AgentProviderFor`, `InteractiveStreamSelector.StreamProviderFor`, `ChatRequest`, `ChatResponse`, `ChatChunk`, `AgentMission`, `AgentResult`, `EventBus{Publish(instanceID, Event); Subscribe; SubscribeAll}`, `Event{Type, Timestamp, Data}`.
- `provider.ResolvedBinding` (`domain/provider/provider.go:98`): `Kind, Provider, BaseURL, Extras, Model, Backend, BackendExplicit, AllowedTools, ContextWindow, Effort, Pinned, OpaquePinRoute, ConnectionRef, RevealKey`. LunaRoute canonical provider: `provider.CanonicalLunaRoute = "lunaroute"` (`domain/provider/canonical.go:20`). `provider.ErrLunaRouteBusy` (`domain/provider/lunaroute_errors.go:36`).
- Family: `modelidentity.ClassifyFamily(routeProvider, model string) modelidentity.Family`; `modelidentity.FamilyUnresolved`.
- Process: `adapter/process/runner.go:run(ctx, commandSpec) (commandResult, error)`; `adapter/process/env.go:buildEnv(passthrough, nodeEnv, vars)`; executors `NewScriptExecutor(fr, passthrough)` / `NewToolCallExecutor(passthrough)`.
- Composition root: `cmd/arbitron/main.go:initDomainWithModelDependencies` (:572). Provider factory built at :1033 (`providersadapter.NewFactory`); executors map at :1079-1087; `newRunModelConsumers(fileReader, providerFactory, pinningResolver, ...)` in `cmd/arbitron/model_consumers.go` builds the LLM/agent executors and `childInputs` preparer from the factory; interactive stream selector at :1376; `NewInteractiveExecutor` :1441, `NewConversationHandler` :1448, `NewGroupResolutionBridge` :1482; `eng.FreezeExecutors()` :1514; `domainStack` struct :235; MCP deps `&adaptmcp.Deps{...}` :435; `startRuntime` in `cmd/arbitron/model_dependencies.go:72` (calls `RecoverCrashedInstancesContext` at :82).
- Audit: `audit.Service.Record(ctx, action, target, detail string)`; action constants in `domain/audit/audit.go`.
- Identity: `identity.FromContext`, `identity.WithIdentity`, `Identity.IsAdmin()`, `identity.ErrForbidden`, `identity.ErrUnauthenticated`.
- Run visibility precedent: private `domain/engine/authz.go:canView(ctx, *instance.Instance) bool`.
- Notifications: `notification.Type` constants in `domain/notification/notification.go`; precedent `TypeProfileRefusesTriggers` (instance-less), emitted at `cmd/arbitron/pin_preflight.go:56`; Teams mapping `adapter/notification/teams_payload.go`.
- MCP: tools registered in `adapter/mcp/register.go:RegisterAllTools`; deps in `adapter/mcp/deps.go:Deps`; handler/test pattern `adapter/mcp/tools_token.go` + `tools_token_test.go` (`NewServerWithDeps`, `callReq`, `toolText`, `userCtx`).
- HTTP: chi router `adapter/http/server.go:NewRouterFromDeps`, `RouterDeps` (:57), `/api/v1` group with `d.Auth.Handler` middleware; system stream `adapter/http/system_stream_handler.go` (`canSee` :119 — admins see all; owner otherwise; fails closed).
- WebSocket lib: `nhooyr.io/websocket` v1.8.17.

## 1. Migration numbers (no other numbers may be used)

| # | File stem | Owner task | Content |
|---|---|---|---|
| 040 | `040_node_invocations` | P1 T2 | `node_invocations` |
| 041 | `041_runner_pool` | P1 T5 | `runners`, `runner_sources`, `runner_jobs`, `runner_job_attempts`, `workspace_homes`, `pool_events` |
| 042 | `042_node_execution_placement` | P1 T14 | `node_executions` columns `runner_id`, `source_id`, `placement_reason`, `placement_history` (JSON text) |
| 043 | `043_runner_keys` | P2 T2 | `runner_keys`; `runners` columns for liveness/compat/telemetry versions |
| 044 | `044_workspace_mobility` | P3 T1 | `workspace_checkpoints`, `home_moves`, `transfer_sessions`, `step_commits`, `drain_items`, `runner_copies` |
| 045 | `045_interactive_sessions` | P4 T1 | `turn_leases`, `turn_intents`, `session_moves`; `node_executions` columns `session_checkpoint_ref`, `session_pin_runner`, `session_generation` |

## 2. Config (`domain/config/config.go`, loader keys in `adapter/config/loader.go`)

```go
// Config gains: Runners RunnersConfig
type RunnersConfig struct {
	// Phase 1 (P1 T1)
	DefaultStepLimit        int // "runners.defaultsteplimit" default 8; must be 1..256
	ProbeIntervalSeconds    int // "runners.probeintervalseconds" default 60; >= 5
	ProbeJitterSeconds      int // "runners.probejitterseconds" default 10; >= 0
	TransientMaxAttempts    int // "runners.transientmaxattempts" default 3; 1..10
	TransientBackoffMinMs   int // "runners.transientbackoffminms" default 1000
	TransientBackoffMaxMs   int // "runners.transientbackoffmaxms" default 30000; >= min
	EventRetention          int // "runners.eventretention" default 1000; >= 100
	// Phase 2 (P2 T2)
	HeartbeatSeconds        int // "runners.heartbeatseconds" default 5
	SuspectAfterSeconds     int // "runners.suspectafterseconds" default 30
	LostAfterSeconds        int // "runners.lostafterseconds" default 120; > suspect
	OfferTTLSeconds         int // "runners.offerttlseconds" default 30
	OutputBufferFrames      int // "runners.outputbufferframes" default 4096
	TelemetryWindow         int // "runners.telemetrywindow" default 60 samples
	ScratchRoot             string // "runners.scratchroot" default "<workspace>/.runner-scratch" on built-in
	// Phase 3 (P3 T1)
	MoveLoadThresholdPct    int   // "runners.moveloadthresholdpct" default 50
	MoveSizeCapBytes        int64 // "runners.movesizecapbytes" default 2147483648 (2 GiB)
	RestartGraceSeconds     int   // "runners.restartgraceseconds" default 180
	TransferPartBytes       int64 // "runners.transferpartbytes" default 67108864 (64 MiB)
	StagingDir              string // "runners.stagingdir" default "<database dir>/runner-staging"
	CheckpointDir           string // "runners.checkpointdir" default "<database dir>/checkpoints"
	StagingRetentionSeconds int   // "runners.stagingretentionseconds" default 3600
	// Phase 4 (P4 T1)
	TurnLeaseSeconds        int // "runners.turnleaseseconds" default 60; renewed every TTL/3
	SessionBundleRetention  int // "runners.sessionbundleretention" default 1 (latest successful turn per session)
	RebuildMaxAttempts      int // "runners.rebuildmaxattempts" default 2
}
func (c RunnersConfig) Validate() error // returns errors.Join of every violation; called from Load
```

## 3. Step identity and durable invocations (`domain/engine`, P1 T2)

```go
// domain/engine/stepidentity.go
type OperationKind string
const (
	OperationNode       OperationKind = "node"
	OperationChildInput OperationKind = "child_input"
	OperationTurn       OperationKind = "turn"
	OperationResolution OperationKind = "resolution"
	OperationProbe      OperationKind = "probe"
)
type StepIdentity struct {
	RunID        uuid.UUID
	NodeID       string
	InvocationID uuid.UUID     // one per node VISIT (back-edge => new); reused across restart
	OperationID  string        // e.g. "node", "node/child-input", "turn/<turnIndex>", "resolution/<attemptID>"
	Kind         OperationKind
	Attempt      int           // workflow retry attempt (1-based); resets every visit
}
func (s StepIdentity) Valid() bool // RunID != Nil && NodeID != "" && InvocationID != Nil && OperationID != "" && Attempt >= 1
func ContextWithStepIdentity(ctx context.Context, id StepIdentity) context.Context
func StepIdentityFromContext(ctx context.Context) (StepIdentity, bool)
// WithSubOperation derives a child operation under the same invocation: OperationID = parent.OperationID + "/" + suffix.
func WithSubOperation(ctx context.Context, suffix string, kind OperationKind) context.Context
var ErrMissingStepIdentity = errors.New("engine: dispatched operation has no step identity")

// domain/engine/invocations.go — durable invocation producer (RR-01)
type InvocationStore interface {
	// Begin returns the OPEN (uncommitted) invocation for (runID,nodeID) if one exists
	// (restart reuse), else creates a new one with visit = previous visit + 1.
	Begin(ctx context.Context, runID uuid.UUID, nodeID string) (Invocation, error)
	// Commit marks the invocation committed (node finished and engine advanced).
	Commit(ctx context.Context, invocationID uuid.UUID) error
	Get(ctx context.Context, invocationID uuid.UUID) (Invocation, error)
}
type Invocation struct {
	ID        uuid.UUID
	RunID     uuid.UUID
	NodeID    string
	Visit     int
	CreatedAt time.Time
	CommittedAt *time.Time
}
// Engine setter (nil => engine allocates an in-memory uuid per visit, used only by legacy tests):
func (e *Engine) SetInvocationStore(s InvocationStore)
```
SQLite adapter: `adapter/sqlite/invocation_repo.go:InvocationRepo` (`NewInvocationRepo(db *sql.DB) *InvocationRepo`).
Stamping sites: `Engine.executeWithRetry` (OperationNode), `conversationHandler.HandleMessage` (OperationTurn, OperationID `turn/<n>` where n = count of user turns so far + 1), `GroupResolutionBridge.StartResolution` (OperationResolution, OperationID `resolution/<resolution attempt id>`), `LLMChildInputPreparer.Prepare` (WithSubOperation(ctx, "child-input", OperationChildInput)).

## 4. Placement facts sink (`domain/engine`, P1 T2 defines; P1 T8 writes; P1 T14 persists)

```go
// domain/engine/placement_sink.go
type PlacementFact struct {
	JobID      uuid.UUID `json:"jobId"`
	RunnerID   string    `json:"runnerId"`
	SourceID   string    `json:"sourceId,omitempty"`
	Reason     string    `json:"reason"`
	Generation int64     `json:"generation"`
	Outcome    string    `json:"outcome"` // "completed" | "exhausted" | "logged_out" | "transient" | "runner_lost" | "other" | "fenced"
	At         time.Time `json:"at"`
}
type PlacementSink struct { /* mu sync.Mutex; facts []PlacementFact */ }
func NewPlacementSink() *PlacementSink
func (s *PlacementSink) Record(f PlacementFact)
func (s *PlacementSink) Facts() []PlacementFact // copy, in order
func ContextWithPlacementSink(ctx context.Context, s *PlacementSink) context.Context
func PlacementSinkFromContext(ctx context.Context) (*PlacementSink, bool)
```
`instance.NodeExecution` gains (P1 T14): `RunnerID *string; SourceID *string; PlacementReason *string; PlacementHistory []instance.PlacementRecord`
with `type PlacementRecord struct { JobID, RunnerID, SourceID, Reason, Outcome string; Generation int64; At time.Time }` (json camelCase).

## 5. Process port (`domain/engine`, P1 T9)

```go
// domain/engine/process_runner.go
type ProcessKind string
const ( ProcessScript ProcessKind = "script"; ProcessToolCall ProcessKind = "tool_call" )
type ProcessSpec struct {
	Kind        ProcessKind
	Command     string            // interpreter (script) or command (tool_call)
	Args        []string          // rendered; for script Args[0] is the script path (Phase 1) or bundle-relative entrypoint (Phase 3)
	Dir         string            // cwd; script = script dir; tool_call = workspace (Phase 3 sets it)
	Stdin       []byte            // nil = none
	NodeEnv     map[string]string // node env with templates ALREADY resolved on the server
	Passthrough []string          // passthrough NAMES; values are read on the executing machine
	TimeoutSecs int
	WorkDir     string            // canonical {{workspace}} for affinity; "" = no workspace
	Bundle      *ScriptBundleRef  // nil in Phase 1; Phase 3 P3 T8 fills
}
type ScriptBundleRef struct { Hash string; Entrypoint string } // Entrypoint is bundle-relative
type ProcessResult struct { Stdout, Stderr string; ExitCode int }
type ProcessRunner interface { RunProcess(ctx context.Context, spec ProcessSpec) (ProcessResult, error) }
```
Adapter: `adapter/process/local_runner.go:LocalProcessRunner` (`NewLocalProcessRunner() *LocalProcessRunner`), builds env with `buildEnvFromResolved(passthrough []string, nodeEnv map[string]string) ([]string, error)` (same collision policy as `buildEnv`). Executors change to `NewScriptExecutor(fr engine.FileReader, passthrough []string, pr engine.ProcessRunner)` and `NewToolCallExecutor(passthrough []string, pr engine.ProcessRunner)`; a nil `pr` panics at construction.

## 6. Pool vocabulary (`domain/runner`, P1 T3)

```go
package runner
type RunnerID string
const BuiltInRunnerID RunnerID = "builtin"
type Liveness string   // "online" | "suspect" | "lost" | "offline"
type CompatState string // "compatible" | "incompatible" | "chat_only"
type Compatibility struct { State CompatState; Reason string }
type Runner struct {
	ID RunnerID; Name string; BuiltIn bool
	StepLimit int; Labels map[string]string
	Cordoned bool; Draining bool
	Compat Compatibility; Liveness Liveness; LastContact *time.Time
	OS string; RunnerVersion string; HarnessVersions map[string]string
	WorkspaceRoot string
	CreatedAt time.Time
}
type CredentialKind string
const (
	CredAnthropicSubscription CredentialKind = "anthropic_subscription"
	CredOpenAISubscription    CredentialKind = "openai_subscription"
	CredLunaRoute             CredentialKind = "lunaroute"
	CredServerConnection      CredentialKind = "server_connection" // built-in only
	CredLocalHarness          CredentialKind = "local_harness"     // pi/opencode no-connection paths; built-in only (RR-02 decision)
	CredNone                  CredentialKind = "none"              // process steps: slot only
)
type SourceID string
func MakeSourceID(r RunnerID, k CredentialKind, ref string) SourceID // "<runner>/<kind>/<ref>" (ref "" => "-")
type AuthState string     // "unknown" | "ok" | "logged_out"
type CapacityState string // "available" | "exhausted" | "probation"
type SourceHealth struct {
	Auth AuthState; Capacity CapacityState
	ResetAt *time.Time         // nil = unknown reset (only meaningful when exhausted)
	Headroom *float64          // 0..1 remaining; nil = unknown (NEVER treated as 0 or 1)
	Windows []LimitWindow
	Congested bool; CongestedUntil *time.Time // transient pressure, never exhaustion
	LastProbeAt *time.Time; LastOutcome OutcomeClass; Detail string // detail is class/reset only, never provider prose
}
type Source struct {
	ID SourceID; RunnerID RunnerID; Kind CredentialKind; CredentialRef string
	Families []modelidentity.Family // advertised served families; empty = none proven
	Backends []string               // e.g. "claude-code","codex"; chat paths use ""
	Models []string                 // optional exact-model allowlist; empty = any model of the family
	Health SourceHealth
}
type ModelGroup struct { Kind CredentialKind; ConnectionRef string; Family modelidentity.Family }
func (g ModelGroup) String() string // "anthropic_subscription:anthropic", "server_connection:<ref>:openai", "none"
type OpType string // "chat" | "stream" | "agent" | "process"
type Affinity string
const (
	AffinityNone      Affinity = "none"      // hermetic chat (Phase 2+)
	AffinityBuiltIn   Affinity = "builtin"   // Phase 1 everything; server-local staging
	AffinityWorkspace Affinity = "workspace" // Phase 3
	AffinitySession   Affinity = "session"   // Phase 4
)
type ExecRequirements struct {
	Group ModelGroup; Model string; Backend string; Op OpType
	Affinity Affinity; RunID uuid.UUID; WorkspaceBytes int64
	PinnedRunner RunnerID // Phase 4 session pin; "" = none
	RequireExecutedModel bool; Tools []string
}
// GroupFor derives the model group from a binding (P1 T3). Rules (design P1 §3.6):
// 1) Provider == provider.CanonicalLunaRoute -> CredLunaRoute, ConnectionRef = b.ConnectionRef
// 2) other non-empty ConnectionRef -> CredServerConnection, ConnectionRef = b.ConnectionRef
// 3) no connection & (op chat|stream with Backend "" or "claude-code") or (agent with backend claude-code) -> CredAnthropicSubscription
// 4) no connection & backend codex -> CredOpenAISubscription
// 5) no connection & backend pi|opencode -> CredLocalHarness, ConnectionRef = backend
// Family = modelidentity.ClassifyFamily(b.Provider, b.Model). Unresolved family => ErrUnresolvedFamily, except
// CredServerConnection/CredLocalHarness (built-in only) which carry FamilyUnresolved and match only their own source.
func GroupFor(b provider.ResolvedBinding, op OpType) (ModelGroup, error)
var ErrUnresolvedFamily = errors.New("runner: model family unresolved; refusing remote placement")
```

### Outcomes (`domain/runner/outcome.go`, P1 T3)
```go
type OutcomeClass string
const ( OutcomeNone OutcomeClass = ""; OutcomeExhausted = "exhausted"; OutcomeLoggedOut = "logged_out";
        OutcomeTransient = "transient"; OutcomeRunnerLost = "runner_lost"; OutcomeOther = "other" )
type LimitWindow struct { LimitID string; ModelScope string; DurationMins int; UsedPercent *float64; ResetAt *time.Time }
type SourceOutcome struct {
	Class OutcomeClass; ResetAt *time.Time; RetryAfter time.Duration
	Windows []LimitWindow; HeadroomHint *float64
}
type OutcomeError struct { Outcome SourceOutcome; Err error }
func (e *OutcomeError) Error() string; func (e *OutcomeError) Unwrap() error
func WithOutcome(err error, o SourceOutcome) error // nil err => nil
// ClassifyError: *OutcomeError via errors.As -> its outcome; errors.Is(err, provider.ErrLunaRouteBusy) -> Transient
// (RetryAfter from provider when available); ErrRunnerLost -> RunnerLost; anything else -> Other. Never reads prose.
func ClassifyError(err error) SourceOutcome
var ErrRunnerLost = errors.New("runner: runner lost")
```
Adapters attach outcomes before sanitizing: `claudecli` wraps with `runner.WithOutcome(...)` before `SanitizeProcessError`; the sanitized error must still `errors.As` to `*runner.OutcomeError` (P1 T11 adds `Unwrap` preservation).

## 7. Placement (`domain/runner/placement.go`, pure, P1 T4; tiers added P3 T4)

```go
type Telemetry struct {
	SampledAt time.Time
	CPUPercent *float64; MemUsedBytes *uint64; MemPressure *string // "normal"|"warn"|"critical"
	Load1Pct *float64; DiskFreeBytes *uint64; Thermal *string // "nominal"|"fair"|"serious"|"critical"
	UptimeSeconds *uint64; Cores *int; RAMBytes *uint64
}
type Home struct { RunID uuid.UUID; RunnerID RunnerID; Generation int64; CheckpointVersion int64; Dirty bool } // RunnerID "" = server checkpoint only
type PoolSnapshot struct {
	Runners []Runner; Sources []Source
	SlotsInUse map[RunnerID]int; SourceInFlight map[SourceID]int
	Telemetry map[RunnerID]Telemetry
	Homes map[uuid.UUID]Home // by run (Phase 3)
	Now time.Time
}
type WaitKind string // "slot" | "capacity"
type WaitReason struct { Kind WaitKind; Group ModelGroup; Model string; NextReset *time.Time; Detail string }
type Decision struct {
	Placed bool; Runner RunnerID; Source SourceID; Reason string
	Tier string // Phase 3: "must_move"|"should_move"|"may_move_load"|"stay"|"" (non-workspace)
	Wait *WaitReason
}
type PlacementPolicy struct { MoveLoadThresholdPct int; MoveSizeCapBytes int64 } // zero value = Phase 1 behaviour
func Place(req ExecRequirements, snap PoolSnapshot, policy PlacementPolicy) Decision
```
Rules: exclude cordoned, draining, suspect, lost, offline, incompatible runners (chat_only runners accept only `AffinityNone`);
`CredServerConnection`/`CredLocalHarness`/`AffinityBuiltIn` only on `BuiltInRunnerID`; source must match Kind, ConnectionRef (when kind is LunaRoute/server/local), Family (never crossed), Backend, and Model (if `Models` non-empty);
source health eligible iff `Auth != logged_out && (Capacity == available || (Capacity == probation && SourceInFlight[src]==0))`;
free slot iff `SlotsInUse[r] < StepLimit`. If eligible sources exist but none has a slot => `Wait{Kind: slot}`.
If none eligible => `Wait{Kind: capacity, NextReset: earliest known ResetAt among matching exhausted sources}`.
Rank: headroom desc (nil = unknown ranks after known > 0.2 and before known <= 0.2), then lower slot utilisation, then
healthier telemetry (nil = unknown, neutral), then deterministic tie-break `fnv32(runID + sourceID)` so equal jobs spread.
Reason string: `"<tier-or-rank>: runner=<id> source=<id> headroom=<v|unknown> slots=<used>/<limit>"`.

## 8. Ports in `domain/runner` (P1 T5/T6)

```go
type RunnerConfigPatch struct { StepLimit *int; Labels map[string]string } // nil = unchanged; Labels non-nil replaces
type Registry interface {
	Snapshot(ctx context.Context) (PoolSnapshot, error) // runners+sources+slots+in-flight (+homes from P3); Telemetry filled by caller
	GetRunner(ctx context.Context, id RunnerID) (Runner, []Source, error) // ErrRunnerNotFound
	UpsertRunner(ctx context.Context, r Runner) error
	UpdateConfig(ctx context.Context, id RunnerID, p RunnerConfigPatch) (Runner, error)
	SetCordoned(ctx context.Context, id RunnerID, cordoned bool) error
	SetDraining(ctx context.Context, id RunnerID, draining bool) error
	SetLiveness(ctx context.Context, id RunnerID, l Liveness, at time.Time) error
	SetCompat(ctx context.Context, id RunnerID, c Compatibility) error
	ReplaceSources(ctx context.Context, id RunnerID, sources []Source) error // keeps existing Health for unchanged IDs
	RecordHealth(ctx context.Context, id SourceID, h SourceHealth) error
	GetSource(ctx context.Context, id SourceID) (Source, error) // ErrSourceNotFound
	ListRunners(ctx context.Context) ([]Runner, error)
}
type JobState string // "queued" | "offered" | "running" | "completed" | "failed" | "fenced"
type JobKey struct { RunID uuid.UUID; InvocationID uuid.UUID; OperationID string; Attempt int }
type Job struct {
	ID uuid.UUID; Key JobKey; NodeID string; Op OpType; Group ModelGroup; Model string
	RunnerID RunnerID; SourceID SourceID; Generation int64; State JobState
	LeaseUntil *time.Time; Reason string; Wait *WaitReason
	ResultRef string; CheckpointRef string
	CreatedAt, UpdatedAt time.Time
}
type Fence struct { JobID uuid.UUID; RunnerID RunnerID; Generation int64 }
type Ledger interface {
	EnsureIntent(ctx context.Context, key JobKey, nodeID string, req ExecRequirements) (Job, error) // idempotent on key; writes queued
	SetWait(ctx context.Context, jobID uuid.UUID, w *WaitReason) error
	// Reserve: one transaction — job must be queued; counts offered+running on runner; if count >= limit => ErrSlotTaken;
	// sets runner/source/reason, generation+1, state offered, lease. Returns the fence.
	Reserve(ctx context.Context, jobID uuid.UUID, d Decision, limit int, leaseUntil time.Time) (Job, error)
	MarkRunning(ctx context.Context, f Fence) error
	Complete(ctx context.Context, f Fence, resultRef string) error
	Fail(ctx context.Context, f Fence, class OutcomeClass) error            // terminal failure of this attempt (slot released)
	Requeue(ctx context.Context, f Fence, class OutcomeClass) (Job, error)  // back to queued, slot released, history appended
	FenceCheck(ctx context.Context, f Fence) error                          // THE single fence; ErrFenced when stale/terminal
	ExtendLease(ctx context.Context, f Fence, until time.Time) error
	Get(ctx context.Context, jobID uuid.UUID) (Job, error)
	FindByKey(ctx context.Context, key JobKey) (Job, bool, error)
	ListByRun(ctx context.Context, runID uuid.UUID) ([]Job, error)
	ListActive(ctx context.Context) ([]Job, error)                         // queued, offered, running
	ListActiveByRunner(ctx context.Context, id RunnerID) ([]Job, error)
	Attempts(ctx context.Context, jobID uuid.UUID) ([]Attempt, error)
}
type Attempt struct { JobID uuid.UUID; Generation int64; RunnerID RunnerID; SourceID SourceID; Reason, Outcome string; StartedAt time.Time; EndedAt *time.Time }
var ( ErrSlotTaken, ErrFenced, ErrRunnerNotFound, ErrSourceNotFound, ErrJobNotFound error )
type PoolEvent struct {
	ID uuid.UUID; At time.Time; Kind string
	RunnerID RunnerID; SourceID SourceID; RunID *uuid.UUID // RunID non-nil => run-scoped (visibility filtered)
	Cause Cause; Detail map[string]any // never prompts/provider prose/credentials
}
type Cause struct { Kind string /* "outcome"|"operator"|"placement"|"liveness"|"system" */; Actor string; Outcome OutcomeClass }
type EventLog interface {
	Append(ctx context.Context, ev PoolEvent) error
	List(ctx context.Context, limit int) ([]PoolEvent, error) // newest first
	Prune(ctx context.Context, keep int) error
}
```
SQLite adapters: `adapter/sqlite/runner_registry_repo.go:RunnerRegistryRepo`, `adapter/sqlite/runner_ledger_repo.go:RunnerLedgerRepo`, `adapter/sqlite/pool_event_repo.go:PoolEventRepo` (constructors `NewRunnerRegistryRepo(db)`, `NewRunnerLedgerRepo(db)`, `NewPoolEventRepo(db)`).

## 9. Event vocabulary (bus `Event.Type`; pool events use `Kind` = same string)

System-scope events are published with `instanceID = uuid.Nil`; P1 T7 changes `SystemStreamHandler` so `uuid.Nil`-scoped events whose type starts with `runner.`, `source.` or `pool.` go to every authenticated subscriber. Run-scoped events are published with the run's ID so the existing `canSee` filters them.
- `runner.updated`, `runner.cordoned`, `runner.uncordoned` (P1); `runner.connected`, `runner.suspect`, `runner.lost`, `runner.reconnected`, `runner.incompatible`, `runner.key_revoked` (P2); `runner.draining`, `runner.drained` (P3).
- `source.health_changed`, `source.logged_out`, `source.recovered` (P1).
- `execution.status` (run-scoped; P1, extended P3/P4), payload `instance.ExecutionStatus`.
- `job.rerouted` (P1), `job.recovered`, `job.adopted` (P3), `workspace.moved` (P3, run-scoped).
- `session.moved`, `interactive.turn_restarted`, `interactive.context_notice` (P4, run-scoped).
Recorder: `runner.EventRecorder` — `NewEventRecorder(log EventLog, bus engine.EventBus, retention int, now func() time.Time) *EventRecorder`; `Record(ctx, ev PoolEvent)` appends (logs error, never fails caller) and publishes `engine.Event{Type: ev.Kind, Data: PoolEventView}` scoped to `*ev.RunID` or `uuid.Nil`.

## 10. Execution status (P1 T7)

```go
// domain/instance/execution_status.go
type ExecutionState string // "queued_slot" | "waiting_capacity" | "running" | "moving_workspace" | "recovering"
type ExecutionStatus struct {
	RunID uuid.UUID `json:"runId"`; NodeID string `json:"nodeId"`; OperationID string `json:"operationId"`
	JobID uuid.UUID `json:"jobId"`; State ExecutionState `json:"state"`
	RunnerID string `json:"runnerId,omitempty"`; SourceID string `json:"sourceId,omitempty"`
	Group string `json:"group,omitempty"`; Model string `json:"model,omitempty"`
	Reason string `json:"reason,omitempty"`; NextReset *time.Time `json:"nextReset,omitempty"`
	Progress *MoveProgress `json:"progress,omitempty"` // P3
	HomeRunnerID string `json:"homeRunnerId,omitempty"`; SessionPinRunnerID string `json:"sessionPinRunnerId,omitempty"` // P3/P4
}
type MoveProgress struct { BytesDone, BytesTotal int64 `json:"..."` } // json: bytesDone, bytesTotal
// EnrichedInstanceSummary gains: Execution []ExecutionStatus `json:"execution,omitempty"`
```
Port in engine: `type ExecutionStatusProvider interface { ExecutionStatuses(ctx context.Context, runIDs []uuid.UUID) (map[uuid.UUID][]instance.ExecutionStatus, error) }`; `(*InstanceEnricher).SetExecutionStatusProvider(p)` (nil rejected; default `NoopExecutionStatusProvider{}`).
Producer: `runner.NewExecutionStatusProjector(ledger Ledger) *ExecutionStatusProjector` (implements the port from queued/offered/running jobs) and `(*ExecutionStatusProjector).Publish(ctx, job Job, state instance.ExecutionState)` publishes `execution.status` on the bus (dispatcher calls it).

## 11. Dispatcher and runner port (`domain/runner`, P1 T8/T10)

```go
type StepJob struct {
	Op OpType
	Binding provider.ResolvedBinding // server-side only; NEVER serialized to remote runners
	Chat *engine.ChatRequest          // chat & stream
	Agent *engine.AgentMission
	Process *engine.ProcessSpec
}
type Assignment struct { Job Job; Fence Fence }
type StepOutput struct {
	Chat *engine.ChatResponse; Stream <-chan engine.ChatChunk
	Agent *engine.AgentResult; Process *engine.ProcessResult
}
type StepRunner interface { Run(ctx context.Context, a Assignment, job StepJob) (StepOutput, error) } // error carries outcome via WithOutcome
type RunnerSet interface { For(id RunnerID) (StepRunner, bool) }
type StaticRunnerSet map[RunnerID]StepRunner // P1; P2 T6 adds HubRunnerSet composing builtin + remote
type DispatcherDeps struct {
	Registry Registry; Ledger Ledger; Runners RunnerSet; Events *EventRecorder
	Status *ExecutionStatusProjector; Health HealthTransitioner; Telemetry TelemetrySource
	Policy PlacementPolicy; Clock func() time.Time
	TransientMaxAttempts int; BackoffMin, BackoffMax time.Duration
	Wake <-chan struct{} // pool-change notifications (registry/ledger writes call Notifier.Notify)
}
type HealthTransitioner interface { ApplyOutcome(ctx context.Context, src SourceID, o SourceOutcome) error } // implemented by HealthMonitor
type TelemetrySource interface { Latest() map[RunnerID]Telemetry } // P1: built-in sampler or empty; P2: hub window
type PoolNotifier struct{} // NewPoolNotifier(); Notify(); Subscribe() (<-chan struct{}, func())
func NewStepDispatcher(d DispatcherDeps) *StepDispatcher
func (d *StepDispatcher) Dispatch(ctx context.Context, req ExecRequirements, job StepJob) (StepOutput, error)
```
Flow (design P1 §3.5): require `engine.StepIdentityFromContext` (else `engine.ErrMissingStepIdentity`); `EnsureIntent` (ledger write failure => return error, never execute); loop { snapshot+telemetry → `Place`; on Wait → `SetWait`, `Status.Publish(waiting_capacity|queued_slot)`, block on Wake/ctx (ctx cancel ends wait) ; `Reserve` (ErrSlotTaken => loop) ; `Status.Publish(running)`; record `engine.PlacementFact` into the context sink; `MarkRunning`; `Runners.For(runner).Run`; classify: exhausted/logged_out → `Health.ApplyOutcome`, `Requeue`, `Events.Record(job.rerouted)`, continue (no workflow retry spent); transient → backoff (RetryAfter honoured, bounded), same source, up to TransientMaxAttempts then `Fail` + return error; runner_lost → `Requeue` and continue; other → `Fail` and return error; success → `Complete` (result ref = job id) and return }.
Streams: the dispatcher returns a wrapping channel; the slot is held until the terminal chunk; a classified exhausted/logged_out terminal error BEFORE any token was forwarded re-places transparently; after tokens it applies the outcome to the source and forwards the error (Phase 4 adds turn restart).

Decorators (`domain/runner/selectors.go`, P1 T10):
```go
func NewRoutingChatSelector(inner engine.ChatProviderSelector, d *StepDispatcher, affinity func(provider.ResolvedBinding) Affinity) engine.ChatProviderSelector
func NewRoutingAgentSelector(inner engine.AgentProviderSelector, d *StepDispatcher) engine.AgentProviderSelector
func NewRoutingStreamSelector(inner engine.InteractiveStreamSelector, d *StepDispatcher) engine.InteractiveStreamSelector
func NewRoutedProcessRunner(d *StepDispatcher) engine.ProcessRunner
```
The decorated selector still calls `inner.XFor(b)` first so factory validation errors surface unchanged, then returns a routed provider whose methods build `ExecRequirements` via `GroupFor` and call `Dispatch`. Phase 1 affinity is `AffinityBuiltIn` for all ops; P2 T6 makes `llm_call`/child-input chat `AffinityNone`.
Built-in runner: `adapter/runner/builtin/runner.go` — `builtin.New(builtin.Deps{Chats engine.ChatProviderSelector; Agents engine.AgentProviderSelector; Streams engine.InteractiveStreamSelector; Process engine.ProcessRunner}) *builtin.Runner` (INNER undecorated selectors + `process.LocalProcessRunner`).

## 12. Health (`domain/runner/health.go`, P1 T13)

```go
type AuthObservation struct { State AuthState }
type CapacityObservation struct { State CapacityState; ResetAt *time.Time; OrdinaryUsageAllowed *bool; Windows []LimitWindow; Headroom *float64 }
type ProbeResult struct { Auth AuthState; Capacity *CapacityObservation } // Capacity nil = probe cannot observe capacity
type SourceProber interface { Probe(ctx context.Context, s Source) (ProbeResult, error) } // read-only: spends no tokens, no slot
type ConfirmationProber interface { Confirm(ctx context.Context, s Source) error } // token-spending same-model probe; dispatched WITH a slot
type SourceAlertNotifier interface { SourceLoggedOut(ctx context.Context, s Source) error; SourceRecovered(ctx context.Context, s Source) error }
type MonitorDeps struct {
	Registry Registry; Probers map[CredentialKind]SourceProber; Confirm ConfirmationProber
	Alerts SourceAlertNotifier; Events *EventRecorder; Notifier *PoolNotifier
	Clock func() time.Time; Interval, Jitter time.Duration; Rand func() float64
}
func NewHealthMonitor(d MonitorDeps) *HealthMonitor
func (m *HealthMonitor) Start(ctx context.Context) // goroutine; stops on ctx
func (m *HealthMonitor) ProbeNow(ctx context.Context, id SourceID) (SourceHealth, error)
func (m *HealthMonitor) ApplyOutcome(ctx context.Context, id SourceID, o SourceOutcome) error // implements HealthTransitioner
func (m *HealthMonitor) Tick(ctx context.Context) error // one pass; tests drive it with a fake clock
```
Notification: `notification.TypeSourceLoggedOut Type = "source_logged_out"` (instance-less; `WorkflowName` = `"runner <runner> source <kind>"`). Adapter notifier: `adapter/notification/source_alerts.go:SourceAlerts` implementing `runner.SourceAlertNotifier` (creates notification row + Teams card via existing notifier).
Probers: `adapter/runner/probe/claude.go:ClaudeAuthProber` (`env -u ANTHROPIC_API_KEY claude auth status`, JSON/exit code), `adapter/runner/probe/codex.go:CodexProber` (`codex.Readiness` + `account/rateLimits/read` via `codex app-server`), `adapter/runner/probe/lunaroute.go:LunaRouteProber` (wraps `adapter/probe` factory). Confirmation: `adapter/runner/probe/confirm.go:DispatchConfirmer` dispatches a bounded 1-token same-model chat through the `StepDispatcher` with synthetic identity `OperationProbe`.

## 13. Source inventory (P1 T13)

```go
// domain/runner/inventory.go
type InventoryInput struct {
	ClaudeCLI *HarnessPresence // nil = not found
	Codex *HarnessPresence
	Connections []provider.Connection // from ConnectionStore.List (metadata only; no key material)
	LocalBackends []string // "pi","opencode" when binaries found
}
type HarnessPresence struct { Path string; Version string; Supported bool; Reason string }
func BuiltInSources(in InventoryInput) []Source // pure
```
Adapter producer: `adapter/runner/inventory/local.go:DetectLocal(ctx, cfg) (runner.InventoryInput, error)` (binary lookup + `--version` + `codex.VersionEvidence`). Version table: `adapter/claudecli/versions.go:SupportedVersions` and `adapter/codex/versions.go:SupportedVersions` (`type HarnessFloor struct{ Feature string; Min string }`, `func CheckVersion(v string) (ok bool, reason string)`).

## 14. Runner service and operator surface (P1 T15; extended P2/P3)

```go
type AuditRecorder interface { Record(ctx context.Context, action, target, detail string) } // *audit.Service satisfies
type RunVisibility interface { CanView(ctx context.Context, runID uuid.UUID) bool }
// engine exports: func CanViewInstance(ctx context.Context, inst *instance.Instance) bool (wraps canView)
// runner.NewInstanceVisibility(repo instance.InstanceRepository) RunVisibility
type ServiceDeps struct { Registry Registry; Ledger Ledger; Events EventLog; Recorder *EventRecorder; Audit AuditRecorder; Visibility RunVisibility; Monitor *HealthMonitor; Telemetry TelemetrySource; Notifier *PoolNotifier; Clock func() time.Time }
func NewService(d ServiceDeps) *Service
type RunnerView struct { Runner Runner; Sources []Source; SlotsUsed int; Queued int; Telemetry *Telemetry; HomedWorkspaces int; Drain *DrainProgress /*P3*/ }
type RunnerDetail struct { RunnerView; Assignments []AssignmentView; RecentTelemetry []Telemetry /*P2*/ ; Sessions []SessionPinView /*P4*/ }
type AssignmentView struct { JobID uuid.UUID; RunID *uuid.UUID; NodeID string; State JobState; SourceID SourceID; Hidden bool } // RunID nil + Hidden when caller cannot view
func (s *Service) List(ctx) ([]RunnerView, error)          // any authenticated user
func (s *Service) Get(ctx, id RunnerID) (RunnerDetail, error)
func (s *Service) Events(ctx, limit int) ([]PoolEvent, error) // run-scoped events the caller cannot view are dropped
func (s *Service) Update(ctx, id RunnerID, p RunnerConfigPatch) (Runner, error) // admin; audit runner.update
func (s *Service) Cordon(ctx, id RunnerID) error   // admin; audit runner.cordon
func (s *Service) Uncordon(ctx, id RunnerID) error // admin; audit runner.uncordon
func (s *Service) ProbeSource(ctx, id SourceID) (SourceHealth, error) // admin; audit runner.source_probe
```
Audit actions (add to `domain/audit/audit.go`): `ActionRunnerUpdate = "runner.update"`, `ActionRunnerCordon = "runner.cordon"`, `ActionRunnerUncordon = "runner.uncordon"`, `ActionRunnerSourceProbe = "runner.source_probe"`, `ActionRunnerKeyCreate = "runner_key.create"`, `ActionRunnerKeyRevoke = "runner_key.revoke"`, `ActionRunnerDrain = "runner.drain"`.
MCP (`adapter/mcp/tools_runner.go`, `Deps.Runners RunnerService` interface mirroring the service): `runner_list`, `runner_get`, `runner_events`, `runner_update`, `runner_cordon`, `runner_uncordon`, `runner_source_probe` (P1); `runner_key_create`, `runner_key_list`, `runner_key_revoke` (P2, `Deps.RunnerKeys`); `runner_drain` (P3).
REST (`adapter/http/runner_handler.go`, `RouterDeps.Runners`, under the user-auth `/api/v1` group): `GET /runners`, `GET /runners/events`, `GET /runners/{id}`, `PATCH /runners/{id}`, `POST /runners/{id}/cordon`, `POST /runners/{id}/uncordon`, `POST /runners/sources/{sourceId}/probe` (sourceId URL-escaped) (P1); `GET|POST /runners/keys`, `DELETE /runners/keys/{id}` with JSON body `{"acknowledge": true}` (P2); `POST /runners/{id}/drain` (P3).
Runner-key-only routes (outside user auth, P2/P3): `GET /api/v1/runners/connect` (WebSocket), `/api/v1/runners/transfers/*`.
JSON DTOs (both MCP and REST, defined once in `adapter/http/runner_dto.go` and reused by MCP via `adapter/mcp` importing nothing from http — MCP defines `adapter/mcp/runner_dto.go` with identical json tags). Field names camelCase: `id,name,builtIn,stepLimit,labels,cordoned,draining,compat{state,reason},liveness,lastContact,os,runnerVersion,harnessVersions,slotsUsed,queued,homedWorkspaces,sources[{id,kind,credentialRef,families,health{auth,capacity,resetAt,headroom,congested,lastProbeAt,lastOutcome}}],telemetry{...},drain{...}`.

## 15. Phase 2 names

- Protocol package `adapter/runnerproto`: `const ProtocolVersion = 1`; `type Envelope struct { V int; Kind string; Seq uint64; JobID string; Generation int64; Payload json.RawMessage }` (json: v,kind,seq,jobId,generation,payload); kinds `KindHello="hello"`, `KindHeartbeat="heartbeat"`, `KindAccept="accept"`, `KindReject="reject"`, `KindOutput="output"`, `KindResult="result"`, `KindDispatch="dispatch"`, `KindCancel="cancel"`, `KindProbe="probe"`, `KindProbeResult="probe_result"`, `KindAck="ack"`, `KindFenced="fenced"`, `KindChallenge="challenge"`, `KindChallengeResponse="challenge_response"`, `KindAdmitted="admitted"`, `KindRefused="refused"`; P3 adds `KindDeleteCopy="delete_copy"`, `KindTransferReady="transfer_ready"`; P4 adds `KindSessionExport="session_export"`, `KindSessionImport="session_import"`. `func Encode(e Envelope) ([]byte, error)`, `func Decode(b []byte) (Envelope, error)` (rejects unknown V>ProtocolVersion except Hello/Refused, missing kind, >1 MiB).
- Payloads: `Hello{OS, Arch, ProtocolVersion int, RunnerVersion string, HarnessVersions map[string]string, WorkspaceRoot, ScratchRoot string, Capabilities []string, Sources []WireSource, Held []HeldJob}`, `HeldJob{JobID string; Generation int64; State string /*running|terminal*/; LastOutputSeq uint64}`, `Heartbeat{Telemetry WireTelemetry; Sources []WireSource; Jobs []HeldJob}`, `Dispatch{Op string; Chat *WireChat; Agent *WireAgent; Process *WireProcess; LeaseUntil time.Time}`, `WireChat{Model, SystemPrompt, Prompt string; MaxTokens int; Temperature float64; OutputSchema json.RawMessage; SessionID string; RequireExecutedModel bool; Stream bool; CredentialKind string; Backend string; ContextWindow string; Effort string; Tools []string}` (no RevealKey/BaseURL/ConnectionRef secrets), `Output{Seq uint64; Token string}`, `Result{OK bool; Chat *WireChatResult; Agent *WireAgentResult; Process *WireProcessResult; Outcome WireOutcome; ErrorText string /*sanitized*/}`, `WireOutcome{Class string; ResetAt *time.Time; RetryAfterMs int64; Windows []WireWindow; HeadroomHint *float64}`.
- Keys (`domain/runner/keys.go`): `type RunnerKey struct { ID uuid.UUID; Name string; RunnerID RunnerID; Hash string; Prefix string; MachinePublicKey []byte; CreatedAt time.Time; ClaimedAt, LastUsedAt, RevokedAt *time.Time }`; `type KeyStore interface { Create(ctx, *RunnerKey) error; GetByHash(ctx, hash string) (RunnerKey, error); List(ctx) ([]RunnerKey, error); Claim(ctx, id uuid.UUID, pub []byte, at time.Time) error /* atomic: only if machine_public_key IS NULL, ErrKeyAlreadyClaimed */; TouchLastUsed(ctx, id, at) error; Revoke(ctx, id, at) error }`; `type KeyService` with `NewKeyService(KeyServiceDeps{Store KeyStore; Registry Registry; Audit AuditRecorder; Revoker SessionRevoker; Clock; DefaultStepLimit int})`, `Create(ctx, name string) (plaintext string, k RunnerKey, err error)` (prefix `ark_`, 32 random bytes base64url, SHA-256 hex hash, name `^[a-z0-9][a-z0-9-]{1,62}$`, registers a runner row with `Liveness offline`), `List(ctx) ([]RunnerKey, error)`, `Revoke(ctx, id uuid.UUID, ack RevokeAck) error` (`type RevokeAck struct{ Acknowledged bool }`; false => `ErrRevokeNotAcknowledged`), `Authenticate(ctx, plaintext string) (RunnerKey, error)`; `type SessionRevoker interface { RevokeRunner(ctx context.Context, id RunnerID) error }` (Hub implements). Machine binding: ed25519; server challenge = 32 random bytes; runner signs `"arbitron-runner-v1:" + keyID + ":" + base64(challenge)`.
- SQLite: `adapter/sqlite/runner_key_repo.go:RunnerKeyRepo`.
- Hub (`domain/runner/hub.go`): `type RunnerConn interface { Send(ctx context.Context, m Outbound) error; Close(code int, reason string) error }`; `type Outbound struct { Kind string; JobID uuid.UUID; Generation int64; Dispatch *DispatchPayload; Ack *AckPayload }` and `type Inbound struct { Kind string; JobID uuid.UUID; Generation int64; Seq uint64; Hello *HelloInfo; Heartbeat *HeartbeatInfo; Output *OutputFrame; Result *RemoteResult; Accept bool; RejectReason string }` (domain mirrors; `adapter/http/runner_connect.go` translates `runnerproto` <-> these). `NewHub(HubDeps{Registry; Ledger; Events *EventRecorder; Health HealthTransitioner; Notifier *PoolNotifier; Clock; Config HubConfig{Heartbeat, SuspectAfter, LostAfter, OfferTTL time.Duration; TelemetryWindow int; ServerWorkspaceRoot string; Compat CompatTable}})`; methods `Admit(ctx, key RunnerKey, conn RunnerConn, hello HelloInfo) (*Session, error)`, `Handle(ctx, s *Session, in Inbound) error`, `Disconnect(s *Session)`, `Tick(ctx) error` (liveness), `Start(ctx)`, `RevokeRunner(ctx, id) error`, `Run(ctx, a Assignment, job StepJob) (StepOutput, error)` (used by remote StepRunner), `Latest() map[RunnerID]Telemetry`, `Window(id RunnerID) []Telemetry` (TelemetrySource).
- `type CompatTable struct { ProtocolMin, ProtocolMax int; RunnerMin string; Harness map[string]string /* harness -> min version */ }`; `func (c CompatTable) Check(h HelloInfo, serverRoot string) Compatibility`.
- Remote StepRunner `adapter/runner/remote/runner.go`: `remote.New(h *runner.Hub) *remote.Runner`; `runner.NewHubRunnerSet(builtin StepRunner, remote StepRunner, reg Registry) RunnerSet` (any non-builtin ID → remote).
- Telemetry: `runner.MachineTelemetry interface { Sample(ctx context.Context) (Telemetry, error) }`; adapters `adapter/telemetry/darwin.go` (`//go:build darwin`), `adapter/telemetry/linux.go` (`//go:build linux`), `adapter/telemetry/null.go` (all fields nil); `telemetry.New() runner.MachineTelemetry`. Server built-in feeds the hub window through `runner.NewBuiltInTelemetryLoop(t MachineTelemetry, h *Hub, every time.Duration)`.
- Runner binary: `cmd/arbitron-runner/main.go`; domain `domain/runneragent` (`Agent`, ports `Connection`, `JobExecutor`, `LocalSources`, `Spool`, `IdentityStore`); adapters `adapter/runneragent/wsclient`, `adapter/runneragent/config`, `adapter/runneragent/executor`, `adapter/runneragent/spool`, `adapter/runneragent/identity`. Config dir `~/.arbitron-runner/` (`config.yaml`, `runner.env` 0600: `ARBITRON_SERVER_URL`, `ARBITRON_RUNNER_KEY`, `LUNAROUTE_API_KEY` optional, `ARBITRON_EXTRA_HEADERS` optional `Name: value;Name2: value2`), machine key `~/.arbitron-runner/machine_ed25519` (0600).

## 16. Phase 3 names

- `domain/runner/checkpoint.go`: `type Manifest struct { Version int64; Entries []ManifestEntry; Hash string }`, `type ManifestEntry struct { Path string; Size int64; Mode uint32; SHA256 string; Symlink string; Deleted bool }`, `type CheckpointRef struct { RunID uuid.UUID; Version int64; ManifestHash string }`; port `CheckpointStore interface { Commit(ctx, runID uuid.UUID, srcDir string) (CheckpointRef, Manifest, error); Restore(ctx, ref CheckpointRef, dstDir string) error; Manifest(ctx, ref CheckpointRef) (Manifest, error); Latest(ctx, runID uuid.UUID) (CheckpointRef, bool, error); StageDelta(ctx, runID uuid.UUID, base CheckpointRef, delta io.Reader) (StagedCheckpoint, error); Publish(ctx, staged StagedCheckpoint, f Fence) (CheckpointRef, error); Discard(ctx, staged StagedCheckpoint) error; ReadFile(ctx, ref CheckpointRef, path string) (io.ReadCloser, error); Delete(ctx, runID uuid.UUID) error }`. Adapter `adapter/checkpoint/store.go` (`checkpoint.NewStore(dir string) *Store`), archives use stdlib `archive/tar` + `compress/gzip` (RR-15 decision: gzip, no new dependency).
- Homes: `type HomeStore interface { GetHome(ctx, runID) (Home, error); AcquireResource(ctx, runID uuid.UUID, owner RunnerID, expectGen int64) (Home, error); HandOff(ctx, runID uuid.UUID, to RunnerID, reason string, bytes int64, expectGen int64) (Home, error); SetCheckpoint(ctx, runID uuid.UUID, version int64) error; MarkDirty(ctx, runID uuid.UUID, dirty bool) error; History(ctx, runID uuid.UUID) ([]HomeMove, error); ListByRunner(ctx, id RunnerID) ([]Home, error); Release(ctx, runID uuid.UUID, expectGen int64) error }`; `type HomeMove struct { RunID uuid.UUID; From, To RunnerID; Reason string; Bytes int64; Generation int64; At time.Time }`. SQLite `adapter/sqlite/workspace_home_repo.go:WorkspaceHomeRepo`.
- Commit protocol (RR-10) `domain/engine/step_commit.go`: `type StepCommit struct { RunID, InvocationID uuid.UUID; NodeID string; Execution *instance.NodeExecution; Variables map[string]any; NextNodeID string; CheckpointRef string; JobFence *JobFenceRef }`, `type JobFenceRef struct { JobID uuid.UUID; RunnerID string; Generation int64 }`, `type StepCommitter interface { CommitStep(ctx context.Context, c StepCommit) error; CommittedResult(ctx context.Context, invocationID uuid.UUID) (*StepCommit, bool, error) }`; `(*Engine).SetStepCommitter(c)`. SQLite `adapter/sqlite/step_commit_repo.go:StepCommitRepo` (one transaction: node_executions insert, instances variables + current_node_id, node_invocations committed, runner_jobs completed via fence, step_commits row). Errors are propagated (fixes ignored `Create` error).
- Workspace sync service `domain/runner/sync.go`: `NewWorkspaceSync(SyncDeps{Checkpoints CheckpointStore; Homes HomeStore; Ledger Ledger; Tokens TransferTokens; Events *EventRecorder; Status *ExecutionStatusProjector; Clock})`, `PreparePull(ctx, a Assignment, runID) (PullPlan, error)`, `AcceptPush(ctx, f Fence, runID uuid.UUID, staged StagedCheckpoint) (CheckpointRef, error)`, `CommitBuiltIn(ctx, f Fence, runID uuid.UUID, dir string) (CheckpointRef, error)`, `RestoreBuiltIn(ctx, runID uuid.UUID, dir string) error`.
- Transfer tokens `domain/runner/transfer.go`: `type TransferToken struct { Token string; JobID uuid.UUID; Generation int64; RunnerID RunnerID; RunID uuid.UUID; Direction string /*pull|push|bundle*/; ExpiresAt time.Time }`; `TransferTokens interface { Issue(ctx, ...) (TransferToken, error); Validate(ctx, token string, runner RunnerID) (TransferToken, error) }`. HTTP `adapter/http/runner_transfer_handler.go` routes: `GET /api/v1/runners/transfers/manifest`, `GET /api/v1/runners/transfers/files?path=`, `PUT /api/v1/runners/transfers/push/{part}`, `POST /api/v1/runners/transfers/push/commit`, `GET /api/v1/runners/transfers/bundles/{hash}`; headers `Authorization: Runner <key>`, `X-Arbitron-Transfer: <token>`.
- Bundles: `adapter/process/bundle.go:BuildScriptBundle(scriptPath string) (engine.ScriptBundleRef, []byte, error)`; `domain/runner/bundles.go:BundleStore` port; SQLite-less filesystem store `adapter/checkpoint/bundles.go`.
- Recovery: `domain/runner/recovery.go:NewPoolRecovery(...)`, `(*PoolRecovery).Recover(ctx) error`; engine `(*Engine).RecoverRunningInstances(ctx context.Context, rec RecoveryPolicy) error` replacing `RecoverCrashedInstancesContext` in `startRuntime`; `type RecoveryPolicy interface { PrepareRun(ctx context.Context, inst *instance.Instance) (RecoveryAction, error) }` (`RecoveryResume`, `RecoveryReplayCommitted`, `RecoveryFail`).
- Drain: `(*Service).Drain(ctx, id RunnerID) (DrainProgress, error)`; `type DrainProgress struct { ActiveSteps int; HomedWorkspaces int; Sessions int; Blocked []DrainBlocker; Complete bool }`, `type DrainBlocker struct { Kind string /*workspace|session*/; RunID *uuid.UUID; Group string; Reason string }`.
- Workspace removal: `runner.NewRemovalNotifier(...)` implementing `engine.WorkspaceRemovalHook interface { WorkspaceRemoved(ctx context.Context, runID uuid.UUID) }`; `(*Engine).SetWorkspaceRemovalHook(h)`.

## 17. Phase 4 names

- `domain/engine/session_portability.go`: `type SessionBundle struct { Backend, SessionID, HarnessVersion, OriginWorkDir, Checksum string; Data []byte }`; `type SessionPortability interface { Export(ctx context.Context, sessionID, workDir string) (SessionBundle, error); Import(ctx context.Context, b SessionBundle, workDir string) (sessionID string, err error) }`; `var ErrBundleRequiresRebuild = errors.New(...)`. Adapters `adapter/claudecli/session_portability.go:SessionPortability`, `adapter/codex/session_portability.go:SessionPortability` (strips reasoning; compaction => ErrBundleRequiresRebuild).
- `domain/engine/session_checkpoints.go`: `type SessionKey struct { RunID uuid.UUID; InvocationID uuid.UUID; TurnIndex int }`; `type SessionCheckpointStore interface { Put(ctx, SessionKey, SessionBundle) (ref string, err error); Get(ctx, ref string) (SessionBundle, error); Prune(ctx, runID, invocationID uuid.UUID, keepRef string) error }`; adapter `adapter/checkpoint/sessions.go`.
- `domain/engine/turn_lease.go`: `type TurnLease struct { Key string /* "run:<id>" or "resolution:<identity>" */; Token uuid.UUID; Generation int64; ExpiresAt time.Time }`; `type TurnLeaseStore interface { Acquire(ctx, key string, ttl time.Duration) (TurnLease, error) /* ErrInteractiveTurnInProgress */; Renew(ctx, l TurnLease, ttl time.Duration) (TurnLease, error); Release(ctx, l TurnLease) error; BumpGeneration(ctx, l TurnLease) (TurnLease, error); Current(ctx, key string) (TurnLease, bool, error) }`; `type TurnIntent struct { ID uuid.UUID; LeaseKey string; RunID, InvocationID uuid.UUID; NodeID string; TurnIndex int; Kind string /*first|message|end|abandon|move*/; Prompt, SystemPrompt, Nonce, Message string; Model string; Tools []string; PriorCheckpointRef string; SessionGeneration int64; OperationID string; CreatedAt time.Time; CompletedAt *time.Time }`; `type TurnIntentStore interface { Persist(ctx, TurnIntent) error; Complete(ctx, id uuid.UUID) error; Pending(ctx) ([]TurnIntent, error); PendingFor(ctx, leaseKey string) (*TurnIntent, error) }`. SQLite `adapter/sqlite/turn_lease_repo.go`.
- Mover `domain/runner/session_mover.go`: `NewSessionMover(MoverDeps{...})`, `Move(ctx, lease engine.TurnLease, req MoveRequest) (MoveResult, error)`; `type MoveRequest struct { RunID, InvocationID uuid.UUID; NodeID string; Reason string /*exhausted|logged_out|drain|runner_lost*/; From RunnerID; Requirements ExecRequirements }`; `type MoveResult struct { To RunnerID; SessionID string; Lossy bool; Notice string; Generation int64 }`. Engine port `engine.SessionMover interface { MoveSession(ctx context.Context, lease TurnLease, req SessionMoveRequest) (SessionMoveResult, error) }` (domain/engine mirror types so engine does not import runner).
- Events: `interactive.turn_restarted` `{instanceId,nodeId,turnIndex,generation}`; `interactive.context_notice` `{instanceId,nodeId,notice:"tool details and reasoning may be lost",lossy:true}`; `session.moved` `{from,to,reason,lossy}`.

## 18. Phase 5 names (dashboard)

`dashboard/src/api/types.ts` additions: `RunnerView`, `RunnerDetail`, `RunnerSource`, `SourceHealth`, `RunnerTelemetry`, `PoolEvent`, `RunnerKey`, `RunnerKeyCreateResponse`, `ExecutionStatus`, `PlacementRecord`, `HomeMove`, `DrainProgress`, `SessionMove`. API fns in `dashboard/src/api/runners.ts`: `listRunners()`, `getRunner(id)`, `listPoolEvents(limit)`, `listRunnerKeys()`, `createRunnerKey(name)`, `revokeRunnerKey(id)`. Hooks `dashboard/src/hooks/usePool.ts`, `useRunner.ts`. Routes `/pool`, `/pool/runners/:id`.

## 19. Escalations recorded at plan level

See each phase's task files for the `ESCALATION:` lines; the summary list is reproduced in the final report.
