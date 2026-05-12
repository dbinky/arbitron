import { render, screen } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import ChatMessage from './ChatMessage'
import type { InteractiveMessage } from '../api/types'

// ---------------------------------------------------------------------------
// Happy path: role label and content rendered correctly
// ---------------------------------------------------------------------------

describe('ChatMessage — happy path', () => {
  it('renders "You" label for user messages', () => {
    const message: InteractiveMessage = { role: 'user', content: 'Hello' }
    render(<ChatMessage message={message} />)
    expect(screen.getByText('You')).toBeInTheDocument()
    expect(screen.getByText('Hello')).toBeInTheDocument()
  })

  it('renders "Assistant" label for assistant messages', () => {
    const message: InteractiveMessage = { role: 'assistant', content: 'Hi there' }
    render(<ChatMessage message={message} />)
    expect(screen.getByText('Assistant')).toBeInTheDocument()
    expect(screen.getByText('Hi there')).toBeInTheDocument()
  })

  it('renders markdown bold text', () => {
    const message: InteractiveMessage = { role: 'assistant', content: '**bold**' }
    render(<ChatMessage message={message} />)
    // react-markdown renders **bold** as <strong>bold</strong>
    const strong = screen.getByText('bold')
    expect(strong.tagName).toBe('STRONG')
  })

  it('renders markdown code inline', () => {
    const message: InteractiveMessage = { role: 'assistant', content: 'Use `const x = 1`' }
    render(<ChatMessage message={message} />)
    const code = screen.getByText('const x = 1')
    expect(code.tagName).toBe('CODE')
  })
})

// ---------------------------------------------------------------------------
// Edge: empty content renders without crashing
// ---------------------------------------------------------------------------

describe('ChatMessage — edge cases', () => {
  it('renders without crashing when content is empty string', () => {
    const message: InteractiveMessage = { role: 'user', content: '' }
    render(<ChatMessage message={message} />)
    expect(screen.getByText('You')).toBeInTheDocument()
  })

  it('renders multi-paragraph markdown correctly', () => {
    const message: InteractiveMessage = {
      role: 'assistant',
      content: 'First paragraph.\n\nSecond paragraph.',
    }
    render(<ChatMessage message={message} />)
    expect(screen.getByText('First paragraph.')).toBeInTheDocument()
    expect(screen.getByText('Second paragraph.')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Boundary: role determines visual class (assistant vs user)
// ---------------------------------------------------------------------------

describe('ChatMessage — role class application', () => {
  it('applies distinct wrapper class for assistant vs user role', () => {
    const { container: aContainer } = render(
      <ChatMessage message={{ role: 'assistant', content: 'hi' }} />,
    )
    const { container: uContainer } = render(
      <ChatMessage message={{ role: 'user', content: 'hi' }} />,
    )
    // The outermost div carries both the base 'message' class and the role class
    const assistantClass = aContainer.firstElementChild?.className ?? ''
    const userClass = uContainer.firstElementChild?.className ?? ''
    expect(assistantClass).not.toBe(userClass)
    expect(assistantClass).toBeTruthy()
    expect(userClass).toBeTruthy()
  })
})
