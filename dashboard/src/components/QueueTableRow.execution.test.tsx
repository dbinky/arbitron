import { render } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import { T0, execStatus, iso, row } from '../api/runners.fixtures'
import type { EnrichedInstanceSummary, QueueDensity } from '../api/types'
import QueueTableRow from './QueueTableRow'
import { getColumn, visibleColumns } from './queueColumns'

function renderRow(r: EnrichedInstanceSummary, density: QueueDensity = 'balanced') {
  return render(
    <MemoryRouter>
      <table>
        <tbody>
          <QueueTableRow row={r} columns={visibleColumns(density)} timeMode="elapsed" authMode="local" now={T0} />
        </tbody>
      </table>
    </MemoryRouter>,
  )
}
const cell = (c: HTMLElement, key: string) => c.querySelector<HTMLElement>(`td[data-col="${key}"]`)

describe('Runner column declaration', () => {
  it('sits right after Status, is sortable by the active runner, and is hidden in dense mode', () => {
    const col = getColumn('runner')!
    expect(col.header).toBe('Runner')
    expect(col.sortable).toBe(true)
    expect(col.densities).toEqual(['balanced', 'expanded'])
    const keys = visibleColumns('balanced').map((c) => c.key)
    expect(keys[keys.indexOf('status') + 1]).toBe('runner')
    expect(visibleColumns('dense').map((c) => c.key)).not.toContain('runner')
    expect(col.accessor(row('r', [execStatus({ runnerId: 'mac-a' })]))).toBe('mac-a')
  })
})

describe('QueueTableRow — execution status', () => {
  it('shows a waiting · capacity badge with its reason next to the run state', () => {
    const { container } = renderRow(row('run-1', [execStatus({ state: 'waiting_capacity', runnerId: undefined, reason: 'all anthropic subscriptions exhausted', nextReset: iso(120_000) })]))
    const status = cell(container, 'status')!
    expect(status).toHaveTextContent('running')
    expect(status.querySelector('[data-exec-badge="waiting_capacity"]')).toHaveTextContent(
      'waiting · capacity — all anthropic subscriptions exhausted · model claude-opus-4-1 · resets in 2m 0s',
    )
    expect(cell(container, 'runner')).toHaveTextContent('unassigned')
  })

  it('shows workspace move progress and recovery as distinct badges', () => {
    const { container } = renderRow(
      row('run-1', [
        execStatus({ jobId: 'j1', nodeId: 'build', state: 'moving_workspace', progress: { bytesDone: 1024 ** 3, bytesTotal: 4 * 1024 ** 3 } }),
        execStatus({ jobId: 'j2', nodeId: 'chat', state: 'recovering' }),
      ]),
    )
    const status = cell(container, 'status')!
    expect(status.querySelector('[data-exec-badge="moving_workspace"]')).toHaveTextContent('build: moving workspace · 25% (1.0 GiB of 4.0 GiB)')
    expect(status.querySelector('[data-exec-badge="recovering"]')).toHaveTextContent('chat: recovering')
  })

  it('an idle interactive session waiting at the instance level still shows its recovery and pin', () => {
    const { container } = renderRow(row('run-1', [execStatus({ state: 'recovering', runnerId: undefined, sessionPinRunnerId: 'mac-c' })], { state: 'waiting' }))
    expect(cell(container, 'status')).toHaveTextContent('waiting')
    expect(cell(container, 'status')?.querySelector('[data-exec-badge="recovering"]')).not.toBeNull()
    expect(cell(container, 'runner')?.querySelector('[data-pin-runner="mac-c"]')).toHaveTextContent('pinned mac-c')
  })

  it('the active chat runner differs visibly from the workspace home', () => {
    const { container } = renderRow(row('run-1', [execStatus({ runnerId: 'mac-b', homeRunnerId: 'mac-a' })]))
    const runner = cell(container, 'runner')!
    expect(runner.querySelector('[data-exec-runner="mac-b"]')).not.toBeNull()
    expect(runner.querySelector('[data-home-runner="mac-a"]')).toHaveTextContent('home mac-a')
  })

  it('a row without execution data renders the plain state and a dash', () => {
    const { container } = renderRow(row('run-1'))
    expect(cell(container, 'status')?.textContent).toBe('running')
    expect(cell(container, 'runner')?.textContent).toBe('—')
  })
})
