import type { GroupGraph } from '../api/types'
import type { Geometry, Rect } from '../graph/arranged/types'
import { groupOverlayItems, groupTextKey } from '../graph/arranged/groupVocabulary'
import type { RenderedGroupElement, RenderedGroupConnector, RenderedGroupCrossing, RenderedText } from './job'
import type { GroupProbeCase } from './groupProbes'
import { textReadabilityIssues, rectInside, stabilityIssues, toCanvasRect } from './report'
import type { CollectContext } from './report'

export interface GroupSnapshot {
  groupElements?: RenderedGroupElement[]
  groupConnectors?: RenderedGroupConnector[]
  groupCrossings?: RenderedGroupCrossing[]
}
const sameBox = (a: Rect, b: Rect) => ['x', 'y', 'w', 'h'].every(k => Math.abs(a[k as keyof Rect] - b[k as keyof Rect]) <= 1)

type OverlayRows = Map<string, Rect>
const rowKey = (id: string, row: string) => JSON.stringify([id, row])
export function collectGroupRows(ctx: CollectContext): OverlayRows {
  const rows: OverlayRows = new Map()
  for (const overlay of ctx.root.querySelectorAll('[data-group-overlay]')) {
    for (const row of overlay.querySelectorAll('[data-overlay-row]')) {
      const key = rowKey(overlay.getAttribute('data-group-overlay')!, row.getAttribute('data-overlay-row')!)
      if (rows.has(key)) throw new Error(`render surface: duplicate group overlay row ${key}`)
      rows.set(key, toCanvasRect(row.getBoundingClientRect(), ctx.root.getBoundingClientRect(), ctx.zoom, ctx.origin))
    }
  }
  return rows
}
export function groupRowIssues(base: OverlayRows, now: OverlayRows, elements: RenderedGroupElement[]): string[] {
  const issues: string[] = []
  if (base.size !== now.size) issues.push('group overlay row coverage changed')
  for (const [key, box] of base) {
    const actual = now.get(key)
    if (!actual || !sameBox(box, actual)) issues.push(`group overlay row ${key} moved`)
  }
  for (const e of elements) for (const t of e.texts) {
    const field = JSON.parse(t.key)[2] as string
    if (!field.startsWith('overlay:')) continue
    const parts = field.split(':'), row = parts[1] === 'progress' || parts[1] === 'badge' ? `${parts[1]}:${parts[2]}` : parts[1]
    const box = base.get(rowKey(e.id, row))
    if (!box || !rectInside(t.text.box, box) || t.text.glyphs.some(g => !rectInside(g, box))) issues.push(`group text ${t.key} outside reserved row`)
  }
  return issues
}

