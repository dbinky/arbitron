import { useState, useCallback, useRef, useEffect, type ReactNode } from 'react'
import styles from './ResizablePanel.module.css'

interface ResizablePanelProps {
  left: ReactNode
  right: ReactNode
  defaultRightWidth?: number
  minLeftWidth?: number
  minRightWidth?: number
  /**
   * When set, overrides the right panel width as a fraction of the total
   * container width (e.g. 0.6 = 60%). The user can still drag to resize,
   * but releasing the override (setting back to undefined) restores the
   * previous manually-set width.
   */
  rightWidthFraction?: number
}

export default function ResizablePanel({
  left,
  right,
  defaultRightWidth = 380,
  minLeftWidth = 200,
  minRightWidth = 200,
  rightWidthFraction,
}: ResizablePanelProps) {
  const [manualRightWidth, setManualRightWidth] = useState(defaultRightWidth)
  const [dragging, setDragging] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)
  // Track the width before the fraction override so we can restore it.
  const savedWidthRef = useRef(defaultRightWidth)

  // Compute the effective right width.
  const computeFractionWidth = useCallback(() => {
    if (rightWidthFraction == null || !containerRef.current) return null
    const totalWidth = containerRef.current.getBoundingClientRect().width
    const dividerWidth = 4
    return Math.max(
      minRightWidth,
      Math.min(
        totalWidth * rightWidthFraction,
        totalWidth - minLeftWidth - dividerWidth,
      ),
    )
  }, [rightWidthFraction, minLeftWidth, minRightWidth])

  // When rightWidthFraction activates, save the current manual width.
  // When it deactivates, restore.
  const prevFractionRef = useRef(rightWidthFraction)
  useEffect(() => {
    if (prevFractionRef.current == null && rightWidthFraction != null) {
      // Fraction just activated: save current width
      savedWidthRef.current = manualRightWidth
    } else if (prevFractionRef.current != null && rightWidthFraction == null) {
      // Fraction just deactivated: restore saved width
      setManualRightWidth(savedWidthRef.current)
    }
    prevFractionRef.current = rightWidthFraction
  }, [rightWidthFraction, manualRightWidth])

  const [fractionWidth, setFractionWidth] = useState<number | null>(null)

  // Recompute fraction width on resize
  useEffect(() => {
    if (rightWidthFraction == null) {
      setFractionWidth(null)
      return
    }

    const update = () => setFractionWidth(computeFractionWidth())
    update()

    window.addEventListener('resize', update)
    return () => window.removeEventListener('resize', update)
  }, [rightWidthFraction, computeFractionWidth])

  const rightWidth = fractionWidth ?? manualRightWidth

  const onMouseDown = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    setDragging(true)
  }, [])

  useEffect(() => {
    if (!dragging) return

    const onMouseMove = (e: MouseEvent) => {
      if (!containerRef.current) return
      const rect = containerRef.current.getBoundingClientRect()
      const totalWidth = rect.width
      const mouseX = e.clientX - rect.left
      const newRightWidth = totalWidth - mouseX - 2 // 2px for divider half
      const clampedRight = Math.max(minRightWidth, Math.min(newRightWidth, totalWidth - minLeftWidth - 4))
      setManualRightWidth(clampedRight)
      // If user is manually dragging while fraction is active, clear the fraction width
      setFractionWidth(null)
    }

    const onMouseUp = () => {
      setDragging(false)
    }

    document.addEventListener('mousemove', onMouseMove)
    document.addEventListener('mouseup', onMouseUp)
    return () => {
      document.removeEventListener('mousemove', onMouseMove)
      document.removeEventListener('mouseup', onMouseUp)
    }
  }, [dragging, minLeftWidth, minRightWidth])

  return (
    <div className={styles.container} ref={containerRef}>
      <div className={styles.left}>{left}</div>
      <div
        className={`${styles.divider} ${dragging ? styles.dividerActive : ''}`}
        onMouseDown={onMouseDown}
      />
      <div className={styles.right} style={{ width: rightWidth }}>
        {right}
      </div>
    </div>
  )
}
