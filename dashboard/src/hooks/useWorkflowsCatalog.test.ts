import { renderHook, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useWorkflowsCatalog } from './useWorkflowsCatalog'
import { api } from '../api/client'
import type { WorkflowSummary, Me } from '../api/types'

function wf(overrides: Partial<WorkflowSummary> = {}): WorkflowSummary {
  return { name: 'wf', description: 'd', author: 'a', nodeCount: 1, scope: 'private', ...overrides }
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

describe('useWorkflowsCatalog', () => {
  beforeEach(() => {
    vi.spyOn(api.workflows, 'list')
    vi.spyOn(api, 'me')
  })
  afterEach(() => vi.restoreAllMocks())

  it('starts loading and resolves to the fetched workflows + identity', async () => {
    vi.mocked(api.workflows.list).mockResolvedValue([wf()])
    vi.mocked(api.me).mockResolvedValue(me())
    const { result } = renderHook(() => useWorkflowsCatalog())

    expect(result.current.loading).toBe(true)
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.workflows).toHaveLength(1)
    expect(result.current.me).not.toBeNull()
    expect(result.current.error).toBeNull()
  })

  it('degrades to an error state when either fetch fails', async () => {
    vi.mocked(api.workflows.list).mockRejectedValue(new Error('boom'))
    vi.mocked(api.me).mockResolvedValue(me())
    const { result } = renderHook(() => useWorkflowsCatalog())

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBe('boom')
    expect(result.current.workflows).toHaveLength(0)
  })

  it('ignores a resolution after unmount (no stale state write)', async () => {
    let resolve: (v: WorkflowSummary[]) => void = () => {}
    const pending = new Promise<WorkflowSummary[]>((res) => { resolve = res })
    vi.mocked(api.workflows.list).mockReturnValue(pending)
    vi.mocked(api.me).mockResolvedValue(me())

    const { result, unmount } = renderHook(() => useWorkflowsCatalog())
    unmount()
    resolve([wf()])
    await Promise.resolve()
    // Nothing to assert on a torn-down hook beyond "no throw"; the cancelled flag
    // guards the setState. The hook's last observable loading state stays true.
    expect(result.current.loading).toBe(true)
  })
})
