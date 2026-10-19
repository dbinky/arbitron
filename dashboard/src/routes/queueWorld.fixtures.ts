import type { EnrichedInstanceSummary, GraphState, Me, WebSocketEvent } from '../api/types'
import { CHILD_PAGE_LIMIT } from '../hooks/useRunHierarchy'

/** Every argument api.instances.list can receive from the queue. */
export interface ListArgs {
  state?: string
  since?: string
  parent?: string
  limit?: number
  offset?: number
  includeDismissed?: boolean
  activeDescendantAncestors?: boolean
}

/** What the server would answer, per request shape. */
export interface QueueWorld {
  /** Ordinary per-state queue lists (running/waiting/queued/paused/failed/completed). */
  byState: Record<string, EnrichedInstanceSummary[]>
  /** The ancestor-discovery answer, or the error it fails with. */
  ancestors: EnrichedInstanceSummary[] | Error
  /** EVERY child of a parent; served one CHILD_PAGE_LIMIT page at a time. */
  children: Record<string, EnrichedInstanceSummary[]>
  /** graph-state by instance id; an absent id is a 404 (hidden or missing). */
  graphStates: Record<string, GraphState | Error>
}

export const newWorld = (): QueueWorld => ({ byState: {}, ancestors: [], children: {}, graphStates: {} })

export const localAdmin: Me = {
  userId: 'u1',
  username: 'local',
  role: 'admin',
  departments: ['default'],
  ledDepartments: [],
  isAdmin: true,
  authMode: 'local',
}

const notFound = () => Promise.reject(new Error('API 404: instance not found'))

/**
 * The list endpoint keyed on EVERY load-bearing argument: an ancestor-discovery
 * request must carry exactly the narrowing (no state, no parent, dismissed
 * included); a child page must have the hierarchy's page shape and is cut by
 * offset/limit. A caller that drops or mangles any of them gets a wrong
 * answer, never a lucky one.
 */
export function listFrom(world: QueueWorld) {
  return (p: ListArgs = {}): Promise<EnrichedInstanceSummary[]> => {
    if (p.activeDescendantAncestors !== undefined) {
      if (p.activeDescendantAncestors !== true || p.state || p.parent || p.includeDismissed !== true) {
        return Promise.reject(new Error('API 400: unexpected ancestor-discovery scope'))
      }
      if (world.ancestors instanceof Error) return Promise.reject(world.ancestors)
      const from = p.offset ?? 0
      return Promise.resolve(world.ancestors.slice(from, from + (p.limit ?? 500)))
    }
    if (p.parent) {
      const all = world.children[p.parent]
      if (!all) return notFound()
      if (p.limit !== CHILD_PAGE_LIMIT || p.includeDismissed !== true) {
        return Promise.reject(new Error('API 400: unexpected child page shape'))
      }
      const from = p.offset ?? 0
      return Promise.resolve(all.slice(from, from + CHILD_PAGE_LIMIT))
    }
    return Promise.resolve(p.state ? world.byState[p.state] ?? [] : [])
  }
}

/** graph-state keyed by instance id: any other id is a 404, so a pane fed the wrong id shows nothing. */
export function graphStateFrom(world: QueueWorld) {
  return (id: string): Promise<GraphState> => {
    const v = world.graphStates[id]
    if (v === undefined) return notFound()
    return v instanceof Error ? Promise.reject(v) : Promise.resolve(v)
  }
}

/** Every WebSocket a view opened, with a way to push a frame into it. */
export const sockets: Array<{ url: string; emit: (e: WebSocketEvent) => void }> = []

/** Browser WebSocket stand-in (install with vi.stubGlobal('WebSocket', StubSocket)). */
export class StubSocket {
  static readonly OPEN = 1
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((m: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  constructor(readonly url: string) {
    sockets.push({ url, emit: (e) => this.onmessage?.({ data: JSON.stringify(e) }) })
  }
  addEventListener() {}
  send() {}
  close() {}
}
