import { useState } from 'react'
import type { NodeExecution } from '../api/types'
import NodeOutput from './NodeOutput'
import VariablesPanel from './VariablesPanel'
import styles from './SidePanel.module.css'

type Tab = 'output' | 'variables'

interface SidePanelProps {
  execution: NodeExecution | null
  variables: Record<string, unknown> | null
}

export default function SidePanel({ execution, variables }: SidePanelProps) {
  const [activeTab, setActiveTab] = useState<Tab>('output')

  return (
    <div className={styles.panel}>
      <div className={styles.tabs}>
        <button
          className={`${styles.tab} ${activeTab === 'output' ? styles.tabActive : ''}`}
          onClick={() => setActiveTab('output')}
        >
          Node Output
        </button>
        <button
          className={`${styles.tab} ${activeTab === 'variables' ? styles.tabActive : ''}`}
          onClick={() => setActiveTab('variables')}
        >
          Variables
        </button>
      </div>
      <div className={styles.content}>
        {activeTab === 'output' ? (
          <NodeOutput execution={execution} />
        ) : (
          <VariablesPanel variables={variables} />
        )}
      </div>
    </div>
  )
}
