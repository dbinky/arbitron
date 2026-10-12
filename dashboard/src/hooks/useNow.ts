import { useEffect, useState } from 'react'

/**
 * useNow returns Date.now(), refreshed every `intervalMs` (default 1s), so
 * time-derived cells (elapsed, "3m ago") advance without server events. One
 * call per page — not per row. Ticking pauses while the tab is hidden and
 * resyncs immediately when it becomes visible again.
 */
export function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    let timer: ReturnType<typeof setInterval> | null = null
    const start = () => {
      if (timer === null) timer = setInterval(() => setNow(Date.now()), intervalMs)
    }
    const stop = () => {
      if (timer !== null) {
        clearInterval(timer)
        timer = null
      }
    }
    const onVisibility = () => {
      if (document.visibilityState === 'hidden') {
        stop()
      } else {
        setNow(Date.now())
        start()
      }
    }
    if (document.visibilityState !== 'hidden') start()
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      stop()
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [intervalMs])

  return now
}
