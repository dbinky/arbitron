import { describe, it, expect } from 'vitest'
import {
  chooseSide,
  GUTTER_OFFSET,
  loopBackGeometry,
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

describe('loopBackGeometry', () => {
  // ReactFlow hands the edge the DEFAULT handle coords: source = the tail node's
  // bottom-center, target = the head node's top-center. Floating geometry derives
  // the SIDE attachment points (vertical center of the chosen side) plus the
  // channel the edge bows out to — no node handles required.
  const base = { sourceX: 200, sourceY: 400, targetX: 200, targetY: 100, nodeWidth: 200, nodeHeight: 80 }

  it('attaches at the LEFT side-center of both nodes and channels further left', () => {
    const g = loopBackGeometry({ ...base, side: 'left' })
    expect(g.tailX).toBe(100) // 200 - 200/2
    expect(g.tailY).toBe(360) // bottom 400 - 80/2
    expect(g.headX).toBe(100)
    expect(g.headY).toBe(140) // top 100 + 80/2
    expect(g.channelX).toBe(100 - GUTTER_OFFSET) // bows out past the side
    expect(g.channelX).toBeLessThan(g.tailX)
  })

  it('attaches at the RIGHT side-center of both nodes and channels further right', () => {
    const g = loopBackGeometry({ ...base, side: 'right' })
    expect(g.tailX).toBe(300) // 200 + 200/2
    expect(g.headX).toBe(300)
    expect(g.channelX).toBe(300 + GUTTER_OFFSET)
    expect(g.channelX).toBeGreaterThan(g.tailX)
  })
})

describe('buildLoopBackPath', () => {
  it('starts at the tail side-point, bows through the channel, ends at the head side-point', () => {
    const { path } = buildLoopBackPath({ tailX: 100, tailY: 360, headX: 100, headY: 140, channelX: 40 })
    expect(path.startsWith('M 100,360')).toBe(true)
    expect(path).toContain('40,360') // control reaching out to the channel
    expect(path).toContain('40,140')
    expect(path.trimEnd().endsWith('100,140')).toBe(true)
  })

  it('places the label out in the channel, between the two nodes', () => {
    const { labelX, labelY } = buildLoopBackPath({ tailX: 100, tailY: 360, headX: 100, headY: 140, channelX: 40 })
    expect(labelX).toBe(40)
    expect(labelY).toBe(250)
  })
})
