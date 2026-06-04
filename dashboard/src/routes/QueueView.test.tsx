import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'

vi.mock('../hooks/useSystemWebSocket', () => ({
  useSystemWebSocket: () => ({ connected: true }),
}))
vi.mock('../hooks/useRefetchOnReconnect', () => ({ useRefetchOnReconnect: () => {} }))

const { list, dismiss, restore, dismissAll } = vi.hoisted(() => ({
  list: vi.fn(),
  dismiss: vi.fn(),
  restore: vi.fn(),
  dismissAll: vi.fn(),
}))
vi.mock('../api/client', () => ({
  api: {
    instances: { list, dismiss, restore, dismissAll },
  },
}))

import QueueView from './QueueView'

describe('QueueView fetchAll window', () => {
  beforeEach(() => {
    list.mockReset()
    list.mockResolvedValue([])
  })

  // Robustness: fetchAll must pass the same 5-day since window to the terminal list
  // calls that handleDismissAll uses. If since is dropped from list but not dismissAll,
  // the user would see a longer history than what "Dismiss All" would clear.
  it('passes a since date to the failed and completed list calls on mount', async () => {
    render(
      <MemoryRouter>
        <QueueView />
      </MemoryRouter>,
    )
    await screen.findByRole('button', { name: /show dismissed/i })

    // Both terminal list calls must include a since parameter.
    const terminalCalls = list.mock.calls.filter(
      (c) => (c[0] as { state?: string })?.state === 'failed' || (c[0] as { state?: string })?.state === 'completed',
    )
    expect(terminalCalls.length).toBeGreaterThanOrEqual(2)
    for (const call of terminalCalls) {
      const params = call[0] as { since?: string }
      expect(params.since).toMatch(/^\d{4}-\d{2}-\d{2}T/)
    }
  })

  it('does NOT pass a since date to the active (running/waiting/queued/paused) list calls', async () => {
    render(
      <MemoryRouter>
        <QueueView />
      </MemoryRouter>,
    )
    await screen.findByRole('button', { name: /show dismissed/i })

    const activeCalls = list.mock.calls.filter(
      (c) => ['running', 'waiting', 'queued', 'paused'].includes(
        (c[0] as { state?: string })?.state ?? '',
      ),
    )
    expect(activeCalls.length).toBeGreaterThanOrEqual(1)
    for (const call of activeCalls) {
      const params = call[0] as { since?: string }
      expect(params.since).toBeUndefined()
    }
  })
})

describe('QueueView show/hide dismissed toggle', () => {
  beforeEach(() => {
    list.mockReset()
    list.mockResolvedValue([])
  })

  it('starts as [Show Dismissed] and flips to [Hide Dismissed]; passes includeDismissed when shown', async () => {
    render(
      <MemoryRouter>
        <QueueView />
      </MemoryRouter>,
    )
    const toggle = await screen.findByRole('button', { name: /show dismissed/i })
    fireEvent.click(toggle)
    await screen.findByRole('button', { name: /hide dismissed/i })

    // After enabling, the failed/completed fetches request includeDismissed.
    await waitFor(() => {
      const calledWithInclude = list.mock.calls.some(
        (c) => (c[0] as { includeDismissed?: boolean } | undefined)?.includeDismissed === true,
      )
      expect(calledWithInclude).toBe(true)
    })
  })

  // Slice: toggling back to "Hide Dismissed" must stop sending includeDismissed so
  // the backend omits dismissed items. A regression leaving includeDismissed=true
  // permanently would make dismissed items always visible after one toggle.
  it('omits includeDismissed after toggling back to Hide Dismissed', async () => {
    render(
      <MemoryRouter>
        <QueueView />
      </MemoryRouter>,
    )
    // Show dismissed.
    const showBtn = await screen.findByRole('button', { name: /show dismissed/i })
    fireEvent.click(showBtn)
    await screen.findByRole('button', { name: /hide dismissed/i })

    // Hide dismissed again.
    list.mockClear()
    const hideBtn = screen.getByRole('button', { name: /hide dismissed/i })
    fireEvent.click(hideBtn)
    await screen.findByRole('button', { name: /show dismissed/i })

    // The re-fetch after hiding must NOT include includeDismissed=true.
    await waitFor(() => {
      const terminalCalls = list.mock.calls.filter(
        (c) => ['failed', 'completed'].includes(
          (c[0] as { state?: string })?.state ?? '',
        ),
      )
      expect(terminalCalls.length).toBeGreaterThanOrEqual(1)
      const anyWithInclude = terminalCalls.some(
        (c) => (c[0] as { includeDismissed?: boolean })?.includeDismissed === true,
      )
      expect(anyWithInclude).toBe(false)
    })
  })
})

