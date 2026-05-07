# Phase 3.2 Task 1: React Flow Setup & Custom Node Components

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Install React Flow and dagre, create custom node components for each Arbitron node type with state-dependent styling, and build the graph layout engine.

**Architecture:** One custom React Flow node component per Arbitron node type. A `useGraphLayout` hook converts workflow definition nodes/edges into React Flow format, applying dagre layout when positions are missing (all-or-nothing rule).

**Tech Stack:** React Flow, dagre, TypeScript

---

### Task 1.1: Install React Flow and dagre

- [ ] **Step 1: Install dependencies**

```bash
cd dashboard && npm install @xyflow/react dagre @types/dagre
```

- [ ] **Step 2: Commit**

```bash
git add dashboard/package.json dashboard/package-lock.json
git commit -m "feat(dashboard): add React Flow and dagre dependencies"
```

---

### Task 1.2: Workflow graph types

**Files:**
- Create: `dashboard/src/graph/types.ts`

- [ ] **Step 1: Define graph types**

```ts
export type NodeExecutionState = 'pending' | 'running' | 'completed' | 'failed' | 'waiting' | 'skipped'

export interface WorkflowNode {
  id: string
  type: string
  label: string
  description: string
  config: Record<string, unknown>
  entrypoint?: boolean
  position?: { x: number; y: number }
}

export interface WorkflowEdge {
  source: string
  target: string
  output?: string
  label?: string
}

export interface WorkflowDefinition {
  name: string
  description: string
  author: string
  nodes: WorkflowNode[]
  edges: WorkflowEdge[]
  inputs?: Array<{
    name: string
    type: string
    description: string
    default?: unknown
    required: boolean
  }>
  defaultErrorBehavior?: Record<string, unknown>
}
```

- [ ] **Step 2: Commit**

```bash
git add dashboard/src/graph/types.ts
git commit -m "feat(dashboard): add workflow graph type definitions"
```

---

### Task 1.3: Custom node components

**Files:**
- Create: `dashboard/src/graph/nodes/ArbitronNode.tsx`
- Create: `dashboard/src/graph/nodes/ArbitronNode.module.css`
- Create: `dashboard/src/graph/nodes/nodeTypes.ts`

- [ ] **Step 1: Create ArbitronNode.module.css**

```css
.node {
  padding: 8px 16px;
  min-width: 140px;
  text-align: center;
  border-radius: 8px;
  border: 2px solid var(--border-color);
  background: var(--bg-surface);
  cursor: pointer;
  transition: border-color 0.2s, box-shadow 0.2s;
}

.nodeSelected {
  border-color: var(--accent) !important;
  box-shadow: 0 0 8px rgba(124, 140, 245, 0.3);
}

.typeLabel {
  font-size: 9px;
  text-transform: uppercase;
  letter-spacing: 1px;
  margin-bottom: 2px;
}

.name {
  font-weight: 500;
  font-size: 12px;
  color: var(--text-secondary);
}

.stateLabel {
  font-size: 10px;
  margin-top: 2px;
}

/* Execution states */
.completed { border-color: var(--state-completed); }
.completed .stateLabel { color: var(--state-completed); }

.running {
  border-color: var(--state-running);
  box-shadow: 0 0 12px rgba(52, 152, 219, 0.3);
}
.running .stateLabel { color: var(--state-running); }

.pending { border-color: var(--border-color); }
.pending .name { color: var(--text-muted); }
.pending .stateLabel { color: var(--text-dim); }

.failed { border-color: var(--state-failed); }
.failed .stateLabel { color: var(--state-failed); }

.waiting { border-color: var(--state-waiting); }
.waiting .stateLabel { color: var(--state-waiting); }
```

- [ ] **Step 2: Create ArbitronNode.tsx**

