import { memo } from 'react'
import { Link } from 'react-router-dom'
import { Handle, Position } from '@xyflow/react'
import type { NodeProps, Node } from '@xyflow/react'
import type {
  GroupContainerData,
  GroupEndpointData,
  GroupLiveTask,
  GroupTaskCardData,
} from '../groupLayout'
import { gatherText, progressText, ruleText, stateText, unfinishedText, waitingReasonText } from '../groupText'
import styles from './GroupContainerNode.module.css'
import ReviewedGroupElement from '../arranged/ReviewedGroupElement'

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

function statusClass(status: string): string {
  return STATUS_CLASS[status] ?? styles.statusPending
}

type GroupContainerNodeType = Node<GroupContainerData, 'groupContainer'>

/** Non-interactive box around a group's header, bodies/task cards and footer. */
function GroupContainerNode({ data }: NodeProps<GroupContainerNodeType>) {
  if (data.reviewed) return <ReviewedGroupElement data={data.reviewed} />
  return (
    <div className={`${styles.container} ${data.form === 'workflows' ? styles.containerWorkflows : ''}`} data-layout-instance={data.layoutInstanceId} data-layout-mode="fallback">
      <span className={styles.containerLabel}>{data.label}</span>
    </div>
  )
}

type GroupHeaderNodeType = Node<GroupEndpointData, 'groupHeader'>

/** Fan-out header: the group's entry point; task edges leave it. */
function GroupHeaderNodeImpl({ data, selected }: NodeProps<GroupHeaderNodeType>) {
  if (data.reviewed) return <ReviewedGroupElement data={data.reviewed} selected={selected} />
  return (
    <div className={`${styles.endpoint} ${selected ? styles.selected : ''}`} data-testid={`group-header-${data.definitionNodeId}`} data-layout-instance={data.layoutInstanceId} data-layout-mode="fallback">
      <Handle type="target" position={Position.Top} />
      <div className={styles.typeLabel}>parallel group · fan-out</div>
      <div className={styles.title}>{data.label}</div>
      <div className={styles.meta}>
        {data.taskKeys.length} {data.taskKeys.length === 1 ? 'task' : 'tasks'} · {data.form}
      </div>
      <Handle type="source" position={Position.Bottom} />
    </div>
  )
}

type GroupFooterNodeType = Node<GroupEndpointData, 'groupFooter'>

/** Gather footer: rule, progress, winner and file/resolution activity; success/failure leave it. */
function GroupFooterNodeImpl({ data, selected }: NodeProps<GroupFooterNodeType>) {
  if (data.reviewed) return <ReviewedGroupElement data={data.reviewed} selected={selected} />
  const live = data.live
  return (
    <div className={`${styles.endpoint} ${styles.footer} ${selected ? styles.selected : ''}`} data-testid={`group-footer-${data.definitionNodeId}`} data-layout-instance={data.layoutInstanceId} data-layout-mode="fallback">
      <Handle type="target" position={Position.Top} />
      <div className={styles.typeLabel}>gather · {ruleText(data.completionRule)}</div>
      <div className={styles.meta}>
        {unfinishedText(data.unfinishedBehavior)} · {gatherText(data.gatherMode)}
      </div>
      {live && (
        <div className={styles.liveRow}>
          <span className={`${styles.status} ${statusClass(live.state)}`}>{stateText(live.state)}</span>
          {live.progress && <span className={styles.meta}>{progressText(live.progress)}</span>}
        </div>
      )}
      {live?.winner && <div className={styles.winner}>winner: {live.winner}</div>}
      {live?.activity && <div className={styles.activity}>{live.activity}</div>}
      {!live?.activity && live?.waitingReason && <div className={styles.activity}>{waitingReasonText(live.waitingReason)}</div>}
      <Handle type="source" position={Position.Bottom} />
    </div>
  )
}

function TaskBadges({ live }: { live: GroupLiveTask }) {
  return (
    <div className={styles.badges}>
      {live.winner && <span className={`${styles.badge} ${styles.badgeWinner}`}>winner</span>}
      {live.continuingIndependently && <span className={`${styles.badge} ${styles.badgeIndependent}`}>non-blocking</span>}
      {live.cancelRequested && <span className={`${styles.badge} ${styles.badgeCancel}`}>cancel requested</span>}
    </div>
  )
}

type GroupTaskCardNodeType = Node<GroupTaskCardData, 'groupTaskCard'>

/**
 * One workflows-form task, in author order. Static views link the child
 * workflow definition; live views link the child run and show its active nodes.
 * A redacted task shows a generic hidden dependency and no identifiers.
 */
function GroupTaskCardNodeImpl({ data, selected }: NodeProps<GroupTaskCardNodeType>) {
  if (data.reviewed) return <ReviewedGroupElement data={data.reviewed} selected={selected} />
  const live = data.live
  const hidden = live?.redacted === true
  const classes = [
    styles.card,
    live ? statusClass(live.status) : '',
    live?.continuingIndependently ? styles.cardIndependent : '',
    selected ? styles.selected : '',
  ].filter(Boolean).join(' ')
  return (
    <div className={classes} data-testid={`group-task-${data.definitionNodeId}-${data.taskKey}`} data-layout-instance={data.layoutInstanceId} data-layout-mode="fallback">
      <Handle type="target" position={Position.Top} />
      <div className={styles.typeLabel}>task {data.index + 1} · {data.taskKey}</div>
      {hidden ? (
        <div className={styles.hidden}>hidden dependency</div>
      ) : (
        <>
          <div className={styles.title}>
            {live?.childInstanceId ? (
              <Link to={`/instances/${live.childInstanceId}`} className={styles.link} onClick={(e) => e.stopPropagation()}>
                {data.workflow || 'child run'}
              </Link>
            ) : data.workflow ? (
              <Link to={`/workflows/${data.workflow}`} className={styles.link} onClick={(e) => e.stopPropagation()}>
                {data.workflow}
              </Link>
            ) : (
              data.taskKey
            )}
          </div>
          {live && (
            <div className={styles.liveRow}>
              <span className={`${styles.status} ${statusClass(live.status)}`}>{stateText(live.status)}</span>
              {live.frozenStatus && live.frozenStatus !== live.status && (
                <span className={styles.meta}>gathered as {live.frozenStatus}</span>
              )}
            </div>
          )}
          {live && live.activeNodes.length > 0 && (
            <div className={styles.activeNodes}>active: {live.activeNodes.join(', ')}</div>
          )}
          {live?.launchError && <div className={styles.error}>{live.launchError}</div>}
        </>
      )}
      {live && <TaskBadges live={live} />}
      <Handle type="source" position={Position.Bottom} />
    </div>
  )
}

export const GroupLaneNode = memo(function GroupLaneNode({ data }: NodeProps<GroupContainerNodeType>) {
  return data.reviewed ? <ReviewedGroupElement data={data.reviewed} /> : null
})
export const GroupHeaderNode = memo(GroupHeaderNodeImpl)
export const GroupFooterNode = memo(GroupFooterNodeImpl)
export const GroupTaskCardNode = memo(GroupTaskCardNodeImpl)
export default memo(GroupContainerNode)
