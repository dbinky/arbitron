import { render, screen, fireEvent, within } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import type { GatherReport, GroupPanel as GroupPanelData, GroupPanelTask } from '../api/types'
import GroupPanel, { outstandingTasks } from './GroupPanel'

function task(key: string, over: Partial<GroupPanelTask> = {}): GroupPanelTask {
  return {
    key, kind: 'branch', origin: key, childInstanceId: `child-${key}`, liveStatus: 'running', frozenStatus: '',
    blocking: true, continuingIndependently: false, cancelRequested: false, redacted: false, ...over,
  }
}

function panel(over: Partial<GroupPanelData> = {}): GroupPanelData {
  return {
    groupRunId: 'run-1', instanceId: 'root', nodeId: 'g1', form: 'branches', completionRule: 'all_succeed',
    unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results', state: 'running',
    tasks: [task('a'), task('b')],
    progress: { total: 2, completed: 0, failed: 0, cancelled: 0, active: 2, notStarted: 0 },
    waitingReasons: ['Waiting for tasks a, b'],
    ...over,
  }
}

const show = (p: GroupPanelData | null, onOpenSession = vi.fn()) =>
  render(
    <MemoryRouter>
      <GroupPanel panel={p} label="Review in parallel" onOpenSession={onOpenSession} />
    </MemoryRouter>,
  )

const row = (key: string) => screen.getByTestId(`group-task-row-${key}`)

describe('GroupPanel — configuration and ordered tasks (happy path)', () => {
  it('shows rule, unfinished behaviour, gather mode, form and state', () => {
    show(panel())
    expect(screen.getByText('Review in parallel')).toBeInTheDocument()
    expect(screen.getByText('All succeed')).toBeInTheDocument()
    expect(screen.getByText('cancel unfinished')).toBeInTheDocument()
    expect(screen.getByText('results')).toBeInTheDocument()
    expect(screen.getByText('branches')).toBeInTheDocument()
    expect(within(screen.getByTestId('group-panel')).getAllByText('running').length).toBeGreaterThan(0)
  })

  it('lists tasks in author order with links to their child runs', () => {
    show(panel({ tasks: [task('zeta'), task('alpha')] }))
    const keys = [...document.querySelectorAll('[data-testid^="group-task-row-"]')].map((r) => r.getAttribute('data-testid'))
    expect(keys).toEqual(['group-task-row-zeta', 'group-task-row-alpha'])
    expect(within(row('zeta')).getByRole('link', { name: 'open run' }).getAttribute('href')).toBe('/instances/child-zeta')
  })

  it('shows progress and the outstanding blocking tasks', () => {
    show(panel({ tasks: [task('a', { liveStatus: 'completed' }), task('b')], progress: { total: 2, completed: 1, failed: 0, cancelled: 0, active: 1, notStarted: 0 } }))
    expect(screen.getByText('1/2 done · 1 active')).toBeInTheDocument()
    expect(screen.getByText('1 outstanding blocking task: b')).toBeInTheDocument()
    expect(screen.getByText('Waiting for tasks a, b')).toBeInTheDocument()
  })
})

