import { describe, expect, it } from 'vitest'
import type { ExecutionContext, GraphState, GroupGraph } from '../api/types'
import { nestedContexts } from './nestedGraphs'

const grouped: GroupGraph = { groups: {}, owner: {}, order: ['g'] }
const graph = (nodes: unknown[] = [{ id: 'start', type: 'start' }]) => ({ nodes, edges: [] }) as Record<string, unknown>
const ctx = (instanceId: string, over: Partial<ExecutionContext> = {}): ExecutionContext => ({
  instanceId, parentInstanceId: 'root', kind: 'workflow', state: 'running', blocking: false, redacted: false,
  nodes: [], graph: graph(), layout: grouped, ...over,
})
const state = (...contexts: ExecutionContext[]) => ({ rootInstanceId: 'root', contexts }) as unknown as GraphState
const ids = (gs: GraphState) => nestedContexts(gs).map(c => c.instanceId)

describe('nestedContexts', () => {
  it('keeps visible workflow and spawn children with a grouped graph, in context order', () => {
    expect(ids(state(ctx('root', { kind: 'root', parentInstanceId: undefined }), ctx('w'), ctx('s', { kind: 'spawn' })))).toEqual(['w', 's'])
  })

  it.each([
    ['a branch context, even when it carries a graph', ctx('x', { kind: 'branch' })],
    ['a redacted context, even when it carries a graph', ctx('x', { redacted: true })],
    ['an empty graph', ctx('x', { graph: graph([]) })],
    ['a missing graph', ctx('x', { graph: undefined })],
    ['a malformed graph', ctx('x', { graph: { nodes: 'no' } })],
    ['no ownership layout', ctx('x', { layout: undefined })],
    ['an ungrouped layout', ctx('x', { layout: { groups: {}, owner: {}, order: [] } })],
    ['the root instance itself', ctx('root', { parentInstanceId: undefined })],
  ])('drops %s, so no tile and no layout GET is derived', (_name, c) => {
    expect(ids(state(c, ctx('keep')))).toEqual(['keep'])
  })
})
