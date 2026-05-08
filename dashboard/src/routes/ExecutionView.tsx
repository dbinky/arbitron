import { useState, useEffect, useCallback, useMemo } from 'react'
import { useParams, Link } from 'react-router-dom'
import { ReactFlowProvider, ReactFlow, Background, Controls } from '@xyflow/react'
import '@xyflow/react/dist/style.css'

import type { Instance, NodeExecution, WebSocketEvent } from '../api/types'
import type { WorkflowDefinition, NodeExecutionState } from '../graph/types'
import { api } from '../api/client'
import { useGraphLayout } from '../graph/useGraphLayout'
import { nodeTypes } from '../graph/nodes/nodeTypes'
import { useInstanceWebSocket } from '../hooks/useInstanceWebSocket'
import { useInteractiveSession } from '../hooks/useInteractiveSession'
import CalloutConnector from '../graph/CalloutConnector'
import StateBadge from '../components/StateBadge'
import InstanceControls from '../components/InstanceControls'
import ResizablePanel from '../components/ResizablePanel'
import SidePanel from '../components/SidePanel'
import InteractivePanel from '../components/InteractivePanel'
import styles from './ExecutionView.module.css'

/**
 * Map trace data + instance state onto per-node execution states for the graph.
 */
function buildExecutionStates(
  instance: Instance | null,
  trace: NodeExecution[],
  workflowDef: WorkflowDefinition | null,
): Record<string, NodeExecutionState> {
  const states: Record<string, NodeExecutionState> = {}

  if (!workflowDef) return states

  // Initialize all nodes as pending.
  for (const node of workflowDef.nodes) {
    states[node.id] = 'pending'
  }

  // Apply trace results.
  for (const exec of trace) {
    switch (exec.status) {
      case 'succeeded':
        states[exec.nodeId] = 'completed'
        break
      case 'failed':
        states[exec.nodeId] = 'failed'
        break
      case 'skipped':
        states[exec.nodeId] = 'skipped'
        break
    }
  }

  // Mark the current node as running/waiting if the instance is active.
  if (instance?.currentNodeId) {
    if (instance.state === 'running') {
      states[instance.currentNodeId] = 'running'
    } else if (instance.state === 'waiting') {
      states[instance.currentNodeId] = 'waiting'
    }
  }

  return states
}

function GraphView({
  instance,
  workflowDef,
  trace,
  onNodeClick,
  selectedNodeId,
}: {
  instance: Instance | null
  workflowDef: WorkflowDefinition | null
  trace: NodeExecution[]
  onNodeClick: (nodeId: string) => void
  selectedNodeId: string | null
}) {
  const executionStates = buildExecutionStates(instance, trace, workflowDef)
  const { rfNodes, rfEdges } = useGraphLayout({
    nodes: workflowDef?.nodes ?? [],
    edges: workflowDef?.edges ?? [],
    executionStates,
  })

  // Mark selected node
  const nodesWithSelection = rfNodes.map((n) => ({
    ...n,
    selected: n.id === selectedNodeId,
  }))

  return (
    <div className={styles.graphContainer}>
      <ReactFlow
        nodes={nodesWithSelection}
        edges={rfEdges}
        nodeTypes={nodeTypes}
        onNodeClick={(_event, node) => onNodeClick(node.id)}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        minZoom={0.3}
        maxZoom={2}
        proOptions={{ hideAttribution: true }}
      >
        <Background color="#2a2a4a" gap={20} size={1} />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  )
}

const priorityClass: Record<string, string> = {
  high: styles.priorityHigh,
  normal: styles.priorityNormal,
  low: styles.priorityLow,
}

