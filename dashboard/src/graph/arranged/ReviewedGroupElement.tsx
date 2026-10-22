import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties } from 'react'
import { groupOverlayItems, groupTextKey } from './groupVocabulary'
import type { ArrangedGroupElementData } from './buildArrangedFlow'
import { GeometryHandles, type GeometryHandle } from './GeometryHandles'
import { nodeTextClassNames } from './ArrangedNode'
import { edgeLabelClassName } from './RoutedEdge'
import { typeAccent } from '../nodes/typeColors'

const classes = { node_label: nodeTextClassNames.label, node_type: nodeTextClassNames.typeCaption, edge_label: edgeLabelClassName }
/** The fallback parts and reviewed parts share their registration, never their sizing. */
export default function ReviewedGroupElement({ data, selected }: { data: ArrangedGroupElementData; selected?: boolean }) {
  const { element: e, contract: c, nodeBox: n } = data
  const pos = (b: { x: number; y: number; w: number; h: number }): CSSProperties => ({ position: 'absolute', left: b.x - e.box.x - c.nodeBorderPx, top: b.y - e.box.y - c.nodeBorderPx, width: b.w, height: b.h, margin: 0, padding: 0, transform: 'none', background: 'transparent', pointerEvents: 'auto' })
  return <div data-arranged-group-element={e.id} data-arranged-node={n?.id} data-layout-instance={data.layoutInstanceId}
    style={{ position: 'relative', pointerEvents: 'auto', boxSizing: 'border-box', width: e.box.w, height: e.box.h, border: `${c.nodeBorderPx}px solid ${c.forwardColor}`, background: e.role === 'container' || e.role === 'lane' ? '#1a1a2e' : '#1e1e38', boxShadow: selected ? '0 0 0 2px var(--accent)' : undefined }}>
    <GeometryHandles box={e.box} handles={data.handles as GeometryHandle[] ?? []} />
    {n && <>
      <div style={{ height: c.nodeTypeBarPx, background: typeAccent('parallel_group') }} />
      <div style={{ padding: `${c.nodePaddingY}px ${c.nodePaddingX}px` }}>
        <span data-node-text="type" className={classes.node_type} style={{ lineHeight: c.lineHeight }}>{n.typeCaption}</span>
        <span data-node-text="label" className={classes.node_label} style={{ lineHeight: c.lineHeight, marginTop: c.nodeTextGapPx }}>{n.text}</span>
      </div>
    </>}
    {e.texts.map(t => <span key={t.key} data-group-text={t.key} className={classes[t.style]} style={pos(t.box)}>{data.taskLive?.redacted && JSON.parse(t.key)[2] === 'title' ? 'hidden dependency' : t.text}</span>)}
    {e.overlayBox && <GroupOverlay data={data} style={pos(e.overlayBox)} />}
  </div>
}

