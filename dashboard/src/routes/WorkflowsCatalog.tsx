import { useState, useEffect, useMemo, useCallback } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import type { WorkflowSummary, Me } from '../api/types'
import { api } from '../api/client'
import styles from './WorkflowsCatalog.module.css'

type Scope = 'mine' | 'shared' | 'all'
type VisFilter = 'all' | 'public' | 'department'

/** Mode-aware default scope: local mode shows everything, org/edge default to the
 * shared catalog. Used when no explicit ?scope= URL param is present. */
function defaultScope(me: Me | null): Scope {
  return me && me.authMode !== 'local' ? 'shared' : 'all'
}

const SCOPE_LABELS: Record<Scope, string> = { mine: 'Mine', shared: 'Shared', all: 'All' }

export default function WorkflowsCatalog() {
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const [workflows, setWorkflows] = useState<WorkflowSummary[]>([])
  const [me, setMe] = useState<Me | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    async function load() {
      try {
        const [wf, meResp] = await Promise.all([api.workflows.list(), api.me()])
        if (!cancelled) {
          setWorkflows(wf)
          setMe(meResp)
        }
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : 'Failed to load workflows')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [])

  // URL-backed filter state. An unrecognized value falls back to the default.
  const search = searchParams.get('q') ?? ''
  const scopeParam = searchParams.get('scope')
  const scope: Scope =
    scopeParam === 'mine' || scopeParam === 'shared' || scopeParam === 'all'
      ? scopeParam
      : defaultScope(me)
  const visParam = searchParams.get('vis')
  const vis: VisFilter = visParam === 'public' || visParam === 'department' ? visParam : 'all'
  const dept = searchParams.get('dept') ?? ''

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

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return workflows.filter((w) => {
      if (scope === 'mine' && w.scope !== 'private') return false
      if (scope === 'shared' && w.scope !== 'shared') return false
      if (showVisibility && vis !== 'all' && w.visibility !== vis) return false
      if (showDepartment && dept && w.department !== dept) return false
      if (q && !w.name.toLowerCase().includes(q)) return false
      return true
    })
  }, [workflows, scope, vis, dept, search, showVisibility, showDepartment])

  const handleRowClick = useCallback((name: string) => navigate(`/workflows/${name}`), [navigate])

  if (loading) return <div className={styles.loading}>Loading workflows...</div>
  if (error) return <div className={styles.error}>{error}</div>

  const hasFilters =
    !!search ||
    scope !== defaultScope(me) ||
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
                onClick={() => setParam('scope', s, defaultScope(me))}
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
                key={`${w.scope ?? ''}:${w.name}`}
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
