import { useState, useCallback, useRef } from 'react'
import type {
  InteractiveMessage,
  InteractiveEvent,
  WebSocketEvent,
} from '../api/types'
import { useInstanceWebSocket } from './useInstanceWebSocket'

/** A message with a stable key for React list rendering. */
export interface KeyedMessage extends InteractiveMessage {
  /** Stable identifier for use as a React list key. */
  key: number
}

/** State and actions returned by the useInteractiveSession hook. */
export interface InteractiveSessionState {
  /** Complete messages in the conversation. */
  messages: KeyedMessage[]
  /** Partial content being streamed (current assistant response). */
  streamingContent: string
  /** True while tokens are arriving. */
  isStreaming: boolean
  /** True while the interactive session is live. */
  isActive: boolean
  /** The node ID that initiated the interactive session. */
  nodeId: string | null
  /** Last error message, if any. */
  error: string | null
  /** Whether the WebSocket is connected. */
  connected: boolean
  /** True while the end-session request is in flight (button should be disabled). */
  isEnding: boolean
  /** Send a user message to the interactive session. */
  sendMessage: (content: string) => void
  /** End the interactive session. */
  endSession: () => void
  /**
   * Pre-warm session state from the REST instance response.
   * Activates the panel immediately on page load without waiting for
   * interactive.history — eliminates the flash of the normal side panel.
   * Safe to call multiple times; nodeId is only set if not already known.
   */
  seedFromInstance: (nodeId: string) => void
}

/**
 * Manages an interactive chat session for a workflow instance.
 *
 * Wraps useInstanceWebSocket and provides a chat-specific interface with
 * state management for messages, streaming tokens, and session lifecycle.
 */
export function useInteractiveSession(
  instanceId: string | undefined,
): InteractiveSessionState {
  const [messages, setMessages] = useState<KeyedMessage[]>([])
  const [streamingContent, setStreamingContent] = useState('')
  const [isStreaming, setIsStreaming] = useState(false)
  const [isActive, setIsActive] = useState(false)
  const [isEnding, setIsEnding] = useState(false)
  const [nodeId, setNodeId] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Counter for stable message keys, persists across re-renders.
  const nextKeyRef = useRef(0)

  // Use a ref to hold the send function so event handler doesn't go stale
  const sendRef = useRef<(data: object) => void>(() => {})

  const assignKey = useCallback((msg: InteractiveMessage): KeyedMessage => {
    return { ...msg, key: nextKeyRef.current++ }
  }, [])

  const handleEvent = useCallback((event: WebSocketEvent) => {
    const e = event as unknown as InteractiveEvent
    switch (e.type) {
      case 'interactive.history':
        nextKeyRef.current = 0
        setMessages(e.data.messages.map((m) => ({ ...m, key: nextKeyRef.current++ })))
        setNodeId(e.data.nodeId)
        setIsActive(true)
        setError(null)
        setStreamingContent('')
        setIsStreaming(false)
        break

      case 'interactive.message':
        if (e.data.role === 'assistant') {
          // Replace streaming content with the complete message
          setStreamingContent('')
          setIsStreaming(false)
        }
        setMessages((prev) => [
          ...prev,
          assignKey({ role: e.data.role, content: e.data.content }),
        ])
        break

      case 'interactive.token':
        setStreamingContent((prev) => prev + e.data.token)
        setIsStreaming(true)
        break

      case 'interactive.ended':
        setIsActive(false)
        setIsEnding(false)
        setNodeId(null)
        setStreamingContent('')
        setIsStreaming(false)
        break

      case 'interactive.error':
        setError(e.data.message)
        setIsStreaming(false)
        setStreamingContent('')
        setIsEnding(false)
        break

      default:
        // Ignore non-interactive events (instance state changes, etc.)
        break
    }
  }, [assignKey])

  const { connected, send } = useInstanceWebSocket(instanceId, handleEvent)
  sendRef.current = send

  const sendMessage = useCallback(
    (content: string) => {
      sendRef.current({ type: 'interactive.send', content })
    },
    [],
  )

  const endSession = useCallback(() => {
    setIsEnding(true)
    sendRef.current({ type: 'interactive.end' })
  }, [])

  const seedFromInstance = useCallback((newNodeId: string) => {
    setIsActive(true)
    setNodeId((current) => current ?? newNodeId)
  }, [])

  return {
    messages,
    streamingContent,
    isStreaming,
    isActive,
    isEnding,
    nodeId,
    error,
    connected,
    sendMessage,
    endSession,
    seedFromInstance,
  }
}
