# Phase 3.4 Task 2: Notification Bell Frontend & Interactive Callout Shell

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the notification bell popover with unread badge, read/dismiss functionality, dismissed notification paging, cross-tab sync via WebSocket, and the interactive step callout placeholder in the execution view.

**Architecture:** The NavBar's bell icon gains a badge and click-to-open popover. The popover fetches notifications from the REST API and subscribes to WebSocket events for cross-tab sync. The interactive callout is a React Flow overlay component positioned relative to an interactive node.

**Tech Stack:** React, TypeScript, CSS Modules

---

### Task 2.1: Notification bell with popover

**Files:**
- Create: `dashboard/src/components/NotificationBell.tsx`
- Create: `dashboard/src/components/NotificationBell.module.css`
- Modify: `dashboard/src/components/NavBar.tsx`

- [ ] **Step 1: Create NotificationBell.module.css**

```css
.container {
  position: relative;
}

.bell {
  cursor: pointer;
  display: flex;
  align-items: center;
  position: relative;
}

.badge {
  position: absolute;
  top: -4px;
  right: -6px;
  background: var(--danger);
  color: white;
  font-size: 9px;
  border-radius: 50%;
  width: 16px;
  height: 16px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 600;
}

.popover {
  position: absolute;
  top: 100%;
  right: 0;
  margin-top: 8px;
  width: 360px;
  max-height: 480px;
  background: var(--bg-surface);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-lg);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
  overflow: auto;
  z-index: 100;
}

.item {
  display: flex;
  align-items: center;
  padding: 10px 12px;
  border-bottom: 1px solid var(--border-subtle);
  cursor: pointer;
  transition: background 0.15s;
  position: relative;
}

.item:hover {
  background: var(--bg-hover);
}

.unreadDot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--danger);
  flex-shrink: 0;
  margin-right: 8px;
}

.readDot {
  width: 6px;
  height: 6px;
  margin-right: 8px;
}

.icon {
  font-size: 14px;
  margin-right: 8px;
  flex-shrink: 0;
}

.content {
  flex: 1;
  min-width: 0;
}

.title {
  font-size: 12px;
  color: var(--text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.time {
  font-size: 10px;
  color: var(--text-dim);
}

.dismiss {
  background: none;
  border: none;
  color: var(--text-dim);
  cursor: pointer;
  font-size: 14px;
  padding: 2px 4px;
  opacity: 0;
  transition: opacity 0.15s;
}

.item:hover .dismiss {
  opacity: 1;
}

.dismiss:hover {
  color: var(--text-secondary);
}

.footer {
  padding: 10px;
  text-align: center;
  border-top: 1px solid var(--border-subtle);
}

.footerLink {
  color: var(--accent);
  font-size: 11px;
  cursor: pointer;
  background: none;
  border: none;
}

.footerLink:hover {
  color: var(--accent-hover);
}

.empty {
  padding: 24px;
  text-align: center;
  color: var(--text-dim);
  font-size: 12px;
}
```

- [ ] **Step 2: Create NotificationBell.tsx**

