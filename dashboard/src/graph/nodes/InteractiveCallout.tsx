import type { ReactNode } from 'react'
import styles from './InteractiveCallout.module.css'

interface InteractiveCalloutProps {
  children?: ReactNode
}

/**
 * A comic-book speech-bubble callout for interactive workflow steps.
 * Renders a rounded rectangle with a triangular tail pointing downward
 * and a pulsing orange/amber border animation.
 */
export default function InteractiveCallout({ children }: InteractiveCalloutProps) {
  return (
    <div className={styles.callout}>
      {children ?? 'Interactive session — available in a future release.'}
    </div>
  )
}
