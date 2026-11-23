import type { LayoutView } from '../graph/arranged/types'

/** Lifecycle state of a workflow instance. */
export type InstanceState =
  | 'queued'
  | 'running'
  | 'waiting'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'paused'

/** Scheduling priority of a workflow instance. */
export type Priority = 'high' | 'normal' | 'low'

/** How a run was started. Mirrors the backend instance.TriggerSource. */
export type TriggerSource = 'manual' | 'schedule' | 'run_now'

/**
 * Workflow node types the backend accepts (mirrors domain/workflow NodeType).
 * Graph code keeps WorkflowNode.type a plain string so an unknown or newer type
 * still renders through the default node; this union is for code that wants to
 * be exhaustive over the known set.
 */
export type NodeType =
  | 'llm_call'
  | 'agent_call'
  | 'script'
  | 'tool_call'
  | 'conditional'
  | 'transform'
  | 'timer_delay'
  | 'webhook_wait'
  | 'interactive_llm'
  | 'interactive_agent'
  | 'spawn_workflow'
  | 'parallel_group'

/** Lightweight instance representation returned by the list endpoint. */
export interface InstanceSummary {
  id: string
  ownerId: string
  workflowName: string
  state: InstanceState
  priority: Priority
  /** How this run was started. Backend defaults legacy rows to 'manual'. */
  triggerSource?: TriggerSource
  /** Originating schedule id for schedule/run_now runs; absent for manual. */
  scheduleId?: string
  currentNodeId?: string
  createdAt: string
  updatedAt: string
  completedAt?: string
  queuedAt?: string
  /** Whether the acting user has dismissed this instance from their view. */
  dismissed?: boolean
}

/** Density views for the queue list. */
export type QueueDensity = 'balanced' | 'dense' | 'expanded'

/**
 * Enriched instance row returned by the list endpoint (Phase 1+2). Extends
 * InstanceSummary with the additive fields the richer queue table shows.
 * Every enriched field is best-effort: a missing field renders as a
 * placeholder cell.
 */
export interface EnrichedInstanceSummary extends InstanceSummary {
  /** Always present (derived server-side). */
  runName: string
  /** Resolved owner display name; empty when user lookup degraded. */
  submitterName?: string
  /** Account identity (email/subject) for shared-mode row details. */
  submitterIdentity?: string
  /** Friendly label of the current/failed node; empty when none. */
  friendlyStep?: string
  /** Configured model of the node the run is on now (failed node when failed); empty otherwise. */
  model?: string
  /** Configured reasoning effort of that node; empty = default. */
  effort?: string
  /** Agent harness of that node (agent nodes only); empty for chat/non-model nodes. */
  backend?: string
  /** Provider family of that node, e.g. "claude-cli", "litellm". */
  provider?: string
  /** Type of that node, e.g. "agent_call", "script". Empty for completed/cancelled/queued. */
  nodeType?: string
  /** Branch/worktree the run is working on; empty when unknown. */
  gitRef?: string
  /** Max retry attempt across the instance's node executions. */
  retryCount: number
  /** 1-based priority-aware position for queued rows; 0/absent otherwise. */
  queuePosition?: number
  /** Failed node id, for the detail panel's lazy last-error load. Absent for non-failed rows. */
  failedNodeId?: string
  /** The spawning parent run. Absent on a root run — presence means "this row nests". */
  parentInstanceId?: string
  /** Number of spawned children the CALLER may see. Absent/0 renders no expander. */
  childCount?: number
  /** Dispatched operations for this run. */
  execution?: ExecutionStatus[]
}

/** Full instance representation returned by the get endpoint. */
export interface Instance {
  id: string
  ownerId: string
  /** Run department, used with the caller's led departments for intervention controls. */
  department?: string
  workflowName: string
  state: InstanceState
  priority: Priority
  variables: Record<string, unknown>
  originalVariables: Record<string, unknown>
  currentNodeId?: string
  failedNodeId?: string
  createdAt: string
  updatedAt: string
  completedAt?: string
  queuedAt?: string
  waitingUntil?: string
  webhookUrl?: string
  webhookReceived?: boolean
  workflowSnapshot?: Record<string, unknown>
  /** True when the instance is in waiting state on an interactive node. */
  interactiveActive: boolean
  /** The node ID of the interactive node waiting for input, or null. */
  interactiveNodeId: string | null
  /** Arrangement status and best/fallback geometry for the snapshot revision. */
  layout?: LayoutView
  workspaceMoves?: HomeMove[]
  recovery?: RecoveryNotice[]
}

