import type { NotificationItem } from '../api/types'
import { relativeTime } from '../utils/time'
import styles from './NotificationBell.module.css'

/** Map notification type to a compact icon. */
function typeIcon(type: string): string {
  switch (type) {
    case 'failed':
      return '⚠'
    case 'completed':
      return '✓'
    case 'needs_interaction':
      return '💬'
    default:
      return '🔔'
  }
}

/** Return the first 8 characters of an instance ID for display. */
function shortId(id: string): string {
  return id.slice(0, 8)
}

interface NotificationPopoverProps {
  items: NotificationItem[]
  dismissedItems: NotificationItem[]
  showDismissed: boolean
  hasMoreDismissed: boolean
  onItemClick: (instanceId: string) => void
  onDismiss: (e: React.MouseEvent, id: string) => void
  onShowDismissed: () => void
  onLoadMore: () => void
}

export default function NotificationPopover({
  items,
  dismissedItems,
  showDismissed,
  hasMoreDismissed,
  onItemClick,
  onDismiss,
  onShowDismissed,
  onLoadMore,
}: NotificationPopoverProps) {
  return (
    <div className={styles.popover}>
      <div className={styles.header}>
        <span>Notifications</span>
      </div>

      <div className={styles.list}>
        {items.length === 0 && !showDismissed && (
          <div className={styles.empty}>No notifications</div>
        )}

        {items.map((n) => (
          <div
            key={n.id}
            className={styles.item}
            onClick={() => onItemClick(n.instanceId)}
          >
            <span className={styles.itemIcon}>{typeIcon(n.type)}</span>
            <div className={styles.itemBody}>
              <div className={styles.itemWorkflow}>
                {n.workflowName}{' '}
                <span className={styles.itemMeta}>{shortId(n.instanceId)}</span>
              </div>
              <div className={styles.itemMeta}>{relativeTime(n.createdAt)}</div>
            </div>
            {!n.read && <span className={styles.unreadDot} />}
            <button
              className={styles.dismissButton}
              title="Dismiss"
              aria-label="Dismiss notification"
              onClick={(e) => onDismiss(e, n.id)}
            >
              &times;
            </button>
          </div>
        ))}

        {showDismissed && dismissedItems.length > 0 && (
          <>
            {dismissedItems.map((n) => (
              <div
                key={n.id}
                className={styles.item}
                style={{ opacity: 0.6 }}
                onClick={() => onItemClick(n.instanceId)}
              >
                <span className={styles.itemIcon}>{typeIcon(n.type)}</span>
                <div className={styles.itemBody}>
                  <div className={styles.itemWorkflow}>
                    {n.workflowName}{' '}
                    <span className={styles.itemMeta}>{shortId(n.instanceId)}</span>
                  </div>
                  <div className={styles.itemMeta}>{relativeTime(n.createdAt)}</div>
                </div>
              </div>
            ))}
          </>
        )}
      </div>

      <div className={styles.footer}>
        {!showDismissed ? (
          <button
            className={styles.footerLink}
            onClick={onShowDismissed}
          >
            Show dismissed
          </button>
        ) : hasMoreDismissed ? (
          <button
            className={styles.footerLink}
            onClick={onLoadMore}
          >
            Load more
          </button>
        ) : (
          <span className={styles.footerLink} style={{ cursor: 'default', opacity: 0.5 }}>
            {dismissedItems.length === 0 ? 'No dismissed notifications' : 'All loaded'}
          </span>
        )}
      </div>
    </div>
  )
}
