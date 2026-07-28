import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import QueueSection from './QueueSection'
import type { EnrichedInstanceSummary } from '../api/types'

vi.mock('../api/client', () => ({ api: { instances: { getNodeTrace: vi.fn(), list: vi.fn() } } }))

function summary(overrides: Partial<EnrichedInstanceSummary> = {}): EnrichedInstanceSummary {
  return {
    id: 'i1',
    ownerId: 'u1',
    workflowName: 'wf',
    state: 'failed',
    priority: 'normal',
    createdAt: '2024-01-01T00:00:00Z',
    updatedAt: '2024-01-01T00:00:00Z',
    runName: 'run — wf',
    retryCount: 0,
    ...overrides,
  }
}

function renderSection(props: Partial<React.ComponentProps<typeof QueueSection>>) {
  return render(
    <MemoryRouter>
      <QueueSection
        title="Failed"
        instances={[]}
        titleColor="red"
        countBg="red"
        countColor="red"
        timeMode="relative"
        density="balanced"
        authMode="local"
        {...props}
      />
    </MemoryRouter>,
  )
}

describe('QueueSection dismiss-all', () => {
  it('renders [Dismiss All] when onDismissAll is provided', () => {
    const onDismissAll = vi.fn()
    renderSection({ onDismissAll })
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss All' }))
    expect(onDismissAll).toHaveBeenCalled()
  })

  it('does not render [Dismiss All] when onDismissAll is absent (Active section)', () => {
    renderSection({ title: 'Active' })
    expect(screen.queryByRole('button', { name: /dismiss all/i })).toBeNull()
  })
})

describe('QueueSection table rendering', () => {
  it('renders a table with column headers from queueColumns', () => {
    renderSection({ instances: [summary()] })
    expect(screen.getByRole('table')).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: /run/i })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: /status/i })).toBeInTheDocument()
  })

  it('renders one row per instance', () => {
    renderSection({
      instances: [summary({ id: 'a' }), summary({ id: 'b' })],
    })
    const rows = screen.getAllByRole('row')
    // 1 header row + 2 body rows
    expect(rows.length).toBe(3)
  })

  it('empty section shows the empty state', () => {
    renderSection({ instances: [] })
    expect(screen.getByText(/no instances/i)).toBeInTheDocument()
  })

  it('default order for active section is elapsed desc (oldest createdAt first)', () => {
    const older = summary({ id: 'old', state: 'running', createdAt: '2024-01-01T00:00:00Z' })
    const newer = summary({ id: 'new', state: 'running', createdAt: '2024-06-01T00:00:00Z' })
    renderSection({ instances: [newer, older], timeMode: 'elapsed' })
    const bodyRows = screen.getAllByRole('row').slice(1) // skip header
    expect(bodyRows[0].getAttribute('data-row-id')).toBe('old')
    expect(bodyRows[1].getAttribute('data-row-id')).toBe('new')
  })

  it('default order for queued section is queue position ascending', () => {
    const pos3 = summary({ id: 'p3', state: 'queued', queuePosition: 3 })
    const pos1 = summary({ id: 'p1', state: 'queued', queuePosition: 1 })
    renderSection({ instances: [pos3, pos1], timeMode: 'elapsed' })
    const bodyRows = screen.getAllByRole('row').slice(1)
    expect(bodyRows[0].getAttribute('data-row-id')).toBe('p1')
    expect(bodyRows[1].getAttribute('data-row-id')).toBe('p3')
  })

  it('default order for terminal section is updatedAt desc (most-recent first)', () => {
    const older = summary({ id: 'old', state: 'completed', updatedAt: '2024-01-01T00:00:00Z' })
    const newer = summary({ id: 'new', state: 'completed', updatedAt: '2024-06-01T00:00:00Z' })
    renderSection({ title: 'Completed', instances: [older, newer], timeMode: 'relative' })
    const bodyRows = screen.getAllByRole('row').slice(1)
    expect(bodyRows[0].getAttribute('data-row-id')).toBe('new')
    expect(bodyRows[1].getAttribute('data-row-id')).toBe('old')
  })

  it('user re-sort by clicking a sortable header overrides the default order', () => {
    const a = summary({ id: 'a', runName: 'A — wf', state: 'running' })
    const b = summary({ id: 'b', runName: 'B — wf', state: 'running' })
    renderSection({ instances: [a, b], timeMode: 'elapsed' })
    fireEvent.click(screen.getByRole('columnheader', { name: /run/i }))
    const bodyRows = screen.getAllByRole('row').slice(1)
    // Ascending by runName: A before B
    expect(bodyRows[0].getAttribute('data-row-id')).toBe('a')
  })

  it('density change hides dense-excluded columns', () => {
    renderSection({ instances: [summary()], density: 'dense' })
    // 'friendlyStep' is excluded in dense; its header should be absent
    expect(screen.queryByRole('columnheader', { name: /step/i })).toBeNull()
  })
})

