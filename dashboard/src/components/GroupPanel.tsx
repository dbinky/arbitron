import { Link } from 'react-router-dom'
import type {
  FrozenTask,
  GroupPanel as GroupPanelData,
  GroupPanelTask,
  ResolutionSessionRef,
} from '../api/types'
import { gatherText, progressText, ruleText, stateText, unfinishedText, waitingReasonText } from '../graph/groupText'
import { localDateTime } from '../utils/time'
import styles from './GroupPanel.module.css'

interface GroupPanelProps {
  /** The group run's panel; null when the group has not started (no run yet). */
  panel: GroupPanelData | null
  /** Display name of the group node. */
  label?: string
  /** Opens the interactive conflict-resolution session (the parent's session at the group node). */
  onOpenSession?: (session: ResolutionSessionRef) => void
}

const TERMINAL_TASK = new Set(['completed', 'failed', 'cancelled', 'unfinished'])

const STATUS_CLASS: Record<string, string> = {
  not_started: styles.statusPending,
  queued: styles.statusQueued,
  running: styles.statusRunning,
  waiting: styles.statusWaiting,
  paused: styles.statusWaiting,
  completed: styles.statusCompleted,
  failed: styles.statusFailed,
  cancelled: styles.statusCancelled,
  unfinished: styles.statusCancelled,
  unknown: styles.statusPending,
}

function Status({ value }: { value: string }) {
  return <span className={`${styles.status} ${STATUS_CLASS[value] ?? styles.statusPending}`}>{stateText(value)}</span>
}

/** Blocking tasks whose live status is not terminal yet. */
export function outstandingTasks(panel: GroupPanelData): GroupPanelTask[] {
  return panel.tasks.filter((t) => t.blocking && !TERMINAL_TASK.has(t.liveStatus))
}

function failureText(f: Record<string, unknown> | undefined): string | null {
  if (!f) return null
  const err = typeof f.error === 'string' ? f.error : f.error != null ? JSON.stringify(f.error) : ''
  const node = typeof f.failedNodeId === 'string' && f.failedNodeId ? ` (node ${f.failedNodeId})` : ''
  const state = typeof f.state === 'string' ? f.state : ''
  const text = [state, err].filter(Boolean).join(': ')
  return text ? `${text}${node}` : null
}

function TaskRow({ task, index, frozen }: { task: GroupPanelTask; index: number; frozen: FrozenTask | undefined }) {
  const gathered = task.frozenStatus || frozen?.status || ''
  return (
    <tr data-testid={`group-task-row-${task.key}`} className={task.continuingIndependently ? styles.rowIndependent : undefined}>
      <td className={styles.cellMuted}>{index + 1}</td>
      <td className={styles.cellKey}>{task.key}</td>
      <td className={styles.cellMuted}>{task.kind}</td>
      <td>
        <Status value={task.liveStatus} />
      </td>
      <td>{gathered ? <Status value={gathered} /> : <span className={styles.cellMuted}>—</span>}</td>
      <td className={styles.flags}>
        {task.continuingIndependently && <span className={`${styles.badge} ${styles.badgeIndependent}`}>non-blocking</span>}
        {task.blocking && !TERMINAL_TASK.has(task.liveStatus) && <span className={styles.badge}>blocking</span>}
        {task.cancelRequested && <span className={`${styles.badge} ${styles.badgeCancel}`}>cancel requested</span>}
        {task.launchError && <span className={styles.error}>{task.launchError}</span>}
      </td>
      <td>
        {task.redacted ? (
          <span className={styles.hidden}>hidden dependency</span>
        ) : task.childInstanceId ? (
          <Link to={`/instances/${task.childInstanceId}`} className={styles.link}>
            open run
          </Link>
        ) : (
          <span className={styles.cellMuted}>not launched</span>
        )}
      </td>
    </tr>
  )
}

/**
 * GroupPanel shows one group run: completion rule, unfinished behaviour,
 * gather mode and state; ordered tasks with LIVE status next to the FROZEN
 * gathered status (a former unfinished non-winner may read completed live while
 * the gathered snapshot still says unfinished); progress, outstanding blocking
 * tasks and why the gather is waiting (zero outstanding tasks is not "ready");
 * winner; frozen results/failure details; the file-merge report and resolution
 * history; and a link into the live resolution session. Redacted tasks show a
 * generic hidden dependency and no identifiers.
 */
