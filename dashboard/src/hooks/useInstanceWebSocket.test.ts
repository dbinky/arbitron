import { renderHook } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useInstanceWebSocket } from './useInstanceWebSocket'

const opened: string[] = []

class RecordingWebSocket {
  static readonly OPEN = 1
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((m: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  constructor(url: string) {
    opened.push(url)
  }
  addEventListener() {}
  send() {}
  close() {}
}

beforeEach(() => {
  opened.length = 0
  vi.stubGlobal('WebSocket', RecordingWebSocket)
})
afterEach(() => vi.unstubAllGlobals())

describe('useInstanceWebSocket', () => {
  it('connects to the plain per-instance stream by default', () => {
    renderHook(() => useInstanceWebSocket('i-1', () => {}))
    expect(opened).toEqual([`ws://${window.location.host}/api/v1/instances/i-1/ws`])
  })

  it('connects to the subtree stream when subtree is set', () => {
    renderHook(() => useInstanceWebSocket('i-1', () => {}, { subtree: true }))
    expect(opened).toEqual([`ws://${window.location.host}/api/v1/instances/i-1/ws?subtree=1`])
  })

  it('opens nothing without an instance id', () => {
    renderHook(() => useInstanceWebSocket(undefined, () => {}, { subtree: true }))
    expect(opened).toEqual([])
  })
})
