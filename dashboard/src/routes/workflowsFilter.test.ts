import { describe, it, expect } from 'vitest'
import type { WorkflowSummary, Me } from '../api/types'
import {
  defaultScope,
  filterWorkflows,
  parseFilterParams,
  type FilterCriteria,
} from './workflowsFilter'

function wf(overrides: Partial<WorkflowSummary> = {}): WorkflowSummary {
  return {
    name: 'wf',
    description: 'd',
    author: 'a',
    nodeCount: 1,
    scope: 'private',
    ...overrides,
  }
}

function me(overrides: Partial<Me> = {}): Me {
  return {
    userId: 'u1',
    username: 'tester',
    role: 'user',
    departments: [],
    ledDepartments: [],
    isAdmin: false,
    authMode: 'local',
    ...overrides,
  }
}

function criteria(overrides: Partial<FilterCriteria> = {}): FilterCriteria {
  return {
    scope: 'all',
    vis: 'all',
    dept: '',
    search: '',
    showVisibility: false,
    showDepartment: false,
    ...overrides,
  }
}

describe('defaultScope', () => {
  it('defaults to "all" in local mode', () => {
    expect(defaultScope(me({ authMode: 'local' }))).toBe('all')
  })

  it('defaults to "shared" in org mode', () => {
    expect(defaultScope(me({ authMode: 'org' }))).toBe('shared')
  })

  it('defaults to "shared" in edge mode', () => {
    expect(defaultScope(me({ authMode: 'edge' }))).toBe('shared')
  })

  it('defaults to "all" when identity is not yet loaded', () => {
    expect(defaultScope(null)).toBe('all')
  })
})

describe('filterWorkflows', () => {
  const mine = wf({ name: 'mine', scope: 'private' })
  const sharedPublic = wf({ name: 'pub', scope: 'shared', visibility: 'public' })
  const sharedDept = wf({ name: 'dept', scope: 'shared', visibility: 'department', department: 'finance' })

  it('scope "mine" keeps only private rows', () => {
    const out = filterWorkflows([mine, sharedPublic], criteria({ scope: 'mine' }))
    expect(out.map((w) => w.name)).toEqual(['mine'])
  })

  it('scope "shared" keeps only shared rows', () => {
    const out = filterWorkflows([mine, sharedPublic], criteria({ scope: 'shared' }))
    expect(out.map((w) => w.name)).toEqual(['pub'])
  })

  it('scope "all" keeps both private and shared rows', () => {
    const out = filterWorkflows([mine, sharedPublic], criteria({ scope: 'all' }))
    expect(out.map((w) => w.name).sort()).toEqual(['mine', 'pub'])
  })

  it('visibility filter is inert unless showVisibility is true', () => {
    // Even with vis=department, the filter does not apply when the facet is hidden.
    const out = filterWorkflows([sharedPublic, sharedDept], criteria({ vis: 'department', showVisibility: false }))
    expect(out.map((w) => w.name).sort()).toEqual(['dept', 'pub'])
  })

  it('visibility filter narrows to the chosen level when the facet is shown', () => {
    const out = filterWorkflows([sharedPublic, sharedDept], criteria({ vis: 'department', showVisibility: true }))
    expect(out.map((w) => w.name)).toEqual(['dept'])
  })

  it('department filter is inert unless showDepartment is true', () => {
    const out = filterWorkflows([sharedDept], criteria({ dept: 'finance', showDepartment: false }))
    expect(out.map((w) => w.name)).toEqual(['dept'])
  })

  it('department filter narrows to the chosen department when shown', () => {
    const other = wf({ name: 'mktg', scope: 'shared', visibility: 'department', department: 'marketing' })
    const out = filterWorkflows([sharedDept, other], criteria({ dept: 'finance', showDepartment: true }))
    expect(out.map((w) => w.name)).toEqual(['dept'])
  })

  it('search matches a case-insensitive substring of the name', () => {
    const out = filterWorkflows([wf({ name: 'NightlyReport' }), wf({ name: 'other' })], criteria({ search: 'report' }))
    expect(out.map((w) => w.name)).toEqual(['NightlyReport'])
  })

  it('trims surrounding whitespace from the search term', () => {
    const out = filterWorkflows([wf({ name: 'alpha' }), wf({ name: 'beta' })], criteria({ search: '  alpha  ' }))
    expect(out.map((w) => w.name)).toEqual(['alpha'])
  })

  it('an empty criteria set returns every row unchanged', () => {
    const all = [mine, sharedPublic, sharedDept]
    expect(filterWorkflows(all, criteria())).toEqual(all)
  })
})

describe('parseFilterParams', () => {
  const departments = ['finance', 'marketing']

  it('reads recognized scope/vis/dept/q params', () => {
    const sp = new URLSearchParams('scope=mine&vis=public&dept=finance&q=hello')
    const got = parseFilterParams(sp, me(), departments)
    expect(got).toEqual({ scope: 'mine', vis: 'public', dept: 'finance', search: 'hello' })
  })

  it('falls back to the mode-aware default for an unrecognized scope', () => {
    const sp = new URLSearchParams('scope=bogus')
    expect(parseFilterParams(sp, me({ authMode: 'org' }), departments).scope).toBe('shared')
  })

  it('neutralizes an unrecognized visibility to "all"', () => {
    const sp = new URLSearchParams('vis=bogus')
    expect(parseFilterParams(sp, me(), departments).vis).toBe('all')
  })

  it('neutralizes a dept that is not in the derived department set (stale bookmark footgun)', () => {
    // A bookmarked ?dept=legal where "legal" no longer exists must NOT strand the
    // table in an empty state — it neutralizes to "" (All departments).
    const sp = new URLSearchParams('dept=legal')
    expect(parseFilterParams(sp, me(), departments).dept).toBe('')
  })

  it('keeps a dept that is present in the derived department set', () => {
    const sp = new URLSearchParams('dept=marketing')
    expect(parseFilterParams(sp, me(), departments).dept).toBe('marketing')
  })

  it('defaults to empty params when the URL is empty', () => {
    const got = parseFilterParams(new URLSearchParams(''), me({ authMode: 'local' }), departments)
    expect(got).toEqual({ scope: 'all', vis: 'all', dept: '', search: '' })
  })
})
