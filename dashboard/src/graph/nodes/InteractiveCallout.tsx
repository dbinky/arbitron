import type { ReactNode } from 'react'
import styles from './InteractiveCallout.module.css'

interface InteractiveCalloutProps {
  /** Content to display inside the callout. Omit for the waiting state. */
  children?: ReactNode
  /** Whether the node is currently waiting for user interaction. */
  waiting?: boolean
}

/**
 * A comic-book speech-bubble callout for interactive workflow steps.
 * Renders a rounded rectangle with a triangular tail pointing downward.
 * When `waiting` is true, shows a pulsing orange border animation.
 */
export default function InteractiveCallout({ children, waiting = false }: InteractiveCalloutProps) {
  const classes = [
    styles.callout,
    waiting ? styles.waiting : '',
  ].filter(Boolean).join(' ')

  return (
    <div className={classes}>
      {children}
    </div>
  )
}
