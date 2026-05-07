export type NodeExecutionState = 'pending' | 'running' | 'completed' | 'failed' | 'waiting' | 'skipped'

export interface WorkflowNode {
  id: string
  type: string
  label: string
  description: string
  config: Record<string, unknown>
  entrypoint?: boolean
  position?: { x: number; y: number }
}

export interface WorkflowEdge {
  source: string
  target: string
  output?: string
  label?: string
}

export interface WorkflowDefinition {
  name: string
  description: string
  author: string
  nodes: WorkflowNode[]
  edges: WorkflowEdge[]
  inputs?: Array<{ name: string; type: string; description: string; default?: unknown; required: boolean }>
  defaultErrorBehavior?: Record<string, unknown>
}
