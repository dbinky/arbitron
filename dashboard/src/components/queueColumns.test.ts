import { describe, it, expect } from 'vitest'
import { COLUMNS, visibleColumns, getColumn } from './queueColumns'
import type { EnrichedInstanceSummary, QueueDensity } from '../api/types'

function row(overrides: Partial<EnrichedInstanceSummary> = {}): EnrichedInstanceSummary {
  return {
    id: 'i1',
    ownerId: 'u1',
    workflowName: 'wf',
    state: 'running',
    priority: 'normal',
    createdAt: '2024-01-01T00:00:00Z',
    updatedAt: '2024-01-01T00:00:00Z',
    runName: 'run — wf',
    retryCount: 0,
    ...overrides,
  }
}

describe('COLUMNS', () => {
  it('declares all spec columns', () => {
    const keys = COLUMNS.map((c) => c.key)
    expect(keys).toEqual(
      expect.arrayContaining([
        'workflow',
        'runName',
        'submitter',
        'submittedAt',
        'status',
        'friendlyStep',
        'elapsed',
        'queuePosition',
        'modelBackend',
        'retryCount',
      ]),
    )
  })

  it('every column has a key, header, accessor, sortable flag, and density visibility', () => {
    for (const c of COLUMNS) {
      expect(c.key).toBeTruthy()
      expect(c.header).toBeTruthy()
      expect(typeof c.accessor).toBe('function')
      expect(typeof c.sortable).toBe('boolean')
      expect(c.densities).toBeDefined()
    }
  })
})

describe('visibleColumns', () => {
  it('balanced density shows the full set minus only secondary detail columns', () => {
    const cols = visibleColumns('balanced' as QueueDensity)
    const keys = cols.map((c) => c.key)
    expect(keys).toContain('runName')
    expect(keys).toContain('status')
    expect(keys).toContain('friendlyStep')
    expect(keys).toContain('submitter')
  })

  it('dense density hides secondary columns to fit more rows', () => {
    const cols = visibleColumns('dense' as QueueDensity)
    const keys = cols.map((c) => c.key)
    expect(keys).not.toContain('submittedAt')
    expect(keys).not.toContain('friendlyStep')
    expect(keys).toContain('runName')
    expect(keys).toContain('status')
  })

  it('expanded density shows every column including secondary detail', () => {
    const cols = visibleColumns('expanded' as QueueDensity)
    const keys = cols.map((c) => c.key)
    expect(keys).toContain('submittedAt')
    expect(keys).toContain('modelBackend')
    expect(keys).toContain('retryCount')
  })
})

describe('accessors', () => {
  it('runName accessor returns the run name', () => {
    const c = getColumn('runName')!
    expect(c.accessor(row({ runName: 'My — wf' }))).toBe('My — wf')
  })

  it('submitter accessor returns the submitter name', () => {
    const c = getColumn('submitter')!
    expect(c.accessor(row({ submitterName: 'alice' }))).toBe('alice')
  })

  it('queuePosition accessor returns the position as a string', () => {
    const c = getColumn('queuePosition')!
    expect(c.accessor(row({ queuePosition: 3 }))).toBe('3')
  })

  it('queuePosition accessor returns placeholder for absent position', () => {
    const c = getColumn('queuePosition')!
    expect(c.accessor(row({ queuePosition: undefined }))).toBe('—')
  })

  it('modelBackend accessor combines model and backend', () => {
    const c = getColumn('modelBackend')!
    expect(c.accessor(row({ model: 'gpt-4o', backend: 'claude-code' }))).toBe('gpt-4o · claude-code')
  })

  it('modelBackend accessor returns model only when backend unset', () => {
    const c = getColumn('modelBackend')!
    expect(c.accessor(row({ model: 'gpt-4o' }))).toBe('gpt-4o')
  })

  it('modelBackend accessor returns placeholder when both empty', () => {
    const c = getColumn('modelBackend')!
    expect(c.accessor(row())).toBe('—')
  })
})