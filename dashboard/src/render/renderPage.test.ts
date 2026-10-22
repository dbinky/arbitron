import { describe, expect, it } from 'vitest'
import html from '../../render.html?raw'

// render.html is the second Vite entry; the Go browser renderer navigates to
// /render.html?job=<token>. These guards keep captures deterministic.
describe('render.html', () => {
  it('boots the render surface entry into #render-root', () => {
    expect(html).toContain('<div id="render-root"></div>')
    expect(html).toContain('<script type="module" src="/src/render/main.tsx"></script>')
  })
  it('freezes capture after runtime animation phase probes', () => {
    expect(html).toContain('transition: none !important')
    expect(html).toContain('animation-play-state: paused !important')
  })
  it('limits hit-testing to arranged content so occlusion probes are meaningful', () => {
    expect(html).toContain('#render-root * { pointer-events: none !important; }')
    expect(html).toContain('#render-root path[data-edge-path] { pointer-events: auto !important; }')
  })
  it('hides the React Flow attribution so only the graph is painted', () => {
    expect(html).toContain('.react-flow__attribution { display: none !important; }')
  })
})
