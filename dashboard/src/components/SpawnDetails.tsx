import { Link } from 'react-router-dom'
import type { SpawnConfig } from '../api/types'
import styles from '../routes/WorkflowReview.module.css'

export type MappingKind = 'reference' | 'template' | 'literal'

const MAX_LITERAL_CHARS = 120

const EMPTY_COPY =
  'No inputs mapped — every required input without a default will be prepared by the LLM at run time.'

/**
 * Mirrors the Go wholeReference test: the string is exactly one {{path}} with
 * nothing outside the braces, no nested braces, and a non-empty trimmed path.
 */
export function mappingKind(v: unknown): MappingKind {
  if (typeof v !== 'string') return 'literal'
  if (v.length >= 4 && v.startsWith('{{') && v.endsWith('}}')) {
    const inner = v.slice(2, -2)
    if (!inner.includes('{{') && !inner.includes('}}') && inner.trim() !== '') return 'reference'
  }
  return v.includes('{{') ? 'template' : 'literal'
}

/**
 * Compact JSON for a literal, cut after MAX_LITERAL_CHARS characters of the
 * value itself (a string's surrounding quotes do not count against the cap).
 */
function renderValue(v: unknown): { text: string; full: string } {
  const full = JSON.stringify(v) ?? String(v)
  const quotes = typeof v === 'string' ? 2 : 0
  const chars = Array.from(full)
  if (chars.length <= MAX_LITERAL_CHARS + quotes) return { text: full, full }
  return { text: chars.slice(0, MAX_LITERAL_CHARS + quotes / 2).join('') + '…', full }
}

const BADGE_CLASS: Record<MappingKind, string> = {
  reference: styles.spawnBadgeReference,
  template: styles.spawnBadgeTemplate,
  literal: styles.spawnBadgeLiteral,
}

export function SpawnDetails({ config }: { config: Record<string, unknown> }) {
  const c = config as SpawnConfig
  const child = typeof c.childWorkflow === 'string' && c.childWorkflow !== '' ? c.childWorkflow : null
  const mode = c.spawnMode === 'wait_for_child' ? 'waits for child' : c.spawnMode === 'fire_and_forget' ? 'fire and forget' : '—'
  const inputs =
    c.inputs !== null && typeof c.inputs === 'object' && !Array.isArray(c.inputs) ? c.inputs : {}
  const keys = Object.keys(inputs).sort()

  return (
    <div className={styles.nodeDetail} data-testid="review-spawn-details">
      <div className={styles.nodeDetailRow}>
        <span className={styles.nodeDetailLabel}>Child</span>
        <span className={styles.nodeDetailValue} data-testid="spawn-child">
          {child ? <Link to={`/workflows/${encodeURIComponent(child)}`}>{child}</Link> : '—'}
        </span>
      </div>
      <div className={styles.nodeDetailRow}>
        <span className={styles.nodeDetailLabel}>Mode</span>
        <span className={styles.nodeDetailValue}>{mode}</span>
      </div>
      {keys.length === 0 ? (
        <div className={styles.nodeDetailRow}>
          <span className={styles.nodeDetailValue}>{EMPTY_COPY}</span>
        </div>
      ) : (
        <table className={styles.spawnInputTable}>
          <tbody>
            {keys.map((key) => {
              const value = inputs[key]
              const kind = mappingKind(value)
              const { text, full } = renderValue(value)
              return (
                <tr key={key} data-testid="spawn-input-row">
                  <td className={styles.inputName} data-testid="spawn-input-key">{key}</td>
                  <td>
                    <span className={`${styles.spawnBadge} ${BADGE_CLASS[kind]}`}>{kind}</span>
                  </td>
                  <td className={styles.nodeDetailValue} data-testid="spawn-input-value" title={full}>
                    {text}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
    </div>
  )
}
