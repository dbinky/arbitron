import { useCallback, useState } from 'react'
import type { InstanceState } from '../api/types'
import { api } from '../api/client'
import styles from './InstanceControls.module.css'

interface InstanceControlsProps {
  instanceId: string
  state: InstanceState
  onAction?: () => void
}

export default function InstanceControls({ instanceId, state, onAction }: InstanceControlsProps) {
  const [loading, setLoading] = useState(false)

  const exec = useCallback(
    async (action: () => Promise<unknown>) => {
      setLoading(true)
      try {
        await action()
        onAction?.()
      } catch (err) {
        console.error('Instance action failed:', err)
      } finally {
        setLoading(false)
      }
    },
    [instanceId, onAction],
  )

  const canPause = state === 'running'
  const canResume = state === 'paused'
  const canCancel = state === 'running' || state === 'paused' || state === 'queued'
  const canRetry = state === 'failed'
  const isTerminal = state === 'completed' || state === 'failed' || state === 'cancelled'

  return (
    <div className={styles.controls}>
      {canPause && (
        <button
          className={styles.btn}
          disabled={loading}
          onClick={() => exec(() => api.instances.pause(instanceId))}
        >
          Pause
        </button>
      )}
      {canResume && (
        <button
          className={styles.btnSuccess}
          disabled={loading}
          onClick={() => exec(() => api.instances.resume(instanceId))}
        >
          Resume
        </button>
      )}
      {canCancel && (
        <button
          className={styles.btnDanger}
          disabled={loading}
          onClick={() => exec(() => api.instances.cancel(instanceId))}
        >
          Cancel
        </button>
      )}
      {canRetry && (
        <button
          className={styles.btnSuccess}
          disabled={loading}
          onClick={() => exec(() => api.instances.retry(instanceId))}
        >
          Retry
        </button>
      )}
      {isTerminal && (
        <button
          className={styles.btn}
          disabled={loading}
          onClick={() => exec(() => api.instances.rerun(instanceId))}
        >
          Re-run
        </button>
      )}
    </div>
  )
}
