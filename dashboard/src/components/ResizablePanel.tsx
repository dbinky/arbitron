import { useState, useCallback, useRef, useEffect, type ReactNode } from 'react'
import styles from './ResizablePanel.module.css'

interface ResizablePanelProps {
  left: ReactNode
  right: ReactNode
  defaultRightWidth?: number
  minLeftWidth?: number
  minRightWidth?: number
}

export default function ResizablePanel({
  left,
  right,
  defaultRightWidth = 380,
  minLeftWidth = 200,
  minRightWidth = 200,
}: ResizablePanelProps) {
  const [rightWidth, setRightWidth] = useState(defaultRightWidth)
  const [dragging, setDragging] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)

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
      setRightWidth(clampedRight)
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
