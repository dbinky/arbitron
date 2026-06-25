# Phase 3 — Task 5: Inline row-detail panel with lazy last-error

**Goal:** Add the `RowDetailPanel` that renders when a row is expanded: secondary fields (submitter account identity/email in shared mode, full submitted timestamp, model/backend, retry detail) and lazy-loads the last error via the existing trace endpoint (`api.instances.getNodeTrace(failedNodeId)`) only on expand. Collapsed by default; one open panel is independent of the row's navigation behavior. A fetch failure shows an inline "couldn't load details" state and never affects the table.

**Architecture:** A new component under `dashboard/src/components/RowDetailPanel.tsx` that takes the enriched row + authMode. It calls `api.instances.getNodeTrace` only when the row has a `failedNodeId` and only on mount/expand (lazy). The fetch is isolated to the panel — a failure shows an inline error state. A row with no `failedNodeId` shows no error section. The panel is rendered by `QueueSection` below the expanded row (Task 4 already threads the `expandedId`).

**Tech Stack:** TypeScript, React 19, Vitest + `@testing-library/react` with mocked `api.instances.getNodeTrace`.

---

### Task 5: RowDetailPanel

**Files:**
- Create: `dashboard/src/components/RowDetailPanel.tsx`
- Create: `dashboard/src/components/RowDetailPanel.module.css`
- Create: `dashboard/src/components/RowDetailPanel.test.tsx`
- Modify: `dashboard/src/components/QueueSection.tsx` (render `RowDetailPanel` under the expanded row)
- Modify: `dashboard/src/components/QueueSection.test.tsx` (add expand/detail assertion)

- [ ] Step 1: Write the failing test for the detail panel

Create `dashboard/src/components/RowDetailPanel.test.tsx`:

```tsx
import { render, screen, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import RowDetailPanel from './RowDetailPanel'
import type { EnrichedInstanceSummary } from '../api/types'

vi.mock('../api/client', () => ({
  api: {
    instances: {
      getNodeTrace: vi.fn(),
    },
  },
}))

import { api } from '../api/client'

function row(overrides: Partial<EnrichedInstanceSummary> = {}): EnrichedInstanceSummary {
  return {
    id: 'i1',
    workflowName: 'wf',
    state: 'failed',
    priority: 'normal',
    createdAt: '2024-01-01T00:00:00Z',
    updatedAt: '2024-01-01T00:00:00Z',
    runName: 'run — wf',
    retryCount: 2,
    failedNodeId: 'fail-node',
    ...overrides,
  }
}

describe('RowDetailPanel', () => {
  beforeEach(() => {
    vi.mocked(api.instances.getNodeTrace).mockReset()
  })

  it('renders secondary fields (submitter identity in shared mode)', () => {
    render(
      <RowDetailPanel
        row={row({ submitterIdentity: 'alice@example.com', submitterName: 'alice' })}
        authMode="org"
      />,
    )
    expect(screen.getByText('alice@example.com')).toBeInTheDocument()
  })

  it('hides submitter identity in local mode', () => {
    render(
      <RowDetailPanel
        row={row({ submitterIdentity: 'alice@example.com' })}
        authMode="local"
      />,
    )
    expect(screen.queryByText('alice@example.com')).toBeNull()
  })

  it('lazy-loads last error via getNodeTrace(failedNodeId) on expand', async () => {
    vi.mocked(api.instances.getNodeTrace).mockResolvedValue({
      id: 'e1', instanceId: 'i1', nodeId: 'fail-node', attempt: 2, status: 'failed',
      error: { error: 'boom: connection refused' },
      startedAt: '2024-01-01T00:00:00Z',
    } as any)
    render(<RowDetailPanel row={row({ failedNodeId: 'fail-node' })} authMode="local" />)
    await waitFor(() => {
      expect(api.instances.getNodeTrace).toHaveBeenCalledWith('i1', 'fail-node')
    })
    expect(screen.getByText(/connection refused/i)).toBeInTheDocument()
  })

  it('shows inline error state when the trace fetch rejects', async () => {
    vi.mocked(api.instances.getNodeTrace).mockRejectedValue(new Error('404'))
    render(<RowDetailPanel row={row({ failedNodeId: 'fail-node' })} authMode="local" />)
    await waitFor(() => {
      expect(screen.getByText(/couldn't load details/i)).toBeInTheDocument()
    })
  })

  it('shows no error section when failedNodeId is absent', () => {
    render(<RowDetailPanel row={row({ failedNodeId: undefined, state: 'completed' })} authMode="local" />)
    expect(screen.queryByText(/last error/i)).toBeNull()
    expect(api.instances.getNodeTrace).not.toHaveBeenCalled()
  })

  it('renders model/backend and retry detail', () => {
    render(
      <RowDetailPanel
        row={row({ model: 'gpt-4o', backend: 'claude-code', retryCount: 3 })}
        authMode="local"
      />,
    )
    expect(screen.getByText('gpt-4o')).toBeInTheDocument()
    expect(screen.getByText('claude-code')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
  })

  it('renders the full submitted timestamp', () => {
    render(<RowDetailPanel row={row({ createdAt: '2024-06-25T12:00:00Z' })} authMode="local" />)
    expect(screen.getByText(/2024-06-25/)).toBeInTheDocument()
  })
})
```

