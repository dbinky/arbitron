import { useCallback, useEffect, useMemo, useState } from 'react'
import { api } from '../api/client'
import type { GraphState, Instance, NodeExecution, WebSocketEvent } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'
import { buildRunnerMarks, type RunnerMarks, type RunnerMarksByOwner } from '../graph/runnerMarks'
import { useInstanceWebSocket } from './useInstanceWebSocket'
import { useRefetchOnReconnect } from './useRefetchOnReconnect'

// Each owner reads its instance and trace in parallel (at most four requests).
const OWNER_READ_CONCURRENCY = 2

/** Authorized context/refresh epochs fence both cached marks and late reads. */
export function useRunnerMarksByOwner(instance: Instance | null, definition: WorkflowDefinition | null, trace: NodeExecution[], graph: GraphState | null) {
  const [epoch, setEpoch] = useState(0)
  const refresh = useCallback(() => setEpoch(e => e + 1), [])
  const onEvent = useCallback((event: WebSocketEvent) => {
    if (/^(node|instance|group|runner|workspace|session|interactive)\./.test(event.type) && event.type !== 'interactive.token') refresh()
  }, [refresh])
  const { connected } = useInstanceWebSocket(instance?.id, onEvent, { subtree: true })
  useRefetchOnReconnect(connected, refresh)
  const rootId = instance?.id
  const scope = useMemo(() => ({ rootId, graph, definition, epoch }), [rootId, graph, definition, epoch])
  const [loaded, setLoaded] = useState<{ scope: typeof scope; marks: RunnerMarksByOwner; errors: Record<string, string> } | null>(null)
  useEffect(() => {
    if (!scope.rootId) return
    let cancelled = false
    const marks: Record<string, RunnerMarks> = {}
    const errors: Record<string, string> = {}
    const contexts = (scope.graph?.contexts ?? []).filter(c => !c.redacted && c.instanceId !== scope.rootId)
    const inheritedDefinition = (id: string | undefined, seen = new Set<string>()): WorkflowDefinition | null => {
      if (id === scope.rootId) return scope.definition
      if (!id || seen.has(id)) return null
      seen.add(id)
      const context = contexts.find(c => c.instanceId === id)
      if (context?.graph) return context.graph as unknown as WorkflowDefinition
      return context?.kind === 'branch' ? inheritedDefinition(context.parentInstanceId, seen) : null
    }
    let cursor = 0
    const worker = async () => {
      while (!cancelled && cursor < contexts.length) {
        const c = contexts[cursor++]
        try {
          const [owner, executions] = await Promise.all([api.instances.get(c.instanceId), api.instances.getTrace(c.instanceId)])
          const def = (c.graph ?? owner.workflowSnapshot ?? inheritedDefinition(c.instanceId)) as unknown as WorkflowDefinition | null
          if (!def) throw new Error('Owning workflow definition unavailable')
          marks[c.instanceId] = buildRunnerMarks(c.instanceId, def, executions, owner.workspaceMoves)
        } catch (error) {
          errors[c.instanceId] = error instanceof Error ? error.message : 'Runner placement unavailable'
        }
      }
    }
    void Promise.all(Array.from({ length: Math.min(OWNER_READ_CONCURRENCY, contexts.length) }, worker))
      .then(() => { if (!cancelled) setLoaded({ scope, marks, errors }) })
    return () => { cancelled = true }
  }, [scope])
  const current = loaded?.scope === scope ? loaded : null
  const marks = useMemo(() => ({ ...current?.marks, ...(instance ? { [instance.id]: buildRunnerMarks(instance.id, definition, trace, instance.workspaceMoves) } : {}) }), [current, instance, definition, trace])
  return { marks, errors: current?.errors ?? {} }
}