describe('QueueView show-dismissed end-to-end render', () => {
  beforeEach(() => {
    list.mockReset()
    restore.mockReset()
    restore.mockResolvedValue({ status: 'ok' })
  })

  // Slice: enabling Show Dismissed causes dismissed items to appear with Restore,
  // not Dismiss. This tests the full toggle→refetch→render chain at the QueueView level.
  // QueueSection covers the dimming at the component level; this covers the orchestration.
  it('shows a Restore button (not Dismiss) for dismissed items after Show Dismissed is enabled', async () => {
    const dismissed = {
      id: 'd1',
      workflowName: 'wf',
      state: 'failed' as const,
      priority: 'normal' as const,
      dismissed: true,
      createdAt: '2024-01-01T00:00:00Z',
      updatedAt: '2024-01-01T00:00:00Z',
    }
    // Initial load: no dismissed items visible (includeDismissed=false).
    list.mockImplementation((params: { state?: string; includeDismissed?: boolean } = {}) => {
      if (params.state === 'failed' && params.includeDismissed) return Promise.resolve([dismissed])
      return Promise.resolve([])
    })

    render(
      <MemoryRouter>
        <QueueView />
      </MemoryRouter>,
    )
    await screen.findByRole('button', { name: /show dismissed/i })
    // No Restore button yet — dismissed item not in view.
    expect(screen.queryByRole('button', { name: 'Restore' })).toBeNull()

    // Enable Show Dismissed.
    fireEvent.click(screen.getByRole('button', { name: /show dismissed/i }))

    // After refetch includes dismissed item, Restore button appears.
    await screen.findByRole('button', { name: 'Restore' })
    expect(screen.queryByRole('button', { name: 'Dismiss' })).toBeNull()
  })
})

describe('QueueView loading and error states', () => {
  beforeEach(() => {
    list.mockReset()
  })

  // Error: when fetchAll fails on initial load, the component renders an error
  // message and a Retry button; clicking Retry re-triggers fetchAll.
  it('shows error state when fetchAll rejects, and Retry re-runs it', async () => {
    list.mockRejectedValue(new Error('network error'))

    render(
      <MemoryRouter>
        <QueueView />
      </MemoryRouter>,
    )

    // Error UI appears with a Retry button.
    const retryBtn = await screen.findByRole('button', { name: /retry/i })
    expect(screen.getByText(/failed to load instances/i)).toBeInTheDocument()

    // After Retry, a successful fetch renders the normal queue view.
    list.mockReset()
    list.mockResolvedValue([])
    fireEvent.click(retryBtn)
    await screen.findByRole('button', { name: /show dismissed/i })
  })
})

