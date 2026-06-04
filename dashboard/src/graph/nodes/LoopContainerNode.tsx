import { memo } from 'react'
import type { NodeProps, Node } from '@xyflow/react'
import styles from './LoopContainerNode.module.css'

export interface LoopContainerData {
  label: string
  depth: number
  [key: string]: unknown
}

type LoopContainerNodeType = Node<LoopContainerData, 'loopContainer'>

function LoopContainerNode({ data }: NodeProps<LoopContainerNodeType>) {
  return (
    <div className={styles.container}>
      <span className={styles.label}>{data.label}</span>
    </div>
  )
}

export default memo(LoopContainerNode)
