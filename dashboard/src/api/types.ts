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

/** Lightweight instance representation returned by the list endpoint. */
export interface InstanceSummary {
  id: string
  workflowName: string
  state: InstanceState
  priority: Priority
  currentNodeId?: string
  createdAt: string
  updatedAt: string
  completedAt?: string
  queuedAt?: string
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
}

/** System configuration returned by /system/config. */
export interface SystemConfig {
  server: { host: string; port: number }
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
}

/** Queue configuration update response. */
export interface QueueConfigUpdate {
  globalCap: number
  perUserCap: number
}

/** Domain event streamed over WebSocket. */
export interface WebSocketEvent {
  type: string
  timestamp: string
  data: unknown
}
