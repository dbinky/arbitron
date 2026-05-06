import { useState, useEffect, useCallback } from 'react'
import type { SystemConfig, WebSocketEvent } from '../api/types'
import { api } from '../api/client'
import { useSystemWebSocket } from '../hooks/useSystemWebSocket'
import styles from './Settings.module.css'

export default function Settings() {
  const [config, setConfig] = useState<SystemConfig | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Editable queue config fields.
  const [globalCap, setGlobalCap] = useState(0)
  const [perUserCap, setPerUserCap] = useState(0)
  const [saving, setSaving] = useState(false)
  const [saveMsg, setSaveMsg] = useState<string | null>(null)
  const [saveErr, setSaveErr] = useState<string | null>(null)

  // Live queue status counts.
  const [runningCount, setRunningCount] = useState(0)
  const [queuedCount, setQueuedCount] = useState(0)
  const [waitingCount, setWaitingCount] = useState(0)

  useEffect(() => {
    let cancelled = false
    async function load() {
      try {
        const data = await api.system.getConfig()
        if (!cancelled) {
          setConfig(data)
          setGlobalCap(data.queue.globalCap)
          setPerUserCap(data.queue.perUserCap)
          setRunningCount(data.queue.runningCount)
          setQueuedCount(data.queue.queuedCount)
          setWaitingCount(data.queue.waitingCount)
        }
      } catch (err) {
        if (!cancelled)
          setError(
            err instanceof Error ? err.message : 'Failed to load config',
          )
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [])

  // Live-update queue status via WebSocket.
  const handleWsEvent = useCallback((event: WebSocketEvent) => {
    if (event.type === 'queue.status') {
      const data = event.data as {
        runningCount?: number
        queuedCount?: number
        waitingCount?: number
      } | null
      if (data) {
        if (data.runningCount != null) setRunningCount(data.runningCount)
        if (data.queuedCount != null) setQueuedCount(data.queuedCount)
        if (data.waitingCount != null) setWaitingCount(data.waitingCount)
      }
    }
    // Also refresh on any instance state change to update counts.
    if (
      event.type === 'instance.state_changed' ||
      event.type === 'instance.created'
    ) {
      api.system.getConfig().then((cfg) => {
        setRunningCount(cfg.queue.runningCount)
        setQueuedCount(cfg.queue.queuedCount)
        setWaitingCount(cfg.queue.waitingCount)
      }).catch(console.error)
    }
  }, [])

  const { connected } = useSystemWebSocket(handleWsEvent)

  const handleSave = useCallback(async () => {
    setSaving(true)
    setSaveMsg(null)
    setSaveErr(null)
    try {
      await api.system.updateQueueConfig(globalCap, perUserCap)
      setSaveMsg('Saved successfully.')
    } catch (err) {
      setSaveErr(err instanceof Error ? err.message : 'Save failed')
    } finally {
      setSaving(false)
    }
  }, [globalCap, perUserCap])

  if (loading) {
    return <div className={styles.loading}>Loading settings...</div>
  }

  if (error || !config) {
    return <div className={styles.error}>{error ?? 'Failed to load'}</div>
  }

  return (
    <div className={styles.page}>
      <h1 className={styles.title}>Settings</h1>

      {/* Server (read-only) */}
      <div className={styles.card}>
        <div className={styles.cardHeader}>Server</div>
        <div className={styles.cardBody}>
          <div className={styles.fieldRow}>
            <span className={styles.fieldLabel}>Host</span>
            <span className={styles.fieldValue}>{config.server.host}</span>
          </div>
          <div className={styles.fieldRow}>
            <span className={styles.fieldLabel}>Port</span>
            <span className={styles.fieldValue}>{config.server.port}</span>
          </div>
        </div>
      </div>

      {/* Queue Configuration (editable) */}
      <div className={styles.card}>
        <div className={styles.cardHeader}>Queue Configuration</div>
        <div className={styles.cardBody}>
          <div className={styles.fieldRow}>
            <span className={styles.fieldLabel}>Global Cap</span>
            <input
              className={styles.numberInput}
              type="number"
              min={0}
              value={globalCap}
              onChange={(e) => setGlobalCap(Number(e.target.value))}
            />
          </div>
          <div className={styles.fieldRow}>
            <span className={styles.fieldLabel}>Per-User Cap</span>
            <input
              className={styles.numberInput}
              type="number"
              min={0}
              value={perUserCap}
              onChange={(e) => setPerUserCap(Number(e.target.value))}
            />
          </div>
          <div className={styles.saveRow}>
            {saveMsg && <span className={styles.saveMessage}>{saveMsg}</span>}
            {saveErr && <span className={styles.saveError}>{saveErr}</span>}
            <button
              className={styles.saveButton}
              onClick={handleSave}
              disabled={saving}
            >
              {saving ? 'Saving...' : 'Save'}
            </button>
          </div>
        </div>
      </div>

      {/* Queue Status (live) */}
      <div className={styles.card}>
        <div className={styles.cardHeader}>
          Queue Status
          <span
            className={`${styles.connDot} ${connected ? styles.connDotConnected : styles.connDotDisconnected}`}
            title={connected ? 'Live' : 'Disconnected'}
          />
        </div>
        <div className={styles.cardBody}>
          <div className={styles.statusGrid}>
            <div className={styles.statusCard}>
              <div className={styles.statusLabel}>Running</div>
              <div className={`${styles.statusValue} ${styles.statusRunning}`}>
                {runningCount}
              </div>
            </div>
            <div className={styles.statusCard}>
              <div className={styles.statusLabel}>Queued</div>
              <div className={`${styles.statusValue} ${styles.statusQueued}`}>
                {queuedCount}
              </div>
            </div>
            <div className={styles.statusCard}>
              <div className={styles.statusLabel}>Waiting</div>
              <div className={`${styles.statusValue} ${styles.statusWaiting}`}>
                {waitingCount}
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
