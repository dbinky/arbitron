import { useMemo, useState, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ReactFlowProvider } from '@xyflow/react'
import type { NodeExecution } from '../api/types'
import { useGraphState } from '../hooks/useGraphState'
import { findGroupPanel, nodeLabelIn, type GraphSelection } from '../graph/aggregateState'
import { activeNodesOf, EMPTY_DEFINITION, rootDefinitionOf } from '../graph/liveSummary'
import { GraphView } from '../routes/ExecutionView'
import GroupPanel from './GroupPanel'
import styles from './RunLivePane.module.css'

const NO_TRACE: NodeExecution[] = []

interface RunLivePaneProps {
  /** The queue row's run; it and every descendant the caller may see are shown. */
  instanceId: string
}

/**
 * RunLivePane is the queue's live-run view of one row: the AUTHORIZED aggregate
 * of the run and its descendants (P4-T6 useGraphState — subtree events,
 * bounded polling while active, reconnect resync), drawn with the same
 * GraphView/group layout as the run view (P4-T7/P4-T8), an instance-qualified
 * list of every concurrently active node, and the group panel of a selected
 * group header/footer. Selecting a step names the instance that OWNS it and
 * links to that run, never to whichever row was expanded.
 */
export default function RunLivePane({ instanceId }: RunLivePaneProps) {
  const graph = useGraphState(instanceId)
  const navigate = useNavigate()
  const [selection, setSelection] = useState<GraphSelection | null>(null)
  const state = graph.state
  const def = useMemo(() => rootDefinitionOf(state), [state])
  // Active nodes are listed even when the root snapshot is unreadable (labels
  // then fall back to each context's own snapshot, else the node id).
  const active = useMemo(() => (state ? activeNodesOf(state, def ?? EMPTY_DEFINITION) : []), [state, def])
  const labelOf = (sel: GraphSelection) => nodeLabelIn(state, def, sel.instanceId, sel.nodeId)

  let detail: ReactNode = null
  if (state && selection?.kind === 'group') {
    detail = (
      <GroupPanel
        panel={findGroupPanel(state, selection.instanceId, selection.nodeId)}
        label={labelOf(selection)}
        // A resolution session belongs to the group's parent instance: open
        // that run, whose view routes the session through its own stream.
        onOpenSession={(s) => navigate(`/instances/${s.instanceId}`)}
      />
    )
  } else if (state && selection?.kind === 'node') {
    const owner = selection.instanceId ? state.contexts.find((c) => c.instanceId === selection.instanceId) ?? null : null
    detail = (
      <div className={styles.selection} data-testid="run-live-selection">
        <span className={styles.selectionLabel}>{labelOf(selection)}</span>
        {!selection.instanceId || !owner || owner.redacted ? (
          <span className={styles.muted}>No visible run for this step (not started, or hidden)</span>
        ) : (
          <>
            <span className={styles.muted}>
              {owner.kind === 'root' ? 'this run' : `${owner.kind}${owner.taskKey ? ` task ${owner.taskKey}` : ''}`}
            </span>
            <Link to={`/instances/${owner.instanceId}`} className={styles.link}>
              Open run
            </Link>
          </>
        )}
      </div>
    )
  }

  return (
    <div className={styles.pane} data-testid="run-live-pane">
      <div className={styles.header}>
        <span className={styles.title}>Live run</span>
        {state && <span className={styles.muted}>{state.active ? 'active' : 'finished'}</span>}
        <span
          className={`${styles.conn} ${graph.connected ? styles.connOn : ''}`}
          title={graph.connected ? 'Live updates connected' : 'Live updates not connected'}
        />
        <div className={styles.spacer} />
        <Link to={`/instances/${instanceId}`} className={styles.link}>
          Open run view
        </Link>
      </div>

      {graph.error && (
        <div className={styles.error} data-testid="run-live-error">
          {state ? 'Live refresh failed: ' : 'Live view unavailable: '}
          {graph.error}
        </div>
      )}
      {!state && !graph.error && <div className={styles.muted}>Loading live state…</div>}

      {state &&
        (state.warnings ?? []).map((w) => (
          <div key={w} className={styles.warning}>
            {w}
          </div>
        ))}

      {state && (
        <div className={styles.active} data-testid="run-live-active">
          <span className={styles.label}>Active now ({active.length})</span>
          {active.length === 0 ? (
            <span className={styles.muted}>nothing running</span>
          ) : (
            <ul className={styles.activeList}>
              {active.map((a) => (
                <li key={a.key} data-testid="run-live-active-item" className={a.independent ? styles.independent : undefined}>
                  {`${a.label} · ${a.state}${a.taskKey ? ` · task ${a.taskKey}` : ''}${a.independent ? ' · non-blocking' : ''}`}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {state && def && (
        <div className={styles.body}>
          <div className={styles.graph}>
            <ReactFlowProvider>
              <GraphView
                instance={null}
                workflowDef={def}
                trace={NO_TRACE}
                graphState={state}
                onSelect={setSelection}
                selectedNodeId={selection?.rfId ?? null}
              />
            </ReactFlowProvider>
          </div>
          {detail && <div className={styles.detail}>{detail}</div>}
        </div>
      )}
      {state && !def && <div className={styles.muted}>Workflow graph unavailable for this run.</div>}
    </div>
  )
}
