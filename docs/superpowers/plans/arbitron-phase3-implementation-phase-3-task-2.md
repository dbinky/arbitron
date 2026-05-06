# Phase 3.3 Task 2: Workflow Catalog, Review Panel & Settings Pages

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Workflows Catalog page, Workflow Review Panel with draggable graph, usage stats, input schema, trigger modal, and the Settings page.

**Architecture:** The catalog lists workflows from the REST API. The review panel renders the workflow graph with React Flow (drag enabled, positions saved on drag end). The settings page fetches and edits system config via the REST API.

**Tech Stack:** React, React Flow, TypeScript, CSS Modules

---

### Task 2.1: Workflows Catalog page

**Files:**
- Modify: `dashboard/src/routes/WorkflowsCatalog.tsx`

- [ ] **Step 1: Implement the catalog page**

```tsx
import { useState, useEffect, useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import type { WorkflowSummary } from '../api/types'
import { api } from '../api/client'

export default function WorkflowsCatalog() {
  const [workflows, setWorkflows] = useState<WorkflowSummary[]>([])
  const [search, setSearch] = useState('')
  const [loading, setLoading] = useState(true)
  const navigate = useNavigate()

  useEffect(() => {
    api.workflows.list().then(setWorkflows).finally(() => setLoading(false))
  }, [])

  const filtered = useMemo(() => {
    if (!search) return workflows
    const q = search.toLowerCase()
    return workflows.filter(w => w.name.toLowerCase().includes(q))
  }, [workflows, search])

  if (loading) return <div style={{ padding: 20, color: 'var(--text-muted)' }}>Loading...</div>

  return (
    <div style={{ padding: 20 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <h2 style={{ fontSize: 16, fontWeight: 600, color: 'var(--text-secondary)' }}>Workflows</h2>
        <input
          type="text"
          placeholder="Search by name..."
          value={search}
          onChange={e => setSearch(e.target.value)}
          style={{
            background: 'var(--bg-surface)',
            border: '1px solid var(--border-color)',
            borderRadius: 4,
            padding: '6px 12px',
            color: 'var(--text-secondary)',
            fontSize: 12,
            width: 220,
          }}
        />
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        {filtered.length === 0 ? (
          <div style={{ color: 'var(--text-dim)', textAlign: 'center', padding: 20 }}>No workflows found</div>
        ) : (
          filtered.map(wf => (
            <div
              key={wf.name}
              onClick={() => navigate(`/workflows/${wf.name}`)}
              style={{
                display: 'flex',
                alignItems: 'center',
                padding: '10px 16px',
                background: 'var(--bg-surface)',
                borderRadius: 6,
                cursor: 'pointer',
                fontSize: 12,
                transition: 'background 0.15s',
              }}
              onMouseOver={e => (e.currentTarget.style.background = 'var(--bg-hover)')}
              onMouseOut={e => (e.currentTarget.style.background = 'var(--bg-surface)')}
            >
              <span style={{ width: '30%', color: 'var(--text-secondary)', fontWeight: 500 }}>{wf.name}</span>
              <span style={{ width: '40%', color: 'var(--text-muted)' }}>{wf.description || '—'}</span>
              <span style={{ width: '15%', color: 'var(--text-dim)' }}>{wf.author || '—'}</span>
              <span style={{ width: '15%', color: 'var(--text-dim)', textAlign: 'right' }}>
                {wf.nodeCount != null ? `${wf.nodeCount} nodes` : '—'}
              </span>
            </div>
          ))
        )}
      </div>
    </div>
  )
}
```

- [ ] **Step 2: Commit**

```bash
git add dashboard/src/routes/WorkflowsCatalog.tsx
git commit -m "feat(dashboard): implement Workflows Catalog page"
```

---

### Task 2.2: Workflow Review Panel page

**Files:**
- Modify: `dashboard/src/routes/WorkflowReview.tsx`
- Create: `dashboard/src/components/UsageStats.tsx`
- Create: `dashboard/src/components/TriggerModal.tsx`

- [ ] **Step 1: Create UsageStats component**