/**
 * Read-only schedule projection, matching the backend Schedule.WireMap. The
 * dashboard never mutates schedules — management stays on REST + MCP.
 */
export interface Schedule {
  id: string
  workflowName: string
  cronExpr: string
  timezone: string
  creatorId: string
  department: string
  enabled: boolean
  /** ISO timestamp of the last fire, or null/absent if never fired. */
  lastFiredAt?: string | null
  createdAt: string
  updatedAt: string
}

/** Node execution record within an instance trace. */
export interface NodeExecution {
  id: string
  instanceId: string
  nodeId: string
  attempt: number
  status: 'succeeded' | 'failed' | 'skipped'
  resolvedInputs?: Record<string, unknown>
  output?: Record<string, unknown>
  error?: Record<string, unknown>
  llmConversation?: Record<string, unknown>
  llmModel?: string
  llmTokensIn?: number
  llmTokensOut?: number
  scriptStdout?: string
  scriptStderr?: string
  scriptExitCode?: number
  startedAt: string
  completedAt?: string
  runnerId?: string
  sourceId?: string
  placementReason?: string
  placementHistory?: PlacementRecord[]
  sessionInvocationId?: string
  sessionPinRunnerId?: string
  sessionGeneration?: number
  sessionMoves?: SessionMove[]
  recovery?: RecoveryNotice[]
  workspaceMoves?: HomeMove[]
}

/** Variable snapshot captured at a node boundary. */
export interface VariableSnapshot {
  id: string
  instanceId: string
  nodeId: string
  variables: Record<string, unknown>
  capturedAt: string
}

/** Lightweight workflow representation returned by the list endpoint. */
export interface WorkflowSummary {
  name: string
  description: string
  author: string
  nodeCount?: number
  /** "private" (caller's own) or "shared" (published). */
  scope?: 'private' | 'shared'
  /** Publishing user's id for shared entries; empty for private. */
  publisher?: string
  /** "public" or "department" for shared entries; empty for private. */
  visibility?: 'public' | 'department'
  /** Owning department for department-visibility shared entries. */
  department?: string
}

/** System configuration returned by /system/config. */
export interface SystemConfig {
  server: { host: string; port: number; publicBaseUrl: string }
  database: { connected: boolean }
  workspace: { path: string }
  llm: { provider: string; apiKey: string }
  logging: { level: string; file: string }
  tracing: { enabled: boolean; exportDir: string }
  queue: {
    globalCap: number
    perUserCap: number
    runningCount: number
    queuedCount: number
    waitingCount: number
  }
  /** webhookUrl is redacted by the API: "" (unset) or "**REDACTED**" (set). */
  teams: { enabled: boolean; webhookUrl: string }
}

/** Queue configuration update response. */
export interface QueueConfigUpdate {
  globalCap: number
  perUserCap: number
}

/** Teams configuration update response (webhookUrl redacted). */
export interface TeamsConfigUpdate {
  enabled: boolean
  webhookUrl: string
}

/** Workflow usage statistics over a date range. */
export interface WorkflowStats {
  totalRuns: number
  successCount: number
  failureCount: number
  avgDurationSeconds: number
}

/** The authenticated caller's identity projection from GET /api/v1/me. */
export interface Me {
  userId: string
  username: string
  role: 'user' | 'admin'
  /** Effective department membership (claim-derived ∪ led), normalized. */
  departments: string[]
  /** Subset of departments the caller leads. */
  ledDepartments: string[]
  isAdmin: boolean
  /** Server operating mode; drives the workflows-catalog default view. */
  authMode: 'local' | 'org' | 'edge'
}

/** Current point-in-time queue counts for the department dashboard. */
export interface DashboardQueueHealth {
  running: number
  queued: number
  waiting: number
  paused: number
  failedNeedingAttention: number
}

/** One time bucket of run volume. `bucket` is an RFC3339 timestamp. */
export interface ThroughputBucket {
  bucket: string
  runs: number
}

/** Aggregate success/failure and duration over the dashboard window. */
export interface DashboardOutcomes {
  totalRuns: number
  /** 0.0..1.0 */
  successRate: number
  /** 0.0..1.0 */
  failureRate: number
  avgDurationSeconds: number
  p95DurationSeconds: number
}