```tsx
import { useState, useEffect, useCallback, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import type { WebSocketEvent } from '../api/types'
import { useSystemWebSocket } from '../hooks/useSystemWebSocket'
import { relativeTime } from '../utils/time'
import styles from './NotificationBell.module.css'

interface Notification {
  id: string
  instanceId: string
  workflowName: string
  type: string
  read: boolean
  dismissed: boolean
  createdAt: string
}

const TYPE_ICONS: Record<string, string> = {
  failed: '⚠',
  completed: '✓',
  needs_interaction: '💬',
}

export default function NotificationBell() {
  const [open, setOpen] = useState(false)
  const [unreadCount, setUnreadCount] = useState(0)
  const [notifications, setNotifications] = useState<Notification[]>([])
  const [dismissed, setDismissed] = useState<Notification[]>([])
  const [showDismissed, setShowDismissed] = useState(false)
  const [dismissedOffset, setDismissedOffset] = useState(0)
  const [hasMoreDismissed, setHasMoreDismissed] = useState(true)
  const popoverRef = useRef<HTMLDivElement>(null)
  const navigate = useNavigate()

  const fetchUnreadCount = useCallback(() => {
    fetch('/api/v1/notifications/unread-count')
      .then(r => r.json())
      .then(d => setUnreadCount(d.count))
      .catch(() => {})
  }, [])

  useEffect(() => { fetchUnreadCount() }, [fetchUnreadCount])

  const handleEvent = useCallback((event: WebSocketEvent) => {
    if (event.type === 'notification.created') {
      setUnreadCount(prev => prev + 1)
      if (open) {
        fetch('/api/v1/notifications?dismissed=false&limit=20')
          .then(r => r.json())
          .then(setNotifications)
      }
    } else if (event.type === 'notification.read') {
      setUnreadCount(0)
      setNotifications(prev => prev.map(n => ({ ...n, read: true })))
    } else if (event.type === 'notification.dismissed') {
      const dismissedId = (event.data as any)?.id
      if (dismissedId) {
        setNotifications(prev => prev.filter(n => n.id !== dismissedId))
      }
    }
  }, [open])

  useSystemWebSocket(handleEvent)

  useEffect(() => {
    if (!open) return
    function handleClickOutside(e: MouseEvent) {
      if (popoverRef.current && !popoverRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [open])

  function handleOpen() {
    setOpen(!open)
    if (!open) {
      fetch('/api/v1/notifications?dismissed=false&limit=20')
        .then(r => r.json())
        .then(setNotifications)
      fetch('/api/v1/notifications/mark-read', { method: 'POST' })
        .then(() => setUnreadCount(0))
    }
  }

  function handleDismiss(id: string, e: React.MouseEvent) {
    e.stopPropagation()
    fetch(`/api/v1/notifications/${id}/dismiss`, { method: 'POST' })
    setNotifications(prev => prev.filter(n => n.id !== id))
  }

  function handleShowDismissed() {
    setShowDismissed(true)
    loadDismissed(0)
  }

  function loadDismissed(offset: number) {
    fetch(`/api/v1/notifications?dismissed=true&limit=20&offset=${offset}`)
      .then(r => r.json())
      .then((data: Notification[]) => {
        if (offset === 0) {
          setDismissed(data)
        } else {
          setDismissed(prev => [...prev, ...data])
        }
        setDismissedOffset(offset + 20)
        setHasMoreDismissed(data.length === 20)
      })
  }

  function handleClick(instanceId: string) {
    navigate(`/instances/${instanceId}`)
    setOpen(false)
  }

  return (
    <div className={styles.container} ref={popoverRef}>
      <div className={styles.bell} onClick={handleOpen}>
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="var(--text-secondary)" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
          <path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9" />
          <path d="M13.73 21a2 2 0 0 1-3.46 0" />
        </svg>
        {unreadCount > 0 && <span className={styles.badge}>{unreadCount > 9 ? '9+' : unreadCount}</span>}
      </div>

      {open && (
        <div className={styles.popover}>
          {notifications.length === 0 && !showDismissed ? (
            <div className={styles.empty}>No notifications</div>
          ) : (
            <>
              {notifications.map(n => (
                <div key={n.id} className={styles.item} onClick={() => handleClick(n.instanceId)}>
                  <div className={n.read ? styles.readDot : styles.unreadDot} />
                  <span className={styles.icon}>{TYPE_ICONS[n.type] || '•'}</span>
                  <div className={styles.content}>
                    <div className={styles.title}>{n.workflowName} · {n.instanceId.substring(0, 8)}</div>
                    <div className={styles.time}>{n.type.replace('_', ' ')} · {relativeTime(n.createdAt)}</div>
                  </div>
                  <button className={styles.dismiss} onClick={e => handleDismiss(n.id, e)}>✕</button>
                </div>
              ))}

              {showDismissed && dismissed.map(n => (
                <div key={n.id} className={styles.item} style={{ opacity: 0.6 }} onClick={() => handleClick(n.instanceId)}>
                  <div className={styles.readDot} />
                  <span className={styles.icon}>{TYPE_ICONS[n.type] || '•'}</span>
                  <div className={styles.content}>
                    <div className={styles.title}>{n.workflowName} · {n.instanceId.substring(0, 8)}</div>
                    <div className={styles.time}>{n.type.replace('_', ' ')} · {relativeTime(n.createdAt)}</div>
                  </div>
                </div>
              ))}

              <div className={styles.footer}>
                {!showDismissed ? (
                  <button className={styles.footerLink} onClick={handleShowDismissed}>Show dismissed</button>
                ) : hasMoreDismissed ? (
                  <button className={styles.footerLink} onClick={() => loadDismissed(dismissedOffset)}>Load more</button>
                ) : null}
              </div>
            </>
          )}
        </div>
      )}
    </div>
  )
}
```