describe('QueueSection — submitter sortability by auth mode', () => {
  it('submitter header is NOT sortable in local mode (every row renders "you")', () => {
    renderSection({
      instances: [summary({ id: 'a', submitterName: 'zed' }), summary({ id: 'b', submitterName: 'amy' })],
      authMode: 'local',
    })
    const header = screen.getByRole('columnheader', { name: /submitter/i })
    // Not sortable: clicking does not toggle aria-sort away from "none".
    fireEvent.click(header)
    expect(header.getAttribute('aria-sort')).toBe('none')
  })

  it('submitter header IS sortable in org mode', () => {
    renderSection({
      instances: [summary({ id: 'a', submitterName: 'zed' }), summary({ id: 'b', submitterName: 'amy' })],
      authMode: 'org',
    })
    const header = screen.getByRole('columnheader', { name: /submitter/i })
    fireEvent.click(header)
    expect(header.getAttribute('aria-sort')).toBe('ascending')
    // Ascending by submitterName: amy before zed.
    const bodyRows = screen.getAllByRole('row').slice(1)
    expect(bodyRows[0].getAttribute('data-row-id')).toBe('b')
  })
})

describe('QueueSection — dismissed row rendering', () => {
  it('renders a restore control for a dismissed terminal instance', () => {
    const onRestore = vi.fn()
    renderSection({
      instances: [summary({ id: 'i1', state: 'completed', dismissed: true })],
      onDismiss: vi.fn(),
      onRestore,
    })
    const btn = screen.getByRole('button', { name: /restore/i })
    fireEvent.click(btn)
    expect(onRestore).toHaveBeenCalledWith('i1')
  })

  it('renders a dismiss control for a non-dismissed terminal instance', () => {
    const onDismiss = vi.fn()
    renderSection({
      instances: [summary({ id: 'i2', state: 'failed', dismissed: false })],
      onDismiss,
      onRestore: vi.fn(),
    })
    const btn = screen.getByRole('button', { name: /dismiss/i })
    fireEvent.click(btn)
    expect(onDismiss).toHaveBeenCalledWith('i2')
  })

  it('does not render a dismiss or restore control for active (running) instances', () => {
    renderSection({
      instances: [summary({ state: 'running' })],
      onDismiss: vi.fn(),
      onRestore: vi.fn(),
    })
    expect(screen.queryByRole('button', { name: /dismiss/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /restore/i })).toBeNull()
  })
})

describe('QueueSection — row detail panel', () => {
  it('renders the detail panel when a row is expanded', async () => {
    const { api } = await import('../api/client')
    vi.mocked(api.instances.getNodeTrace).mockResolvedValue({
      id: 'e1', instanceId: 'i1', nodeId: 'fail-node', attempt: 1, status: 'failed',
      error: { error: 'boom' }, startedAt: '2024-01-01T00:00:00Z',
    } as any)
    renderSection({
      instances: [summary({ id: 'i1', state: 'failed', failedNodeId: 'fail-node' })],
    })
    fireEvent.click(screen.getByRole('button', { name: /expand/i }))
    expect(screen.getByTestId('row-detail-panel')).toBeInTheDocument()
  })

  it('collapses the detail panel on second expand click', () => {
    renderSection({ instances: [summary({ id: 'i1' })] })
    fireEvent.click(screen.getByRole('button', { name: /expand/i }))
    expect(screen.getByTestId('row-detail-panel')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /collapse/i }))
    expect(screen.queryByTestId('row-detail-panel')).toBeNull()
  })
})

describe('QueueSection — run hierarchy cache invalidation on refresh', () => {
  it('a refreshSignal bump collapses an expanded row and drops its stale cached children', async () => {
    const { api } = await import('../api/client')
    vi.mocked(api.instances.list).mockResolvedValue([
      summary({ id: 'child1', runName: 'Child Run', parentInstanceId: 'p1' }),
    ])

    const parent = summary({ id: 'p1', runName: 'Parent Run', childCount: 1 })
    const { rerender } = renderSection({ instances: [parent], refreshSignal: 0 })

    fireEvent.click(screen.getByRole('button', { name: /show child runs of parent run/i }))
    expect(await screen.findByText('Child Run')).toBeInTheDocument()

    rerender(
      <MemoryRouter>
        <QueueSection
          title="Failed"
          instances={[parent]}
          titleColor="red"
          countBg="red"
          countColor="red"
          timeMode="relative"
          density="balanced"
          authMode="local"
          refreshSignal={1}
        />
      </MemoryRouter>,
    )

    expect(screen.queryByText('Child Run')).toBeNull()
    expect(screen.getByRole('button', { name: /show child runs of parent run/i })).toBeInTheDocument()
  })

  it('re-rendering with the same refreshSignal does not disturb an expanded row', async () => {
    const { api } = await import('../api/client')
    vi.mocked(api.instances.list).mockResolvedValue([
      summary({ id: 'child1', runName: 'Child Run', parentInstanceId: 'p1' }),
    ])

    const parent = summary({ id: 'p1', runName: 'Parent Run', childCount: 1 })
    const { rerender } = renderSection({ instances: [parent], refreshSignal: 0 })

    fireEvent.click(screen.getByRole('button', { name: /show child runs of parent run/i }))
    expect(await screen.findByText('Child Run')).toBeInTheDocument()

    rerender(
      <MemoryRouter>
        <QueueSection
          title="Failed"
          instances={[parent]}
          titleColor="red"
          countBg="red"
          countColor="red"
          timeMode="relative"
          density="balanced"
          authMode="local"
          refreshSignal={0}
        />
      </MemoryRouter>,
    )

    expect(screen.getByText('Child Run')).toBeInTheDocument()
  })
})