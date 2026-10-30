import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { useParams, Link } from 'react-router-dom'
import { ReactFlowProvider } from '@xyflow/react'
import '@xyflow/react/dist/style.css'

import type { WorkflowDefinition, WorkflowNode } from '../graph/types'
import type { GroupGraph, GroupScope, Me } from '../api/types'
import { api } from '../api/client'
import ArrangedGraph from '../graph/arranged/ArrangedGraph'
import LayoutStatusPanel from '../graph/arranged/LayoutStatusPanel'
import { resolveDisplayGeometry } from '../graph/arranged/validate'
import { selectAnchorNodeId } from '../graph/selectAnchorNodeId'
import { useLayoutStatus } from '../hooks/useLayoutStatus'
import { gatherText, ruleText, unfinishedText } from '../graph/groupText'
import ResizablePanel from '../components/ResizablePanel'
import { SpawnDetails } from '../components/SpawnDetails'
import UsageStats from '../components/UsageStats'
import SchedulesPanel from '../components/SchedulesPanel'
import TriggerModal from '../components/TriggerModal'
import styles from './WorkflowReview.module.css'

/**
 * Whether the caller may retry arrangement of this workflow's saved revision.
 * Mirrors the server's retry authorisation (Service.AuthorizeLayoutRetry, which
 * shares Service.Update's policy): a private workflow is the caller's own and
 * writable; a shared workflow is writable only by an admin (edit-in-place); a
 * legacy (unstamped) workflow has no revision to retry. The server re-checks
 * every retry — this only hides a control the server would refuse.
 */
function canRetryLayout(def: WorkflowDefinition, me: Me | null): boolean {
  switch (def.presentation?.scope) {
    case 'private':
      return true
    case 'shared':
      return me?.isAdmin === true
    default:
      return false
  }
}

function ReviewSidePanel({
  workflowDef,
  selectedNode,
  groupScope,
}: {
  workflowDef: WorkflowDefinition
  selectedNode: WorkflowNode | null
  groupScope: GroupScope | null
}) {
  return (
    <div className={styles.sidePanel}>
      {/* Usage Stats */}
      <div className={styles.section}>
        <UsageStats workflowName={workflowDef.name} />
      </div>

      {/* Schedules (read-only) */}
      <div className={styles.section}>
        <SchedulesPanel workflowName={workflowDef.name} />
      </div>

      {/* Input Schema */}
      <div className={styles.section}>
        <div className={styles.sectionTitle}>Input Schema</div>
        {workflowDef.inputs && workflowDef.inputs.length > 0 ? (
          <div className={styles.inputList}>
            {workflowDef.inputs.map((input) => (
              <div key={input.name} className={styles.inputItem}>
                <span className={styles.inputName}>{input.name}</span>
                <span className={styles.inputType}>{input.type}</span>
                {input.required && (
                  <span className={styles.inputRequired}>required</span>
                )}
                {input.description && (
                  <span className={styles.inputDesc}>
                    - {input.description}
                  </span>
                )}
              </div>
            ))}
          </div>
        ) : (
          <div className={styles.emptySection}>No inputs defined.</div>
        )}
      </div>

      {/* Node Details */}
      <div className={styles.section}>
        <div className={styles.sectionTitle}>Node Details</div>
        {selectedNode ? (
          <>
            <div className={styles.nodeDetail}>
              <div className={styles.nodeDetailRow}>
                <span className={styles.nodeDetailLabel}>ID</span>
                <span className={styles.nodeDetailValue}>
                  {selectedNode.id}
                </span>
              </div>
              <div className={styles.nodeDetailRow}>
                <span className={styles.nodeDetailLabel}>Type</span>
                <span className={styles.nodeDetailValue}>
                  {selectedNode.type}
                </span>
              </div>
              <div className={styles.nodeDetailRow}>
                <span className={styles.nodeDetailLabel}>Label</span>
                <span className={styles.nodeDetailValue}>
                  {selectedNode.label}
                </span>
              </div>
              {selectedNode.description && (
                <div className={styles.nodeDetailRow}>
                  <span className={styles.nodeDetailLabel}>Description</span>
                  <span className={styles.nodeDetailValue}>
                    {selectedNode.description}
                  </span>
                </div>
              )}
              {selectedNode.entrypoint && (
                <div className={styles.nodeDetailRow}>
                  <span className={styles.nodeDetailLabel}>Entrypoint</span>
                  <span className={styles.nodeDetailValue}>Yes</span>
                </div>
              )}
            </div>
            {groupScope && <GroupDetails scope={groupScope} />}
            {selectedNode.type === 'spawn_workflow' && <SpawnDetails config={selectedNode.config} />}
            {Object.keys(selectedNode.config).length > 0 && (
              <div className={styles.configBlock}>
                <div className={styles.sectionTitle}>Config</div>
                <pre className={styles.configPre}>
                  {JSON.stringify(selectedNode.config, null, 2)}
                </pre>
              </div>
            )}
          </>
        ) : (
          <div className={styles.emptySection}>
            Click a node to view its details.
          </div>
        )}
      </div>
    </div>
  )
}

