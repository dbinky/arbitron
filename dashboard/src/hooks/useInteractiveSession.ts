import { useState, useCallback, useRef } from 'react'
import type {
  InteractiveMessage,
  InteractiveEvent,
  WebSocketEvent,
} from '../api/types'
import { useInstanceWebSocket } from './useInstanceWebSocket'

/** State and actions returned by the useInteractiveSession hook. */
export interface InteractiveSessionState {
  /** Complete messages in the conversation. */
  messages: InteractiveMessage[]
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
  /** Send a user message to the interactive session. */
  sendMessage: (content: string) => void
  /** End the interactive session. */
  endSession: () => void
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
  const [messages, setMessages] = useState<InteractiveMessage[]>([])
  const [streamingContent, setStreamingContent] = useState('')
  const [isStreaming, setIsStreaming] = useState(false)
  const [isActive, setIsActive] = useState(false)
  const [nodeId, setNodeId] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Use a ref to hold the send function so event handler doesn't go stale
  const sendRef = useRef<(data: object) => void>(() => {})

  const handleEvent = useCallback((event: WebSocketEvent) => {
    const e = event as unknown as InteractiveEvent
    switch (e.type) {
      case 'interactive.history':
        setMessages(e.data.messages)
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
          { role: e.data.role, content: e.data.content },
        ])
        break

      case 'interactive.token':
        setStreamingContent((prev) => prev + e.data.token)
        setIsStreaming(true)
        break

      case 'interactive.ended':
        setIsActive(false)
        setNodeId(null)
        setStreamingContent('')
        setIsStreaming(false)
        break

      case 'interactive.error':
        setError(e.data.message)
        setIsStreaming(false)
        setStreamingContent('')
        break

      default:
        // Ignore non-interactive events (instance state changes, etc.)
        break
    }
  }, [])

  const { connected, send } = useInstanceWebSocket(instanceId, handleEvent)
  sendRef.current = send

  const sendMessage = useCallback(
    (content: string) => {
      sendRef.current({ type: 'interactive.send', content })
    },
    [],
  )

  const endSession = useCallback(() => {
    sendRef.current({ type: 'interactive.end' })
  }, [])

  return {
    messages,
    streamingContent,
    isStreaming,
    isActive,
    nodeId,
    error,
    connected,
    sendMessage,
    endSession,
  }
}