export default function ExecutionView() {
  const { id } = useParams<{ id: string }>()
  const [instance, setInstance] = useState<Instance | null>(null)
  const [workflowDef, setWorkflowDef] = useState<WorkflowDefinition | null>(null)
  const [trace, setTrace] = useState<NodeExecution[]>([])
  const [variables, setVariables] = useState<Record<string, unknown> | null>(null)
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
  const [selectedExec, setSelectedExec] = useState<NodeExecution | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Interactive session state.
  const interactiveSession = useInteractiveSession(id)

  // Resolve the interactive session's nodeId to a human-readable label.
  const interactiveNodeName = useMemo(() => {
    if (!interactiveSession.nodeId || !workflowDef) return 'Unknown Node'
    const node = workflowDef.nodes.find(
      (n) => n.id === interactiveSession.nodeId,
    )
    return node?.label ?? interactiveSession.nodeId
  }, [interactiveSession.nodeId, workflowDef])

  // Fetch initial data.
  const fetchData = useCallback(async () => {
    if (!id) return
    try {
      const [inst, traceData, vars] = await Promise.all([
        api.instances.get(id),
        api.instances.getTrace(id),
        api.instances.getVariables(id),
      ])
      setInstance(inst)
      setTrace(traceData)
      setVariables(vars)

      if (inst.workflowSnapshot) {
        setWorkflowDef(inst.workflowSnapshot as unknown as WorkflowDefinition)
      }

      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load instance')
    } finally {
      setLoading(false)
    }
  }, [id])

  useEffect(() => {
    fetchData()
  }, [fetchData])

  // Handle WebSocket events for live updates.
  const handleWsEvent = useCallback(
    (event: WebSocketEvent) => {
      const data = event.data as Record<string, unknown> | undefined

      switch (event.type) {
        case 'node.started':
        case 'node.completed':
        case 'node.failed':
        case 'node.skipped':
          // Refresh trace and instance on node state changes.
          if (id) {
            api.instances.getTrace(id).then(setTrace).catch(console.error)
            api.instances.get(id).then((inst) => {
              setInstance(inst)
              if (inst.workflowSnapshot) {
                setWorkflowDef(inst.workflowSnapshot as unknown as WorkflowDefinition)
              }
            }).catch(console.error)
          }
          break
        case 'variables.updated':
          if (data && typeof data === 'object' && 'variables' in data) {
            setVariables(data.variables as Record<string, unknown>)
          } else if (id) {
            api.instances.getVariables(id).then(setVariables).catch(console.error)
          }
          break
        case 'instance.state_changed':
          if (id) {
            api.instances.get(id).then((inst) => {
              setInstance(inst)
              if (inst.workflowSnapshot) {
                setWorkflowDef(inst.workflowSnapshot as unknown as WorkflowDefinition)
              }
            }).catch(console.error)
          }
          break
      }
    },
    [id],
  )

  const { connected } = useInstanceWebSocket(id, handleWsEvent)

  // Handle node click: fetch node trace data.
  const handleNodeClick = useCallback(
    async (nodeId: string) => {
      setSelectedNodeId(nodeId)
      if (!id) return
      try {
        const exec = await api.instances.getNodeTrace(id, nodeId)
        setSelectedExec(exec)
      } catch {
        // Node may not have been executed yet.
        setSelectedExec(null)
      }
    },
    [id],
  )

  if (loading) {
    return <div className={styles.loading}>Loading instance...</div>
  }

  if (error) {
    return <div className={styles.error}>{error}</div>
  }

  if (!instance) {
    return <div className={styles.error}>Instance not found</div>
  }

  const shortId = instance.id.slice(0, 8)

  return (
    <ReactFlowProvider>
      <div className={styles.page}>
        {/* Breadcrumb bar */}
        <div className={styles.breadcrumb}>
          <Link to="/" className={styles.breadcrumbLink}>
            Queue
          </Link>
          <span className={styles.breadcrumbSep}>/</span>
          <span className={styles.breadcrumbCurrent}>
            {instance.workflowName}
          </span>
          <span className={styles.breadcrumbSep}>/</span>
          <span className={styles.instanceId}>{shortId}</span>
          <StateBadge state={instance.state} />
          <span
            className={`${styles.priorityBadge} ${priorityClass[instance.priority] ?? ''}`}
          >
            {instance.priority}
          </span>
          <div
            className={`${styles.connIndicator} ${connected ? styles.connConnected : styles.connDisconnected}`}
            title={connected ? 'WebSocket connected' : 'WebSocket disconnected'}
          />
          <div className={styles.spacer} />
          <InstanceControls
            instanceId={instance.id}
            state={instance.state}
            onAction={fetchData}
          />
        </div>

        {/* Main content: graph + side panel */}
        <ResizablePanel
          left={
            <GraphView
              instance={instance}
              workflowDef={workflowDef}
              trace={trace}
              onNodeClick={handleNodeClick}
              selectedNodeId={selectedNodeId}
            />
          }
          right={
            interactiveSession.isActive ? (
              <InteractivePanel
                session={interactiveSession}
                nodeName={interactiveNodeName}
              />
            ) : (
              <SidePanel
                execution={selectedExec}
                variables={variables}
                selectedNodeId={selectedNodeId}
                workflowDef={workflowDef}
              />
            )
          }
          rightWidthFraction={interactiveSession.isActive ? 0.6 : undefined}
        />

        {/* Connector line from interactive node to panel border */}
        {interactiveSession.nodeId && (
          <CalloutConnector
            nodeId={interactiveSession.nodeId}
            visible={interactiveSession.isActive}
          />
        )}
      </div>
    </ReactFlowProvider>
  )
}