- [ ] **Step 3: Replace bell SVG in NavBar with NotificationBell component**

In `dashboard/src/components/NavBar.tsx`, replace the bell div:

```tsx
import NotificationBell from './NotificationBell'
```

Replace the bell div with:
```tsx
<NotificationBell />
```

- [ ] **Step 4: Verify bell works**

Start the server and frontend. The bell should show an unread count when notifications exist. Clicking opens the popover. Dismissing removes entries. Cross-tab sync works when opening in two tabs.

- [ ] **Step 5: Commit**

```bash
git add dashboard/src/components/NotificationBell.tsx dashboard/src/components/NotificationBell.module.css dashboard/src/components/NavBar.tsx
git commit -m "feat(dashboard): add notification bell with popover and cross-tab sync"
```

---

### Task 2.2: Interactive step callout placeholder

**Files:**
- Create: `dashboard/src/graph/nodes/InteractiveCallout.tsx`
- Create: `dashboard/src/graph/nodes/InteractiveCallout.module.css`
- Modify: `dashboard/src/routes/ExecutionView.tsx`

- [ ] **Step 1: Create InteractiveCallout.module.css**

```css
.callout {
  position: absolute;
  z-index: 10;
  pointer-events: auto;
}

.bubble {
  background: rgba(30, 30, 56, 0.95);
  border: 2px solid var(--state-waiting);
  border-radius: 12px;
  padding: 16px;
  min-width: 280px;
  max-width: 360px;
  animation: pulse-border 2s ease-in-out infinite;
}

@keyframes pulse-border {
  0%, 100% { border-color: var(--state-waiting); }
  50% { border-color: rgba(230, 126, 34, 0.4); }
}

.tail {
  width: 0;
  height: 0;
  border-left: 8px solid transparent;
  border-right: 8px solid transparent;
  border-top: 10px solid var(--state-waiting);
  margin: 0 auto;
}

.placeholder {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--text-muted);
  background: #111;
  border-radius: 4px;
  padding: 12px;
  text-align: center;
  line-height: 1.6;
}

.title {
  font-size: 11px;
  color: var(--state-waiting);
  font-weight: 600;
  margin-bottom: 8px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
```

- [ ] **Step 2: Create InteractiveCallout.tsx**

```tsx
import type { ReactNode } from 'react'
import styles from './InteractiveCallout.module.css'

interface Props {
  style?: React.CSSProperties
  children?: ReactNode
}

export default function InteractiveCallout({ style, children }: Props) {
  return (
    <div className={styles.callout} style={style}>
      <div className={styles.bubble}>
        <div className={styles.title}>Interactive Step</div>
        {children || (
          <div className={styles.placeholder}>
            Interactive session — available in a future release.
          </div>
        )}
      </div>
      <div className={styles.tail} />
    </div>
  )
}
```

- [ ] **Step 3: Wire callout into ExecutionView**

In `ExecutionView.tsx`, after the React Flow component, add logic to render the callout when a node is in waiting/interactive state. Since interactive nodes don't exist yet, this renders when any node has a `waiting` execution state for demonstration:

After the ReactFlow component (inside the graph panel), add:
```tsx
{/* Interactive callout rendered as an overlay — positioned by React Flow coordinates */}
```

The callout will be fully wired when the interactive node type is implemented in product Phase 4. For now it's a component that can be manually tested by placing it in the DOM.

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/graph/nodes/InteractiveCallout.tsx dashboard/src/graph/nodes/InteractiveCallout.module.css dashboard/src/routes/ExecutionView.tsx
git commit -m "feat(dashboard): add interactive step callout placeholder component"
```
