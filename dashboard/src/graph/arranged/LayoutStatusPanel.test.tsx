import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import LayoutStatusPanel, { type LayoutStatusPanelProps } from './LayoutStatusPanel'
import type { DisplayStatus, LayoutView } from './types'
import { fixtureView } from './testFixtures'

function view(displayStatus: DisplayStatus, over: Partial<LayoutView> = {}): LayoutView {
  return fixtureView({ displayStatus, attemptsUsed: 2, ...over })
}

function renderPanel(over: Partial<LayoutStatusPanelProps> = {}) {
  const props: LayoutStatusPanelProps = {
    view: null,
    error: null,
    onRetry: vi.fn(),
    retrying: false,
    retryError: null,
    canRetry: true,
    ...over,
  }
  return { ...render(<LayoutStatusPanel {...props} />), props }
}

describe('LayoutStatusPanel — titles', () => {
  it.each([
    ['pending', 'Arrangement pending'],
    ['arranging', 'Arranging layout'],
    ['rendering', 'Rendering for review'],
    ['reviewing', 'Reviewing readability'],
    ['complete', 'Layout reviewed'],
    ['needs_attention', 'Layout needs attention'],
    ['failed', 'Layout arrangement failed'],
    ['unavailable', 'Arrangement status unavailable'],
  ] as Array<[DisplayStatus, string]>)('%s → %s', (status, title) => {
    renderPanel({ view: view(status) })
    expect(screen.getByRole('status')).toHaveTextContent(title)
  })

  it('shows attempt N of M while arranging, rendering or reviewing', () => {
    renderPanel({ view: view('rendering', { attemptsUsed: 3, attemptLimit: 5 }) })
    expect(screen.getByText('attempt 3 of 5')).toBeInTheDocument()
  })

  it('does not show an attempt counter for pending or terminal states', () => {
    renderPanel({ view: view('pending', { attemptsUsed: 0 }) })
    expect(screen.queryByText(/attempt \d+ of/)).toBeNull()
  })

  it('renders nothing for a legacy (pre-feature) revision', () => {
    const { container } = renderPanel({ view: view('legacy', { provenance: 'legacy' }) })
    expect(container).toBeEmptyDOMElement()
  })

  it('shows a checking state before the first status arrives', () => {
    renderPanel()
    expect(screen.getByRole('status')).toHaveTextContent('Checking arrangement status')
  })
})

describe('LayoutStatusPanel — truthful verdicts', () => {
  it.each([
    ['unreviewed', { unreviewed: true }, []],
    ['missing geometry', { geometry: undefined }, []],
    ['retained geometry', { provenance: 'retained_geometry' }, []],
    ['retained positions', { provenance: 'retained_positions' }, []],
    ['legacy provenance', { provenance: 'legacy' }, []],
    ['unavailable', { available: false }, []],
    ['incompatible contract', {}, ['contract mismatch']],
  ] as Array<[string, Partial<LayoutView>, string[]]>)('downgrades complete with %s', (_, overrides, displayIssues) => {
    renderPanel({ view: view('complete', overrides), displayIssues })
    expect(screen.getByRole('status')).toHaveTextContent('Layout needs attention')
    expect(screen.getByRole('status')).not.toHaveTextContent('Layout reviewed')
  })
  it('never shows reviewed/complete wording for needs_attention (including unfinished superseded work)', () => {
    renderPanel({ view: view('needs_attention', { state: 'superseded', provenance: 'retained_geometry' }) })
    const status = screen.getByRole('status')
    expect(status).not.toHaveTextContent('Layout reviewed')
    expect(status).not.toHaveTextContent(/complete/i)
    expect(status).toHaveTextContent('Layout needs attention')
  })

  it('downgrades complete to needs attention when the geometry could not be displayed', () => {
    renderPanel({
      view: view('complete'),
      displayIssues: ['renderer contract version "arranged-renderer/2" does not match "arranged-renderer/1"'],
    })
    const status = screen.getByRole('status')
    expect(status).not.toHaveTextContent('Layout reviewed')
    expect(status).toHaveTextContent('Layout needs attention')
    expect(status).toHaveTextContent(
      'This layout could not be displayed: renderer contract version "arranged-renderer/2" does not match "arranged-renderer/1"',
    )
    expect(status).toHaveTextContent('Showing the previous layout — not yet reviewed for this revision')
  })

  it.each(['retained_geometry', 'retained_positions'] as const)(
    'notes that a %s layout is not yet reviewed for this revision',
    (provenance) => {
      renderPanel({ view: view('arranging', { provenance }) })
      expect(screen.getByText('Showing the previous layout — not yet reviewed for this revision')).toBeInTheDocument()
    },
  )

  it('notes an unreviewed best layout', () => {
    renderPanel({ view: view('needs_attention', { unreviewed: true }) })
    expect(screen.getByText('Showing the previous layout — not yet reviewed for this revision')).toBeInTheDocument()
  })

  it('does not add the note for a reviewed arranged layout', () => {
    renderPanel({ view: view('complete') })
    expect(screen.queryByText(/not yet reviewed/)).toBeNull()
  })
})

