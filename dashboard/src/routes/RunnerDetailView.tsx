import type { ReactNode } from 'react'
import { Link, useParams } from 'react-router-dom'
import { telemetryAvailable, telemetryFresh } from '../api/runnerDecode'
import type { RunnerDetail, RunnerTelemetry } from '../api/types'
import DrainProgressView from '../components/DrainProgressView'
import SourceChip from '../components/SourceChip'
import Sparkline from '../components/Sparkline'
import { causeLabel, eventLabel, formatBytes, formatPercent, OUTCOME_TEXT } from '../components/poolModel'
import { POOL_POLL_MS } from '../hooks/useLiveSnapshot'
import { useNow } from '../hooks/useNow'
import { useRunner } from '../hooks/useRunner'
import { formatDuration, localDateTime, relativeTime } from '../utils/time'
import styles from './RunnerDetailView.module.css'

const dash = (v: string | number | null | undefined) => (v === null || v === undefined || v === '' ? '—' : String(v))

function Stat({ name, label, children }: { name: string; label: string; children: ReactNode }) {
  return (
    <>
      <dt>{label}</dt>
      <dd data-stat={name}>{children}</dd>
    </>
  )
}

function runLink(runId: string | null, text: string) {
  return runId ? <Link to={`/instances/${encodeURIComponent(runId)}`}>{text}</Link> : <span className={styles.muted}>hidden run</span>
}

function TelemetrySection({ d, now }: { d: RunnerDetail; now: number }) {
  const t: RunnerTelemetry | null = telemetryFresh(d.telemetry, now) ? d.telemetry : null
  const samples = d.recentTelemetry
  const values = (metric: 'cpuPercent' | 'memUsedBytes' | 'load1Pct' | 'diskFreeBytes') =>
    samples.map((s) => telemetryFresh(s, now) ? s[metric] : null)
  const ram = t?.ramBytes ?? Math.max(0, ...samples.map((s) => s.ramBytes ?? s.memUsedBytes ?? 0))
  const disk = Math.max(0, ...samples.map((s) => s.diskFreeBytes ?? 0))
  return (
    <section className={styles.section} aria-label="Telemetry">
      <h2 className={styles.sectionTitle}>Telemetry</h2>
      <div className={styles.sparks}>
        <Sparkline label="CPU" values={values('cpuPercent')} max={100} latest={formatPercent(t?.cpuPercent ?? null)} />
        <Sparkline label="Memory" values={values('memUsedBytes')} max={ram} latest={formatBytes(t?.memUsedBytes ?? null)} />
        <Sparkline label="Load" values={values('load1Pct')} max={100} latest={formatPercent(t?.load1Pct ?? null)} />
        <Sparkline label="Free disk" values={values('diskFreeBytes')} max={disk} latest={formatBytes(t?.diskFreeBytes ?? null)} />
      </div>
      {telemetryAvailable(t) ? (
        <dl className={styles.facts}>
          <Stat name="cpu" label="CPU">{formatPercent(t.cpuPercent)}</Stat>
          <Stat name="memory" label="Memory">{`${formatBytes(t.memUsedBytes)} / ${formatBytes(t.ramBytes)}`}</Stat>
          <Stat name="pressure" label="Memory pressure">{dash(t.memPressure)}</Stat>
          <Stat name="load" label="Load (1 min)">{formatPercent(t.load1Pct)}</Stat>
          <Stat name="disk" label="Free workspace disk">{formatBytes(t.diskFreeBytes)}</Stat>
          <Stat name="thermal" label="Thermal state">{dash(t.thermal)}</Stat>
          <Stat name="uptime" label="Uptime">{t.uptimeSeconds === null ? '—' : formatDuration(t.uptimeSeconds * 1000)}</Stat>
          <Stat name="sampled" label="Sampled">{t.sampledAt ? localDateTime(t.sampledAt) : '—'}</Stat>
        </dl>
      ) : (
        <p className={styles.muted}>Telemetry unavailable</p>
      )}
    </section>
  )
}

function MachineSection({ d, now }: { d: RunnerDetail; now: number }) {
  const t = telemetryFresh(d.telemetry, now) ? d.telemetry : null
  const labels = Object.entries(d.labels).map(([k, v]) => `${k}=${v}`).join(', ')
  return (
    <section className={styles.section} aria-label="Machine">
      <h2 className={styles.sectionTitle}>Machine</h2>
      <dl className={styles.facts}>
        <Stat name="os" label="OS">{dash(d.os)}</Stat>
        <Stat name="cores" label="Cores">{dash(t?.cores)}</Stat>
        <Stat name="ram" label="RAM">{formatBytes(t?.ramBytes ?? null)}</Stat>
        <Stat name="runner-version" label="Runner version">{dash(d.runnerVersion)}</Stat>
        {Object.entries(d.harnessVersions).map(([name, v]) => (
          <Stat key={name} name={`harness-${name}`} label={name}>
            {v}
          </Stat>
        ))}
        <Stat name="step-limit" label="Execution limit">{d.stepLimit}</Stat>
        <Stat name="labels" label="Labels">{labels || '—'}</Stat>
        <Stat name="last-contact" label="Last contact">{d.lastContact ? relativeTime(d.lastContact, now) : 'no contact yet'}</Stat>
      </dl>
      <h3 className={styles.subTitle}>Health checks</h3>
      <ul className={styles.list}>
        {d.sources.map((s) => (
          <li key={s.id} data-health-check={s.id}>
            <SourceChip source={s} now={now} /> · {s.health.lastProbeAt ? `last check ${relativeTime(s.health.lastProbeAt, now)}` : 'never checked'}
            {s.health.lastOutcome ? ` · last outcome: ${OUTCOME_TEXT[s.health.lastOutcome] ?? s.health.lastOutcome}` : ''}
            {s.health.headroom !== null ? ` · headroom ${formatPercent(s.health.headroom * 100)}` : ''}
            {s.backends.length > 0 ? ` · ${s.backends.join(', ')}` : ''}
          </li>
        ))}
      </ul>
    </section>
  )
}

