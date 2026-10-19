import { render, renderHook, screen } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import type { GroupEndpointData, GroupLiveSummary, GroupLiveTask, GroupTaskCardData } from '../groupLayout'

// Handle needs the React Flow store; stub it to a marker so handles are assertable.
vi.mock('@xyflow/react', async () => {
  const actual = await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')
  return {
    ...actual,
    Handle: ({ type, position }: { type: string; position: string }) => (
      <div data-testid="handle" data-handle-type={type} data-position={position} />
    ),
  }
})

import GroupContainerNode, { GroupFooterNode, GroupHeaderNode, GroupTaskCardNode } from './GroupContainerNode'
import { nodeTypes } from './nodeTypes'
import { useGraphLayout } from '../useGraphLayout'
import { mixedNodes, mixedEdges, mixedGraph } from '../groupLayout.fixtures'

// React Flow passes NodeProps; only data/selected are read.
const headerProps = (data: GroupEndpointData) =>
  ({ data, selected: false }) as unknown as React.ComponentProps<typeof GroupHeaderNode>
const footerProps = (data: GroupEndpointData) =>
  ({ data, selected: false }) as unknown as React.ComponentProps<typeof GroupFooterNode>
const cardProps = (data: GroupTaskCardData) =>
  ({ data, selected: false }) as unknown as React.ComponentProps<typeof GroupTaskCardNode>

const endpoint = (over: Partial<GroupEndpointData> = {}): GroupEndpointData => ({
  definitionNodeId: 'g1',
  role: 'footer',
  label: 'Review in parallel',
  form: 'branches',
  completionRule: 'first_success',
  unfinishedBehavior: 'finish_independently',
  gatherMode: 'results_and_files',
  taskKeys: ['a', 'b', 'c'],
  instanceId: 'root-1',
  live: null,
  ...over,
})

const liveTask = (over: Partial<GroupLiveTask> = {}): GroupLiveTask => ({
  key: 't1',
  status: 'running',
  frozenStatus: '',
  childInstanceId: 'child-1',
  blocking: true,
  continuingIndependently: false,
  cancelRequested: false,
  redacted: false,
  winner: false,
  activeNodes: [],
  ...over,
})

const card = (over: Partial<GroupTaskCardData> = {}): GroupTaskCardData => ({
  definitionNodeId: 'w1',
  role: 'task',
  taskKey: 't1',
  index: 0,
  workflow: 'child-a',
  instanceId: 'root-1',
  live: null,
  ...over,
})

function renderIn(ui: React.ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>)
}

describe('nodeTypes registry', () => {
  it('registers the group presentation node types alongside the existing ones', () => {
    expect(Object.keys(nodeTypes).sort()).toEqual(
      ['arbitronNode', 'groupContainer', 'groupFooter', 'groupHeader', 'groupTaskCard', 'loopContainer'],
    )
  })
})

describe('registered group renderers fed by useGraphLayout', () => {
  it('renders live progress and the task child link from the layout output', () => {
    const live: GroupLiveSummary = {
      instanceId: 'branch-b', groupRunId: 'run-w1', state: 'running',
      progress: { total: 3, completed: 1, failed: 0, cancelled: 0, active: 2, notStarted: 0 },
      tasks: { t2: liveTask({ key: 't2', childInstanceId: 'child-t2', activeNodes: ['Build', 'Test'] }) },
    }
    const { result } = renderHook(() => useGraphLayout({
      nodes: mixedNodes, edges: mixedEdges, groups: mixedGraph, groupLive: { w1: live },
    }))
    const Header = nodeTypes.groupHeader
    const Footer = nodeTypes.groupFooter
    const Card = nodeTypes.groupTaskCard
    renderIn(<>{result.current.rfNodes.map((n) => {
      if (n.type === 'groupHeader') return <Header key={n.id} {...headerProps(n.data as GroupEndpointData)} />
      if (n.type === 'groupFooter') return <Footer key={n.id} {...footerProps(n.data as GroupEndpointData)} />
      if (n.type === 'groupTaskCard') return <Card key={n.id} {...cardProps(n.data as GroupTaskCardData)} />
      return null
    })}</>)
    expect(screen.getByTestId('group-header-w1')).toHaveTextContent('3 tasks · workflows')
    expect(screen.getByTestId('group-footer-w1')).toHaveTextContent('1/3 done · 2 active')
    expect(screen.getByRole('link', { name: 'child-b' })).toHaveAttribute('href', '/instances/child-t2')
    expect(screen.getByTestId('group-task-w1-t2')).toHaveTextContent('active: Build, Test')
  })
})

