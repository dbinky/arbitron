import { useState, useEffect, useCallback } from 'react'
import type { SystemConfig, WebSocketEvent } from '../api/types'
import { api } from '../api/client'
import type { SystemSettings, SystemSettingsPatch } from '../api/client'
import { useSystemWebSocket } from '../hooks/useSystemWebSocket'
import styles from './Settings.module.css'

function ModelAnnouncementSettings() {
  const [modelSettings, setModelSettings] = useState<SystemSettings | null>(null)
  const [catalogUrl, setCatalogUrl] = useState('')
  const [webhook, setWebhook] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [message, setMessage] = useState<string | null>(null)

  const apply = useCallback((data: SystemSettings) => {
    setModelSettings(data)
    setCatalogUrl(data.catalogUrl)
    setWebhook('')
  }, [])

  useEffect(() => {
    let cancelled = false
    api.system.getSettings().then((data) => {
      if (!cancelled) apply(data)
    }).catch((err: unknown) => {
      if (!cancelled) setError(err instanceof Error ? err.message : 'Failed to load model settings')
    })
    return () => { cancelled = true }
  }, [apply])

  const refresh = async () => {
    setBusy(true)
    setError(null)
    setMessage(null)
    try {
      apply(await api.system.getSettings())
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load model settings')
    } finally {
      setBusy(false)
    }
  }

  const save = async (patch: SystemSettingsPatch) => {
    setBusy(true)
    setError(null)
    setMessage(null)
    try {
      apply(await api.system.updateSettings(patch))
      setMessage('Saved successfully.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Save failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className={styles.card}>
      <div className={styles.cardHeader}>Model Announcements</div>
      <div className={styles.cardBody}>
        <div className={styles.fieldRow}>
          <span className={styles.fieldLabel}>Status</span>
          <span className={styles.fieldValue}>{modelSettings ? (modelSettings.teamsConfigured ? 'Configured' : 'Not configured') : 'Unavailable'}</span>
        </div>
        <div className={styles.fieldRow}>
          <label className={styles.fieldLabel} htmlFor="model-catalog-url">Catalog URL</label>
          <input
            className={styles.urlInput}
            id="model-catalog-url"
            type="url"
            value={catalogUrl}
            onChange={(e) => setCatalogUrl(e.target.value)}
            disabled={busy || !modelSettings}
          />
        </div>
        <div className={styles.fieldRow}>
          <label className={styles.fieldLabel} htmlFor="model-webhook-url">Replacement webhook URL</label>
          <input
            className={styles.urlInput}
            id="model-webhook-url"
            type="password"
            placeholder="Leave blank to keep current"
            value={webhook}
            onChange={(e) => setWebhook(e.target.value)}
            disabled={busy || !modelSettings}
          />
        </div>
        <div className={styles.saveRow}>
          {message && <span className={styles.saveMessage}>{message}</span>}
          {error && <span className={styles.saveError} role="alert">{error}</span>}
          <button className={styles.saveButton} disabled={busy} onClick={() => void refresh()}>
            Refresh
          </button>
          <button
            className={styles.saveButton}
            disabled={busy || !modelSettings}
            onClick={() => void save({ teamsWebhookUrl: '' })}
          >
            Clear webhook
          </button>
          <button
            className={styles.saveButton}
            disabled={busy || !modelSettings}
            onClick={() => void save({
              catalogUrl,
              ...(webhook.trim() ? { teamsWebhookUrl: webhook.trim() } : {}),
            })}
          >
            {busy ? 'Saving...' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  )
}

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

  // Editable Teams notification config.
  const [teamsEnabled, setTeamsEnabled] = useState(false)
  const [teamsConfigured, setTeamsConfigured] = useState(false)
  const [teamsWebhook, setTeamsWebhook] = useState('')
  const [teamsSaving, setTeamsSaving] = useState(false)
  const [teamsSaveMsg, setTeamsSaveMsg] = useState<string | null>(null)
  const [teamsSaveErr, setTeamsSaveErr] = useState<string | null>(null)

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
          setTeamsEnabled(data.teams.enabled)
          setTeamsConfigured(data.teams.webhookUrl !== '')
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
    // Refresh on any event that causes an instance to enter or leave a queue slot.
    if (
      event.type === 'instance.created' ||
      event.type === 'instance.started' ||
      event.type === 'instance.completed' ||
      event.type === 'instance.failed' ||
      event.type === 'instance.cancelled' ||
      event.type === 'instance.paused' ||
      event.type === 'instance.resumed'
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

  const handleSaveTeams = useCallback(async () => {
    setTeamsSaving(true)
    setTeamsSaveMsg(null)
    setTeamsSaveErr(null)
    try {
      const body: { webhookUrl?: string; enabled?: boolean } = { enabled: teamsEnabled }
      const trimmed = teamsWebhook.trim()
      if (trimmed !== '') body.webhookUrl = trimmed
      await api.system.updateTeamsConfig(body)
      if (trimmed !== '') setTeamsConfigured(true)
      setTeamsWebhook('')
      setTeamsSaveMsg('Saved successfully.')
    } catch (err) {
      setTeamsSaveErr(err instanceof Error ? err.message : 'Save failed')
    } finally {
      setTeamsSaving(false)
    }
  }, [teamsEnabled, teamsWebhook])

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

      {/* Teams Notifications (editable) */}
      <div className={styles.card}>
        <div className={styles.cardHeader}>Teams Notifications</div>
        <div className={styles.cardBody}>
          <div className={styles.fieldRow}>
            <span className={styles.fieldLabel}>Status</span>
            <span className={styles.fieldValue}>
              {teamsConfigured ? 'Configured' : 'Not configured'}
            </span>
          </div>
          <div className={styles.fieldRow}>
            <span className={styles.fieldLabel}>Webhook URL</span>
            <input
              className={styles.numberInput}
              type="text"
              placeholder={teamsConfigured ? 'Leave blank to keep current' : 'https://...'}
              value={teamsWebhook}
              onChange={(e) => setTeamsWebhook(e.target.value)}
            />
          </div>
          <div className={styles.fieldRow}>
            <span className={styles.fieldLabel}>Enabled</span>
            <input
              type="checkbox"
              checked={teamsEnabled}
              onChange={(e) => setTeamsEnabled(e.target.checked)}
            />
          </div>
          <div className={styles.fieldRow}>
            <span className={styles.fieldLabel}>Public Base URL</span>
            <span className={styles.fieldValue}>
              {config.server.publicBaseUrl || '(not set — deep links disabled)'}
            </span>
          </div>
          <div className={styles.saveRow}>
            {teamsSaveMsg && <span className={styles.saveMessage}>{teamsSaveMsg}</span>}
            {teamsSaveErr && <span className={styles.saveError}>{teamsSaveErr}</span>}
            <button
              className={styles.saveButton}
              onClick={handleSaveTeams}
              disabled={teamsSaving}
            >
              {teamsSaving ? 'Saving...' : 'Save'}
            </button>
          </div>
        </div>
      </div>

      <ModelAnnouncementSettings />

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
