import { memo } from 'react'
import type { CSSProperties } from 'react'
import { Handle, Position } from '@xyflow/react'
import type { Node, NodeProps } from '@xyflow/react'
import type { NodeExecutionState } from '../types'
import type { NodeBox, RendererContract } from './types'
import { typeAccent } from '../nodes/typeColors'
import styles from './ArrangedNode.module.css'
import { GeometryHandles, type GeometryHandle } from './GeometryHandles'
import type { RunnerMark } from '../runnerMarks'
import RunnerStripe from '../nodes/RunnerStripe'

export interface ArrangedNodeData {
  definitionNodeId?: string
  instanceId?: string | null
  independent?: boolean
  hiddenDependency?: boolean
  layoutInstanceId?: string
  handles?: GeometryHandle[]
  nodeBox: NodeBox
  nodeType: string
  executionState: NodeExecutionState
  contract: RendererContract
  runnerMark?: RunnerMark
  [key: string]: unknown
}

/** Class names the measure surface (P3T4) applies to measure text exactly as nodes render it. */
export const nodeTextClassNames = { label: styles.label, typeCaption: styles.typeCaption }

const stateClass: Record<NodeExecutionState, string> = {
  pending: styles.statePending,
  running: styles.stateRunning,
  completed: styles.stateCompleted,
  failed: styles.stateFailed,
  waiting: styles.stateWaiting,
  skipped: styles.stateSkipped,
  queued: styles.stateQueued,
  paused: styles.statePaused,
  cancelled: styles.stateCancelled,
  unknown: styles.stateUnknown,
}

type ArrangedNodeType = Node<ArrangedNodeData, 'arrangedNode'>

function ArrangedNode({ data, selected }: NodeProps<ArrangedNodeType>) {
  const { nodeBox, contract } = data
  const state: NodeExecutionState = data.executionState ?? 'pending'
  const className = [styles.node, stateClass[state], selected ? styles.selected : '']
    .filter(Boolean)
    .join(' ')

  // Border width is set here (constant in every state); colour comes from the
  // state class. Never use the `border` shorthand: it would reset the colour.
  const rootStyle = {
    width: nodeBox.box.w,
    height: nodeBox.box.h,
    borderWidth: contract.nodeBorderPx,
    '--node-text-max': `${contract.nodeTextMaxWidth}px`,
  } as CSSProperties

  return (
    <div className={className} style={rootStyle} data-arranged-node={nodeBox.id} data-layout-instance={data.layoutInstanceId} data-state={state}
      aria-label={data.hiddenDependency ? 'hidden dependency' : data.independent ? 'continuing independently' : undefined}>
      {data.handles ? <GeometryHandles box={nodeBox.box} handles={data.handles} /> : <Handle type="target" position={Position.Top} isConnectable={false} />}
      <div
        className={styles.typeBar}
        style={{ height: contract.nodeTypeBarPx, background: typeAccent(data.nodeType) }}
      />
      <div
        className={styles.content}
        style={{ padding: `${contract.nodePaddingY}px ${contract.nodePaddingX}px` }}
      >
        {nodeBox.typeCaption && (
          <span
            data-node-text="type"
            className={styles.typeCaption}
            style={{ lineHeight: contract.lineHeight }}
          >
            {nodeBox.typeCaption}
          </span>
        )}
        <span
          data-node-text="label"
          className={styles.label}
          style={{ lineHeight: contract.lineHeight, marginTop: nodeBox.typeCaption ? contract.nodeTextGapPx : 0 }}
        >
          {nodeBox.text}
        </span>
      </div>
      <span
        className={styles.stateDot}
        style={{ top: contract.nodeTypeBarPx + 4 }}
        aria-hidden="true"
      />
      <RunnerStripe mark={data.runnerMark} />
      {!data.handles && <Handle type="source" position={Position.Bottom} isConnectable={false} />}
    </div>
  )
}

export default memo(ArrangedNode)
