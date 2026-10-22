// Measure mode of the render surface: lays every requested text item out with
// the exact CSS class the arranged renderer uses for that style, so Phase 1
// sizes boxes from the real browser, fonts and wrapping.
import { flushSync } from 'react-dom'
import type { Root } from 'react-dom/client'
import { nodeTextClassNames } from '../graph/arranged/ArrangedNode'
import { edgeLabelClassName } from '../graph/arranged/RoutedEdge'
import type { MeasureJob, Measurements, TextItem, TextMetrics, TextStyle } from './job'
import { parseJob } from './job'
import { RENDERER_CONTRACT } from '../graph/arranged/validate'
import { ensureFonts, settle } from './report'

/** The renderer class for each text style (identical to ArrangedNode / RoutedEdge). */
export function classForStyle(style: TextStyle): string {
  switch (style) {
    case 'node_label':
      return nodeTextClassNames.label
    case 'node_type':
      return nodeTextClassNames.typeCaption
    case 'edge_label':
      return edgeLabelClassName
  }
}

export interface BoxExtras { padX: number; padY: number }

/**
 * Content-box metrics from a laid-out element: the border box minus padding
 * and border, retaining actual fractional bounds; domain ceilPx owns box
 * rounding/slack. Lines come from actual content height and line height.
 */
export function metricsFrom(rect: { width: number; height: number }, extras: BoxExtras, lineHeightPx: number, text: string): TextMetrics {
  const w = Math.max(0, rect.width - extras.padX)
  const h = Math.max(0, rect.height - extras.padY)
  if (text === '') return { width: 0, height: 0, lines: 0 }
  return { width: w, height: h, lines: Math.max(1, Math.round(h / lineHeightPx)) }
}

export function MeasureSurface({ items, fontFamily }: { items: TextItem[]; fontFamily: string }) {
  return (
    <div
      data-measure-root="true"
      style={{ position: 'absolute', left: 0, top: 0, width: 0, height: 0, overflow: 'visible', visibility: 'hidden', fontFamily }}
    >
      {items.map((it) => (
        <div
          key={it.key}
          data-measure-key={it.key}
          className={classForStyle(it.style)}
          style={{ position: 'absolute', left: 0, top: 0, margin: 0, width: 'max-content', maxWidth: it.maxWidth, boxSizing: 'content-box', transform: 'none' }}
        >
          {it.text}
        </div>
      ))}
    </div>
  )
}

function px(v: string): number {
  const n = parseFloat(v)
  return Number.isFinite(n) ? n : 0
}

export async function runMeasureJob(job: MeasureJob, root: Root, doc: Document, win: Window): Promise<Measurements> {
  parseJob(job)
  const items = job.request.items
  await ensureFonts(doc, RENDERER_CONTRACT, items.map(it => it.text))
  flushSync(() => root.render(<MeasureSurface items={items} fontFamily={RENDERER_CONTRACT.fontFamily} />))
  await doc.fonts?.ready
  await settle(doc, win)
  const fonts = await ensureFonts(doc, RENDERER_CONTRACT, items.map(it => it.text))

  const out: Record<string, TextMetrics> = {}
  for (const it of items) {
    const el = doc.querySelector(`[data-measure-key="${CSS.escape(it.key)}"]`)
    if (!el) throw new Error(`measure surface: item ${it.key} did not render`)
    const cs = win.getComputedStyle(el)
    const extras = {
      padX: px(cs.paddingLeft) + px(cs.paddingRight) + px(cs.borderLeftWidth) + px(cs.borderRightWidth),
      padY: px(cs.paddingTop) + px(cs.paddingBottom) + px(cs.borderTopWidth) + px(cs.borderBottomWidth),
    }
    const fontPx = px(cs.fontSize)
    const lh = parseFloat(cs.lineHeight)
    const lineHeightPx = Number.isFinite(lh) && lh > 0 ? lh : fontPx * RENDERER_CONTRACT.lineHeight
    out[it.key] = metricsFrom(el.getBoundingClientRect(), extras, lineHeightPx, it.text)
  }
  return { contract: RENDERER_CONTRACT, fontsReady: items.length > 0 && fonts.fontsReady && fonts.fontFamilyResolved, items: out }
}
