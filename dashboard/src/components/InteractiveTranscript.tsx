import type { InteractiveMessage } from '../api/types'
import ChatMessage from './ChatMessage'
import styles from './InteractiveTranscript.module.css'

interface InteractiveTranscriptProps {
  messages: InteractiveMessage[] | null
}

/**
 * Read-only view of a completed interactive session's conversation transcript.
 * Reuses the ChatMessage component to display each message with consistent
 * styling as the live interactive panel.
 */
export default function InteractiveTranscript({ messages }: InteractiveTranscriptProps) {
  if (!messages || messages.length === 0) {
    return (
      <div className={styles.empty}>
        No interactive conversation recorded for this node.
      </div>
    )
  }

  return (
    <div className={styles.container}>
      <div className={styles.header}>
        <span className={styles.headerLabel}>Completed Interactive Session</span>
        <span className={styles.messageCount}>
          {messages.length} message{messages.length !== 1 ? 's' : ''}
        </span>
      </div>
      <div className={styles.messages}>
        {messages.map((msg, i) => (
          <ChatMessage key={i} message={msg} />
        ))}
      </div>
    </div>
  )
}
