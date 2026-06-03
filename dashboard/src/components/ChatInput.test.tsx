import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, vi } from 'vitest'
import ChatInput from './ChatInput'

// ---------------------------------------------------------------------------
// Happy path: renders correctly and sends trimmed content
// ---------------------------------------------------------------------------

describe('ChatInput — happy path', () => {
  it('renders the textarea and Send button', () => {
    render(<ChatInput onSend={vi.fn()} />)
    expect(screen.getByRole('textbox')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /send/i })).toBeInTheDocument()
  })

  it('clicking Send calls onSend with trimmed content and clears input', async () => {
    const onSend = vi.fn()
    render(<ChatInput onSend={onSend} />)
    const textarea = screen.getByRole('textbox')
    await userEvent.type(textarea, '  Hello world  ')
    await userEvent.click(screen.getByRole('button', { name: /send/i }))
    expect(onSend).toHaveBeenCalledWith('Hello world')
    expect(textarea).toHaveValue('')
  })

  it('pressing Enter calls onSend with trimmed content', async () => {
    const onSend = vi.fn()
    render(<ChatInput onSend={onSend} />)
    const textarea = screen.getByRole('textbox')
    await userEvent.type(textarea, 'Test message')
    await userEvent.keyboard('{Enter}')
    expect(onSend).toHaveBeenCalledWith('Test message')
  })

  it('renders hint text for keyboard shortcuts', () => {
    render(<ChatInput onSend={vi.fn()} />)
    expect(screen.getByText('Markdown supported')).toBeInTheDocument()
    expect(screen.getByText(/shift\+enter/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Error path: does not send empty or whitespace-only content
// ---------------------------------------------------------------------------

describe('ChatInput — empty/whitespace guard', () => {
  it('does not call onSend when input is empty', async () => {
    const onSend = vi.fn()
    render(<ChatInput onSend={onSend} />)
    await userEvent.click(screen.getByRole('button', { name: /send/i }))
    expect(onSend).not.toHaveBeenCalled()
  })

  it('does not call onSend when input is only whitespace', async () => {
    const onSend = vi.fn()
    render(<ChatInput onSend={onSend} />)
    const textarea = screen.getByRole('textbox')
    await userEvent.type(textarea, '   ')
    await userEvent.keyboard('{Enter}')
    expect(onSend).not.toHaveBeenCalled()
  })

  it('Send button is disabled when input is empty', () => {
    render(<ChatInput onSend={vi.fn()} />)
    expect(screen.getByRole('button', { name: /send/i })).toBeDisabled()
  })
})

// ---------------------------------------------------------------------------
// Edge: Shift+Enter inserts newline rather than sending
// ---------------------------------------------------------------------------

describe('ChatInput — Shift+Enter newline', () => {
  it('does not send when Shift+Enter is pressed', async () => {
    const onSend = vi.fn()
    render(<ChatInput onSend={onSend} />)
    const textarea = screen.getByRole('textbox')
    await userEvent.type(textarea, 'First line')
    await userEvent.keyboard('{Shift>}{Enter}{/Shift}')
    expect(onSend).not.toHaveBeenCalled()
  })
})

// ---------------------------------------------------------------------------
// Boundary: disabled prop prevents all sending
// ---------------------------------------------------------------------------

describe('ChatInput — disabled state', () => {
  it('disables the textarea when disabled=true', () => {
    render(<ChatInput onSend={vi.fn()} disabled />)
    expect(screen.getByRole('textbox')).toBeDisabled()
  })

  it('disables the Send button when disabled=true', () => {
    render(<ChatInput onSend={vi.fn()} disabled />)
    expect(screen.getByRole('button', { name: /send/i })).toBeDisabled()
  })

  it('does not call onSend via Enter when disabled', async () => {
    const onSend = vi.fn()
    // Pre-populate the textarea by rendering enabled, typing, then re-rendering disabled
    const { rerender } = render(<ChatInput onSend={onSend} />)
    await userEvent.type(screen.getByRole('textbox'), 'Hello')
    rerender(<ChatInput onSend={onSend} disabled />)
    await userEvent.keyboard('{Enter}')
    expect(onSend).not.toHaveBeenCalled()
  })
})

// ---------------------------------------------------------------------------
// Focus management: return focus to the input after an assistant reply
// (disabled flips true -> false), but never steal focus on first mount.
// ---------------------------------------------------------------------------

describe('ChatInput — focus management', () => {
  it('refocuses the textarea when re-enabled after being disabled', () => {
    const { rerender } = render(<ChatInput onSend={vi.fn()} disabled />)
    const textarea = screen.getByRole('textbox')
    expect(textarea).not.toHaveFocus()

    // Assistant finished streaming: input becomes enabled again.
    rerender(<ChatInput onSend={vi.fn()} disabled={false} />)
    expect(textarea).toHaveFocus()
  })

  it('does not steal focus on initial mount when already enabled', () => {
    render(<ChatInput onSend={vi.fn()} />)
    expect(screen.getByRole('textbox')).not.toHaveFocus()
  })

  it('does not focus the textarea while it stays disabled', () => {
    const { rerender } = render(<ChatInput onSend={vi.fn()} disabled />)
    rerender(<ChatInput onSend={vi.fn()} disabled />)
    expect(screen.getByRole('textbox')).not.toHaveFocus()
  })
})

// ---------------------------------------------------------------------------
// Integration: multiple sends work correctly in sequence
// ---------------------------------------------------------------------------

describe('ChatInput — integration', () => {
  it('can send multiple messages in sequence', async () => {
    const onSend = vi.fn()
    render(<ChatInput onSend={onSend} />)
    const textarea = screen.getByRole('textbox')

    await userEvent.type(textarea, 'First')
    await userEvent.keyboard('{Enter}')
    expect(onSend).toHaveBeenNthCalledWith(1, 'First')
    expect(textarea).toHaveValue('')

    await userEvent.type(textarea, 'Second')
    await userEvent.keyboard('{Enter}')
    expect(onSend).toHaveBeenNthCalledWith(2, 'Second')
    expect(textarea).toHaveValue('')

    expect(onSend).toHaveBeenCalledTimes(2)
  })
})
