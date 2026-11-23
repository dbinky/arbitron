import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import type { Instance, NodeExecution } from '../api/types'
import PlacementPanel, { LOSSY_SESSION_NOTICE } from './PlacementPanel'
import { RECOVERY_DISCLOSURE } from './poolModel'

// instance.RecoveryNoticeText (P3 T10), carried on each `recovery` item's `text`.
const NOTICE = 'interrupted work may repeat external effects; exactly-once effects are not guaranteed'
const lostMove = { id: 'm1', runId: 'run-1', invocationId: 'inv-1', requestId: 'request-1', turnIndex: 1, nodeId: 'review', fromRunner: 'mac-b', toRunner: 'mac-a', reason: 'runner_lost', lossy: true, generation: 3, at: '2026-11-20T09:57:00Z' }
const exec = (over: Partial<NodeExecution> = {}): NodeExecution => ({
  id: 'x',
  instanceId: 'run-1',
  nodeId: 'review',
  attempt: 3,
  status: 'succeeded',
  startedAt: '2026-11-20T10:00:00Z',
  runnerId: 'mac-a',
  sourceId: 'mac-a/anthropic_subscription/-',
  placementReason: 'least loaded eligible runner with anthropic headroom',
  sessionPinRunnerId: 'mac-a',
  sessionGeneration: 3,
  sessionMoves: [lostMove],
  recovery: [{ nodeId: 'review', jobId: 'j', runnerId: 'mac-b', reason: 'runner_lost', at: '2026-11-20T09:50:00Z', text: NOTICE }],
  placementHistory: [
    { jobId: 'j', runnerId: 'mac-b', sourceId: 'mac-b/anthropic_subscription/-', reason: 'workspace home', outcome: 'runner_lost', generation: 1, at: '2026-11-20T09:50:00Z' },
    { jobId: 'j', runnerId: 'builtin', sourceId: 'builtin/anthropic_subscription/-', reason: 'only eligible runner', outcome: 'fenced', generation: 2, at: '2026-11-20T09:55:00Z' },
    { jobId: 'j', runnerId: 'mac-a', sourceId: 'mac-a/anthropic_subscription/-', reason: 'least loaded eligible runner with anthropic headroom', outcome: '', generation: 3, at: '2026-11-20T10:00:00Z' },
  ],
  ...over,
})
const instance = (over: Partial<Instance> = {}) =>
  ({
    id: 'run-1',
    workspaceMoves: [{ from: 'mac-b', to: 'mac-a', reason: 'mac-b lost', bytes: 2048, generation: 2, at: '2026-11-20T09:56:00Z' }],
    ...over,
  }) as unknown as Instance

function renderPanel(props: Partial<Parameters<typeof PlacementPanel>[0]> = {}) {
  return render(
    <MemoryRouter>
      <PlacementPanel execution={exec()} instance={instance()} nodeId="review" nodeType="agent_call" ownerIsRoute {...props} />
    </MemoryRouter>,
  )
}
const field = (name: string) => document.querySelector(`[data-placement="${name}"]`)

describe('PlacementPanel', () => {
  it('shows runner, source, reason, pin and every attempt with its outcome', () => {
    renderPanel()
    expect(screen.getByRole('link', { name: 'mac-a' })).toHaveAttribute('href', '/pool/runners/mac-a')
    expect(field('source')).toHaveTextContent('mac-a/anthropic_subscription/-')
    expect(field('reason')).toHaveTextContent('least loaded eligible runner with anthropic headroom')
    expect(field('pin')).toHaveTextContent('mac-a · generation 3')
    const attempts = [...document.querySelectorAll('[data-attempt]')].map((li) => li.textContent)
    expect(attempts[0]).toContain('mac-b · mac-b/anthropic_subscription/- · workspace home · runner lost')
    expect(attempts[1]).toContain('rolled back to the last committed checkpoint')
    expect(attempts[2]).not.toContain(' · no outcome')
  })

  it('shows the workspace home history, the lossy session notice and the durable recovery notice', () => {
    renderPanel()
    expect(document.querySelector('[data-home-move]')).toHaveTextContent('mac-b → mac-a · mac-b lost · 2.0 KiB')
    expect(document.querySelector('[data-session-move]')).toHaveTextContent('Session moved mac-b → mac-a · runner_lost')
    expect(screen.getByRole('note')).toHaveTextContent(LOSSY_SESSION_NOTICE)
    expect(document.querySelector('[data-recovery-disclosure]')).toHaveTextContent(NOTICE)
  })

  it('server-side nodes say they run on the server', () => {
    renderPanel({ execution: null, nodeType: 'conditional' })
    expect(field('server')).toHaveTextContent('Runs on the server')
  })

  it('a step that has not run yet is not placed and has no disclosure', () => {
    renderPanel({ execution: null, instance: instance({ workspaceMoves: [] }) })
    expect(field('none')).toHaveTextContent('Not placed yet')
    expect(document.querySelector('[data-recovery-disclosure]')).toBeNull()
  })

  it('falls back to the standard disclosure for interrupted work without a server notice; a non-lossy move has no notice', () => {
    renderPanel({
      execution: exec({ recovery: [], sessionMoves: [{ ...lostMove, fromRunner: 'a', toRunner: 'b', reason: 'drain', lossy: false, generation: 2 }] }),
    })
    expect(document.querySelector('[data-recovery-disclosure]')).toHaveTextContent(RECOVERY_DISCLOSURE)
    expect(screen.queryByRole('note')).toBeNull()
  })

  it('a branch node shows its own placement but not the route run\'s home history', () => {
    renderPanel({ ownerIsRoute: false, execution: exec({ attempt: 1, placementHistory: [], sessionMoves: [], recovery: [] }) })
    expect(screen.getByRole('link', { name: 'mac-a' })).toBeInTheDocument()
    expect(document.querySelector('[data-home-move]')).toBeNull()
    expect(screen.queryByRole('note')).toBeNull()
    expect(document.querySelector('[data-recovery-disclosure]')).toBeNull()
  })
})
