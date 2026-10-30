import { describe, it, expect } from 'vitest'
import { buildRoutedPath } from './routedPath'

describe('buildRoutedPath', () => {
  it('draws an orthogonal polyline through every point when there are no gaps', () => {
    expect(
      buildRoutedPath([{ x: 10, y: 20 }, { x: 10, y: 80 }, { x: 50, y: 80 }], [], 10),
    ).toBe('M 10,20 L 10,80 L 50,80')
  })

  it('splits a vertical segment around one gap with a new M subpath', () => {
    expect(buildRoutedPath([{ x: 10, y: 0 }, { x: 10, y: 100 }], [{ x: 10, y: 50 }], 10)).toBe(
      'M 10,0 L 10,45 M 10,55 L 10,100',
    )
  })

  it('splits a horizontal segment around a gap', () => {
    expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 100, y: 0 }], [{ x: 40, y: 0 }], 10)).toBe(
      'M 0,0 L 35,0 M 45,0 L 100,0',
    )
  })

  it('opens two gaps on one segment in order along the segment regardless of input order', () => {
    expect(
      buildRoutedPath([{ x: 10, y: 0 }, { x: 10, y: 100 }], [{ x: 10, y: 70 }, { x: 10, y: 30 }], 10),
    ).toBe('M 10,0 L 10,25 M 10,35 L 10,65 M 10,75 L 10,100')
  })

  it('clamps a gap near the end of a segment to the segment end', () => {
    expect(
      buildRoutedPath([{ x: 0, y: 0 }, { x: 0, y: 100 }, { x: 50, y: 100 }], [{ x: 0, y: 97 }], 10),
    ).toBe('M 0,0 L 0,92 M 0,100 L 50,100')
  })

  it('consumes a gap at a shared corner only once', () => {
    expect(
      buildRoutedPath([{ x: 0, y: 0 }, { x: 0, y: 100 }, { x: 100, y: 100 }], [{ x: 0, y: 100 }], 10),
    ).toBe('M 0,0 L 0,95 M 0,100 L 100,100')
  })

  it('ignores a gap point that is not on any segment', () => {
    expect(
      buildRoutedPath([{ x: 10, y: 20 }, { x: 10, y: 80 }, { x: 50, y: 80 }], [{ x: 30, y: 50 }], 10),
    ).toBe('M 10,20 L 10,80 L 50,80')
  })

  it('rounds coordinates to two decimals', () => {
    expect(buildRoutedPath([{ x: 0.123, y: 0.456 }, { x: 0.123, y: 10.006 }], [], 10)).toBe(
      'M 0.12,0.46 L 0.12,10.01',
    )
  })

  it('orders gaps along reversed segments without mutating persisted points or crossings', () => {
    const points = [{ x: 100, y: 10 }, { x: 0, y: 10 }, { x: 0, y: 10 }, { x: 0, y: -100 }]
    const gaps = [{ x: 30, y: 10 }, { x: 70, y: 10 }, { x: 0, y: -40 }]
    const original = structuredClone({ points, gaps })
    expect(buildRoutedPath(points, gaps, 10)).toBe('M 100,10 L 75,10 M 65,10 L 35,10 M 25,10 L 0,10 L 0,-35 M 0,-45 L 0,-100')
    expect({ points, gaps }).toEqual(original)
  })

  it('does not draw backward through overlapping gaps', () => {
    expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 100, y: 0 }], [{ x: 40, y: 0 }, { x: 45, y: 0 }], 10))
      .toBe('M 0,0 L 35,0 M 45,0 M 50,0 L 100,0')
  })

  describe('dashed restarts', () => {
    const dash = (overs: { x: number; y: number }[] = []) => ({ onPx: 6, overs })

    it('restarts the dash so an over crossing is inked across the whole gap span', () => {
      expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 100, y: 0 }], [], 10, dash([{ x: 40, y: 0 }])))
        .toBe('M 0,0 L 34,0 M 34,0 L 40,0 M 40,0 L 100,0')
    })

    it('restarts the dash one dash before an under gap so both banks are inked', () => {
      expect(buildRoutedPath([{ x: 10, y: 0 }, { x: 10, y: 100 }], [{ x: 10, y: 50 }], 10, dash()))
        .toBe('M 10,0 L 10,39 M 10,39 L 10,45 M 10,55 L 10,100')
    })

    it('clamps a bank restart to the bend that starts the segment', () => {
      expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 0, y: 100 }, { x: 100, y: 100 }], [{ x: 8, y: 100 }], 10, dash()))
        .toBe('M 0,0 L 0,100 M 0,100 L 3,100 M 13,100 L 100,100')
    })

    it('skips a restart already covered by the previous gap\'s fresh subpath', () => {
      expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 100, y: 0 }], [{ x: 40, y: 0 }, { x: 52, y: 0 }], 10, dash()))
        .toBe('M 0,0 L 29,0 M 29,0 L 35,0 M 45,0 L 47,0 M 57,0 L 100,0')
    })

    it('never inks inside a gap when the next gap\'s bank restart falls within it', () => {
      // Gaps [35,45] and [48,58]; the second bank restart (37) lies inside the first gap.
      expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 100, y: 0 }], [{ x: 40, y: 0 }, { x: 53, y: 0 }], 10, dash()))
        .toBe('M 0,0 L 29,0 M 29,0 L 35,0 M 45,0 L 48,0 M 58,0 L 100,0')
    })

    it('does not restart at the path start or for a zero dash', () => {
      expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 0, y: 100 }], [{ x: 0, y: 8 }], 10, dash()))
        .toBe('M 0,0 L 0,3 M 0,13 L 0,100')
      expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 100, y: 0 }], [], 10, { onPx: 0, overs: [{ x: 40, y: 0 }] }))
        .toBe('M 0,0 L 100,0')
    })
  })

  describe('rounded corners', () => {
    const corner = (overs: { x: number; y: number }[] = []) => ({ radiusPx: 6, overs, arrowPx: 10 })

    it('draws each interior bend as a quarter arc of the corner radius', () => {
      // Right then down is a clockwise turn (sweep 1); down then right is anticlockwise (sweep 0).
      expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 0, y: 50 }, { x: 40, y: 50 }, { x: 40, y: 100 }], [], 10, undefined, corner()))
        .toBe('M 0,0 L 0,44 A 6,6 0 0 0 6,50 L 34,50 A 6,6 0 0 1 40,56 L 40,100')
    })

    it('leaves a route without interior bends unchanged', () => {
      expect(buildRoutedPath([{ x: 10, y: 0 }, { x: 10, y: 100 }], [], 10, undefined, corner())).toBe('M 10,0 L 10,100')
    })

    it('clamps the radius to half of the shorter adjacent segment', () => {
      // A 6px jog leaves room for 3px corners at both of its ends.
      expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 0, y: 50 }, { x: 6, y: 50 }, { x: 6, y: 100 }], [], 10, undefined, corner()))
        .toBe('M 0,0 L 0,47 A 3,3 0 0 0 3,50 A 3,3 0 0 1 6,53 L 6,100')
    })

    it('shrinks a corner so it never reaches into a nearby crossing gap', () => {
      // The closest crossing preflight allows (7px): its gap spans 2..12 below the
      // bend, and its upper bank (1..2) must stay straight for the gap to read.
      expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 40, y: 0 }, { x: 40, y: 100 }], [{ x: 40, y: 7 }], 10, undefined, corner()))
        .toBe('M 0,0 L 39,0 A 1,1 0 0 1 40,1 L 40,2 M 40,12 L 40,100')
      // Any closer and the corner would be under a pixel: the bend stays sharp.
      expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 40, y: 0 }, { x: 40, y: 100 }], [{ x: 40, y: 6.5 }], 10, undefined, corner()))
        .toBe('M 0,0 L 40,0 L 40,1.5 M 40,11.5 L 40,100')
    })

    it('keeps an over crossing on straight ink, clear of the corner', () => {
      expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 40, y: 0 }, { x: 40, y: 100 }], [], 10, undefined, corner([{ x: 31, y: 0 }])))
        .toBe('M 0,0 L 37,0 A 3,3 0 0 1 40,3 L 40,100')
    })

    it('draws a sharp bend when a gap sits on the bend itself', () => {
      expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 0, y: 100 }, { x: 100, y: 100 }], [{ x: 0, y: 100 }], 10, undefined, corner()))
        .toBe('M 0,0 L 0,95 M 0,100 L 100,100')
    })

    it('keeps the arrowhead on straight ink: the last corner leaves arrowPx of the final segment', () => {
      // A self-loop's 14px return leaves 4px for its corner.
      expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 30, y: 0 }, { x: 30, y: 30 }, { x: 16, y: 30 }], [], 10, undefined, corner()))
        .toBe('M 0,0 L 24,0 A 6,6 0 0 1 30,6 L 30,26 A 4,4 0 0 1 26,30 L 16,30')
      // Shorter than the arrow plus a pixel: the last bend stays sharp.
      expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 30, y: 0 }, { x: 30, y: 30 }, { x: 20.5, y: 30 }], [], 10, undefined, corner()))
        .toBe('M 0,0 L 24,0 A 6,6 0 0 1 30,6 L 30,30 L 20.5,30')
    })

    it('counts a crossing within half a pixel of a segment as on it', () => {
      expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 40, y: 0 }, { x: 40, y: 100 }], [], 10, undefined, corner([{ x: 31, y: 0.4 }])))
        .toBe('M 0,0 L 36.99,0 A 3.01,3.01 0 0 1 40,3.01 L 40,100')
    })

    it('draws a sharp bend for a direction reversal', () => {
      expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 0, y: 20 }, { x: 0, y: 10 }], [], 10, undefined, corner())).toBe('M 0,0 L 0,20 L 0,10')
    })

    it('restarts a dashed loop return after its corner and keeps both banks inked', () => {
      const d = buildRoutedPath(
        [{ x: 100, y: 0 }, { x: 140, y: 0 }, { x: 140, y: 100 }, { x: 100, y: 100 }],
        [{ x: 140, y: 50 }],
        10,
        { onPx: 6, overs: [{ x: 120, y: 100 }] },
        corner([{ x: 120, y: 100 }]),
      )
      expect(d).toBe('M 100,0 L 134,0 A 6,6 0 0 1 140,6 L 140,39 M 140,39 L 140,45 M 140,55 L 140,94 A 6,6 0 0 1 134,100 L 126,100 M 126,100 L 120,100 M 120,100 L 100,100')
    })
  })

  it('returns an empty string for a route with fewer than two points', () => {
    expect(buildRoutedPath([{ x: 1, y: 1 }], [], 10)).toBe('')
    expect(buildRoutedPath([], [], 10)).toBe('')
  })
})