describe('GroupPanel — success: winner, frozen outcome, gather history', () => {
  it('shows the winner and the frozen decision with each task\'s gathered status', () => {
    show(panel({
      completionRule: 'first_success', unfinishedBehavior: 'finish_independently', state: 'done', winner: 'b',
      tasks: [
        task('a', { liveStatus: 'completed', frozenStatus: 'unfinished', blocking: false, continuingIndependently: true }),
        task('b', { liveStatus: 'completed', frozenStatus: 'completed', blocking: false }),
      ],
      progress: { total: 2, completed: 2, failed: 0, cancelled: 0, active: 0, notStarted: 0 },
      waitingReasons: [],
      decision: {
        outcome: 'success', rule: 'first_success', behavior: 'finish_independently', winnerKey: 'b', reason: 'task b succeeded first',
        decidedAt: '2026-10-16T10:00:00Z',
        tasks: [
          { key: 'a', kind: 'branch', origin: 'a1', childInstanceId: 'child-a', status: 'unfinished', cancelRequested: false, winner: false },
          { key: 'b', kind: 'branch', origin: 'b1', childInstanceId: 'child-b', status: 'completed', result: { verdict: 'ok' }, cancelRequested: false, winner: true },
        ],
      },
      finalOutput: { winner: 'b' },
    }))
    expect(screen.getByText('Winner:')).toBeInTheDocument()
    expect(screen.getByText('task b succeeded first', { exact: false })).toBeInTheDocument()
    expect(screen.queryByTestId('group-waiting')).toBeNull()
    expect(screen.getByText('Gathered output')).toBeInTheDocument()
  })

  // A former unfinished non-winner can read COMPLETED live while the immutable
  // gathered snapshot still says UNFINISHED; both must be visible, side by side.
  it('keeps the live status separate from the frozen gathered status', () => {
    show(panel({
      state: 'done',
      tasks: [task('a', { liveStatus: 'completed', frozenStatus: 'unfinished', blocking: false, continuingIndependently: true })],
      waitingReasons: [],
    }))
    const r = row('a')
    expect(within(r).getByText('completed')).toBeInTheDocument()
    expect(within(r).getByText('unfinished')).toBeInTheDocument()
    expect(within(r).getByText('non-blocking')).toBeInTheDocument()
  })

  it('shows the file-merge report and the resolution history', () => {
    const gather: GatherReport = {
      mode: 'results_and_files', state: 'applied', mergeIndex: 2,
      tasks: [{ key: 'a', merged: true, reason: 'clean' }, { key: 'b', merged: true, reason: 'resolved', conflictPaths: ['src/x.go'] }],
      attempts: [
        { mergeIndex: 1, attempt: 1, taskKey: 'b', conflictPaths: ['src/x.go'], outcome: 'still_conflicted', startedAt: '2026-10-16T10:00:00Z', endedAt: '2026-10-16T10:05:00Z' },
        { mergeIndex: 1, attempt: 2, taskKey: 'b', conflictPaths: ['src/x.go'], outcome: 'resolved', startedAt: '2026-10-16T10:06:00Z' },
      ],
    }
    show(panel({ gatherMode: 'results_and_files', state: 'done', waitingReasons: [], gather }))
    expect(screen.getByText('results + files')).toBeInTheDocument()
    const history = screen.getByTestId('group-resolution-history')
    expect(within(history).getByText('merge 2, attempt 1 (task b): still conflicted')).toBeInTheDocument()
    expect(within(history).getByText('merge 2, attempt 2 (task b): resolved')).toBeInTheDocument()
    expect(within(screen.getByTestId('group-gather')).getByText('conflicts: src/x.go')).toBeInTheDocument()
  })
})

describe('GroupPanel — failure details', () => {
  it('shows frozen failure details and a launch error, and styles cancelled work distinctly', () => {
    const { container } = show(panel({
      state: 'done', waitingReasons: [],
      tasks: [
        task('a', { liveStatus: 'failed', frozenStatus: 'failed', blocking: false }),
        task('b', { liveStatus: 'cancelled', frozenStatus: 'cancelled', blocking: false, cancelRequested: true }),
        task('c', { liveStatus: 'failed', childInstanceId: null, launchError: 'launch failed: child workflow not found', blocking: false }),
      ],
      decision: {
        outcome: 'failure', rule: 'all_succeed', behavior: 'cancel_unfinished', reason: 'task a failed', decidedAt: '2026-10-16T10:00:00Z',
        tasks: [{ key: 'a', kind: 'branch', origin: 'a1', childInstanceId: 'child-a', status: 'failed', failure: { state: 'failed', failedNodeId: 'a2', error: 'exit status 1' }, cancelRequested: false, winner: false }],
      },
    }))
    expect(within(screen.getByTestId('group-decision')).getByText('failed: exit status 1 (node a2)')).toBeInTheDocument()
    expect(screen.getByText('launch failed: child workflow not found')).toBeInTheDocument()
    expect(within(row('b')).getByText('cancel requested')).toBeInTheDocument()
    expect(within(row('c')).getByText('not launched')).toBeInTheDocument()
    expect(container.querySelector('[class*="statusCancelled"]')).not.toBeNull()
  })

  it('shows a failed gather error', () => {
    show(panel({ gatherMode: 'results_and_files', state: 'done', waitingReasons: [], gather: { mode: 'results_and_files', state: 'rolled_back', mergeIndex: 0, tasks: [], attempts: [], rollbackStatus: 'rolled_back', error: 'merge aborted' } }))
    expect(within(screen.getByTestId('group-gather')).getByText('merge aborted')).toBeInTheDocument()
    expect(within(screen.getByTestId('group-gather')).getByText('rolled back · rolled back')).toBeInTheDocument()
  })
})