describe('GroupContainerNode', () => {
  it('renders the container label', () => {
    // @ts-expect-error — partial NodeProps is sufficient for this unit test.
    render(<GroupContainerNode data={{ definitionNodeId: 'g1', label: 'Parallel branches · all succeed', depth: 0, form: 'branches' }} />)
    expect(screen.getByText('Parallel branches · all succeed')).toBeInTheDocument()
  })
})

describe('GroupHeaderNode', () => {
  it('shows the fan-out label, task count and form, with one top target and one bottom source handle', () => {
    const { container } = renderIn(<GroupHeaderNode {...headerProps(endpoint({ role: 'header' }))} />)
    expect(screen.getByText('parallel group · fan-out')).toBeInTheDocument()
    expect(screen.getByText('Review in parallel')).toBeInTheDocument()
    expect(screen.getByText('3 tasks · branches')).toBeInTheDocument()
    const handles = [...container.querySelectorAll('[data-testid="handle"]')]
    expect(handles.map((h) => `${h.getAttribute('data-handle-type')}@${h.getAttribute('data-position')}`)).toEqual(['target@top', 'source@bottom'])
  })
})

describe('GroupFooterNode', () => {
  it.each([
    ['launching', 'Launching tasks'],
    ['waiting_for_tasks:zeta,alpha', 'Waiting for tasks: zeta, alpha'],
    ['cancelling:alpha,zeta', 'Cancelling tasks: alpha, zeta'],
    ['preparing_files', 'Preparing files'],
    ['gathering', 'Gathering files'],
    ['resolving_conflicts', 'Resolving conflicts'],
    ['rollback_failed', 'Rollback failed'],
    ['continuation_pending', 'Waiting for parent continuation'],
    ['future_wait:detail', 'future_wait:detail'],
  ])('explains the server waiting reason %s', (waitingReason, text) => {
    const live: GroupLiveSummary = {
      instanceId: 'root-1', groupRunId: 'run-1', state: 'done', progress: null, waitingReason, tasks: {},
    }
    renderIn(<GroupFooterNode {...footerProps(endpoint({ live }))} />)
    expect(screen.getByTestId('group-footer-g1')).toHaveTextContent(text)
  })

  it('statically shows rule, unfinished behaviour and gather mode', () => {
    renderIn(<GroupFooterNode {...footerProps(endpoint())} />)
    expect(screen.getByText('gather · First success')).toBeInTheDocument()
    expect(screen.getByText('finish independently · results + files')).toBeInTheDocument()
  })

  it('shows live state, progress, winner and file/resolution activity', () => {
    const live: GroupLiveSummary = {
      instanceId: 'root-1',
      groupRunId: 'run-1',
      state: 'resolving_conflicts',
      progress: { total: 3, completed: 2, failed: 1, cancelled: 0, active: 0, notStarted: 0 },
      winner: 'b',
      activity: 'Resolving conflicts (attempt 2)',
      tasks: {},
    }
    renderIn(<GroupFooterNode {...footerProps(endpoint({ live }))} />)
    expect(screen.getByText('resolving conflicts')).toBeInTheDocument()
    expect(screen.getByText('3/3 done · 1 failed')).toBeInTheDocument()
    expect(screen.getByText('winner: b')).toBeInTheDocument()
    expect(screen.getByText('Resolving conflicts (attempt 2)')).toBeInTheDocument()
  })

  it('falls back to the first waiting reason when there is no activity', () => {
    const live: GroupLiveSummary = {
      instanceId: 'root-1', groupRunId: 'run-1', state: 'running',
      progress: { total: 2, completed: 0, failed: 0, cancelled: 0, active: 2, notStarted: 0 },
      waitingReason: 'Waiting for tasks a, b', tasks: {},
    }
    renderIn(<GroupFooterNode {...footerProps(endpoint({ live }))} />)
    expect(screen.getByText('Waiting for tasks a, b')).toBeInTheDocument()
    expect(screen.getByText('0/2 done · 2 active')).toBeInTheDocument()
  })
})

