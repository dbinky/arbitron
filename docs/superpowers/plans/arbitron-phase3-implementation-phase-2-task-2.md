# Phase 3.2 Task 2: Execution View Page

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the execution view page with workflow graph visualization, resizable side panel with node output and variables tabs, breadcrumb navigation, and instance control buttons.

**Architecture:** The ExecutionView route fetches instance data and connects to the per-instance WebSocket. It renders a React Flow graph with live state updates. A resizable split panel shows node output and variables. Instance controls (pause/resume/cancel/retry/rerun) call the existing REST API.

**Tech Stack:** React, React Flow, TypeScript, CSS Modules

---

### Task 2.1: Resizable split panel component

**Files:**
- Create: `dashboard/src/components/ResizablePanel.tsx`
- Create: `dashboard/src/components/ResizablePanel.module.css`

- [ ] **Step 1: Create ResizablePanel.module.css**

```css
.container {
  display: flex;
  height: 100%;
  overflow: hidden;
}

.left {
  overflow: auto;
  min-width: 200px;
}

.divider {
  width: 4px;
  background: var(--border-color);
  cursor: col-resize;
  flex-shrink: 0;
  transition: background 0.15s;
}

.divider:hover,
.dividerDragging {
  background: var(--accent);
}

.right {
  overflow: auto;
  min-width: 200px;
}
```

- [ ] **Step 2: Create ResizablePanel.tsx**

```tsx
import { useRef, useState, useCallback, type ReactNode } from 'react'
import styles from './ResizablePanel.module.css'

interface Props {
  left: ReactNode
  right: ReactNode
  defaultRightWidth?: number
}

export default function ResizablePanel({ left, right, defaultRightWidth = 360 }: Props) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [rightWidth, setRightWidth] = useState(defaultRightWidth)
  const [dragging, setDragging] = useState(false)

  const handleMouseDown = useCallback(() => {
    setDragging(true)

    const onMouseMove = (e: MouseEvent) => {
      if (!containerRef.current) return
      const rect = containerRef.current.getBoundingClientRect()
      const newRight = rect.right - e.clientX
      setRightWidth(Math.max(200, Math.min(newRight, rect.width - 200)))
    }

    const onMouseUp = () => {
      setDragging(false)
      document.removeEventListener('mousemove', onMouseMove)
      document.removeEventListener('mouseup', onMouseUp)
    }

    document.addEventListener('mousemove', onMouseMove)
    document.addEventListener('mouseup', onMouseUp)
  }, [])

  return (
    <div ref={containerRef} className={styles.container}>
      <div className={styles.left} style={{ flex: 1 }}>{left}</div>
      <div
        className={`${styles.divider} ${dragging ? styles.dividerDragging : ''}`}
        onMouseDown={handleMouseDown}
      />
      <div className={styles.right} style={{ width: rightWidth, flexShrink: 0 }}>{right}</div>
    </div>
  )
}
```

- [ ] **Step 3: Commit**

```bash
git add dashboard/src/components/ResizablePanel.tsx dashboard/src/components/ResizablePanel.module.css
git commit -m "feat(dashboard): add ResizablePanel component with drag divider"
```

---

### Task 2.2: Side panel with tabs (Node Output + Variables)

**Files:**
- Create: `dashboard/src/components/SidePanel.tsx`
- Create: `dashboard/src/components/SidePanel.module.css`
- Create: `dashboard/src/components/NodeOutput.tsx`
- Create: `dashboard/src/components/VariablesPanel.tsx`

- [ ] **Step 1: Create SidePanel.module.css**

