import { act, render, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import type { ReactFlowProps } from '@xyflow/react'
import type { GroupGraph } from '../../api/types'
import type { WorkflowDefinition } from '../types'
import type { LayoutView } from './types'
import { fixtureView, fixtureWorkflow } from './testFixtures'
import { mixedEdges, mixedGraph, mixedNodes } from '../groupLayout.fixtures'
import { groupHeaderId } from '../groupLayout'
import v2 from './groupGeometry.v2.json'
import type { Geometry } from './types'
import { selectionFromNode } from '../aggregateState'

const port = vi.hoisted(() => ({ leaf: null as ReactFlowProps | null, fontInputs: [] as string[], unavailableText: '' }))
vi.mock('@xyflow/react', async () => ({
  ...await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react'),
  useReactFlow: () => ({ setCenter: vi.fn(), fitView: vi.fn() }),
  Background: () => null,
  Controls: () => null,
  ReactFlow: (props: ReactFlowProps) => {
    port.leaf = props
    return <div data-testid="flow">{props.children as ReactNode}</div>
  },
}))
vi.mock('./fonts', () => ({ ensureBundledTextFonts: async (doc: Document, texts: readonly string[]) => {
  if (doc !== document || !texts.length) throw new Error('missing font inputs')
  port.fontInputs = [...texts]
  const ready = !port.unavailableText || !texts.includes(port.unavailableText)
  return { fontsReady: ready, fontFamilyResolved: ready }
} }))
import ArrangedGraph from './ArrangedGraph'

function mode(container: HTMLElement) {
  return container.querySelector('[data-arranged-canvas]')?.getAttribute('data-layout-mode')
}

/** The reviewed fixture with node c turned into a parallel group (manifest and geometry agree). */
function groupWorkflowAndView(): { def: WorkflowDefinition; view: LayoutView } {
  const def = fixtureWorkflow()
  def.nodes[2] = { ...def.nodes[2], type: 'parallel_group' }
  const view = fixtureView()
  view.geometry!.nodes[2].typeCaption = 'parallel group'
  return { def, view }
}
const cGroup: GroupGraph = {
  groups: { c: { ...mixedGraph.groups.g1, nodeId: 'c', bodies: { a: ['b'] }, nested: [], successTarget: '', failureTarget: '' } },
  owner: {},
  order: ['c'],
}

beforeEach(() => { port.leaf = null; port.fontInputs = []; port.unavailableText = '' })

describe('ArrangedGraph — parallel groups', () => {
  it.each([1, 2, 3, 4])('feeds complete reviewed group geometry and original contexts for fixture %i', async index => {
    const fixture = structuredClone(v2[index]) as unknown as { definition: WorkflowDefinition; geometry: Geometry; groups: GroupGraph }
    const def = fixture.definition as WorkflowDefinition
    def.presentation = { revision: 'groups-revision' } as WorkflowDefinition['presentation']
    const geometry = fixture.geometry as Geometry
    const view = fixtureView({ savedRevision: 'groups-revision', geometry, groups: fixture.groups as GroupGraph })
    const body = Object.keys(fixture.groups!.owner)[0]
    const contexts = body ? { [body]: { instanceId: 'branch-original', independent: true, hiddenDependency: false } } : {}
    const { container } = render(<ArrangedGraph workflowDef={def} view={view} nodeContexts={contexts}
      selectedNodeId={null} onNodeClick={vi.fn()} anchorNodeId={null} />)
    await waitFor(() => expect(mode(container)).toBe('arranged'))
    expect(port.fontInputs).toEqual(expect.arrayContaining(geometry.groupElements!.flatMap(e => e.texts.map(t => t.text))))
    for (const g of Object.values(fixture.groups.groups)) for (const task of g.config.tasks) expect(port.fontInputs).toContain(`winner: ${task.key}`)
    expect(port.fontInputs).toContain('gathered as unfinished')
    const nodes = port.leaf!.nodes!
    expect(nodes.filter(n => n.data.reviewed)).toHaveLength(geometry.groupElements!.length)
    expect(nodes.filter(n => n.data.nodeBox && !n.data.reviewed)).toHaveLength(geometry.nodes.length - fixture.groups!.order.length)
    expect(port.leaf!.edges).toHaveLength(geometry.edges.length + geometry.groupConnectors!.length)
    for (const element of geometry.groupElements!) {
      const node = nodes.find(n => (n.data.reviewed as { element?: { id: string } } | undefined)?.element?.id === element.id)!
      expect(node).toBeDefined()
      expect(node.width).toBe(element.box.w)
      expect(node.height).toBe(element.box.h)
      const parent = nodes.find(n => n.id === node.parentId)
      const parentBox = (parent?.data.reviewed as { element: { box: { x: number; y: number } } } | undefined)?.element.box
      expect(node.position).toEqual({ x: element.box.x - (parentBox?.x ?? 0), y: element.box.y - (parentBox?.y ?? 0) })
    }
    if (body && !fixture.groups!.groups[body]) {
      const original = nodes.find(n => n.data.definitionNodeId === body)!
      expect(selectionFromNode(original, 'root')).toEqual({ kind: 'node', rfId: original.id, nodeId: body, instanceId: 'branch-original' })
    }
  })
  it('fences overlay failures to their geometry epoch and recovers on a new payload', async () => {
    const f = structuredClone(v2[2]) as unknown as { definition: WorkflowDefinition; geometry: Geometry; groups: GroupGraph }
    f.definition.presentation = { revision: 'overlay-epoch' } as WorkflowDefinition['presentation']
    const view = fixtureView({ savedRevision: 'overlay-epoch', geometry: f.geometry, groups: f.groups })
    const mount = (view: LayoutView) => <ArrangedGraph workflowDef={f.definition} view={view} groups={f.groups}
      selectedNodeId={null} onNodeClick={vi.fn()} anchorNodeId={null} />
    const callback = () => (port.leaf!.nodes!.find(n => n.data.reviewed)!.data.reviewed as { onOverlayOverflow: () => void }).onOverlayOverflow
    const { container, rerender } = render(mount(view))
    await waitFor(() => expect(mode(container)).toBe('arranged'))
    const old = callback()
    act(old)
    expect(mode(container)).toBe('fallback')
    const next = structuredClone(view)
    next.geometry!.options.nodeTextSlack++
    rerender(mount(next))
    await waitFor(() => expect(mode(container)).toBe('arranged'))
    act(callback())
    expect(mode(container)).toBe('fallback')
    act(old)
    expect(mode(container)).toBe('fallback')
  })
  it('suppresses reviewed mode when a winner-only font input is unavailable', async () => {
    const f = structuredClone(v2[2]) as unknown as { definition: WorkflowDefinition; geometry: Geometry; groups: GroupGraph }
    f.definition.presentation = { revision: 'winner-font' } as WorkflowDefinition['presentation']
    port.unavailableText = `winner: ${f.groups.groups[f.groups.order[0]].config.tasks[0].key}`
    const issues = vi.fn()
    const { container } = render(<ArrangedGraph workflowDef={f.definition}
      view={fixtureView({ savedRevision: 'winner-font', geometry: f.geometry, groups: f.groups })}
      selectedNodeId={null} onNodeClick={vi.fn()} anchorNodeId={null} onDisplayIssues={issues} />)
    await waitFor(() => expect(mode(container)).toBe('fallback'))
    expect(issues).toHaveBeenLastCalledWith(expect.arrayContaining(['Reviewed fonts could not be loaded']))
  })
  it('preserves explicit null redacted ownership and absent static ownership', async () => {
    const f = structuredClone(v2[1]) as unknown as { definition: WorkflowDefinition; geometry: Geometry; groups: GroupGraph }
    f.definition.presentation = { revision: 'contexts' } as WorkflowDefinition['presentation']
    const body = Object.keys(f.groups.owner).find(id => !f.groups.groups[id])!
    expect(body).toBeTruthy()
    const { rerender } = render(<ArrangedGraph workflowDef={f.definition} view={fixtureView({ savedRevision: 'contexts', geometry: f.geometry, groups: f.groups })}
      nodeContexts={{ [body]: { instanceId: null, independent: false, hiddenDependency: true } }} selectedNodeId={null} onNodeClick={vi.fn()} anchorNodeId={null} />)
    await waitFor(() => expect(port.leaf?.nodes?.some(n => n.data.definitionNodeId === body)).toBe(true))
    const redacted = port.leaf!.nodes!.find(n => n.data.definitionNodeId === body)!
    expect(redacted.data.instanceId).toBeNull()
    expect(redacted.data.hiddenDependency).toBe(true)
    expect(selectionFromNode(redacted, 'root')).toEqual({ kind: 'node', rfId: redacted.id, nodeId: body, instanceId: null })
    rerender(<ArrangedGraph workflowDef={f.definition} view={fixtureView({ savedRevision: 'contexts', geometry: f.geometry, groups: f.groups })}
      selectedNodeId={null} onNodeClick={vi.fn()} anchorNodeId={null} />)
    await waitFor(() => expect(port.leaf!.nodes!.find(n => n.data.definitionNodeId === body)!.data).not.toHaveProperty('instanceId'))
    const staticNode = port.leaf!.nodes!.find(n => n.data.definitionNodeId === body)!
    expect(selectionFromNode(staticNode, 'root').instanceId).toBe('root')
  })
  // Missing ownership must be visible at the actual display sink.
  it('rejects ordinary boxes without complete group ownership', async () => {
    const { def, view } = groupWorkflowAndView()
    const onDisplayIssues = vi.fn()
    const { container } = render(<ArrangedGraph workflowDef={def} view={view} groups={cGroup} selectedNodeId={null}
      onNodeClick={vi.fn()} anchorNodeId={null} onDisplayIssues={onDisplayIssues} />)
    await waitFor(() => expect(mode(container)).toBe('fallback'))
    expect(onDisplayIssues).toHaveBeenLastCalledWith(expect.arrayContaining(['group geometry: missing or mismatched ownership']))
  })

  // No ownership cannot authorize reviewed group mode.
  it('rejects group geometry when no group analysis is supplied', async () => {
    const { def, view } = groupWorkflowAndView()
    const { container } = render(<ArrangedGraph workflowDef={def} view={view} selectedNodeId={null}
      onNodeClick={vi.fn()} anchorNodeId={null} />)
    await waitFor(() => expect(mode(container)).toBe('fallback'))
  })

  // EDGE — a group analysis for a workflow with no parallel_group node is ignored.
  it('ignores a group analysis when the workflow has no parallel group', async () => {
    const { container } = render(<ArrangedGraph workflowDef={fixtureWorkflow()} view={fixtureView()} groups={cGroup}
      selectedNodeId={null} onNodeClick={vi.fn()} anchorNodeId={null} />)
    await waitFor(() => expect(mode(container)).toBe('arranged'))
  })

  // FAILURE (no geometry) — group parts render, a header click selects the group's
  // definition node, and its parts show selected for that definition id.
  it('draws group header/footer/task cards and maps clicks and selection to the definition node', async () => {
    const def: WorkflowDefinition = { name: 'wf-mixed', description: '', author: '', nodes: mixedNodes, edges: mixedEdges }
    const onNodeClick = vi.fn()
    const { rerender } = render(<ArrangedGraph workflowDef={def} view={null} groups={mixedGraph} selectedNodeId={null}
      onNodeClick={onNodeClick} anchorNodeId={null} />)
    await waitFor(() => expect(port.leaf?.nodes?.some(n => n.id === groupHeaderId('g1'))).toBe(true))
    const header = port.leaf!.nodes!.find(n => n.id === groupHeaderId('g1'))!
    act(() => port.leaf!.onNodeClick?.({} as never, header))
    expect(onNodeClick).toHaveBeenCalledWith('g1', expect.objectContaining({ data: expect.objectContaining({ definitionNodeId: 'g1' }) }))
    rerender(<ArrangedGraph workflowDef={def} view={null} groups={mixedGraph} selectedNodeId="g1"
      onNodeClick={onNodeClick} anchorNodeId={null} />)
    const parts = port.leaf!.nodes!.filter(n => (n.data as { definitionNodeId?: string }).definitionNodeId === 'g1')
    expect(parts.length).toBeGreaterThan(1)
    expect(parts.filter(n => n.type !== 'groupContainer').every(n => n.selected)).toBe(true)
    expect(parts.filter(n => n.type === 'groupContainer').every(n => !n.selected)).toBe(true)
  })

  // ERROR — the anchor of a group node is its header (the group never renders itself).
  it('anchors a group node on its header', async () => {
    const def: WorkflowDefinition = { name: 'wf-mixed', description: '', author: '', nodes: mixedNodes, edges: mixedEdges }
    const onAnchorPosition = vi.fn()
    render(<ArrangedGraph workflowDef={def} view={null} groups={mixedGraph} selectedNodeId={null}
      onNodeClick={vi.fn()} anchorNodeId="g1" onAnchorPosition={onAnchorPosition} />)
    await waitFor(() => expect(onAnchorPosition).toHaveBeenLastCalledWith(expect.objectContaining({ x: expect.any(Number), y: expect.any(Number) })))
  })
})
