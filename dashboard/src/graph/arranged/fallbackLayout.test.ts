import { describe, it, expect } from 'vitest'
import { fallbackPositions, FALLBACK_STACK_PITCH } from './fallbackLayout'
import { NODE_HEIGHT } from '../useGraphLayout'
import type { WorkflowNode } from '../types'

const node = (id: string, position?: { x: number; y: number }): WorkflowNode => ({
  id, type: 'script', label: id, description: '', config: {}, ...(position ? { position } : {}),
})

describe('fallbackPositions', () => {
  it('returns null without positions or nodes', () => {
    expect(fallbackPositions([node('a'), node('b')], undefined)).toBeNull()
    expect(fallbackPositions([node('a')], {})).toBeNull()
    expect(fallbackPositions([], { a: { x: 1, y: 2 } })).toBeNull()
  })
  it('prefers retained positions, then manifest positions, ignoring unknown ids', () => {
    expect(fallbackPositions([node('a', { x: 500, y: 500 }), node('b', { x: 70, y: 300 })], {
      a: { x: 1, y: 2 }, ghost: { x: 9, y: 9 },
    })).toEqual({ a: { x: 1, y: 2 }, b: { x: 70, y: 300 } })
    expect(fallbackPositions([node('a'), node('b')], {
      a: { x: 10, y: 20 }, b: { x: 10, y: 200 },
    })).toEqual({ a: { x: 10, y: 20 }, b: { x: 10, y: 200 } })
  })
  it('stacks missing nodes below placed boxes in manifest order at minimum x', () => {
    const nodes = [node('a'), node('new1'), node('b'), node('new2')]
    const retained = { a: { x: 40, y: 0 }, b: { x: 300, y: 400 } }
    const before = structuredClone({ nodes, retained })
    const out = fallbackPositions(nodes, retained)!
    expect(FALLBACK_STACK_PITCH).toBe(120)
    expect(out).toEqual({ ...retained, new1: { x: 40, y: 520 }, new2: { x: 40, y: 640 } })
    expect(out.new1.y).toBeGreaterThanOrEqual(400 + NODE_HEIGHT)
    expect(fallbackPositions(nodes, retained)).toEqual(out)
    expect({ nodes, retained }).toEqual(before)
    expect(out.a).not.toBe(retained.a)
  })
  it('preserves legal node ids that coincide with object prototype keys', () => {
    const retained = Object.fromEntries([['__proto__', { x: 30, y: 40 }]])
    expect(fallbackPositions([node('__proto__'), node('constructor')], retained)).toEqual(
      Object.fromEntries([['__proto__', { x: 30, y: 40 }], ['constructor', { x: 30, y: 160 }]]),
    )
  })
  it.each([NaN, Infinity, -Infinity])('treats nonfinite %s as missing, trying the manifest first', (bad) => {
    expect(fallbackPositions([node('a'), node('b')], { a: { x: 0, y: 0 }, b: { x: bad, y: 5 } }))
      .toEqual({ a: { x: 0, y: 0 }, b: { x: 0, y: 120 } })
    expect(fallbackPositions([node('a', { x: -10, y: -200 })], { a: { x: 0, y: bad } }))
      .toEqual({ a: { x: -10, y: -200 } })
    expect(fallbackPositions([node('a', { x: bad, y: 0 })], undefined)).toBeNull()
  })
})