- [ ] Step 2: Run it, confirm it fails

```bash
cd dashboard && npx vitest run src/components/RowDetailPanel.test.tsx
```

Expected: failure — `Failed to resolve import "./RowDetailPanel"`.

- [ ] Step 3: Implement the `RowDetailPanel`

Create `dashboard/src/components/RowDetailPanel.tsx`:

```tsx
import { useEffect, useState } from 'react'
import type { EnrichedInstanceSummary } from '../api/types'
import { api } from '../api/client'
import styles from './RowDetailPanel.module.css'

interface RowDetailPanelProps {
  row: EnrichedInstanceSummary
  authMode: 'local' | 'org' | 'edge'
}

/**
 * RowDetailPanel renders secondary fields and lazy-loads the last error via
 * the trace endpoint only on expand. A fetch failure shows an inline error
 * state and never affects the table. A row with no failedNodeId shows no
 * error section.
 */
export default function RowDetailPanel({ row: rowVal, authMode }: RowDetailPanelProps) {
  const [errorText, setErrorText] = useState<string | null>(null)
  const [errorLoading, setErrorLoading] = useState(false)

  useEffect(() => {
    if (!rowVal.failedNodeId) return
    setErrorLoading(true)
    api.instances
      .getNodeTrace(rowVal.id, rowVal.failedNodeId)
      .then((exec) => {
        const msg = exec.error?.error
        setErrorText(typeof msg === 'string' ? msg : msg ? JSON.stringify(msg) : null)
      })
      .catch(() => setErrorText(null))
      .finally(() => setErrorLoading(false))
  }, [rowVal.id, rowVal.failedNodeId])

  return (
    <div className={styles.panel} data-testid="row-detail-panel">
      <div className={styles.section}>
        <div className={styles.field}>
          <span className={styles.label}>Submitted</span>
          <span className={styles.value}>{rowVal.createdAt}</span>
        </div>
        {authMode !== 'local' && rowVal.submitterIdentity && (
          <div className={styles.field}>
            <span className={styles.label}>Account</span>
            <span className={styles.value}>{rowVal.submitterIdentity}</span>
          </div>
        )}
        {(rowVal.model || rowVal.backend) && (
          <div className={styles.field}>
            <span className={styles.label}>Model / Backend</span>
            <span className={styles.value}>
              {[rowVal.model, rowVal.backend].filter(Boolean).join(' · ')}
            </span>
          </div>
        )}
        <div className={styles.field}>
          <span className={styles.label}>Retries</span>
          <span className={styles.value}>{rowVal.retryCount ?? 0}</span>
        </div>
      </div>
      {rowVal.failedNodeId && (
        <div className={styles.section}>
          <span className={styles.label}>Last error</span>
          {errorLoading ? (
            <span className={styles.value}>Loading…</span>
          ) : errorText ? (
            <pre className={styles.errorText}>{errorText}</pre>
          ) : (
            <span className={styles.value}>couldn't load details</span>
          )}
        </div>
      )}
    </div>
  )
}
```

Create `dashboard/src/components/RowDetailPanel.module.css`:

```css
.panel {
  padding: 8px 16px 12px;
  background: var(--bg-surface);
  border-bottom: 1px solid var(--border-color);
}

.section {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  margin-bottom: 8px;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.label {
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.3px;
  color: var(--text-muted);
}

.value {
  font-size: 12px;
  color: var(--text-secondary);
}

.errorText {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--state-failed);
  white-space: pre-wrap;
  margin: 4px 0 0;
  max-width: 100%;
  overflow-x: auto;
}
```

- [ ] Step 4: Run the RowDetailPanel test, confirm it passes

```bash
cd dashboard && npx vitest run src/components/RowDetailPanel.test.tsx
```

Expected: all assertions pass.

- [ ] Step 5: Render `RowDetailPanel` under the expanded row in `QueueSection`

In `dashboard/src/components/QueueSection.tsx`, import the panel:

