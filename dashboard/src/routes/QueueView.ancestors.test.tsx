import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { MemoryRouter, useLocation } from 'react-router-dom'
import QueueView from './QueueView'
import { localAdmin } from './queueWorld.fixtures'
// Produced by the real router -> engine -> SQLite list pipeline, including
// the 500-row cap and child counts; Go checks this fixture on every test run.
import wire from '../api/fixtures/queue-ancestors.wire.json'

vi.mock('../hooks/useSystemWebSocket', () => ({ useSystemWebSocket: () => ({ connected: true }) }))
vi.mock('../hooks/useDensityPreference', () => ({
  useDensityPreference: () => ({ density: 'balanced', cycle: vi.fn() }),
}))

afterEach(() => vi.unstubAllGlobals())

function Location() {
  return <output data-testid="location">{useLocation().pathname}</output>
}

// Omitting discovery, offset, includeDismissed, or the explicit limit cannot
// reach the final parent or its child through the real API client and rows.
it('discovers 501 real-store ancestors and navigates to the final parent’s child', async () => {
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = new URL(input, 'http://localhost')
    const p = url.searchParams
    const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), {
      status, headers: { 'Content-Type': 'application/json' },
    })
    if (url.pathname === '/api/v1/me') return json(localAdmin)
    if (url.pathname !== '/api/v1/instances') return json({ message: 'not found' }, 404)
    if (p.has('activeDescendantAncestors')) {
      if (p.get('activeDescendantAncestors') !== 'true' || p.get('includeDismissed') !== 'true'
        || p.get('limit') !== '500' || p.has('state') || p.has('parent')) {
        return json({ message: 'incorrect discovery scope' }, 400)
      }
      return json(wire.pages[Number(p.get('offset') ?? 0) / 500] ?? [])
    }
    if (p.get('parent') === '00000000-0000-0000-0000-000000000001'
      && p.get('limit') === '100' && p.get('includeDismissed') === 'true') return json(wire.children)
    return json([])
  }))
  render(<MemoryRouter><QueueView /><Location /></MemoryRouter>)

  const parentId = '00000000-0000-0000-0000-000000000001'
  const childId = '10000000-0000-0000-0000-000000000001'
  const row = (id: string) => document.querySelector<HTMLElement>(`tr[data-row-id="${id}"]`)
  await waitFor(() => expect(row(parentId)).not.toBeNull())
  // All ancestors are consumed, including 499 nested terminal rows. Keep their
  // root collapsed: rendering them adds no coverage of discovery or navigation.
  expect(screen.getByText('Active', { selector: 'span' }).parentElement).toHaveTextContent('501')
  expect(document.querySelectorAll('tr[data-row-id]')).toHaveLength(2)
  expect(row(wire.pages[0][0].id)).not.toBeNull()
  expect(within(row(parentId)!).getByText('owns active work')).toBeInTheDocument()
  fireEvent.click(within(row(parentId)!).getByRole('button', { name: /Show child runs/ }))
  await waitFor(() => expect(row(childId)).not.toBeNull())
  expect(row(childId)).toHaveAttribute('data-depth', '1')
  fireEvent.click(row(childId)!)
  expect(screen.getByTestId('location')).toHaveTextContent(`/instances/${childId}`)
})
