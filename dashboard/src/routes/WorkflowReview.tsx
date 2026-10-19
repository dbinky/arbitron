import { useState, useEffect, useCallback, useRef } from 'react'
import { useParams, Link } from 'react-router-dom'
import {
  ReactFlowProvider,
  ReactFlow,
  Background,
  Controls,
  type OnNodeDrag,
  type Node,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'

import type { GroupGraph, GroupScope } from '../api/types'
import type { WorkflowDefinition, WorkflowNode } from '../graph/types'
import { api } from '../api/client'
import { useGraphLayout } from '../graph/useGraphLayout'
import { definitionNodeIdOf, toDefinitionPositions } from '../graph/groupLayout'
import { gatherText, ruleText, unfinishedText } from '../graph/groupText'
import { nodeTypes } from '../graph/nodes/nodeTypes'
import { edgeTypes } from '../graph/edges/edgeTypes'
import ResizablePanel from '../components/ResizablePanel'
import UsageStats from '../components/UsageStats'
import SchedulesPanel from '../components/SchedulesPanel'
import TriggerModal from '../components/TriggerModal'
import styles from './WorkflowReview.module.css'

const GROUP_LAYOUT_TIMEOUT_MS = 15_000

/** Persist definition positions, never a group's derived presentation ids. */
function collectPositions(
  nodes: Node[],
): Record<string, { x: number; y: number }> {
  return toDefinitionPositions(nodes)
}

/** Highlight every selected group part except its container box. */
function isSelected(n: Node, selectedNodeId: string | null): boolean {
  if (selectedNodeId == null || n.type === 'groupContainer') return false
  return definitionNodeIdOf(n) === selectedNodeId
}

function ReviewGraph({
  workflowDef,
  groupLayout,
  onNodeClick,
  selectedNodeId,
  workflowName,
}: {
  workflowDef: WorkflowDefinition
  groupLayout: GroupGraph | null
  onNodeClick: (nodeId: string) => void
  selectedNodeId: string | null
  workflowName: string
}) {
  const { rfNodes, rfEdges, needsAutoSave } = useGraphLayout({
    nodes: workflowDef.nodes,
    edges: workflowDef.edges,
    groups: groupLayout,
  })

  const [nodes, setNodes] = useState(rfNodes)
  const nodesRef = useRef(nodes)
  nodesRef.current = nodes

  // Sync when layout recomputes.
  useEffect(() => {
    setNodes(rfNodes)
  }, [rfNodes])

  // Auto-save on first layout computation if positions were dagre-generated.
  const autoSaved = useRef(false)
  useEffect(() => {
    if (needsAutoSave && !autoSaved.current && rfNodes.length > 0) {
      autoSaved.current = true
      const positions = collectPositions(rfNodes)
      api.workflows.saveLayout(workflowName, positions).catch(console.error)
    }
  }, [needsAutoSave, rfNodes, workflowName])

  const onNodeDragStop: OnNodeDrag<Node> = useCallback(
    (_event, node) => {
      // Update local state with new position.
      setNodes((prev) =>
        prev.map((n) =>
          n.id === node.id ? { ...n, position: node.position } : n,
        ),
      )
      // Save all positions.
      const updated = nodesRef.current.map((n) =>
        n.id === node.id ? { ...n, position: node.position } : n,
      )
      const positions = collectPositions(updated)
      api.workflows.saveLayout(workflowName, positions).catch(console.error)
    },
    [workflowName],
  )

  const nodesWithSelection = nodes.map((n) => ({
    ...n,
    selected: isSelected(n, selectedNodeId),
  }))

  return (
    <div className={styles.graphContainer}>
      <ReactFlow
        nodes={nodesWithSelection}
        edges={rfEdges}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        onNodeClick={(_event, node) => onNodeClick(definitionNodeIdOf(node))}
        onNodeDragStop={onNodeDragStop}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        minZoom={0.3}
        maxZoom={2}
        nodesDraggable
        proOptions={{ hideAttribution: true }}
      >
        <Background color="#2a2a4a" gap={20} size={1} />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  )
}

/** Static rule, continuation targets and author-ordered tasks. */
function GroupDetails({ scope }: { scope: GroupScope }) {
  const { config } = scope
  return (
    <div className={styles.section} data-testid="review-group-details">
      <div className={styles.sectionTitle}>Parallel Group</div>
      <div className={styles.nodeDetail}>
        <div className={styles.nodeDetailRow}>
          <span className={styles.nodeDetailLabel}>Form</span>
          <span className={styles.nodeDetailValue}>{config.form === 'workflows' ? 'child workflows' : 'branches'}</span>
        </div>
        <div className={styles.nodeDetailRow}>
          <span className={styles.nodeDetailLabel}>Rule</span>
          <span className={styles.nodeDetailValue}>{ruleText(config.completionRule)}</span>
        </div>
        <div className={styles.nodeDetailRow}>
          <span className={styles.nodeDetailLabel}>Unfinished</span>
          <span className={styles.nodeDetailValue}>{unfinishedText(config.unfinishedBehavior)}</span>
        </div>
        <div className={styles.nodeDetailRow}>
          <span className={styles.nodeDetailLabel}>Gather</span>
          <span className={styles.nodeDetailValue}>{gatherText(config.gatherMode)}</span>
        </div>
        <div className={styles.nodeDetailRow}>
          <span className={styles.nodeDetailLabel}>On success</span>
          <span className={styles.nodeDetailValue}>{scope.successTarget || '—'}</span>
        </div>
        <div className={styles.nodeDetailRow}>
          <span className={styles.nodeDetailLabel}>On failure</span>
          <span className={styles.nodeDetailValue}>{scope.failureTarget || '—'}</span>
        </div>
      </div>
      <ol className={styles.groupTaskList} data-testid="review-group-tasks">
        {/* Keep invalid draft tasks visible, even when their keys repeat. */}
        {config.tasks.map((t, i) => (
          <li key={i} className={styles.groupTaskItem}>
            <span className={styles.inputName}>{t.key}</span>
            {t.workflow ? (
              <Link to={`/workflows/${t.workflow}`} className={styles.groupTaskLink}>
                {t.workflow}
              </Link>
            ) : (
              <span className={styles.inputType}>entry {t.entry}</span>
            )}
          </li>
        ))}
      </ol>
    </div>
  )
}

function ReviewSidePanel({
  workflowDef,
  selectedNode,
  groupScope,
}: {
  workflowDef: WorkflowDefinition
  selectedNode: WorkflowNode | null
  groupScope: GroupScope | null
}) {
  return (
    <div className={styles.sidePanel}>
      {/* Usage Stats */}
      <div className={styles.section}>
        <UsageStats workflowName={workflowDef.name} />
      </div>

      {/* Schedules (read-only) */}
      <div className={styles.section}>
        <SchedulesPanel workflowName={workflowDef.name} />
      </div>

      {/* Input Schema */}
      <div className={styles.section}>
        <div className={styles.sectionTitle}>Input Schema</div>
        {workflowDef.inputs && workflowDef.inputs.length > 0 ? (
          <div className={styles.inputList}>
            {workflowDef.inputs.map((input) => (
              <div key={input.name} className={styles.inputItem}>
                <span className={styles.inputName}>{input.name}</span>
                <span className={styles.inputType}>{input.type}</span>
                {input.required && (
                  <span className={styles.inputRequired}>required</span>
                )}
                {input.description && (
                  <span className={styles.inputDesc}>
                    - {input.description}
                  </span>
                )}
              </div>
            ))}
          </div>
        ) : (
          <div className={styles.emptySection}>No inputs defined.</div>
        )}
      </div>

      {/* Node Details */}
      <div className={styles.section}>
        <div className={styles.sectionTitle}>Node Details</div>
        {selectedNode ? (
          <>
            <div className={styles.nodeDetail}>
              <div className={styles.nodeDetailRow}>
                <span className={styles.nodeDetailLabel}>ID</span>
                <span className={styles.nodeDetailValue}>
                  {selectedNode.id}
                </span>
              </div>
              <div className={styles.nodeDetailRow}>
                <span className={styles.nodeDetailLabel}>Type</span>
                <span className={styles.nodeDetailValue}>
                  {selectedNode.type}
                </span>
              </div>
              <div className={styles.nodeDetailRow}>
                <span className={styles.nodeDetailLabel}>Label</span>
                <span className={styles.nodeDetailValue}>
                  {selectedNode.label}
                </span>
              </div>
              {selectedNode.description && (
                <div className={styles.nodeDetailRow}>
                  <span className={styles.nodeDetailLabel}>Description</span>
                  <span className={styles.nodeDetailValue}>
                    {selectedNode.description}
                  </span>
                </div>
              )}
              {selectedNode.entrypoint && (
                <div className={styles.nodeDetailRow}>
                  <span className={styles.nodeDetailLabel}>Entrypoint</span>
                  <span className={styles.nodeDetailValue}>Yes</span>
                </div>
              )}
            </div>
            {groupScope && <GroupDetails scope={groupScope} />}
            {Object.keys(selectedNode.config).length > 0 && (
              <div className={styles.configBlock}>
                <div className={styles.sectionTitle}>Config</div>
                <pre className={styles.configPre}>
                  {JSON.stringify(selectedNode.config, null, 2)}
                </pre>
              </div>
            )}
          </>
        ) : (
          <div className={styles.emptySection}>
            Click a node to view its details.
          </div>
        )}
      </div>
    </div>
  )
}

export default function WorkflowReview() {
  const { name } = useParams<{ name: string }>()
  const [workflowDef, setWorkflowDef] = useState<WorkflowDefinition | null>(
    null,
  )
  const [groupLayout, setGroupLayout] = useState<GroupGraph | null>(null)
  const [groupLayoutError, setGroupLayoutError] = useState<string | null>(null)
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
  const [showTrigger, setShowTrigger] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!name) return
    let cancelled = false
    const controller = new AbortController()
    const aborted = new Promise<never>((_, reject) => {
      controller.signal.addEventListener('abort', () => reject(controller.signal.reason), { once: true })
    })
    const deadline = setTimeout(
      () => controller.abort(new Error('Group layout request timed out')),
      GROUP_LAYOUT_TIMEOUT_MS,
    )
    async function load() {
      try {
        // Layout analysis failure degrades to the plain graph, not a failed page.
        const [data, layout] = await Promise.all([
          api.workflows.get(name!),
          // Race the deadline as well as passing its signal: a stalled body or
          // transport that ignores abort must not hold the whole page open.
          Promise.race([api.workflows.groupLayout(name!, controller.signal), aborted]).finally(
            () => clearTimeout(deadline),
          ).then(
            (graph) => ({ graph, error: null as string | null }),
            (err: unknown) => ({ graph: null, error: err instanceof Error ? err.message : 'Failed to load group layout' }),
          ),
        ])
        if (!cancelled) {
          setWorkflowDef(data)
          setGroupLayout(layout.graph)
          setGroupLayoutError(layout.error)
        }
      } catch (err) {
        controller.abort()
        if (!cancelled)
          setError(
            err instanceof Error ? err.message : 'Failed to load workflow',
          )
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void load()
    return () => {
      cancelled = true
      clearTimeout(deadline)
      controller.abort()
    }
  }, [name])

  const handleNodeClick = useCallback((nodeId: string) => {
    setSelectedNodeId(nodeId)
  }, [])

  const selectedNode =
    workflowDef?.nodes.find((n) => n.id === selectedNodeId) ?? null
  const selectedGroup = (selectedNodeId && groupLayout?.groups[selectedNodeId]) || null
  const hasGroupNodes = workflowDef?.nodes.some((n) => n.type === 'parallel_group') ?? false

  if (loading) {
    return <div className={styles.loading}>Loading workflow...</div>
  }

  if (error) {
    return <div className={styles.error}>{error}</div>
  }

  if (!workflowDef || !name) {
    return <div className={styles.error}>Workflow not found</div>
  }

  const errorBehavior = workflowDef.defaultErrorBehavior
    ? Object.entries(workflowDef.defaultErrorBehavior)
        .map(([k, v]) => `${k}: ${String(v)}`)
        .join(', ')
    : 'none'

  return (
    <ReactFlowProvider>
      <div className={styles.page}>
        {/* Breadcrumb / metadata bar */}
        <div className={styles.breadcrumb}>
          <Link to="/workflows" className={styles.breadcrumbLink}>
            Workflows
          </Link>
          <span className={styles.breadcrumbSep}>/</span>
          <span className={styles.breadcrumbCurrent}>{workflowDef.name}</span>

          <span className={styles.metaItem}>
            Author: <span className={styles.metaValue}>{workflowDef.author}</span>
          </span>
          <span className={styles.metaItem}>
            Nodes:{' '}
            <span className={styles.metaValue}>
              {workflowDef.nodes.length}
            </span>
          </span>
          <span className={styles.metaItem}>
            Error:{' '}
            <span className={styles.metaValue}>{errorBehavior}</span>
          </span>

          {groupLayoutError && hasGroupNodes && (
            <span className={styles.metaItem} role="status">
              Group layout unavailable: {groupLayoutError}
            </span>
          )}

          <div className={styles.spacer} />

          <button
            className={styles.triggerButton}
            onClick={() => setShowTrigger(true)}
          >
            Trigger
          </button>
        </div>

        {/* Main content: graph + side panel */}
        <ResizablePanel
          left={
            <ReviewGraph
              workflowDef={workflowDef}
              groupLayout={groupLayout}
              onNodeClick={handleNodeClick}
              selectedNodeId={selectedNodeId}
              workflowName={name}
            />
          }
          right={
            <ReviewSidePanel
              workflowDef={workflowDef}
              selectedNode={selectedNode}
              groupScope={selectedGroup}
            />
          }
        />

        {/* Trigger modal */}
        {showTrigger && (
          <TriggerModal
            workflowName={name}
            inputs={workflowDef.inputs ?? []}
            onClose={() => setShowTrigger(false)}
          />
        )}
      </div>
    </ReactFlowProvider>
  )
}
