import { useEffect, useId, useRef, useState } from 'react'
import { createRunnerKey } from '../api/runners'
import type { RunnerKey } from '../api/types'
import styles from './RunnerKeys.module.css'

/** Unique, DNS-label-like runner key name (the server enforces uniqueness). */
export const RUNNER_KEY_NAME = /^[a-z0-9][a-z0-9-]{1,62}$/

/** runner.env contents (contracts §15). The secret is pasted by the operator, never templated in. */
export function runnerEnvSnippet(serverUrl: string): string {
  return `ARBITRON_SERVER_URL=${serverUrl}\nARBITRON_RUNNER_KEY=<paste the key you copied>`
}

export function workspaceRootGuidance(root: string | null): string {
  const target = root ? root : "the server's workspace root (see Settings → System)"
  return `Set workspace_root in ~/.arbitron-runner/config.yaml to ${target}. A runner whose workspace root differs from the server's runs chat steps only.`
}

type CopyState = 'idle' | 'copied' | 'failed'

/**
 * Add runner: name → Create → the one-time secret. The secret lives only in
 * this component's state; onCreated receives key metadata only. Closing clears
 * the state before the dialog unmounts.
 */
export default function RunnerKeyAddModal({
  serverUrl,
  workspaceRoot,
  onCreated,
  onClose,
}: {
  serverUrl: string
  workspaceRoot: string | null
  onCreated: (key: RunnerKey) => void
  onClose: () => void
}) {
  const titleId = useId()
  const epoch = useRef(0)
  const [closed, setClosed] = useState(false)
  useEffect(() => () => { epoch.current++ }, [])
  const [name, setName] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [secret, setSecret] = useState<string | null>(null)
  const [setupGuidance, setSetupGuidance] = useState('')
  const [reveal, setReveal] = useState(false)
  const [copy, setCopy] = useState<CopyState>('idle')

  const valid = RUNNER_KEY_NAME.test(name)

  async function handleCreate() {
    if (!valid || submitting || closed) return
    const requestEpoch = ++epoch.current
    setSubmitting(true)
    setError(null)
    try {
      const created = await createRunnerKey(name)
      if (epoch.current !== requestEpoch) return
      setSecret(created.plaintext)
      setSetupGuidance(created.setup)
      onCreated(created.key)
    } catch (err) {
      if (epoch.current === requestEpoch) setError(err instanceof Error ? err.message : 'Failed to create the key')
    } finally {
      if (epoch.current === requestEpoch) setSubmitting(false)
    }
  }

  async function handleCopy() {
    if (secret === null) return
    const requestEpoch = epoch.current
    try {
      if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable')
      await navigator.clipboard.writeText(secret)
      if (epoch.current === requestEpoch) setCopy('copied')
    } catch {
      if (epoch.current !== requestEpoch) return
      setCopy('failed')
      setReveal(true)
    }
  }

  function close() {
    epoch.current++
    setClosed(true)
    setSecret(null)
    setSetupGuidance('')
    setReveal(false)
    onClose()
  }

  if (closed) return null

  return (
    <div className={styles.overlay}>
      <div className={styles.modal} role="dialog" aria-modal="true" aria-labelledby={titleId}>
        <h2 id={titleId} className={styles.title}>
          Add runner
        </h2>
        {secret === null ? (
          <>
            <label className={styles.label}>
              Runner name
              <input
                className={styles.input}
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="mac-mini"
                autoComplete="off"
                spellCheck={false}
              />
            </label>
            <p className={styles.hint}>Lowercase letters, digits and dashes; must be unique.</p>
            {error && (
              <div className={styles.error} role="alert">
                Create failed: {error}
              </div>
            )}
            <div className={styles.actions}>
              <button type="button" className={styles.button} onClick={close}>
                Cancel
              </button>
              <button type="button" className={styles.button} disabled={!valid || submitting} onClick={() => void handleCreate()}>
                Create
              </button>
            </div>
          </>
        ) : (
          <>
            <div className={styles.warning} role="alert">
              This key won't be shown again. Copy it now and store it only in the runner's runner.env.
            </div>
            <div className={styles.label}>
              <span>Runner key</span>
              <span className={styles.secretRow}>
                <input
                  aria-label="Runner key"
                  className={styles.input}
                  type={reveal ? 'text' : 'password'}
                  value={secret}
                  readOnly
                  autoComplete="off"
                  spellCheck={false}
                />
                <button type="button" className={styles.button} onClick={() => setReveal((r) => !r)}>
                  {reveal ? 'Hide' : 'Show'}
                </button>
                <button type="button" className={styles.button} onClick={() => void handleCopy()}>
                  Copy
                </button>
              </span>
            </div>
            {copy !== 'idle' && (
              <div className={styles.status} role="status">
                {copy === 'copied' ? 'Copied' : 'Copy failed — select the key and copy it manually. It stays visible until you close this dialog.'}
              </div>
            )}
            <p className={styles.hint}>{setupGuidance}</p>
            <p className={styles.hint}>Put these lines in ~/.arbitron-runner/runner.env on the new machine (mode 0600):</p>
            <pre className={styles.snippet} data-testid="runner-env">
              {runnerEnvSnippet(serverUrl)}
            </pre>
            <p className={styles.hint} data-testid="workspace-root">
              {workspaceRootGuidance(workspaceRoot)}
            </p>
            <div className={styles.actions}>
              <button type="button" className={styles.button} onClick={close}>
                Done
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}
