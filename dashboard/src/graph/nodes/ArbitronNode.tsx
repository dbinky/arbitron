import { memo } from 'react'
import { Handle, Position } from '@xyflow/react'
import type { NodeProps, Node } from '@xyflow/react'
import type { NodeType } from '../../api/types'
import type { NodeExecutionState } from '../types'
import styles from './ArbitronNode.module.css'

/**
 * Accent colors keyed by workflow node type. `satisfies` rejects a key that is
 * not a known NodeType (a typo); any type without an entry — including one the
 * backend adds later — falls back to the default grey below.
 */
const typeColors: Record<string, string> = {
  llm_call: '#7c8cf5',
  script: '#e67e22',
  tool_call: '#1abc9c',
  conditional: '#f39c12',
  transform: '#9b59b6',
  timer_delay: '#00bcd4',
  webhook_wait: '#e91e63',
  spawn_workflow: '#2ecc71',
  parallel_group: '#3498db',
} satisfies Partial<Record<NodeType, string>>

const stateNodeClass: Record<NodeExecutionState, string> = {
  completed: styles.stateCompleted,
  running: styles.stateRunning,
  pending: styles.statePending,
  failed: styles.stateFailed,
  waiting: styles.stateWaiting,
  skipped: styles.stateSkipped,
  queued: styles.stateQueued,
  paused: styles.statePaused,
  cancelled: styles.stateCancelled,
  unknown: styles.stateUnknown,
}

const stateIndicatorClass: Record<NodeExecutionState, string> = {
  completed: styles.indicatorCompleted,
  running: styles.indicatorRunning,
  pending: styles.indicatorPending,
  failed: styles.indicatorFailed,
  waiting: styles.indicatorWaiting,
  skipped: styles.indicatorSkipped,
  queued: styles.indicatorQueued,
  paused: styles.indicatorPaused,
  cancelled: styles.indicatorCancelled,
  unknown: styles.indicatorUnknown,
}

export interface ArbitronNodeData {
  label: string
  nodeType: string
  executionState: NodeExecutionState
  /** Owning instance; null for unlaunched or hidden work. */
  instanceId?: string | null
  independent?: boolean
  hiddenDependency?: boolean
  [key: string]: unknown
}

type ArbitronNodeType = Node<ArbitronNodeData, 'arbitronNode'>

function ArbitronNode({ data, selected }: NodeProps<ArbitronNodeType>) {
  const accentColor = typeColors[data.nodeType] ?? '#888888'
  const execState = data.executionState ?? 'pending'
  const nodeClasses = [
    styles.node,
    stateNodeClass[execState] ?? styles.statePending,
    data.independent ? styles.independent : '',
    selected ? styles.selected : '',
  ].filter(Boolean).join(' ')

  return (
    <div className={nodeClasses} data-state={execState}>
      <Handle type="target" position={Position.Top} />
      <div className={styles.typeBar} style={{ background: accentColor }} />
      <div className={styles.content}>
        <div className={styles.typeLabel} style={{ color: accentColor }}>
          {data.nodeType.replace(/_/g, ' ')}
        </div>
        <div className={styles.name}>
          <span
            className={`${styles.stateIndicator} ${stateIndicatorClass[execState] ?? styles.indicatorPending}`}
          />
          {data.label}
        </div>
        {data.hiddenDependency && <div className={styles.note}>hidden dependency</div>}
        {!data.hiddenDependency && data.independent && <div className={styles.note}>non-blocking</div>}
      </div>
      <Handle type="source" position={Position.Bottom} />
    </div>
  )
}

export default memo(ArbitronNode)
