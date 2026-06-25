import { renderHook, act } from '@testing-library/react'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { useDensityPreference } from './useDensityPreference'

describe('useDensityPreference', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('defaults to balanced on first use', () => {
    const { result } = renderHook(() => useDensityPreference('u1'))
    expect(result.current.density).toBe('balanced')
  })

  it('cycles balanced -> dense -> expanded -> balanced', () => {
    const { result } = renderHook(() => useDensityPreference('u1'))
    act(() => result.current.cycle())
    expect(result.current.density).toBe('dense')
    act(() => result.current.cycle())
    expect(result.current.density).toBe('expanded')
    act(() => result.current.cycle())
    expect(result.current.density).toBe('balanced')
  })

  it('persists to localStorage under a per-user key', () => {
    const { result } = renderHook(() => useDensityPreference('u1'))
    act(() => result.current.cycle())
    expect(localStorage.getItem('arbitron.queueDensity.u1')).toBe('dense')
  })

  it('reloads the persisted value on next mount', () => {
    localStorage.setItem('arbitron.queueDensity.u1', 'expanded')
    const { result } = renderHook(() => useDensityPreference('u1'))
    expect(result.current.density).toBe('expanded')
  })

  it('uses a global fallback key when userId is absent', () => {
    const { result } = renderHook(() => useDensityPreference(null))
    act(() => result.current.cycle())
    expect(localStorage.getItem('arbitron.queueDensity')).toBe('dense')
  })

  it('falls back to balanced when stored value is corrupt', () => {
    localStorage.setItem('arbitron.queueDensity.u1', 'garbage')
    const { result } = renderHook(() => useDensityPreference('u1'))
    expect(result.current.density).toBe('balanced')
  })

  it('falls back to balanced when localStorage throws on read', () => {
    const spy = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('storage disabled')
    })
    const { result } = renderHook(() => useDensityPreference('u1'))
    expect(result.current.density).toBe('balanced')
    spy.mockRestore()
  })

  it('does not throw when localStorage throws on write', () => {
    const spy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('storage full')
    })
    const { result } = renderHook(() => useDensityPreference('u1'))
    expect(() => act(() => result.current.cycle())).not.toThrow()
    spy.mockRestore()
  })
})