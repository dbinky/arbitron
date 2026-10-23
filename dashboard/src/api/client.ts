import type {
  DashboardStats,
  EnrichedInstanceSummary,
  GraphState,
  GroupGraph,
  GroupProgress,
  GroupScope,
  Instance,
  InstanceState,
  Me,
  NodeExecution,
  NotificationListResponse,
  Priority,
  QueueConfigUpdate,
  Schedule,
  SystemConfig,
  TeamsConfigUpdate,
  WorkflowStats,
  WorkflowSummary,
} from './types'
import type { WorkflowDefinition } from '../graph/types'
import type { LayoutView, RetryAck } from '../graph/arranged/types'

export interface SystemSettings {
  catalogUrl: string
  teamsWebhookUrl: string
  teamsConfigured: boolean
}

export interface SystemSettingsPatch {
  catalogUrl?: string
  teamsWebhookUrl?: string
}

/** Parameters for listing instances. */
export interface ListInstancesParams {
  state?: InstanceState
  workflowName?: string
  since?: string
  limit?: number
  offset?: number
  includeDismissed?: boolean
  /**
   * Narrow by position in the spawn hierarchy: omit for all runs, 'roots' for
   * top-level runs only, or an instance UUID for that run's children. The
   * server treats this as a NARROWING that composes with — and can never
   * replace — the visibility scope it derives from the request identity.
   */
  parent?: string
  /**
   * Narrow to terminal ancestors of visible active descendants, including
   * completed parents that still own independent work. Composes with parent
   * and the server-set visibility scope.
   */
  activeDescendantAncestors?: boolean
}

const EMPTY_PROGRESS: GroupProgress = { total: 0, completed: 0, failed: 0, cancelled: 0, active: 0, notStarted: 0 }

/** Normalizes Go nil collections once, at the API boundary. */
export function normalizeGroupGraph(raw: GroupGraph | null | undefined): GroupGraph {
  const groups: Record<string, GroupScope> = {}
  for (const [id, scope] of Object.entries(raw?.groups ?? {})) {
    if (!scope) continue
    groups[id] = {
      ...scope,
      config: { ...scope.config, tasks: scope.config?.tasks ?? [] },
      bodies: scope.bodies ?? {},
      nested: scope.nested ?? [],
    }
  }
  return { groups, owner: raw?.owner ?? {}, order: raw?.order ?? [] }
}

/** Normalizes a Go-encoded aggregate (nil slices/maps become empty). */
export function normalizeGraphState(raw: GraphState): GraphState {
  return {
    ...raw,
    contexts: (raw.contexts ?? []).map((c) => ({
      ...c,
      nodes: c.nodes ?? [],
      layout: c.layout ? normalizeGroupGraph(c.layout) : undefined,
    })),
    groups: (raw.groups ?? []).map((g) => ({
      ...g,
      tasks: g.tasks ?? [],
      waitingReasons: g.waitingReasons ?? [],
      progress: g.progress ?? EMPTY_PROGRESS,
    })),
    layout: raw.layout ? normalizeGroupGraph(raw.layout) : undefined,
    warnings: raw.warnings ?? [],
  }
}

/** Throws on non-2xx responses with a descriptive message. */
async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init)
  if (!res.ok) {
    let detail = res.statusText
    try {
      const body: unknown = await res.json()
      if (typeof body === 'object' && body !== null) {
        const { error, message } = body as { error?: unknown; message?: unknown }
        if (typeof error === 'string' && error.trim()) detail = error
        else if (typeof message === 'string' && message.trim()) detail = message
      }
    } catch {
      // ignore parse errors
    }
    throw Object.assign(new Error(`API ${res.status}: ${detail}`), { status: res.status })
  }
  return res.json() as Promise<T>
}

function post<T>(url: string, body?: unknown): Promise<T> {
  return request<T>(url, {
    method: 'POST',
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  })
}

