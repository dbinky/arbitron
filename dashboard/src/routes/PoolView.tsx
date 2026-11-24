import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { telemetryFresh } from '../api/runnerDecode'
import { api } from '../api/client'
import type { Me, PoolEvent, RunnerView } from '../api/types'
import RunnerKeysSection from '../components/RunnerKeysSection'
import DrainProgressView from '../components/DrainProgressView'
import SourceChip from '../components/SourceChip'
import {
  buildAlerts,
  buildModelGroups,
  causeLabel,
  eventLabel,
  formatBytes,
  formatPercent,
  STATUS_TEXT,
  type ModelGroupCard,
  type PoolAlert,
} from '../components/poolModel'
import { useNow } from '../hooks/useNow'
import { POOL_POLL_MS } from '../hooks/useLiveSnapshot'
import { usePool } from '../hooks/usePool'
import { formatCountdown } from '../utils/countdown'
import { relativeTime } from '../utils/time'
import styles from './PoolView.module.css'

function versionText(r: RunnerView): string {
  const harness = Object.entries(r.harnessVersions).map(([name, v]) => `${name} ${v}`)
  return [`runner ${r.runnerVersion || 'unknown'}`, ...harness].join(' · ')
}

function telemetryText(r: RunnerView, now: number): string {
  const t = r.telemetry
  if (!telemetryFresh(t, now)) return 'Telemetry unavailable'
  return `CPU ${formatPercent(t.cpuPercent)} · Mem ${formatBytes(t.memUsedBytes)} / ${formatBytes(t.ramBytes)}`
}

function RunnerCard({ r, now }: { r: RunnerView; now: number }) {
  return (
    <article className={styles.card} data-runner-card={r.id} data-liveness={r.liveness}>
      <header className={styles.cardHeader}>
        <Link to={`/pool/runners/${encodeURIComponent(r.id)}`} className={styles.runnerName}>
          {r.name}
        </Link>
        {r.builtIn && <span className={styles.badge}>built-in</span>}
        <span className={`${styles.badge} ${styles[`live_${r.liveness}`] ?? ''}`} data-badge="liveness">
          {r.liveness}
        </span>
        {r.cordoned && (
          <span className={styles.badge} data-badge="cordoned">
            cordoned
          </span>
        )}
        {r.draining && (
          <span className={styles.badge} data-badge="draining">
            draining
          </span>
        )}
        {r.compat.state !== 'compatible' && (
          <span className={`${styles.badge} ${styles.badgeError}`} data-badge="compat">
            {r.compat.state === 'chat_only' ? 'chat only' : 'incompatible'}
          </span>
        )}
      </header>
      {r.compat.state !== 'compatible' && (
        <p className={styles.compatReason} data-compat-reason="">
          {r.compat.reason || 'No explanation reported'}
        </p>
      )}
      <dl className={styles.facts}>
        <dt>Version</dt>
        <dd data-fact="version">{versionText(r)}</dd>
        <dt>Heartbeat</dt>
        <dd data-fact="heartbeat">{r.lastContact ? relativeTime(r.lastContact, now) : 'no contact yet'}</dd>
        <dt>Slots</dt>
        <dd data-fact="slots">
          {r.slotsUsed}/{r.stepLimit}
        </dd>
        <dt>Machine</dt>
        <dd data-fact="telemetry">{telemetryText(r, now)}</dd>
        <dt>Homed workspaces</dt>
        <dd data-fact="homed">{r.homedWorkspaces}</dd>
      </dl>
      <div className={styles.chips}>
        {r.sources.length === 0 ? <span className={styles.muted}>No sources reported</span> : r.sources.map((s) => <SourceChip key={s.id} source={s} now={now} />)}
      </div>
      <DrainProgressView draining={r.draining} drain={r.drain} />
    </article>
  )
}

function GroupCard({ g, now }: { g: ModelGroupCard; now: number }) {
  const total = g.spread.reduce((n, s) => n + s.inFlight, 0)
  return (
    <article className={styles.card} data-group={g.key} data-pressure={g.pressure ? 'true' : undefined}>
      <header className={styles.cardHeader}>
        <span className={styles.groupLabel}>{g.label}</span>
      </header>
      <dl className={styles.facts}>
        <dt>Healthy</dt>
        <dd data-fact="healthy">
          {g.healthy}/{g.total}
        </dd>
        <dt>In flight</dt>
        <dd data-fact="inflight">{g.inFlight}</dd>
        <dt>Queued</dt>
        <dd data-fact="queued">{g.queued}</dd>
        <dt>Next reset</dt>
        <dd data-fact="next-reset">{g.nextReset ? formatCountdown(g.nextReset, now) : '—'}</dd>
      </dl>
      {g.pressure && (
        <p className={styles.pressure} data-fact="pressure">
          Provider busy · pressure (retried; not exhaustion)
        </p>
      )}
      <div className={styles.spread} aria-label={`Distribution of ${g.label}`}>
        {g.spread.map((s) => (
          <span
            key={s.runnerId}
            className={styles.spreadSegment}
            data-spread-runner={s.runnerId}
            data-count={s.inFlight}
            style={{ width: total > 0 ? `${(s.inFlight / total) * 100}%` : `${100 / g.spread.length}%`, opacity: s.inFlight > 0 ? 1 : 0.25 }}
            title={`${s.runnerName}: ${s.inFlight} in flight`}
          />
        ))}
      </div>
      <ul className={styles.members}>
        {g.members.map((m) => (
          <li key={`${m.runnerId}:${m.source.id}`} data-member={m.source.id} data-status={m.status}>
            {m.runnerName} · {STATUS_TEXT[m.status]}
            {m.eligible ? '' : ' · not accepting work'}
          </li>
        ))}
      </ul>
    </article>
  )
}

