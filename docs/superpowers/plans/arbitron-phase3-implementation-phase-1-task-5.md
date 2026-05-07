# Phase 3.1 Task 5: Queue View Components

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Queue View home page with three collapsible sections (Active, Failed, Completed), instance rows with live-updating data, and attention flags.

**Architecture:** The QueueView route component fetches instances on mount and subscribes to the system WebSocket for live updates. Three `QueueSection` components render independently collapsible/sortable/filterable instance lists. `InstanceRow` renders a single instance with state badge and attention flag.

**Tech Stack:** React, TypeScript, CSS Modules

---

### Task 5.1: StateBadge component

**Files:**
- Create: `dashboard/src/components/StateBadge.tsx`
- Create: `dashboard/src/components/StateBadge.module.css`

- [ ] **Step 1: Create StateBadge.module.css**

```css
.badge {
  display: inline-block;
  padding: 1px 6px;
  border-radius: var(--radius-sm);
  font-size: 10px;
  font-weight: 500;
  color: white;
  text-transform: lowercase;
}

.running { background: var(--state-running); }
.completed { background: var(--state-completed); }
.failed { background: var(--state-failed); }
.waiting { background: var(--state-waiting); }
.queued { background: var(--state-queued); }
.paused { background: var(--state-paused); }
.cancelled { background: var(--state-queued); }
```

- [ ] **Step 2: Create StateBadge.tsx**

```tsx
import type { InstanceState } from '../api/types'
import styles from './StateBadge.module.css'

interface Props {
  state: InstanceState
}

export default function StateBadge({ state }: Props) {
  return (
    <span className={`${styles.badge} ${styles[state] || ''}`}>
      {state}
    </span>
  )
}
```

- [ ] **Step 3: Commit**

```bash
git add dashboard/src/components/StateBadge.tsx dashboard/src/components/StateBadge.module.css
git commit -m "feat(dashboard): add StateBadge component"
```

---

### Task 5.2: InstanceRow component

**Files:**
- Create: `dashboard/src/components/InstanceRow.tsx`
- Create: `dashboard/src/components/InstanceRow.module.css`
- Create: `dashboard/src/utils/time.ts`

- [ ] **Step 1: Create time utilities**

Create `dashboard/src/utils/time.ts`:

```ts
export function relativeTime(dateStr: string): string {
  const now = Date.now()
  const then = new Date(dateStr).getTime()
  const diffMs = now - then
  const diffSec = Math.floor(diffMs / 1000)

  if (diffSec < 60) return `${diffSec}s ago`
  const diffMin = Math.floor(diffSec / 60)
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHr = Math.floor(diffMin / 60)
  if (diffHr < 24) return `${diffHr}h ago`
  const diffDay = Math.floor(diffHr / 24)
  return `${diffDay}d ago`
}

export function elapsed(dateStr: string): string {
  const now = Date.now()
  const start = new Date(dateStr).getTime()
  const diffSec = Math.floor((now - start) / 1000)
  const min = Math.floor(diffSec / 60)
  const sec = diffSec % 60
  if (min >= 60) {
    const hr = Math.floor(min / 60)
    const remMin = min % 60
    return `${hr}h ${remMin}m`
  }
  return `${min}m ${sec.toString().padStart(2, '0')}s`
}
```

- [ ] **Step 2: Create InstanceRow.module.css**

```css
.row {
  display: flex;
  align-items: center;
  padding: 8px 12px;
  background: var(--bg-surface);
  border-radius: var(--radius-md);
  border-left: 3px solid var(--border-color);
  font-size: 12px;
  cursor: pointer;
  transition: background 0.15s;
  position: relative;
}

.row:hover {
  background: var(--bg-hover);
}

.running { border-left-color: var(--state-running); }
.completed { border-left-color: var(--state-completed); }
.failed { border-left-color: var(--state-failed); }
.waiting { border-left-color: var(--state-waiting); }
.queued { border-left-color: var(--state-paused); }
.paused { border-left-color: var(--state-paused); }

.col { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.workflowName { width: 20%; color: var(--text-secondary); }
.state { width: 12%; }
.node { width: 15%; color: var(--text-muted); }
.priority { width: 10%; font-size: 10px; }
.time { width: 12%; color: var(--text-muted); }
.owner { width: 10%; color: var(--text-dim); }

.priorityHigh { color: var(--priority-high); }
.priorityNormal { color: var(--priority-normal); }
.priorityLow { color: var(--priority-low); }

.attention {
  position: absolute;
  right: 12px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--danger);
  font-size: 10px;
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: 4px;
}
```

- [ ] **Step 3: Create InstanceRow.tsx**

