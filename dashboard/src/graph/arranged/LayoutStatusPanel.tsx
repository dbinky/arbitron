import { ACTIVE_STATUSES, type DisplayStatus, type LayoutFinding, type LayoutView } from './types'
import styles from './LayoutStatusPanel.module.css'

export interface LayoutStatusPanelProps {
  view: LayoutView | null
  error: string | null
  onRetry: () => void
  retrying: boolean
  retryError: string | null
  canRetry: boolean
  /** Client-side problems drawing view.geometry (resolveDisplayGeometry issues); never shown as reviewed. */
  displayIssues?: readonly string[]
}

type Tone = 'active' | 'ok' | 'attention' | 'failed'

const TITLES: Record<Exclude<DisplayStatus, 'legacy'>, { title: string; tone: Tone }> = {
  pending: { title: 'Arrangement pending', tone: 'active' },
  arranging: { title: 'Arranging layout', tone: 'active' },
  rendering: { title: 'Rendering for review', tone: 'active' },
  reviewing: { title: 'Reviewing readability', tone: 'active' },
  complete: { title: 'Layout reviewed', tone: 'ok' },
  needs_attention: { title: 'Layout needs attention', tone: 'attention' },
  failed: { title: 'Layout arrangement failed', tone: 'failed' },
  unavailable: { title: 'Arrangement status unavailable', tone: 'failed' },
}

const TONE_CLASS: Record<Tone, string> = {
  active: styles.toneActive,
  ok: styles.toneOk,
  attention: styles.toneAttention,
  failed: styles.toneFailed,
}

const IN_PROGRESS: ReadonlySet<DisplayStatus> = new Set(['arranging', 'rendering', 'reviewing'])

function sortedFindings(findings: LayoutFinding[] | null): LayoutFinding[] {
  const list = findings ?? []
  return [...list.filter((f) => f.severity === 'blocking'), ...list.filter((f) => f.severity !== 'blocking')]
}

/**
 * Truthful arrangement/review status for the displayed revision. "Layout
 * reviewed" appears only for displayStatus complete with the reviewed geometry
 * actually drawn; needs_attention (including unfinished superseded work) and
 * any client-side display problem never read as reviewed.
 */
export default function LayoutStatusPanel({
  view,
  error,
  onRetry,
  retrying,
  retryError,
  canRetry,
  displayIssues = [],
}: LayoutStatusPanelProps) {
  if (view?.displayStatus === 'legacy' && !error && displayIssues.length === 0) return null

  let heading: { title: string; tone: Tone }
  if (!view) {
    heading = error
      ? { title: 'Arrangement status unavailable', tone: 'failed' }
      : { title: 'Checking arrangement status', tone: 'active' }
  } else if (view.displayStatus === 'legacy') {
    // Only reached with an error or display issue: a legacy revision has no arrangement to report.
    heading = TITLES.unavailable
  } else {
    heading = TITLES[view.displayStatus]
    if (view.displayStatus === 'complete' && (displayIssues.length > 0 || view.unreviewed || !view.geometry || view.provenance !== 'arranged' || !view.available)) {
      heading = TITLES.needs_attention
    }
  }

  const showAttempt = view != null && IN_PROGRESS.has(view.displayStatus) && view.attemptLimit > 0
  const notReviewed =
    view != null &&
    (view.provenance === 'retained_geometry' ||
      view.provenance === 'retained_positions' ||
      view.unreviewed ||
      displayIssues.length > 0)
  const findings = sortedFindings(view?.findings ?? null)
  const showRetry = canRetry && ((view?.retryEligible ?? false) || error != null || view?.available === false)

  return (
    <div role="status" aria-live="polite" className={`${styles.panel} ${TONE_CLASS[heading.tone]}`}>
      <div className={styles.header}>
        <span className={styles.title}>{heading.title}</span>
        {showAttempt && (
          <span className={styles.attempt}>
            attempt {Math.max(1, view!.attemptsUsed)} of {view!.attemptLimit}
          </span>
        )}
        <span className={styles.spacer} />
        {showRetry && (
          <button type="button" className={styles.retryButton} onClick={onRetry} disabled={retrying}>
            {retrying ? 'Retrying…' : 'Retry arrangement'}
          </button>
        )}
      </div>
      {notReviewed && (
        <div className={styles.note}>Showing the previous layout — not yet reviewed for this revision</div>
      )}
      {view?.failureReason && !ACTIVE_STATUSES.has(view.displayStatus) && (
        <div className={styles.problem}>
          Reason{view.failureStage ? ` (${view.failureStage})` : ''}: {view.failureReason}
        </div>
      )}
      {view?.availabilityError && <div className={styles.problem}>{view.availabilityError}</div>}
      {displayIssues.map((issue) => (
        <div key={issue} className={styles.problem}>
          This layout could not be displayed: {issue}
        </div>
      ))}
      {error && <div className={styles.problem}>Could not refresh arrangement status: {error}</div>}
      {retryError && <div className={styles.problem}>Retry failed: {retryError}</div>}
      {findings.length > 0 && (
        <ul className={styles.findings}>
          {findings.map((f, i) => (
            <li
              key={`${f.criterion}-${i}`}
              className={`${styles.finding} ${f.severity === 'blocking' ? styles.blocking : styles.advisory}`}
              data-severity={f.severity}
            >
              <span className={styles.criterion}>{f.criterion}</span> ({f.severity}): {f.evidence}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
