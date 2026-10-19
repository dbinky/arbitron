# Parallel Workflow Gathering — Phase 4 Task 9: WorkflowReview static group layout, task cards and child links

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Make the static workflow review page show fan-out paths, gathers, nested groups and child-workflow relationships: the same group boxes as the live view, ordered task cards linking each child workflow, a group details section, and layout persistence that maps a group header back to its definition node and never writes presentation ids. No editor.
**Architecture:** `WorkflowReview` fetches `api.workflows.groupLayout(name)` (P4-T6) next to `api.workflows.get(name)` and hands the `GroupGraph` to `useGraphLayout({ groups })` (P4-T7), with no live overlays, so cards render statically (links to `/workflows/<child>`). Node clicks resolve to the definition node via `definitionNodeIdOf`; saved positions go through `toDefinitionPositions`. A failed layout fetch degrades to the plain graph with a notice.
**Tech Stack:** React 19, React Router 7, `@xyflow/react` 12, TypeScript 6, Vitest 4 + @testing-library/react 16, CSS modules.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` ("The workflow UI clearly displays fan-out paths, gathers, nested groups, and child workflow relationships"; non-goal "A visual workflow editor"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-4.md` §4 ("Workflow-form boxes have ordered task cards and links to child runs. `WorkflowReview` uses the same layout with static task/child relationships") and Testing edge-case "static workflow view".

**Interfaces**
- Consumes (existing code):
  - `dashboard/src/routes/WorkflowReview.tsx:24-33` `collectPositions`, `:35-115` `ReviewGraph` (layout, `setNodes(rfNodes)` sync at `:55-58`, auto-save at `:60-68`, `onNodeDragStop` at `:70-86`, selection at `:88-92`, `onNodeClick` at `:101`), `:117-217` `ReviewSidePanel`, `:219-340` `WorkflowReview` (load effect `:229-249`, breadcrumb `:280-309`, panels `:312-327`).
  - `dashboard/src/api/client.ts` `api.workflows.get(name)`, `api.workflows.saveLayout(name, positions)` (existing; `PUT /api/v1/workflows/{name}/layout`).
- Consumes (earlier tasks):
  - P4-T6: `api.workflows.groupLayout(name: string): Promise<GroupGraph>`; types `GroupGraph`, `GroupScope`.
  - P4-T7: `useGraphLayout({ nodes, edges, groups })`; `definitionNodeIdOf(n)`, `toDefinitionPositions(rfNodes)` (`dashboard/src/graph/groupLayout.ts`); `ruleText`, `unfinishedText`, `gatherText` (`dashboard/src/graph/groupText.ts`); node types `groupContainer`/`groupHeader`/`groupFooter`/`groupTaskCard` registered in `nodeTypes` (static cards link `/workflows/<workflow>` when `live` is null); fixtures `mixedNodes/mixedEdges/mixedGraph` (`dashboard/src/graph/groupLayout.fixtures.ts`).
- Produces: no new exported symbols. Behaviour later tasks rely on: the review page never persists `group:*` ids; a group header drag persists the group node's position.

**Wiring & data provenance**
- Composition root (live UI): `WorkflowReview` is mounted at `<Route path="/workflows/:name">` in `dashboard/src/App.tsx:22`; this task makes it fetch and pass the group layout. Embedded by `make ui` (P4-T10).
- Producers:
  - `groupLayout` ← `GET /api/v1/workflows/{name}/group-layout` (`api.workflows.groupLayout`, P4-T6) ← `adapter/http` handler routed under `r.Route("/workflows", …)` in `adapter/http/server.go:270` (P4-T3) ← `domain/workflow/group_ownership.go:AnalyzeGroups` (P1-T2) of the caller-visible definition (`Service.Load`, visibility enforced by the service).
  - `workflowDef` ← existing `GET /api/v1/workflows/{name}` (`adapter/http/workflow_handler.go:WorkflowHandler.Get`); task edges carry `output: "task"` written by `domain/workflow/group_ops.go:Service.UpsertGroup` (P1-T6).
  - Persisted positions → existing `PUT /api/v1/workflows/{name}/layout` (`WorkflowHandler.SaveLayout`, `adapter/http/workflow_handler.go:411`).
