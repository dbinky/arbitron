import { renderHook, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { useInteractiveSession } from './useInteractiveSession'
import type { WebSocketEvent } from '../api/types'

// ---------------------------------------------------------------------------
// Mock useInstanceWebSocket so tests control what events the hook receives
// ---------------------------------------------------------------------------

let capturedHandler: ((event: WebSocketEvent) => void) | null = null
let mockSend: ReturnType<typeof vi.fn>

vi.mock('./useInstanceWebSocket', () => ({
  useInstanceWebSocket: vi.fn((
    _instanceId: string | undefined,
    onEvent: (event: WebSocketEvent) => void,
  ) => {
    capturedHandler = onEvent
    return { connected: true, send: mockSend }
  }),
}))

function fireEvent(event: WebSocketEvent) {
  act(() => {
    capturedHandler!(event)
  })
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const historyEvent = (): WebSocketEvent => ({
  type: 'interactive.history',
  timestamp: new Date().toISOString(),
  data: {
    nodeId: 'review-design',
    messages: [
      { role: 'user', content: 'Please review' },
      { role: 'assistant', content: 'I will review.' },
    ],
  },
})

const tokenEvent = (token: string): WebSocketEvent => ({
  type: 'interactive.token',
  timestamp: new Date().toISOString(),
  data: { token },
})

const assistantMessageEvent = (content: string): WebSocketEvent => ({
  type: 'interactive.message',
  timestamp: new Date().toISOString(),
  data: { role: 'assistant', content },
})

const userMessageEvent = (content: string): WebSocketEvent => ({
  type: 'interactive.message',
  timestamp: new Date().toISOString(),
  data: { role: 'user', content },
})

const endedEvent = (reason: 'user_ended' | 'llm_completed'): WebSocketEvent => ({
  type: 'interactive.ended',
  timestamp: new Date().toISOString(),
  data: { reason },
})

const errorEvent = (message: string): WebSocketEvent => ({
  type: 'interactive.error',
  timestamp: new Date().toISOString(),
  data: { message },
})

// ---------------------------------------------------------------------------
// Test setup
// ---------------------------------------------------------------------------

beforeEach(() => {
  capturedHandler = null
  mockSend = vi.fn()
})

// ---------------------------------------------------------------------------
// Happy path: initial state
// ---------------------------------------------------------------------------

describe('useInteractiveSession — initial state', () => {
  it('starts with empty messages and inactive session', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    expect(result.current.messages).toEqual([])
    expect(result.current.streamingContent).toBe('')
    expect(result.current.isStreaming).toBe(false)
    expect(result.current.isActive).toBe(false)
    expect(result.current.nodeId).toBeNull()
    expect(result.current.error).toBeNull()
    expect(result.current.connected).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// Happy path: interactive.history activates the session
// ---------------------------------------------------------------------------

describe('useInteractiveSession — interactive.history', () => {
  it('sets messages, nodeId, and marks session active', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    fireEvent(historyEvent())
    expect(result.current.messages).toHaveLength(2)
    expect(result.current.messages[0]).toEqual({ role: 'user', content: 'Please review' })
    expect(result.current.messages[1]).toEqual({ role: 'assistant', content: 'I will review.' })
    expect(result.current.nodeId).toBe('review-design')
    expect(result.current.isActive).toBe(true)
    expect(result.current.error).toBeNull()
  })

  it('clears any prior error when history is received', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    fireEvent(errorEvent('prior error'))
    expect(result.current.error).toBe('prior error')
    fireEvent(historyEvent())
    expect(result.current.error).toBeNull()
  })

  it('replaces messages on second history event (reconnect scenario)', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    fireEvent(historyEvent())
    expect(result.current.messages).toHaveLength(2)

    const secondHistory: WebSocketEvent = {
      type: 'interactive.history',
      timestamp: new Date().toISOString(),
      data: {
        nodeId: 'review-design',
        messages: [{ role: 'user', content: 'New message only' }],
      },
    }
    fireEvent(secondHistory)
    expect(result.current.messages).toHaveLength(1)
    expect(result.current.messages[0].content).toBe('New message only')
  })
})

// ---------------------------------------------------------------------------
// Happy path: token streaming accumulates content
// ---------------------------------------------------------------------------

describe('useInteractiveSession — interactive.token', () => {
  it('accumulates tokens and sets isStreaming true', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    fireEvent(tokenEvent('Hello'))
    expect(result.current.streamingContent).toBe('Hello')
    expect(result.current.isStreaming).toBe(true)
    fireEvent(tokenEvent(', world'))
    expect(result.current.streamingContent).toBe('Hello, world')
    expect(result.current.isStreaming).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// Happy path: assistant message clears streaming and appends complete message
// ---------------------------------------------------------------------------

describe('useInteractiveSession — interactive.message (assistant)', () => {
  it('clears streaming state and appends the complete message', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))

    fireEvent(tokenEvent('partial'))
    expect(result.current.isStreaming).toBe(true)
    expect(result.current.streamingContent).toBe('partial')

    fireEvent(assistantMessageEvent('Complete response'))
    expect(result.current.isStreaming).toBe(false)
    expect(result.current.streamingContent).toBe('')
    expect(result.current.messages).toHaveLength(1)
    expect(result.current.messages[0]).toEqual({ role: 'assistant', content: 'Complete response' })
  })
})

// ---------------------------------------------------------------------------
// Happy path: user message is appended without touching streaming state
// ---------------------------------------------------------------------------

describe('useInteractiveSession — interactive.message (user)', () => {
  it('appends user message without changing streaming state', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))

    fireEvent(tokenEvent('assistant thinking...'))
    fireEvent(userMessageEvent('Hello there'))

    expect(result.current.messages).toHaveLength(1)
    expect(result.current.messages[0]).toEqual({ role: 'user', content: 'Hello there' })
    // Streaming state was not touched by the user message
    expect(result.current.isStreaming).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// Happy path: session ends cleanly
// ---------------------------------------------------------------------------

describe('useInteractiveSession — interactive.ended', () => {
  it('marks session inactive and clears streaming (user_ended)', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    fireEvent(historyEvent())
    fireEvent(tokenEvent('partial'))
    fireEvent(endedEvent('user_ended'))

    expect(result.current.isActive).toBe(false)
    expect(result.current.nodeId).toBeNull()
    expect(result.current.isStreaming).toBe(false)
    expect(result.current.streamingContent).toBe('')
  })

  it('marks session inactive and clears streaming (llm_completed)', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    fireEvent(historyEvent())
    fireEvent(endedEvent('llm_completed'))

    expect(result.current.isActive).toBe(false)
    expect(result.current.isStreaming).toBe(false)
  })

  it('retains messages after session ends (transcript remains visible)', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    fireEvent(historyEvent())
    fireEvent(assistantMessageEvent('Final answer'))
    fireEvent(endedEvent('llm_completed'))

    expect(result.current.messages).toHaveLength(3) // 2 from history + 1 assistant
    expect(result.current.isActive).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// Error path: interactive.error sets error message
// ---------------------------------------------------------------------------

describe('useInteractiveSession — interactive.error', () => {
  it('sets the error field when an error event is received', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    fireEvent(errorEvent('LLM call failed'))
    expect(result.current.error).toBe('LLM call failed')
  })

  it('clears streaming state when error fires mid-stream', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))

    // Simulate tokens arriving then an error
    fireEvent(tokenEvent('some'))
    fireEvent(tokenEvent(' partial'))
    expect(result.current.isStreaming).toBe(true)
    expect(result.current.streamingContent).toBe('some partial')

    fireEvent(errorEvent('connection lost'))

    expect(result.current.error).toBe('connection lost')
    // isStreaming and streamingContent must be cleared — showing a typing
    // indicator alongside an error is confusing
    expect(result.current.isStreaming).toBe(false)
    expect(result.current.streamingContent).toBe('')
  })
})

