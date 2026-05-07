import { useEffect, useRef, useState, useCallback } from 'react'
import type { WebSocketEvent } from '../api/types'

const RECONNECT_DELAY_MS = 3000

interface UseWebSocketOptions {
  /** Full WebSocket URL to connect to. If undefined, no connection is made. */
  url: string | undefined
  /** Callback invoked for each received event. */
  onEvent: (event: WebSocketEvent) => void
}

/**
 * Shared WebSocket hook with auto-reconnect on disconnect.
 *
 * Handles the connection lifecycle, message parsing, and reconnection logic
 * that was previously duplicated across useSystemWebSocket and
 * useInstanceWebSocket.
 *
 * @returns An object with the current connection state.
 */
export function useWebSocket({ url, onEvent }: UseWebSocketOptions) {
  const [connected, setConnected] = useState(false)
  const onEventRef = useRef(onEvent)
  onEventRef.current = onEvent

  const connect = useCallback(() => {
    if (!url) return null
    const ws = new WebSocket(url)

    ws.onopen = () => {
      setConnected(true)
    }

    ws.onmessage = (msg) => {
      try {
        const event = JSON.parse(msg.data as string) as WebSocketEvent
        onEventRef.current(event)
      } catch {
        // Ignore malformed messages
      }
    }

    ws.onclose = () => {
      setConnected(false)
    }

    ws.onerror = () => {
      ws.close()
    }

    return ws
  }, [url])

  useEffect(() => {
    if (!url) return

    let ws = connect()
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null
    let unmounted = false

    const scheduleReconnect = () => {
      if (unmounted) return
      reconnectTimer = setTimeout(() => {
        if (!unmounted) {
          ws = connect()
          ws?.addEventListener('close', scheduleReconnect)
        }
      }, RECONNECT_DELAY_MS)
    }

    ws?.addEventListener('close', scheduleReconnect)

    return () => {
      unmounted = true
      if (reconnectTimer) clearTimeout(reconnectTimer)
      ws?.close()
    }
  }, [url, connect])

  return { connected }
}
