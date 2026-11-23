import { describe, expect, it } from 'vitest'
import { execStatus, row } from '../api/runners.fixtures'
import { humanBytes, moveProgressText, orderedStatuses, runnerCellText, runnerSummary } from './executionStatus'

const parallel = [
  execStatus({ jobId: 'q', nodeId: 'b', state: 'queued_slot', runnerId: undefined }),
  execStatus({ jobId: 'r', nodeId: 'a', state: 'running', runnerId: 'mac-b', homeRunnerId: 'mac-a' }),
  execStatus({ jobId: 'p', nodeId: 'chat', state: 'running', runnerId: 'mac-c', sessionPinRunnerId: 'mac-c' }),
]

describe('move progress', () => {
  it('formats bytes and progress edges', () => {
    expect(humanBytes(900)).toBe('900 B')
    expect(humanBytes(2048)).toBe('2.0 KiB')
    expect(moveProgressText(undefined)).toBe('progress unknown')
    expect(moveProgressText({ bytesDone: 0, bytesTotal: 0 })).toBe('progress unknown')
    expect(moveProgressText({ bytesDone: 3_000, bytesTotal: 2_000 })).toBe('100% (2.9 KiB of 2.0 KiB)')
    expect(moveProgressText({ bytesDone: 512 * 1024 ** 2, bytesTotal: 2 * 1024 ** 3 })).toBe('25% (512.0 MiB of 2.0 GiB)')
  })
})

describe('orderedStatuses / runnerSummary', () => {
  it('puts active operations first and keeps each operation distinct', () => {
    expect(orderedStatuses(parallel).map((s) => s.jobId)).toEqual(['r', 'p', 'q'])
    expect(orderedStatuses(undefined)).toEqual([])
    expect(parallel.map((s) => s.jobId)).toEqual(['q', 'r', 'p'])
  })

  it('reports the active runner separately from the workspace home and the session pin', () => {
    expect(runnerSummary(parallel)).toEqual({
      active: [
        { jobId: 'r', nodeId: 'a', runnerId: 'mac-b', state: 'running' },
        { jobId: 'p', nodeId: 'chat', runnerId: 'mac-c', state: 'running' },
        { jobId: 'q', nodeId: 'b', runnerId: null, state: 'queued_slot' },
      ],
      home: 'mac-a',
      pin: 'mac-c',
    })
    expect(runnerSummary([])).toEqual({ active: [], home: null, pin: null })
  })

  it('the sortable cell text is the active operation runner', () => {
    expect(runnerCellText(row('r1', parallel))).toBe('mac-b')
    expect(runnerCellText(row('r2', [execStatus({ runnerId: undefined, state: 'queued_slot' })]))).toBe('—')
    expect(runnerCellText(row('r3'))).toBe('—')
  })
})