```tsx
import { useState, useEffect } from 'react'
import { api } from '../api/client'

interface Stats {
  totalRuns: number
  successCount: number
  failureCount: number
  avgDurationSeconds: number
}

interface Props {
  workflowName: string
}

const PRESETS = [
  { label: 'Today', days: 0 },
  { label: 'Yesterday', days: 1 },
  { label: 'Last 7 days', days: 7 },
  { label: 'Last 30 days', days: 30 },
]

function formatDuration(secs: number): string {
  if (secs < 60) return `${Math.round(secs)}s`
  const min = Math.floor(secs / 60)
  const sec = Math.round(secs % 60)
  return `${min}m ${sec}s`
}

export default function UsageStats({ workflowName }: Props) {
  const [stats, setStats] = useState<Stats | null>(null)
  const [selectedPreset, setSelectedPreset] = useState(3) // Last 30 days

  useEffect(() => {
    const now = new Date()
    let from: Date
    const preset = PRESETS[selectedPreset]
    if (preset.days === 0) {
      from = new Date(now.getFullYear(), now.getMonth(), now.getDate())
    } else if (preset.days === 1) {
      from = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1)
    } else {
      from = new Date(now.getTime() - preset.days * 86400000)
    }

    fetch(`/api/v1/workflows/${workflowName}/stats?from=${from.toISOString()}&to=${now.toISOString()}`)
      .then(r => r.json())
      .then(setStats)
      .catch(() => setStats(null))
  }, [workflowName, selectedPreset])

  return (
    <div style={{ marginBottom: 20 }}>
      <div style={{ fontSize: 12, color: 'var(--text-secondary)', fontWeight: 600, marginBottom: 10 }}>Usage Statistics</div>
      <select
        value={selectedPreset}
        onChange={e => setSelectedPreset(Number(e.target.value))}
        style={{
          background: 'var(--bg-hover)',
          color: 'var(--text-muted)',
          border: '1px solid var(--border-color)',
          borderRadius: 4,
          padding: '2px 6px',
          fontSize: 10,
          marginBottom: 8,
        }}
      >
        {PRESETS.map((p, i) => <option key={i} value={i}>{p.label}</option>)}
      </select>
      {stats && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 11 }}>
          <div style={{ display: 'flex', justifyContent: 'space-between' }}>
            <span style={{ color: 'var(--text-muted)' }}>Total runs</span>
            <span style={{ color: 'var(--text-secondary)', fontWeight: 500 }}>{stats.totalRuns}</span>
          </div>
          <div style={{ display: 'flex', justifyContent: 'space-between' }}>
            <span style={{ color: 'var(--text-muted)' }}>Success rate</span>
            <span style={{ color: 'var(--state-completed)', fontWeight: 500 }}>
              {stats.totalRuns > 0 ? ((stats.successCount / stats.totalRuns) * 100).toFixed(1) + '%' : '—'}
            </span>
          </div>
          <div style={{ display: 'flex', justifyContent: 'space-between' }}>
            <span style={{ color: 'var(--text-muted)' }}>Failure rate</span>
            <span style={{ color: 'var(--state-failed)', fontWeight: 500 }}>
              {stats.totalRuns > 0 ? ((stats.failureCount / stats.totalRuns) * 100).toFixed(1) + '%' : '—'}
            </span>
          </div>
          <div style={{ display: 'flex', justifyContent: 'space-between' }}>
            <span style={{ color: 'var(--text-muted)' }}>Avg duration</span>
            <span style={{ color: 'var(--text-secondary)', fontWeight: 500 }}>
              {stats.avgDurationSeconds > 0 ? formatDuration(stats.avgDurationSeconds) : '—'}
            </span>
          </div>
        </div>
      )}
    </div>
  )
}
```

- [ ] **Step 2: Create TriggerModal component**

