import { createRoot } from 'react-dom/client'
import '../src/styles/theme.css'
import { fixtureGeometry, fixtureWorkflow } from '../src/graph/arranged/testFixtures'
import { RUNTIME_STATES } from '../src/render/job'
import type { RenderJob } from '../src/render/job'
import { runRenderJob } from '../src/render/RenderSurface'
import { runMeasureJob } from '../src/render/MeasureSurface'
import { nodeTextClassNames } from '../src/graph/arranged/ArrangedNode'
import { collectCrossings, collectEdges, collectNodes, stabilityIssues } from '../src/render/report'
import type { CollectContext } from '../src/render/report'

let assertions = 0
function check(value: unknown, message: string) { assertions++; if (!value) throw new Error(message) }
const result = document.querySelector('#result')!
try {
  const geometry = fixtureGeometry()
  geometry.nodes[1].text = 'Café 測試 😀'
  geometry.edges[1].text!.box.w = 40
  const hitTesting = document.createElement('style')
  hitTesting.textContent = '#app * { pointer-events:none !important } #app [data-arranged-node], #app [data-arranged-node] *, #app [data-edge-label], #app [data-edge-label] *, #app path[data-edge-path] { pointer-events:auto !important }'
  document.head.append(hitTesting)
  const job: RenderJob = { mode: 'render', revision: 'rev-1', attempt: 1, geometry, workflowNodeTypes: Object.fromEntries(fixtureWorkflow().nodes.map(n => [n.id, n.type])), states: [...RUNTIME_STATES] }
  const root = createRoot(document.querySelector('#app')!)
  const report = await runRenderJob(job, root, document, window)
  check(report.fontsReady && report.fontFamilyResolved, 'bundled Unicode fonts must be ready')
  check(report.nodes.length === 3 && report.edges.length === 3, 'complete actual DOM coverage')
  check(report.nodes[1].label.content === 'Café 測試 😀' && report.nodes[1].label.glyphs.length > 0, 'actual Unicode glyph content')
  check(report.edges.every(e => e.visible && e.hasArrow && e.arrowPoints.length === 3 && e.segments.length > 0), 'painted route and arrow outlines')
  check(report.edges[2].dashed && !report.edges[0].dashed, 'actual dash styles')
  check(report.states.length === 7 && report.states.every(s => s.nodeBoxesStable && s.labelsReadable), `readable stable state probes: ${JSON.stringify(report.states)}`)
  const ctx: CollectContext = { doc: document, win: window, root: document.querySelector('[data-render-root]')!, zoom: geometry.contract.practicalZoom, origin: geometry.canvas }
  const label = document.querySelector<HTMLElement>('[data-arranged-node="a"] [data-node-text="label"]')!
  label.textContent = 'Changed'
  check(collectNodes(geometry, ctx)[0].label.content === 'Changed', 'collector must not echo candidate text')
  label.parentElement!.style.opacity = '0'
  check(!collectNodes(geometry, ctx)[0].label.visible, 'ancestor opacity must suppress glyph visibility')
  label.parentElement!.style.opacity = ''
  label.textContent = geometry.nodes[0].text
  const path = document.querySelector<SVGPathElement>('[data-edge-path="0"]')!, original = path.getAttribute('d')!
  path.setAttribute('d', original.replace('130,116', '125,116'))
  const changed = collectEdges(geometry, ctx)
  check(stabilityIssues(report, { ...report, edges: changed }).some(i => i.includes('route changed')), 'middle route changes with stable endpoints must fail')
  path.setAttribute('d', original)
  const marker = path.parentElement!.querySelector('marker')!
  marker.setAttribute('orient', '180')
  check(!collectEdges(geometry, ctx)[0].hasArrow, 'reversed marker must not be a valid arrow')
  marker.setAttribute('orient', 'auto')
  marker.style.opacity = '0'
  check(!collectEdges(geometry, ctx)[0].hasArrow, 'invisible marker cannot pass')
  marker.style.opacity = ''
  path.style.opacity = '0'
  check(!collectEdges(geometry, ctx)[0].hasArrow, 'referencing path opacity suppresses its marker ink too')
  path.style.opacity = ''
  const outline = marker.querySelector('path')!
  outline.style.transform = 'translate(1px, 0px)'
  let transformedMarkerBlocked = false
  try { transformedMarkerBlocked = !collectEdges(geometry, ctx)[0].hasArrow } catch { transformedMarkerBlocked = true }
  check(transformedMarkerBlocked, 'CSS-transformed clipped marker must not be accepted unchanged')
  outline.style.transform = ''
  // Probe a true M-gap and its full neighborhood. Candidate crossings supply only
  // locations; the paths supply all painted stroke evidence.
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
  svg.style.cssText = 'position:absolute;left:0;top:0;width:100px;height:100px'
  svg.innerHTML = '<path data-edge-path="8" d="M 0,50 L 100,50" style="stroke:#8a8ab0;stroke-width:2;fill:none"/><path data-edge-path="9" d="M 50,0 L 50,45 M 50,55 L 50,100" style="stroke:#8a8ab0;stroke-width:2;fill:none"/>'
  document.body.append(svg)
  const crossingContext = { ...ctx, root: svg, zoom: 1, origin: { x: 0, y: 0 } }
  const crossed = { ...geometry, crossings: [{ point: { x: 50, y: 50 }, over: 8, under: 9 }] }
  check(collectCrossings(crossed, crossingContext).every(c => c.underGapVisible && c.overContinuous), 'full actual crossing gap')
  svg.children[1].setAttribute('d', 'M 50,0 L 50,49 M 50,51 L 50,100')
  check(!collectCrossings(crossed, crossingContext)[0].underGapVisible, 'tiny hole at centre is not a full gap')
  svg.remove()
  // Exercise the real state loop, not only the comparison helper. A runtime-only
  // branch-label displacement must be recorded at the final StateProbe sink.
  const style = document.createElement('style')
  style.textContent = '[data-arranged-canvas]:has([data-state="running"]) [data-edge-label] { margin-left:4px !important }'
  document.head.append(style)
  const defective = await runRenderJob(job, root, document, window)
  check(defective.states.some(s => s.state === 'running' && !s.nodeBoxesStable && s.issues?.some(i => i.includes('label changed'))), 'runtime branch-label movement must block')
  style.textContent = '@keyframes badLabel { 0%,100% { transform:translateX(0) } 50% { transform:translateX(5px) } } [data-state="running"] [data-node-text="label"] { animation:badLabel 800ms infinite }'
  const animated = await runRenderJob(job, root, document, window)
  check(animated.states.some(s => s.state === 'running' && !s.nodeBoxesStable && s.issues?.some(i => i.includes('text changed'))), 'harmless endpoint animation frames cannot hide a defective midpoint')
  style.remove()
  const observer = new MutationObserver(() => {
    if (!document.querySelector('[data-state="running"]')) return
    const changedD = 'M 130,92 L 130,100 M 130,110 L 130,116 L 140,116 L 140,140'
    for (const el of document.querySelectorAll('[data-edge-path="0"], [data-edge-overlay="0"]')) if (el.getAttribute('d') !== changedD) el.setAttribute('d', changedD)
  })
  observer.observe(document.querySelector('#app')!, { subtree: true, childList: true, attributes: true, attributeFilter: ['data-state', 'd'] })
  const changingGap = await runRenderJob(job, root, document, window)
  observer.disconnect()
  check(changingGap.states.some(s => s.state === 'running' && !s.nodeBoxesStable && s.issues?.some(i => i.includes('route changed'))), 'runtime gap movement with unchanged endpoints must block')
  document.querySelector('[data-edge-path="0"]')!.setAttribute('d', original)
  const measureHost = document.createElement('div')
  document.body.append(measureHost)
  const measureRoot = createRoot(measureHost)
  const items = [
    { key: 'wrapped', text: 'Café 測試 😀 '.repeat(20), style: 'node_label' as const, maxWidth: 240 },
    { key: 'empty', text: '', style: 'edge_label' as const, maxWidth: 160 },
    { key: 'caption', text: 'llm call', style: 'node_type' as const, maxWidth: 240 },
  ]
  const measured = await runMeasureJob({ mode: 'measure', request: { contract: geometry.contract, items } }, measureRoot, document, window)
  check(measured.fontsReady && measured.items.wrapped.width > 0 && measured.items.wrapped.width <= 240 && measured.items.wrapped.lines > 1, 'real fractional Unicode/wrapping measurements')
  check(JSON.stringify(measured.items.empty) === JSON.stringify({ width: 0, height: 0, lines: 0 }), 'empty text has zero metrics')
  const measuredLabel = measureHost.querySelector('[data-measure-key="wrapped"]')!
  check(measuredLabel.className === nodeTextClassNames.label, 'measure uses actual renderer class')
  const actualLabel = document.querySelector('[data-arranged-node="b"] [data-node-text="label"]')!
  check(['fontFamily', 'fontSize', 'fontWeight', 'lineHeight', 'whiteSpace', 'overflowWrap'].every(key => getComputedStyle(measuredLabel)[key as keyof CSSStyleDeclaration] === getComputedStyle(actualLabel)[key as keyof CSSStyleDeclaration]), 'measured/rendered computed typography parity')
  const narrower = await runMeasureJob({ mode: 'measure', request: { contract: geometry.contract, items: [{ ...items[0], maxWidth: 80 }] } }, measureRoot, document, window)
  check(narrower.items.wrapped.width <= 80 && narrower.items.wrapped.lines > measured.items.wrapped.lines, 'maxWidth must feed the metric sink')
  const empty = await runMeasureJob({ mode: 'measure', request: { contract: geometry.contract, items: [] } }, measureRoot, document, window)
  check(!empty.fontsReady && Object.keys(empty.items).length === 0, 'absent measurements cannot claim font readiness')
  let unsupported = false
  try { await runMeasureJob({ mode: 'measure', request: { contract: geometry.contract, items: [{ ...items[0], text: '\u{10ffff}' }] } }, measureRoot, document, window) } catch { unsupported = true }
  check(unsupported, 'unsupported glyph cannot fall through to system fonts')
  measureRoot.unmount()
  measureHost.remove()
  result.textContent = JSON.stringify({ ready: true, assertions })
} catch (error) {
  result.textContent = JSON.stringify({ error: String(error), details: document.querySelector('#app')!.innerHTML })
}
