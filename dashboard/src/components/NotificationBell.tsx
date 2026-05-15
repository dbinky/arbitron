import { useState, useEffect, useRef, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { NotificationItem, WebSocketEvent } from '../api/types'
import { useSystemWebSocket } from '../hooks/useSystemWebSocket'
import NotificationPopover from './NotificationPopover'
import styles from './NotificationBell.module.css'

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
    } catch (err) {
      console.error('failed to fetch unread count', err)
    }
  }, [])

  const fetchActive = useCallback(async () => {
    try {
      const { items: data } = await api.notifications.list({ dismissed: false, limit: 50 })
      setItems(data)
    } catch (err) {
      console.error('failed to fetch active notifications', err)
    }
  }, [])

  const fetchDismissed = useCallback(async (offset: number) => {
    try {
      const { items: data, hasMore } = await api.notifications.list({
        dismissed: true,
        limit: PAGE_SIZE,
        offset,
      })
      if (offset === 0) {
        setDismissedItems(data)
      } else {
        setDismissedItems((prev) => [...prev, ...data])
      }
      setHasMoreDismissed(hasMore)
      setDismissedOffset(offset + data.length)
    } catch (err) {
      console.error('failed to fetch dismissed notifications', err)
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
  const closePopover = useCallback(() => {
    setOpen(false)
    setShowDismissed(false)
    setDismissedItems([])
    setDismissedOffset(0)
    setHasMoreDismissed(false)
  }, [])

  const openPopover = useCallback(() => {
    setOpen(true)
    void fetchActive().then(() => {
      if (unreadCount > 0) {
        void api.notifications.markRead().catch((err: unknown) => {
          console.error('failed to mark notifications read', err)
        })
        setUnreadCount(0)
        setItems((prev) => prev.map((n) => ({ ...n, read: true })))
      }
    })
  }, [fetchActive, unreadCount])

  const handleToggle = useCallback(() => {
    if (open) {
      closePopover()
    } else {
      openPopover()
    }
  }, [open, openPopover, closePopover])

  // --- click outside closes popover ---
  useEffect(() => {
    if (!open) return

    function handleMouseDown(e: MouseEvent) {
      if (
        wrapperRef.current &&
        !wrapperRef.current.contains(e.target as Node)
      ) {
        closePopover()
      }
    }

    document.addEventListener('mousedown', handleMouseDown)
    return () => document.removeEventListener('mousedown', handleMouseDown)
  }, [open, closePopover])

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
        <NotificationPopover
          items={items}
          dismissedItems={dismissedItems}
          showDismissed={showDismissed}
          hasMoreDismissed={hasMoreDismissed}
          onItemClick={handleItemClick}
          onDismiss={handleDismiss}
          onShowDismissed={handleShowDismissed}
          onLoadMore={handleLoadMore}
        />
      )}
    </div>
  )
}