// ---------------------------------------------------------------------------
// Edge: undefined instanceId
// ---------------------------------------------------------------------------

describe('useInteractiveSession — edge cases', () => {
  it('does not crash when instanceId is undefined', () => {
    const { result } = renderHook(() => useInteractiveSession(undefined))
    expect(result.current.messages).toEqual([])
    expect(result.current.isActive).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// Boundary: sendMessage and endSession send correct payloads
// ---------------------------------------------------------------------------

describe('useInteractiveSession — send actions', () => {
  it('sendMessage sends correct interactive.send payload', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    act(() => {
      result.current.sendMessage('Hello world')
    })
    expect(mockSend).toHaveBeenCalledWith({ type: 'interactive.send', content: 'Hello world' })
  })

  it('endSession sends correct interactive.end payload', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    act(() => {
      result.current.endSession()
    })
    expect(mockSend).toHaveBeenCalledWith({ type: 'interactive.end' })
  })
})

// ---------------------------------------------------------------------------
// Integration: full conversation round-trip
// ---------------------------------------------------------------------------

describe('useInteractiveSession — integration', () => {
  it('handles a complete session from history to ended with multiple messages', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))

    // Receive initial history
    fireEvent(historyEvent())
    expect(result.current.isActive).toBe(true)
    expect(result.current.messages).toHaveLength(2)

    // User sends message
    act(() => {
      result.current.sendMessage('What do you think?')
    })
    expect(mockSend).toHaveBeenCalledWith({ type: 'interactive.send', content: 'What do you think?' })

    // User message echoed back
    fireEvent(userMessageEvent('What do you think?'))
    expect(result.current.messages).toHaveLength(3)

    // LLM streams response
    fireEvent(tokenEvent('I think '))
    fireEvent(tokenEvent('it looks good.'))
    expect(result.current.isStreaming).toBe(true)

    // LLM message complete
    fireEvent(assistantMessageEvent('I think it looks good.'))
    expect(result.current.isStreaming).toBe(false)
    expect(result.current.streamingContent).toBe('')
    expect(result.current.messages).toHaveLength(4)

    // Session ends
    fireEvent(endedEvent('llm_completed'))
    expect(result.current.isActive).toBe(false)
    expect(result.current.messages).toHaveLength(4)
  })
})