function AlertItem({ a, now }: { a: PoolAlert; now: number }) {
  return (
    <li className={`${styles.alert} ${a.severity === 'error' ? styles.alertError : styles.alertWarning}`} data-alert={a.kind}>
      <strong>{a.title}</strong> <span>{a.detail}</span>
      {a.resetAt !== undefined && <span> · {formatCountdown(a.resetAt, now)}</span>}
      {a.runId && (
        <>
          {' '}
          <Link to={`/instances/${encodeURIComponent(a.runId)}`}>Open run</Link>
        </>
      )}
    </li>
  )
}

function EventItem({ ev, names, now }: { ev: PoolEvent; names: Map<string, string>; now: number }) {
  return (
    <li className={styles.event} data-event={ev.id}>
      <time dateTime={ev.at} className={styles.muted}>
        {relativeTime(ev.at, now)}
      </time>{' '}
      <span>{eventLabel(ev.kind)}</span>
      {ev.runnerId && <span> · {names.get(ev.runnerId) ?? ev.runnerId}</span>}
      <span className={styles.cause}> · {causeLabel(ev.cause)}</span>
      {ev.runId && (
        <>
          {' '}
          <Link to={`/instances/${encodeURIComponent(ev.runId)}`}>run</Link>
        </>
      )}
    </li>
  )
}

export default function PoolView() {
  const { state, loading, error, stale, connected, refetch } = usePool()
  const now = useNow(1000)
  const [me, setMe] = useState<Me | null>(null)
  useEffect(() => {
    let cancelled = false
    api
      .me()
      .then((identity) => {
        if (!cancelled) setMe(identity)
      })
      .catch(() => {
        // Unknown identity keeps key management hidden; the pool still renders.
      })
    return () => {
      cancelled = true
    }
  }, [])
  const groups = useMemo(() => (state ? buildModelGroups(state.runners, state.executions) : []), [state])
  const alerts = useMemo(() => (state ? buildAlerts(state.runners, state.executions) : []), [state])
  const names = useMemo(() => new Map((state?.runners ?? []).map((r) => [r.id, r.name])), [state])

  const errorBox = error && (
    <div className={styles.error} role="alert">
      Failed to load the pool: {error}{' '}
      <button type="button" onClick={() => void refetch()}>
        Retry
      </button>
    </div>
  )

  if (!state) {
    return (
      <div className={styles.page} data-view="arbitron-pool-view">
        <h1 className={styles.title}>Pool</h1>
        {loading ? <div className={styles.muted}>Loading pool…</div> : errorBox}
      </div>
    )
  }

  const local = state.runners.length === 1 && state.runners[0].builtIn
  return (
    <div className={styles.page} data-view="arbitron-pool-view">
      <h1 className={styles.title}>Pool</h1>
      {stale && (
        <div className={styles.stale} role="status" data-testid="pool-stale">
          {connected
            ? error ? 'Pool refresh failed — data may be stale.' : 'Refreshing pool — data may be stale.'
            : `Live updates disconnected — data may be stale; refreshing every ${POOL_POLL_MS / 1000}s.`}
        </div>
      )}
      {errorBox}
      {local && (
        <p className={styles.localMode} data-testid="local-mode">
          Local mode: only the built-in runner is in the pool. Add a runner to spread work across machines.
        </p>
      )}

      <section aria-label="Alerts">
        <h2 className={styles.sectionTitle}>Alerts</h2>
        {alerts.length === 0 ? (
          <p className={styles.muted}>No alerts</p>
        ) : (
          <ul className={styles.alerts}>
            {alerts.map((a) => (
              <AlertItem key={a.id} a={a} now={now} />
            ))}
          </ul>
        )}
      </section>

      <section aria-label="Model groups">
        <h2 className={styles.sectionTitle}>Model groups</h2>
        <div className={styles.grid}>
          {groups.map((g) => (
            <GroupCard key={g.key} g={g} now={now} />
          ))}
        </div>
      </section>

      <section aria-label="Runners">
        <h2 className={styles.sectionTitle}>Runners</h2>
        <div className={styles.grid}>
          {state.runners.map((r) => (
            <RunnerCard key={r.id} r={r} now={now} />
          ))}
        </div>
      </section>

      <section aria-label="Recent pool events">
        <h2 className={styles.sectionTitle}>Recent events</h2>
        {state.events.length === 0 ? (
          <p className={styles.muted}>No recent events</p>
        ) : (
          <ol className={styles.events}>
            {state.events.map((ev) => (
              <EventItem key={ev.id} ev={ev} names={names} now={now} />
            ))}
          </ol>
        )}
      </section>

      <RunnerKeysSection me={me} />
    </div>
  )
}