function AssignmentsSection({ d }: { d: RunnerDetail }) {
  return (
    <section className={styles.section} aria-label="Assignments">
      <h2 className={styles.sectionTitle}>Assignments</h2>
      <dl className={styles.facts}>
        <Stat name="slots" label="Slots">{`${d.slotsUsed}/${d.stepLimit}`}</Stat>
        <Stat name="homed" label="Homed workspaces">{d.homedWorkspaces}</Stat>
      </dl>
      <h3 className={styles.subTitle}>Running steps</h3>
      {d.assignments.length === 0 ? (
        <p className={styles.muted}>No active steps</p>
      ) : (
        <ul className={styles.list}>
          {d.assignments.map((a) => (
            <li key={a.jobId} data-assignment={a.jobId}>
              {a.runId ? runLink(a.runId, a.nodeId) : runLink(null, '')} · {a.state}
            </li>
          ))}
        </ul>
      )}
      {d.homes.length > 0 && (
        <>
          <h3 className={styles.subTitle}>Homed workspaces</h3>
          <ul className={styles.list}>
            {d.homes.map((h, i) => (
              <li key={`${h.runId ?? 'hidden'}:${i}`} data-home="">
                {runLink(h.runId, h.runId ? `run ${h.runId.slice(0, 8)}` : '')}
                {` · generation ${h.generation}`}
                {h.dirty ? ' · unsynced changes' : ''}
              </li>
            ))}
          </ul>
        </>
      )}
      <h3 className={styles.subTitle}>Pinned interactive sessions</h3>
      {d.sessions.length === 0 ? (
        <p className={styles.muted}>No pinned sessions</p>
      ) : (
        <ul className={styles.list}>
          {d.sessions.map((s, i) => (
            <li key={`${s.runId ?? 'hidden'}:${i}`} data-session-pin="">
              {s.runId ? runLink(s.runId, s.nodeId) : runLink(null, '')}
              {` · generation ${s.generation}`}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

export default function RunnerDetailView() {
  const { id } = useParams<{ id: string }>()
  const { state, loading, error, stale, refetch } = useRunner(id)
  const now = useNow(1000)
  const back = (
    <Link to="/pool" className={styles.back}>
      Back to pool
    </Link>
  )
  const errorBox = error && (
    <div className={styles.error} role="alert">
      Failed to load runner: {error}{' '}
      <button type="button" onClick={() => void refetch()}>
        Retry
      </button>
    </div>
  )
  if (!state) {
    return (
      <div className={styles.page} data-view="arbitron-runner-detail">
        {back}
        {loading ? <p className={styles.muted}>Loading runner…</p> : errorBox}
      </div>
    )
  }
  const d = state.detail
  return (
    <div className={styles.page} data-view="arbitron-runner-detail">
      {back}
      <header className={styles.header}>
        <h1 className={styles.title}>{d.name}</h1>
        {d.builtIn && <span className={styles.badge}>built-in</span>}
        <span className={styles.badge}>{d.liveness}</span>
        {d.cordoned && <span className={styles.badge}>cordoned</span>}
        {d.draining && <span className={styles.badge}>draining</span>}
        {d.compat.state !== 'compatible' && <span className={styles.badge}>{d.compat.state === 'chat_only' ? 'chat only' : 'incompatible'}</span>}
      </header>
      {d.compat.state !== 'compatible' && (
        <p className={styles.error} data-compat-reason="">
          {d.compat.reason || 'No explanation reported'}
        </p>
      )}
      {stale && (
        <div className={styles.stale} role="status" data-testid="runner-stale">
          Live updates disconnected — data may be stale; refreshing every {POOL_POLL_MS / 1000}s.
        </div>
      )}
      {errorBox}
      <TelemetrySection d={d} now={now} />
      <MachineSection d={d} now={now} />
      <AssignmentsSection d={d} />
      <section className={styles.section} aria-label="Drain">
        <h2 className={styles.sectionTitle}>Drain</h2>
        {d.draining || d.drain?.complete ? <DrainProgressView draining={d.draining} drain={d.drain} /> : <p className={styles.muted}>Not draining</p>}
      </section>
      <section className={styles.section} aria-label="Recent events">
        <h2 className={styles.sectionTitle}>Recent events</h2>
        {state.pool.events.length === 0 ? (
          <p className={styles.muted}>No recent events</p>
        ) : (
          <ol className={styles.list}>
            {state.pool.events.map((ev) => (
              <li key={ev.id} data-event={ev.id}>
                {relativeTime(ev.at, now)} · {eventLabel(ev.kind)} · {causeLabel(ev.cause)}
                {ev.runId && (
                  <>
                    {' '}
                    <Link to={`/instances/${encodeURIComponent(ev.runId)}`}>run</Link>
                  </>
                )}
              </li>
            ))}
          </ol>
        )}
      </section>
    </div>
  )
}
