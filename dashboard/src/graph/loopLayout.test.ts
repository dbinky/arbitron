import { describe, it, expect } from 'vitest'
import type { Loop } from './loops'
import {
  loopContainerLabel,
  loopContainerBox,
  CONTAINER_PAD,
  DEPTH_INSET,
} from './loopLayout'

const makeLoop = (over: Partial<Loop>): Loop => ({
  id: 'loop-b-c',
  headId: 'b',
  tailId: 'c',
  bodyNodeIds: ['b', 'c'],
  backEdge: { source: 'c', target: 'b' },
  depth: 0,
  ...over,
})

describe('loopContainerLabel — happy path & boundary', () => {
  it('prefixes a non-empty back-edge label with "Loop until "', () => {
    expect(
      loopContainerLabel(makeLoop({ backEdge: { source: 'c', target: 'b', label: 'tests pass' } })),
    ).toBe('Loop until tests pass')
  })

  it('falls back to "Loop" when the label is missing', () => {
    expect(loopContainerLabel(makeLoop({ backEdge: { source: 'c', target: 'b' } }))).toBe('Loop')
  })

  it('falls back to "Loop" when the label is whitespace-only', () => {
    expect(
      loopContainerLabel(makeLoop({ backEdge: { source: 'c', target: 'b', label: '   ' } })),
    ).toBe('Loop')
  })
})

describe('loopContainerBox — happy path', () => {
  it('wraps the body nodes with padding, accounting for node size', () => {
    // b at (100,0), c at (100,160). NODE_WIDTH=200, NODE_HEIGHT=80.
    const positionMap = { b: { x: 100, y: 0 }, c: { x: 100, y: 160 } }
    const box = loopContainerBox(makeLoop({}), positionMap)!
    // minX=100, minY=0, maxX=100+200=300, maxY=160+80=240.
    expect(box.x).toBe(100 - CONTAINER_PAD)
    expect(box.y).toBe(0 - CONTAINER_PAD)
    expect(box.width).toBe(300 - 100 + CONTAINER_PAD * 2)
    expect(box.height).toBe(240 - 0 + CONTAINER_PAD * 2)
  })
})

describe('loopContainerBox — boundary: nesting inset', () => {
  it('insets deeper loops so nested containers sit inside their parent', () => {
    const positionMap = { b: { x: 100, y: 0 }, c: { x: 100, y: 160 } }
    const shallow = loopContainerBox(makeLoop({ depth: 0 }), positionMap)!
    const deep = loopContainerBox(makeLoop({ depth: 2 }), positionMap)!
    expect(deep.x).toBe(shallow.x + DEPTH_INSET * 2)
    expect(deep.y).toBe(shallow.y + DEPTH_INSET * 2)
    expect(deep.width).toBe(shallow.width - DEPTH_INSET * 2 * 2)
    expect(deep.height).toBe(shallow.height - DEPTH_INSET * 2 * 2)
  })
})

describe('loopContainerBox — edge case: self-loop (single node)', () => {
  it('wraps a single body node', () => {
    const positionMap = { b: { x: 50, y: 50 } }
    const box = loopContainerBox(
      makeLoop({ id: 'loop-b-b', headId: 'b', tailId: 'b', bodyNodeIds: ['b'] }),
      positionMap,
    )!
    expect(box.x).toBe(50 - CONTAINER_PAD)
    expect(box.width).toBe(200 + CONTAINER_PAD * 2)
  })
})

describe('loopContainerBox — error/invalid: no known positions', () => {
  it('returns null when none of the body nodes are positioned', () => {
    expect(loopContainerBox(makeLoop({}), {})).toBeNull()
  })

  it('ignores body ids absent from the positionMap', () => {
    const box = loopContainerBox(
      makeLoop({ bodyNodeIds: ['b', 'ghost'] }),
      { b: { x: 0, y: 0 } },
    )!
    expect(box.x).toBe(0 - CONTAINER_PAD)
    expect(box.width).toBe(200 + CONTAINER_PAD * 2)
  })
})
