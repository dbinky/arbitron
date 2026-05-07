import { useMemo } from 'react'
import type { WebSocketEvent } from '../api/types'
import { useWebSocket } from './useWebSocket'

/**
 * Connects to the per-instance WebSocket at /api/v1/instances/:id/ws.
 * Auto-reconnects on disconnect after a 3-second delay.
 */
export function useInstanceWebSocket(
  instanceId: string | undefined,
  onEvent: (event: WebSocketEvent) => void,
) {
  const url = useMemo(() => {
    if (!instanceId) return undefined
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    return `${protocol}//${window.location.host}/api/v1/instances/${instanceId}/ws`
  }, [instanceId])

  return useWebSocket({ url, onEvent })
}