describe('LayoutStatusPanel — findings and problems', () => {
  it('lists blocking findings before advisory ones with criterion and evidence', () => {
    renderPanel({
      view: view('needs_attention', {
        findings: [
          { criterion: 'crossings', severity: 'advisory', source: 'preflight', evidence: '3 crossings' },
          { criterion: 'text_clipped', severity: 'blocking', source: 'review', evidence: 'label of "b" clipped' },
        ],
      }),
    })
    const items = screen.getAllByRole('listitem')
    expect(items.map((li) => li.getAttribute('data-severity'))).toEqual(['blocking', 'advisory'])
    expect(items[0]).toHaveTextContent('text_clipped (blocking): label of "b" clipped')
    expect(items[1]).toHaveTextContent('crossings (advisory): 3 crossings')
  })

  it('treats null findings as none', () => {
    renderPanel({ view: view('failed', { findings: null }) })
    expect(screen.queryAllByRole('listitem')).toHaveLength(0)
  })

  it('shows the failure stage and reason', () => {
    renderPanel({ view: view('failed', { failureStage: 'rendering', failureReason: 'no Chrome/Chromium browser available' }) })
    expect(screen.getByText('Reason (rendering): no Chrome/Chromium browser available')).toBeInTheDocument()
  })

  it.each([
    ['pending', 'Arrangement pending'],
    ['arranging', 'Arranging layout'],
    ['rendering', 'Rendering for review'],
    ['reviewing', 'Reviewing readability'],
  ] as Array<[DisplayStatus, string]>)('defers invalid-group failure diagnostics during %s until the job fails', (status, title) => {
    // Reader.View includes the graph diagnostic even while this generation is
    // active. Rendering it unconditionally would report failure before the
    // worker settles the row.
    const active = view(status, {
      state: status === 'pending' ? 'pending' : 'in_progress',
      failureStage: 'arranging',
      failureReason: 'layout: invalid parallel group graph',
      geometry: undefined,
      contract: undefined,
      evidence: undefined,
      provenance: 'retained_positions',
      retained: { source: 'manifest', positions: { fan: { x: 123, y: 456 } } },
      unreviewed: true,
      retryEligible: false,
    })
    const { props, rerender } = renderPanel({ view: active })
    expect(screen.getByRole('status')).toHaveTextContent(title)
    expect(screen.getByRole('status')).not.toHaveTextContent(/Reason|invalid parallel group graph|Layout reviewed|Layout arrangement failed/)
    expect(screen.queryByRole('button', { name: 'Retry arrangement' })).toBeNull()

    rerender(<LayoutStatusPanel {...props} view={{ ...active, state: 'failed', displayStatus: 'failed', retryEligible: true }} />)
    expect(screen.getByRole('status')).toHaveTextContent('Layout arrangement failed')
    expect(screen.getByText('Reason (arranging): layout: invalid parallel group graph')).toBeInTheDocument()
    expect(screen.getByRole('status')).not.toHaveTextContent('Layout reviewed')
    fireEvent.click(screen.getByRole('button', { name: 'Retry arrangement' }))
    expect(props.onRetry).toHaveBeenCalledTimes(1)
  })

  it('shows the availability error', () => {
    renderPanel({ view: view('pending', { availabilityError: 'awaiting recovery' }) })
    expect(screen.getByText('awaiting recovery')).toBeInTheDocument()
  })

  it('shows a network error while keeping the last status', () => {
    renderPanel({ view: view('reviewing'), error: 'network down' })
    expect(screen.getByRole('status')).toHaveTextContent('Reviewing readability')
    expect(screen.getByText('Could not refresh arrangement status: network down')).toBeInTheDocument()
  })

  it('shows status unavailable when the first read failed', () => {
    renderPanel({ error: 'API 503: layout: status store unavailable' })
    expect(screen.getByRole('status')).toHaveTextContent('Arrangement status unavailable')
  })

  it('shows the retry error', () => {
    renderPanel({ view: view('failed', { retryEligible: true }), retryError: 'API 409: conflict' })
    expect(screen.getByText('Retry failed: API 409: conflict')).toBeInTheDocument()
  })
})

describe('LayoutStatusPanel — Retry', () => {
  it('offers reload for an initial unavailable read, including after a failed retry', () => {
    const { props } = renderPanel({ error: 'store offline', retryError: 'still offline' })
    expect(screen.getByRole('status')).toHaveTextContent('Arrangement status unavailable')
    expect(screen.getByText('Retry failed: still offline')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Retry arrangement' }))
    expect(props.onRetry).toHaveBeenCalledTimes(1)
  })

  it('offers Retry for an unavailable view without geometry even when retryEligible is false', () => {
    renderPanel({ view: view('unavailable', { available: false, geometry: undefined, retryEligible: false }) })
    expect(screen.getByRole('button', { name: 'Retry arrangement' })).toBeInTheDocument()
  })

  it('keeps legacy invisible unless there is a read error or display issue', () => {
    renderPanel({ view: view('legacy', { provenance: 'legacy' }), error: 'offline' })
    expect(screen.getByRole('status')).toHaveTextContent('Arrangement status unavailable')
    expect(screen.getByText('Could not refresh arrangement status: offline')).toBeInTheDocument()
  })
  it('offers Retry for a retry-eligible revision and calls onRetry', () => {
    const { props } = renderPanel({ view: view('needs_attention', { retryEligible: true }) })
    const btn = screen.getByRole('button', { name: 'Retry arrangement' })
    expect(btn).toHaveAttribute('type', 'button')
    fireEvent.click(btn)
    expect(props.onRetry).toHaveBeenCalledTimes(1)
  })

  it('offers Retry after a status read error even when the view is not eligible', () => {
    renderPanel({ view: view('reviewing'), error: 'network down' })
    expect(screen.getByRole('button', { name: 'Retry arrangement' })).toBeInTheDocument()
  })

  it('hides Retry when not eligible and there is no error', () => {
    renderPanel({ view: view('complete') })
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('hides Retry when the caller cannot retry', () => {
    renderPanel({ view: view('failed', { retryEligible: true }), canRetry: false })
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('disables Retry while a retry is in flight', () => {
    renderPanel({ view: view('failed', { retryEligible: true }), retrying: true })
    expect(screen.getByRole('button', { name: 'Retrying…' })).toBeDisabled()
  })
})
