import { useMemo } from 'react'
import type { WebSocketEvent } from '../api/types'
import { useWebSocket } from './useWebSocket'

/**
 * Connects to the system-level WebSocket at /api/v1/ws.
 * Auto-reconnects on disconnect after a 3-second delay.
 *
 * @param onEvent - Callback invoked for each received event.
 * @returns An object with the current connection state.
 */
export function useSystemWebSocket(onEvent: (event: WebSocketEvent) => void) {
  const url = useMemo(() => {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    return `${protocol}//${window.location.host}/api/v1/ws`
  }, [])

  return useWebSocket({ url, onEvent })
}