```css
.panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: var(--bg-primary);
}

.tabs {
  display: flex;
  border-bottom: 1px solid var(--border-color);
  flex-shrink: 0;
}

.tab {
  flex: 1;
  padding: 8px;
  text-align: center;
  font-size: 12px;
  color: var(--text-dim);
  cursor: pointer;
  border: none;
  background: none;
  border-bottom: 2px solid transparent;
  transition: color 0.15s, border-color 0.15s;
}

.tab:hover {
  color: var(--text-secondary);
}

.tabActive {
  color: var(--accent);
  border-bottom-color: var(--accent);
  background: var(--bg-surface);
}

.content {
  flex: 1;
  overflow: auto;
  padding: 12px;
}
```

- [ ] **Step 2: Create NodeOutput.tsx**

```tsx
import type { NodeExecutionState } from '../graph/types'

interface NodeExecution {
  nodeId: string
  status: string
  resolvedInputs?: Record<string, unknown>
  output?: Record<string, unknown>
  error?: Record<string, unknown>
  llmConversation?: Record<string, unknown>
  llmModel?: string
  llmTokensIn?: number
  llmTokensOut?: number
  scriptStdout?: string
  scriptStderr?: string
  scriptExitCode?: number
  startedAt: string
  completedAt?: string
}

interface Props {
  execution?: NodeExecution
  nodeType?: string
  streamingOutput?: string
}

export default function NodeOutput({ execution, nodeType, streamingOutput }: Props) {
  if (!execution && !streamingOutput) {
    return <div style={{ color: 'var(--text-dim)', fontStyle: 'italic' }}>Click a node to see its output</div>
  }

  return (
    <div>
      {execution && (
        <div style={{ marginBottom: 8 }}>
          <span style={{ color: 'var(--state-running)', fontSize: 11, fontWeight: 600 }}>{execution.nodeId}</span>
          <span style={{ color: 'var(--text-dim)', fontSize: 10, marginLeft: 8 }}>{nodeType}</span>
        </div>
      )}

      {(execution?.scriptStdout || streamingOutput) && (
        <div style={{
          background: '#111',
          borderRadius: 4,
          padding: 10,
          fontFamily: 'var(--font-mono)',
          fontSize: 11,
          lineHeight: 1.6,
          color: 'var(--text-muted)',
          whiteSpace: 'pre-wrap',
          wordBreak: 'break-all',
          maxHeight: 400,
          overflow: 'auto',
        }}>
          {execution?.scriptStdout || streamingOutput}
        </div>
      )}

      {execution?.scriptStderr && (
        <div style={{
          background: '#1a0a0a',
          borderRadius: 4,
          padding: 10,
          fontFamily: 'var(--font-mono)',
          fontSize: 11,
          color: 'var(--state-failed)',
          whiteSpace: 'pre-wrap',
          marginTop: 8,
        }}>
          {execution.scriptStderr}
        </div>
      )}

      {execution?.llmConversation && (
        <div style={{
          background: '#111',
          borderRadius: 4,
          padding: 10,
          fontSize: 12,
          lineHeight: 1.6,
          color: 'var(--text-secondary)',
          maxHeight: 400,
          overflow: 'auto',
        }}>
          <pre style={{ whiteSpace: 'pre-wrap', margin: 0 }}>
            {JSON.stringify(execution.llmConversation, null, 2)}
          </pre>
        </div>
      )}

      {execution?.output && (
        <div style={{ marginTop: 12 }}>
          <div style={{ fontSize: 10, color: 'var(--text-dim)', marginBottom: 4 }}>Output</div>
          <pre style={{
            background: '#111',
            borderRadius: 4,
            padding: 8,
            fontSize: 11,
            color: 'var(--text-muted)',
            whiteSpace: 'pre-wrap',
            margin: 0,
          }}>
            {JSON.stringify(execution.output, null, 2)}
          </pre>
        </div>
      )}

      {execution?.error && (
        <div style={{ marginTop: 12 }}>
          <div style={{ fontSize: 10, color: 'var(--state-failed)', marginBottom: 4 }}>Error</div>
          <pre style={{
            background: '#1a0a0a',
            borderRadius: 4,
            padding: 8,
            fontSize: 11,
            color: 'var(--state-failed)',
            whiteSpace: 'pre-wrap',
            margin: 0,
          }}>
            {JSON.stringify(execution.error, null, 2)}
          </pre>
        </div>
      )}

      {execution && (
        <div style={{ marginTop: 12, fontSize: 10, color: 'var(--text-dim)' }}>
          <div>Started: {new Date(execution.startedAt).toLocaleTimeString()}</div>
          {execution.completedAt && <div>Completed: {new Date(execution.completedAt).toLocaleTimeString()}</div>}
          {execution.llmModel && <div>Model: {execution.llmModel}</div>}
          {execution.llmTokensIn != null && <div>Tokens: {execution.llmTokensIn} in / {execution.llmTokensOut} out</div>}
          {execution.scriptExitCode != null && <div>Exit code: {execution.scriptExitCode}</div>}
        </div>
      )}
    </div>
  )
}
```

