# Remote Runners — Shared Plan Contracts (source of truth for names)

Every task file under `docs/plans/remote-runners/` uses the names, signatures,
file paths, migration numbers and event/audit vocabulary below. Sections 19–24
record the reviewed producer graph, execution gates and cross-phase completion
contract; read them before implementing any task. A task that needs
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
| 041 | `041_runner_pool` | P1 T5 | `runners`, `runner_sources`, `runner_jobs`, `runner_job_attempts`, `workspace_homes`, `pool_events`; source inventory availability, confirmation model and persisted job requirements |
| 042 | `042_node_execution_placement` | P1 T14 | `node_executions` columns `runner_id`, `source_id`, `placement_reason`, `placement_history` (JSON text) |
| 043 | `043_runner_keys` | P2 T2 | `runner_keys`, `runner_inbox`, `runner_inbox_cursors`; `runner_sources.capabilities` (runner metadata already belongs to 041) |
| 044 | `044_workspace_mobility` | P3 T1 | `workspace_checkpoints` (prepared/committed), `workspace_leases`, `home_moves`, `transfer_sessions`, `step_commits`, `step_commit_operations`, `node_retry_progress`, `node_retry_failures`, suspension records, maintenance operations, `builtin_processes`, `drain_items`, `runner_copies` |
| 045 | `045_interactive_sessions` | P4 T1 | `turn_leases`, `turn_intents` (accepted binding and request ID), `session_moves`, resolution resources/materialization state; `node_executions` columns `session_checkpoint_ref`, `session_pin_runner`, `session_generation`, `session_invocation_id` |

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
	Portable    bool              // P3 T8 (§22 RR-09a): from workflow.NodeConfig.Portable; false = AffinityBuiltIn
	Requires    []string          // P3 T8 (§22 RR-09b): from workflow.NodeConfig.Requires; checked by runner LookPath at accept
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
	Revision uint64 // P1 T13 availability/outcome revision; 041 runner_sources.revision
	Auth AuthState; Capacity CapacityState
	ResetAt *time.Time         // nil = unknown reset (only meaningful when exhausted)
	Headroom *float64          // 0..1 remaining; nil = unknown (NEVER treated as 0 or 1)
	ConfirmationModel string  // exact last exhausted request model, stamped by dispatcher outcome
	Windows []LimitWindow
	Congested bool; CongestedUntil *time.Time // transient pressure, never exhaustion
	LastProbeAt *time.Time; LastOutcome OutcomeClass; Detail string // detail is class/reset only, never provider prose
}
type Source struct {
	ID SourceID; RunnerID RunnerID; Kind CredentialKind; CredentialRef string
	UnavailableReason string // inventory evidence: non-empty => visible but ineligible; independent of auth health
	Families []modelidentity.Family // advertised served families; empty = none proven
	Backends []string               // e.g. "claude-code","codex"; chat paths use ""
	Models []string                 // optional exact-model allowlist; empty = any model of the family
	Capabilities []ExecutionCapability // populated by P2 inventory; remote empty means ineligible
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
	Provider string; ContextWindow provider.ContextWindow; Effort provider.Effort; EndpointFingerprint string
	Affinity Affinity; RunID uuid.UUID; WorkspaceBytes int64
	PinnedRunner RunnerID // Phase 4 session pin; "" = none
	RequireExecutedModel bool; Tools []string

	RequiredSource SourceID // synthetic OperationProbe only; exact credential source
}
type ExecutionCapability struct {
    Op OpType
    Backend, Provider, EndpointFingerprint string
    Families []modelidentity.Family
    Models []string
    ContextWindows []provider.ContextWindow
    Efforts []provider.Effort
    Tools []string
    ExecutedModel bool
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
func EndpointFingerprint(b provider.ResolvedBinding) (string, error) // P1 T3 domain/runner/endpoint.go
var ErrUnresolvedFamily = errors.New("runner: model family unresolved; refusing remote placement")
```

### Outcomes (`domain/runner/outcome.go`, P1 T3)
```go
type OutcomeClass string
const ( OutcomeNone OutcomeClass = ""; OutcomeExhausted = "exhausted"; OutcomeLoggedOut = "logged_out";
        OutcomeTransient = "transient"; OutcomeRunnerLost = "runner_lost"; OutcomeOther = "other" )
type LimitWindow struct { LimitID string; ModelScope string; DurationMins int; UsedPercent *float64; ResetAt *time.Time }
type SourceOutcome struct {
	Model string // dispatcher stamps the exact request model before health transition
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
type Home struct { RunID uuid.UUID; RunnerID RunnerID; Generation int64; CheckpointVersion int64; Dirty bool; NonPortableReason string /* P3 T3, §22 RR-08; "" = portable */ } // RunnerID "" = server checkpoint only
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
type PlacementPolicy struct { MoveLoadThresholdPct int; MoveSizeCapBytes int64; TelemetryMaxAge time.Duration } // zero value = Phase 1 behaviour
func Place(req ExecRequirements, snap PoolSnapshot, policy PlacementPolicy) Decision
```
Rules: exclude cordoned, draining, suspect, lost, offline, incompatible runners (chat_only runners accept only `AffinityNone`);
`CredServerConnection`/`CredLocalHarness`/`AffinityBuiltIn` only on `BuiltInRunnerID`; source must match Kind, ConnectionRef (when kind is LunaRoute/server/local), Family (never crossed), Backend, and Model (if `Models` non-empty);
source inventory eligible iff `UnavailableReason == ""` and verified capabilities match the complete request;
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
	// Reserve: one transaction — job must be queued; re-read persisted runner/source eligibility, actual step limit,
	// and source probation single-flight. Caller limit is not authority. Reject changed inventory/cordon/drain/liveness/compat.
	// Counts offered+running on runner; if count >= persisted limit => ErrSlotTaken;
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
- `job.rerouted` (P1), `job.recovered`, `job.adopted`, `job.recovery_blocked` (P3; the last is run-scoped, from P3 T10 on `runner.ErrNoRecoveryBase`, RR-17 B), `workspace.moved` (P3, run-scoped).
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
Port in engine: `type ExecutionStatusProvider interface { ExecutionStatuses(ctx context.Context, runIDs []uuid.UUID) (map[uuid.UUID][]instance.ExecutionStatus, error) }`; `(*InstanceEnricher).SetExecutionStatusProvider(p)` (nil rejected; default `NoopExecutionStatusProvider{}`). P1 T7 also defines `(*Engine).GetExecutionStatus(ctx context.Context, id uuid.UUID) ([]instance.ExecutionStatus, error)`, authorizing via `GetInstance` before projection; REST instance detail and MCP `instance_get` consume it.
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
	Completion *engine.StepCompletion // P3 T6; streams record final receipt before Done
}
type StepRunner interface { Run(ctx context.Context, a Assignment, job StepJob) (StepOutput, error) } // error carries outcome via WithOutcome
type RunnerSet interface { For(id RunnerID) (StepRunner, bool) }
type StaticRunnerSet map[RunnerID]StepRunner // P1; P2 T6 adds HubRunnerSet composing builtin + remote
type DispatcherDeps struct {
	Registry Registry; Ledger Ledger; Runners RunnerSet; Events *EventRecorder
	Status *ExecutionStatusProjector; Health HealthTransitioner; Telemetry TelemetrySource
	Policy PlacementPolicy; Clock func() time.Time
	TransientMaxAttempts int; BackoffMin, BackoffMax time.Duration
	Wake <-chan struct{} // one forwarding goroutine broadcasts to all waiters
	Sleep func(context.Context, time.Duration) error // injectable cancellable delay
}
type HealthTransitioner interface { ApplyOutcome(ctx context.Context, src SourceID, o SourceOutcome) error } // implemented by HealthMonitor
type TelemetrySource interface { Latest() map[RunnerID]Telemetry } // P1: built-in sampler or empty; P2: hub window
type PoolNotifier struct{} // NewPoolNotifier(); Notify(); Subscribe() (<-chan struct{}, func())
func NewStepDispatcher(d DispatcherDeps) *StepDispatcher
func (d *StepDispatcher) Dispatch(ctx context.Context, req ExecRequirements, job StepJob) (StepOutput, error)
func (d *StepDispatcher) Close() // idempotent: stops and joins wake forwarder
```
Flow (design P1 §3.5): require `engine.StepIdentityFromContext` (else `engine.ErrMissingStepIdentity`); `EnsureIntent` (ledger write failure => return error, never execute); loop { snapshot+telemetry → `Place`; on Wait → `SetWait`, `Status.Publish(waiting_capacity|queued_slot)`, block on Wake/ctx (ctx cancel ends wait) ; `Reserve` (ErrSlotTaken => loop) ; `Status.Publish(running)`; record `engine.PlacementFact` into the context sink; `MarkRunning`; `Runners.For(runner).Run`; classify: exhausted/logged_out → `Health.ApplyOutcome`, `Requeue`, `Events.Record(job.rerouted)`, continue (no workflow retry spent); transient → backoff (exponential part bounded; provider RetryAfter is a minimum and is never shortened; wait remains cancellable), same source, up to TransientMaxAttempts then `Fail` + return error; runner_lost → `Requeue` and continue; other → `Fail` and return error; success → `Complete` (result ref = job id) and return }.
Streams: the dispatcher returns a wrapping channel; the slot is held until the terminal chunk; a classified exhausted/logged_out terminal error BEFORE any token was forwarded re-places transparently; after tokens it applies the outcome to the source and forwards the error (Phase 4 adds turn restart).

Decorators (`domain/runner/selectors.go`, P1 T10):
```go
func NewRoutingChatSelector(inner engine.ChatProviderSelector, d *StepDispatcher, affinity func(provider.ResolvedBinding) Affinity) engine.ChatProviderSelector
func NewRoutingAgentSelector(inner engine.AgentProviderSelector, d *StepDispatcher) engine.AgentProviderSelector
func NewRoutingStreamSelector(inner engine.InteractiveStreamSelector, d *StepDispatcher) engine.InteractiveStreamSelector
func NewRoutedProcessRunner(d *StepDispatcher) engine.ProcessRunner
```
The decorated selector still calls `inner.XFor(b)` first so factory validation errors surface unchanged, then returns a routed provider whose methods build `ExecRequirements` via `GroupFor` and call `Dispatch`. Phase 1 affinity is `AffinityBuiltIn` for all ops; P2 T6 makes `llm_call`/child-input chat `AffinityNone`. Apply the deployment default backend outside routing: `WithDefaultAgentBackend(NewRoutingStreamSelector(inner, dispatcher), defaultBackend)`, so `GroupFor` and the inner stream adapter see the same effective backend.
Built-in runner: `adapter/runner/builtin/runner.go` — `builtin.New(builtin.Deps{Chats engine.ChatProviderSelector; Agents engine.AgentProviderSelector; Streams engine.InteractiveStreamSelector; Process engine.ProcessRunner}) *builtin.Runner` (INNER undecorated selectors + `process.LocalProcessRunner`).

## 12. Health (`domain/runner/health.go`, P1 T13)

```go
type AuthObservation struct { State AuthState }
type CapacityObservation struct { State CapacityState; ModelScope string; ResetAt *time.Time; OrdinaryUsageAllowed *bool; Windows []LimitWindow; Headroom *float64 }
type ProbeResult struct { Auth AuthState; Capacity *CapacityObservation; HealthRevision uint64 } // Capacity nil = probe cannot observe capacity
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
func (m *HealthMonitor) Wait() // joins the loop and quota confirmations on shutdown
```
Notification: `notification.TypeSourceLoggedOut Type = "source_logged_out"` (instance-less; `WorkflowName` = `"runner <runner> source <kind>"`). Adapter notifier: `adapter/notification/source_alerts.go:SourceAlerts` implementing `runner.SourceAlertNotifier` (creates notification row + Teams card via existing notifier).
Probers: `adapter/runner/probe/claude.go:ClaudeAuthProber` (`env -u ANTHROPIC_API_KEY claude auth status`, JSON/exit code), `adapter/runner/probe/codex.go:CodexProber` (`codex.Readiness` + `account/rateLimits/read` via `codex app-server`), `adapter/runner/probe/lunaroute.go:LunaRouteProber` (wraps `adapter/probe` factory). Confirmation: `adapter/runner/probe/confirm.go:DispatchConfirmer` dispatches a bounded 1-token exact-model chat through the `StepDispatcher` with synthetic identity `OperationProbe`. `SourceHealth.ConfirmationModel` is populated from the dispatcher-stamped exhaustion outcome; a different same-family model cannot confirm recovery. No matching authorized profile/binding yields `ErrNoConfirmationModel` and retains probation for a real exact-model request.

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
type ServiceDeps struct { Registry Registry; Ledger Ledger; Events EventLog; Recorder *EventRecorder; Audit AuditRecorder; Visibility RunVisibility; Monitor *HealthMonitor; Telemetry TelemetrySource; Notifier *PoolNotifier; Clock func() time.Time; Window TelemetryWindow /*P2 T12*/; Homes HomeStore /*P3 T14*/; Sessions RunnerSessionIndex /*P4 T14*/ }
func NewService(d ServiceDeps) *Service
type RunnerView struct { Runner Runner; Sources []Source; SlotsUsed int; Queued int; Telemetry *Telemetry; HomedWorkspaces int; Drain *DrainProgress /*P3*/ }
type RunnerDetail struct { RunnerView; Assignments []AssignmentView; RecentTelemetry []Telemetry /*P2 T12*/; Homes []HomeView /*P3 T14*/; Sessions []SessionPinView /*P4 T14*/ }
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
JSON DTOs use separate adapter-local mappers in `adapter/http/runner_dto.go` and `adapter/mcp/runner_dto.go`, with identical JSON tags and fixture-contract tests. MCP does not import the HTTP adapter. Field names camelCase: `id,name,builtIn,stepLimit,labels,cordoned,draining,compat{state,reason},liveness,lastContact,os,runnerVersion,harnessVersions,slotsUsed,queued,homedWorkspaces,sources[{id,kind,credentialRef,unavailableReason,families,health{auth,capacity,resetAt,headroom,congested,lastProbeAt,lastOutcome}}],telemetry{...},drain{...}`.

## 15. Phase 2 names

- Protocol package `adapter/runnerproto`: `const ProtocolVersion = 1`; `type Envelope struct { V int; Kind string; Seq uint64; JobID string; Generation int64; Payload json.RawMessage }` (json: v,kind,seq,jobId,generation,payload); kinds `KindHello="hello"`, `KindHeartbeat="heartbeat"`, `KindAccept="accept"`, `KindReject="reject"`, `KindOutput="output"`, `KindResult="result"`, `KindDispatch="dispatch"`, `KindCancel="cancel"`, `KindProbe="probe"`, `KindProbeResult="probe_result"`, `KindAck="ack"`, `KindFenced="fenced"`, `KindChallenge="challenge"`, `KindChallengeResponse="challenge_response"`, `KindAdmitted="admitted"`, `KindRefused="refused"`; P3 adds `KindDeleteCopy="delete_copy"`, `KindTransferReady="transfer_ready"`; P4 adds `KindSessionExport="session_export"`, `KindSessionImport="session_import"`. `func Encode(e Envelope) ([]byte, error)`, `func Decode(b []byte) (Envelope, error)` (rejects unknown V>ProtocolVersion except Hello/Refused, missing kind, >1 MiB).
- Payloads: `Hello{OS, Arch string; ProtocolVersion int; RunnerVersion string, HarnessVersions map[string]string, WorkspaceRoot, ScratchRoot string, Capabilities []string, Sources []WireSource, Held []HeldJob}`, `HeldJob{JobID string; Generation int64; State string /*running|terminal*/; LastOutputSeq uint64}`, `Heartbeat{Telemetry WireTelemetry; Sources []WireSource; Jobs []HeldJob}`, `Dispatch{Op string; Chat *WireChat; Agent *WireAgent; Process *WireProcess; LeaseUntil time.Time}`, `WireChat{Model, SystemPrompt, Prompt string; MaxTokens int; Temperature float64; OutputSchema json.RawMessage; SessionID string; RequireExecutedModel bool; Stream bool; CredentialKind string; Backend string; ContextWindow string; Effort string; Tools []string}` (no RevealKey/BaseURL/ConnectionRef secrets), `Output{Seq uint64; Token string}`, `Result{OK bool; Chat *WireChatResult; Agent *WireAgentResult; Process *WireProcessResult; Outcome WireOutcome; ErrorText string /*sanitized*/}`, `WireOutcome{Class string; ResetAt *time.Time; RetryAfterMs int64; Windows []WireWindow; HeadroomHint *float64}`.
- Keys (`domain/runner/keys.go`): `type RunnerKey struct { ID uuid.UUID; Name string; RunnerID RunnerID; Hash string; Prefix string; MachinePublicKey []byte; CreatedAt time.Time; ClaimedAt, LastUsedAt, RevokedAt *time.Time }`; `type KeyStore interface { Create(ctx, *RunnerKey) error; GetByHash(ctx, hash string) (RunnerKey, error); List(ctx) ([]RunnerKey, error); Claim(ctx, id uuid.UUID, pub []byte, at time.Time) error /* atomic: only if machine_public_key IS NULL, ErrKeyAlreadyClaimed */; TouchLastUsed(ctx, id, at) error; Revoke(ctx, id, at) error }`; `type KeyService` with `NewKeyService(KeyServiceDeps{Store KeyStore; Registry Registry; Audit AuditRecorder; Revoker SessionRevoker; Clock; DefaultStepLimit int})`, `Create(ctx, name string) (plaintext string, k RunnerKey, err error)` (prefix `ark_`, 32 random bytes base64url, SHA-256 hex hash, name `^[a-z0-9][a-z0-9-]{1,62}$`, registers a runner row with `Liveness offline`), `List(ctx) ([]RunnerKey, error)`, `Revoke(ctx, id uuid.UUID, ack RevokeAck) error` (`type RevokeAck struct{ Acknowledged bool }`; false => `ErrRevokeNotAcknowledged`), `Authenticate(ctx, plaintext string) (RunnerKey, error)`; `type SessionRevoker interface { RevokeRunner(ctx context.Context, id RunnerID) error }` (Hub implements). Machine binding: ed25519; server challenge = 32 random bytes; runner signs `"arbitron-runner-v1:" + keyID + ":" + base64(challenge)`.
- SQLite: `adapter/sqlite/runner_key_repo.go:RunnerKeyRepo`.
- Hub (`domain/runner/hub.go`): `type RunnerConn interface { Send(ctx context.Context, m Outbound) error; Close(code int, reason string) error }`; `type Outbound struct { Kind string; JobID uuid.UUID; Generation int64; Dispatch *DispatchPayload; Ack *AckPayload }` and `type Inbound struct { Kind string; JobID uuid.UUID; Generation int64; Seq uint64; Hello *HelloInfo; Heartbeat *HeartbeatInfo; Output *OutputFrame; Result *RemoteResult; Accept bool; RejectReason string }` (domain mirrors; `adapter/http/runner_connect.go` translates `runnerproto` <-> these). `NewHub(HubDeps{Registry; Ledger; Inbox RemoteInbox; Events *EventRecorder; Health HealthTransitioner; Notifier *PoolNotifier; Clock; Config HubConfig{Heartbeat, SuspectAfter, LostAfter, OfferTTL time.Duration; TelemetryWindow int; ServerWorkspaceRoot string; Compat CompatTable}})`; methods `Admit(ctx, key RunnerKey, conn RunnerConn, hello HelloInfo) (*Session, error)`, `Handle(ctx, s *Session, in Inbound) error`, `Disconnect(s *Session)`, `Tick(ctx) error` (liveness), `Start(ctx)`, `RevokeRunner(ctx, id) error`, `Run(ctx, a Assignment, job StepJob) (StepOutput, error)` (used by remote StepRunner), `Latest() map[RunnerID]Telemetry`, `Window(id RunnerID) []Telemetry` (TelemetrySource).
- `type CompatTable struct { ProtocolMin, ProtocolMax int; RunnerMin, RunnerMaxExclusive string; Harness map[string]string /* harness -> min version */ }`; `func (c CompatTable) Check(h HelloInfo, serverRoot string) Compatibility`.
- Remote StepRunner `adapter/runner/remote/runner.go`: `remote.New(h *runner.Hub) *remote.Runner`; `runner.NewHubRunnerSet(builtin StepRunner, remote StepRunner, reg Registry) RunnerSet` (any non-builtin ID → remote).
- Telemetry: `runner.MachineTelemetry interface { Sample(ctx context.Context) (Telemetry, error) }`; adapters `adapter/telemetry/darwin.go` (`//go:build darwin`), `adapter/telemetry/linux.go` (`//go:build linux`), `adapter/telemetry/null.go` (all fields nil); `telemetry.NewForRoot(root string) runner.MachineTelemetry` (samples the actual workspace volume). Server built-in feeds the hub window through `runner.NewBuiltInTelemetryLoop(t MachineTelemetry, h *Hub, every time.Duration)`.
- Runner binary: `cmd/arbitron-runner/main.go`; domain `domain/runneragent` (`Agent`, ports `Connection`, `JobExecutor`, `LocalSources`, `Spool`, `IdentityStore`); adapters `adapter/runneragent/wsclient`, `adapter/runneragent/config`, `adapter/runneragent/executor`, `adapter/runneragent/spool`, `adapter/runneragent/identity`. Config dir `~/.arbitron-runner/` (`config.yaml`, `runner.env` 0600: `ARBITRON_SERVER_URL`, `ARBITRON_RUNNER_KEY`, `LUNAROUTE_API_KEY` optional, `ARBITRON_EXTRA_HEADERS` optional `Name: value;Name2: value2`), machine key `~/.arbitron-runner/machine_ed25519` (0600).

## 16. Phase 3 names

- `domain/runner/checkpoint.go`: `type Manifest struct { Version int64; Entries []ManifestEntry; Hash string }`, `type ManifestEntry struct { Path string; Size int64; Mode uint32; SHA256 string; Symlink string; Deleted bool }`, `type CheckpointRef struct { RunID uuid.UUID; Version int64; ManifestHash string }`; port `CheckpointStore interface { Commit(ctx, runID uuid.UUID, srcDir string) (CheckpointRef, Manifest, error) /* bootstrap only */; StageBuiltIn(ctx context.Context, runID uuid.UUID, srcDir string, f Fence, resource engine.ResourceFenceRef) (CheckpointRef, Manifest, error); Restore(ctx, ref CheckpointRef, dstDir string) error; Manifest(ctx, ref CheckpointRef) (Manifest, error); Latest(ctx, runID uuid.UUID) (CheckpointRef, bool, error); StageDelta(ctx, runID uuid.UUID, base CheckpointRef, delta io.Reader) (StagedCheckpoint, error); Publish(ctx, staged StagedCheckpoint, f Fence) (CheckpointRef, error); Discard(ctx, staged StagedCheckpoint) error; ReadFile(ctx, ref CheckpointRef, path string) (io.ReadCloser, error); Delete(ctx, runID uuid.UUID) error }`. Adapter `adapter/checkpoint/store.go` (`checkpoint.NewStore(dir string, opts ...Option) *Store`; production requires `WithCatalog`), archives use stdlib `archive/tar` + `compress/gzip` (RR-15 decision: gzip, no new dependency).
- Homes: `type HomeStore interface { GetHome(ctx, runID) (Home, error); AcquireResource(ctx, runID uuid.UUID, owner RunnerID, expectGen int64) (Home, error); HandOff(ctx, runID uuid.UUID, to RunnerID, reason string, bytes int64, expectGen int64, cause HomeMoveCause) (Home, error); SetCheckpoint(ctx, runID uuid.UUID, version int64) error; MarkDirty(ctx, runID uuid.UUID, dirty bool) error; SetNonPortable(ctx, runID uuid.UUID, reason string) error /* P3 T3, §22 RR-08 */; BootstrapLocalCheckpoint(ctx, runID uuid.UUID, ref string) error /* P3 T16, §22 RR-17 */; History(ctx, runID uuid.UUID) ([]HomeMove, error); ListByRunner(ctx, id RunnerID) ([]Home, error); Release(ctx, runID uuid.UUID, expectGen int64) error }`; `type HomeMove struct { RunID uuid.UUID; From, To RunnerID; Reason string; Bytes int64; Generation int64; At time.Time; TargetJobID, InvocationID *uuid.UUID; NodeID, OperationID string; FromGeneration, ToGeneration int64 }`. SQLite `adapter/sqlite/workspace_home_repo.go:WorkspaceHomeRepo`.
- Commit protocol (RR-10) `domain/engine/step_commit.go`: `type StepCommit struct { RunID, InvocationID uuid.UUID; NodeID string; Execution *instance.NodeExecution; Variables map[string]any; NextNodeID string; Completions []StepCompletion; Mode StepCommitMode; Suspension *StepSuspension }`, `type JobFenceRef struct { JobID uuid.UUID; RunnerID string; Generation int64 }`, `type StepCommitter interface { CommitStep(ctx context.Context, c StepCommit) error; CommittedResult(ctx context.Context, invocationID uuid.UUID) (*StepCommit, bool, error) }`; `(*Engine).SetStepCommitter(c)`. SQLite `adapter/sqlite/step_commit_repo.go:StepCommitRepo` (one transaction: promote exact prepared candidates/home pointers, node_executions insert, instances variables + current_node_id, node_invocations committed, every runner_job completed via its receipt, step_commits row; see §21). Errors are propagated (fixes ignored `Create` error).
- Workspace sync service `domain/runner/sync.go`: `NewWorkspaceSync(SyncDeps{Checkpoints CheckpointStore; Homes HomeStore; Leases ResourceLeaseStore; Ledger Ledger; Tokens TransferTokens; Sizes WorkspaceSizer; Events *EventRecorder; Status *ExecutionStatusProjector; Clock})`, `PreparePull(ctx, a Assignment, runID) (PullPlan, error)`, `AcceptPush(ctx, f Fence, runID uuid.UUID, staged StagedCheckpoint) (CheckpointRef, error)` (prepared candidate only; §21), `CommitBuiltIn(ctx, f Fence, runID uuid.UUID, dir string) (CheckpointRef, error)` (prepared candidate only; §21), `RestoreBuiltIn(ctx, runID uuid.UUID, dir string) error`.
- Transfer tokens `domain/runner/transfer.go`: `type TransferToken struct { Token string; JobID uuid.UUID; Generation int64; RunnerID RunnerID; RunID uuid.UUID; Direction string /*pull|push|bundle*/; BaseRef, BundleHash, MaintenanceOperationID string; Resource engine.ResourceFenceRef; ExpiresAt time.Time }`; `TransferTokens interface { Issue(ctx context.Context, scope TransferScope, ttl time.Duration) (TransferToken, error); Validate(ctx context.Context, token string, runner RunnerID) (TransferToken, error) }`. HTTP `adapter/http/runner_transfer_handler.go` routes: `GET /api/v1/runners/transfers/manifest`, `GET /api/v1/runners/transfers/files?path=`, `PUT /api/v1/runners/transfers/push/{part}`, `POST /api/v1/runners/transfers/push/commit`, `GET /api/v1/runners/transfers/bundles/{hash}`; headers `Authorization: Runner <key>`, `X-Arbitron-Transfer: <token>`.
- Bundles: `adapter/process/bundle.go:BuildScriptBundle(scriptPath string) (engine.ScriptBundleRef, []byte, error)`; `domain/runner/bundles.go:BundleStore` port; SQLite-less filesystem store `adapter/checkpoint/bundles.go`.
- Recovery: `domain/runner/recovery.go:NewPoolRecovery(...)`, `(*PoolRecovery).Recover(ctx) error`; engine `(*Engine).RecoverRunningInstances(ctx context.Context, rec RecoveryPolicy) error` replacing `RecoverCrashedInstancesContext` in `startRuntime`; `type RecoveryPolicy interface { PrepareRun(ctx context.Context, inst *instance.Instance) (RecoveryAction, error) }` (`RecoveryResume`, `RecoveryReplayCommitted`, `RecoveryFail`).
- Local checkpoints for non-portable homes (P3 T16, §22 RR-17 decision B): `domain/runner/checkpoint.go`: `type LocalCheckpointRef struct { RunID uuid.UUID; Version int64; ManifestHash string }` (`String()` = `"local:<runID>@<version>:<manifestHash>"`, `ParseLocalCheckpointRef`), port `LocalCheckpointStore interface { StageLocal(ctx, runID uuid.UUID, srcDir string, version int64) (LocalCheckpointRef, error) /* prepared only */; RestoreLocal(ctx, ref LocalCheckpointRef, dstDir string) error; PruneLocal(ctx, runID uuid.UUID, keep LocalCheckpointRef) error; Delete(ctx, runID uuid.UUID) error }`, `var ErrNoRecoveryBase error`. `Home` gains `LocalCheckpoint string` (committed ref; `""` = none), persisted in `workspace_homes.local_checkpoint` (migration 044). Adapter `adapter/checkpoint/local.go`: `checkpoint.NewLocalStore(dir string, opts ...LocalOption) *LocalStore`, `WithLocalFileCreator`; production root `<cfg.Runners.CheckpointDir>/local`. It does a plain copy written to a temp dir and renamed into place, keeps only the latest committed copy, and does no tar or Git materialization. Domain policy `domain/runner/local_checkpoint.go`: `NewLocalCheckpoints(LocalCheckpointDeps{Local, Homes, Checkpoints, Leases}) (*LocalCheckpoints, error)` with `Take`, `Bootstrap`, `Restore`, and `NewLocalCheckpointRetention(inner engine.StepCommitter, local LocalCheckpointStore) (*LocalCheckpointRetention, error)`. `SyncDeps.Local` and `BaselineDeps.{Homes, Local}` are required. `runner.JoinCheckpointDeleters(...CheckpointDeleter) CheckpointDeleter` feeds `RemovalDeps.Checkpoints`. The root sets `eng.SetStepCommitter(mobility.StepCommitter)`.
- Drain: `(*Service).Drain(ctx, id RunnerID) (DrainProgress, error)`; `type DrainProgress struct { ActiveSteps int; HomedWorkspaces int; Sessions int; Blocked []DrainBlocker; Complete bool }`, `type DrainBlocker struct { Kind string /*workspace|session*/; RunID *uuid.UUID; Group string; Reason string }`.
- Workspace removal: `runner.NewRemovalNotifier(...)` implementing `engine.WorkspaceRemovalHook interface { WorkspaceRemoved(ctx context.Context, runID uuid.UUID) }`; `(*Engine).SetWorkspaceRemovalHook(h)`.

## 17. Phase 4 names

- `domain/engine/session_portability.go`: `type SessionBundle struct { Backend, SessionID, HarnessVersion, OriginWorkDir, Checksum string; Data []byte }`; `type SessionPortability interface { Export(ctx context.Context, sessionID, workDir string) (SessionBundle, error); Import(ctx context.Context, b SessionBundle, workDir string) (sessionID string, err error) }`; `var ErrBundleRequiresRebuild = errors.New(...)`. Adapters `adapter/claudecli/session_portability.go:SessionPortability`, `adapter/codex/session_portability.go:SessionPortability` (strips reasoning; compaction => ErrBundleRequiresRebuild).
- `domain/engine/session_checkpoints.go`: `type SessionKey struct { RunID uuid.UUID; InvocationID uuid.UUID; TurnIndex int }`; `type SessionCheckpointStore interface { Put(ctx, SessionKey, SessionBundle) (ref string, err error); Get(ctx, ref string) (SessionBundle, error); Prune(ctx, runID, invocationID uuid.UUID, keepRef string) error }`; adapter `adapter/checkpoint/sessions.go`.
- `domain/engine/turn_lease.go`: `type TurnLease struct { Key string /* "run:<id>" or "resolution:<identity>" */; Token uuid.UUID; Generation int64; ExpiresAt time.Time }`; `type TurnLeaseStore interface { Acquire(ctx, key string, ttl time.Duration) (TurnLease, error) /* ErrInteractiveTurnInProgress */; Renew(ctx, l TurnLease, ttl time.Duration) (TurnLease, error); Release(ctx, l TurnLease) error; BumpGeneration(ctx, l TurnLease) (TurnLease, error); Current(ctx, key string) (TurnLease, bool, error) }`; `type TurnIntent struct { ID uuid.UUID; RequestID uuid.UUID; Binding AcceptedTurnBinding; LeaseKey string; RunID, InvocationID uuid.UUID; NodeID string; TurnIndex int; Kind string /*first|message|end|abandon|move*/; Prompt, SystemPrompt, Nonce, Message string; Model string; Tools []string; PriorCheckpointRef string; SessionGeneration int64; OperationID string; CreatedAt time.Time; CompletedAt *time.Time }`; `type TurnIntentStore interface { Persist(ctx, TurnIntent) error; Complete(ctx, id uuid.UUID) error; Pending(ctx) ([]TurnIntent, error); PendingFor(ctx, leaseKey string) (*TurnIntent, error) }`. SQLite `adapter/sqlite/turn_lease_repo.go`.
- Mover `domain/runner/session_mover.go`: `NewSessionMover(MoverDeps{...})`, `Move(ctx, lease engine.TurnLease, req MoveRequest) (MoveResult, error)`; `type MoveRequest struct { RunID, InvocationID uuid.UUID; NodeID string; Reason string /*exhausted|logged_out|drain|runner_lost*/; From RunnerID; Requirements ExecRequirements }`; `type MoveResult struct { To RunnerID; SessionID string; Lossy bool; Notice string; Generation int64 }`. Engine port `engine.SessionMover interface { MoveSession(ctx context.Context, lease TurnLease, req SessionMoveRequest) (SessionMoveResult, error) }` (domain/engine mirror types so engine does not import runner).
- Events: `interactive.turn_restarted` `{instanceId,nodeId,turnIndex,generation}`; `interactive.context_notice` `{instanceId,nodeId,notice:"tool details and reasoning may be lost",lossy:true}`; `session.moved` `{from,to,reason,lossy}`.
- (unstick 2026-11-23, P4 T10) Active session selection: `adapter/sqlite/session_pin_index.go:activeSessionExecSQL`. It selects the record whose `session_invocation_id` is the OPEN `node_invocations` row of (run, node), ordered by `session_generation DESC, started_at DESC, rowid DESC`. `MAX(attempt)` is never used. A zero or NULL invocation is never active. It is shared by `ListPinned` (P4 T10) and `ListByRunner` (P4 T14). The index reports a `source_id` only when it belongs to the pinned runner.
- (unstick 2026-11-23, P4 T10) Recovery coordinator:
  - `runner.TurnRecoveryDeps{Ledger; Leases engine.TurnLeaseStore; RecoveryLeases engine.TurnLeaseRecoveryStore; Adopter TurnAdopter; StopEvidence TurnStopEvidence; TTL; Clock}`.
  - `runner.ErrTurnRecoveryNotReady` and `runner.ErrTurnRecoveryUnproven`.
  - `runner.NewTurnStopEvidence(TurnStopEvidenceDeps{Registry; Presence RunnerPresence; Ledger; Stopper BuiltInStopper; Grace; Started; Clock}) (TurnStopEvidence, error)`.
  - `(*Hub).Connected(RunnerID) bool` and `(*Hub).Revoked(RunnerID) bool`.
  - `runner.TurnPreparer`, `runner.SessionMoveResumer`, `runner.NewFirstTurnResumeRouter`.
  - `engine.PendingFirstTurnResumer`, `(*Engine).ResumeFirstTurn`, `engine.NewAdoptedTurns`/`AdoptedTurns`, `engine.ContextWithAdoptedTurn` and `engine.ContextWithRecoveredTurnLease` (adopted by `holdTurnLease` and never re-acquired).
  - `engine.ErrAdoptedTurnIncomplete`, `engine.ErrNotSessionTurnIntent`.
  - `engine.AdoptedTurn.PreparedBundleRef`.
  - `SessionRecoveryDeps` drops `Adopter` and gains `Coordinator`, `Leases`, `FirstTurns` and `Moves`.
  - StartupScan runs BEFORE `RecoverRunningInstances`, which skips runs claimed by `ResumeFirstTurn`.
- P4 T11 resolution resources (unstick 2026-11-23 additions; exact names in `2026-11-20-phase-4-task-11.md` Produces): `engine.ResolutionResourceKey(id) = "resolution:<resourceUUID>"`, `ResolutionResource.Validate`, `ContextWithResolutionResource`/`ResolutionResourceFromContext`, `ErrMissingResolutionResource`/`ErrObsoleteResolutionAttempt`/`ErrResolutionNotCommitted`; `ResolutionResourcePort` adds `GetResolution(ctx, id)` and `CloseResolution(ctx, id)`; `(*conversationHandler).SetResolutionResources(port)`; `SessionMoveRequest.Resolution *ResolutionResource`; runner `ResourceKindWorkspace|ResourceKindResolution`, `ResolutionDescriptor`, `(*ResourceDescriptor).Validate`, `WorkspaceResourceID(er, job)`, `ErrMissingResourceDescriptor`, `ResolutionPins.PinForResolution(ctx, invocationID, identity)` (implemented by `sqlite.SessionPinRepo`), `MoveRequest.Resource *ResourceDescriptor`, registry `MaterializedRef`/`CloseResolution`, `ErrResolutionClosed`/`ErrResolutionResourceConflict`; adapter/git `ResolutionResourceRoot()`, `WithPortableGit(fn)` (injects P3 T2 `checkpoint.MaterializePortableGit`), `WithResolutionMaxBytes`, `ErrResolutionPortability`/`ErrResolutionTooLarge`; migration 045 table `resolution_resources`.

- P4 T6 rebuild additions (unstick 2026-11-23): `engine.TurnCommit` also carries `RequireWorkspace bool`, `RequestID uuid.UUID`, `TurnIndex int` and `ResourceRunID uuid.UUID` (Nil = the run workspace; P4 T11 sets `ResolutionResourceID`); `engine.ErrMissingTurnReceipt`, `engine.ErrUnknownSessionBackend`; `(*SessionCheckpointDeps).commitOperation` (End/Abandon, no stream fence); `checkpoint.SessionStore.SetMaxBundleBytes(int64)`, `CollectGarbage(ctx, live func(ref string) bool, olderThan time.Time) (int, error)`, `checkpoint.DefaultMaxSessionBundleBytes`, `checkpoint.ErrSessionBundleTooLarge`; `runner.ParseTurnWorkspaceRef(string) (CheckpointRef, error)` (inverse of `FormatTurnWorkspaceRef`, format `<resourceUUID>@<version>:<manifestHash>`). `TurnWorkspaceCheckpointer` has `StageTurn` only. Native session bundles exist for `claude-code`/`codex`; `opencode`/`pi` turns commit transcript+workspace without a bundle and later moves rebuild visibly.

- P4 T12 (unstick 2026-11-23):
  - Protocol: `runnerproto.KindSessionImport`/`OpSessionImport` only, with no export kind or op. `WireChat.NativeCheckpoint`/`SessionExportToken`, `WireChatResult.Session *WireSessionBundle`, and `Dispatch.Resource *WireResource{ID, Kind, WorkDir, FenceKey, FenceOwner, FenceGeneration, FenceToken, CheckpointRef}`.
  - Runner-agent mirror: `runneragent.ResourceJob`.
  - Durable staging: port `runner.SessionStaging{Put, Get, Delete, Seal(f, checksum, size) (ref), Open(ref), GC(ctx, before, keep)}` and `runner.StagedBundleRef(f, checksum)` = `staged:<job>/<gen>/<sha256>`. The production store is `checkpoint.OpenSessionBlobs(dir, perBlob, total) (*checkpoint.SessionBlobs, error)` in adapter/checkpoint; `runner.NewSessionBlobs` is the memory test double.
  - Prepared receipts: `runner.PreparedWorkspaceReceipts{PreparedFor(ctx, Fence) (engine.WorkspaceCompletion, bool, error)}`, implemented by `sqlite.NewPreparedReceiptRepo(db)`.
  - Result fields: `RemoteResult.Completion *engine.StepCompletion` (hub-sealed). `RemoteChatResult.Session *SessionBundleMeta` with a server-only `PreparedBundleRef`.
  - Errors: `runner.ErrMissingPreparedReceipt`, `runner.ErrSessionBundleMissing`.

- **Decided (user, 2026-11-23)**, P4 T2 Cycle 2c / P4 T10 Cycle 4 / P4 T13 Cycle 2: `TurnRecoveryProof` gains `StaleHolder bool`. `turn_leases` gains `boot_id TEXT NOT NULL DEFAULT ''` (045, authorized). `(*sqlite.TurnLeaseRepo).SetBootID(bootID string)` takes the same process boot ID as `NewResourceLeaseRepo`. A stale-holder proof names only the intent. The repo accepts it only when the stored holder boot ID and its own boot ID are both non-empty and differ, and no `runner_jobs` row exists for the intent's operation. Same boot is refused, a missing boot ID is refused, and success fences at generation+1.
- **Decided (user, 2026-11-23)**, P4 T3: recoverable turn errors (`ErrTurnLeaseLost`, `ErrSessionMoveRequired`, `ErrNoSessionTarget`, context cancellation/deadline) keep the intent pending; definitive errors complete it.
- **Decided (user, 2026-11-23)**, P4 T6: `opencode`/`pi` interactive turns commit transcript + workspace with no native bundle; a move rebuilds the conversation from the transcript with the loss notice. These backends are not refused.

## 18. Phase 5 names (dashboard)

`dashboard/src/api/types.ts` additions: `RunnerView`, `RunnerDetail`, `RunnerSource`, `SourceHealth`, `RunnerTelemetry`, `PoolEvent`, `RunnerKey`, `RunnerKeyCreateResponse`, `ExecutionStatus`, `PlacementRecord`, `HomeMove`, `DrainProgress`, `SessionMove`. API fns in `dashboard/src/api/runners.ts`: `listRunners()`, `getRunner(id)`, `listPoolEvents(limit)`, `listRunnerKeys()`, `createRunnerKey(name)`, `revokeRunnerKey(id)`. Hooks `dashboard/src/hooks/usePool.ts`, `useRunner.ts`. Routes `/pool`, `/pool/runners/:id`.

## 19. Reviewed producer graph and implementation order

A design's RR labels describe the pre-implementation baseline. A dependency is
closed **for planning** only when an owning task produces it, names its runtime
input and wires it into its consumer; planned code is not evidence that it is
already implemented. The former task-level `ESCALATION:` evidence/platform
items were resolved on 2026-11-22 (§22) and replaced by `DECISION` paragraphs in
the consuming tasks. A conservative fallback closes a dependency only when it meets the spec;
chat-only admission or built-in-only recovery does not prove workspace mobility.

| Design dependency | Producer tasks and real composition root | Required downstream sink |
|---|---|---|
| RR-01 identity, registry, ledger, authz and events | P1 T2–T8, T15; P1 T16 `initDomainWithModelDependencies` | SQL job/invocation/trace rows, authorized REST/MCP and filtered stream |
| RR-02 complete request eligibility and local credential routing | P1 T3/T4/T10/T13; P2 T1/T4/T6/T9; P1 T16 and P2 T10 roots | selected runner executes exact model/backend/route/tools; unsupported requests never offered |
| RR-03 outcomes, recovery probes, floors and logout alerts | P1 T11–T13; P1 T16; runner-local probes P2 T9 | actual process/RPC bytes → classified outcome → registry transition → reroute/alert; floors evidence-inferred per §22 RR-03a–d |
| RR-04 validated policy/config and clocks | P1 T1, P2 T2, P3 T1, P4 T1; phase roots P1 T16/P2 T10/P3 T13/P4 T13 | configured limits/timeouts affect production behavior; invalid config refuses startup |
| RR-05 keys, protocol, durable replay, runner binary/install | P2 T1–T6/T8–T11; server P2 T10 and runner P2 T9 | real WebSocket → durable inbox → ACK → result; launch context per §22 RR-05 (per-user LaunchAgent) |
| RR-06 machine measurements | P2 T7; P2 T9/T10; detail P2 T12 | samples of execution volume → hub window/placement/API; unsupported measurements stay explicit; macOS thermal via notifyutil per §22 RR-06 |
| RR-07 common execution root | P2 T4/T9/T11 and P2 T10 root comparison | matching usable absolute root (`/opt/arbitron/workspaces`, §22 RR-07) admits workspace jobs; mismatched roots are chat_only |
| RR-08 checkpoints, ownership, transfer, drain, cleanup | P3 T1–T3/T6/T7/T9/T11–T14/T16; P3 T13 roots | committed files restored on another runner, fenced writer/deletion, durable drain progress |
| RR-09 process descriptors, bundles, local environment | P1 T9/T10; P3 T8/T9; P1 T16/P3 T13 | real remote process sees relative bundle entrypoint, canonical workspace, runner environment; remote scripts only with `portable: true` and declared `requires` (§22 RR-09a/b) |
| RR-10 crash-safe completion/replay | P3 T2/T3/T5/T6/T10; P4 T6/T10; roots P3 T13/P4 T13 | validated result + exact prepared refs + all fences + continuation commit atomically, replay without execution |
| RR-11 branch/gather and resolution resource mobility | P3 T13; P4 T11/T12/T13 | remote staging turn commits and server gather consumes restored committed staging without touching branch/parent copies |
| RR-12 durable turns, leases, accepted request and recovery | P4 T1–T3/T6–T10; P4 T13 | restart of a waiting run adopts live turn or resends exact accepted message under current fences |
| RR-13 bundle export/import and visible fallback | P4 T4–T6/T8/T12; P4 T13 | native import preserving available bytes or automatic transcript rebuild with durable loss notice |
| RR-14 cross-account uncertainty | P4 T4/T5/T8 conservative policies | always strip Codex reasoning; uncertain Claude fidelity/refused native import visibly falls back; optional fingerprint is not consumed |
| RR-15 archive codec | P3 T2/T8/T9 | standard-library tar+gzip round trip and integrity checks; no zstd dependency |
| RR-16 read models and live clients | P1 T7/T14/T15, P2 T3/T12, P3 T12/T14, P4 T1/T14, P5 T1–T8 | real authorized routes and aggregate graph → browser views, durable notices; rebuilt embedded dist |
| RR-17 non-portable (partial-clone) recovery base | P3 T16 (local checkpoint store/policy/receipt/root); consumed by P3 T10 recovery, P3 T12 drain blocker, P4 T6 turn commit | interrupted step on a non-portable home restored from its latest committed local checkpoint and repeated; no base → durable `job.recovery_blocked` |

Execute phases in numeric order. Within each phase, numeric task order is the
default except P1 T9 must precede T8: T9 produces ProcessSpec/ProcessResult, which T8 StepJob/StepOutput consume. P1 order is 1–7, 9, 8, 10–16. P3 T16 (RR-17 B) is the other exception: its Cycles 1–5 run after P3 T6 and before P3 T10, and its Cycle 6 root wiring runs after P3 T13 Cycle 5. P3 order is 1–6, 16 (Cycles 1–5), 7–12, 13 (through Cycle 5), 16 (Cycle 6), the rest of 13, 14, 15. When a contract is needed before its implementation task (for example,
P3 T5 uses receipt types consumed by T6, or P4 T6 consumes later remote adapters),
its earlier owner defines the port and test double; later tasks supply and wire
the adapter. Never import a later task's concrete adapter into domain code.
Phase root tests depend on every preceding producer, including appended contract
cycles. P2 T13 and P3 T15 close remote health and native-source production wiring before their phases are accepted; §24 records their order. P2 T12, P3 T14 and P4 T14 finish their read surfaces before Phase 5 starts.

## 20. Mandatory test and wiring gates for every task

Every task executes STRICT TDD: add a behavior assertion, run the named focused
command and observe its intended failure, implement only that behavior, rerun
to green, then refactor. This applies separately to happy/success, rejected
failure, injected error and boundary/edge cases. A test added after a complete
implementation and made red with a temporary mutation does not satisfy TDD.
Mutation checks can strengthen an already completed red/green cycle.

For every newly introduced dependency or field, its owning task must name the
producer and carry it through real construction/injection and serialization to
the final sink. Phase root tests drive `initDomainWithModelDependencies`,
`startRuntime`, the runner binary constructor, or `App` as appropriate. Assert
non-empty production-derived values and effects: persisted rows, restored file
bytes, exact provider request arguments, HTTP/MCP payloads and rendered views.
Removing the injection, emptying the inventory or dropping a load-bearing
argument must fail the relevant test. Fakes must inspect those arguments.
A port-only unit test or an upload-success assertion is insufficient evidence
for execution, recovery, or successful checkpoint publication.

Snapshots read from multiple REST endpoints have no common atomic cursor.
Pool and execution-status WebSocket frames invalidate/coalesce authoritative
reloads; they must not patch state while assuming event-log rows were reflected
in a different response. Serialize reloads or discard superseded response
epochs. Deduplicate the recent-event log separately. Test delayed frames and
out-of-order HTTP completion through mounted routes and queue views.

## 21. Fenced completion receipts (Phases 3 and 4)

The following extends §§11/16/17 once Phase 3 is enabled. Earlier Phase-1
immediate `Ledger.Complete` behavior is replaced for recoverable executions;
it must not release ownership before executor validation and durable commit.
Placement history is presentation data and must never be used as the authority
for a completion fence, checkpoint selection or a list of suboperations.

```go
// domain/engine/resource_fence.go — P3 T1 defines before P3 T2/T3 consumers.
type ResourceFenceRef struct { Key, Owner string; Generation int64; Token uuid.UUID }
// domain/engine/step_completion.go — P3 T5 owns receipt types and sink.
type WorkspaceCompletion struct { Ref string; Resource ResourceFenceRef; NonPortable bool /* P3 T5, RR-08 */; LocalRef string /* P3 T16, RR-17 B: set iff NonPortable */ }
type StepCompletion struct { JobFence JobFenceRef; Workspace *WorkspaceCompletion }
// StepCommit carries Completions []StepCompletion (all operations, including
// child-input preparation), instead of inferring one job from placement history.
type StepCompletionSink struct { /* synchronized receipt collection */ }
func NewStepCompletionSink() *StepCompletionSink
func ContextWithStepCompletionSink(context.Context, *StepCompletionSink) context.Context
func StepCompletionSinkFromContext(context.Context) (*StepCompletionSink, bool)
func (s *StepCompletionSink) Record(c StepCompletion) error
func (s *StepCompletionSink) Completions() []StepCompletion
// The sink records every dispatched operation and is read by the engine's
// commitNodeSuccess / checkpointAndCommit after executor validation.

// domain/runner/resource_lease.go — P3 T3 defines; 044 persists workspace_leases.
type ResourceLeaseStore interface {
    Claim(ctx context.Context, key, holder string, owner RunnerID) (engine.ResourceFenceRef, error)
    Check(ctx context.Context, f engine.ResourceFenceRef) error
    Release(ctx context.Context, f engine.ResourceFenceRef) error
    Supersede(ctx context.Context, old engine.ResourceFenceRef, holder string, owner RunnerID) (engine.ResourceFenceRef, error)
}
```
**Superseded (unstick 2026-11-23):** the interface above is replaced by the exact
`ResourceLeaseStore` in §25 (adds `ClaimJob`, `Active`, and a `SupersedeProof`
argument to `Supersede`).

A home identifies location; it is not a writer lock. `Claim` rejects a second
active holder even on the same runner. Resource leases have no automatic expiry
that bypasses liveness grace. `Supersede` is permitted only with transactional
fencing of the old operation after confirmed loss/revocation or explicit
cancellation and process stop. Unfenced `HomeStore.SetCheckpoint`/`MarkDirty`
cannot publish terminal progress.

`CheckpointStore.Publish`/built-in staging produce immutable **prepared**
candidates tagged with exact job and resource receipts. `Latest` selects only
committed versions. P3 T5 `StepCommitRepo` and P4 T6 `TurnCommitRepo` atomically
validate every job/resource/session fence, promote the exact candidates, update
home checkpoint pointers, persist result/messages/variables/continuation and
complete the logical intent. Missing workspace receipts for a workspace-writing
operation, missing job receipts, stale tokens or references to a different job
fail closed with no progress. All checks and writes use the same `*sql.Tx` via
transaction-aware helpers; calling a repo through its `*sql.DB` while that
transaction holds the single connection would deadlock. Tests use the normal
single-connection SQLite setup and a bounded context to catch that failure. Never select "newest checkpoint for job" as a
substitute for an explicit reference. Artifact upload and `PublishTurn` after a
DB commit are not completion boundaries. P3 T13 server-side group mutations use
the same resource lease and a transactional resource committer. Orphan prepared
artifacts are reclaimed only if no committed record references them.

Restart and failed validation restore the previous committed checkpoint. Test
crashes before candidate preparation, after preparation, inside the transaction
and after commit; successful result replay must not call the executor again.
Tests for child-input operations require all receipts, not only the last one.


### Accepted turn binding (P4 T3; consumed by P4 T8–T11)

```go
// domain/engine/turn_intent.go
type AcceptedTurnBinding struct {
    Kind provider.ProfileKind
    Provider, Model, Backend, ConnectionRef string
    BackendExplicit bool
    AllowedTools []string
    ContextWindow provider.ContextWindow
    Effort provider.Effort
    Pinned bool
    OpaquePinRoute string
    RequireExecutedModel bool
    RouteConfigHash string
}
func CaptureAcceptedTurnBinding(b provider.ResolvedBinding) (AcceptedTurnBinding, error)
type TurnBindingRestorer interface {
    RestoreTurnBinding(ctx context.Context, accepted AcceptedTurnBinding) (provider.ResolvedBinding, error)
}
```

P4 T3 freezes the effective accepted backend/model/constraints and hashes
canonical route Provider/BaseURL/Extras into `RouteConfigHash`; the persisted
snapshot contains neither credentials nor `RevealKey`. The turn intent stores
`request_id` and `accepted_binding` before accepting a submission. First-turn
success also stores this binding in the durable conversation, feeding idle
moves without re-resolving an edited profile. Adapter
`adapter/provider/turn_binding.go:NewTurnBindingRestorer(store provider.ConnectionStore)`
loads the original live connection, enforces access/revocation and unchanged
route hash, and attaches lazy key retrieval while restoring accepted model,
backend, tools, pin/evidence, effort and context. A changed route fails visibly;
it cannot silently replace the accepted request. P4 T13 injects the restorer
into executor, conversation handler, resolution bridge and startup recovery.

### Additional production collaborators and their owners

- P1 T6 stores `ExecRequirements` JSON on `runner_jobs` from `EnsureIntent`.
  `Reserve` revalidates that persisted request and the latest inventory, health,
  runner state, step limit and probation single-flight in its write transaction;
  stale decisions cannot bypass cordon or run incompatible harnesses.
- P2 T1/T9 produce source `Capabilities` from verified harness version/operation axes and runner-local endpoint configuration; P2 T4 persists advertised evidence and P2 T6 compares full `ExecRequirements`. Non-empty matching endpoint fingerprint is required for remote routes; a same-named connection alias alone does not prove equivalent routing. Hosted LunaRoute endpoint validation remains unchanged; a custom LunaRoute URL
  is rejected before dispatch, never silently replaced by a runner default.
  Generic connection-backed routes remain built-in-only. Source `UnavailableReason` is persisted and serialized beside capabilities in both protocol and operator DTOs. P2 T10 tests incompatible route/context/effort/tool/model-evidence requests receive no offers.
- P2 T4 owns `runner.RemoteInbox`; P2 T2 owns migration 043's `runner_inbox`;
  P2 T4 owns `adapter/sqlite/remote_inbox_repo.go:RemoteInboxRepo` and P2 T10
  injects it into `HubDeps.Inbox`. Accepted output, terminal result and ACK cursor
  are persisted under the job fence before any ACK; restart/reconnect reload
  that journal, never an in-memory cursor. A crash before/after ACK must preserve
  exactly the accepted frames and unconsumed terminal result.
- P3 T10 owns `runner.BuiltInStopper.StopAndWait(ctx context.Context, f Fence) error`
  and `RecoveryDeps.Stopper`; P3 T13 installs its supervised process-group/guardian
  adapter in both built-in harness command execution and `LocalProcessRunner`.
  A durable launch permit and PID start marker identify surviving descendants.
  Recovery fences, stops and waits, then restores and retries. Failed stop cannot
  restore a workspace while a surviving writer can still mutate it.
- P4 T1 persists `node_executions.session_invocation_id`. Pin, move, recovery and
  session-index reads carry the exact invocation/session execution ID; maximum
  retry attempt for `(run,node)` is not a session identity because loops reset
  retry attempts. A repeated node with an older high-retry session must select
  the current visit, never its predecessor.
- P4 T12 emits interactive stream identity (invocation/session, turn index and
  session generation) in history, token, message and restart frames. P5 T7
  updates `useInteractiveSession` and `InteractivePanel`: restart clears partial
  text, superseded-generation frames are ignored, reconnect history restores
  committed text and durable context-loss notices render. A test must drive
  actual client event handlers; a decoder-only test does not prove continuity.

### Turn commit and lease recovery (P4 T2/T6/T10)

P4 T6 `engine.TurnCommit` carries `InvocationID uuid.UUID`,
`AcceptedBinding AcceptedTurnBinding` and `Completions []StepCompletion` alongside
the exact session lease token/generation, intent ID, conversation execution,
bundle ref and target pin. There is no filesystem publication after the DB
transaction: job/resource/session checks and workspace promotion are inside it.

```go
// domain/engine/turn_lease.go — defined P4 T2, consumed P4 T10.
type TurnRecoveryProof struct {
    IntentID uuid.UUID
    Adopted bool
    Jobs []JobFenceRef
    Resource *ResourceFenceRef
}
type TurnLeaseRecoveryStore interface {
    RecoverTurnLease(ctx context.Context, old TurnLease, proof TurnRecoveryProof, ttl time.Duration) (TurnLease, error)
}
// domain/runner/remote_inbox.go — P2 T4 defines; P2 T10 wires.
type InboxReceipt struct { Fence Fence; LastOutputSeq uint64; Result *RemoteResult }
type RemoteInbox interface {
    AcceptOutput(ctx context.Context, f Fence, frame OutputFrame) (uint64, error)
    AcceptResult(ctx context.Context, f Fence, result RemoteResult) error
    Receipt(ctx context.Context, f Fence) (InboxReceipt, bool, error)
    Outputs(ctx context.Context, f Fence, after uint64) ([]OutputFrame, error)
}
```

Ordinary `Acquire` never steals an expired occupied turn token. P4 T10's
coordinator produces a recovery proof only after exact live-job adoption, or
after confirmed loss/revocation/cancel plus process-stop acknowledgment and
durable job/resource fencing. SQLite validates the current intent, lease and
job/resource facts with CAS. Adoption preserves the session generation; replay
increments it. P4 T13 injects the same SQLite turn lease repository as
`RecoveryLeases`; nil or fabricated proofs cannot permit overlapping turns.

### Checkpoint catalog candidate (P3 T2)

```go
// domain/runner/checkpoint.go
type CheckpointCandidate struct {
    Ref, Base CheckpointRef
    Job *Fence
    Resource engine.ResourceFenceRef
    ArchivePath string
    SizeBytes int64
}
type CheckpointCatalog interface {
    Publish(ctx context.Context, c CheckpointCandidate) error // prepare only
    Bootstrap(ctx context.Context, c CheckpointCandidate) error
    Latest(ctx context.Context, runID uuid.UUID) (CheckpointRef, string, bool, error)
    Get(ctx context.Context, runID uuid.UUID, version int64) (CheckpointRef, string, error)
    DeleteRun(ctx context.Context, runID uuid.UUID) error
}
```

`Publish` receives the actual resource receipt rather than claiming to validate
one absent from its arguments. Nil job is permitted only by `Bootstrap`, which
transactionally rejects any existing committed checkpoint or active job/resource
writer. Remote `StagedCheckpoint.Resource` comes from the durable scoped
transfer session, never from the uploaded archive. The normal instance and group
checkpoint builders copy/materialize Git objects in a private staging directory;
they never rewrite a user's source repository to remove alternates. Missing
promisor objects never produce a falsely portable snapshot: per §22 RR-08 the
home is marked non-portable with a visible reason and pinned to the built-in
runner (no network fetch, no step failure).

### Version and telemetry eligibility (P2 T1/T4/T7/T9/T10; P3 T4/T13)

A release server `vM.m.p` admits runner versions in `[vM.m.p, vM.(m+1).0)`
with the same protocol/capability epoch. `CompatTable.RunnerMin` and
`RunnerMaxExclusive` carry that range. An unstamped server uses the explicit
`unreleased` sentinel and admits no remote execution; incompatible machines
remain visible with a reason. Built-in local operation still starts without
runner keys. Root fixtures stamp deterministic semver instead of accepting
unknown versions via an empty minimum.

`PlacementPolicy.TelemetryMaxAge` is `3 * cfg.Runners.HeartbeatSeconds`, 15 seconds
by default. Zero, future or older sample timestamps provide unknown metrics;
they cannot trigger disk or health load moves. The runner wire telemetry carries
`SampleAgeMs`, measured monotonically by the sampler; the hub validates it and
normalizes `SampledAt` to server receipt time minus sample age. Remote wall-clock
skew cannot fabricate fresh samples. Built-in sampling uses the server clock.
P2 T7 owns age measurement, P2 T1/T9 wire it, P2 T4 normalizes it, P2 T10 and P3
T13 feed policy and the dashboard shows stale/unavailable values explicitly.

## 22. Resolved external inputs and policy decisions (2026-11-22)

These ten inputs, plus RR-17, were open at the planning gate. Each is now closed by a named
producer (observed evidence under `docs/evidence/remote-runners/`) or by an
explicit human decision recorded here, in the phase designs' provenance tables
and in every consuming plan task. RR-17, the recovery consequence of RR-08 recorded in §24, was decided on 2026-11-22 (option B, row below). Implementers follow the consuming task's
`DECISION (2026-11-22, …)` paragraph; nothing below is gated on further evidence.

| ID | Consumer task | Producer / decision | Rule an implementer follows |
|---|---|---|---|
| RR-03a | P1 T11 Claude outcome decoder and version table | **Decision C:** synthetic fixtures derived from `docs/evidence/remote-runners/research-exhaustion.md` are the authoritative coverage | Unrecognized shapes fail closed to `other`. Real per-version capture certification, `capture.sh` and the captured-matrix gate are **Non-Goals**. `SupportedVersions` floors are evidence-inferred, not certified (observed local 2.1.329). |
| RR-03b | P1 T13 `ClaudeAuthProber` | **Decision A:** observed shape, `docs/evidence/remote-runners/claude-auth-status.md` (2.1.329) | `loggedIn:true` + exit 0 → ok; `loggedIn:false` + non-zero exit → logged_out (alert, park); anything else → unknown (never parks or recovers). Bare zero exit without JSON is not ok. |
| RR-03c | P1 T12 Codex rollout decoder and version table | **Decision A:** synthetic fixtures plus observed logged-out evidence, `docs/evidence/remote-runners/codex-logged-out.md` (0.171.0) | Read `codex_error_info` from `error` events **and** `task_complete.error`; object form with `http_status_code` 401/403 → logged_out; other object forms keep transient. Capture certification is a Non-Goal; floor 0.170.0 is evidence-inferred. |
| RR-03d | P1 T12 `RateLimitsRead`, consumed P1 T13 | **Decision A:** observed exchange, `docs/evidence/remote-runners/codex-app-server.md` (0.171.0) | Dedicated stdio session; stdin kept open until the id-1 answer (server exits on EOF without answering); skip id-less notifications; any id-1 error/EOF/timeout → `ErrRateLimitsUnavailable` → capacity unknown. |
| RR-05 | P2 T11 runner autostart | **Decision A:** per-user LaunchAgent (`gui/<uid>`); evidence `docs/evidence/remote-runners/macos-runner-host.md` | Runner starts with the user's session (including the session opened by FileVault pre-boot unlock) with keychain/file credentials usable. Unattended reboot only via `sudo fdesetup authrestart`; offline before login is the normal lost/offline state. LaunchDaemon and auto-login are Non-Goals. |
| RR-06 | P2 T7 `Telemetry.Thermal` | **Decision A:** `/usr/bin/notifyutil -g com.apple.system.thermalpressurelevel`; evidence `macos-runner-host.md` | 0→nominal, 1→fair, 2→serious, 3/4→critical; any other output → nil. `pmset` parsing dropped; no cgo helper; no hardware load test. |
| RR-07 | P2 T11 root setup, P2 T4/T9 workspace compatibility | **Decision A:** canonical root `/opt/arbitron/workspaces` | New Linux shared server installs default to it; Mac runner installer creates it with one prompted `sudo install -d -o $USER -g staff -m 0750`; roots with symlinked components are refused; runner verifies before advertising; otherwise chat_only. Existing servers keep their root (manual migration out of scope). |
| RR-08 | P3 T2 `MaterializePortableGit` | **Decision A:** narrow support, no network fetch | Missing promisor objects → home marked non-portable with a visible reason, pinned to the built-in runner, excluded from remote placement/moves; steps continue on built-in; marker clears when a later checkpoint finds all objects. Partial clones are a Non-Goal for remote execution. |
| RR-09a | P3 T8 bundles, P3 T9 remote scripts | **Decision A:** enforced affinity with author opt-in | Scripts are `AffinityBuiltIn` unless the node declares `portable: true`; a portable script that cannot be bundled is a load-time validation error. Outside-bundle dependency manifests are a Non-Goal. |
| RR-09b | P3 T8/P3 T9 process eligibility | **Decision A:** declared command names, accept-time `LookPath`, built-in fallback | Node `requires: [names]` (with `portable: true`); runner `LookPath`s interpreter/command + requires, rejects `command_unavailable: <name>`; dispatcher excludes that runner for the attempt, tries the next remote, then the built-in runner. Version constraints are a Non-Goal. |
| RR-17 | P3 T10 recovery, P3 T12 drain, P4 T6 turn commit (producer **P3 T16**) | **Decision B:** local checkpoints for non-portable homes | After each workspace-changing step completes, at the same point a portable home checkpoints, the server takes a plain local copy of the home directory on its own disk. No portable-Git materialization is done, and the copy never leaves the built-in runner. The copy is staged in a temp dir and renamed into place, then committed in the step transaction (`WorkspaceCompletion.LocalRef` → `workspace_homes.local_checkpoint`). A failed copy means the node is not done. PoolRecovery restores the latest committed local copy and re-runs the interrupted step under the same generation and epoch fences. Only the latest committed copy is kept, plus the in-progress one until it commits. With no local copy, recovery restores the initial state: the local baseline from `EnsureBaseline`, or the last committed portable checkpoint. With neither, it raises the visible blocker (`ErrNoRecoveryBase` → `job.recovery_blocked`). |

RR-13 foreign-home native restore and RR-14 cross-account fidelity are handled
by P4's mandatory automatic transcript rebuild, unconditional Codex reasoning
stripping and durable loss notices. No required path consumes an account
fingerprint. They are optional native-fidelity research, not unresolved inputs
on the supported fallback path. RR-11 staging portability and RR-16 branch/client
observability now have required producer, wiring and sink-test tasks; neither
may retain a built-in-only or decoder-only shortcut.

## 23. Suspension, maintenance and lifecycle completion owners

```go
// domain/engine/step_commit.go — P3 T5
type StepCommitMode string
const (
    StepCommitAdvance StepCommitMode = "advance"
    StepCommitSuspend StepCommitMode = "suspend"
)
type StepSuspension struct { Kind string; ChildLinkID *uuid.UUID; State map[string]any }
// StepCommit includes Mode StepCommitMode and Suspension *StepSuspension.
// domain/runner/transfer.go — P3 T6 defines, P3 T7 implements.
type TransferScope struct {
    Fence Fence
    RunID uuid.UUID
    Direction, BaseRef, BundleHash, MaintenanceOperationID string
    Resource engine.ResourceFenceRef
}
// domain/runner/homes.go — P3 T3
type HomeMoveCause struct {
    TargetJobID, InvocationID *uuid.UUID
    NodeID, OperationID string
    FromGeneration, ToGeneration int64
}
```

An awaited spawn may execute a routed child-input preparation before it waits.
P3 T5's successful suspension commit consumes those exact validated operation
receipts and releases their slots while keeping the node invocation open and
current node unchanged. Persist the existing child-link ID and prepared inputs
before exposing the wait. On commit failure/restart preserve/reuse that durable
link; never spawn a second child. Final child completion extends the invocation
without committing already-consumed receipts again. P4 first turns already
commit through `TurnCommit` and are not committed a second time by suspension.
P3 T13's step-limit-one root test proves a child can execute while its parent
waits and the waiting parent holds zero runner slots.

Synthetic `OperationProbe` has no node continuation. Its explicit probe
completion owner validates the exact-model confirmation and commits its ledger
result, releasing its slot without manufacturing a node invocation/step commit.
P3 T5/T6 define that policy and P3 T13 exercises the P1 T13 monitor through the
real root after deferred completion is enabled. Ordinary workflow operations
cannot use the probe exception.

A drain's pull-only maintenance operation is persisted by P3 T12 before moving
bytes; P3 T7 validates its exact target runner, resource, pinned checkpoint and
operation ID. A transfer scope names exactly one active job fence or one durable
maintenance operation. Zero job fields are allowed only in that explicit
maintenance branch, never as wildcard authorization. The target's verified pull
receipt is required before drain reports completion; waiting for a future
workflow step to pull does not complete maintenance.

P3 T3 records `HomeMoveCause` atomically with handoff; P3 T6 supplies assignment
job/invocation/node/operation and generations, and drain supplies its maintenance
operation. P3 T12 read DTOs preserve the correlation; P5 T7 joins move badges to
the exact target job/operation, never a timestamp heuristic across loop visits.
A drain-only move remains in history without inventing an execution edge.

P1 T16 owns `RefreshInventory func(context.Context) error` and periodically
re-runs `sourceSync` under the server background lifetime, updating missing and
upgraded harness inventory independently of health. The root retains notifier
unsubscribe callbacks, closes/joins `StepDispatcher`, waits for `HealthMonitor`
and outstanding confirmations, and only then closes DB/runtime dependencies.
Tests remove these production producers and check durable source changes and
shutdown joins, rather than reading a constructor field.

### Probe and process supervision adapters

P3 T5 defines `ProbeCompletion{RequestedModel, ExecutedModel string; Validated bool}`
and `ProbeCommitter.CommitProbe(ctx context.Context, f engine.JobFenceRef,
result ProbeCompletion) error`; `adapter/sqlite/probe_commit_repo.go:ProbeCommitRepo`
is the production producer, wired by P3 T13 into dispatcher/confirmation policy.
It validates the explicit synthetic operation kind and exact successful model
before completing its fenced ledger job. Missing owner or invalid evidence
fails without leaving a successful slot-holding operation.

`adapter/runner/builtin/supervisor.go:ProcessSupervisor` implements
`BuiltInStopper.StopAndWait(ctx context.Context, f runner.Fence) error` and owns
durable launch permits/guardian identities before child execution. P3 T13
injects it into both subprocess harness command launch and local processes;
P3 T10 recovery uses the same supervisor. Runner-side
`adapter/runneragent/workspace/gate.go:ResourceGate` is shared by executor,
restore/sync, cancel/fenced handling and janitor. A typed `stopped` acknowledgment
is emitted only after the exact old process tree exits. A newer generation
cannot restore or launch on that local resource while stop is pending; stale
delete cannot remove a newer copy. Stop failure keeps the gate closed.

P3 T9 produces structured reject codes `command_unavailable`,
`capability_unavailable`, `workspace_preparation_failed`. P3 T6 maps only those
allowlisted codes through `Reject.Code` to `OutcomePreparationFailed`; the
operation excludes that runner and re-places across bounded remaining targets.
Unknown reject prose is ordinary failure, never exhaustion or an unbounded
reroute. P3 T13's missing-interpreter root test proves the final command executes
once on a capable alternative.

### Interactive event identity and supervised assignment provenance

P4 T3 `domain/engine/turn_identity.go` owns the following; P4 T6 persists it in
the atomic conversation commit and P4 T12 reads that same record for reconnect
history. P4 T8 publishes events with the typed identity; P5 T7 consumes it.

```go
type InteractiveTurnIdentity struct {
    RunID uuid.UUID `json:"instanceId"`
    NodeID string `json:"nodeId"`
    InvocationID uuid.UUID `json:"invocationId"`
    RequestID uuid.UUID `json:"requestId"`
    TurnIndex int `json:"turnIndex"`
    Generation int64 `json:"generation"`
}
const ConversationKeyTurnIdentity = "turnIdentity"
func ContextWithTurnIdentity(context.Context, InteractiveTurnIdentity) context.Context
func TurnIdentityFromContext(context.Context) (InteractiveTurnIdentity, bool)
func CommittedTurnIdentity(conv map[string]any) (InteractiveTurnIdentity, bool)
func PublishTurnRestarted(bus EventBus, turn InteractiveTurnIdentity)
func PublishContextNotice(bus EventBus, turn InteractiveTurnIdentity, notice string)
// domain/runner/assignment_context.go — P3 T10
func ContextWithAssignment(ctx context.Context, a Assignment) context.Context
func AssignmentFromContext(ctx context.Context) (Assignment, bool)
```

`builtin.Run` stamps the actual assignment for every supervised harness/process
launch; the supervisor records its durable permit against that exact job fence.
Read-only probes explicitly use unsupervised launch; token-spending confirmation
uses a real assignment. Missing provenance cannot register or start a supervised
workflow process. P3 T13 checks the actual `builtin_processes` row and surviving
child identity; constructing a supervisor alone does not prove safe recovery.

For mutable streamed turns P3 T6 `BeginAttempt` retains the writer claim, and
the terminal forwarder invokes `FinishSuccess` to prepare the exact workspace
candidate and record its full `StepCompletion` **before** forwarding `Done`.
P4 T6 `StageTurn` validates/reuses this candidate. It does not create a second
candidate or infer a missing resource token through `Latest`; missing receipt
fails the turn before bundle/result commit.

### Baseline before assignment (P3 T6; wired P3 T13)

```go
// domain/engine/workspace_baseline.go
type WorkspaceBaselineInitializer interface {
    EnsureBaseline(ctx context.Context, runID uuid.UUID, dir string) error
}
func (e *Engine) SetWorkspaceBaselineInitializer(WorkspaceBaselineInitializer)
// domain/runner/baseline.go
type BaselineDeps struct { Checkpoints CheckpointStore; Ledger Ledger; Leases ResourceLeaseStore }
func NewWorkspaceBaselineInitializer(BaselineDeps) *BaselineInitializer
```

`Engine.prepareWorkspace` calls this hook after `EnsureInstanceDir` and completed
branch setup, before any intent, slot reservation or resource claim. P3 T13
installs it before executors freeze/runtime starts. Bootstrap's no-active-writer
check is therefore satisfiable. An existing committed baseline is reused and
never overwritten by a mutable restart copy; missing/active/failed initialization
fails before any remote offer. `WorkspaceSync.BeginAttempt` only consumes the
baseline and must never attempt bootstrap after it claims an active resource.
Root tests verify first-step files and branch-fork bytes on a second runner and
that baseline disk failure prevents dispatch entirely.

## Source-bound verification requirement (alignment review 2026-11-22)

`ExecRequirements` gains `RequiredSource SourceID` (defined by P1 T3). Empty means ordinary placement; only synthetic `engine.OperationProbe` may set it. P1 T13 `DispatchConfirmer` sets it from the actual source being confirmed. P1 T4 `sourceMatches` requires exact source identity when nonempty, and P1 T6 persists/rechecks it within `Reserve` alongside the complete request. It never relaxes runner, compatibility, auth, slot or probation gates. P2 T13 extends confirmation to the owning remote runner; `PinnedRunner` alone does not prove which credential source succeeded.

Write/run `TestPlace_RequiredSourceCannotUseHealthyAlternative` RED before the predicate, then GREEN; P1 T6 adds `TestReserve_RequiredSourceCannotBeChangedByDecision`. A successful request on B cannot recover exhausted A even when runner, family and model match. P1 T16 and P2 T13 own the actual monitor-to-ledger-to-health sink tests. Ordinary user dispatch with this internal field is rejected before an offer.

## 24. Alignment review: remaining production edges (2026-11-22)

This section supplements the exact earlier types and supersedes conflicting
implementation transcripts. New work is assigned below; no component is
accepted merely because a constructor or fake says it exists.

| Gap | Owner / production source | Root and final proof |
|---|---|---|
| Cached remote operator probe; wrong-machine quota verification; lost nullable quota observations | P1 T13 merger, P2 T1/T4/T5/T8/T9 transport, new P2 T13 fresh probes/source-bound confirmation | P2 T13 actual server + runner constructors; fresh check reads runner-local auth; exact source job → health/alert/REST sink |
| Remote Codex/Claude agent implementation with no matching advertised source, missing Codex config and local capability rules | P2 T9 codex_binary loader; P3 T9 executor; new P3 T15 inventory/rules/root closure | Installed-version/login/RPC/rules → real hello → reservation → actual CLI launch/checkpoint on remote Mac; Codex chat remains unsupported |
| Session export overcommits a slot or fails at limit one | P4 T6 exact export receipt, P4 T12 export/upload inside originating stream assignment; P4 T13 wiring | Limit 1 and fully occupied limit 8: first/later/terminal turn commits matching bundle/workspace with no second export reservation |
| Restart resets workflow retry attempt, cannot find live attempt N | P3 T1 retry journal, P3 T5 RetryProgressStore transaction; P3 T10 recovery consumes it | P3 T13 restart during attempt 2 adopts that exact job, preserves delay/budget; no attempt-1 execution |
| Missing promisor objects accepted as successful progress with no recoverable checkpoint | **Resolved — DECISION RR-17: B** below; owner **P3 T16** (`2026-11-20-phase-3-task-16.md`), consumed by P3 T10/T12 and P4 T6 | P3 T16 `TestPhase3E2E_NonPortableRestartRestoresLocalCheckpoint` (crash mid-step → local checkpoint restored → step repeats once) and `TestPhase3E2E_NonPortableLocalCopyFailureLeavesStepUndone` |

Execution order: P2 T13 follows P2 T12 and is a prerequisite for Phase 3;
P3 T15 follows T14 and is a prerequisite for Phase 4. P3 T16 Cycles 1–5
run after P3 T6 and before P3 T10 (which consumes them); its Cycle 6 root
wiring runs after P3 T13 Cycle 5 and before the P3 T13 final gate and P3 T15. Ports and fields used
by earlier tasks are declared by their earlier owners as noted in those tasks;
new closure tasks supply adapters/runtime integration, not duplicate services.
All new cycles follow RED → minimum implementation → GREEN → refactor for
happy, success, failure, injected error and edge cases. Run the named focused
commands, then the existing full phase gates. No new Go/UI implementation is
part of this documentation review.

### Retry progress and terminal failure replay (P3 T1/T5/T10/T13)

```go
// domain/engine/retry_progress.go — P3 T5
// The workflow snapshot supplies retry policy; source reroutes never advance it.
type RetryProgress struct {
    InvocationID uuid.UUID
    Attempt int
    RetryNotBefore *time.Time
    Exhausted bool
    LastExecution *instance.NodeExecution
}
type RetryProgressStore interface {
    LoadRetryProgress(context.Context, uuid.UUID) (RetryProgress, error)
    CommitRetryFailure(ctx context.Context, invocationID uuid.UUID, attempt int,
        execution *instance.NodeExecution, retryAt *time.Time) (RetryProgress, error)
}
func (e *Engine) SetRetryProgressStore(RetryProgressStore)
```

P3 T1 migration 044 adds `node_retry_progress(invocation_id PRIMARY KEY,
attempt, retry_not_before, exhausted, last_execution_id)` and
`node_retry_failures(invocation_id,attempt,execution_id,payload_hash)` unique
by invocation/attempt. P3 T5 `StepCommitRepo` implements this port with the same
SQLite DB. Initial load for an open invocation is attempt 1. CommitRetryFailure
atomically checks invocation/run/node/attempt, writes the failed NodeExecution
and failure receipt and advances to attempt+1 when retryAt is non-nil; nil marks
retry exhaustion. Identical replay is idempotent, conflicting replay rejected.
The engine resumes the persisted attempt and remaining delay; exhausted retry
state replays ordinary existing halt/skip/route behavior with no provider call.
Cleanup/stop of an uncommitted workspace attempt precedes the failure commit;
storage/ownership failures do not consume provider retries. Successful step
commit closes the same invocation; a graph back-edge gets fresh attempt 1.
P3 T13 injects the same committer as RetryProgressStore before startup recovery.

### Session export is part of its originating turn (P4 T6/T12/T13)

The completed harness stream still holds its single slot/resource claim until
TurnCommit. Export is file checkpoint work under that exact assignment, not
another model execution or independently reserved runner job. Remove
`Reserve(...StepLimit+1...)`, `lastTurnJob` inference and post-hoc export-job
selection. Atomic Reserve never trusts a caller-supplied higher limit.

P4 T12's remote stream executor intercepts terminal Done, stops/reaps the
harness, prepares the workspace, exports the native session via its linked
SessionPortability adapter and uploads the bundle under the originating job's
session-transfer token BEFORE producing the terminal successful result. It
retains the verified session descriptor in `Result.Session` and the durable
spool/inbox. The server forwards successful Done only after receiving/verifying
that result and exact workspace/session receipts. Partial tokens still stream.
Export/upload failure is a failed turn with no conversation/workspace commit.

```go
// domain/engine/session_checkpoints.go — P4 T6
// Extend the existing request; resource is nil only for nonmutable chat.
type SessionExportRequest struct {
    RunnerID, Backend, SessionID, WorkDir string
    JobFence JobFenceRef
    Resource *ResourceFenceRef
}
```

P4 T6 obtains this fence from the operation completion sink, never timestamps
or latest-job lookup. For remote sessions Hub.ExportSession reads the exact
persisted parent Result.Session and uploaded bundle from P4 T12, validates all
identity/checksum/resource fields and returns the bytes; it sends no extra job.
Built-in export uses the same parent context/fence and in-process adapter.
P4 T12 adds optional `WireChat.SessionExportToken`/runner mirror and an explicit
native-checkpoint flag: token producer is Hub stream dispatch using the shared
TransferTokens issuer, scoped to that parent assignment/session/resource. The
transfer session binds parent fence and session identity; uploaded content
cannot choose its own authority. Stateless API chat commits transcript with no
native export flag or bundle. Native session import for a move still owns its
own single reserved operation and exact receipt, completed by CommitMove.
Retire the old nested export tests; P4 T13 covers first/later/terminal turns
and restart adoption while every normal slot is occupied.

### DECISION (2026-11-22) RR-17: B — local checkpoints for non-portable homes

The RR-08 decision still forbids network fetching and keeps non-portable homes
on the built-in runner. The human resolved the missing recovery base with
**option B**: a non-portable home takes a **local checkpoint**. After each
workspace-changing step completes, at the same point a portable home takes its
checkpoint, the server copies the home's directory to its own disk. The node is
not done until that checkpoint lands. The copy is staged under
`<CheckpointDir>/local/<run>/.staging-*` and renamed atomically into
`v<version>-<hash>`. The step transaction then commits it as
`workspace_homes.local_checkpoint` via `WorkspaceCompletion.LocalRef`, in place
of P3 T5's former `dirty = 1` rule. The copy never leaves the built-in runner,
so it needs no portable-Git materialization.

PoolRecovery restores a non-portable home from its latest committed local
checkpoint through the unchanged `BuiltInRestorer` port
(`WorkspaceSync.RestoreBuiltIn`). It re-runs the interrupted step exactly as for
a portable home, fenced by the same ledger generation and resource epoch. Each
home keeps only its latest committed local checkpoint, plus the in-progress
copy until it commits (`PruneLocal` before staging, `LocalCheckpointRetention`
after commit).

If no local checkpoint exists yet, recovery restores the home's initial state:
the local baseline that `EnsureBaseline` commits for a run that was
non-portable from the start, or the last committed portable checkpoint for a
home that became non-portable mid-run. If neither exists, or the committed
local copy is missing or corrupt, `RestoreBuiltIn` returns
`runner.ErrNoRecoveryBase`. P3 T10 then records the durable
`job.recovery_blocked` event and fails only that run with the visible reason.
It never silently re-runs against dirty files and never falls back to an older
base.

Owner: **P3 T16**. Consumers: P3 T10 (recovery), P3 T12 (built-in drain keeps a
non-portable home as a visible local-only blocker, since the home can never
move), P4 T6 (turn commits on a non-portable home carry the same `LocalRef`
receipt and promotion) and P5 T3 (renders the blocker only for the no-base
case).

### Additional exact execution policy port (P1 T10/P3 T8/T13/T15)

```go
// domain/runner/agent_policy.go — declared P1 T10; adapter P3 T8; wired P3 T13, verified P3 T15.
type AgentToolPolicy func(provider.ResolvedBinding, []string) ([]string,error)
func (d *StepDispatcher) SetAgentToolPolicy(AgentToolPolicy)
```

P3 production initialization requires this policy. The routed RunAgent applies it before EnsureIntent and copies the effective scope into BOTH the immutable request and ExecRequirements.Tools. P4 T3 uses the same helper before freezing an accepted agent-turn binding. P3 T8 defines/implements adapter `Factory.EffectiveAgentTools`, which reads the same per-binding/config/default policy as its actual agent selector; it cannot claim tool restrictions a backend cannot enforce. Supported unrestricted defaults remain explicitly described; absent evidence is not a universal empty-list grant. P3 T8 owns helper implementation before P3 T13 consumes it; P3 T15 owns native-source closure tests, and P3 T13's root supplies the real factory method.

### Durable remote session-artifact staging (P4 T12)

`checkpoint.SessionStore.Put` after Hub.Run returns is too late to back a terminal ACK. P4 T12 adds `OpenSessionBlobs(dir string, perBlob,total int64) (*SessionBlobs,error)` and a filesystem-backed implementation using atomic writes/fsync beneath `<CheckpointDir>/session-staging`. The old memory constructor is explicitly test-only. A verified upload receives a server-generated immutable staged ref keyed by parent job/generation/checksum; `RemoteSessionResult.PreparedBundleRef` is added by the server and persisted in RemoteInbox BEFORE ACK. Restart opens the same staging directory and resolves that exact ref. P4 T6 consumes it through the exact SessionExportRequest; it cannot reconstruct it from a global latest blob. Orphan GC respects live inbox/intent/move/committed-session refs and staging retention. P4 T13 wires one shared persistent staging store into transfer/hub/checkpoint/export and tests ACK→server-restart→turn-commit without another harness call.

### Observation revision fence (P1 T3/T5/T13; P2 T1/T13)

`SourceHealth.Revision uint64` is monitor-produced and persisted in `runner_sources.revision`; `ProbeResult.HealthRevision uint64` is captured before probe I/O. Local monitor probes and remote pending-request entries stamp that value. Correlated wire Probe/ProbeResult preserve it alongside RequestID. A returned observation cannot recover capacity if a newer outcome changed the revision while I/O ran. Periodic remote requests and operator checks both use the real remote prober; uncorrelated cached heartbeats cannot prove recovery of a newer exhaustion episode. Native confirmation still requires exact source/model evidence. All probe work is bounded, background and joined so slow auth/RPC calls do not starve runner heartbeats.

### Spec-purpose audit and release gate

Fan-out still has per-operation placement/atomic normal limits (P1–P3);
waiting holds no execution slot; exact family/model/backend/tool constraints
survive both placement and replay. P2 T13 makes real account restoration and
operator health checks functional on the owning machine. P3 T15 makes native
remote subscriptions discoverable rather than merely linking idle adapters.
P3 T5's retry journal closes restart identity, and P4's parent-assignment export
plus durable staging closes full-capacity turn completion and ACK/restart.
P3 T12/T13 and P4 T13 drain the built-in execution resource too, retaining
honest blockers for local-only/server-only capacity. Existing authorized read
models and P5 hooks/browser sinks carry these actual statuses; the dashboard
still adds no execution controls. The spec is unchanged. RR-17 is decided
(option B, P3 T16): non-portable partial clones recover from their latest
local checkpoint, and only a home with no base at all shows the recovery blocker.

### P2 T13 fresh-health and confirmation names (unstick 2026-11-23)

These names are defined by `2026-11-20-phase-2-task-13.md`. They supersede §12's "bounded 1-token exact-model chat" confirmation.

**Confirmation vocabulary (`domain/runner/confirmation.go`)**
- `OpConfirm OpType = "confirm"` and `ConfirmationPrompt`.
- `ConfirmationRequest{WorkDir}`: server-side only, never serialized.
- `ConfirmationResult{RequestedModel, ExecutedModel, SessionID}`.
- `ConfirmationExecutor.Confirm(ctx, provider.ResolvedBinding, workDir) (ConfirmationResult, error)`.
- `ErrConfirmationNoProfile`, `ErrConfirmationModelMismatch`, and `VerifyConfirmation(want, *ConfirmationResult) error`.
- `StepJob.Confirmation` and `StepOutput.Confirmation`.

**Verifier port and executor**
- `domain/engine/model_verifier.go:ExecutedModelProber` is satisfied by `claudecli.AgentAdapter` and `codex.Provider`.
- `adapter/runner/probe/execution.go:NewConfirmationExecutor(engine.AgentProviderSelector) *HarnessConfirmationExecutor`. It clones an agent-kind binding, calls `ProbeExecutedModel`, and never calls `RunAgent`.
- Required dependencies: `builtin.Deps.Confirmations` (required) and the runner's `executor.Config.Confirmations`.

**DispatchConfirmer**
- It dispatches `OpConfirm` with `engine.OperationProbe`, `RequiredSource=s.ID`, `PinnedRunner=s.RunnerID` and `AffinityNone`.
- The binding is an exact-model no-connection profile of the source's native backend: `claude-code` for `anthropic_subscription`, `codex` for `openai_subscription`.
- `probe.ErrNoConfirmationModel == runner.ErrConfirmationNoProfile`.

**Monitor (`domain/runner/health.go`)**
- Probation reasons go in `SourceHealth.Detail` as one of the `DetailConfirm{NoProfile,Unsupported,Transport,Timeout,Mismatch,Failed}` strings.
- Periodic remote checks run asynchronously at `Interval`, one in flight per source, and are joined by `Wait`.

**Remote prober (`domain/runner/remote_prober.go`)**
- Errors: `ErrRemoteProbeUnavailable`, `ErrRemoteProbeTimeout`, and `ErrRemoteProberNotWired` (which wraps Unavailable).
- `NewRemoteSourceProber(*Hub) *RemoteSourceProber`.

**Hub (`domain/runner`)**
- Messages: `MsgProbe="probe"` and `MsgProbeResult="probe_result"`.
- Payloads:
  - `ProbePayload{RequestID uuid.UUID; HealthRevision uint64; SourceKind; CredentialRef}`
  - `ProbeReply{RequestID; HealthRevision; Source SourceReport}`
  - `ConfirmationDispatch{Model, Backend, Provider, EndpointFingerprint, ContextWindow, Effort}`
- Struct additions:
  - `SourceReport` gains `Observation *ProbeResult`, `ObservationSeq uint64` and `SampleAgeMs int64`.
  - `DispatchPayload.Confirmation` and `RemoteResult.Confirmation`.
  - `Outbound.Probe` and `Inbound.ProbeResult`.
  - `HubConfig.ProbeTimeout`; 0 means `DefaultRemoteProbeTimeout` (10s).
- `HubHealth` = `HealthTransitioner` + `ObserveProbe`. `HubDeps.Health` is a `HubHealth`, so P2 T10's `remoteRunnerInputs.Health` becomes `*runner.HealthMonitor`.
- Methods: `Hub.ProbeSource(ctx, SourceID) (ProbeResult, error)`, `Hub.PendingProbes() int`, `Hub.Wait()`.
- `applyReportedHealth` is removed. Only ordered, fresh, bounded-age (3×heartbeat) observations reach `ObserveProbe`, and heartbeat hints never recover capacity.

**Wire (`adapter/runnerproto`)**
- `WireConfirmation{model, backend, provider, endpointFingerprint?, contextWindow?, effort?}` on `Dispatch{op:"confirm"}`.
- `WireConfirmationResult{requestedModel, executedModel, sessionId?}` on `Result`.
- `ExecutionCapability.Op` accepts `"confirm"`.

**Runner side**
- `runneragent.OpConfirm`.
- `ConfirmationJob` (the same six fields).
- `ProbeRequest{RequestID, HealthRevision}`.
- `Source{Observation, ObservationSeq, SampleAgeMs}`.
- `Outbound{RequestID, HealthRevision}`.
- `Dispatch.Confirmation` and `Result.Confirmation`.
- `FreshSourceProber.ProbeSource(ctx, kind, ref) (Source, error)` and `ErrUnknownSource`. `sources.Inventory` implements it.
- `cmd/arbitron-runner/build.go:buildExecutor(cfg, local *providersadapter.Factory)`.

## 25. Phase 3 unstick decisions (2026-11-23)

Derived from the spec, the Phase 3 design and §21/§22/§24; each owning task
plan carries the same text and its TDD steps.

### Resource lease — durable recovery-proof contract (owner P3 T3)

Derived from design phase 3 §3.1 ("acquire a durable resource lease ... a distinct token/generation/operation holder is required ... Claims do not expire into takeover during suspect/restart grace. Handoff fences the old writer before the target executes; job, workspace and session generations are checked together") and contracts §21 ("`Supersede` is permitted only with transactional fencing of the old operation after confirmed loss/revocation or explicit cancellation and process stop"), mirroring P4 T2's `TurnRecoveryProof`.

```go
// domain/runner/resource_lease.go (P3 T3)
type LeaseRecord struct {
    Fence  engine.ResourceFenceRef // exact active key/owner/generation/token
    Holder string                  // durable operation identity
    Job    *Fence                  // exact P1 ledger fence of the writing job; nil for server-side holders
    BootID string                  // server boot that took a server-side (Job == nil) claim; "" for job claims
}
type SupersedeKind string
const (
    SupersedeRunnerLost    SupersedeKind = "runner_lost"    // remote owner: runners.liveness = 'lost'
    SupersedeRevoked       SupersedeKind = "revoked"        // remote owner: runner_keys.revoked_at IS NOT NULL
    SupersedeStopped       SupersedeKind = "stopped"        // cancellation + confirmed stop: builtin_processes(job,gen).state='stopped' (built-in) or runner_inbox_cursors(job,gen).result_received=1 (remote)
    SupersedeServerRestart SupersedeKind = "server_restart" // server-side holder (Job == nil) whose BootID != the repo's current boot ID (design D7: restart = built-in runner loss)
)
type SupersedeProof struct {
    Kind   SupersedeKind
    OldJob *Fence // must equal LeaseRecord.Job exactly when the lease has a job; nil otherwise
}
type ResourceLeaseStore interface {
    // Server-side holder (group mutation, drain/maintenance, recovery restore). Records BootID.
    Claim(ctx context.Context, key, holder string, owner RunnerID) (engine.ResourceFenceRef, error)
    // Dispatched job holder. In ONE transaction: the job fence must be active in runner_jobs
    // (same rule as Ledger.FenceCheck), owner = job.RunnerID, and the job association is persisted.
    // The SAME exact job fence re-claiming returns the existing ref (adoption/idempotent retry).
    ClaimJob(ctx context.Context, key, holder string, job Fence) (engine.ResourceFenceRef, error)
    Check(ctx context.Context, f engine.ResourceFenceRef) error
    Active(ctx context.Context, key string) (LeaseRecord, bool, error)
    Release(ctx context.Context, f engine.ResourceFenceRef) error
    Supersede(ctx context.Context, old engine.ResourceFenceRef, proof SupersedeProof, holder string, owner RunnerID) (engine.ResourceFenceRef, error)
}
func JobHolder(j Job) string // "job:<jobID>/inv:<invocationID>/op:<operationID>/attempt:<n>"
var ErrResourceBusy, ErrResourceFenced, ErrSupersedeUnproven error
// Transaction-aware helper for P3 T5/T7/T13/P4 T6 committers (same *sql.Tx, never the *sql.DB):
// adapter/sqlite: func checkResourceFenceTx(ctx, tx groupQueryer, f engine.ResourceFenceRef) error
//                 func releaseResourceTx(ctx, tx groupQueryer, f engine.ResourceFenceRef) error
```

Persistence (appended to 044; owner P3 T1 file, implemented by P3 T3; no new migration number; `workspace_leases` columns are NOT changed so the landed positional-insert constraint test stays valid):

```sql
CREATE TABLE workspace_lease_holders (
 resource_key TEXT NOT NULL, generation INTEGER NOT NULL CHECK(generation >= 1),
 token TEXT NOT NULL, holder TEXT NOT NULL, owner TEXT NOT NULL,
 job_id TEXT, job_runner_id TEXT, job_generation INTEGER,
 boot_id TEXT NOT NULL DEFAULT '', claimed_at TEXT NOT NULL,
 CHECK ((job_id IS NULL AND job_generation IS NULL AND job_runner_id IS NULL AND boot_id != '')
     OR (job_id IS NOT NULL AND job_generation >= 1 AND job_runner_id IS NOT NULL AND boot_id = '')),
 PRIMARY KEY (resource_key, generation)
);
CREATE TABLE workspace_lease_supersessions (
 resource_key TEXT NOT NULL, old_generation INTEGER NOT NULL, old_token TEXT NOT NULL,
 new_generation INTEGER NOT NULL, kind TEXT NOT NULL CHECK (kind IN ('runner_lost','revoked','stopped','server_restart')),
 job_id TEXT, job_generation INTEGER, superseded_at TEXT NOT NULL,
 PRIMARY KEY (resource_key, old_generation)
);
```

`Supersede` rules, all inside ONE SQLite transaction (any failure changes nothing and returns `ErrSupersedeUnproven` or `ErrResourceFenced`):
1. The active `workspace_leases` row for `old.Key` matches `old` exactly (token, generation, owner, active=1); else `ErrResourceFenced`.
2. Its `workspace_lease_holders` row is loaded. If it has a job, `proof.OldJob` must equal it exactly, and `runner_jobs` must NOT still authorize that fence (`NOT (generation = ? AND runner_id = ? AND state IN ('offered','running'))`): the caller fences the old job (Requeue/Fail) first. A job-less holder requires `proof.OldJob == nil`.
3. Kind evidence, read in the same transaction:
   - `runner_lost`: owner != builtin AND `runners.liveness = 'lost'`. Suspect/offline is never enough.
   - `revoked`: owner != builtin AND `runner_keys.revoked_at IS NOT NULL` for that runner_id.
   - `stopped`: lease has a job; owner == builtin → `builtin_processes(job_id, generation).state = 'stopped'` (written by P3 T10 `ProcessSupervisor.StopAndWait` after confirmed group exit); remote → `runner_inbox_cursors(job_id, generation).result_received = 1`.
   - `server_restart`: lease has no job AND `boot_id != ` current boot ID.
   - Any other combination: `ErrSupersedeUnproven`.
4. Insert the `workspace_lease_supersessions` receipt, replace the lease with a new random token, generation+1, new holder/owner, and insert the new holder row (server-side holder, current boot ID). Commit.

There is no TTL and no liveness-only path; suspect never authorizes Supersede. `Release` is exact token/generation (stale release returns `ErrResourceFenced` and changes nothing). `NewResourceLeaseRepo(db *sql.DB, bootID string) *ResourceLeaseRepo`; P3 T13 passes one process-lifetime `uuid.NewString()` boot ID created at startup and builds exactly one repo shared by sync, group, recovery, token issuer and committers.

Consumers: P3 T6 `BeginAttempt` uses `ClaimJob(ctx, "run:<uuid>", JobHolder(a.Job), a.Fence)`; P3 T10 PoolRecovery reads `Active`, fences the old job, `StopAndWait`s (built-in), then `Supersede` with the matching kind, restores under the new claim and `Release`s it before re-entry; P3 T13 group mutations use `Claim`; P3 T7 issuer and P3 T5/T13/P4 T6 transactions use `checkResourceFenceTx`/`releaseResourceTx` inside their own transaction.

### Transfer token issuer (owner P3 T7)

```go
type ResourceChecker interface { Check(ctx context.Context, f engine.ResourceFenceRef) error } // ResourceLeaseStore satisfies it
func NewTransferTokenService(store TransferSessionStore, fences FenceChecker, resources ResourceChecker, now func() time.Time) *TransferTokenService
func (s *TransferTokenService) Issue(ctx context.Context, scope TransferScope, ttl time.Duration) (TransferToken, error)
```
`Issue` requires exactly one authority (job fence with `Resource.Owner == Fence.RunnerID`,
or a maintenance operation ID with zero job fence), `Resource.Key == "run:<RunID>"`
and a non-zero pinned `BaseRef` of the same run for pull/push, a 64-hex
`BundleHash` and empty `BaseRef` for bundle. `Issue` and every `Validate` re-check
the job fence (job scope) and `resources.Check` (all scopes). Pulls are served
only from the pinned `BaseRef`, never `Latest`.

### Portable-script bundle checker binding (owner P3 T8)

```go
type ScriptBundleChecker interface { CheckBundleable(ctx context.Context, owner uuid.UUID, workflowName, scriptFile string) error }
func WithScriptBundleChecker(ctx context.Context, owner uuid.UUID, c ScriptBundleChecker) ValidateOption
func (s *workflow.Service) SetScriptBundleChecker(c ScriptBundleChecker)
```
Owner binding follows the landed parallel_group checkers: validate endpoints use
`caller(ctx).UserID` inside `GroupValidateOptions(ctx)`; Create uses the caller
and Update uses `groupWriteFileOwner`; no identity means no checker, so the result
is fail-closed `SCRIPT_BUNDLE_CHECKER_UNAVAILABLE`.

### Engine release of uncommitted receipts (owner P3 T5; implemented P3 T6; wired P3 T13)

```go
// domain/engine/step_commit.go
type StepCompletionReleaser interface { ReleaseCompletions(ctx context.Context, completions []StepCompletion, cause error) error }
func (e *Engine) SetStepCompletionReleaser(r StepCompletionReleaser)
```
`*runner.StepDispatcher` implements it (`FinishFailure` + `Ledger.Fail` for exactly
those receipts). The engine calls it whenever an attempt that recorded receipts
ends without a successful `CommitStep`. A production root with a committer and no
releaser fails at startup.

### Server-side group mutation commit (owner P3 T13; staging P3 T2)

Landed migration 044 forbids a job-less `prepared` checkpoint row, so a server-side
group mutation never prepares a catalog row:
```go
// domain/runner (P3 T2): bytes only, no catalog write
StageResource(ctx context.Context, runID uuid.UUID, srcDir string, base CheckpointRef) (StagedCheckpoint, error) // CheckpointStore method
// domain/runner/resource_commit.go (P3 T13)
type ResourceCommitter interface { CommitResource(ctx context.Context, claim engine.ResourceFenceRef, staged StagedCheckpoint) error }
```
`ResourceCommitRepo.CommitResource` runs one transaction. It checks the exact
active server-side claim (a holder row with no job) and `base == committed pointer`.
It then inserts the row as `committed` with the claim's resource fence, advances
`checkpoint_version`, clears dirty and releases the claim.

### Placement fixtures (P3 T4 Step 3b)

A workspace request with no home, and an unpinned session, get ordinary ranked
placement with empty `Tier`. Telemetry influences ranking only when it is fresh:
`SampledAt` must be within `PlacementPolicy.TelemetryMaxAge`. P3 T4 Step 3b
authorizes the matching updates to `TestPlace_AffinityRestrictsRunner` and
`TestPlace_HealthierTelemetryWinsUnknownIsNeutral`.

## Unstick 2026-11-23 — P1/P2 contract decisions

These are settled by the spec/design or by a user decision. The owning task carries the TDD steps.

- **Codex launch enforcement (P1 T10 Steps 11–22; user decision 2026-11-23).** `adapter/codex/launch_policy.go` defines:
  - `ErrToolScopeUnsupported` and `ErrContextWindowUnsupported`;
  - `codexToolPolicy(scope) (toolPolicy, error)` and `codexContextArgs(binding, rules) ([]string, error)`.

  Agent, stream and interactive launches apply them as follows:
  - **Non-empty scope:**
    - always: `--strict-config`, `features.plugins=false`, `features.apps=false` and a per-server `mcp_servers={…enabled=false}`;
    - `web_search="disabled"` unless `WebSearch` is granted;
    - read-only sandbox unless a write tool is granted.
  - **Refusals:** a scope without `Bash`, `mcp__*`/unknown names (including pattern-scoped `Bash(gh:*)`-style entries; user decision 2026-11-23), or a restricted launch on codex < 0.163.0. The error message quotes the offending scope entry.
  - **Context:** `200k`/`1m` → `-c model_context_window=200000/1000000`, after `CapabilityRules.Validate`.
  - **Defaults:** an empty scope or default context emits nothing new.
- **Probe correlation (P2 T1 Cycle 4).** `Probe.RequestID` and `ProbeResult.RequestID` are mandatory UUIDs. An uncorrelated probe frame is malformed.
- **Telemetry age (P2 T1 Cycle 5, P2 T8 Cycle 2b, P2 T7 Step O1).**
  - `WireTelemetry.SampleAgeMs` maps to `runner.HeartbeatInfo.TelemetryAgeMs` and `runneragent.Heartbeat.TelemetryAgeMs`.
  - The hub stores `SampledAt = receipt − age`. A negative age, or one over `3×Heartbeat`, records all metrics unavailable.
  - The built-in loop treats a zero producer `SampledAt` as unavailable metrics.
- **Remote dispatch route (P2 T8 Cycle 2b).**
  - `runner.ChatDispatch`, `runnerproto.WireChat` and `runneragent.ChatJob` carry `Provider` (canonical) and `EndpointFingerprint` (from `runner.EndpointFingerprint(binding)`). A fingerprint error dispatches nothing.
  - `runneragent.Source` carries `UnavailableReason` and `Capabilities []runner.ExecutionCapability`.
- **Hub restart adoption (P2 T4 Cycle R).** Applies when a reconnecting runner holds a `HeldRunning` job whose ledger fence passes `FenceCheck`, and the new hub has no in-memory waiter:
  - the hub extends the lease and ACKs `receipt.LastOutputSeq`;
  - heartbeats keep the job alive;
  - orphan output/result frames are journaled and ACKed;
  - a later `Hub.Run` attaches through the retained-receipt path.

  Stale or unknown jobs are still fenced.
- **Generation after re-placement.** Requeue and Reserve each increment the generation, so the first re-placement runs at generation 3 (P1 ledger contract; P2 T6 Step 3a).
- **Telemetry window port.** `TelemetryWindow.Window(RunnerID) []Telemetry` has no error result. Absence is "unknown" (P2 T12).

### User decisions on the P3 open questions (2026-11-23)

- **Agent tool policy (P3 T15, P2 T8 Cycle 2b Step 6).** `runner.ToolPolicy` is
  `allowlist` or `unrestricted`. It is carried on `runner.ExecutionCapability.ToolPolicy`
  and on `runnerproto.ExecutionCapability.ToolPolicy` (JSON `toolPolicy`).
  - The single check is `runner.ToolScopeSatisfied(c, scope)`.
  - `claude-code` advertises `allowlist`: it needs a non-empty scope, and every
    requested tool must be in `Tools`.
  - `codex` advertises `unrestricted`: it accepts only an empty scope.
  - Non-agent capabilities leave the field empty.
- **Runner capability rules (P3 T15 Cycle 9).** Rules load from
  `<ConfigDir>/capabilities.yaml`. If the file is missing, the embedded rules
  apply. If it is present but unparseable, startup fails visibly.
- **Capability inventory names (P2 T9 Cycle 7 Step 6).**
  - `BuildCapabilities(kind runner.CredentialKind, harnessVersion string, in DiscoverInput) ([]runner.ExecutionCapability, string)`.
  - `DiscoverInput.Rules provider.CapabilityRules`.
  - `DiscoverInput.Confirmations runner.ConfirmationExecutor`.
- **Checkpoint symlinks (P3 T2).** Link targets are kept exactly as written,
  including absolute ones. A link is never followed while writing, deleting or
  reading.
- **Checkpoint pruning (P3 T2 Cycle 7; P3 T13 Cycle 6b).**
  - `(*checkpoint.Store).Prune(ctx, now) (PruneReport, error)`, with
    `WithPruneGrace` (default `DefaultPruneGrace` = 24h) and the catalog
    method `Retention`.
  - It runs once at boot, after recovery, and then hourly.
  - Held runs, committed and prepared artifacts, and the run's highest version
    marker are never removed.

- **Claude tool vocabulary (P2 T9 Cycle 7 Step 7, P3 T15 Cycle 3b; lead decision 2026-11-23).**
  - `sources.ClaudeCodeTools` = `Bash, Edit, Glob, Grep, LS, Read, WebFetch, WebSearch, Write`. It is advertised on claude-code capabilities (`allowlist`).
  - `runner.ToolScopeSatisfied` accepts a requested `Tool(<non-empty pattern>)` (e.g. `Bash(gh:*)`) when an allowlist capability lists `Tool`. Claude Code enforces the pattern natively.
  - Unset and unrestricted policies never expand patterns.

## Verification gates (unstick 2026-11-24)

This rule applies to every task in every phase. It supersedes any step text
that demands a broad, package-wide or combined gate be wholly green.

1. **Scope.** A *broad gate* is any verification command that runs more than
   the tests the step itself names: package-wide `go test ./pkg/...`, combined
   multi-package race gates (e.g. `go test -race ./domain/runner/...
   ./adapter/...`), `-count=N` repetition gates over a whole package,
   `go test ./...`, and `npx vitest run` over the whole dashboard.
2. **Pass criterion.** A broad gate passes when BOTH hold:
   - there are **no NEW failures** compared with the branch's recorded test
     baseline (`.arbitron/pipeline/remote-runners.baseline.json`) plus the
     failures listed under item 3; and
   - **every test the step names** (focused `-run` targets, tests the step
     adds or edits) passes exactly, including under `-race` and any
     `-count=N` the step specifies for those named tests.
3. **Failures that do not block the step.** A failing test does not block a
   step when it is any of:
   - in the baseline's `failing_tests`, or it already failed on the step's
     parent commit (re-run it there to confirm);
   - owned by a different task (a test file or test function that another
     task's plan creates or edits), including a later task's unfinished work
     or a producer that has not landed yet;
   - a timing/flake failure in a test the step neither names nor touches
     (it passes on at least one rerun, or also fails on the parent commit).
   The agent records each such failure in a single one-line note under the
   step, `> GATE NOTE (unstick 2026-11-24): <test> — <baseline|pre-existing|
   owned by PX TY|flaky>`, ticks the step, and moves on. It must not edit,
   skip or loosen those tests to make the gate green.
4. **Still mandatory.**
   - Focused tests named by the step must pass exactly.
   - Race-detector findings whose stack touches code the step changed (or
     test code the step added) must be fixed before the step is ticked.
   - `go build ./...` and `go vet` on the packages the step changed must pass.
   - A failure the step introduced (it passes on the parent commit and fails
     after the step) is a NEW failure and blocks, wherever the test lives.
5. **Citation.** Plans cite this rule as "(gate per contracts §Verification
   gates (unstick 2026-11-24))". The rule applies even where a step does not
   cite it.
