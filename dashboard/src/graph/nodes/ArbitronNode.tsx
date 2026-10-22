import { memo } from 'react'
import { Handle, Position } from '@xyflow/react'
import type { NodeProps, Node } from '@xyflow/react'
import type { NodeExecutionState } from '../types'
import styles from './ArbitronNode.module.css'
import { typeAccent } from './typeColors'

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
  layoutInstanceId?: string
  /** Owning instance; null for unlaunched or hidden work. */
  instanceId?: string | null
  independent?: boolean
  hiddenDependency?: boolean
  [key: string]: unknown
}

type ArbitronNodeType = Node<ArbitronNodeData, 'arbitronNode'>

function ArbitronNode({ data, selected }: NodeProps<ArbitronNodeType>) {
  const accentColor = typeAccent(data.nodeType)
  const execState = data.executionState ?? 'pending'
  const nodeClasses = [
    styles.node,
    stateNodeClass[execState] ?? styles.statePending,
    data.independent ? styles.independent : '',
    selected ? styles.selected : '',
  ].filter(Boolean).join(' ')

  return (
    <div className={nodeClasses} data-state={execState} data-layout-instance={data.layoutInstanceId} data-layout-mode="fallback">
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
