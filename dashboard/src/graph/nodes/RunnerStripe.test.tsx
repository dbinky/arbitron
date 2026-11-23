import { render, screen } from '@testing-library/react'
import { createPortal } from 'react-dom'
import { describe, expect, it, vi } from 'vitest'

vi.mock('@xyflow/react', async () => ({
  ...await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react'),
  Handle: () => null,
  EdgeLabelRenderer: ({ children }: { children: React.ReactNode }) => createPortal(children, document.body),
}))
import ArbitronNode from './ArbitronNode'
import ArrangedNode from '../arranged/ArrangedNode'
import RoutedEdge from '../arranged/RoutedEdge'
import { fixtureGeometry } from '../arranged/testFixtures'
import { RENDERER_CONTRACT } from '../arranged/validate'
import { runnerColor, SERVER_SIDE_COLOR, type RunnerMark } from '../runnerMarks'

const mac: RunnerMark = { runnerId: 'mac-a', color: runnerColor('mac-a'), label: 'mac-a' }
const server: RunnerMark = { runnerId: null, color: SERVER_SIDE_COLOR, label: 'server' }
const fallback = (runnerMark?: RunnerMark) => render(<ArbitronNode {...{ data: { label: 'Build', nodeType: 'agent_call', executionState: 'completed', runnerMark }, selected: false } as Parameters<typeof ArbitronNode>[0]} />)
const arranged = (runnerMark?: RunnerMark) => render(<ArrangedNode {...{ data: { nodeBox: fixtureGeometry().nodes[1], nodeType: 'llm_call', executionState: 'completed', contract: RENDERER_CONTRACT, runnerMark }, selected: false } as Parameters<typeof ArrangedNode>[0]} />)

describe('RunnerStripe on production renderers', () => {
  it('paints fallback runner colour with an accessible label', () => {
    fallback(mac)
    expect(screen.getByRole('img', { name: 'Ran on mac-a' })).toHaveAttribute('data-color', runnerColor('mac-a'))
  })
  it('paints arranged runner and server-side grey', () => {
    arranged(mac)
    expect(screen.getByRole('img', { name: 'Ran on mac-a' })).toHaveAttribute('data-color', runnerColor('mac-a'))
    arranged(server)
    expect(screen.getByRole('img', { name: 'Runs on the server' })).toHaveAttribute('data-color', SERVER_SIDE_COLOR)
  })
  it('discloses the workspace move', () => {
    fallback({ ...mac, moved: { from: 'builtin', to: 'mac-a', reason: 'exhausted', bytes: 1, generation: 2, at: '2026-11-20T10:00:00Z' } })
    expect(screen.getByRole('img')).toHaveAttribute('data-moved', 'true')
    expect(screen.getByRole('img')).toHaveAttribute('title', 'Ran on mac-a · workspace moved builtin → mac-a')
  })
  it('renders no stripe for unplaced steps', () => {
    fallback(); arranged()
    expect(screen.queryByRole('img')).toBeNull()
  })
})

const route = fixtureGeometry().edges[1]
function edge(moveBadge?: string) {
  const props = { id: 'e', source: route.source, target: route.target, data: { route, gaps: [], overs: [], contract: RENDERER_CONTRACT, graphInstanceId: 'g', active: false, moveBadge } } as unknown as Parameters<typeof RoutedEdge>[0]
  return render(<svg><RoutedEdge {...props} /></svg>)
}
describe('routed move badge', () => {
  it('renders at the actual route target', () => {
    edge('workspace moved builtin → mac-a')
    const badge = screen.getByText('workspace moved builtin → mac-a')
    expect(badge).toHaveAttribute('data-move-badge', String(route.ordinal))
    const end = route.points[route.points.length - 1]
    expect(badge.style.transform).toContain(`translate(${end.x}px, ${end.y}px)`)
  })
  it('renders nothing without a move', () => {
    edge()
    expect(document.querySelector('[data-move-badge]')).toBeNull()
  })
})