```tsx
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'

interface InputField {
  name: string
  type: string
  description: string
  default?: unknown
  required: boolean
}

interface Props {
  workflowName: string
  inputs: InputField[]
  onClose: () => void
}

export default function TriggerModal({ workflowName, inputs, onClose }: Props) {
  const [values, setValues] = useState<Record<string, string>>(() => {
    const init: Record<string, string> = {}
    for (const inp of inputs) {
      if (inp.default != null) init[inp.name] = String(inp.default)
    }
    return init
  })
  const [priority, setPriority] = useState('normal')
  const [submitting, setSubmitting] = useState(false)
  const navigate = useNavigate()

  async function handleSubmit() {
    setSubmitting(true)
    try {
      const vars: Record<string, unknown> = {}
      for (const inp of inputs) {
        const raw = values[inp.name]
        if (raw != null && raw !== '') {
          vars[inp.name] = inp.type === 'number' ? Number(raw) : raw
        }
      }
      const result = await api.workflows.trigger(workflowName, vars, priority)
      navigate(`/instances/${result.instanceId}`)
    } catch (err: any) {
      alert(err.message || 'Failed to trigger')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex',
      alignItems: 'center', justifyContent: 'center', zIndex: 1000,
    }} onClick={onClose}>
      <div style={{
        background: 'var(--bg-surface)', borderRadius: 8, padding: 24, minWidth: 360,
        border: '1px solid var(--border-color)',
      }} onClick={e => e.stopPropagation()}>
        <h3 style={{ fontSize: 14, color: 'var(--text-secondary)', marginBottom: 16 }}>Trigger {workflowName}</h3>
        {inputs.map(inp => (
          <div key={inp.name} style={{ marginBottom: 12 }}>
            <label style={{ fontSize: 11, color: 'var(--text-muted)', display: 'block', marginBottom: 4 }}>
              {inp.name} <span style={{ color: inp.required ? 'var(--state-failed)' : 'var(--text-dim)' }}>
                {inp.required ? '(required)' : '(optional)'}
              </span>
            </label>
            <input
              type={inp.type === 'number' ? 'number' : 'text'}
              value={values[inp.name] || ''}
              onChange={e => setValues({ ...values, [inp.name]: e.target.value })}
              style={{
                width: '100%', background: 'var(--bg-primary)', border: '1px solid var(--border-color)',
                borderRadius: 4, padding: '6px 8px', color: 'var(--text-secondary)', fontSize: 12,
              }}
            />
          </div>
        ))}
        <div style={{ marginBottom: 16 }}>
          <label style={{ fontSize: 11, color: 'var(--text-muted)', display: 'block', marginBottom: 4 }}>Priority</label>
          <select
            value={priority}
            onChange={e => setPriority(e.target.value)}
            style={{
              background: 'var(--bg-primary)', border: '1px solid var(--border-color)',
              borderRadius: 4, padding: '6px 8px', color: 'var(--text-secondary)', fontSize: 12,
            }}
          >
            <option value="high">High</option>
            <option value="normal">Normal</option>
            <option value="low">Low</option>
          </select>
        </div>
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <button onClick={onClose} style={{
            background: 'none', border: '1px solid var(--border-color)', color: 'var(--text-muted)',
            padding: '6px 16px', borderRadius: 4, cursor: 'pointer', fontSize: 12,
          }}>Cancel</button>
          <button onClick={handleSubmit} disabled={submitting} style={{
            background: 'var(--accent)', border: 'none', color: 'white',
            padding: '6px 16px', borderRadius: 4, cursor: 'pointer', fontSize: 12,
          }}>{submitting ? 'Triggering...' : 'Trigger'}</button>
        </div>
      </div>
    </div>
  )
}
```

- [ ] **Step 3: Implement WorkflowReview page**