- Provenance probe: `fetches the workflow's group layout and renders nested boxes…` fails if the page does not fetch the layout or does not pass it to `useGraphLayout` (verified against the pre-task page: 6 of 8 tests fail, first with `Unable to find an element by: [data-testid="node-group:g1:header"]`); `auto-saves dagre positions mapped back to definition ids` fails if positions are collected from rendered ids (verified by restoring the old `collectPositions`).

### Task 9: WorkflowReview static group layout
**Files:**
- Modify: `dashboard/src/routes/WorkflowReview.tsx:1-340` (whole file)
- Modify: `dashboard/src/routes/WorkflowReview.module.css` (append after `:200`)
- Test: `dashboard/src/routes/WorkflowReview.groups.test.tsx` (new)

- [x] Step 1: Write the failing tests `dashboard/src/routes/WorkflowReview.groups.test.tsx`. React Flow is stubbed to render the REAL node components from the `nodeTypes` prop (so static cards and their links are what the user sees) plus a drag trigger per node; the API fake is keyed by workflow name:

```tsx
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import type { ComponentType } from 'react'
import type { GroupGraph } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'
import { mixedEdges, mixedGraph, mixedNodes } from '../graph/groupLayout.fixtures'

// ---------------------------------------------------------------------------
// React Flow stub that renders the REAL node components from `nodeTypes`, so
// the static task cards (and their child-workflow links) are what the user
// would see. Each node is wrapped in a clickable/draggable div.
// ---------------------------------------------------------------------------
type RFNode = { id: string; type?: string; selected?: boolean; position: { x: number; y: number }; data: Record<string, unknown> }
vi.mock('@xyflow/react', () => ({
  ReactFlow: (props: {
    nodes: RFNode[]
    nodeTypes: Record<string, ComponentType<{ data: unknown; selected?: boolean }>>
    onNodeClick?: (e: unknown, n: RFNode) => void
    onNodeDragStop?: (e: unknown, n: RFNode, nodes: RFNode[]) => void
    children?: React.ReactNode
  }) => (
    <div>
      {props.nodes.map((n) => {
        const C = props.nodeTypes[n.type ?? 'arbitronNode']
        return (
          <div key={n.id} data-testid={`node-${n.id}`} data-type={n.type} data-selected={n.selected ? 'true' : 'false'} onClick={() => props.onNodeClick?.({}, n)}>
            <C data={n.data} selected={n.selected} />
            <button type="button" data-testid={`drag-${n.id}`} onClick={(e) => { e.stopPropagation(); props.onNodeDragStop?.({}, { ...n, position: { x: 999, y: 111 } }, []) }} />
          </div>
        )
      })}
      {props.children}
    </div>
  ),
  ReactFlowProvider: ({ children }: { children: React.ReactNode }) => children,
  Background: () => null,
  Controls: () => null,
  Handle: () => null,
  Position: { Top: 'top', Bottom: 'bottom', Left: 'left', Right: 'right' },
  MarkerType: { ArrowClosed: 'arrowclosed' },
}))

// API fake keyed by workflow name: the layout of ANOTHER workflow is a 404.
const apiMock = vi.hoisted(() => ({
  workflows: { get: vi.fn(), groupLayout: vi.fn(), saveLayout: vi.fn(), getStats: vi.fn(), trigger: vi.fn() },
  schedules: { list: vi.fn() },
}))
vi.mock('../api/client', () => ({ api: apiMock }))

import WorkflowReview from './WorkflowReview'

const mixedDef: WorkflowDefinition = { name: 'wf-mixed', description: '', author: 'ann', nodes: mixedNodes, edges: mixedEdges }
const plainDef: WorkflowDefinition = {
  name: 'wf-plain', description: '', author: 'ann',
  nodes: [{ id: 'a', type: 'llm_call', label: 'A', description: '', config: {} }, { id: 'b', type: 'llm_call', label: 'B', description: '', config: {} }],
  edges: [{ source: 'a', target: 'b' }],
}
const EMPTY: GroupGraph = { groups: {}, owner: {}, order: [] }

let defs: Record<string, WorkflowDefinition>
let layouts: Record<string, GroupGraph | Error>

beforeEach(() => {
  defs = { 'wf-mixed': mixedDef, 'wf-plain': plainDef }
  layouts = { 'wf-mixed': mixedGraph, 'wf-plain': EMPTY }
  apiMock.workflows.get.mockImplementation((name: string) => (defs[name] ? Promise.resolve(defs[name]) : Promise.reject(new Error('API 404: workflow not found'))))
  apiMock.workflows.groupLayout.mockImplementation((name: string) => {
    const l = layouts[name]
    if (!l) return Promise.reject(new Error('API 404: workflow not found'))
    return l instanceof Error ? Promise.reject(l) : Promise.resolve(l)
  })
  apiMock.workflows.saveLayout.mockResolvedValue(undefined)
  apiMock.workflows.getStats.mockResolvedValue({ totalRuns: 0, successCount: 0, failureCount: 0, avgDurationSeconds: 0 })
  apiMock.schedules.list.mockResolvedValue([])
})
afterEach(() => vi.clearAllMocks())

function renderReview(name: string) {
  return render(
    <MemoryRouter initialEntries={[`/workflows/${name}`]}>
      <Routes>
        <Route path="/workflows/:name" element={<WorkflowReview />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('WorkflowReview — static group layout (happy path)', () => {
  // Provenance: without the fetched group layout the group nodes render as
  // plain step nodes and none of these presentation nodes exist.
  it('fetches the workflow\'s group layout and renders nested boxes with header, footer and task cards', async () => {
    renderReview('wf-mixed')
    expect(await screen.findByTestId('node-group:g1:header')).toBeInTheDocument()
    expect(apiMock.workflows.groupLayout).toHaveBeenCalledWith('wf-mixed')
    expect(screen.getByTestId('node-group:g1:box').getAttribute('data-type')).toBe('groupContainer')
    expect(screen.getByTestId('node-group:w1:box').getAttribute('data-type')).toBe('groupContainer')
    expect(screen.getByTestId('node-group:w1:footer').getAttribute('data-type')).toBe('groupFooter')
    expect(screen.queryByTestId('node-g1')).toBeNull()
    expect(screen.queryByTestId('node-w1')).toBeNull()
    expect(screen.getAllByText('Parallel branches · all succeed').length).toBe(1)
    expect(screen.getAllByText('Parallel workflows · first success').length).toBe(1)
  })

  it('renders the workflows-form tasks as ordered cards linking their child workflows', async () => {
    renderReview('wf-mixed')
    const cards = await screen.findAllByTestId(/^node-group:w1:task:/)
    expect(cards.map((c) => c.getAttribute('data-testid'))).toEqual([
      'node-group:w1:task:t1',
      'node-group:w1:task:t2',
      'node-group:w1:task:t3',
    ])
    expect(within(cards[0]).getByRole('link', { name: 'child-a' }).getAttribute('href')).toBe('/workflows/child-a')
    expect(within(cards[1]).getByRole('link', { name: 'child-b' }).getAttribute('href')).toBe('/workflows/child-b')
    expect(within(cards[2]).getByRole('link', { name: 'child-a' }).getAttribute('href')).toBe('/workflows/child-a')
    // Static: no live status, no run links.
    expect(within(cards[0]).queryByText('running')).toBeNull()
    expect(document.querySelector('a[href^="/instances/"]')).toBeNull()
  })
})

describe('WorkflowReview — selection and group details (success)', () => {
  it('header and footer both select the group node and show its ordered tasks with child links', async () => {
    renderReview('wf-mixed')
    fireEvent.click(await screen.findByTestId('node-group:w1:footer'))
    const details = await screen.findByTestId('review-group-details')
    expect(within(details).getByText('First success')).toBeInTheDocument()
    expect(within(details).getByText('cancel unfinished')).toBeInTheDocument()
    const items = within(screen.getByTestId('review-group-tasks')).getAllByRole('listitem')
    expect(items.map((li) => li.textContent)).toEqual(['t1child-a', 't2child-b', 't3child-a'])
    expect(screen.getByTestId('node-group:w1:header').getAttribute('data-selected')).toBe('true')
    expect(screen.getByTestId('node-group:w1:footer').getAttribute('data-selected')).toBe('true')
    expect(screen.getByTestId('node-group:w1:box').getAttribute('data-selected')).toBe('false')

    fireEvent.click(screen.getByTestId('node-group:g1:header'))
    const branches = await screen.findByTestId('review-group-tasks')
    expect(within(branches).getByText('entry a1')).toBeInTheDocument()
    expect(within(branches).getByText('entry w1')).toBeInTheDocument()
  })

  it('auto-saves dagre positions mapped back to definition ids (header saved as the group node)', async () => {
    renderReview('wf-mixed')
    await waitFor(() => expect(apiMock.workflows.saveLayout).toHaveBeenCalledTimes(1))
    const [name, positions] = apiMock.workflows.saveLayout.mock.calls[0] as [string, Record<string, { x: number; y: number }>]
    expect(name).toBe('wf-mixed')
    expect(Object.keys(positions).sort()).toEqual(['a1', 'b2', 'b3', 'done', 'g1', 'oops', 'start', 'w1'])
    expect(Object.keys(positions).some((k) => k.startsWith('group:'))).toBe(false)
  })

  it('dragging a group header persists the GROUP NODE position and no presentation ids', async () => {
    renderReview('wf-mixed')
    await waitFor(() => expect(apiMock.workflows.saveLayout).toHaveBeenCalledTimes(1))
    fireEvent.click(screen.getByTestId('drag-group:w1:header'))
    await waitFor(() => expect(apiMock.workflows.saveLayout).toHaveBeenCalledTimes(2))
    const positions = apiMock.workflows.saveLayout.mock.calls[1][1] as Record<string, { x: number; y: number }>
    expect(positions.w1).toEqual({ x: 999, y: 111 })
    expect(Object.keys(positions).some((k) => k.startsWith('group:'))).toBe(false)
  })
})

describe('WorkflowReview — failure and edge cases', () => {
  it('still renders the workflow, with a notice, when the group layout cannot be loaded', async () => {
    layouts['wf-mixed'] = new Error('API 500: analysis failed')
    renderReview('wf-mixed')
    expect(await screen.findByTestId('node-g1')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('Group layout unavailable: API 500: analysis failed')
    expect(screen.queryByTestId('node-group:g1:header')).toBeNull()
  })

  it('a workflow without groups renders exactly as before and shows no notice', async () => {
    renderReview('wf-plain')
    expect(await screen.findByTestId('node-a')).toBeInTheDocument()
    expect(screen.getByTestId('node-b')).toBeInTheDocument()
    expect(screen.queryByRole('status')).toBeNull()
    expect(document.querySelector('[data-type^="group"]')).toBeNull()
  })

  it('does not auto-save when every node already has a stored position', async () => {
    defs['wf-mixed'] = { ...mixedDef, nodes: mixedNodes.map((n, i) => ({ ...n, position: { x: i * 10, y: i * 100 } })) }
    renderReview('wf-mixed')
    await screen.findByTestId('node-group:w1:header')
    expect(apiMock.workflows.saveLayout).not.toHaveBeenCalled()
  })
})
```

