import { useMemo } from 'react'
import type { WebSocketEvent } from '../api/types'
import { useWebSocket } from './useWebSocket'

export interface InstanceWebSocketOptions {
  /** Include authorized descendants, with sourceInstanceId on each frame. */
  subtree?: boolean
}

/**
 * Connects to /api/v1/instances/:id/ws, optionally with ?subtree=1.
 * Auto-reconnects after a 3-second delay; an undefined instanceId closes the
 * socket and stops reconnecting. Interactive sessions keep the plain stream.
 */
export function useInstanceWebSocket(
  instanceId: string | undefined,
  onEvent: (event: WebSocketEvent) => void,
  options: InstanceWebSocketOptions = {},
) {
  const subtree = options.subtree === true
  const url = useMemo(() => {
    if (!instanceId) return undefined
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const base = `${protocol}//${window.location.host}/api/v1/instances/${instanceId}/ws`
    return subtree ? `${base}?subtree=1` : base
  }, [instanceId, subtree])

  return useWebSocket({ url, onEvent })
}