```tsx
import { useState, useEffect, useCallback } from 'react'
import { useParams, Link } from 'react-router-dom'
import { ReactFlow, ReactFlowProvider, type NodeDragHandler } from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import type { WorkflowDefinition } from '../graph/types'
import { nodeTypes } from '../graph/nodes/nodeTypes'
import { useGraphLayout } from '../graph/useGraphLayout'
import ResizablePanel from '../components/ResizablePanel'
import UsageStats from '../components/UsageStats'
import TriggerModal from '../components/TriggerModal'

export default function WorkflowReview() {
  const { name } = useParams<{ name: string }>()
  const [workflow, setWorkflow] = useState<WorkflowDefinition | null>(null)
  const [selectedNode, setSelectedNode] = useState<any>(null)
  const [showTrigger, setShowTrigger] = useState(false)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    if (!name) return
    fetch(`/api/v1/workflows/${name}`)
      .then(r => r.json())
      .then(wf => { setWorkflow(wf); setLoading(false) })
      .catch(() => setLoading(false))
  }, [name])

  const { rfNodes, rfEdges, needsAutoSave } = useGraphLayout({
    nodes: workflow?.nodes || [],
    edges: workflow?.edges || [],
  })

  useEffect(() => {
    if (needsAutoSave && name && rfNodes.length > 0) {
      const positions: Record<string, { x: number; y: number }> = {}
      for (const n of rfNodes) {
        positions[n.id] = n.position
      }
      fetch(`/api/v1/workflows/${name}/layout`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ positions }),
      })
    }
  }, [needsAutoSave, name, rfNodes])

  const onNodeDragStop: NodeDragHandler = useCallback((_event, _node, nodes) => {
    if (!name) return
    const positions: Record<string, { x: number; y: number }> = {}
    for (const n of nodes) {
      positions[n.id] = n.position
    }
    fetch(`/api/v1/workflows/${name}/layout`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ positions }),
    })
  }, [name])

  if (loading || !workflow) {
    return <div style={{ padding: 20, color: 'var(--text-muted)' }}>Loading...</div>
  }

  const graphPanel = (
    <div style={{ height: '100%', background: 'var(--bg-primary)' }}>
      <div style={{ padding: '8px 16px', fontSize: 11, color: 'var(--text-muted)', display: 'flex', gap: 16, alignItems: 'center', borderBottom: '1px solid var(--border-color)' }}>
        <span><strong style={{ color: 'var(--text-secondary)' }}>Author:</strong> {workflow.author || '—'}</span>
        <span><strong style={{ color: 'var(--text-secondary)' }}>Nodes:</strong> {workflow.nodes.length}</span>
        <span style={{ marginLeft: 'auto' }}>
          <button
            onClick={() => setShowTrigger(true)}
            style={{
              background: 'var(--bg-hover)', border: '1px solid var(--border-color)',
              color: 'var(--text-secondary)', padding: '3px 10px', borderRadius: 4,
              cursor: 'pointer', fontSize: 11,
            }}
          >
            Trigger ▶
          </button>
        </span>
      </div>
      <div style={{ height: 'calc(100% - 40px)' }}>
        <ReactFlow
          nodes={rfNodes}
          edges={rfEdges}
          nodeTypes={nodeTypes}
          onNodeClick={(_e, node) => setSelectedNode(workflow.nodes.find(n => n.id === node.id))}
          onNodeDragStop={onNodeDragStop}
          fitView
          proOptions={{ hideAttribution: true }}
          style={{ background: 'var(--bg-primary)' }}
        />
      </div>
    </div>
  )

  const sidePanel = (
    <div style={{ padding: 16, overflow: 'auto', height: '100%' }}>
      {name && <UsageStats workflowName={name} />}

      <div style={{ marginBottom: 20 }}>
        <div style={{ fontSize: 12, color: 'var(--text-secondary)', fontWeight: 600, marginBottom: 8 }}>Input Schema</div>
        {(workflow.inputs || []).length === 0 ? (
          <div style={{ color: 'var(--text-dim)', fontSize: 11 }}>No inputs defined</div>
        ) : (
          (workflow.inputs || []).map(inp => (
            <div key={inp.name} style={{
              background: 'var(--bg-surface)', padding: '6px 8px', borderRadius: 4, fontSize: 11, marginBottom: 4,
            }}>
              <span style={{ color: 'var(--accent)' }}>{inp.name}</span>{' '}
              <span style={{ color: 'var(--text-dim)' }}>{inp.type}</span>{' '}
              <span style={{ color: inp.required ? 'var(--state-failed)' : 'var(--text-muted)', fontSize: 9 }}>
                {inp.required ? 'required' : `optional${inp.default != null ? `, default: ${inp.default}` : ''}`}
              </span>
            </div>
          ))
        )}
      </div>

      <div>
        <div style={{ fontSize: 12, color: 'var(--text-secondary)', fontWeight: 600, marginBottom: 8 }}>Node Details</div>
        {selectedNode ? (
          <pre style={{
            background: 'var(--bg-surface)', padding: 10, borderRadius: 4,
            fontSize: 11, color: 'var(--text-muted)', whiteSpace: 'pre-wrap', margin: 0,
          }}>
            {JSON.stringify(selectedNode, null, 2)}
          </pre>
        ) : (
          <div style={{ color: 'var(--text-dim)', fontSize: 11, fontStyle: 'italic' }}>
            Click a node in the graph to see its configuration
          </div>
        )}
      </div>
    </div>
  )

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      <div style={{ padding: '8px 20px', fontSize: 11, borderBottom: '1px solid var(--border-color)' }}>
        <Link to="/workflows" style={{ color: 'var(--accent)' }}>Workflows</Link>
        {' › '}
        <span style={{ color: 'var(--text-muted)' }}>{name}</span>
      </div>
      <div style={{ flex: 1 }}>
        <ReactFlowProvider>
          <ResizablePanel left={graphPanel} right={sidePanel} defaultRightWidth={280} />
        </ReactFlowProvider>
      </div>
      {showTrigger && (
        <TriggerModal
          workflowName={name!}
          inputs={workflow.inputs || []}
          onClose={() => setShowTrigger(false)}
        />
      )}
    </div>
  )
}
```

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/
git commit -m "feat(dashboard): implement WorkflowReview with drag positions, stats, and trigger"
```

---

### Task 2.3: Settings page

**Files:**
- Modify: `dashboard/src/routes/Settings.tsx`

- [ ] **Step 1: Implement Settings page**

```tsx
import { useState, useEffect, useCallback } from 'react'
import type { SystemConfig, WebSocketEvent } from '../api/types'
import { api } from '../api/client'
import { useSystemWebSocket } from '../hooks/useSystemWebSocket'