function put<T>(url: string, body: unknown): Promise<T> {
  return request<T>(url, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

/** Typed REST API client for the Arbitron backend. */
export const api = {
  /** Get the authenticated caller's identity projection. */
  me(): Promise<Me> {
    return request('/api/v1/me')
  },

  dashboard: {
    /**
     * Get department-scoped operational stats. Pass `departments` = null to use
     * the caller's union default (the backend clamps to the caller's allowed
     * set regardless). `window` is a Go duration string, e.g. "24h" or "6h".
     */
    stats(departments: string[] | null, window: string): Promise<DashboardStats> {
      const q = new URLSearchParams()
      if (departments && departments.length > 0) q.set('departments', departments.join(','))
      q.set('window', window)
      return request(`/api/v1/dashboard/stats?${q.toString()}`)
    },
  },

  instances: {
    /** List instances with optional filters. */
    list(params?: ListInstancesParams): Promise<EnrichedInstanceSummary[]> {
      const q = new URLSearchParams()
      if (params?.state) q.set('state', params.state)
      if (params?.workflowName) q.set('workflowName', params.workflowName)
      if (params?.since) q.set('since', params.since)
      if (params?.limit != null) q.set('limit', String(params.limit))
      if (params?.offset != null) q.set('offset', String(params.offset))
      if (params?.includeDismissed) q.set('includeDismissed', 'true')
      if (params?.parent) q.set('parent', params.parent)
      if (params?.activeDescendantAncestors) q.set('activeDescendantAncestors', 'true')
      const qs = q.toString()
      return request(`/api/v1/instances${qs ? `?${qs}` : ''}`)
    },

    /** Get a single instance by ID. */
    get(id: string): Promise<Instance> {
      return request(`/api/v1/instances/${id}`)
    },

    /** Pause a running instance. */
    pause(id: string): Promise<{ status: string }> {
      return post(`/api/v1/instances/${id}/pause`)
    },

    /** Resume a paused instance. */
    resume(id: string): Promise<{ status: string }> {
      return post(`/api/v1/instances/${id}/resume`)
    },

    /** Cancel an instance. */
    cancel(id: string): Promise<{ status: string }> {
      return post(`/api/v1/instances/${id}/cancel`)
    },

    /** Retry a failed instance with optional variable edits. */
    retry(id: string, variables?: Record<string, unknown>): Promise<{ status: string }> {
      return post(`/api/v1/instances/${id}/retry`, variables ? { variables } : undefined)
    },

    /** Rerun a completed/failed instance, creating a new instance. */
    rerun(id: string): Promise<{ instanceId: string; state: string }> {
      return post(`/api/v1/instances/${id}/rerun`)
    },

    /** Dismiss a terminal instance from the acting user's view (per user). */
    dismiss(id: string): Promise<{ status: string }> {
      return post(`/api/v1/instances/${id}/dismiss`)
    },

    /** Restore a previously dismissed instance for the acting user. */
    restore(id: string): Promise<{ status: string }> {
      return post(`/api/v1/instances/${id}/restore`)
    },

    /**
     * Dismiss every currently listed instance in a terminal category. Pass the
     * same `since` window the view uses (Failed/Completed are windowed to
     * `daysAgo(5)`) so the backend dismisses exactly what the user sees, matching
     * the spec's "every item currently listed in that category." Omitting `since`
     * dismisses the full server-side window for that state (up to the list limit).
     */
    dismissAll(state: 'failed' | 'completed', since?: string): Promise<{ status: string }> {
      const q = new URLSearchParams({ state })
      if (since) q.set('since', since)
      return post(`/api/v1/instances/dismiss-all?${q.toString()}`)
    },

    /** Get the execution trace for an instance. */
    getTrace(id: string): Promise<NodeExecution[]> {
      return request(`/api/v1/instances/${id}/trace`)
    },

    /** Get a specific node execution within an instance trace. */
    getNodeTrace(id: string, nodeId: string): Promise<NodeExecution> {
      return request(`/api/v1/instances/${id}/trace/${nodeId}`)
    },

    /** Get current variables for an instance. */
    getVariables(id: string): Promise<Record<string, unknown>> {
      return request(`/api/v1/instances/${id}/variables`)
    },

    /** Read arrangement status for the instance's immutable snapshot revision. */
    getLayout(id: string, signal?: AbortSignal): Promise<LayoutView> {
      return request(`/api/v1/instances/${id}/layout`, signal ? { signal } : undefined)
    },

    /** Acknowledge an asynchronous retry of the expected snapshot revision. */
    retryLayout(id: string, expectedRevision: string): Promise<RetryAck> {
      return post(`/api/v1/instances/${id}/layout/retry`, { expectedRevision })
    },

    /** Authorized aggregate of this instance and its visible descendants. */
    graphState(id: string, signal?: AbortSignal): Promise<GraphState> {
      return request<GraphState>(`/api/v1/instances/${id}/graph-state`, signal ? { signal } : undefined).then(normalizeGraphState)
    },
  },

  workflows: {
    /** List all available workflows. */
    list(): Promise<WorkflowSummary[]> {
      return request('/api/v1/workflows')
    },

    /** Get a workflow definition by name. */
    get(name: string): Promise<WorkflowDefinition> {
      return request(`/api/v1/workflows/${name}`)
    },

    /** Trigger a new workflow instance. */
    trigger(
      name: string,
      variables?: Record<string, unknown>,
      priority?: Priority,
    ): Promise<{ instanceId: string; state: string }> {
      return post(`/api/v1/workflows/${name}/trigger`, {
        variables: variables ?? {},
        priority: priority ?? 'normal',
      })
    },

    /** Read arrangement status for the workflow's current saved revision. */
    getLayout(name: string, signal?: AbortSignal): Promise<LayoutView> {
      return request(`/api/v1/workflows/${name}/layout`, signal ? { signal } : undefined)
    },

    /** Acknowledge a retry; a concurrent authoring save makes the token stale. */
    retryLayout(name: string, expectedRevision: string): Promise<RetryAck> {
      return post(`/api/v1/workflows/${name}/layout/retry`, { expectedRevision })
    },

    /** Static group ownership analysis of a workflow definition. */
    groupLayout(name: string, signal?: AbortSignal): Promise<GroupGraph> {
      return request<GroupGraph>(`/api/v1/workflows/${name}/group-layout`, signal ? { signal } : undefined).then(normalizeGroupGraph)
    },

    /** Get usage stats for a workflow over a date range. */
    getStats(name: string, from: string, to: string): Promise<WorkflowStats> {
      return request(
        `/api/v1/workflows/${name}/stats?from=${from}&to=${to}`,
      )
    },
  },

  system: {
    getSettings(): Promise<SystemSettings> {
      return request('/api/v1/system/settings')
    },
    updateSettings(patch: SystemSettingsPatch): Promise<SystemSettings> {
      return put('/api/v1/system/settings', patch)
    },
    /** Get system configuration. */
    getConfig(): Promise<SystemConfig> {
      return request('/api/v1/system/config')
    },

    /** Update queue configuration. */
    updateQueueConfig(globalCap: number, perUserCap: number): Promise<QueueConfigUpdate> {
      return put('/api/v1/system/config/queue', { globalCap, perUserCap })
    },

    /**
     * Update Teams notification config. Omitting `webhookUrl` leaves the stored
     * (redacted) secret unchanged; passing a value replaces it.
     */
    updateTeamsConfig(body: { webhookUrl?: string; enabled?: boolean }): Promise<TeamsConfigUpdate> {
      return put('/api/v1/system/config/teams', body)
    },
  },
  schedules: {
    /**
     * List schedules, optionally narrowed to one workflow. READ-ONLY: the
     * dashboard exposes no schedule mutation — create/edit/enable/disable/
     * delete/run-now stay on REST + MCP. The backend wraps the array in
     * { schedules: [...] } and scopes results to the caller's own (or, for an
     * admin, all) schedules.
     */
    async list(workflowName?: string): Promise<Schedule[]> {
      const q = new URLSearchParams()
      if (workflowName) q.set('workflowName', workflowName)
      const qs = q.toString()
      const res = await request<{ schedules: Schedule[] }>(
        `/api/v1/schedules${qs ? `?${qs}` : ''}`,
      )
      return res.schedules
    },
  },
  notifications: {
    /** List notifications with optional filters. */
    list(params: { dismissed?: boolean; limit?: number; offset?: number } = {}): Promise<NotificationListResponse> {
      const qs = new URLSearchParams()
      if (params.dismissed != null) qs.set('dismissed', String(params.dismissed))
      if (params.limit != null) qs.set('limit', String(params.limit))
      if (params.offset != null) qs.set('offset', String(params.offset))
      const query = qs.toString()
      return request(`/api/v1/notifications${query ? '?' + query : ''}`)
    },

    /** Get the count of unread notifications. */
    unreadCount(): Promise<{ count: number }> {
      return request('/api/v1/notifications/unread-count')
    },

    /** Mark all notifications as read. */
    markRead(): Promise<{ status: string }> {
      return post('/api/v1/notifications/mark-read')
    },

    /** Dismiss a single notification. */
    dismiss(id: string): Promise<void> {
      return post(`/api/v1/notifications/${id}/dismiss`)
    },
  },
} as const