export default function GroupPanel({ panel, label, onOpenSession }: GroupPanelProps) {
  if (!panel) {
    return (
      <div className={styles.panel} data-testid="group-panel">
        <div className={styles.header}>
          <span className={styles.title}>{label ?? 'Parallel group'}</span>
        </div>
        <div className={styles.empty}>This group has not started yet.</div>
      </div>
    )
  }

  const outstanding = outstandingTasks(panel)
  const frozenByKey = new Map((panel.decision?.tasks ?? []).map((t) => [t.key, t]))
  const gather = panel.gather
  const waiting = panel.state !== 'done' || panel.waitingReasons.length > 0

  return (
    <div className={styles.panel} data-testid="group-panel">
      <div className={styles.header}>
        <span className={styles.title}>{label ?? panel.nodeId}</span>
        <Status value={panel.state} />
      </div>

      <dl className={styles.facts}>
        <dt>Rule</dt>
        <dd>{ruleText(panel.completionRule)}</dd>
        <dt>Unfinished tasks</dt>
        <dd>{unfinishedText(panel.unfinishedBehavior)}</dd>
        <dt>Gather</dt>
        <dd>{gatherText(panel.gatherMode)}</dd>
        <dt>Form</dt>
        <dd>{panel.form}</dd>
      </dl>

      <section className={styles.section}>
        <div className={styles.sectionTitle}>Progress</div>
        <div>{progressText(panel.progress)}</div>
        <div className={styles.cellMuted}>
          {outstanding.length} outstanding blocking {outstanding.length === 1 ? 'task' : 'tasks'}
          {outstanding.length > 0 ? `: ${outstanding.map((t) => t.key).join(', ')}` : ''}
        </div>
      </section>

      {waiting && (
        <section className={styles.section} data-testid="group-waiting">
          <div className={styles.sectionTitle}>Waiting on</div>
          {panel.waitingReasons.length > 0 ? (
            <ul className={styles.list}>
              {panel.waitingReasons.map((r) => (
                <li key={r}>{waitingReasonText(r)}</li>
              ))}
            </ul>
          ) : (
            <div className={styles.cellMuted}>
              {outstanding.length === 0
                ? `No tasks outstanding — the group has not continued yet (${stateText(panel.state)}).`
                : 'Outstanding tasks (see below).'}
            </div>
          )}
        </section>
      )}

      {panel.winner && (
        <div className={styles.winner}>
          Winner: <strong>{panel.winner}</strong>
        </div>
      )}

      {gather?.activeSession && onOpenSession && (
        <button type="button" className={styles.sessionButton} onClick={() => onOpenSession(gather.activeSession!)}>
          Open resolution session
        </button>
      )}

      <section className={styles.section}>
        <div className={styles.sectionTitle}>Tasks</div>
        <table className={styles.table}>
          <thead>
            <tr>
              <th>#</th>
              <th>Task</th>
              <th>Kind</th>
              <th>Live</th>
              <th>Gathered</th>
              <th />
              <th>Run</th>
            </tr>
          </thead>
          <tbody>
            {panel.tasks.map((t, i) => (
              <TaskRow key={t.key} task={t} index={i} frozen={frozenByKey.get(t.key)} />
            ))}
          </tbody>
        </table>
      </section>

      {panel.decision && (
        <section className={styles.section} data-testid="group-decision">
          <div className={styles.sectionTitle}>Gathered outcome (frozen)</div>
          <div>
            <Status value={panel.decision.outcome === 'success' ? 'completed' : 'failed'} /> {panel.decision.outcome} — {panel.decision.reason}
          </div>
          <div className={styles.cellMuted}>decided {localDateTime(panel.decision.decidedAt)}</div>
          <ul className={styles.list}>
            {panel.decision.tasks.map((t) => {
              const failure = failureText(t.failure)
              return (
                <li key={t.key}>
                  <span className={styles.cellKey}>{t.key}</span> <Status value={t.status} />
                  {t.winner && <span className={`${styles.badge} ${styles.badgeWinner}`}>winner</span>}
                  {failure && <div className={styles.error}>{failure}</div>}
                  {t.result && Object.keys(t.result).length > 0 && (
                    <details>
                      <summary>result</summary>
                      <pre className={styles.pre}>{JSON.stringify(t.result, null, 2)}</pre>
                    </details>
                  )}
                </li>
              )
            })}
          </ul>
        </section>
      )}

      {gather && gather.mode === 'results_and_files' && (
        <section className={styles.section} data-testid="group-gather">
          <div className={styles.sectionTitle}>File gathering</div>
          <div>
            {stateText(gather.state)}
            {gather.rollbackStatus ? ` · ${stateText(gather.rollbackStatus)}` : ''}
          </div>
          {gather.error && <div className={styles.error}>{gather.error}</div>}
          <ul className={styles.list}>
            {gather.tasks.map((t) => (
              <li key={t.key}>
                <span className={styles.cellKey}>{t.key}</span> {t.merged ? 'merged' : 'not merged'}
                {t.reason ? ` — ${t.reason}` : ''}
                {t.conflictPaths && t.conflictPaths.length > 0 && <div className={styles.cellMuted}>conflicts: {t.conflictPaths.join(', ')}</div>}
              </li>
            ))}
          </ul>
          {gather.attempts.length > 0 && (
            <>
              <div className={styles.sectionTitle}>Resolution history</div>
              <ul className={styles.list} data-testid="group-resolution-history">
                {gather.attempts.map((a) => (
                  <li key={`${a.mergeIndex}-${a.attempt}`}>
                    merge {a.mergeIndex + 1}, attempt {a.attempt} (task {a.taskKey}): {stateText(a.outcome)}
                    {a.conflictPaths.length > 0 && <div className={styles.cellMuted}>{a.conflictPaths.join(', ')}</div>}
                  </li>
                ))}
              </ul>
            </>
          )}
        </section>
      )}

      {panel.finalOutput && (
        <details className={styles.section}>
          <summary>Gathered output</summary>
          <pre className={styles.pre}>{JSON.stringify(panel.finalOutput, null, 2)}</pre>
        </details>
      )}
    </div>
  )
}
