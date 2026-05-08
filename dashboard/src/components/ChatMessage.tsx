import Markdown from 'react-markdown'
import type { InteractiveMessage } from '../api/types'
import styles from './ChatMessage.module.css'

interface ChatMessageProps {
  message: InteractiveMessage
}

const roleLabels: Record<InteractiveMessage['role'], string> = {
  assistant: 'Assistant',
  user: 'You',
}

export default function ChatMessage({ message }: ChatMessageProps) {
  const roleClass =
    message.role === 'assistant' ? styles.assistant : styles.user

  return (
    <div className={`${styles.message} ${roleClass}`}>
      <div className={styles.roleLabel}>{roleLabels[message.role]}</div>
      <div className={styles.content}>
        <Markdown>{message.content}</Markdown>
      </div>
    </div>
  )
}
