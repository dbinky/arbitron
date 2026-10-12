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

describe('Git and Model columns', () => {
  it('Git sits directly after Run', () => {
    const keys = COLUMNS.map((c) => c.key)
    expect(keys.slice(0, 3)).toEqual(['workflow', 'runName', 'git'])
  })
  it('Git column shape', () => {
    const git = getColumn('git')!
    expect(git.header).toBe('Git')
    expect(git.sortable).toBe(true)
    expect(git.densities).toEqual(['balanced', 'expanded'])
    expect(git.accessor(row({ gitRef: 'feat/x' }))).toBe('feat/x')
    expect(git.accessor(row({ gitRef: undefined }))).toBe('—')
  })
  it('model column is headed Model and formats the binding', () => {
    const model = getColumn('modelBackend')!
    expect(model.header).toBe('Model')
    expect(model.accessor(row({ model: 'm', effort: 'high', backend: 'codex', provider: 'litellm' }))).toBe(
      'm (high) · codex · litellm',
    )
  })
})

describe('sortAccessor', () => {
  it('date/numeric columns declare a sortAccessor returning a sortable primitive', () => {
    const elapsed = getColumn('elapsed')!
    expect(typeof elapsed.sortAccessor).toBe('function')
    // elapsed sorts by run duration (ms), so terminal rows rank by how long they took.
    const quick = elapsed.sortAccessor!(
      row({ state: 'completed', createdAt: '2024-01-01T00:00:00Z', completedAt: '2024-01-01T00:01:00Z' }),
    )
    const slow = elapsed.sortAccessor!(
      row({ state: 'completed', createdAt: '2024-01-01T00:00:00Z', completedAt: '2024-01-01T02:00:00Z' }),
    )
    expect(quick).toBe(60_000)
    expect(slow).toBe(2 * 3600_000)

    const queue = getColumn('queuePosition')!
    expect(queue.sortAccessor!(row({ queuePosition: 5 }))).toBe(5)
    // absent position sorts last (a large number), not 0.
    expect(queue.sortAccessor!(row({ queuePosition: undefined }))).toBe(Number.MAX_SAFE_INTEGER)

    const retries = getColumn('retryCount')!
    expect(retries.sortAccessor!(row({ retryCount: 3 }))).toBe(3)
  })

  it('string columns have no sortAccessor (default to the string accessor)', () => {
    const workflow = getColumn('workflow')!
    expect(workflow.sortAccessor).toBeUndefined()
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