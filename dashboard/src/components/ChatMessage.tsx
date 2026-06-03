import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { InteractiveMessage } from '../api/types'
import styles from './ChatMessage.module.css'

interface ChatMessageProps {
  message: InteractiveMessage
}

const roleLabels: Record<InteractiveMessage['role'], string> = {
  assistant: 'Assistant',
  user: 'You',
}

/** Only allow safe URL schemes. Blocks javascript: and data: URLs. */
const ALLOWED_PROTOCOLS = new Set(['http:', 'https:', 'mailto:'])

function sanitizeUrl(url: string): string {
  try {
    const parsed = new URL(url, window.location.origin)
    return ALLOWED_PROTOCOLS.has(parsed.protocol) ? url : ''
  } catch {
    return ''
  }
}

// Note: rehype-raw must never be added without DOMPurify — it would enable
// arbitrary HTML injection via markdown content.

export default function ChatMessage({ message }: ChatMessageProps) {
  const roleClass =
    message.role === 'assistant' ? styles.assistant : styles.user

  return (
    <div className={`${styles.message} ${roleClass}`}>
      <div className={styles.roleLabel}>{roleLabels[message.role]}</div>
      <div className={styles.content}>
        <Markdown remarkPlugins={[remarkGfm]} urlTransform={sanitizeUrl}>
          {message.content}
        </Markdown>
      </div>
    </div>
  )
}
