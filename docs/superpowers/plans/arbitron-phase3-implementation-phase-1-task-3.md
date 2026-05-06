# Phase 3.1 Task 3: API Client and WebSocket Hook

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the REST API client and system-wide WebSocket hook that the Queue View and other pages consume.

**Architecture:** A thin fetch-based API client with typed responses. A `useSystemWebSocket` hook that connects to `GET /api/v1/ws` for system-wide events (instance state changes). Automatic reconnection on disconnect.

**Tech Stack:** React, TypeScript, native fetch, native WebSocket

---

### Task 3.1: API types

**Files:**
- Create: `dashboard/src/api/types.ts`

- [ ] **Step 1: Define shared API types**

```ts
export type InstanceState =
  | 'queued'
  | 'running'
  | 'waiting'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'paused'

export type Priority = 'high' | 'normal' | 'low'

export interface InstanceSummary {
  id: string
  workflowName: string
  state: InstanceState
  priority: Priority
  currentNodeId?: string
  createdAt: string
  updatedAt: string
  completedAt?: string
  queuedAt?: string
}

export interface Instance {
  id: string
  ownerId: string
  workflowName: string
  state: InstanceState
  priority: Priority
  variables: Record<string, unknown>
  originalVariables: Record<string, unknown>
  workflowSnapshot?: Record<string, unknown>
  currentNodeId?: string
  failedNodeId?: string
  createdAt: string
  updatedAt: string
  completedAt?: string
  queuedAt?: string
  waitingUntil?: string
  webhookUrl?: string
  webhookReceived?: boolean
}

export interface WorkflowSummary {
  name: string
  description: string
  author: string
  nodeCount?: number
}

export interface SystemConfig {
  server: { host: string; port: number }
  database: { connected: boolean }
  workspace: { path: string }
  llm: { provider: string; apiKey: string }
  logging: { level: string; file: string }
  tracing: { enabled: boolean; exportDir: string }
  queue: {
    globalCap: number
    perUserCap: number
    runningCount: number
    queuedCount: number
    waitingCount: number
  }
}

export interface WebSocketEvent {
  type: string
  timestamp: string
  data: Record<string, unknown>
}
```

- [ ] **Step 2: Commit**

```bash
git add dashboard/src/api/types.ts
git commit -m "feat(dashboard): add API type definitions"
```

---

### Task 3.2: REST API client

**Files:**
- Create: `dashboard/src/api/client.ts`

- [ ] **Step 1: Create the API client**

```ts
import type { InstanceSummary, Instance, WorkflowSummary, SystemConfig } from './types'

const BASE = '/api/v1'

async function fetchJSON<T>(url: string): Promise<T> {
  const res = await fetch(url)
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new Error(body.error || `HTTP ${res.status}`)
  }
  return res.json()
}

async function postJSON<T>(url: string, body?: unknown): Promise<T> {
  const res = await fetch(url, {
    method: 'POST',
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  })
  if (!res.ok) {
    const respBody = await res.json().catch(() => ({}))
    throw new Error(respBody.error || `HTTP ${res.status}`)
  }
  return res.json()
}

async function putJSON<T>(url: string, body: unknown): Promise<T> {
  const res = await fetch(url, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const respBody = await res.json().catch(() => ({}))
    throw new Error(respBody.error || `HTTP ${res.status}`)
  }
  return res.json()
}

export const api = {
  instances: {
    list(params: { state?: string; since?: string; limit?: number; offset?: number } = {}): Promise<InstanceSummary[]> {
      const qs = new URLSearchParams()
      if (params.state) qs.set('state', params.state)
      if (params.since) qs.set('since', params.since)
      if (params.limit) qs.set('limit', String(params.limit))
      if (params.offset) qs.set('offset', String(params.offset))
      const query = qs.toString()
      return fetchJSON(`${BASE}/instances${query ? '?' + query : ''}`)
    },

    get(id: string): Promise<Instance> {
      return fetchJSON(`${BASE}/instances/${id}`)
    },

    pause(id: string) { return postJSON(`${BASE}/instances/${id}/pause`) },
    resume(id: string) { return postJSON(`${BASE}/instances/${id}/resume`) },
    cancel(id: string) { return postJSON(`${BASE}/instances/${id}/cancel`) },
    retry(id: string, variables?: Record<string, unknown>) {
      return postJSON(`${BASE}/instances/${id}/retry`, variables ? { variables } : undefined)
    },
    rerun(id: string) { return postJSON<{ instanceId: string }>(`${BASE}/instances/${id}/rerun`) },
    setPriority(id: string, priority: string) {
      return postJSON(`${BASE}/instances/${id}/priority`, { priority })
    },

    getTrace(id: string) { return fetchJSON(`${BASE}/instances/${id}/trace`) },
    getNodeTrace(id: string, nodeId: string) { return fetchJSON(`${BASE}/instances/${id}/trace/${nodeId}`) },
    getVariables(id: string) { return fetchJSON<Record<string, unknown>>(`${BASE}/instances/${id}/variables`) },
    getVariableHistory(id: string) { return fetchJSON(`${BASE}/instances/${id}/variables/history`) },
  },

  workflows: {
    list(): Promise<WorkflowSummary[]> {
      return fetchJSON(`${BASE}/workflows`)
    },

    get(name: string) {
      return fetchJSON(`${BASE}/workflows/${name}`)
    },

    trigger(name: string, variables?: Record<string, unknown>, priority?: string) {
      return postJSON<{ instanceId: string; state: string }>(`${BASE}/workflows/${name}/trigger`, {
        variables: variables || {},
        priority: priority || 'normal',
      })
    },
  },

  system: {
    getConfig(): Promise<SystemConfig> {
      return fetchJSON(`${BASE}/system/config`)
    },

    updateQueueConfig(globalCap?: number, perUserCap?: number) {
      return putJSON(`${BASE}/system/config/queue`, { globalCap, perUserCap })
    },
  },
}
```

- [ ] **Step 2: Commit**

```bash
git add dashboard/src/api/client.ts
git commit -m "feat(dashboard): add REST API client"
```

---

### Task 3.3: System-wide WebSocket hook

**Files:**
- Create: `dashboard/src/hooks/useSystemWebSocket.ts`

- [ ] **Step 1: Create the WebSocket hook**

```ts
import { useEffect, useRef, useCallback, useState } from 'react'
import type { WebSocketEvent } from '../api/types'

type EventHandler = (event: WebSocketEvent) => void

export function useSystemWebSocket(onEvent: EventHandler) {
  const wsRef = useRef<WebSocket | null>(null)
  const onEventRef = useRef(onEvent)
  const [connected, setConnected] = useState(false)

  onEventRef.current = onEvent

  const connect = useCallback(() => {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${protocol}//${window.location.host}/api/v1/ws`
    const ws = new WebSocket(url)

    ws.onopen = () => setConnected(true)

    ws.onmessage = (msg) => {
      try {
        const event: WebSocketEvent = JSON.parse(msg.data)
        onEventRef.current(event)
      } catch {
        // ignore malformed messages
      }
    }

    ws.onclose = () => {
      setConnected(false)
      setTimeout(connect, 3000)
    }

    ws.onerror = () => {
      ws.close()
    }

    wsRef.current = ws
  }, [])

  useEffect(() => {
    connect()
    return () => {
      wsRef.current?.close()
    }
  }, [connect])

  return { connected }
}
```

- [ ] **Step 2: Commit**

```bash
git add dashboard/src/hooks/useSystemWebSocket.ts
git commit -m "feat(dashboard): add system-wide WebSocket hook with auto-reconnect"
```
