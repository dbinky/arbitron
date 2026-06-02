import { useState, useCallback, useRef, useEffect } from 'react'
import styles from './ChatInput.module.css'

interface ChatInputProps {
  onSend: (content: string) => void
  disabled?: boolean
}

export default function ChatInput({ onSend, disabled = false }: ChatInputProps) {
  const [value, setValue] = useState('')
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  // Auto-resize textarea to fit content
  useEffect(() => {
    const el = textareaRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 160)}px`
  }, [value])

  // Return focus to the input each time it re-enables after the assistant
  // finishes a reply (disabled flips true -> false). When the session ends,
  // the whole panel unmounts, so there is no input to refocus — which is
  // exactly the "don't refocus on the final reply" behavior we want.
  const wasDisabledRef = useRef(disabled)
  useEffect(() => {
    if (wasDisabledRef.current && !disabled) {
      textareaRef.current?.focus()
    }
    wasDisabledRef.current = disabled
  }, [disabled])

  const handleSend = useCallback(() => {
    const trimmed = value.trim()
    if (!trimmed || disabled) return
    onSend(trimmed)
    setValue('')
  }, [value, disabled, onSend])

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault()
        handleSend()
      }
    },
    [handleSend],
  )

  return (
    <div className={styles.inputArea}>
      <div className={styles.inputRow}>
        <textarea
          ref={textareaRef}
          className={styles.textarea}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder="Type a message..."
          disabled={disabled}
          rows={1}
        />
        <button
          className={styles.sendButton}
          onClick={handleSend}
          disabled={disabled || !value.trim()}
          title="Send message"
        >
          Send
        </button>
      </div>
      <div className={styles.hints}>
        <span className={styles.hintLeft}>Markdown supported</span>
        <span className={styles.hintRight}>
          Enter to send &middot; Shift+Enter for newline
        </span>
      </div>
    </div>
  )
}