```tsx
import { memo } from 'react'
import { Handle, Position, type NodeProps } from '@xyflow/react'
import type { NodeExecutionState } from '../types'
import styles from './ArbitronNode.module.css'

const NODE_TYPE_COLORS: Record<string, string> = {
  llm_call: '#7c8cf5',
  script: '#e67e22',
  tool_call: '#1abc9c',
  conditional: '#f39c12',
  transform: '#9b59b6',
  timer_delay: '#00bcd4',
  webhook_wait: '#e91e63',
}

const STATE_ICONS: Record<string, string> = {
  completed: '✓',
  running: '▶',
  failed: '✕',
  waiting: '⏳',
  pending: '',
}

interface ArbitronNodeData {
  nodeType: string
  label: string
  executionState: NodeExecutionState
  [key: string]: unknown
}

function ArbitronNode({ data, selected }: NodeProps<ArbitronNodeData>) {
  const { nodeType, label, executionState } = data as unknown as ArbitronNodeData
  const accentColor = NODE_TYPE_COLORS[nodeType] || '#888'
  const stateClass = styles[executionState] || styles.pending
  const icon = STATE_ICONS[executionState] || ''

  return (
    <>
      <Handle type="target" position={Position.Top} style={{ background: '#555' }} />
      <div className={`${styles.node} ${stateClass} ${selected ? styles.nodeSelected : ''}`}>
        <div className={styles.typeLabel} style={{ color: accentColor }}>{nodeType.replace('_', ' ')}</div>
        <div className={styles.name}>{label}</div>
        {executionState !== 'pending' && (
          <div className={styles.stateLabel}>{icon} {executionState}</div>
        )}
      </div>
      <Handle type="source" position={Position.Bottom} style={{ background: '#555' }} />
    </>
  )
}

export default memo(ArbitronNode)
```

- [ ] **Step 3: Create nodeTypes registry**

Create `dashboard/src/graph/nodes/nodeTypes.ts`:

```ts
import ArbitronNode from './ArbitronNode'

export const nodeTypes = {
  arbitronNode: ArbitronNode,
}
```

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/graph/
git commit -m "feat(dashboard): add custom React Flow node component with state visualization"
```

---

### Task 1.4: Dagre layout hook

**Files:**
- Create: `dashboard/src/graph/useGraphLayout.ts`

- [ ] **Step 1: Create the layout hook**

```ts
import { useMemo } from 'react'
import dagre from 'dagre'
import type { Node, Edge } from '@xyflow/react'
import type { WorkflowNode, WorkflowEdge, NodeExecutionState } from './types'

const NODE_WIDTH = 180
const NODE_HEIGHT = 80

interface LayoutOptions {
  nodes: WorkflowNode[]
  edges: WorkflowEdge[]
  executionStates?: Record<string, NodeExecutionState>
}

interface LayoutResult {
  rfNodes: Node[]
  rfEdges: Edge[]
  needsAutoSave: boolean
}

function allHavePositions(nodes: WorkflowNode[]): boolean {
  return nodes.every(n => n.position && typeof n.position.x === 'number' && typeof n.position.y === 'number')
}

export function useGraphLayout({ nodes, edges, executionStates = {} }: LayoutOptions): LayoutResult {
  return useMemo(() => {
    const useExisting = allHavePositions(nodes)
    let positions: Record<string, { x: number; y: number }>

    if (useExisting) {
      positions = {}
      for (const n of nodes) {
        positions[n.id] = { x: n.position!.x, y: n.position!.y }
      }
    } else {
      const g = new dagre.graphlib.Graph()
      g.setGraph({ rankdir: 'TB', nodesep: 60, ranksep: 80 })
      g.setDefaultEdgeLabel(() => ({}))

      for (const n of nodes) {
        g.setNode(n.id, { width: NODE_WIDTH, height: NODE_HEIGHT })
      }
      for (const e of edges) {
        g.setEdge(e.source, e.target)
      }

      dagre.layout(g)

      positions = {}
      for (const n of nodes) {
        const pos = g.node(n.id)
        positions[n.id] = { x: pos.x - NODE_WIDTH / 2, y: pos.y - NODE_HEIGHT / 2 }
      }
    }

    const rfNodes: Node[] = nodes.map(n => ({
      id: n.id,
      type: 'arbitronNode',
      position: positions[n.id],
      data: {
        nodeType: n.type,
        label: n.label || n.id,
        executionState: executionStates[n.id] || 'pending',
      },
    }))

    const rfEdges: Edge[] = edges.map((e, i) => ({
      id: `edge-${i}`,
      source: e.source,
      target: e.target,
      label: e.output || e.label || undefined,
      style: { stroke: '#444' },
      markerEnd: { type: 'arrowclosed' as const, color: '#444' },
      labelStyle: { fill: '#888', fontSize: 10 },
    }))

    return { rfNodes, rfEdges, needsAutoSave: !useExisting }
  }, [nodes, edges, executionStates])
}
```

- [ ] **Step 2: Commit**

```bash
git add dashboard/src/graph/useGraphLayout.ts
git commit -m "feat(dashboard): add dagre auto-layout hook with all-or-nothing position rule"
```
