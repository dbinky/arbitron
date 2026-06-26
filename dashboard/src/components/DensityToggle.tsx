import type { QueueDensity } from '../api/types'
import styles from './DensityToggle.module.css'

interface DensityToggleProps {
  density: QueueDensity
  onCycle: () => void
}

const DENSITY_ICON: Record<QueueDensity, string> = {
  balanced: '☰',
  dense: '≣',
  expanded: '📋',
}

/**
 * DensityToggle is a compact icon button that cycles the queue-list density
 * views (balanced → dense → expanded). Presentation only — the parent owns
 * the hook and passes the current density + cycle callback.
 */
export default function DensityToggle({ density, onCycle }: DensityToggleProps) {
  return (
    <button
      type="button"
      className={styles.toggle}
      onClick={onCycle}
      aria-label={`Cycle density (current: ${density})`}
      title={`Density: ${density} — click to cycle`}
    >
      <span className={styles.icon}>{DENSITY_ICON[density]}</span>
      <span className={styles.label}>{density}</span>
    </button>
  )
}