- [ ] **Step 3: Create VariablesPanel.tsx**

```tsx
interface Props {
  variables: Record<string, unknown>
}

export default function VariablesPanel({ variables }: Props) {
  const entries = Object.entries(variables)

  if (entries.length === 0) {
    return <div style={{ color: 'var(--text-dim)', fontStyle: 'italic' }}>No variables</div>
  }

  return (
    <div style={{ fontSize: 12 }}>
      {entries.map(([key, value]) => (
        <div key={key} style={{
          display: 'flex',
          justifyContent: 'space-between',
          padding: '6px 0',
          borderBottom: '1px solid var(--border-subtle)',
        }}>
          <span style={{ color: 'var(--accent)', fontFamily: 'var(--font-mono)' }}>{key}</span>
          <span style={{ color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', maxWidth: '60%', overflow: 'hidden', textOverflow: 'ellipsis' }}>
            {typeof value === 'string' ? value : JSON.stringify(value)}
          </span>
        </div>
      ))}
    </div>
  )
}
```

- [ ] **Step 4: Create SidePanel.tsx**

```tsx
import { useState } from 'react'
import NodeOutput from './NodeOutput'
import VariablesPanel from './VariablesPanel'
import styles from './SidePanel.module.css'

interface Props {
  selectedExecution?: any
  selectedNodeType?: string
  streamingOutput?: string
  variables: Record<string, unknown>
}

export default function SidePanel({ selectedExecution, selectedNodeType, streamingOutput, variables }: Props) {
  const [activeTab, setActiveTab] = useState<'output' | 'variables'>('output')

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
          <NodeOutput execution={selectedExecution} nodeType={selectedNodeType} streamingOutput={streamingOutput} />
        ) : (
          <VariablesPanel variables={variables} />
        )}
      </div>
    </div>
  )
}
```

- [ ] **Step 5: Commit**

```bash
git add dashboard/src/components/SidePanel.tsx dashboard/src/components/SidePanel.module.css dashboard/src/components/NodeOutput.tsx dashboard/src/components/VariablesPanel.tsx
git commit -m "feat(dashboard): add SidePanel with Node Output and Variables tabs"
```

---

### Task 2.3: Instance controls component

**Files:**
- Create: `dashboard/src/components/InstanceControls.tsx`

- [ ] **Step 1: Create InstanceControls.tsx**

