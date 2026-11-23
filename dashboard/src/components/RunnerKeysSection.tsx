import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import { listRunnerKeys } from '../api/runners'
import type { Me, RunnerKey } from '../api/types'
import { localDateTime, relativeTime } from '../utils/time'
import RunnerKeyAddModal from './RunnerKeyAddModal'
import RunnerKeyRevokeModal from './RunnerKeyRevokeModal'
import styles from './RunnerKeys.module.css'

interface SetupConfig {
  serverUrl: string
  workspaceRoot: string | null
}

function runnerCell(k: RunnerKey) {
  if (k.revokedAt) return <span className={styles.muted}>revoked</span>
  if (k.claimedAt) return <Link to={`/pool/runners/${encodeURIComponent(k.runnerId)}`}>{k.runnerId}</Link>
  return <span className={styles.muted}>unclaimed</span>
}

function AdminRunnerKeys() {
  const [keys, setKeys] = useState<RunnerKey[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [adding, setAdding] = useState(false)
  const [revoking, setRevoking] = useState<RunnerKey | null>(null)
  const [setup, setSetup] = useState<SetupConfig>({ serverUrl: window.location.origin, workspaceRoot: null })

  const load = useCallback(async () => {
    try {
      setKeys(await listRunnerKeys())
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load runner keys')
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    let cancelled = false
    api.system
      .getConfig()
      .then((cfg) => {
        if (cancelled) return
        setSetup({ serverUrl: cfg.server?.publicBaseUrl || window.location.origin, workspaceRoot: cfg.workspace?.path || null })
      })
      .catch(() => {
        // Setup guidance falls back to generic text; key management still works.
      })
    return () => {
      cancelled = true
    }
  }, [])

  return (
    <section className={styles.section} aria-label="Runner keys">
      <div className={styles.sectionHeader}>
        <h2 className={styles.title}>Runner keys</h2>
        <button type="button" className={styles.button} onClick={() => setAdding(true)}>
          Add runner
        </button>
      </div>
      {error && (
        <div className={styles.error} role="alert">
          {error}{' '}
          <button type="button" className={styles.button} onClick={() => void load()}>
            Retry
          </button>
        </div>
      )}
      {keys && keys.length === 0 && <p className={styles.muted}>No runner keys yet</p>}
      {keys && keys.length > 0 && (
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Name</th>
              <th>Runner</th>
              <th>Created</th>
              <th>Last used</th>
              <th aria-label="actions" />
            </tr>
          </thead>
          <tbody>
            {keys.map((k) => (
              <tr key={k.id} data-key-name={k.name}>
                <td>{k.name}</td>
                <td>{runnerCell(k)}</td>
                <td>{localDateTime(k.createdAt)}</td>
                <td>{k.lastUsedAt ? relativeTime(k.lastUsedAt) : 'never'}</td>
                <td>
                  {!k.revokedAt && (
                    <button type="button" className={`${styles.button} ${styles.danger}`} aria-label={`Revoke ${k.name}`} onClick={() => setRevoking(k)}>
                      Revoke
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {adding && (
        <RunnerKeyAddModal
          serverUrl={setup.serverUrl}
          workspaceRoot={setup.workspaceRoot}
          onCreated={(created) => setKeys((prev) => [...(prev ?? []).filter((k) => k.id !== created.id), created])}
          onClose={() => setAdding(false)}
        />
      )}
      {revoking && (
        <RunnerKeyRevokeModal
          runnerKey={revoking}
          onClose={() => setRevoking(null)}
          onRevoked={() => {
            setRevoking(null)
            void load()
          }}
        />
      )}
    </section>
  )
}

/** Admin-only runner key management; the server re-checks authorization on every call. */
export default function RunnerKeysSection({ me }: { me: Me | null }) {
  if (!me?.isAdmin) return null
  return <AdminRunnerKeys />
}
