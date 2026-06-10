import { useMemo, useCallback } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useWorkflowsCatalog } from '../hooks/useWorkflowsCatalog'
import { defaultScope, filterWorkflows, parseFilterParams, type Scope } from './workflowsFilter'
import styles from './WorkflowsCatalog.module.css'

const SCOPE_LABELS: Record<Scope, string> = { mine: 'Mine', shared: 'Shared', all: 'All' }

export default function WorkflowsCatalog() {
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const { workflows, me, loading, error } = useWorkflowsCatalog()

  // Adaptive chrome is driven by what the loaded data actually contains.
  const anyShared = useMemo(() => workflows.some((w) => w.scope === 'shared'), [workflows])
  const departments = useMemo(
    () =>
      Array.from(
        new Set(workflows.map((w) => w.department).filter((d): d is string => !!d)),
      ).sort(),
    [workflows],
  )
  const showVisibility = anyShared
  const showDepartment = departments.length > 0

  // Validate URL params at the boundary. A stale/unknown value neutralizes to its
  // safe default here (notably: a ?dept= not in the derived set → '') so the
  // predicate matrix downstream never has to defend against bad input.
  const { scope, vis, dept, search } = parseFilterParams(searchParams, me, departments)

  // Single source of truth for the mode-aware default, reused by the segmented
  // buttons and the "has active filters" check.
  const resolvedDefault = defaultScope(me)

  // Update one query param; omit it when it equals its default to keep URLs clean.
  const setParam = useCallback(
    (key: string, value: string, defaultValue: string) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          if (!value || value === defaultValue) next.delete(key)
          else next.set(key, value)
          return next
        },
        { replace: true },
      )
    },
    [setSearchParams],
  )

  const filtered = useMemo(
    () =>
      filterWorkflows(workflows, { scope, vis, dept, search, showVisibility, showDepartment }),
    [workflows, scope, vis, dept, search, showVisibility, showDepartment],
  )

  const handleRowClick = useCallback((name: string) => navigate(`/workflows/${name}`), [navigate])

  if (loading) return <div className={styles.loading}>Loading workflows...</div>
  if (error) return <div className={styles.error}>{error}</div>

  const hasFilters =
    !!search ||
    scope !== resolvedDefault ||
    (showVisibility && vis !== 'all') ||
    (showDepartment && !!dept)

  return (
    <div className={styles.page}>
      <div className={styles.header}>
        <h1 className={styles.title}>Workflows</h1>
        <div className={styles.controls}>
          <div className={styles.segmented} role="group" aria-label="Scope">
            {(['mine', 'shared', 'all'] as Scope[]).map((s) => (
              <button
                key={s}
                type="button"
                aria-pressed={scope === s}
                className={scope === s ? styles.segmentActive : styles.segment}
                onClick={() => setParam('scope', s, resolvedDefault)}
              >
                {SCOPE_LABELS[s]}
              </button>
            ))}
          </div>
          {showVisibility && (
            <select
              className={styles.select}
              aria-label="Visibility"
              value={vis}
              onChange={(e) => setParam('vis', e.target.value, 'all')}
            >
              <option value="all">All visibility</option>
              <option value="public">Public</option>
              <option value="department">Department</option>
            </select>
          )}
          {showDepartment && (
            <select
              className={styles.select}
              aria-label="Department"
              value={dept}
              onChange={(e) => setParam('dept', e.target.value, '')}
            >
              <option value="">All departments</option>
              {departments.map((d) => (
                <option key={d} value={d}>
                  {d}
                </option>
              ))}
            </select>
          )}
          <input
            className={styles.searchInput}
            type="text"
            placeholder="Search workflows..."
            value={search}
            onChange={(e) => setParam('q', e.target.value, '')}
          />
        </div>
      </div>

      {filtered.length === 0 ? (
        <div className={styles.empty}>
          {hasFilters ? 'No workflows match your filters.' : 'No workflows found.'}
        </div>
      ) : (
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Name</th>
              <th>Description</th>
              <th>Scope</th>
              {showVisibility && <th>Visibility</th>}
              {showDepartment && <th>Department</th>}
              <th>Author</th>
              <th style={{ textAlign: 'center' }}>Nodes</th>
            </tr>
          </thead>
          <tbody>
            {filtered.map((w) => (
              <tr
                key={`${w.scope ?? 'private'}:${w.visibility ?? ''}:${w.name}`}
                className={styles.row}
                onClick={() => handleRowClick(w.name)}
              >
                <td className={styles.nameCell}>{w.name}</td>
                <td>{w.description || '--'}</td>
                <td>
                  <span className={styles.badge}>{w.scope ?? '--'}</span>
                </td>
                {showVisibility && (
                  <td>{w.visibility ? <span className={styles.badge}>{w.visibility}</span> : '--'}</td>
                )}
                {showDepartment && <td>{w.department || '--'}</td>}
                <td>{w.author || '--'}</td>
                <td className={styles.nodeCountCell}>{w.nodeCount ?? '--'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
