import { act, renderHook, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import type { GraphState, Instance, NodeExecution } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'
import { useRunnerMarksByOwner } from './useRunnerMarks'

const mocks = vi.hoisted(() => ({ get: vi.fn(), getTrace: vi.fn(), onEvent: (_e: { type: string }) => {} }))
vi.mock('../api/client', () => ({ api: { instances: mocks } }))
vi.mock('./useInstanceWebSocket', () => ({ useInstanceWebSocket: (_id: unknown, onEvent: typeof mocks.onEvent) => { mocks.onEvent = onEvent; return { connected: false } } }))
const def: WorkflowDefinition = { name: 'wf', description: '', author: '', nodes: [{ id: 'a', type: 'llm_call', label: 'A', description: '', config: {} }], edges: [] }
const root = { id: 'root' } as Instance
const trace = (owner: string, runnerId: string): NodeExecution[] => [{ id: owner, instanceId: owner, nodeId: 'a', attempt: 1, status: 'succeeded', startedAt: '', runnerId }]
const rootTrace = trace('root', 'builtin')
const graph: GraphState = { rootInstanceId: 'root', version: '1', active: true, groups: [], contexts: [
  { instanceId: 'branch', parentInstanceId: 'root', kind: 'branch', state: 'running', blocking: true, redacted: false, nodes: [] },
  { instanceId: 'child', parentInstanceId: 'root', kind: 'workflow', graph: def as unknown as Record<string, unknown>, state: 'running', blocking: true, redacted: false, nodes: [] },
  { instanceId: 'secret', kind: 'workflow', state: 'unknown', blocking: true, redacted: true, nodes: [] },
] }
beforeEach(() => {
  vi.clearAllMocks()
  mocks.get.mockImplementation(async (id: string) => ({ id, workflowSnapshot: def }))
  mocks.getTrace.mockImplementation(async (id: string) => trace(id, `${id}-runner`))
})
it('loads each visible owner and reuses root data without fetching redacted traces', async () => {
  const { result } = renderHook(() => useRunnerMarksByOwner(root, def, rootTrace, graph))
  await waitFor(() => expect(result.current.marks.child?.nodes.a.runnerId).toBe('child-runner'))
  expect(result.current.marks.branch.nodes.a.runnerId).toBe('branch-runner')
  expect(result.current.marks.root.nodes.a.runnerId).toBe('builtin')
  expect(mocks.getTrace.mock.calls.map(c => c[0]).sort()).toEqual(['branch', 'child'])
  expect(mocks.get.mock.calls.map(c => c[0]).sort()).toEqual(['branch', 'child'])
})
it('clears failed owners and ignores late reads after route or visibility changes', async () => {
  const { result, rerender } = renderHook(({ state }) => useRunnerMarksByOwner(root, def, rootTrace, state), { initialProps: { state: graph } })
  await waitFor(() => expect(result.current.marks.child).toBeDefined())
  mocks.getTrace.mockImplementation(async (id: string) => { if (id === 'child') throw new Error('API 403: forbidden'); return trace(id, 'new') })
  rerender({ state: { ...graph, version: '2' } })
  await waitFor(() => expect(result.current.marks.child).toBeUndefined())
  await waitFor(() => expect(result.current.errors.child).toContain('403'))
  let finish!: (t: NodeExecution[]) => void
  mocks.getTrace.mockImplementation(() => new Promise<NodeExecution[]>(r => { finish = r }))
  rerender({ state: { ...graph, version: '3', contexts: [graph.contexts[1]] } })
  await waitFor(() => expect(mocks.getTrace).toHaveBeenCalledTimes(5))
  rerender({ state: { ...graph, version: '4', contexts: [] } })
  await act(async () => { finish(trace('child', 'obsolete')) })
  expect(result.current.marks.child).toBeUndefined()
  expect(result.current.errors.child).toBeUndefined()
})
it('uses the owning child definition for a nested branch, never the root definition', async () => {
  const childDef = { ...def, nodes: [{ ...def.nodes[0], id: 'child-only' }] }
  const state = { ...graph, contexts: [
    { ...graph.contexts[1], graph: childDef as unknown as Record<string, unknown> },
    { ...graph.contexts[0], parentInstanceId: 'child' },
  ] }
  mocks.get.mockImplementation(async (id: string) => ({ id }))
  mocks.getTrace.mockImplementation(async (id: string) => [{ ...trace(id, 'nested')[0], nodeId: 'child-only' }])
  const { result } = renderHook(() => useRunnerMarksByOwner(root, def, rootTrace, state))
  await waitFor(() => expect(result.current.marks.branch?.nodes['child-only']?.runnerId).toBe('nested'))
  expect(result.current.marks.branch.nodes.a).toBeUndefined()
})
it('refreshes owner marks on placement events and bounds concurrent owner reads', async () => {
  let release!: () => void
  const gate = new Promise<void>(r => { release = r })
  mocks.getTrace.mockImplementation(async (id: string) => { await gate; return trace(id, 'first') })
  const many = { ...graph, contexts: Array.from({ length: 6 }, (_, i) => ({ ...graph.contexts[0], instanceId: `b${i}` })) }
  const { result } = renderHook(() => useRunnerMarksByOwner(root, def, rootTrace, many))
  await act(async () => {})
  expect(mocks.getTrace).toHaveBeenCalledTimes(2)
  await act(async () => { release() })
  await waitFor(() => expect(result.current.marks.b5?.nodes.a.runnerId).toBe('first'))
  mocks.getTrace.mockImplementation(async (id: string) => trace(id, 'updated'))
  act(() => mocks.onEvent({ type: 'runner.placement' }))
  await waitFor(() => expect(result.current.marks.b5?.nodes.a.runnerId).toBe('updated'))
})
