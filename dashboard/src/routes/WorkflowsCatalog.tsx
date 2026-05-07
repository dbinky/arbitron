import { useState, useEffect, useMemo, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import type { WorkflowSummary } from '../api/types'
import { api } from '../api/client'
import styles from './WorkflowsCatalog.module.css'

export default function WorkflowsCatalog() {
  const navigate = useNavigate()
  const [workflows, setWorkflows] = useState<WorkflowSummary[]>([])
  const [search, setSearch] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    async function load() {
      try {
        const data = await api.workflows.list()
        if (!cancelled) setWorkflows(data)
      } catch (err) {
        if (!cancelled)
          setError(err instanceof Error ? err.message : 'Failed to load workflows')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [])

  const filtered = useMemo(() => {
    if (!search.trim()) return workflows
    const q = search.toLowerCase()
    return workflows.filter((w) => w.name.toLowerCase().includes(q))
  }, [workflows, search])

  const handleRowClick = useCallback(
    (name: string) => {
      navigate(`/workflows/${name}`)
    },
    [navigate],
  )

  if (loading) {
    return <div className={styles.loading}>Loading workflows...</div>
  }

  if (error) {
    return <div className={styles.error}>{error}</div>
  }

  return (
    <div className={styles.page}>
      <div className={styles.header}>
        <h1 className={styles.title}>Workflows</h1>
        <input
          className={styles.searchInput}
          type="text"
          placeholder="Search workflows..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </div>

      {filtered.length === 0 ? (
        <div className={styles.empty}>
          {search ? 'No workflows match your search.' : 'No workflows found.'}
        </div>
      ) : (
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Name</th>
              <th>Description</th>
              <th>Author</th>
              <th style={{ textAlign: 'center' }}>Nodes</th>
            </tr>
          </thead>
          <tbody>
            {filtered.map((w) => (
              <tr
                key={w.name}
                className={styles.row}
                onClick={() => handleRowClick(w.name)}
              >
                <td className={styles.nameCell}>{w.name}</td>
                <td>{w.description || '--'}</td>
                <td>{w.author || '--'}</td>
                <td className={styles.nodeCountCell}>
                  {w.nodeCount ?? '--'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