/** One row of the per-workflow ranking. */
export interface WorkflowBreakdown {
  workflowName: string
  runs: number
  /** 0.0..1.0 */
  failureRate: number
  avgDurationSeconds: number
}

/** Full department dashboard envelope from GET /api/v1/dashboard/stats. */
export interface DashboardStats {
  /** The effective department set the stats were computed over. */
  departments: string[]
  queue: DashboardQueueHealth
  throughput: ThroughputBucket[]
  outcomes: DashboardOutcomes
  perWorkflow: WorkflowBreakdown[]
}

/** A notification generated by the workflow engine. */
export interface NotificationItem {
  id: string
  instanceId: string
  workflowName: string
  type: string
  read: boolean
  dismissed: boolean
  createdAt: string
}

/** Paginated response from the notifications list endpoint. */
export interface NotificationListResponse {
  items: NotificationItem[]
  hasMore: boolean
}

/** Domain event streamed over WebSocket. */
export interface WebSocketEvent {
  type: string
  timestamp: string
  data: unknown
  /** Present on system-WS envelopes; identifies the originating instance. */
  instanceId?: string
  /** Originating descendant (or root) on subtree-mode instance streams. */
  sourceInstanceId?: string
}

/**
 * Payload shape of a `node.started` / `node.failed` event's `data` field.
 * The engine emits this as an untyped `map[string]any` (every engine event
 * does), so this is the single TS-side contract the consumer narrows to.
 * Narrowed to the fields the live row patch reads.
 */
export interface NodeEventData {
  nodeId?: string
  nodeLabel?: string
  nodeType?: string
  model?: string
  effort?: string
  backend?: string
  provider?: string
  gitRef?: string
}

// ---------------------------------------------------------------------------
// Interactive session types
// ---------------------------------------------------------------------------

/** A single message in an interactive conversation. */
export interface InteractiveMessage {
  role: 'assistant' | 'user'
  content: string
}

/** Sent on WebSocket connect for instances in interactive-wait state. */
export interface InteractiveHistoryEvent {
  type: 'interactive.history'
  data: { nodeId: string; messages: InteractiveMessage[]; sessionKind?: 'group_resolution' }
}

/** A complete message (user or assistant) added to the conversation. */
export interface InteractiveMessageEvent {
  type: 'interactive.message'
  data: { role: 'assistant' | 'user'; content: string }
}

/** A streaming token fragment from the assistant. */
export interface InteractiveTokenEvent {
  type: 'interactive.token'
  data: { token: string }
}

/** The interactive session has ended. */
export interface InteractiveEndedEvent {
  type: 'interactive.ended'
  data: {
    reason: 'llm_completed' | 'user_ended' | 'abandoned' | 'cancelled'
    output?: unknown
    sessionKind?: 'group_resolution'
  }
}

/** An error occurred during the interactive session. */
export interface InteractiveErrorEvent {
  type: 'interactive.error'
  data: { message: string }
}

/** Union of all interactive event types. */
export type InteractiveEvent =
  | InteractiveHistoryEvent
  | InteractiveMessageEvent
  | InteractiveTokenEvent
  | InteractiveEndedEvent
  | InteractiveErrorEvent

// ---------------------------------------------------------------------------
// Parallel groups — static ownership layout (Go workflow.GroupGraph JSON)
// ---------------------------------------------------------------------------

export type GroupForm = 'branches' | 'workflows'
export type GroupCompletionRule = 'all_succeed' | 'first_success'
export type GroupUnfinishedBehavior = 'wait_for_remaining' | 'cancel_unfinished' | 'finish_independently'
export type GroupGatherMode = 'results' | 'results_and_files'

/** One fixed task of a group (Go workflow.GroupTask). */
export interface GroupTask {
  key: string
  /** Branches form: entry node id of the task body. */
  entry?: string
  /** Workflows form: child workflow name. */
  workflow?: string
  inputs?: Record<string, unknown>
}

/** Config of a spawn_workflow node (all fields optional on the wire). */
export interface SpawnConfig {
  childWorkflow?: string
  spawnMode?: 'fire_and_forget' | 'wait_for_child'
  inputs?: Record<string, unknown>
}