/** Reserve every finite alternative simultaneously; visible rows never move with live state. */
function GroupOverlay({ data, style }: { data: ArrangedGroupElementData; style: CSSProperties }) {
  const { element: e, contract: c, group, live, taskLive } = data
  const root = useRef<HTMLDivElement>(null)
  const rows = useMemo(() => {
    const items = groupOverlayItems({ order: [group.nodeId], groups: { [group.nodeId]: group }, owner: {} }, c.nodeTextMaxWidth)
      .filter(it => JSON.parse(it.key)[1] === e.id && JSON.parse(it.key)[2].startsWith('overlay:'))
    const out = new Map<string, typeof items>()
    for (const it of items) {
      const parts = (JSON.parse(it.key)[2] as string).split(':')
      const row = parts[1] === 'progress' || parts[1] === 'badge' ? `${parts[1]}:${parts[2]}` : parts[1]
      if (row !== 'badge:hidden dependency') out.set(row, [...(out.get(row) ?? []), it])
    }
    return [...out.entries()]
  }, [e.id, group, c.nodeTextMaxWidth])
  const wanted = (row: string): string | null => {
    if (e.role === 'footer') {
      if (!live) return null
      if (row === 'status') return `overlay:status:${live.state}`
      if (row === 'winner') return live.winner ? `overlay:winner:${live.winner}` : null
      const p = live.progress
      if (p) {
        const counts: Record<string, number> = { done: p.completed, active: p.active, 'not started': p.notStarted, failed: p.failed }
        if (row.startsWith('progress:')) return `overlay:${row}:${counts[row.slice(9)]}`
      }
    } else if (taskLive) {
      if (row === 'status') return `overlay:status:${taskLive.status}`
      if (row === 'frozen') return taskLive.frozenStatus ? `overlay:frozen:${taskLive.frozenStatus}` : null
      const badges: Record<string, boolean> = { winner: taskLive.winner, 'non-blocking': taskLive.continuingIndependently, 'cancel requested': taskLive.cancelRequested }
      if (row.startsWith('badge:') && badges[row.slice(6)]) return `overlay:${row}`
    }
    return null
  }
  // A live value with no measured alternative was never reviewed: report it like overflow.
  const unsupported = rows.some(([row, alternatives]) => {
    const selected = wanted(row)
    return selected !== null && !alternatives.some(it => it.key === groupTextKey(e.id, selected))
  })
  const [heights, setHeights] = useState<Record<string, number>>({})
  useLayoutEffect(() => {
    const measure = () => {
      const next: Record<string, number> = {}
      let width = 0
      for (const el of root.current?.querySelectorAll<HTMLElement>('[data-row-measure]') ?? []) {
        const css = getComputedStyle(el), row = el.dataset.rowMeasure!
        // Use untransformed CSS metrics: ReactFlow may be zoomed out.
        next[row] = Math.max(next[row] ?? 0, Math.ceil(parseFloat(css.height) || el.offsetHeight) + 1)
        width = Math.max(width, parseFloat(css.width) || el.offsetWidth)
      }
      const height = Object.values(next).reduce((a, b) => a + b, 0) + Math.max(0, rows.length - 1) * c.nodeTextGapPx
      if (unsupported || height > e.overlayBox!.h || width > e.overlayBox!.w) {
        // Hide synchronously, including ResizeObserver delivery before paint.
        if (root.current) root.current.style.visibility = 'hidden'
        data.onOverlayOverflow?.()
        return
      }
      setHeights(previous => JSON.stringify(previous) === JSON.stringify(next) ? previous : next)
    }
    measure()
    const observer = new ResizeObserver(measure)
    for (const el of root.current?.querySelectorAll('[data-row-measure]') ?? []) observer.observe(el)
    return () => observer.disconnect()
  }, [rows, unsupported, c.nodeTextGapPx, e.overlayBox, data.onOverlayOverflow])
  return <div ref={root} data-group-overlay={e.id} style={{ ...style, display: 'flex', flexDirection: 'column', gap: c.nodeTextGapPx, visibility: Object.keys(heights).length ? 'visible' : 'hidden' }}>
    {rows.map(([row, alternatives]) => {
      const selected = wanted(row), item = alternatives.find(it => it.key === groupTextKey(e.id, selected ?? ''))
      return <div key={row} data-overlay-row={row} style={{ position: 'relative', flex: 'none', height: heights[row] ?? 0 }}>
        {alternatives.map(it => <span key={it.key} aria-hidden="true" data-row-measure={row} className={classes[it.style]}
          style={{ position: 'absolute', width: 'max-content', maxWidth: c.nodeTextMaxWidth, height: 'auto', visibility: 'hidden', margin: 0, padding: 0, background: 'transparent' }}>{it.text}</span>)}
        {item && <span data-group-text={item.key} className={classes[item.style]} style={{ position: 'absolute', width: 'max-content', maxWidth: c.nodeTextMaxWidth, margin: 0, padding: 0, background: 'transparent', pointerEvents: 'auto' }}>{item.text}</span>}
        {selected && !item && <span data-group-text={groupTextKey(e.id, selected)}>unsupported group overlay: {selected}</span>}
      </div>
    })}
  </div>
}