```tsx
import RowDetailPanel from './RowDetailPanel'
```

In the `<tbody>`, render the panel in a second `<tr>` with a single full-width `<td>` when the row is expanded. Replace the `<tbody>` body row map with:

```tsx
              <tbody>
                {sorted.map((inst) => (
                  <tbody key={inst.id}>
                    <QueueTableRow
                      row={inst}
                      columns={columns}
                      timeMode={timeMode}
                      authMode={authMode}
                      dimmed={inst.dismissed === true}
                      expanded={expandedId === inst.id}
                      onExpand={(id) => setExpandedId((prev) => (prev === id ? null : id))}
                      onDismiss={onDismiss}
                      onRestore={onRestore}
                    />
                    {expandedId === inst.id && (
                      <tr className={styles.detailRow}>
                        <td colSpan={columns.length + 1} className={styles.detailCell}>
                          <RowDetailPanel row={inst} authMode={authMode} />
                        </td>
                      </tr>
                    )}
                  </tbody>
                ))}
              </tbody>
```

Note: nesting `<tbody>` inside `<tbody>` is invalid HTML. Use a `<tbody>` per row-group instead of a single `<tbody>` wrapping all rows. Replace the outer `<tbody>` with a fragment that emits one `<tbody>` per row+detail pair:

```tsx
              {sorted.map((inst) => (
                <tbody key={inst.id}>
                  <QueueTableRow
                    row={inst}
                    columns={columns}
                    timeMode={timeMode}
                    authMode={authMode}
                    dimmed={inst.dismissed === true}
                    expanded={expandedId === inst.id}
                    onExpand={(id) => setExpandedId((prev) => (prev === id ? null : id))}
                    onDismiss={onDismiss}
                    onRestore={onRestore}
                  />
                  {expandedId === inst.id && (
                    <tr className={styles.detailRow}>
                      <td colSpan={columns.length + 1} className={styles.detailCell}>
                        <RowDetailPanel row={inst} authMode={authMode} />
                      </td>
                    </tr>
                  )}
                </tbody>
              ))}
```

Add to `QueueSection.module.css`:

```css
.detailRow {
  background: var(--bg-surface);
}

.detailCell {
  padding: 0;
}
```

- [ ] Step 6: Add the expand/detail assertion to `QueueSection.test.tsx`

Append to `dashboard/src/components/QueueSection.test.tsx`:

```tsx
describe('QueueSection — row detail panel', () => {
  it('renders the detail panel when a row is expanded', async () => {
    const { api } = await import('../api/client')
    vi.mocked(api.instances.getNodeTrace).mockResolvedValue({
      id: 'e1', instanceId: 'i1', nodeId: 'fail-node', attempt: 1, status: 'failed',
      error: { error: 'boom' }, startedAt: '2024-01-01T00:00:00Z',
    } as any)
    renderSection({
      instances: [summary({ id: 'i1', state: 'failed', failedNodeId: 'fail-node' })],
    })
    fireEvent.click(screen.getByRole('button', { name: /expand/i }))
    expect(screen.getByTestId('row-detail-panel')).toBeInTheDocument()
  })

  it('collapses the detail panel on second expand click', () => {
    renderSection({ instances: [summary({ id: 'i1' })] })
    fireEvent.click(screen.getByRole('button', { name: /expand/i }))
    expect(screen.getByTestId('row-detail-panel')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /collapse/i }))
    expect(screen.queryByTestId('row-detail-panel')).toBeNull()
  })
})
```

Add the necessary imports at the top of the test file:

```tsx
import { fireEvent, screen } from '@testing-library/react'
import { vi } from 'vitest'
vi.mock('../api/client', () => ({ api: { instances: { getNodeTrace: vi.fn() } } }))
```

- [ ] Step 7: Run all QueueSection + RowDetailPanel tests, confirm they pass

```bash
cd dashboard && npx vitest run src/components/QueueSection.test.tsx src/components/RowDetailPanel.test.tsx
```

Expected: all assertions pass.

- [ ] Step 8: Run typecheck

```bash
cd dashboard && npx tsc --noEmit
```

Expected: no type errors.

- [ ] Step 9: Commit

```bash
git add dashboard/src/components/RowDetailPanel.tsx dashboard/src/components/RowDetailPanel.module.css dashboard/src/components/RowDetailPanel.test.tsx dashboard/src/components/QueueSection.tsx dashboard/src/components/QueueSection.module.css dashboard/src/components/QueueSection.test.tsx
git commit -m "feat(dashboard): add RowDetailPanel with lazy last-error via trace endpoint"
```