export interface GroupConfig {
  form: GroupForm
  tasks: GroupTask[]
  completionRule: GroupCompletionRule
  unfinishedBehavior: GroupUnfinishedBehavior
  gatherMode: GroupGatherMode
  repoPath?: string
  resolverProfile?: string
  resolverPromptFile?: string
}

/** The immediate (group, task) owning a body node. */
export interface BodyOwner {
  groupNodeId: string
  taskKey: string
}

export interface GroupScope {
  nodeId: string
  config: GroupConfig
  /** Absent when the group sits on the main path. */
  parent?: BodyOwner
  /** taskKey -> body node ids in definition order; empty for workflows. */
  bodies: Record<string, string[]>
  /** Group node ids nested directly inside this group's task bodies. */
  nested: string[]
  successTarget: string
  failureTarget: string
}

export interface GroupGraph {
  /** Keyed by group node id. */
  groups: Record<string, GroupScope>
  /** Body node id -> its immediate owning (group, task). */
  owner: Record<string, BodyOwner>
  /** Every group node id in definition order. */
  order: string[]
}

// ---------------------------------------------------------------------------
// Parallel groups — live/frozen run records (Go domain/instance JSON)
// ---------------------------------------------------------------------------

export type GroupTaskKind = 'branch' | 'workflow'
export type GroupRunState =
  | 'launching'
  | 'running'
  | 'decided'
  | 'gathering'
  | 'resolving_conflicts'
  | 'done'

export type GroupTaskStatus =
  | 'not_started'
  | 'queued'
  | 'running'
  | 'waiting'
  | 'paused'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'unfinished'

export interface FrozenTask {
  key: string
  kind: GroupTaskKind
  origin: string
  childInstanceId: string
  status: GroupTaskStatus
  result?: Record<string, unknown>
  failure?: Record<string, unknown>
  cancelRequested: boolean
  winner: boolean
}

/** The immutable group decision. */
export interface GroupDecision {
  outcome: 'success' | 'failure'
  rule: string
  behavior: string
  winnerKey?: string
  reason: string
  decidedAt: string
  tasks: FrozenTask[]
}

export interface GatherTaskReport {
  key: string
  merged: boolean
  reason: string
  conflictPaths?: string[]
}

export interface ResolutionAttempt {
  mergeIndex: number
  attempt: number
  taskKey: string
  conflictPaths: string[]
  /** Empty while open or ended awaiting verification. */
  outcome: 'resolved' | 'still_conflicted' | 'abandoned' | 'cancelled' | 'error' | ''
  startedAt: string
  endedAt?: string
}

/** The live resolution session's identity. */
export interface ResolutionSessionRef {
  groupRunId: string
  mergeIndex: number
  attempt: number
  /** The instance whose interactive session resolves the conflict. */
  instanceId: string
  nodeId: string
}

export interface GatherReport {
  mode: GroupGatherMode
  state: 'pending' | 'merging' | 'resolving' | 'applied' | 'failed' | 'rolled_back' | 'not_applicable'
  handle?: Record<string, unknown>
  mergeIndex: number
  tasks: GatherTaskReport[]
  attempts: ResolutionAttempt[]
  activeSession?: ResolutionSessionRef
  rollbackStatus?: '' | 'rolled_back' | 'rollback_failed'
  error?: string
}

// ---------------------------------------------------------------------------
// Aggregate run visibility (Go engine.GraphState JSON)
// ---------------------------------------------------------------------------

export type LiveNodeStateValue =
  | 'pending'
  | 'queued'
  | 'running'
  | 'waiting'
  | 'paused'
  | 'completed'
  | 'failed'
  | 'skipped'
  | 'cancelled'
  | 'unknown'

/** Identified by (instanceId, nodeId), never nodeId alone. */
export interface LiveNodeState {
  instanceId: string
  nodeId: string
  state: LiveNodeStateValue
  reason?: string
  attempt?: number
}

export type ExecutionContextKind = 'root' | 'branch' | 'workflow' | 'spawn'

export interface ExecutionContext {
  instanceId: string
  parentInstanceId?: string
  groupRunId?: string
  taskKey?: string
  kind: ExecutionContextKind
  workflowName?: string
  state: InstanceState | 'unknown'
  /** False once a decision released the task. */
  blocking: boolean
  /** Hidden descendants withhold identity, definition and results. */
  redacted: boolean
  nodes: LiveNodeState[]
  /** Workflow snapshot; omitted for branch contexts (same graph as parent). */
  graph?: Record<string, unknown>
  /** Ownership analysis of this context's own graph. */
  layout?: GroupGraph
}

