import { createRoot } from 'react-dom/client'
import { Position, ReactFlow, ReactFlowProvider } from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import '../src/styles/theme.css'
import ArrangedNode, { nodeTextClassNames } from '../src/graph/arranged/ArrangedNode'
import RoutedEdge, { edgeLabelClassName } from '../src/graph/arranged/RoutedEdge'
import { fixtureGeometry } from '../src/graph/arranged/testFixtures'
import { RENDERER_CONTRACT as contract } from '../src/graph/arranged/validate'
import { ensureBundledTextFonts } from '../src/graph/arranged/fonts'

const text = 'Café e\u0301 測試 😀 👩‍💻 ☠︎\n' + '界'.repeat(65)
const geo = fixtureGeometry()
const nodeBox = { ...geo.nodes[1], text, typeCaption: 'a very long type caption '.repeat(6), box: { x: 40, y: 40, w: 269, h: 800 } }
const nodes = [
  { id: 'b', type: 'arrangedNode', selected: true, position: { x: 40, y: 40 }, data: { nodeBox, nodeType: 'llm_call', contract, executionState: 'running' } },
  { id: 'c', type: 'arrangedNode', position: { x: 400, y: 40 }, data: { nodeBox: { ...nodeBox, id: 'c' }, nodeType: 'script', contract, executionState: 'completed' } },
]
const route = { ...geo.edges[1], points: [{ x: 174, y: 840 }, { x: 174, y: 900 }, { x: 534, y: 900 }, { x: 534, y: 840 }], text: { text, box: { x: 200, y: 870, w: 173, h: 300 }, lines: 10 } }
for (const node of nodes) Object.assign(node, {
  width: 269, height: 800,
  handles: [
    { type: 'source', position: Position.Bottom, x: 134, y: 800, width: 1, height: 1 },
    { type: 'target', position: Position.Top, x: 134, y: 0, width: 1, height: 1 },
  ],
})
const edges = [{ id: 'e', type: 'routedEdge', source: 'b', target: 'c', data: { route, gaps: [{ x: 300, y: 900 }], overs: [], contract, graphInstanceId: 'browser-test', active: true } }]

function App() {
  return <ReactFlowProvider><ReactFlow nodes={nodes} edges={edges} nodeTypes={{ arrangedNode: ArrangedNode }} edgeTypes={{ routedEdge: RoutedEdge }} defaultViewport={{ x: 0, y: 0, zoom: 1 }} /></ReactFlowProvider>
}

function probe(className: string, width: number, content: string) {
  const el = document.createElement('span')
  el.className = className
  el.style.cssText = `position:absolute;--node-text-max:${width}px;max-width:${width}px;width:max-content;padding:0;border:0;`
  el.textContent = content
  document.body.append(el)
  return el
}
const rect = (el: Element) => { const r = el.getBoundingClientRect(); return { w: r.width, h: r.height } }
const frame = () => new Promise<void>(resolve => setTimeout(resolve, 50))
const result = document.querySelector('#result')!
try {
  await ensureBundledTextFonts(document, [text, nodeBox.typeCaption], contract)
  createRoot(document.querySelector('#app')!).render(<App />)
  for (let i = 0; i < 10; i++) await frame()
  const node = document.querySelector('[data-arranged-node="b"]') as HTMLElement
  const label = node.querySelector('[data-node-text="label"]') as HTMLElement
  const caption = node.querySelector('[data-node-text="type"]') as HTMLElement
  const edge = document.querySelector('[data-edge-label]') as HTMLElement
  const measuredLabel = probe(nodeTextClassNames.label, contract.nodeTextMaxWidth, text)
  const measuredCaption = probe(nodeTextClassNames.typeCaption, contract.nodeTextMaxWidth, nodeBox.typeCaption)
  const measuredEdge = probe(edgeLabelClassName, contract.edgeLabelMaxWidth, text)
  const styles = (el: Element) => {
    const s = getComputedStyle(el)
    return { whiteSpace: s.whiteSpace, wrap: s.overflowWrap, maxWidth: s.maxWidth, lineHeight: s.lineHeight, letterSpacing: s.letterSpacing, textTransform: s.textTransform, family: s.fontFamily, boxSizing: s.boxSizing }
  }
  const before = { node: rect(node), label: rect(label), caption: rect(caption), edge: rect(edge) }
  const snapshots = []
  for (const time of [0, 450, 900, 1500, 2000]) {
    for (const animation of document.getAnimations()) { animation.pause(); animation.currentTime = time }
    snapshots.push({ boxes: { node: rect(node), label: rect(label), caption: rect(caption), edge: rect(edge) }, opacity: getComputedStyle(node).opacity, path: document.querySelector('[data-edge-path]')!.getAttribute('d') })
  }
  result.textContent = JSON.stringify({ ready: true, before, snapshots, label: styles(label), caption: styles(caption), edge: styles(edge), measuredLabel: rect(measuredLabel), measuredCaption: rect(measuredCaption), measuredEdge: rect(measuredEdge), labelText: label.textContent, captionLines: rect(caption).h / (contract.nodeTypeFontPx * contract.lineHeight), border: getComputedStyle(node).borderWidth })
} catch (error) {
  result.textContent = JSON.stringify({ error: String(error), details: document.querySelector('#app')!.innerHTML })
}