```tsx
import type { InstanceState } from '../api/types'
import { api } from '../api/client'

interface Props {
  instanceId: string
  state: InstanceState
}

const btnStyle = (color: string) => ({
  background: 'none',
  border: `1px solid ${color}`,
  color,
  padding: '4px 10px',
  borderRadius: 4,
  fontSize: 11,
  cursor: 'pointer',
  marginLeft: 6,
})

export default function InstanceControls({ instanceId, state }: Props) {
  return (
    <div style={{ display: 'flex', alignItems: 'center' }}>
      {state === 'running' && (
        <button style={btnStyle('var(--state-waiting)')} onClick={() => api.instances.pause(instanceId)}>Pause</button>
      )}
      {state === 'paused' && (
        <button style={btnStyle('var(--state-completed)')} onClick={() => api.instances.resume(instanceId)}>Resume</button>
      )}
      {(state === 'running' || state === 'paused' || state === 'queued') && (
        <button style={btnStyle('var(--state-failed)')} onClick={() => api.instances.cancel(instanceId)}>Cancel</button>
      )}
      {state === 'failed' && (
        <button style={btnStyle('var(--state-waiting)')} onClick={() => api.instances.retry(instanceId)}>Retry</button>
      )}
      {(state === 'completed' || state === 'failed' || state === 'cancelled') && (
        <button style={btnStyle('var(--accent)')} onClick={() => api.instances.rerun(instanceId)}>Re-run</button>
      )}
    </div>
  )
}
```

- [ ] **Step 2: Commit**

```bash
git add dashboard/src/components/InstanceControls.tsx
git commit -m "feat(dashboard): add InstanceControls with state-dependent buttons"
```

---

### Task 2.4: Backend — include workflowSnapshot in instance response

**Files:**
- Modify: `adapter/http/instance_handler.go`

- [ ] **Step 1: Add workflowSnapshot to instanceResponse**

In the `instanceResponse` struct, add:
```go
WorkflowSnapshot map[string]any `json:"workflowSnapshot,omitempty"`
```

In `toInstanceResponse`, add:
```go
resp.WorkflowSnapshot = inst.WorkflowSnapshot
```

- [ ] **Step 2: Run tests**

```bash
go test ./adapter/http/ -v
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add adapter/http/instance_handler.go
git commit -m "feat: include workflowSnapshot in instance GET response"
```

---

### Task 2.5: ExecutionView page assembly

**Files:**
- Modify: `dashboard/src/routes/ExecutionView.tsx`

- [ ] **Step 1: Implement ExecutionView**