export interface GroupProgress {
  total: number
  completed: number
  failed: number
  cancelled: number
  active: number
  notStarted: number
}

export type GroupTaskLiveStatus = GroupTaskStatus | 'unknown'

export interface GroupPanelTask {
  key: string
  kind: GroupTaskKind
  origin: string
  /** Absent/null before launch and for redacted children. */
  childInstanceId?: string | null
  liveStatus: GroupTaskLiveStatus
  /** Empty while undecided. */
  frozenStatus: GroupTaskStatus | ''
  blocking: boolean
  continuingIndependently: boolean
  cancelRequested: boolean
  launchError?: string
  redacted: boolean
}

export type GroupPanelState = GroupRunState | 'unknown'

export interface GroupPanel {
  groupRunId: string
  /** The instance that executes the group node. */
  instanceId: string
  nodeId: string
  form: GroupForm
  completionRule: GroupCompletionRule
  unfinishedBehavior: GroupUnfinishedBehavior
  gatherMode: GroupGatherMode
  state: GroupPanelState
  /** Author order. */
  tasks: GroupPanelTask[]
  progress: GroupProgress
  waitingReasons: string[]
  winner?: string
  decision?: GroupDecision
  finalOutput?: Record<string, unknown>
  gather?: GatherReport
  /** Only present when the session names a visible instance. */
  activeSession?: ResolutionSessionRef
}

export interface GraphState {
  rootInstanceId: string
  /** Equal versions mean identical content. */
  version: string
  /** True while visible work is active or activity is unknown after a read failure. */
  active: boolean
  contexts: ExecutionContext[]
  groups: GroupPanel[]
  /** Root snapshot's ownership layout; absent when it has no groups. */
  layout?: GroupGraph
  warnings?: string[]
}


// ---------------------------------------------------------------------------
// Remote runners (Phase 5). Wire shapes of the Phase 1–4 runner DTOs and
// system-stream payloads, AFTER api/runnerDecode.ts normalization: Go nil
// lists are [], absent timestamps are null, unknown enums fail closed.
// ---------------------------------------------------------------------------

export type RunnerLiveness = 'online' | 'suspect' | 'lost' | 'offline'
export type RunnerCompatState = 'compatible' | 'incompatible' | 'chat_only'
export type SourceAuthState = 'unknown' | 'ok' | 'logged_out'
/** 'unknown' is client-side only: an absent/unrecognized capacity is never shown as available. */
export type SourceCapacityState = 'available' | 'exhausted' | 'probation' | 'unknown'
export type OutcomeClass = '' | 'exhausted' | 'logged_out' | 'transient' | 'runner_lost' | 'other' | 'fenced'

export interface SourceHealth {
  auth: SourceAuthState
  capacity: SourceCapacityState
  /** Server-normalized absolute reset time; null = unknown. */
  resetAt: string | null
  /** 0..1 remaining; null = unknown (never treated as 0 or 1). */
  headroom: number | null
  /** Transient provider pressure (LunaRoute 429s); never exhaustion. */
  congested: boolean
  congestedUntil: string | null
  lastProbeAt: string | null
  lastOutcome: OutcomeClass
}

export interface RunnerSource {
  id: string
  kind: string
  credentialRef: string
  families: string[]
  backends: string[]
  models: string[]
  health: SourceHealth

  unavailableReason: string
}

/** One machine sample; every metric may be unavailable (null). */
export interface RunnerTelemetry {
  sampledAt: string | null
  cpuPercent: number | null
  memUsedBytes: number | null
  memPressure: string | null
  load1Pct: number | null
  diskFreeBytes: number | null
  thermal: string | null
  uptimeSeconds: number | null
  cores: number | null
  ramBytes: number | null
}

export interface DrainBlocker {
  kind: string
  /** Null when the caller may not see the run. */
  runId: string | null
  group: string
  reason: string
}

export interface DrainProgress {
  activeSteps: number
  homedWorkspaces: number
  sessions: number
  blocked: DrainBlocker[]
  complete: boolean
}

