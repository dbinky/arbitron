import { render } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import contract from '../graph/arranged/contract.json'
import type { Geometry, RendererContract } from '../graph/arranged/types'
import type { RenderJob } from './job'
import { RUNTIME_STATES } from './job'
import { fixtureGeometry, fixtureWorkflow } from '../graph/arranged/testFixtures'
import { resolveDisplayGeometry } from '../graph/arranged/validate'
import { createRoot } from 'react-dom/client'
import { buildSurfaceModel, probeFor, RenderSurface, runRenderJob, SURFACE_CONTRACT } from './RenderSurface'

vi.mock('../graph/arranged/ArrangedGraph', () => ({
  // Records the props the surface passes to the shared renderer.
  default: (props: { fixedViewport?: { x: number; y: number; zoom: number }; selectedNodeIds?: ReadonlySet<string>; executionStates?: Record<string, string>; groupLive?: unknown; nodeContexts?: unknown }) => (
    <div
      data-testid="arranged"
      data-viewport={JSON.stringify(props.fixedViewport)}
      data-selected={[...(props.selectedNodeIds ?? [])].join(',')}
      data-states={JSON.stringify(props.executionStates ?? {})}
      data-group-live={JSON.stringify(props.groupLive ?? null)}
      data-node-contexts={JSON.stringify(props.nodeContexts ?? null)}
    />
  ),
}))

const geometry: Geometry = {
  format: 'arbitron.layout/v2',
  contract: contract as RendererContract,
  options: fixtureGeometry().options,
  nodes: [
    { id: 'a', box: { x: 40, y: 40, w: 200, h: 60 }, text: 'Start', typeCaption: 'agent call', textLines: 1, rank: 0 },
    { id: 'b', box: { x: 40, y: 200, w: 200, h: 60 }, text: 'b', typeCaption: 'transform', textLines: 1, rank: 1 },
  ],
  edges: [{
    ordinal: 0, source: 'a', target: 'b', kind: 'forward', points: [{ x: 140, y: 100 }, { x: 140, y: 200 }],
    sourceAttach: { nodeId: 'a', side: 'bottom', point: { x: 140, y: 100 } }, targetAttach: { nodeId: 'b', side: 'top', point: { x: 140, y: 200 } },
  }],
  crossings: [],
  canvas: { x: 0, y: 0, w: 300, h: 300 },
}
const job: RenderJob = { mode: 'render', revision: 'rev-1', attempt: 1, geometry, workflowNodeTypes: { a: 'agent_call', b: 'transform' }, states: [...RUNTIME_STATES] }

describe('buildSurfaceModel', () => {
  it('builds the workflow from geometry text and the job node types', () => {
    const { workflowDef } = buildSurfaceModel(job)
    expect(workflowDef.nodes.map((n) => [n.id, n.type, n.label])).toEqual([['a', 'agent_call', 'Start'], ['b', 'transform', 'b']])
    expect(workflowDef.edges).toEqual([{ source: 'a', target: 'b' }])
  })
  it('passes the candidate geometry through an arranged view', () => {
    const { view } = buildSurfaceModel(job)
    expect(view.geometry).toBe(geometry)
    expect(view.provenance).toBe('arranged')
    expect(view.displayStatus).toBe('reviewing')
    expect(view.contract).toBe(geometry.contract)
  })
  it('produces a model the shared renderer accepts (no fallback)', () => {
    const geo = fixtureGeometry()
    const types = Object.fromEntries(fixtureWorkflow().nodes.map((n) => [n.id, n.type]))
    const { workflowDef, view } = buildSurfaceModel({ mode: 'render', revision: 'rev-1', attempt: 1, geometry: geo, workflowNodeTypes: types, states: [] })
    expect(resolveDisplayGeometry(workflowDef, view)).toEqual({ geometry: geo, issues: [] })
  })
  it('rejects a missing node type instead of inventing one', () => {
    expect(() => buildSurfaceModel({ ...job, workflowNodeTypes: {} })).toThrow(/node type/)
  })
})

describe('probeFor', () => {
  it('applies an execution state to every node', () => {
    expect(probeFor('failed', ['a', 'b'])).toEqual({ executionStates: { a: 'failed', b: 'failed' } })
  })
  it('selects every node for the selected probe', () => {
    expect([...(probeFor('selected', ['a', 'b']).selectedNodeIds ?? [])]).toEqual(['a', 'b'])
  })
})

describe('RenderSurface', () => {
  it('sizes the container to canvas × zoom and pins the viewport to the canvas origin', () => {
    const { workflowDef, view } = buildSurfaceModel(job)
    const { container, getByTestId } = render(<RenderSurface workflowDef={workflowDef} view={view} probe={probeFor('selected', ['a'])} />)
    const root = container.querySelector<HTMLElement>('[data-render-root]')!
    expect(root.style.width).toBe('270px')
    expect(root.style.height).toBe('270px')
    expect(JSON.parse(getByTestId('arranged').dataset.viewport!)).toEqual({ x: 0, y: 0, zoom: 0.9 })
    expect(getByTestId('arranged').dataset.selected).toBe('a')
  })
  it('feeds the probe group runtime and node ownership to the shared renderer', () => {
    const { workflowDef, view } = buildSurfaceModel(job)
    const groupLive = { fan: { instanceId: 'probe:fan', state: 'running' } }
    const nodeContexts = { a: { instanceId: null, independent: false, hiddenDependency: true } }
    const { getByTestId } = render(<RenderSurface workflowDef={workflowDef} view={view} probe={{ groupLive: groupLive as never, nodeContexts }} />)
    expect(JSON.parse(getByTestId('arranged').dataset.groupLive!)).toEqual(groupLive)
    expect(JSON.parse(getByTestId('arranged').dataset.nodeContexts!)).toEqual(nodeContexts)
  })
  it('implements the contract.json renderer contract', () => {
    expect(SURFACE_CONTRACT.version).toBe('arranged-renderer/2')
  })
})

describe('runRenderJob', () => {
  it('refuses to review a geometry the shared renderer would replace with a fallback', async () => {
    const stale = { ...geometry, contract: { ...geometry.contract, version: 'arranged-renderer/0' } }
    const root = createRoot(document.createElement('div'))
    await expect(runRenderJob({ ...job, geometry: stale }, root, document, window)).rejects.toThrow(/rejects this geometry/)
  })
})