/** Check actual snapshots, including changing text against the captured finite vocabulary. */
export function groupProbeIssues(geo: Geometry, groups: GroupGraph, base: GroupSnapshot, now: GroupSnapshot, probe?: GroupProbeCase): { stability: string[]; readability: string[] } {
  const stability: string[] = [], readability: string[] = []
  const vocabulary = groupOverlayItems(groups, geo.contract.nodeTextMaxWidth)
  const checkText = (text: RenderedText, name: string, box: Rect) => {
    readability.push(...textReadabilityIssues(text, name, geo.contract, geo.contract.practicalZoom))
    if (!rectInside(text.box, box) || text.glyphs.some(g => !rectInside(g, box))) readability.push(`${name} outside reserved row`)
  }
  for (const e of geo.groupElements ?? []) {
    const a = base.groupElements?.find(a => a.id === e.id), b = now.groupElements?.find(b => b.id === e.id)
    if (!a || !b) { stability.push(`group element ${e.id} disappeared`); continue }
    if (!sameBox(a.box, b.box) || a.borderPx !== b.borderPx) stability.push(`group element ${e.id} box changed`)
    if (!b.visible || b.borderPx <= 0 || /transparent|rgba\([^)]*,\s*0\)/.test(b.backgroundColor)) readability.push(`group element ${e.id} missing paint`)
    const live = probe?.groupLive[e.groupNodeId], task = live?.tasks[e.taskKey ?? '']
    if (probe && (!live || ((e.role === 'task' || e.role === 'lane') && !task))) readability.push(`group element ${e.id} missing runtime input`)
    const expected = new Map(e.texts.map(t => [t.key, { text: task?.redacted && JSON.parse(t.key)[2] === 'title' ? 'hidden dependency' : t.text, box: t.box }]))
    const fields: string[] = []
    if (e.role === 'footer' && live) {
      fields.push(`overlay:status:${live.state}`)
      if (live.winner) fields.push(`overlay:winner:${live.winner}`)
      if (live.progress) {
        fields.push(`overlay:progress:done:${live.progress.completed}`, `overlay:progress:active:${live.progress.active}`,
          `overlay:progress:not started:${live.progress.notStarted}`, `overlay:progress:failed:${live.progress.failed}`)
      }
    } else if (task && (e.role === 'task' || e.role === 'lane')) {
      fields.push(`overlay:status:${task.status}`)
      if (task.frozenStatus) fields.push(`overlay:frozen:${task.frozenStatus}`)
      if (task.winner) fields.push('overlay:badge:winner')
      if (task.continuingIndependently) fields.push('overlay:badge:non-blocking')
      if (task.cancelRequested) fields.push('overlay:badge:cancel requested')
    }
    for (const field of fields) {
      const key = groupTextKey(e.id, field), item = vocabulary.find(i => i.key === key)
      if (!item || !e.overlayBox) readability.push(`group text ${key} unsupported runtime value`)
      else expected.set(key, { text: item.text, box: e.overlayBox })
    }
    if (b.texts.length !== expected.size) readability.push(`group element ${e.id} runtime text coverage changed`)
    for (const [key, want] of expected) {
      const found = b.texts.filter(t => t.key === key)
      if (found.length !== 1) { readability.push(`group text ${key} missing or duplicate`); continue }
      const text = found[0].text
      if (text.content !== want.text) readability.push(`group text ${key} content changed`)
      checkText(text, `group text ${key}`, want.box)
      const old = a.texts.find(t => t.key === key)?.text
      if (old && !fields.some(f => groupTextKey(e.id, f) === key) && !(task?.redacted && JSON.parse(key)[2] === 'title')) {
        if (!sameBox(old.box, text.box) || JSON.stringify(old.glyphs) !== JSON.stringify(text.glyphs)) stability.push(`group text ${key} moved`)
      }
    }
    // Simultaneously visible overlay rows must not obscure one another.
    const overlays = b.texts.filter(t => fields.some(f => groupTextKey(e.id, f) === t.key))
    for (let i = 1; i < overlays.length; i++) {
      const prev = overlays[i - 1].text.box, next = overlays[i].text.box
      if (prev.y + prev.h > next.y + 1) readability.push(`group element ${e.id} overlay rows overlap`)
    }
  }
  const routes = (s: GroupSnapshot) => (s.groupConnectors ?? []).map((c, ordinal) => ({ ...c, ordinal, dashed: false }))
  stability.push(...stabilityIssues({ nodes: [], edges: routes(base) }, { nodes: [], edges: routes(now) }).map(i => `group connector ${i}`))
  for (const c of now.groupConnectors ?? []) if (!c.visible || !c.hasArrow) readability.push(`group connector ${c.id} missing path or arrow`)
  if (JSON.stringify(base.groupCrossings) !== JSON.stringify(now.groupCrossings)) stability.push('group crossing separation changed')
  for (const c of now.groupCrossings ?? []) if (!c.underGapVisible || !c.overContinuous) readability.push(`group crossing ${c.over}/${c.under} separation missing`)
  return { stability: [...new Set(stability)], readability: [...new Set(readability)] }
}
