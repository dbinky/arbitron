import { createRoot } from 'react-dom/client'
import '../src/styles/theme.css'
import { fixtureGeometryWithBoxes, fixtureWorkflow } from '../src/graph/arranged/testFixtures'
import { RENDERER_CONTRACT as contract } from '../src/graph/arranged/validate'
import { runMeasureJob } from '../src/render/MeasureSurface'
import { runRenderJob } from '../src/render/RenderSurface'
import { RUNTIME_STATES } from '../src/render/job'
import type { Measurements, TextItem } from '../src/render/job'

const supported = 'Café e\u0301 測試 😀 👩‍💻 ☠︎ '.repeat(4).trim()
const cases: Record<string, string> = {
  supported,
  scalar: '\u{10ffff}',
  variation: 'A\ufe0f',
  zwj: 'A\u200d😀',
}
const params = new URLSearchParams(location.search)
const text = cases[params.get('text') ?? 'supported']!
const rejectAtRender = params.get('stage') === 'render'
const root = createRoot(document.querySelector('#render-root')!)
const items: TextItem[] = [
  { key: 'label', text: rejectAtRender ? supported : text, style: 'node_label', maxWidth: contract.nodeTextMaxWidth },
  { key: 'caption', text: 'llm call', style: 'node_type', maxWidth: contract.nodeTextMaxWidth },
  { key: 'edge', text: '好 Café 😀', style: 'edge_label', maxWidth: contract.edgeLabelMaxWidth },
]

// Drive the real measure -> sized payload -> shared renderer seam. No reported
// glyph bounds, loaded-font result or rendered text is supplied by the fixture.
// `measured` replays a baseline measurement so the render stage runs in a fresh
// document whose first real font request happens while rendering (the browser
// never re-requests a face it already loaded during measure).
const replay = params.get('measured')
try {
  document.documentElement.dataset.portabilityStage = replay ? 'render' : 'measure'
  const measured: Measurements = replay ? JSON.parse(replay)
    : await runMeasureJob({ mode: 'measure', request: { contract, items } }, root, document, window)
  const { label, caption, edge } = measured.items
  const width = Math.ceil(Math.max(label.width, caption.width)) + 1 + 2 * (contract.nodePaddingX + contract.nodeBorderPx)
  const height = Math.ceil(label.height) + Math.ceil(caption.height) + 2 + contract.nodeTextGapPx +
    2 * (contract.nodePaddingY + contract.nodeBorderPx) + contract.nodeTypeBarPx
  const geometry = fixtureGeometryWithBoxes(Object.fromEntries(['a', 'b', 'c'].map((id, i) => [id, { x: 40, y: 40 + i * (height + 100), w: width, h: height }])))
  geometry.nodes.forEach(node => { node.text = text; node.textLines = label.lines })
  for (const route of geometry.edges) {
    if (!route.text) continue
    route.text.text = items[2].text
    if (route.output) route.output = items[2].text
    if (route.label) route.label = items[2].text
    route.text.box.w = Math.ceil(edge.width) + 1 + 2 * contract.edgeLabelPaddingX
    route.text.box.h = Math.ceil(edge.height) + 1 + 2 * contract.edgeLabelPaddingY
    route.text.lines = edge.lines
    geometry.canvas.w = Math.max(geometry.canvas.w, route.text.box.x + route.text.box.w + geometry.options.margin)
  }
  document.documentElement.dataset.portabilityStage = 'render'
  const report = await runRenderJob({ mode: 'render', revision: 'rev-portable-fonts', attempt: 1, geometry,
    workflowNodeTypes: Object.fromEntries(fixtureWorkflow().nodes.map(node => [node.id, node.type])), states: [...RUNTIME_STATES],
  }, root, document, window)
  document.querySelector('#result')!.textContent = JSON.stringify({ measured, report })
} catch (error) {
  document.querySelector('#result')!.textContent = JSON.stringify({ error: String(error), stage: document.documentElement.dataset.portabilityStage })
}
