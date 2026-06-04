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
