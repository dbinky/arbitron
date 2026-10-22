// Two complete production graphs: no pre-initialized handles or font/layout doubles.
import { useState } from 'react'
import { createRoot } from 'react-dom/client'
import { ReactFlowProvider } from '@xyflow/react'
import '../src/styles/theme.css'
import ArrangedGraph from '../src/graph/arranged/ArrangedGraph'
import { fixtureGeometry, fixtureView, fixtureWorkflow } from '../src/graph/arranged/testFixtures'

function inputs(loopFirst: boolean) {
  const geometry = fixtureGeometry()
  const definition = fixtureWorkflow()
  // Distinct entry ports keep the forward and loop arrowheads separately visible.
  for (const [ordinal, x] of [[0, 100], [2, 190]]) {
    const edge = geometry.edges[ordinal]
    edge.points[edge.points.length - 2].x = x
    edge.points[edge.points.length - 1].x = x
    edge.targetAttach.point.x = x
  }
  if (loopFirst) {
    const order = [2, 0, 1]
    geometry.edges = order.map((old, ordinal) => ({ ...geometry.edges[old], ordinal }))
    definition.edges = order.map(old => definition.edges[old])
  }
  return { definition, view: fixtureView({ geometry }) }
}
const first = inputs(false), second = inputs(true)
function Graph({ input }: { input: ReturnType<typeof inputs> }) {
  return <ReactFlowProvider><ArrangedGraph workflowDef={input.definition} view={input.view}
    selectedNodeId={null} onNodeClick={() => {}} anchorNodeId={null}
    fixedViewport={{ x: 0, y: 0, zoom: 1 }} /></ReactFlowProvider>
}
function App() {
  const [showFirst, setShowFirst] = useState(true)
  return <>
    <button id="remove-first" onClick={() => setShowFirst(false)} style={{ position: 'absolute', top: 440 }}>Remove first graph</button>
    {showFirst && <section key="first" data-graph="first" style={{ position: 'absolute', left: 0, top: 0, width: 400, height: 400 }}><Graph input={first} /></section>}
    <section key="second" data-graph="second" style={{ position: 'absolute', left: 500, top: 0, width: 400, height: 400 }}><Graph input={second} /></section>
  </>
}
createRoot(document.querySelector('#app')!).render(<App />)

// The driver checks these against both the DOM and real compositor pixels.
export const expected = {
  first: first.view.geometry!.edges.map(edge => ({
    ordinal: edge.ordinal, points: edge.points,
    color: edge.kind === 'forward' ? '#8a8ab0' : '#7c8cf5',
  })),
  second: second.view.geometry!.edges.map(edge => ({
    ordinal: edge.ordinal, points: edge.points,
    color: edge.kind === 'forward' ? '#8a8ab0' : '#7c8cf5',
  })),
}