describe('QueueView dismiss handlers', () => {
  beforeEach(() => {
    list.mockReset()
    list.mockResolvedValue([])
    dismiss.mockReset()
    dismiss.mockResolvedValue({ status: 'ok' })
    restore.mockReset()
    restore.mockResolvedValue({ status: 'ok' })
    dismissAll.mockReset()
    dismissAll.mockResolvedValue({ status: 'ok' })
  })

  // Slice: the item must actually disappear from the UI after dismiss + refetch.
  // This tests the observable outcome: setInstances is called with the new (empty) list
  // and the component re-renders. A bug where the refetch result is silently discarded
  // (e.g. setState not called) would not be caught by the call-count assertions below.
  it('dismissed item disappears from UI after successful dismiss and refetch', async () => {
    const inst = {
      id: 'gone',
      workflowName: 'wf',
      state: 'failed' as const,
      priority: 'normal' as const,
      createdAt: '2024-01-01T00:00:00Z',
      updatedAt: '2024-01-01T00:00:00Z',
    }
    list.mockImplementation((params: { state?: string } = {}) =>
      Promise.resolve(params.state === 'failed' ? [inst] : []),
    )

    render(
      <MemoryRouter>
        <QueueView />
      </MemoryRouter>,
    )
    // Row is visible with a Dismiss button.
    await screen.findByRole('button', { name: 'Dismiss' })

    // After dismiss succeeds, switch the mock so refetch returns an empty list.
    list.mockResolvedValue([])
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))

    // The Dismiss button must be gone once the refetch has been consumed.
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: 'Dismiss' })).toBeNull()
    })
  })

  // Happy: dismiss handler calls api.instances.dismiss with the instance id, then refetches.
  // The Failed section is expanded by default so the row is visible without extra interaction.
  it('handleDismiss calls api.instances.dismiss with the instance id, then refetches', async () => {
    const inst = {
      id: 'f1',
      workflowName: 'wf',
      state: 'failed' as const,
      priority: 'normal' as const,
      createdAt: '2024-01-01T00:00:00Z',
      updatedAt: '2024-01-01T00:00:00Z',
    }
    list.mockImplementation((params: { state?: string } = {}) =>
      Promise.resolve(params.state === 'failed' ? [inst] : []),
    )

    render(
      <MemoryRouter>
        <QueueView />
      </MemoryRouter>,
    )
    await screen.findByRole('button', { name: /show dismissed/i })
    list.mockClear()

    // Dismiss button has aria-label="Dismiss" — exact match avoids header div[role="button"].
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))

    await waitFor(() => {
      expect(dismiss).toHaveBeenCalledWith('f1')
    })
    await waitFor(() => {
      expect(list).toHaveBeenCalled()
    })
  })

  // Happy: restore handler calls api.instances.restore with the instance id, then refetches.
  // A dismissed instance (dismissed=true) causes QueueSection to render it with dimmed=true,
  // which makes InstanceRow show the Restore button instead of Dismiss.
  it('handleRestore calls api.instances.restore with the instance id, then refetches', async () => {
    const inst = {
      id: 'f2',
      workflowName: 'wf',
      state: 'failed' as const,
      priority: 'normal' as const,
      dismissed: true,
      createdAt: '2024-01-01T00:00:00Z',
      updatedAt: '2024-01-01T00:00:00Z',
    }
    list.mockImplementation((params: { state?: string } = {}) =>
      Promise.resolve(params.state === 'failed' ? [inst] : []),
    )

    render(
      <MemoryRouter>
        <QueueView />
      </MemoryRouter>,
    )
    await screen.findByRole('button', { name: /show dismissed/i })
    list.mockClear()

    // Restore button has aria-label="Restore".
    fireEvent.click(screen.getByRole('button', { name: 'Restore' }))

    await waitFor(() => {
      expect(restore).toHaveBeenCalledWith('f2')
    })
    await waitFor(() => {
      expect(list).toHaveBeenCalled()
    })
  })

  // Error: handleDismiss API failure — error is caught; view stays stable and does not refetch.
  it('handleDismiss: API failure is caught; view stays stable and does not refetch', async () => {
    const inst = {
      id: 'f3',
      workflowName: 'wf',
      state: 'failed' as const,
      priority: 'normal' as const,
      createdAt: '2024-01-01T00:00:00Z',
      updatedAt: '2024-01-01T00:00:00Z',
    }
    list.mockImplementation((params: { state?: string } = {}) =>
      Promise.resolve(params.state === 'failed' ? [inst] : []),
    )
    dismiss.mockRejectedValue(new Error('server error'))

    render(
      <MemoryRouter>
        <QueueView />
      </MemoryRouter>,
    )
    await screen.findByRole('button', { name: /show dismissed/i })
    list.mockClear()

    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))

    // After the failed API call the view must still render (no crash, no unhandled rejection).
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /show dismissed/i })).toBeInTheDocument()
    })
    // No refetch — the rejection is caught before fetchAll is reached.
    expect(list).not.toHaveBeenCalled()
  })

  // Error: handleRestore API failure — error is caught; view stays stable and does not refetch.
  it('handleRestore: API failure is caught; view stays stable and does not refetch', async () => {
    const inst = {
      id: 'f4',
      workflowName: 'wf',
      state: 'failed' as const,
      priority: 'normal' as const,
      dismissed: true,
      createdAt: '2024-01-01T00:00:00Z',
      updatedAt: '2024-01-01T00:00:00Z',
    }
    list.mockImplementation((params: { state?: string } = {}) =>
      Promise.resolve(params.state === 'failed' ? [inst] : []),
    )
    restore.mockRejectedValue(new Error('server error'))

    render(
      <MemoryRouter>
        <QueueView />
      </MemoryRouter>,
    )
    await screen.findByRole('button', { name: /show dismissed/i })
    list.mockClear()

    fireEvent.click(screen.getByRole('button', { name: 'Restore' }))

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /show dismissed/i })).toBeInTheDocument()
    })
    expect(list).not.toHaveBeenCalled()
  })

  // Error: handleDismissAll API failure — error is caught; view stays stable and does not refetch.
  it('handleDismissAll: API failure is caught; view stays stable and does not refetch', async () => {
    dismissAll.mockRejectedValue(new Error('server error'))

    render(
      <MemoryRouter>
        <QueueView />
      </MemoryRouter>,
    )
    await screen.findByRole('button', { name: /show dismissed/i })
    list.mockClear()

    const [dismissAllBtn] = screen.getAllByRole('button', { name: 'Dismiss All' })
    fireEvent.click(dismissAllBtn)

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /show dismissed/i })).toBeInTheDocument()
    })
    expect(list).not.toHaveBeenCalled()
  })

  // Edge: the Completed section's [Dismiss All] (index 1) must pass state='completed',
  // not 'failed'. A bug where both callbacks captured the same value would pass the
  // failed-section test above but fail here.
  it('handleDismissAll calls api.instances.dismissAll with state=completed for the Completed section', async () => {
    render(
      <MemoryRouter>
        <QueueView />
      </MemoryRouter>,
    )
    await screen.findByRole('button', { name: /show dismissed/i })
    list.mockClear()

    // index 1 = Completed section header; header is always visible regardless of collapsed state.
    const dismissAllBtns = screen.getAllByRole('button', { name: 'Dismiss All' })
    fireEvent.click(dismissAllBtns[1])

    await waitFor(() => {
      expect(dismissAll).toHaveBeenCalledWith(
        'completed',
        expect.stringMatching(/^\d{4}-\d{2}-\d{2}T/),
      )
    })
    await waitFor(() => {
      expect(list).toHaveBeenCalled()
    })
  })

  // Happy: dismissAll is called with the correct state and a since date matching the 5-day
  // window the section shows, then refetches (list is called again).
  it('handleDismissAll calls api.instances.dismissAll with state and since, then refetches', async () => {
    render(
      <MemoryRouter>
        <QueueView />
      </MemoryRouter>,
    )

    // Wait for loading to finish — the toggle button appears once instances load.
    await screen.findByRole('button', { name: /show dismissed/i })
    list.mockClear()

    // Click "Dismiss All" on the Failed section (first of the two rendered buttons).
    // Use the exact string (not a regex) to avoid matching the section header
    // div[role="button"] whose accessible name also contains "Dismiss All".
    const dismissAllBtns = screen.getAllByRole('button', { name: 'Dismiss All' })
    fireEvent.click(dismissAllBtns[0])

    await waitFor(() => {
      expect(dismissAll).toHaveBeenCalledWith(
        'failed',
        expect.stringMatching(/^\d{4}-\d{2}-\d{2}T/),
      )
    })

    // After dismissAll, fetchAll must re-run (list is called again).
    await waitFor(() => {
      expect(list).toHaveBeenCalled()
    })
  })
})
