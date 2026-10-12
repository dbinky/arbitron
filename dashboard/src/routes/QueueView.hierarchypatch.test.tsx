import { render, screen, act, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import type { WebSocketEvent, EnrichedInstanceSummary, NodeEventData } from '../api/types'

// Expanded children render from each section's run-hierarchy cache, not from
// QueueView's `active` list, so node events must reach that cache too.

let capturedHandler: ((e: WebSocketEvent) => void) | null = null

vi.mock('../hooks/useSystemWebSocket', () => ({
  useSystemWebSocket: (cb: (e: WebSocketEvent) => void) => {
    capturedHandler = cb
    return { connected: true }
  },
}))
vi.mock('../hooks/useRefetchOnReconnect', () => ({ useRefetchOnReconnect: () => {} }))
vi.mock('../hooks/useDensityPreference', () => ({
  useDensityPreference: () => ({ density: 'balanced', cycle: vi.fn() }),
}))

function summary(o: Partial<EnrichedInstanceSummary>): EnrichedInstanceSummary {
  return {
    id: 'x',
    ownerId: 'u1',
    workflowName: 'wf',
    state: 'running',
    priority: 'normal',
    createdAt: '2024-01-01T00:00:00Z',
    updatedAt: '2024-01-01T00:00:00Z',
    runName: 'run',
    retryCount: 0,
    ...o,
  }
}

// Running parent whose running child is ALSO in the running list: topLevelRows
// hides the child at top level, so the only rendered copy is the hierarchy one.
const runningParent = summary({ id: 'p-run', runName: 'Running Parent', childCount: 1 })
const listedChild = summary({ id: 'c-listed', runName: 'Listed Child', parentInstanceId: 'p-run', nodeType: 'script' })

// Failed parent whose running (fire-and-forget) child is in NO section list —
// it exists only in the hierarchy fetch.
const failedParent = summary({
  id: 'p-failed',
  runName: 'Failed Parent',
  state: 'failed',
  childCount: 1,
  completedAt: '2024-01-01T00:03:00Z',
})
const hiddenChild = summary({ id: 'c-hidden', runName: 'Hidden Child', parentInstanceId: 'p-failed', nodeType: 'script' })

const listMock = vi.fn(async (p: { state?: string; parent?: string }) => {
  if (p.parent === 'p-run') return [listedChild]
  if (p.parent === 'p-failed') return [hiddenChild]
  if (p.state === 'running') return [runningParent, listedChild]
  if (p.state === 'failed') return [failedParent]
  return []
})

vi.mock('../api/client', () => ({
  api: {
    instances: { list: (p: { state?: string; parent?: string }) => listMock(p), getNodeTrace: vi.fn() },
    me: vi.fn(async () => ({ userId: 'u1', authMode: 'local' })),
  },
}))

import QueueView from './QueueView'

const agentStarted: NodeEventData = {
  nodeId: 'impl',
  nodeLabel: 'Implement',
  nodeType: 'agent_call',
  model: 'gpt-6.1-sol',
  effort: 'high',
  backend: 'codex',
  provider: 'litellm',
  gitRef: 'feat/child',
}

function cellOf(container: HTMLElement, id: string, col: string) {
  return container.querySelector(`tr[data-row-id="${id}"] td[data-col="${col}"]`)?.textContent
}

async function expandAndAgentStart(parentName: string, childId: string, childName: string) {
  const view = render(
    <MemoryRouter>
      <QueueView />
    </MemoryRouter>,
  )
  fireEvent.click(await screen.findByRole('button', { name: `Show child runs of ${parentName}` }))
  expect(await screen.findByText(childName)).toBeInTheDocument()
  expect(cellOf(view.container, childId, 'modelBackend')).toBe('script')
  const callsBefore = listMock.mock.calls.length

  act(() => {
    capturedHandler?.({ type: 'node.started', timestamp: '2024-01-01T00:01:00Z', instanceId: childId, data: agentStarted })
  })

  await waitFor(() => expect(cellOf(view.container, childId, 'modelBackend')).toBe('gpt-6.1-sol (high) · codex · litellm'))
  expect(cellOf(view.container, childId, 'git')).toBe('feat/child')
  expect(screen.getByText('Implement')).toBeInTheDocument()
  expect(listMock.mock.calls.length).toBe(callsBefore) // no lifecycle event, no refetch
}

describe('QueueView live patch of expanded children', () => {
  beforeEach(() => {
    capturedHandler = null
    listMock.mockClear()
  })

  it('expanded child goes script → agent_call on node.started with no lifecycle event', async () => {
    await expandAndAgentStart('Running Parent', 'c-listed', 'Listed Child')
  })

  it('patches a child present only in the hierarchy fetch, under a terminal parent', async () => {
    await expandAndAgentStart('Failed Parent', 'c-hidden', 'Hidden Child')
  })
})