```tsx
import { useState, useEffect, useCallback, useRef } from 'react'
import { useParams, Link } from 'react-router-dom'
import { ReactFlow, ReactFlowProvider, type NodeMouseHandler } from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import type { Instance, WebSocketEvent } from '../api/types'
import type { WorkflowDefinition, NodeExecutionState } from '../graph/types'
import { api } from '../api/client'
import { nodeTypes } from '../graph/nodes/nodeTypes'
import { useGraphLayout } from '../graph/useGraphLayout'
import ResizablePanel from '../components/ResizablePanel'
import SidePanel from '../components/SidePanel'
import InstanceControls from '../components/InstanceControls'
import StateBadge from '../components/StateBadge'

export default function ExecutionView() {
  const { id } = useParams<{ id: string }>()
  const [instance, setInstance] = useState<Instance | null>(null)
  const [workflow, setWorkflow] = useState<WorkflowDefinition | null>(null)
  const [execStates, setExecStates] = useState<Record<string, NodeExecutionState>>({})
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
  const [selectedExecution, setSelectedExecution] = useState<any>(null)
  const [variables, setVariables] = useState<Record<string, unknown>>({})
  const wsRef = useRef<WebSocket | null>(null)

  useEffect(() => {
    if (!id) return
    async function load() {
      const inst = await api.instances.get(id!)
      setInstance(inst)
      setVariables(inst.variables || {})

      if (inst.workflowSnapshot) {
        setWorkflow(inst.workflowSnapshot as unknown as WorkflowDefinition)
      }

      const trace = await api.instances.getTrace(id!) as any[]
      const states: Record<string, NodeExecutionState> = {}
      for (const exec of trace) {
        states[exec.nodeId] = exec.status === 'succeeded' ? 'completed' : exec.status
      }
      if (inst.currentNodeId && !states[inst.currentNodeId]) {
        states[inst.currentNodeId] = 'running'
      }
      setExecStates(states)
    }
    load()
  }, [id])

  useEffect(() => {
    if (!id) return
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${protocol}//${window.location.host}/api/v1/instances/${id}/ws`
    const ws = new WebSocket(url)

    ws.onmessage = (msg) => {
      try {
        const event: WebSocketEvent = JSON.parse(msg.data)
        if (event.type === 'node.started') {
          const nodeId = (event.data as any).nodeId
          if (nodeId) setExecStates(prev => ({ ...prev, [nodeId]: 'running' }))
        } else if (event.type === 'node.completed') {
          const nodeId = (event.data as any).nodeId
          if (nodeId) setExecStates(prev => ({ ...prev, [nodeId]: 'completed' }))
        } else if (event.type === 'node.failed') {
          const nodeId = (event.data as any).nodeId
          if (nodeId) setExecStates(prev => ({ ...prev, [nodeId]: 'failed' }))
        } else if (event.type === 'state_changed' || event.type.startsWith('instance.')) {
          api.instances.get(id!).then(setInstance)
        } else if (event.type === 'variables_updated') {
          api.instances.getVariables(id!).then(setVariables)
        }
      } catch {}
    }

    ws.onclose = () => {
      // reconnect after delay if not terminal
    }

    wsRef.current = ws
    return () => ws.close()
  }, [id])

  const onNodeClick: NodeMouseHandler = useCallback((_event, node) => {
    setSelectedNodeId(node.id)
    if (id) {
      api.instances.getNodeTrace(id, node.id)
        .then(setSelectedExecution)
        .catch(() => setSelectedExecution(null))
    }
  }, [id])

  const { rfNodes, rfEdges } = useGraphLayout({
    nodes: workflow?.nodes || [],
    edges: workflow?.edges || [],
    executionStates: execStates,
  })

  if (!instance || !workflow) {
    return <div style={{ padding: 20, color: 'var(--text-muted)' }}>Loading...</div>
  }

  const shortId = instance.id.substring(0, 8)

  const graphPanel = (
    <div style={{ height: '100%', background: 'var(--bg-primary)' }}>
      <ReactFlow
        nodes={rfNodes}
        edges={rfEdges}
        nodeTypes={nodeTypes}
        onNodeClick={onNodeClick}
        fitView
        proOptions={{ hideAttribution: true }}
        style={{ background: 'var(--bg-primary)' }}
      />
    </div>
  )

  const sidePanel = (
    <SidePanel
      selectedExecution={selectedExecution}
      selectedNodeType={workflow.nodes.find(n => n.id === selectedNodeId)?.type}
      variables={variables}
    />
  )

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      <div style={{
        padding: '8px 20px',
        fontSize: 11,
        color: 'var(--text-dim)',
        borderBottom: '1px solid var(--border-color)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
      }}>
        <div>
          <Link to="/" style={{ color: 'var(--accent)' }}>Queue</Link>
          {' › '}
          <Link to={`/workflows/${instance.workflowName}`} style={{ color: 'var(--accent)' }}>{instance.workflowName}</Link>
          {' › '}
          <span style={{ color: 'var(--text-muted)' }}>{shortId}</span>
          <span style={{ marginLeft: 12 }}><StateBadge state={instance.state} /></span>
          <span style={{ marginLeft: 8, color: 'var(--priority-' + instance.priority + ')', fontSize: 10 }}>
            {instance.priority} priority
          </span>
        </div>
        <InstanceControls instanceId={instance.id} state={instance.state} />
      </div>
      <div style={{ flex: 1 }}>
        <ReactFlowProvider>
          <ResizablePanel left={graphPanel} right={sidePanel} />
        </ReactFlowProvider>
      </div>
    </div>
  )
}
```

- [ ] **Step 2: Verify execution view renders**

Navigate to `/instances/<some-id>` with a running or completed instance. The graph should render with colored nodes, and clicking a node should show its output in the side panel.

- [ ] **Step 3: Commit**

```bash
git add dashboard/src/routes/ExecutionView.tsx
git commit -m "feat(dashboard): implement ExecutionView with graph, side panel, and controls"
```
