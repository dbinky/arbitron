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
  /** Latest LLM model used; empty for queued/script-only runs. */
  model?: string
  /** Configured agent backend of the relevant node; empty when unset. */
  backend?: string
  /** Max retry attempt across the instance's node executions. */
  retryCount: number
  /** 1-based priority-aware position for queued rows; 0/absent otherwise. */
  queuePosition?: number
  /** Failed node id, for the detail panel's lazy last-error load. Absent for non-failed rows. */
  failedNodeId?: string
}

/** Full instance representation returned by the get endpoint. */
export interface Instance {
  id: string
  ownerId: string
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
  data: { nodeId: string; messages: InteractiveMessage[] }
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
  data: { reason: 'llm_completed' | 'user_ended'; output?: unknown }
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

