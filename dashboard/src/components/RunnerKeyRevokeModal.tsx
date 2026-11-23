import { useEffect, useId, useRef, useState } from 'react'
import { revokeRunnerKey } from '../api/runners'
import type { RunnerKey } from '../api/types'
import styles from './RunnerKeys.module.css'

/** Spec-required confirmation copy (design P5 §3.4). */
export const REVOKE_WARNINGS = [
  'The runner using this key disconnects immediately.',
  'Work it was running recovers automatically on other runners and may repeat interrupted steps, including their external effects.',
  'Recommended: drain the runner first so its work finishes or moves at safe boundaries.',
] as const

export default function RunnerKeyRevokeModal({ runnerKey, onRevoked, onClose }: { runnerKey: RunnerKey; onRevoked: () => void; onClose: () => void }) {
  const titleId = useId()
  const epoch = useRef(0)
  const [closed, setClosed] = useState(false)
  useEffect(() => () => { epoch.current++ }, [])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleRevoke() {
    if (busy || closed) return
    const requestEpoch = ++epoch.current
    setBusy(true)
    setError(null)
    try {
      await revokeRunnerKey(runnerKey.id)
      if (epoch.current === requestEpoch) onRevoked()
    } catch (err) {
      if (epoch.current === requestEpoch) setError(err instanceof Error ? err.message : 'Failed to revoke the key')
    } finally {
      if (epoch.current === requestEpoch) setBusy(false)
    }
  }

  function close() {
    epoch.current++
    setClosed(true)
    onClose()
  }

  if (closed) return null

  return (
    <div className={styles.overlay}>
      <div className={styles.modal} role="dialog" aria-modal="true" aria-labelledby={titleId}>
        <h2 id={titleId} className={styles.title}>
          Revoke runner key "{runnerKey.name}"?
        </h2>
        {REVOKE_WARNINGS.map((w) => (
          <p key={w} className={styles.hint}>
            {w}
          </p>
        ))}
        {error && (
          <div className={styles.error} role="alert">
            Revoke failed: {error}
          </div>
        )}
        <div className={styles.actions}>
          <button type="button" className={styles.button} onClick={close}>
            Cancel
          </button>
          <button type="button" className={`${styles.button} ${styles.danger}`} disabled={busy} onClick={() => void handleRevoke()}>
            Revoke key
          </button>
        </div>
      </div>
    </div>
  )
}