export default function Settings() {
  const [config, setConfig] = useState<SystemConfig | null>(null)
  const [globalCap, setGlobalCap] = useState('')
  const [perUserCap, setPerUserCap] = useState('')
  const [saving, setSaving] = useState(false)

  const fetchConfig = useCallback(() => {
    api.system.getConfig().then(cfg => {
      setConfig(cfg)
      setGlobalCap(String(cfg.queue.globalCap))
      setPerUserCap(String(cfg.queue.perUserCap))
    })
  }, [])

  useEffect(() => { fetchConfig() }, [fetchConfig])

  const handleEvent = useCallback((_event: WebSocketEvent) => {
    fetchConfig()
  }, [fetchConfig])

  useSystemWebSocket(handleEvent)

  async function handleSave() {
    setSaving(true)
    try {
      await api.system.updateQueueConfig(Number(globalCap), Number(perUserCap))
      fetchConfig()
    } finally {
      setSaving(false)
    }
  }

  if (!config) return <div style={{ padding: 20, color: 'var(--text-muted)' }}>Loading...</div>

  const sectionStyle = { marginBottom: 24 }
  const labelStyle = { fontSize: 11, color: 'var(--text-muted)', marginBottom: 4, display: 'block' as const }
  const valueStyle = { fontSize: 13, color: 'var(--text-secondary)' }
  const inputStyle = {
    background: 'var(--bg-surface)', border: '1px solid var(--border-color)', borderRadius: 4,
    padding: '6px 8px', color: 'var(--text-secondary)', fontSize: 12, width: 100,
  }

  return (
    <div style={{ padding: 20, maxWidth: 600 }}>
      <h2 style={{ fontSize: 16, fontWeight: 600, color: 'var(--text-secondary)', marginBottom: 24 }}>Settings</h2>

      <div style={sectionStyle}>
        <h3 style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 12 }}>Server</h3>
        <div style={{ marginBottom: 8 }}><span style={labelStyle}>Host</span><span style={valueStyle}>{config.server.host}</span></div>
        <div><span style={labelStyle}>Port</span><span style={valueStyle}>{config.server.port}</span></div>
      </div>

      <div style={sectionStyle}>
        <h3 style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 12 }}>Queue Configuration</h3>
        <div style={{ display: 'flex', gap: 16, marginBottom: 12 }}>
          <div>
            <label style={labelStyle}>Global Cap</label>
            <input type="number" value={globalCap} onChange={e => setGlobalCap(e.target.value)} style={inputStyle} />
          </div>
          <div>
            <label style={labelStyle}>Per-User Cap</label>
            <input type="number" value={perUserCap} onChange={e => setPerUserCap(e.target.value)} style={inputStyle} />
          </div>
        </div>
        <button onClick={handleSave} disabled={saving} style={{
          background: 'var(--accent)', border: 'none', color: 'white', padding: '6px 16px',
          borderRadius: 4, cursor: 'pointer', fontSize: 12,
        }}>{saving ? 'Saving...' : 'Save'}</button>
      </div>

      <div style={sectionStyle}>
        <h3 style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 12 }}>Queue Status</h3>
        <div style={{ display: 'flex', gap: 24 }}>
          <div><span style={labelStyle}>Running</span><span style={{ ...valueStyle, color: 'var(--state-running)' }}>{config.queue.runningCount}</span></div>
          <div><span style={labelStyle}>Queued</span><span style={{ ...valueStyle, color: 'var(--state-queued)' }}>{config.queue.queuedCount}</span></div>
          <div><span style={labelStyle}>Waiting</span><span style={{ ...valueStyle, color: 'var(--state-waiting)' }}>{config.queue.waitingCount}</span></div>
        </div>
      </div>
    </div>
  )
}
```

- [ ] **Step 2: Commit**

```bash
git add dashboard/src/routes/Settings.tsx
git commit -m "feat(dashboard): implement Settings page with queue config editing"
```
