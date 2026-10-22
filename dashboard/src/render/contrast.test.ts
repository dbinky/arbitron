import { describe, expect, it } from 'vitest'
import { composite, contrastRatio, effectiveBackground, parseColor, relativeLuminance, textContrast } from './contrast'

const hex = (s: string) => parseColor(s)!

describe('parseColor', () => {
  it('parses hex forms', () => {
    expect(parseColor('#fff')).toEqual({ r: 255, g: 255, b: 255, a: 1 })
    expect(parseColor('#1e1e38')).toEqual({ r: 0x1e, g: 0x1e, b: 0x38, a: 1 })
    expect(parseColor('#00000080')!.a).toBeCloseTo(128 / 255, 5)
  })
  it('parses computed-style rgb()/rgba() in comma and space syntax', () => {
    expect(parseColor('rgb(224, 224, 224)')).toEqual({ r: 224, g: 224, b: 224, a: 1 })
    expect(parseColor('rgba(30, 30, 56, 0.5)')).toEqual({ r: 30, g: 30, b: 56, a: 0.5 })
    expect(parseColor('rgb(30 30 56 / 50%)')).toEqual({ r: 30, g: 30, b: 56, a: 0.5 })
  })
  it('treats transparent as zero alpha and rejects garbage', () => {
    expect(parseColor('transparent')!.a).toBe(0)
    expect(parseColor('rgba(0, 0, 0, 0)')!.a).toBe(0)
    expect(parseColor('not-a-colour')).toBeNull()
    expect(parseColor('rgb(1, 2)')).toBeNull()
    for (const css of ['rgb(1garbage 2 3)', 'rgb(1 2 3 / 50% / 10%)', 'rgba(1,2,3,0.5 / 0.2)', 'rgb(1 2 3 0.5)', 'rgb(Infinity 2 3)']) expect(parseColor(css)).toBeNull()
  })
})

describe('contrastRatio', () => {
  it('matches WCAG reference values', () => {
    expect(contrastRatio(hex('#000'), hex('#fff'))).toBeCloseTo(21, 5)
    expect(contrastRatio(hex('#777'), hex('#777'))).toBeCloseTo(1, 5)
    expect(relativeLuminance(hex('#fff'))).toBeCloseTo(1, 5)
  })
  it('passes the arranged renderer label colours on the node surface', () => {
    // label #e0e0e0 and type caption #b4b4cc on --bg-surface #1e1e38 (contracts addendum 7)
    expect(contrastRatio(hex('#e0e0e0'), hex('#1e1e38'))).toBeGreaterThanOrEqual(4.5)
    expect(contrastRatio(hex('#b4b4cc'), hex('#1e1e38'))).toBeGreaterThanOrEqual(4.5)
    // loop-return edge label #9aa6ff on the label background #1a1a2e
    expect(contrastRatio(hex('#9aa6ff'), hex('#1a1a2e'))).toBeGreaterThanOrEqual(4.5)
  })
  it('fails an accent colour used as text', () => {
    expect(contrastRatio(hex('#9b59b6'), hex('#1e1e38'))).toBeLessThan(4.5)
  })
  it('composites a translucent foreground before measuring', () => {
    const half = { r: 224, g: 224, b: 224, a: 0.3 }
    expect(contrastRatio(half, hex('#1e1e38'))).toBeLessThan(contrastRatio(hex('#e0e0e0'), hex('#1e1e38')))
  })
  it('is symmetric', () => {
    expect(contrastRatio(hex('#1e1e38'), hex('#e0e0e0'))).toBeCloseTo(contrastRatio(hex('#e0e0e0'), hex('#1e1e38')), 10)
  })
})

describe('composite', () => {
  it('blends source-over', () => {
    const c = composite({ r: 255, g: 0, b: 0, a: 0.5 }, { r: 0, g: 0, b: 255, a: 1 })
    expect(c).toEqual({ r: 127.5, g: 0, b: 127.5, a: 1 })
  })
})

describe('effectiveBackground / textContrast (DOM)', () => {
  it('uses the first opaque ancestor background and skips transparent layers', () => {
    document.body.innerHTML = '<div id="node" style="background-color: #1e1e38"><span id="wrap"><span id="t" style="color: #e0e0e0">Start</span></span></div>'
    const t = document.getElementById('t')!
    expect(effectiveBackground(t, window)).toEqual({ r: 30, g: 30, b: 56, a: 1 })
    expect(textContrast(t, window)).toBeCloseTo(contrastRatio(hex('#e0e0e0'), hex('#1e1e38')), 5)
  })
  it('composites a translucent overlay over the opaque background below it', () => {
    document.body.innerHTML = '<div style="background-color: rgb(0, 0, 0)"><div id="o" style="background-color: rgba(255, 255, 255, 0.5)"><span id="t" style="color: rgb(0, 0, 0)">x</span></div></div>'
    const bg = effectiveBackground(document.getElementById('t')!, window)
    expect(bg.r).toBeCloseTo(127.5, 5)
    expect(bg.a).toBe(1)
  })
  it('falls back to the white canvas when nothing paints a background', () => {
    document.body.innerHTML = '<span id="t" style="color: #e0e0e0">x</span>'
    expect(effectiveBackground(document.getElementById('t')!, window)).toEqual({ r: 255, g: 255, b: 255, a: 1 })
    expect(textContrast(document.getElementById('t')!, window)).toBeLessThan(4.5)
  })
})