```tsx
import { useNavigate } from 'react-router-dom'
import type { InstanceSummary } from '../api/types'
import StateBadge from './StateBadge'
import { relativeTime, elapsed } from '../utils/time'
import styles from './InstanceRow.module.css'

interface Props {
  instance: InstanceSummary
  timeMode: 'elapsed' | 'relative'
}

const ACTIVE_STATES = new Set(['running', 'waiting', 'queued', 'paused'])

export default function InstanceRow({ instance, timeMode }: Props) {
  const navigate = useNavigate()

  const needsAttention = instance.state === 'failed'

  const timeDisplay = timeMode === 'elapsed'
    ? elapsed(instance.createdAt)
    : relativeTime(instance.completedAt || instance.updatedAt)

  const priorityClass =
    instance.priority === 'high' ? styles.priorityHigh
    : instance.priority === 'low' ? styles.priorityLow
    : styles.priorityNormal

  return (
    <div
      className={`${styles.row} ${styles[instance.state] || ''}`}
      onClick={() => navigate(`/instances/${instance.id}`)}
    >
      <span className={`${styles.col} ${styles.workflowName}`}>{instance.workflowName}</span>
      <span className={`${styles.col} ${styles.state}`}><StateBadge state={instance.state} /></span>
      <span className={`${styles.col} ${styles.node}`}>{instance.currentNodeId || '—'}</span>
      <span className={`${styles.col} ${styles.priority} ${priorityClass}`}>{instance.priority}</span>
      <span className={`${styles.col} ${styles.time}`}>{timeDisplay}</span>
      <span className={`${styles.col} ${styles.owner}`}>local</span>
      {needsAttention && (
        <span className={styles.attention}>⚠ Needs attention</span>
      )}
    </div>
  )
}
```

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/components/InstanceRow.tsx dashboard/src/components/InstanceRow.module.css dashboard/src/utils/time.ts
git commit -m "feat(dashboard): add InstanceRow component with attention flags"
```

---

### Task 5.3: QueueSection component

**Files:**
- Create: `dashboard/src/components/QueueSection.tsx`
- Create: `dashboard/src/components/QueueSection.module.css`

- [ ] **Step 1: Create QueueSection.module.css**

```css
.section {
  padding: 12px 20px 4px;
}

.header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
  cursor: pointer;
  user-select: none;
}

.headerLeft {
  display: flex;
  align-items: center;
  gap: 8px;
}

.arrow {
  color: var(--text-muted);
  font-size: 12px;
  transition: transform 0.15s;
}

.arrowCollapsed {
  transform: rotate(-90deg);
}

.title {
  font-weight: 600;
  font-size: 14px;
}

.count {
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 8px;
}

.subtitle {
  color: var(--text-dim);
  font-size: 10px;
}

.controls {
  display: flex;
  gap: 8px;
}

.controlBtn {
  font-size: 10px;
  color: var(--text-dim);
  border: 1px solid var(--border-color);
  padding: 2px 6px;
  border-radius: var(--radius-sm);
  background: none;
  cursor: pointer;
}

.controlBtn:hover {
  border-color: var(--text-muted);
  color: var(--text-muted);
}