/** Static rule, continuation targets and author-ordered tasks. */
function GroupDetails({ scope }: { scope: GroupScope }) {
  const { config } = scope
  return (
    <div className={styles.section} data-testid="review-group-details">
      <div className={styles.sectionTitle}>Parallel Group</div>
      <div className={styles.nodeDetail}>
        <div className={styles.nodeDetailRow}>
          <span className={styles.nodeDetailLabel}>Form</span>
          <span className={styles.nodeDetailValue}>{config.form === 'workflows' ? 'child workflows' : 'branches'}</span>
        </div>
        <div className={styles.nodeDetailRow}>
          <span className={styles.nodeDetailLabel}>Rule</span>
          <span className={styles.nodeDetailValue}>{ruleText(config.completionRule)}</span>
        </div>
        <div className={styles.nodeDetailRow}>
          <span className={styles.nodeDetailLabel}>Unfinished</span>
          <span className={styles.nodeDetailValue}>{unfinishedText(config.unfinishedBehavior)}</span>
        </div>
        <div className={styles.nodeDetailRow}>
          <span className={styles.nodeDetailLabel}>Gather</span>
          <span className={styles.nodeDetailValue}>{gatherText(config.gatherMode)}</span>
        </div>
        <div className={styles.nodeDetailRow}>
          <span className={styles.nodeDetailLabel}>On success</span>
          <span className={styles.nodeDetailValue}>{scope.successTarget || '—'}</span>
        </div>
        <div className={styles.nodeDetailRow}>
          <span className={styles.nodeDetailLabel}>On failure</span>
          <span className={styles.nodeDetailValue}>{scope.failureTarget || '—'}</span>
        </div>
      </div>
      <ol className={styles.groupTaskList} data-testid="review-group-tasks">
        {/* Keep invalid draft tasks visible, even when their keys repeat. */}
        {config.tasks.map((t, i) => (
          <li key={i} className={styles.groupTaskItem}>
            <span className={styles.inputName}>{t.key}</span>
            {t.workflow ? (
              <Link to={`/workflows/${t.workflow}`} className={styles.groupTaskLink}>
                {t.workflow}
              </Link>
            ) : (
              <span className={styles.inputType}>entry {t.entry}</span>
            )}
          </li>
        ))}
      </ol>
    </div>
  )
}

const GROUP_LAYOUT_TIMEOUT_MS = 15_000

/**
 * Group ownership analysis for the review graph and the group details panel.
 * Failure degrades to the plain graph (and a status line), never a failed page;
 * a deadline bounds a stalled request even if the transport ignores abort.
 */
interface GroupLayoutState {
  graph: GroupGraph | null
  error: string | null
  settled: boolean
}

function useGroupLayout(name: string | undefined): GroupLayoutState {
  const [state, setState] = useState<GroupLayoutState>({ graph: null, error: null, settled: false })
  useEffect(() => {
    if (!name) return
    let cancelled = false
    setState({ graph: null, error: null, settled: false })
    const controller = new AbortController()
    const aborted = new Promise<never>((_, reject) => {
      controller.signal.addEventListener('abort', () => reject(controller.signal.reason), { once: true })
    })
    const deadline = setTimeout(
      () => controller.abort(new Error('Group layout request timed out')),
      GROUP_LAYOUT_TIMEOUT_MS,
    )
    Promise.race([api.workflows.groupLayout(name, controller.signal), aborted])
      .finally(() => clearTimeout(deadline))
      .then(
        (graph) => { if (!cancelled) setState({ graph, error: null, settled: true }) },
        (err: unknown) => {
          if (!cancelled) setState({ graph: null, error: err instanceof Error ? err.message : 'Failed to load group layout', settled: true })
        },
      )
    return () => {
      cancelled = true
      clearTimeout(deadline)
      controller.abort()
    }
  }, [name])
  return state
}

interface ReviewPageProps {
  name: string
  workflowDef: WorkflowDefinition
  /** Group ownership analysis, requested alongside the workflow by the route. */
  groupLayout: GroupLayoutState
  /** Silently re-reads the manifest (keeps the current one on failure). Stable identity. */
  onReloadWorkflow: () => void
}

