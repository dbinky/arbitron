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