export interface RunnerView {
  id: string
  name: string
  builtIn: boolean
  stepLimit: number
  labels: Record<string, string>
  cordoned: boolean
  draining: boolean
  compat: { state: RunnerCompatState; reason: string }
  liveness: RunnerLiveness
  lastContact: string | null
  os: string
  runnerVersion: string
  harnessVersions: Record<string, string>
  slotsUsed: number
  queued: number
  homedWorkspaces: number
  sources: RunnerSource[]
  telemetry: RunnerTelemetry | null
  drain: DrainProgress | null
}

export interface AssignmentView {
  jobId: string
  /** Null (and hidden true) when the caller may not see the run. */
  runId: string | null
  nodeId: string
  state: string
  sourceId: string
  hidden: boolean
}

/** RunnerDetail `sessions` item (P4 T14). */
export interface SessionPinView {
  runId: string | null
  nodeId: string
  hidden: boolean
  generation: number
}

/** RunnerDetail `homes` item (P3 T14). */
export interface HomeView {
  runId: string | null
  hidden: boolean
  generation: number
  dirty: boolean
}

export interface RunnerDetail extends RunnerView {
  assignments: AssignmentView[]
  recentTelemetry: RunnerTelemetry[]
  sessions: SessionPinView[]
  homes: HomeView[]
}

export interface PoolEventCause {
  kind: string
  actor: string
  outcome: OutcomeClass
}

export interface PoolEvent {
  id: string
  at: string
  kind: string
  runnerId: string
  sourceId: string
  /** Null for system-scope events or runs the caller may not see. */
  runId: string | null
  cause: PoolEventCause
  detail: Record<string, unknown>
}

/** P2 T3 `runnerKeyView`. */
export interface RunnerKey {
  id: string
  name: string
  runnerId: string
  /** Non-secret display prefix of the key. */
  prefix: string
  claimed: boolean
  revoked: boolean
  createdAt: string
  /** Null until the first machine claims the key (server omits it). */
  claimedAt: string | null
  lastUsedAt: string | null
  revokedAt: string | null
}

/** P2 T3 `runnerKeyCreateResponse`. Only ever held in the Add-runner modal's local state; never cached or stored. */
export interface RunnerKeyCreateResponse {
  key: RunnerKey
  /** The one-time key secret. */
  plaintext: string
  /** Server setup guidance (`runner.KeySetupGuidance`). */
  setup: string
}

export type ExecutionState = 'queued_slot' | 'waiting_capacity' | 'running' | 'moving_workspace' | 'recovering'

export interface MoveProgress {
  bytesDone: number
  bytesTotal: number
}

export interface ExecutionStatus {
  runId: string
  nodeId: string
  operationId: string
  jobId: string
  state: ExecutionState
  runnerId?: string
  sourceId?: string
  group?: string
  model?: string
  reason?: string
  nextReset?: string
  progress?: MoveProgress
  homeRunnerId?: string
  sessionPinRunnerId?: string
}

/** `placementHistory` item: P1 T14 `placementRecordResponse`. */
export interface PlacementRecord {
  jobId: string
  runnerId: string
  /** Omitted by the server when empty. */
  sourceId?: string
  reason: string
  /** "completed" | "exhausted" | "logged_out" | "transient" | "runner_lost" | "other" | "fenced" */
  outcome: string
  generation: number
  at: string
}

/** `workspaceMoves` item: P3 T12 `instance.WorkspaceMove`. */
export interface HomeMove {
  /** "" = server checkpoint only. */
  from: string
  to: string
  reason: string
  bytes: number
  generation: number
  at: string

  targetJobId?: string | null
  invocationId?: string | null
  nodeId?: string
  operationId?: string
  fromGeneration?: number
  toGeneration?: number
}

/** `sessionMoves` item on a trace node execution: P4 T1 `instance.SessionMoveRecord`. */
export interface SessionMove {
  requestId: string
  turnIndex: number
  id: string
  runId: string
  invocationId: string
  nodeId: string
  fromRunner: string
  toRunner: string
  /** exhausted | logged_out | drain | runner_lost */
  reason: string
  lossy: boolean
  /** Omitted by the server when empty. */
  notice?: string
  generation: number
  at: string
}

/** `recovery` item on instance and trace reads: P3 T10 `instance.RecoveryNotice`. */
export interface RecoveryNotice {
  nodeId: string
  jobId: string
  runnerId: string
  reason: string
  at: string
  /** Server disclosure text (`instance.RecoveryNoticeText`). */
  text: string
}
