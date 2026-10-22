import { render } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { createRoot } from 'react-dom/client'
import contract from '../graph/arranged/contract.json'
import type { RendererContract } from '../graph/arranged/types'
import { classForStyle, MeasureSurface, metricsFrom, runMeasureJob } from './MeasureSurface'

// Keep every real export (the modules are imported for their class names only)
// but pin the class names so the assertions are independent of CSS-module hashing.
vi.mock('../graph/arranged/ArrangedNode', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../graph/arranged/ArrangedNode')>()),
  nodeTextClassNames: { label: 'cls-label', typeCaption: 'cls-type' },
}))
vi.mock('../graph/arranged/RoutedEdge', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../graph/arranged/RoutedEdge')>()),
  edgeLabelClassName: 'cls-edge',
}))

describe('classForStyle', () => {
  it('uses the renderer class of each text style', () => {
    expect(classForStyle('node_label')).toBe('cls-label')
    expect(classForStyle('node_type')).toBe('cls-type')
    expect(classForStyle('edge_label')).toBe('cls-edge')
  })
})

describe('metricsFrom', () => {
  it('subtracts padding/border and retains fractional bounds', () => {
    const m = metricsFrom({ width: 43.2, height: 20.85 }, { padX: 12, padY: 6 }, 14.85, 'yes')
    expect(m.width).toBeCloseTo(31.2)
    expect(m.height).toBeCloseTo(14.85)
    expect(m.lines).toBe(1)
  })
  it('counts wrapped lines from the content height', () => {
    expect(metricsFrom({ width: 240, height: 35.1 }, { padX: 0, padY: 0 }, 17.55, 'long text')).toEqual({ width: 240, height: 35.1, lines: 2 })
  })
  it('reports nothing for empty text', () => {
    expect(metricsFrom({ width: 12, height: 6 }, { padX: 12, padY: 6 }, 14.85, '')).toEqual({ width: 0, height: 0, lines: 0 })
  })
})

describe('MeasureSurface', () => {
  it('renders each item with its style class, its max width and a content box', () => {
    const { container } = render(
      <MeasureSurface fontFamily="Inter" items={[
        { key: 'node:a:label', text: 'Start', style: 'node_label', maxWidth: 240 },
        { key: 'edge:0', text: 'yes', style: 'edge_label', maxWidth: 160 },
      ]} />,
    )
    const root = container.querySelector<HTMLElement>('[data-measure-root]')!
    expect(root.style.visibility).toBe('hidden')
    expect(root.style.fontFamily).toBe('Inter')
    const label = container.querySelector<HTMLElement>('[data-measure-key="node:a:label"]')!
    expect(label.className).toBe('cls-label')
    expect(label.textContent).toBe('Start')
    expect(label.style.maxWidth).toBe('240px')
    expect(label.style.boxSizing).toBe('content-box')
    expect(container.querySelector<HTMLElement>('[data-measure-key="edge:0"]')!.className).toBe('cls-edge')
  })
})

describe('runMeasureJob', () => {
  it('rejects an unvalidated or duplicate-key request before mounting', async () => {
    const root = createRoot(document.createElement('div'))
    const item = { key: 'k', text: 'x', style: 'node_label' as const, maxWidth: 240 }
    await expect(runMeasureJob({ mode: 'measure', request: { contract, items: [item, item] } }, root, document, window)).rejects.toThrow(/invalid text item or duplicate key/)
    root.unmount()
  })
  it('returns metrics for every requested key and never claims fonts it cannot verify', async () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    const root = createRoot(host)
    const m = await runMeasureJob({ mode: 'measure', request: { contract: contract as RendererContract, items: [
      { key: 'node:a:label', text: 'Start', style: 'node_label', maxWidth: 240 },
      { key: 'edge:0', text: '', style: 'edge_label', maxWidth: 160 },
    ] } }, root, document, window)
    expect(Object.keys(m.items).sort()).toEqual(['edge:0', 'node:a:label'])
    expect(m.items['edge:0']).toEqual({ width: 0, height: 0, lines: 0 })
    expect(m.fontsReady).toBe(false) // jsdom has no FontFaceSet
    expect(m.contract.version).toBe('arranged-renderer/2')
  })
})
