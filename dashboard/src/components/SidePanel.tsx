import { useState, useEffect, useMemo } from 'react'
import type { NodeExecution, InteractiveMessage } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'
import NodeOutput from './NodeOutput'
import VariablesPanel from './VariablesPanel'
import InteractiveTranscript from './InteractiveTranscript'
import styles from './SidePanel.module.css'

type Tab = 'output' | 'variables' | 'transcript'

interface SidePanelProps {
  execution: NodeExecution | null
  variables: Record<string, unknown> | null
  /** Currently selected node ID in the graph. */
  selectedNodeId?: string | null
  /** Workflow definition for looking up node types. */
  workflowDef?: WorkflowDefinition | null
}

/**
 * Check whether a node execution contains an interactive conversation
 * transcript. The backend stores it in the output under `interactiveMessages`
 * or as the `llmConversation` field with role/content pairs.
 */
function extractTranscript(execution: NodeExecution | null): InteractiveMessage[] | null {
  if (!execution) return null

  // Check output.interactiveMessages first (primary location)
  const outputMessages = execution.output?.interactiveMessages
  if (Array.isArray(outputMessages) && outputMessages.length > 0) {
    return outputMessages as InteractiveMessage[]
  }

  // Fall back to llmConversation if it looks like an interactive transcript
  if (execution.llmConversation && typeof execution.llmConversation === 'object') {
    const conv = execution.llmConversation
    // Check if it's an array of {role, content} objects
    if (Array.isArray(conv) && conv.length > 0) {
      const first = conv[0] as Record<string, unknown>
      if (first.role && first.content) {
        return conv as unknown as InteractiveMessage[]
      }
    }
    // Check if it has a messages array
    const messages = (conv as Record<string, unknown>).messages
    if (Array.isArray(messages) && messages.length > 0) {
      return messages as InteractiveMessage[]
    }
  }

  return null
}

export default function SidePanel({
  execution,
  variables,
  selectedNodeId,
  workflowDef,
}: SidePanelProps) {
  const [activeTab, setActiveTab] = useState<Tab>('output')

  // Check if the selected node is an interactive type
  const isInteractiveNode = useMemo(() => {
    if (!selectedNodeId || !workflowDef) return false
    const node = workflowDef.nodes.find((n) => n.id === selectedNodeId)
    if (!node) return false
    // Interactive nodes are identified by type or by config flag
    return (
      node.type === 'interactive' ||
      node.type === 'interactive_call' ||
      node.config?.interactive === true
    )
  }, [selectedNodeId, workflowDef])

  // Extract transcript from completed interactive executions
  const transcript = useMemo(() => extractTranscript(execution), [execution])

  // Show transcript tab when the node is interactive or has transcript data
  const showTranscriptTab = isInteractiveNode || transcript !== null

  // Auto-switch to transcript tab when clicking a completed interactive node
  // that has transcript data; otherwise default to output tab.
  useEffect(() => {
    if (transcript && showTranscriptTab) {
      setActiveTab('transcript')
    } else if (activeTab === 'transcript' && !showTranscriptTab) {
      setActiveTab('output')
    }
    // Only trigger on node/execution changes, not on activeTab changes
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedNodeId, transcript, showTranscriptTab])

  return (
    <div className={styles.panel}>
      <div className={styles.tabs}>
        <button
          className={`${styles.tab} ${activeTab === 'output' ? styles.tabActive : ''}`}
          onClick={() => setActiveTab('output')}
        >
          Node Output
        </button>
        {showTranscriptTab && (
          <button
            className={`${styles.tab} ${activeTab === 'transcript' ? styles.tabActive : ''}`}
            onClick={() => setActiveTab('transcript')}
          >
            Transcript
          </button>
        )}
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
        ) : activeTab === 'transcript' ? (
          <InteractiveTranscript messages={transcript} />
        ) : (
          <VariablesPanel variables={variables} />
        )}
      </div>
    </div>
  )
}
