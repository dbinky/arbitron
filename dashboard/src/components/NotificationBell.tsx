import { useState, useEffect, useRef, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { NotificationItem, WebSocketEvent } from '../api/types'
import { useSystemWebSocket } from '../hooks/useSystemWebSocket'
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

const PAGE_SIZE = 20

export default function NotificationBell() {
  const navigate = useNavigate()

  // --- state ---
  const [unreadCount, setUnreadCount] = useState(0)
  const [open, setOpen] = useState(false)
  const [items, setItems] = useState<NotificationItem[]>([])
  const [showDismissed, setShowDismissed] = useState(false)
  const [dismissedItems, setDismissedItems] = useState<NotificationItem[]>([])
  const [dismissedOffset, setDismissedOffset] = useState(0)
  const [hasMoreDismissed, setHasMoreDismissed] = useState(false)
  const wrapperRef = useRef<HTMLDivElement>(null)

  // --- fetch helpers ---
  const fetchUnreadCount = useCallback(async () => {
    try {
      const { count } = await api.notifications.unreadCount()
      setUnreadCount(count)
    } catch {
      // silently ignore
    }
  }, [])

  const fetchActive = useCallback(async () => {
    try {
      const data = await api.notifications.list({ dismissed: false, limit: 50 })
      setItems(data)
    } catch {
      // silently ignore
    }
  }, [])

  const fetchDismissed = useCallback(async (offset: number) => {
    try {
      const data = await api.notifications.list({
        dismissed: true,
        limit: PAGE_SIZE,
        offset,
      })
      if (offset === 0) {
        setDismissedItems(data)
      } else {
        setDismissedItems((prev) => [...prev, ...data])
      }
      setHasMoreDismissed(data.length === PAGE_SIZE)
      setDismissedOffset(offset + data.length)
    } catch {
      // silently ignore
    }
  }, [])

  // --- initial fetch ---
  useEffect(() => {
    void fetchUnreadCount()
  }, [fetchUnreadCount])

  // --- WebSocket events ---
  const handleWsEvent = useCallback(
    (event: WebSocketEvent) => {
      if (event.type === 'notification.created') {
        setUnreadCount((c) => c + 1)
        const item = event.data as NotificationItem
        setItems((prev) => [item, ...prev])
      } else if (event.type === 'notification.read') {
        setUnreadCount(0)
        setItems((prev) => prev.map((n) => ({ ...n, read: true })))
      } else if (event.type === 'notification.dismissed') {
        const payload = event.data as { id: string }
        setItems((prev) => prev.filter((n) => n.id !== payload.id))
      }
    },
    [],
  )

  useSystemWebSocket(handleWsEvent)

  // --- open / close popover ---
  const handleToggle = useCallback(() => {
    setOpen((prev) => {
      const next = !prev
      if (next) {
        // Opening: fetch fresh list and mark-read
        void fetchActive()
        if (unreadCount > 0) {
          void api.notifications.markRead()
          setUnreadCount(0)
          setItems((prev) => prev.map((n) => ({ ...n, read: true })))
        }
      } else {
        // Closing: reset dismissed section
        setShowDismissed(false)
        setDismissedItems([])
        setDismissedOffset(0)
        setHasMoreDismissed(false)
      }
      return next
    })
  }, [fetchActive, unreadCount])

  // --- click outside closes popover ---
  useEffect(() => {
    if (!open) return

    function handleMouseDown(e: MouseEvent) {
      if (
        wrapperRef.current &&
        !wrapperRef.current.contains(e.target as Node)
      ) {
        setOpen(false)
        setShowDismissed(false)
        setDismissedItems([])
        setDismissedOffset(0)
        setHasMoreDismissed(false)
      }
    }

    document.addEventListener('mousedown', handleMouseDown)
    return () => document.removeEventListener('mousedown', handleMouseDown)
  }, [open])

  // --- dismiss ---
  const handleDismiss = useCallback(
    (e: React.MouseEvent, id: string) => {
      e.stopPropagation()
      setItems((prev) => prev.filter((n) => n.id !== id))
      void api.notifications.dismiss(id)
    },
    [],
  )

  // --- navigate to instance ---
  const handleItemClick = useCallback(
    (instanceId: string) => {
      navigate(`/instances/${instanceId}`)
      setOpen(false)
    },
    [navigate],
  )

  // --- show dismissed section ---
  const handleShowDismissed = useCallback(() => {
    setShowDismissed(true)
    void fetchDismissed(0)
  }, [fetchDismissed])

  const handleLoadMore = useCallback(() => {
    void fetchDismissed(dismissedOffset)
  }, [fetchDismissed, dismissedOffset])

  return (
    <div className={styles.wrapper} ref={wrapperRef}>
      <button
        className={styles.bellButton}
        title="Notifications"
        aria-label="Notifications"
        onClick={handleToggle}
      >
        <svg
          width="18"
          height="18"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
          strokeLinejoin="round"
        >
          <path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9" />
          <path d="M13.73 21a2 2 0 0 1-3.46 0" />
        </svg>
        {unreadCount > 0 && (
          <span className={styles.badge}>
            {unreadCount > 99 ? '99+' : unreadCount}
          </span>
        )}
      </button>

      {open && (
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
                onClick={() => handleItemClick(n.instanceId)}
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
                  onClick={(e) => handleDismiss(e, n.id)}
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
                    onClick={() => handleItemClick(n.instanceId)}
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
                onClick={handleShowDismissed}
              >
                Show dismissed
              </button>
            ) : hasMoreDismissed ? (
              <button
                className={styles.footerLink}
                onClick={handleLoadMore}
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
      )}
    </div>
  )
}