- [x] Step 2: Run it, confirm it fails.

Confirmed on the pre-implementation page: 6 failed, 2 passed. Missing rendered group headers/cards, selection/details, mapped persistence and fallback notice fail at their visible/persistence sinks.

```bash
cd dashboard && npx vitest run src/routes/WorkflowReview.groups.test.tsx
```
Expected:
```
 FAIL  src/routes/WorkflowReview.groups.test.tsx > WorkflowReview — static group layout (happy path) > fetches the workflow's group layout and renders nested boxes with header, footer and task cards
TestingLibraryElementError: Unable to find an element by: [data-testid="node-group:g1:header"]
 FAIL  src/routes/WorkflowReview.groups.test.tsx > WorkflowReview — static group layout (happy path) > renders the workflows-form tasks as ordered cards linking their child workflows
TestingLibraryElementError: Unable to find an element by: [data-testid="/^node-group:w1:task:/"]
...
 FAIL  src/routes/WorkflowReview.groups.test.tsx > WorkflowReview — failure and edge cases > still renders the workflow, with a notice, when the group layout cannot be loaded
TestingLibraryElementError: Unable to find an accessible element with the role "status"
      Tests  6 failed | 2 passed (8)
```
(The two passing tests — the plain-workflow regression and "does not auto-save with stored positions" — are regression guards.)

