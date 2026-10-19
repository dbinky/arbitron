import { useEffect, useRef } from 'react'
import type { InteractiveSessionState } from '../hooks/useInteractiveSession'
import ChatMessage from './ChatMessage'
import ChatInput from './ChatInput'
import styles from './InteractivePanel.module.css'

interface InteractivePanelProps {
  session: InteractiveSessionState
  nodeName: string
}

export default function InteractivePanel({
  session,
  nodeName,
}: InteractivePanelProps) {
  const messagesEndRef = useRef<HTMLDivElement>(null)

  // Auto-scroll to bottom when new messages or streaming content arrive
  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [session.messages, session.streamingContent])

  const isGroupResolution = session.sessionKind === 'group_resolution'

  return (
    <div className={styles.panel}>
      {/* Header */}
      <div className={styles.header}>
        <div className={styles.headerTitle}>
          <span className={styles.headerLabel}>
            {isGroupResolution ? 'Gather Conflict Resolution' : 'Interactive Session'}
          </span>
          <span className={styles.headerSep}>&mdash;</span>
          <span className={styles.headerNode}>{nodeName}</span>
        </div>
        <div className={styles.headerActions}>
          {isGroupResolution && (
            <button
              className={styles.abandonButton}
              onClick={session.abandonSession}
              disabled={session.isAbandoning || session.isEnding || session.isStreaming || !session.connected}
              title="Abandon the gather: merged changes are rolled back and the group continues on its failure path"
            >
              Abandon gather
            </button>
          )}
          <button
            className={styles.endButton}
            onClick={session.endSession}
            disabled={session.isEnding || session.isAbandoning || session.isStreaming || !session.connected}
            title={session.isStreaming
              ? 'Wait for the assistant to finish responding'
              : isGroupResolution
                ? 'Finish: Arbitron verifies no conflicts remain, then continues the gather'
                : 'End the interactive session'}
          >
            End Session
          </button>
        </div>
      </div>

      {/* Error banner */}
      {session.error && (
        <div className={styles.errorBanner}>{session.error}</div>
      )}

      {/* Messages area */}
      <div className={styles.messages}>
        {session.messages.length === 0 && !session.streamingContent && !session.isStreaming && (
          <div className={styles.empty}>
            Waiting for the assistant to respond...
          </div>
        )}

        {session.messages.map((msg) => (
          <ChatMessage key={msg.key} message={msg} />
        ))}

        {/* Streaming content (partial assistant response) */}
        {session.streamingContent && (
          <ChatMessage
            message={{ role: 'assistant', content: session.streamingContent }}
          />
        )}

        {/* Typing indicator: shown when streaming has started but no tokens yet */}
        {session.isStreaming && !session.streamingContent && (
          <div className={styles.typingIndicator}>
            <span className={styles.typingDot} />
            Thinking...
          </div>
        )}

        <div ref={messagesEndRef} />
      </div>

      {/* Input */}
      <ChatInput
        onSend={session.sendMessage}
        disabled={session.isStreaming || !session.connected}
      />
    </div>
  )
}