describe('GroupTaskCardNode', () => {
  it('statically links the child workflow definition', () => {
    renderIn(<GroupTaskCardNode {...cardProps(card())} />)
    expect(screen.getByText('task 1 · t1')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'child-a' }).getAttribute('href')).toBe('/workflows/child-a')
  })

  it('live: links the child run, shows its status and active nodes', () => {
    renderIn(<GroupTaskCardNode {...cardProps(card({ live: liveTask({ activeNodes: ['Build', 'Test'] }) }))} />)
    expect(screen.getByRole('link', { name: 'child-a' }).getAttribute('href')).toBe('/instances/child-1')
    expect(screen.getByText('running')).toBeInTheDocument()
    expect(screen.getByText('active: Build, Test')).toBeInTheDocument()
  })

  it('distinguishes a non-blocking independent task and the winner', () => {
    const { container } = renderIn(
      <GroupTaskCardNode {...cardProps(card({ live: liveTask({ continuingIndependently: true, blocking: false, winner: true }) }))} />,
    )
    expect(screen.getByText('non-blocking')).toBeInTheDocument()
    expect(screen.getByText('winner')).toBeInTheDocument()
    expect(container.querySelector('[class*="cardIndependent"]')).not.toBeNull()
  })

  it('shows the frozen gathered status next to a different live status', () => {
    renderIn(<GroupTaskCardNode {...cardProps(card({ live: liveTask({ status: 'completed', frozenStatus: 'unfinished' }) }))} />)
    expect(screen.getByText('completed')).toBeInTheDocument()
    expect(screen.getByText('gathered as unfinished')).toBeInTheDocument()
  })

  it('styles queued and cancelled tasks distinctly', () => {
    const queued = renderIn(<GroupTaskCardNode {...cardProps(card({ live: liveTask({ status: 'queued', childInstanceId: null }) }))} />)
    expect(queued.container.querySelector('[class*="statusQueued"]')).not.toBeNull()
    queued.unmount()
    const cancelled = renderIn(<GroupTaskCardNode {...cardProps(card({ live: liveTask({ status: 'cancelled', cancelRequested: true }) }))} />)
    expect(cancelled.container.querySelector('[class*="statusCancelled"]')).not.toBeNull()
    expect(screen.getByText('cancel requested')).toBeInTheDocument()
  })

  it('a redacted task shows a hidden dependency and no link or identifiers', () => {
    renderIn(<GroupTaskCardNode {...cardProps(card({ live: liveTask({ redacted: true, childInstanceId: null }) }))} />)
    expect(screen.getByText('hidden dependency')).toBeInTheDocument()
    expect(screen.queryByRole('link')).toBeNull()
    expect(screen.queryByText('child-a')).toBeNull()
  })

  it('shows a launch error', () => {
    renderIn(<GroupTaskCardNode {...cardProps(card({ live: liveTask({ status: 'failed', childInstanceId: null, launchError: 'child workflow not found' }) }))} />)
    expect(screen.getByText('child workflow not found')).toBeInTheDocument()
  })
})