describe('GroupPanel — waiting with zero outstanding tasks', () => {
  it('keeps a done group waiting until its continuation is delivered', () => {
    const p = panel({
      state: 'done', tasks: [],
      progress: { total: 0, completed: 0, failed: 0, cancelled: 0, active: 0, notStarted: 0 },
      waitingReasons: ['continuation_pending'],
    })
    const view = show(p)
    expect(within(screen.getByTestId('group-waiting')).getByText('Waiting for parent continuation')).toBeInTheDocument()
    expect(screen.getByText('0 outstanding blocking tasks')).toBeInTheDocument()

    view.rerender(<MemoryRouter><GroupPanel panel={{ ...p, waitingReasons: [] }} /></MemoryRouter>)
    expect(screen.queryByTestId('group-waiting')).toBeNull()
  })

  it('explains task and cancellation waits in server order', () => {
    show(panel({ waitingReasons: ['waiting_for_tasks:zeta,alpha', 'cancelling:alpha,zeta', 'continuation_pending'] }))
    expect(within(screen.getByTestId('group-waiting')).getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'Waiting for tasks: zeta, alpha', 'Cancelling tasks: alpha, zeta', 'Waiting for parent continuation',
    ])
  })

  // Zero outstanding task dependencies does NOT mean the gather is ready.
  it('shows why it waits on resolution although no task is outstanding, and links the session', () => {
    const onOpen = vi.fn()
    const session = { groupRunId: 'run-1', mergeIndex: 1, attempt: 1, instanceId: 'root', nodeId: 'g1' }
    show(panel({
      gatherMode: 'results_and_files', state: 'resolving_conflicts',
      tasks: [task('a', { liveStatus: 'completed', blocking: false }), task('b', { liveStatus: 'completed', blocking: false })],
      progress: { total: 2, completed: 2, failed: 0, cancelled: 0, active: 0, notStarted: 0 },
      waitingReasons: ['Resolving merge conflicts for task b (attempt 1)'],
      gather: { mode: 'results_and_files', state: 'resolving', mergeIndex: 1, tasks: [], attempts: [], activeSession: session },
    }), onOpen)
    expect(screen.getByText('0 outstanding blocking tasks')).toBeInTheDocument()
    expect(within(screen.getByTestId('group-waiting')).getByText('Resolving merge conflicts for task b (attempt 1)')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Open resolution session' }))
    expect(onOpen).toHaveBeenCalledWith(session)
  })

  it('without a reason from the server it still says the group has not continued', () => {
    show(panel({
      state: 'gathering',
      tasks: [task('a', { liveStatus: 'completed', blocking: false })],
      progress: { total: 1, completed: 1, failed: 0, cancelled: 0, active: 0, notStarted: 0 },
      waitingReasons: [],
    }))
    expect(screen.getByText('No tasks outstanding — the group has not continued yet (gathering).')).toBeInTheDocument()
  })
})

describe('GroupPanel — redaction and edge cases', () => {
  it('shows a hidden dependency for a redacted task, without identifiers or links', () => {
    show(panel({ tasks: [task('a'), task('b', { redacted: true, childInstanceId: null, liveStatus: 'unknown' })] }))
    expect(within(row('b')).getByText('hidden dependency')).toBeInTheDocument()
    expect(within(row('b')).queryByRole('link')).toBeNull()
    expect(within(row('a')).getByRole('link')).toBeInTheDocument()
  })

  it('renders an empty state for a group that has not started', () => {
    show(null)
    expect(screen.getByText('This group has not started yet.')).toBeInTheDocument()
  })

  it('outstandingTasks ignores released and terminal tasks', () => {
    const p = panel({ tasks: [task('a'), task('b', { blocking: false }), task('c', { liveStatus: 'completed' }), task('d', { liveStatus: 'queued' })] })
    expect(outstandingTasks(p).map((t) => t.key)).toEqual(['a', 'd'])
  })
})
