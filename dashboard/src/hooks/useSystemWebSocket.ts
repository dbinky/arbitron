import { useEffect, useRef, useState, useCallback } from 'react'
import type { WebSocketEvent } from '../api/types'

const RECONNECT_DELAY_MS = 3000

/**
 * Connects to the system-level WebSocket at /api/v1/ws.
 * Auto-reconnects on disconnect after a 3-second delay.
 *
 * @param onEvent - Callback invoked for each received event.
 * @returns An object with the current connection state.
 */
export function useSystemWebSocket(onEvent: (event: WebSocketEvent) => void) {
  const [connected, setConnected] = useState(false)
  const onEventRef = useRef(onEvent)
  onEventRef.current = onEvent

  const connect = useCallback(() => {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${protocol}//${window.location.host}/api/v1/ws`
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
  }, [])

  useEffect(() => {
    let ws = connect()
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null
    let unmounted = false

    const scheduleReconnect = () => {
      if (unmounted) return
      reconnectTimer = setTimeout(() => {
        if (!unmounted) {
          ws = connect()
          ws.addEventListener('close', scheduleReconnect)
        }
      }, RECONNECT_DELAY_MS)
    }

    ws.addEventListener('close', scheduleReconnect)

    return () => {
      unmounted = true
      if (reconnectTimer) clearTimeout(reconnectTimer)
      ws.close()
    }
  }, [connect])

  return { connected }
}