- [x] Step 3: Implement. Replace the whole of `dashboard/src/routes/WorkflowReview.tsx` (lines 1-340) with the version below. Relative to the current file: `collectPositions` (lines 24-33) now delegates to `toDefinitionPositions` (header → group node; footer/cards/boxes dropped; every other node, including loop containers, unchanged); `ReviewGraph` takes `groupLayout` and passes it as `groups`; selection highlights every rendered part of the selected definition node except its box and clicks resolve through `definitionNodeIdOf`; a `GroupDetails` section lists the selected group's rule/behaviour/gather mode/ports and ORDERED tasks with child-workflow links; the load effect (lines 229-249) fetches the definition and the group layout in parallel, the latter never failing the page; the breadcrumb shows a `role="status"` notice when a group-bearing workflow's layout could not be loaded:

```tsx
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

/**
 * Collect the DEFINITION positions to persist from React Flow nodes. A group's
 * header saves as its group node; footers, task cards and group boxes are
 * derived presentation and are never written into the manifest's layout.
 */
function collectPositions(
  nodes: Node[],
): Record<string, { x: number; y: number }> {
  return toDefinitionPositions(nodes)
}

/** Whether a rendered node is (part of) the selected definition node. */
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
  /** Phase 1 ownership analysis; null when the workflow has no groups or it failed to load. */
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

  // Mark selected node (a group's header, footer and task cards all stand for
  // the group node).
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

/** Static view of one parallel group: rule, behaviours and its ordered tasks. */
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
        {config.tasks.map((t) => (
          <li key={t.key} className={styles.groupTaskItem}>
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
    async function load() {
      try {
        // The group layout is the Phase 1 ownership analysis of the same
        // definition; it never blocks the review. If it cannot be loaded the
        // graph still renders (groups as plain nodes) with a notice.
        const [data, layout] = await Promise.all([
          api.workflows.get(name!),
          api.workflows.groupLayout(name!).then(
            (g) => ({ graph: g, error: null as string | null }),
            (err: unknown) => ({ graph: null, error: err instanceof Error ? err.message : 'Failed to load group layout' }),
          ),
        ])
        if (!cancelled) {
          setWorkflowDef(data)
          setGroupLayout(layout.graph)
          setGroupLayoutError(layout.error)
        }
      } catch (err) {
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
```

