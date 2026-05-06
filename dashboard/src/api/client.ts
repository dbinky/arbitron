import type {
  Instance,
  InstanceSummary,
  InstanceState,
  NodeExecution,
  Priority,
  QueueConfigUpdate,
  SystemConfig,
  VariableSnapshot,
  WorkflowSummary,
} from './types'

/** Parameters for listing instances. */
export interface ListInstancesParams {
  state?: InstanceState
  workflowName?: string
  limit?: number
  offset?: number
}

/** Throws on non-2xx responses with a descriptive message. */
async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init)
  if (!res.ok) {
    let detail = res.statusText
    try {
      const body = (await res.json()) as { message?: string }
      if (body.message) detail = body.message
    } catch {
      // ignore parse errors
    }
    throw new Error(`API ${res.status}: ${detail}`)
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
  instances: {
    /** List instances with optional filters. */
    list(params?: ListInstancesParams): Promise<InstanceSummary[]> {
      const q = new URLSearchParams()
      if (params?.state) q.set('state', params.state)
      if (params?.workflowName) q.set('workflowName', params.workflowName)
      if (params?.limit != null) q.set('limit', String(params.limit))
      if (params?.offset != null) q.set('offset', String(params.offset))
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

    /** Get variable snapshot history for an instance. */
    getVariableHistory(id: string): Promise<VariableSnapshot[]> {
      return request(`/api/v1/instances/${id}/variables/history`)
    },
  },

  workflows: {
    /** List all available workflows. */
    list(): Promise<WorkflowSummary[]> {
      return request('/api/v1/workflows')
    },

    /** Get a workflow definition by name. */
    get(name: string): Promise<unknown> {
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
  },

  system: {
    /** Get system configuration. */
    getConfig(): Promise<SystemConfig> {
      return request('/api/v1/system/config')
    },

    /** Update queue configuration. */
    updateQueueConfig(globalCap: number, perUserCap: number): Promise<QueueConfigUpdate> {
      return put('/api/v1/system/config/queue', { globalCap, perUserCap })
    },
  },
} as const
