import { useState, useMemo } from 'react'
import type { InstanceSummary } from '../api/types'
import InstanceRow from './InstanceRow'
import styles from './QueueSection.module.css'

type SortField = 'priority' | 'time' | 'name'

interface QueueSectionProps {
  title: string
  instances: InstanceSummary[]
  titleColor: string
  countBg: string
  countColor: string
  subtitle?: string
  timeMode: 'elapsed' | 'relative'
  defaultCollapsed?: boolean
}

const priorityOrder: Record<string, number> = {
  high: 0,
  normal: 1,
  low: 2,
}

function sortInstances(
  instances: InstanceSummary[],
  field: SortField,
  timeMode: 'elapsed' | 'relative',
): InstanceSummary[] {
  const sorted = [...instances]
  switch (field) {
    case 'priority':
      sorted.sort(
        (a, b) =>
          (priorityOrder[a.priority] ?? 1) - (priorityOrder[b.priority] ?? 1),
      )
      break
    case 'time':
      if (timeMode === 'elapsed') {
        // Elapsed: sort oldest createdAt first so longest-running instances surface to the top.
        sorted.sort(
          (a, b) =>
            new Date(a.createdAt).getTime() - new Date(b.createdAt).getTime(),
        )
      } else {
        // Relative: sort most-recently-updated first.
        sorted.sort(
          (a, b) =>
            new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime(),
        )
      }
      break
    case 'name':
      sorted.sort((a, b) => a.workflowName.localeCompare(b.workflowName))
      break
  }
  return sorted
}

export default function QueueSection({
  title,
  instances,
  titleColor,
  countBg,
  countColor,
  subtitle,
  timeMode,
  defaultCollapsed = false,
}: QueueSectionProps) {
  const [collapsed, setCollapsed] = useState(defaultCollapsed)
  const [sortField, setSortField] = useState<SortField>('priority')

  const sorted = useMemo(
    () => sortInstances(instances, sortField, timeMode),
    [instances, sortField, timeMode],
  )

  return (
    <div className={styles.section}>
      <div
        className={styles.header}
        onClick={() => setCollapsed((c) => !c)}
        role="button"
        tabIndex={0}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            setCollapsed((c) => !c)
          }
        }}
      >
        <span
          className={`${styles.arrow} ${collapsed ? '' : styles.arrowExpanded}`}
        >
          {'▶'}
        </span>
        <span className={styles.title} style={{ color: titleColor }}>
          {title}
        </span>
        <span
          className={styles.count}
          style={{ background: countBg, color: countColor }}
        >
          {instances.length}
        </span>
        {subtitle && <span className={styles.subtitle}>{subtitle}</span>}
      </div>

      {!collapsed && (
        <>
          <div className={styles.sortControls}>
            {(['priority', 'time', 'name'] as const).map((field) => (
              <button
                key={field}
                className={`${styles.sortButton} ${sortField === field ? styles.sortButtonActive : ''}`}
                onClick={() => setSortField(field)}
              >
                {field.charAt(0).toUpperCase() + field.slice(1)}
              </button>
            ))}
          </div>
          <div className={styles.body}>
            {sorted.length === 0 ? (
              <div className={styles.empty}>No instances</div>
            ) : (
              sorted.map((inst) => (
                <InstanceRow
                  key={inst.id}
                  instance={inst}
                  timeMode={timeMode}
                />
              ))
            )}
          </div>
        </>
      )}
    </div>
  )
}