Append to `dashboard/src/routes/WorkflowReview.module.css` (after line 200):

```css
/* Parallel group details (P4-T9) */
.groupTaskList {
  margin: 8px 0 0 18px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.groupTaskItem {
  display: flex;
  gap: 8px;
  align-items: baseline;
  font-size: 12px;
}

.groupTaskLink {
  font-size: 12px;
  color: var(--accent);
}
```

- [x] Step 4: Run it, confirm it passes; typecheck and run the full suite.

Verification: focused tests 8/8 green; `npx tsc --noEmit` green; full Vitest suite 61 files / 681 tests green. `make ui` rebuilt the embedded dashboard successfully. It reports existing dependency audit advisories (9: 1 low, 2 moderate, 6 high) and the bundle-size warning; no dependency or lockfile changes were made.

Self-review, in order:
1. Spec compliance: checked this task against design §4 and the existing review page. Only static layout loading, definition-id selection/persistence, ordered task details/child links and failure notice were added; no editor or live overlays. The mounted `/workflows/:name` page consumes the real client method, backed by the registered caller-scoped `WorkflowHandler.GroupLayout` and `AnalyzeGroups` producer.
2. Anti-green-theater: the name-keyed API fake feeds the real layout hook and real node components; assertions reach rendered nested boxes, ordered links/details and save-layout payloads. Strengthened details assertions for form, gather mode, both continuation targets and actual child link destinations. Temporarily replacing the production `groups` input with null caused 5 failures; replacing definition-position mapping with raw rendered ids caused 2 failures. Both mutations were restored.
3. Code quality: reviewed neighboring group-node/text/layout components and existing review conventions. Reused the shared helpers and kept styling local; no unrelated cleanup. After review, reran focused tests, TypeScript and all 681 dashboard tests, all green; `git diff --check` clean.

```bash
cd dashboard && npx vitest run src/routes/WorkflowReview.groups.test.tsx && npx tsc --noEmit && npx vitest run
```
Expected:
```
 Test Files  1 passed (1)
      Tests  8 passed (8)
...
 Test Files  … passed (no failures)
```

- [x] Step 5: Commit.

Includes this checked plan and regenerated embedded assets alongside the task's source and tests.

```bash
git add dashboard/src/routes/WorkflowReview.tsx dashboard/src/routes/WorkflowReview.module.css dashboard/src/routes/WorkflowReview.groups.test.tsx
git commit -m "feat(dashboard): static group layout, ordered task cards and child links in workflow review" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
