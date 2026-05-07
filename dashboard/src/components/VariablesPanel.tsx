import styles from './VariablesPanel.module.css'

interface VariablesPanelProps {
  variables: Record<string, unknown> | null
}

function formatValue(value: unknown): string {
  if (value === null) return 'null'
  if (value === undefined) return 'undefined'
  if (typeof value === 'string') return value
  return JSON.stringify(value, null, 2)
}

export default function VariablesPanel({ variables }: VariablesPanelProps) {
  if (!variables || Object.keys(variables).length === 0) {
    return <div className={styles.empty}>No variables available</div>
  }

  const entries = Object.entries(variables)

  return (
    <div className={styles.container}>
      <table className={styles.table}>
        <thead>
          <tr>
            <th>Key</th>
            <th>Value</th>
          </tr>
        </thead>
        <tbody>
          {entries.map(([key, value]) => (
            <tr key={key}>
              <td className={styles.key}>{key}</td>
              <td className={styles.value}>{formatValue(value)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
