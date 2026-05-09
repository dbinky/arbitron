import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import InteractivePanel from './InteractivePanel'
import type { InteractiveSessionState } from '../hooks/useInteractiveSession'

// ---------------------------------------------------------------------------
// Helper: build a minimal session object
// ---------------------------------------------------------------------------

function makeSession(overrides: Partial<InteractiveSessionState> = {}): InteractiveSessionState {
  return {
    messages: [],
    streamingContent: '',
    isStreaming: false,
    isActive: true,
    nodeId: 'review-design',
    error: null,
    connected: true,
    sendMessage: vi.fn(),
    endSession: vi.fn(),
    ...overrides,
  }
}

// ---------------------------------------------------------------------------
// Happy path: panel renders header and node name
// ---------------------------------------------------------------------------

describe('InteractivePanel — happy path', () => {
  it('renders the node name in the header', () => {
    render(<InteractivePanel session={makeSession()} nodeName="review-design" />)
    expect(screen.getByText('review-design')).toBeInTheDocument()
    expect(screen.getByText('Interactive Session')).toBeInTheDocument()
  })

  it('renders messages from session', () => {
    const session = makeSession({
      messages: [
        { role: 'user', content: 'Hello' },
        { role: 'assistant', content: 'Hi there' },
      ],
    })
    render(<InteractivePanel session={session} nodeName="review-design" />)
    expect(screen.getByText('Hello')).toBeInTheDocument()
    expect(screen.getByText('Hi there')).toBeInTheDocument()
  })

  it('renders streaming content as a partial message', () => {
    const session = makeSession({ streamingContent: 'I am thinking ab' })
    render(<InteractivePanel session={session} nodeName="review-design" />)
    expect(screen.getByText('I am thinking ab')).toBeInTheDocument()
  })

  it('calls endSession when End Session button is clicked', async () => {
    const endSession = vi.fn()
    const session = makeSession({ endSession })
    render(<InteractivePanel session={session} nodeName="review-design" />)
    await userEvent.click(screen.getByRole('button', { name: /end session/i }))
    expect(endSession).toHaveBeenCalledOnce()
  })
})

// ---------------------------------------------------------------------------
// Typing indicator: shown during early streaming (no tokens yet)
// ---------------------------------------------------------------------------

describe('InteractivePanel — typing indicator', () => {
  it('shows Thinking… indicator when streaming starts with no content yet', () => {
    const session = makeSession({ isStreaming: true, streamingContent: '' })
    render(<InteractivePanel session={session} nodeName="review-design" />)
    expect(screen.getByText(/thinking/i)).toBeInTheDocument()
  })

  it('hides Thinking… indicator when streaming content is present', () => {
    const session = makeSession({ isStreaming: true, streamingContent: 'Some tokens' })
    render(<InteractivePanel session={session} nodeName="review-design" />)
    expect(screen.queryByText(/thinking/i)).not.toBeInTheDocument()
  })

  it('does not show Thinking… indicator when not streaming', () => {
    const session = makeSession({ isStreaming: false })
    render(<InteractivePanel session={session} nodeName="review-design" />)
    expect(screen.queryByText(/thinking/i)).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Error path: error banner displayed
// ---------------------------------------------------------------------------

describe('InteractivePanel — error banner', () => {
  it('shows error banner when session has an error', () => {
    const session = makeSession({ error: 'LLM call failed' })
    render(<InteractivePanel session={session} nodeName="review-design" />)
    expect(screen.getByText('LLM call failed')).toBeInTheDocument()
  })

  it('does not show error banner when there is no error', () => {
    const session = makeSession({ error: null })
    render(<InteractivePanel session={session} nodeName="review-design" />)
    expect(screen.queryByText(/failed/i)).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Edge: empty state shown when no messages and not streaming
// ---------------------------------------------------------------------------

describe('InteractivePanel — empty state', () => {
  it('shows waiting message when there are no messages and not streaming', () => {
    const session = makeSession({ messages: [], streamingContent: '', isStreaming: false })
    render(<InteractivePanel session={session} nodeName="review-design" />)
    expect(screen.getByText(/waiting for the assistant/i)).toBeInTheDocument()
  })

  it('hides waiting message once messages are present', () => {
    const session = makeSession({ messages: [{ role: 'user', content: 'hi' }] })
    render(<InteractivePanel session={session} nodeName="review-design" />)
    expect(screen.queryByText(/waiting for the assistant/i)).not.toBeInTheDocument()
  })

  it('hides waiting message when streaming (typing indicator replaces it)', () => {
    const session = makeSession({ messages: [], isStreaming: true })
    render(<InteractivePanel session={session} nodeName="review-design" />)
    expect(screen.queryByText(/waiting for the assistant/i)).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Boundary: input disabled during streaming or disconnected
// ---------------------------------------------------------------------------

describe('InteractivePanel — input disabled state', () => {
  it('disables input and Send button while streaming', () => {
    const session = makeSession({ isStreaming: true, streamingContent: 'tokens' })
    render(<InteractivePanel session={session} nodeName="review-design" />)
    expect(screen.getByRole('textbox')).toBeDisabled()
    expect(screen.getByRole('button', { name: /send/i })).toBeDisabled()
  })

  it('disables input when disconnected', () => {
    const session = makeSession({ connected: false })
    render(<InteractivePanel session={session} nodeName="review-design" />)
    expect(screen.getByRole('textbox')).toBeDisabled()
  })

  it('enables input when connected and not streaming', () => {
    const session = makeSession({ isStreaming: false, connected: true })
    render(<InteractivePanel session={session} nodeName="review-design" />)
    expect(screen.getByRole('textbox')).not.toBeDisabled()
  })
})