/**
 * The loaded workflow page. Owns the revision-scoped arrangement status and
 * joins it with the manifest by saved revision: geometry/status are shown only
 * for the manifest's own revision, never for another one.
 */
function ReviewPage({ name, workflowDef, groupLayout, onReloadWorkflow }: ReviewPageProps) {
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
  const [showTrigger, setShowTrigger] = useState(false)
  const [me, setMe] = useState<Me | null>(null)

  useEffect(() => {
    let cancelled = false
    api
      .me()
      .then((m) => {
        if (!cancelled) setMe(m)
      })
      .catch(() => {
        // Unknown identity: admin-only Retry stays hidden; the server still decides.
      })
    return () => {
      cancelled = true
    }
  }, [])

  const layout = useLayoutStatus(
    signal => api.workflows.getLayout(name, signal),
    (expectedRevision) => api.workflows.retryLayout(name, expectedRevision),
    name,
  )
  const { view, refresh: refreshLayout, retry: retryLayout } = layout

  // Join manifest and layout by saved revision. A pre-feature manifest has no
  // presentation and its legacy view has savedRevision "", so they still join.
  const manifestRevision = workflowDef.presentation?.revision ?? ''
  const viewRevision = view?.savedRevision ?? null
  const joinedView = view != null && view.savedRevision === manifestRevision ? view : null

  // A save raced the two reads: re-read both sides once per mismatched
  // (manifest, layout) revision pair. Whichever side was stale catches up; if
  // neither changes, the pair is unchanged and nothing is fetched again.
  const reconciledPairs = useRef(new Set<string>())
  useEffect(() => {
    if (viewRevision == null || viewRevision === manifestRevision) return
    const pair = JSON.stringify([manifestRevision, viewRevision])
    if (reconciledPairs.current.has(pair)) return
    reconciledPairs.current.add(pair)
    onReloadWorkflow()
    refreshLayout()
  }, [manifestRevision, viewRevision, onReloadWorkflow, refreshLayout])

  const [renderIssues, setRenderIssues] = useState<readonly string[]>([])
  const displayIssues = useMemo(
    () => [...resolveDisplayGeometry(workflowDef, joinedView).issues, ...renderIssues],
    [workflowDef, joinedView, renderIssues],
  )
  const anchorNodeId = useMemo(() => selectAnchorNodeId(null, workflowDef, {}, []), [workflowDef])

  const handleNodeClick = useCallback((nodeId: string) => {
    setSelectedNodeId(nodeId)
  }, [])

  const staticGroups = joinedView?.groups ?? groupLayout.graph
  const selectedGroup = (selectedNodeId && staticGroups?.groups[selectedNodeId]) || null
  const hasGroupNodes = workflowDef.nodes.some((n) => n.type === 'parallel_group')

  const handleRetry = useCallback(() => {
    if (joinedView) {
      // useLayoutStatus posts joinedView.savedRevision, i.e. the manifest's revision.
      void retryLayout()
      return
    }
    // No status for this manifest's revision: reload instead of retrying another revision.
    refreshLayout()
    onReloadWorkflow()
  }, [joinedView, retryLayout, refreshLayout, onReloadWorkflow])

  const selectedNode =
    workflowDef.nodes.find((n) => n.id === selectedNodeId) ?? null

  // Wait for the first status response so the first centring is on the layout
  // actually displayed (no dagre flash that then jumps to the arranged result).
  // A group workflow also waits for its group analysis (or its deadline), so
  // the plain graph never flashes before the group boxes replace it.
  const graphReady = (view != null || !layout.loading) && (!hasGroupNodes || joinedView?.groups != null || groupLayout.settled)

  const errorBehavior = workflowDef.defaultErrorBehavior
    ? Object.entries(workflowDef.defaultErrorBehavior)
        .map(([k, v]) => `${k}: ${String(v)}`)
        .join(', ')
    : 'none'

  return (
    <ReactFlowProvider>
      <div className={styles.page}>
        {/* Breadcrumb / metadata bar */}
        <div className={styles.breadcrumb}>
          <Link to="/workflows" className={styles.breadcrumbLink}>
            Workflows
          </Link>
          <span className={styles.breadcrumbSep}>/</span>
          <span className={styles.breadcrumbCurrent}>{workflowDef.name}</span>

          <span className={styles.metaItem}>
            Author: <span className={styles.metaValue}>{workflowDef.author}</span>
          </span>
          <span className={styles.metaItem}>
            Nodes:{' '}
            <span className={styles.metaValue}>
              {workflowDef.nodes.length}
            </span>
          </span>
          <span className={styles.metaItem}>
            Error:{' '}
            <span className={styles.metaValue}>{errorBehavior}</span>
          </span>

          {groupLayout.error && hasGroupNodes && !joinedView?.groups && (
            <span className={styles.metaItem} role="status">
              Group layout unavailable: {groupLayout.error}
            </span>
          )}

          <div className={styles.spacer} />

          <button
            className={styles.triggerButton}
            onClick={() => setShowTrigger(true)}
          >
            Trigger
          </button>
        </div>

        {/* Arrangement / review status for this saved revision */}
        <LayoutStatusPanel
          view={joinedView}
          error={joinedView ? layout.error : (viewRevision != null && viewRevision !== manifestRevision ? 'Arrangement revision changed; reload to refresh this workflow' : layout.error)}
          onRetry={handleRetry}
          retrying={layout.retrying}
          retryError={layout.retryError}
          canRetry={canRetryLayout(workflowDef, me)}
          displayIssues={displayIssues}
        />

        {/* Main content: graph + side panel */}
        <ResizablePanel
          left={
            <div className={styles.graphContainer}>
              {graphReady ? (
                <ArrangedGraph
                  workflowDef={workflowDef}
                  view={joinedView}
                  onDisplayIssues={setRenderIssues}
                  selectedNodeId={selectedNodeId}
                  onNodeClick={handleNodeClick}
                  anchorNodeId={anchorNodeId}
                  groups={staticGroups}
                />
              ) : (
                <div className={styles.loading}>Loading layout...</div>
              )}
            </div>
          }
          right={
            <ReviewSidePanel
              workflowDef={workflowDef}
              selectedNode={selectedNode}
              groupScope={selectedGroup}
            />
          }
        />

        {/* Trigger modal */}
        {showTrigger && (
          <TriggerModal
            workflowName={name}
            inputs={workflowDef.inputs ?? []}
            onClose={() => setShowTrigger(false)}
          />
        )}
      </div>
    </ReactFlowProvider>
  )
}

