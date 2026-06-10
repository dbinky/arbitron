import { describe, it, expect } from 'vitest'
import {
  chooseSide,
  GUTTER_OFFSET,
  loopHandleIds,
  buildLoopBackPath,
} from './loopEdgeRouting'

describe('chooseSide', () => {
  it('routes right when endpoints are left of the box center', () => {
    const r = chooseSide({ sourceX: 120, targetX: 120, boxX: 100, boxWidth: 200 })
    expect(r.side).toBe('right')
    expect(r.gutterX).toBe(100 + 200 + GUTTER_OFFSET)
  })

  it('routes left when endpoints are right of the box center', () => {
    const r = chooseSide({ sourceX: 280, targetX: 280, boxX: 100, boxWidth: 200 })
    expect(r.side).toBe('left')
    expect(r.gutterX).toBe(100 - GUTTER_OFFSET)
  })

  it('defaults to right on a centered tie', () => {
    const r = chooseSide({ sourceX: 200, targetX: 200, boxX: 100, boxWidth: 200 })
    expect(r.side).toBe('right')
  })

  it('averages asymmetric sourceX/targetX to determine side (diamond loops)', () => {
    // source x=50, target x=350 → midpoint=200 = box center → right (tie rule)
    expect(chooseSide({ sourceX: 50, targetX: 350, boxX: 100, boxWidth: 200 }).side).toBe('right')
    // source x=100, target x=400 → midpoint=250 > center=200 → left
    const left = chooseSide({ sourceX: 100, targetX: 400, boxX: 100, boxWidth: 200 })
    expect(left.side).toBe('left')
    expect(left.gutterX).toBe(100 - GUTTER_OFFSET)
    // source x=0, target x=200 → midpoint=100 < center=200 → right
    const right = chooseSide({ sourceX: 0, targetX: 200, boxX: 100, boxWidth: 200 })
    expect(right.side).toBe('right')
    expect(right.gutterX).toBe(100 + 200 + GUTTER_OFFSET)
  })
})

describe('loopHandleIds', () => {
  it('maps the right side to the right source/target loop handles', () => {
    expect(loopHandleIds('right')).toEqual({
      sourceHandle: 'loop-s-right',
      targetHandle: 'loop-t-right',
    })
  })

  it('maps the left side to the left source/target loop handles', () => {
    expect(loopHandleIds('left')).toEqual({
      sourceHandle: 'loop-s-left',
      targetHandle: 'loop-t-left',
    })
  })
})

describe('buildLoopBackPath', () => {
  it('starts at the source side-point and bows out to the chosen gutter', () => {
    const { path } = buildLoopBackPath({
      sourceX: 300,
      sourceY: 250,
      targetX: 300,
      targetY: 150,
      side: 'right',
      gutterX: 360,
    })
    // The curve begins at the tail's side attachment point...
    expect(path.startsWith('M300,250')).toBe(true)
    // ...and bows out through the right-side gutter.
    expect(path).toContain('360')
  })

  it('routes through a different gutter when the side flips', () => {
    const right = buildLoopBackPath({
      sourceX: 300, sourceY: 250, targetX: 300, targetY: 150, side: 'right', gutterX: 360,
    }).path
    const left = buildLoopBackPath({
      sourceX: 300, sourceY: 250, targetX: 300, targetY: 150, side: 'left', gutterX: 40,
    }).path
    expect(right).not.toEqual(left)
    expect(left).toContain('40')
  })
})
