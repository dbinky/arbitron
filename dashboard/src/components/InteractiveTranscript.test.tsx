import { render, screen } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import InteractiveTranscript from './InteractiveTranscript'
import type { InteractiveMessage } from '../api/types'

// ---------------------------------------------------------------------------
// Happy path: messages rendered with count
// ---------------------------------------------------------------------------

describe('InteractiveTranscript — happy path', () => {
  it('renders all messages when messages array is non-empty', () => {
    const messages: InteractiveMessage[] = [
      { role: 'user', content: 'Please review' },
      { role: 'assistant', content: 'I will review the design.' },
    ]
    render(<InteractiveTranscript messages={messages} />)
    expect(screen.getByText('Please review')).toBeInTheDocument()
    expect(screen.getByText('I will review the design.')).toBeInTheDocument()
  })

  it('renders the "Completed Interactive Session" header', () => {
    const messages: InteractiveMessage[] = [
      { role: 'user', content: 'Hello' },
    ]
    render(<InteractiveTranscript messages={messages} />)
    expect(screen.getByText('Completed Interactive Session')).toBeInTheDocument()
  })

  it('shows correct message count for multiple messages', () => {
    const messages: InteractiveMessage[] = [
      { role: 'user', content: 'First' },
      { role: 'assistant', content: 'Second' },
      { role: 'user', content: 'Third' },
    ]
    render(<InteractiveTranscript messages={messages} />)
    expect(screen.getByText('3 messages')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Edge: empty and null messages show empty state
// ---------------------------------------------------------------------------

describe('InteractiveTranscript — empty state', () => {
  it('shows empty state when messages is null', () => {
    render(<InteractiveTranscript messages={null} />)
    expect(screen.getByText(/no interactive conversation recorded/i)).toBeInTheDocument()
  })

  it('shows empty state when messages is an empty array', () => {
    render(<InteractiveTranscript messages={[]} />)
    expect(screen.getByText(/no interactive conversation recorded/i)).toBeInTheDocument()
  })

  it('does not render header when messages is null', () => {
    render(<InteractiveTranscript messages={null} />)
    expect(screen.queryByText('Completed Interactive Session')).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Boundary: singular vs plural message count
// ---------------------------------------------------------------------------

describe('InteractiveTranscript — message count grammar', () => {
  it('shows "1 message" (singular) for exactly one message', () => {
    const messages: InteractiveMessage[] = [{ role: 'user', content: 'Just one' }]
    render(<InteractiveTranscript messages={messages} />)
    expect(screen.getByText('1 message')).toBeInTheDocument()
    expect(screen.queryByText('1 messages')).not.toBeInTheDocument()
  })

  it('shows "2 messages" (plural) for two messages', () => {
    const messages: InteractiveMessage[] = [
      { role: 'user', content: 'First' },
      { role: 'assistant', content: 'Second' },
    ]
    render(<InteractiveTranscript messages={messages} />)
    expect(screen.getByText('2 messages')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Integration: read-only transcript matches same styling as live panel
// ---------------------------------------------------------------------------

describe('InteractiveTranscript — integration', () => {
  it('renders user and assistant role labels via ChatMessage', () => {
    const messages: InteractiveMessage[] = [
      { role: 'user', content: 'Can you help?' },
      { role: 'assistant', content: 'Absolutely!' },
    ]
    render(<InteractiveTranscript messages={messages} />)
    expect(screen.getByText('You')).toBeInTheDocument()
    expect(screen.getByText('Assistant')).toBeInTheDocument()
  })
})