export default function WorkflowReview() {
  const { name } = useParams<{ name: string }>()
  const [workflowDef, setWorkflowDef] = useState<WorkflowDefinition | null>(
    null,
  )
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  // Requested with the workflow, so its deadline runs from the route load.
  const groupLayout = useGroupLayout(name)

  // Newest response wins; responses for a previous route name are dropped.
  const mountedEpoch = useRef(0)
  const currentName = useRef(name)
  currentName.current = name
  const issued = useRef(0)
  const applied = useRef(0)
  const loadedEpoch = useRef<number | null>(null)

  /**
   * Initial load shows loading/error. A reload (focus, visible, revision
   * mismatch) is silent: it replaces the manifest on success and keeps the
   * current one on failure, so external HTTP/MCP saves appear without flicker.
   */
  const loadWorkflow = useCallback(
    async (initial: boolean) => {
      if (!name) return
      const seq = ++issued.current
      const epoch = mountedEpoch.current
      const stale = () => mountedEpoch.current !== epoch || currentName.current !== name || seq < applied.current
      try {
        const data = await api.workflows.get(name)
        if (stale()) return
        applied.current = seq
        loadedEpoch.current = epoch
        setWorkflowDef(data)
        setError(null)
      } catch (err) {
        if (stale()) return
        applied.current = seq
        if (initial || loadedEpoch.current !== epoch) {
          setError(err instanceof Error ? err.message : 'Failed to load workflow')
        }
      } finally {
        if (!stale()) setLoading(false)
      }
    },
    [name],
  )

  useEffect(() => {
    ++mountedEpoch.current
    setWorkflowDef(null)
    setError(null)
    setLoading(true)
    void loadWorkflow(true)

    const onFocus = () => void loadWorkflow(false)
    const onVisibility = () => {
      if (document.visibilityState === 'visible') void loadWorkflow(false)
    }
    window.addEventListener('focus', onFocus)
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      ++mountedEpoch.current
      window.removeEventListener('focus', onFocus)
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [loadWorkflow])

  const reloadWorkflow = useCallback(() => {
    void loadWorkflow(false)
  }, [loadWorkflow])

  if (!name) {
    return <div className={styles.error}>Workflow not found</div>
  }

  if (loading) {
    return <div className={styles.loading}>Loading workflow...</div>
  }

  if (error) {
    return <div className={styles.error}>{error}</div>
  }

  if (!workflowDef) {
    return <div className={styles.error}>Workflow not found</div>
  }

  return (
    <ReviewPage
      key={name}
      name={name}
      workflowDef={workflowDef}
      groupLayout={groupLayout}
      onReloadWorkflow={reloadWorkflow}
    />
  )
}
