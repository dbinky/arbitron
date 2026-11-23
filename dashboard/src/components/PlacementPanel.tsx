import { Link } from 'react-router-dom'
import type { Instance, NodeExecution } from '../api/types'
import { SERVER_SIDE_NODE_TYPES } from '../graph/runnerMarks'
import { localDateTime } from '../utils/time'
import { humanBytes } from './executionStatus'
import { OUTCOME_TEXT, RECOVERY_DISCLOSURE } from './poolModel'
import styles from './PlacementPanel.module.css'

/** Shown when a session move could not restore full context (server `lossy`, empty notice). */
export const LOSSY_SESSION_NOTICE =
  'This session moved without full context: it continued from the original prompt and recorded conversation, so tool details and reasoning may be lost.'

const INTERRUPTED = new Set(['runner_lost', 'exhausted', 'logged_out', 'fenced'])
const outcomeText = (o: string) => (OUTCOME_TEXT as Record<string, string>)[o] ?? o

export default function PlacementPanel({
  execution,
  instance,
  nodeId,
  nodeType,
  ownerIsRoute,
}: {
  execution: NodeExecution | null
  instance: Instance
  nodeId: string
  nodeType: string | null
  /** False for a branch/child node: the route instance's workspace home history does not apply. */
  ownerIsRoute: boolean
}) {
  const serverSide = nodeType !== null && SERVER_SIDE_NODE_TYPES.has(nodeType)
  const history = execution?.placementHistory ?? []
  const homes = ownerIsRoute ? instance.workspaceMoves ?? [] : []
  // Session moves and recovery notices are per node execution on the owner's trace (P4 T1, P3 T10).
  const moves = (execution?.sessionMoves ?? []).filter((m) => m.nodeId === nodeId)
  const notices = execution?.recovery ?? []
  const interrupted = history.some((h) => INTERRUPTED.has(h.outcome)) || (execution?.attempt ?? 1) > 1
  const disclosure = notices.length > 0 ? notices[0].text || RECOVERY_DISCLOSURE : execution && interrupted ? RECOVERY_DISCLOSURE : null

  return (
    <section className={styles.panel} aria-label="Placement" data-testid="placement-panel">
      <h3 className={styles.title}>Placement</h3>
      {serverSide ? (
        <p className={styles.muted} data-placement="server">
          Runs on the server (no runner placement).
        </p>
      ) : !execution ? (
        <p className={styles.muted} data-placement="none">
          Not placed yet.
        </p>
      ) : (
        <dl className={styles.facts}>
          <dt>Runner</dt>
          <dd data-placement="runner">
            {execution.runnerId ? <Link to={`/pool/runners/${encodeURIComponent(execution.runnerId)}`}>{execution.runnerId}</Link> : 'not recorded'}
          </dd>
          <dt>Source</dt>
          <dd data-placement="source">{execution.sourceId || '—'}</dd>
          <dt>Reason</dt>
          <dd data-placement="reason">{execution.placementReason || '—'}</dd>
          {execution.sessionPinRunnerId && (
            <>
              <dt>Session pin</dt>
              <dd data-placement="pin">
                {execution.sessionPinRunnerId}
                {execution.sessionGeneration !== undefined ? ` · generation ${execution.sessionGeneration}` : ''}
              </dd>
            </>
          )}
        </dl>
      )}
      {history.length > 0 && (
        <>
          <h4 className={styles.subTitle}>Attempts</h4>
          <ol className={styles.list}>
            {history.map((h, i) => (
              <li key={`${h.jobId}:${h.generation}:${i}`} data-attempt={h.generation}>
                {h.runnerId} · {h.sourceId || '—'} · {h.reason || '—'}
                {h.outcome ? ` · ${outcomeText(h.outcome)}` : ''}
              </li>
            ))}
          </ol>
        </>
      )}
      {homes.length > 0 && (
        <>
          <h4 className={styles.subTitle}>Workspace homes</h4>
          <ol className={styles.list}>
            {homes.map((m, i) => (
              <li key={`${m.generation}:${i}`} data-home-move={m.generation}>
                {m.from} → {m.to} · {m.reason} · {humanBytes(m.bytes)} · {localDateTime(m.at)}
              </li>
            ))}
          </ol>
        </>
      )}
      {moves.map((m, i) => (
        <div key={`${m.id}:${i}`} className={styles.move} data-session-move={m.generation}>
          Session moved {m.fromRunner} → {m.toRunner} · {m.reason}
          {m.lossy && (
            <p className={styles.notice} role="note">
              {m.notice || LOSSY_SESSION_NOTICE}
            </p>
          )}
        </div>
      ))}
      {disclosure && (
        <p className={styles.disclosure} data-recovery-disclosure="">
          {disclosure}
        </p>
      )}
    </section>
  )
}
