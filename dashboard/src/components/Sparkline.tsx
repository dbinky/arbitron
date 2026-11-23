import styles from './Sparkline.module.css'

export const SPARK_WIDTH = 120
export const SPARK_HEIGHT = 32

const round = (n: number) => String(Math.round(n * 10) / 10)

/** Null or invalid samples split lines; isolated points are not drawn. */
export function sparklineSegments(values: ReadonlyArray<number | null>, max: number, width: number, height: number): string[] {
  if (values.length < 2) return []
  const scale = max > 0 ? max : 1
  const step = width / (values.length - 1)
  const segments: string[] = []
  let current: string[] = []
  const flushSegment = () => {
    if (current.length >= 2) segments.push(current.join(' '))
    current = []
  }
  values.forEach((v, i) => {
    if (v === null || !Number.isFinite(v)) {
      flushSegment()
      return
    }
    const clamped = Math.min(Math.max(v, 0), scale)
    current.push(`${round(i * step)},${round(height - (clamped / scale) * height)}`)
  })
  flushSegment()
  return segments
}

export default function Sparkline({ label, values, max, latest }: { label: string; values: ReadonlyArray<number | null>; max: number; latest: string }) {
  const segments = sparklineSegments(values, max, SPARK_WIDTH, SPARK_HEIGHT)
  return (
    <figure className={styles.spark} data-sparkline={label}>
      <figcaption className={styles.caption}>
        <span className={styles.label}>{label}</span> <span className={styles.value}>{latest}</span>
      </figcaption>
      {segments.length === 0 ? (
        <span className={styles.empty}>Not enough samples</span>
      ) : (
        <svg role="img" aria-label={`${label} trend`} viewBox={`0 0 ${SPARK_WIDTH} ${SPARK_HEIGHT}`} width={SPARK_WIDTH} height={SPARK_HEIGHT} className={styles.svg}>
          {segments.map((points) => (
            <polyline key={points} points={points} fill="none" stroke="currentColor" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
          ))}
        </svg>
      )}
    </figure>
  )
}
