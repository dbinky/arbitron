import styles from './DepartmentSelector.module.css'

/** Sentinel <option> value for the union ("All my departments") default. */
const ALL = '__all__'

interface DepartmentSelectorProps {
  /** The caller's effective departments (from /me). */
  departments: string[]
  /**
   * Current selection: null ⇒ union default ("all my departments"); a
   * single-element array ⇒ that department is focused.
   */
  selection: string[] | null
  /** Called with the new selection (null for union, [dept] for focused). */
  onChange: (selection: string[] | null) => void
}

/**
 * Renders a department focus dropdown. A user with a single effective
 * department (or only the default group) gets no selector at all — there is
 * nothing to choose. Multi-department users get an "All my departments" union
 * default plus one option per department.
 */
export default function DepartmentSelector({ departments, selection, onChange }: DepartmentSelectorProps) {
  if (departments.length <= 1) {
    return null
  }

  const value = selection && selection.length === 1 ? selection[0] : ALL

  return (
    <select
      className={styles.select}
      aria-label="Department"
      value={value}
      onChange={(e) => {
        const v = e.target.value
        onChange(v === ALL ? null : [v])
      }}
    >
      <option value={ALL}>All my departments</option>
      {departments.map((d) => (
        <option key={d} value={d}>
          {d}
        </option>
      ))}
    </select>
  )
}
