import { memo } from 'react'
import { Handle, Position } from '@xyflow/react'
import type { NodeProps, Node } from '@xyflow/react'
import type { NodeExecutionState } from '../types'
import styles from './ArbitronNode.module.css'

/** Accent colors keyed by workflow node type. */
const typeColors: Record<string, string> = {
  llm_call: '#7c8cf5',
  script: '#e67e22',
  tool_call: '#1abc9c',
  conditional: '#f39c12',
  transform: '#9b59b6',
  timer_delay: '#00bcd4',
  webhook_wait: '#e91e63',
  spawn_workflow: '#2ecc71',
}

const stateNodeClass: Record<NodeExecutionState, string> = {
  completed: styles.stateCompleted,
  running: styles.stateRunning,
  pending: styles.statePending,
  failed: styles.stateFailed,
  waiting: styles.stateWaiting,
  skipped: styles.stateSkipped,
}

const stateIndicatorClass: Record<NodeExecutionState, string> = {
  completed: styles.indicatorCompleted,
  running: styles.indicatorRunning,
  pending: styles.indicatorPending,
  failed: styles.indicatorFailed,
  waiting: styles.indicatorWaiting,
  skipped: styles.indicatorSkipped,
}

export interface ArbitronNodeData {
  label: string
  nodeType: string
  executionState: NodeExecutionState
  [key: string]: unknown
}

type ArbitronNodeType = Node<ArbitronNodeData, 'arbitronNode'>

function ArbitronNode({ data, selected }: NodeProps<ArbitronNodeType>) {
  const accentColor = typeColors[data.nodeType] ?? '#888888'
  const execState = data.executionState ?? 'pending'
  const nodeClasses = [
    styles.node,
    stateNodeClass[execState],
    selected ? styles.selected : '',
  ].filter(Boolean).join(' ')

  return (
    <div className={nodeClasses}>
      <Handle type="target" position={Position.Top} />
      <div className={styles.typeBar} style={{ background: accentColor }} />
      <div className={styles.content}>
        <div className={styles.typeLabel} style={{ color: accentColor }}>
          {data.nodeType.replace(/_/g, ' ')}
        </div>
        <div className={styles.name}>
          <span
            className={`${styles.stateIndicator} ${stateIndicatorClass[execState]}`}
          />
          {data.label}
        </div>
      </div>
      <Handle type="source" position={Position.Bottom} />
    </div>
  )
}

export default memo(ArbitronNode)