.list {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.empty {
  color: var(--text-dim);
  font-size: 11px;
  padding: 8px 12px;
  background: var(--bg-surface);
  border-radius: var(--radius-md);
  text-align: center;
  font-style: italic;
}
```

- [ ] **Step 2: Create QueueSection.tsx**

```tsx
import { useState, useMemo } from 'react'
import type { InstanceSummary } from '../api/types'
import InstanceRow from './InstanceRow'
import styles from './QueueSection.module.css'

type SortField = 'priority' | 'time' | 'workflowName'

interface Props {
  title: string
  instances: InstanceSummary[]
  titleColor: string
  countBg: string
  countColor: string
  subtitle?: string
  timeMode: 'elapsed' | 'relative'
  defaultCollapsed?: boolean
}

const PRIORITY_ORDER: Record<string, number> = { high: 0, normal: 1, low: 2 }

export default function QueueSection({
  title, instances, titleColor, countBg, countColor, subtitle, timeMode, defaultCollapsed = false,
}: Props) {
  const [collapsed, setCollapsed] = useState(defaultCollapsed)
  const [sortField, setSortField] = useState<SortField>('time')
  const [sortAsc, setSortAsc] = useState(false)

  const sorted = useMemo(() => {
    const copy = [...instances]
    copy.sort((a, b) => {
      let cmp = 0
      switch (sortField) {
        case 'priority':
          cmp = (PRIORITY_ORDER[a.priority] ?? 1) - (PRIORITY_ORDER[b.priority] ?? 1)
          break
        case 'workflowName':
          cmp = a.workflowName.localeCompare(b.workflowName)
          break
        case 'time':
          cmp = new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime()
          break
      }
      return sortAsc ? -cmp : cmp
    })
    return copy
  }, [instances, sortField, sortAsc])

  function toggleSort(field: SortField) {
    if (sortField === field) {
      setSortAsc(!sortAsc)
    } else {
      setSortField(field)
      setSortAsc(false)
    }
  }

  return (
    <div className={styles.section}>
      <div className={styles.header} onClick={() => setCollapsed(!collapsed)}>
        <div className={styles.headerLeft}>
          <span className={`${styles.arrow} ${collapsed ? styles.arrowCollapsed : ''}`}>▾</span>
          <span className={styles.title} style={{ color: titleColor }}>{title}</span>
          <span className={styles.count} style={{ background: countBg, color: countColor }}>
            {instances.length}
          </span>
          {subtitle && <span className={styles.subtitle}>{subtitle}</span>}
        </div>
        <div className={styles.controls} onClick={e => e.stopPropagation()}>
          <button className={styles.controlBtn} onClick={() => toggleSort('priority')}>Priority</button>
          <button className={styles.controlBtn} onClick={() => toggleSort('time')}>Time</button>
          <button className={styles.controlBtn} onClick={() => toggleSort('workflowName')}>Name</button>
        </div>
      </div>
      {!collapsed && (
        <div className={styles.list}>
          {sorted.length === 0 ? (
            <div className={styles.empty}>No instances</div>
          ) : (
            sorted.map(inst => (
              <InstanceRow key={inst.id} instance={inst} timeMode={timeMode} />
            ))
          )}
        </div>
      )}
    </div>
  )
}
```

- [ ] **Step 3: Commit**

```bash
git add dashboard/src/components/QueueSection.tsx dashboard/src/components/QueueSection.module.css
git commit -m "feat(dashboard): add QueueSection component with collapse/sort"
```

---

### Task 5.4: QueueView route with data fetching and WebSocket

**Files:**
- Modify: `dashboard/src/routes/QueueView.tsx`

- [ ] **Step 1: Implement QueueView with data fetching and live updates**

```tsx
import { useState, useEffect, useCallback } from 'react'
import type { InstanceSummary, WebSocketEvent } from '../api/types'
import { api } from '../api/client'
import { useSystemWebSocket } from '../hooks/useSystemWebSocket'
import QueueSection from '../components/QueueSection'

const ACTIVE_STATES = new Set(['running', 'waiting', 'queued', 'paused'])
const FIVE_DAYS_MS = 5 * 24 * 60 * 60 * 1000

function fiveDaysAgo(): string {
  return new Date(Date.now() - FIVE_DAYS_MS).toISOString()
}

export default function QueueView() {
  const [active, setActive] = useState<InstanceSummary[]>([])
  const [failed, setFailed] = useState<InstanceSummary[]>([])
  const [completed, setCompleted] = useState<InstanceSummary[]>([])
  const [loading, setLoading] = useState(true)

  const fetchAll = useCallback(async () => {
    try {
      const [running, waiting, queued, paused, fail, done] = await Promise.all([
        api.instances.list({ state: 'running', limit: 500 }),
        api.instances.list({ state: 'waiting', limit: 500 }),
        api.instances.list({ state: 'queued', limit: 500 }),
        api.instances.list({ state: 'paused', limit: 500 }),
        api.instances.list({ state: 'failed', since: fiveDaysAgo(), limit: 500 }),
        api.instances.list({ state: 'completed', since: fiveDaysAgo(), limit: 500 }),
      ])
      setActive([...running, ...waiting, ...queued, ...paused])
      setFailed(fail)
      setCompleted(done)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { fetchAll() }, [fetchAll])

  const handleEvent = useCallback((event: WebSocketEvent) => {
    if (event.type === 'instance.state_changed' || event.type.startsWith('instance.')) {
      fetchAll()
    }
  }, [fetchAll])

  useSystemWebSocket(handleEvent)

  if (loading) {
    return <div style={{ padding: 20, color: 'var(--text-muted)' }}>Loading...</div>
  }

  return (
    <div>
      <QueueSection
        title="Active Instances"
        instances={active}
        titleColor="var(--text-secondary)"
        countBg="var(--bg-hover)"
        countColor="var(--text-muted)"
        timeMode="elapsed"
      />
      <QueueSection
        title="Failed Instances"
        instances={failed}
        titleColor="var(--state-failed)"
        countBg="#3a1a1a"
        countColor="var(--state-failed)"
        subtitle="last 5 days"
        timeMode="relative"
      />
      <QueueSection
        title="Completed Instances"
        instances={completed}
        titleColor="var(--state-completed)"
        countBg="#1a3a1a"
        countColor="var(--state-completed)"
        subtitle="last 5 days"
        timeMode="relative"
        defaultCollapsed
      />
    </div>
  )
}
```

- [ ] **Step 2: Verify queue view renders**

Start both the Go backend and Vite dev server. Navigate to `http://localhost:5173`. The Queue View should show three sections. If there are instances in the database, they appear in the appropriate sections.

- [ ] **Step 3: Commit**

```bash
git add dashboard/src/routes/QueueView.tsx
git commit -m "feat(dashboard): implement QueueView with live WebSocket updates"
```
