import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import coverage from './fontCoverage.json'
import { RENDERER_CONTRACT } from './validate'
import { ensureBundledTextFonts, RENDERER_FONT_FAMILIES } from './fonts'

const inRange = (cp: number, range: string) => range.split(',').some(part => {
  const [a, b = a] = part.trim().replace(/^U\+/i, '').split('-').map(v => parseInt(v, 16))
  return a <= cp && cp <= b
})

// The fake font set honours family, weight, unicode range AND requested text.
// It does not make a loaded Inter face stand in for a missing CJK/emoji subset.
function fontSet(omitFamily?: string, status: FontFaceLoadStatus = 'loaded') {
  return {
    ready: Promise.resolve(),
    load: vi.fn(async (spec: string, text: string) => {
      const family = spec.match(/"([^"]+)"/)?.[1]
      const weight = Number(spec.split(' ')[0])
      return coverage.faces.filter(f => f.family === family && f.weight === weight && family !== omitFamily && [...text].some(ch => inRange(ch.codePointAt(0)!, f.unicodeRange))).map(f => ({
        family: f.family, weight: String(f.weight), style: f.style, unicodeRange: f.unicodeRange, status,
      })) as FontFace[]
    }),
    check: vi.fn(() => true),
  }
}

let fonts: ReturnType<typeof fontSet>
beforeEach(() => {
  fonts = fontSet()
  Object.defineProperty(document, 'fonts', { configurable: true, value: fonts })
  // The runtime gate must not depend on Web Crypto or an extra asset fetch:
  // plain-HTTP LAN origins have no crypto.subtle, and asset bytes are verified
  // at build time by build-font-coverage.mjs --check.
  vi.stubGlobal('crypto', undefined)
  vi.stubGlobal('fetch', vi.fn(async () => { throw new Error('runtime font gate must not fetch') }))
})
afterEach(() => vi.unstubAllGlobals())

describe('ensureBundledTextFonts', () => {
  it('loads actual Latin, accent, combining, CJK and emoji faces at every used contract weight', async () => {
    expect(RENDERER_FONT_FAMILIES).toEqual(['Inter', 'Noto Sans SC', 'Noto Emoji'])
    await expect(ensureBundledTextFonts(document, ['Plan café e\u0301\n測試 😀 👩‍💻 ☠︎'], RENDERER_CONTRACT)).resolves.toEqual({ fontsReady: true, fontFamilyResolved: true })
    for (const family of RENDERER_FONT_FAMILIES) {
      for (const weight of [500, 600, 700]) {
        expect(fonts.load.mock.calls.some(([spec]) => spec.startsWith(`${weight} `) && spec.includes(`"${family}"`))).toBe(true)
      }
    }
    expect(fonts.load.mock.calls.some(([, text]) => text.includes('測'))).toBe(true)
    expect(fetch).not.toHaveBeenCalled()
  })

  it.each(['\u{10ffff}', '\ud800', 'A‍😀', 'A\ufe0f', '\u200d', '\u0000', 'A\u200bB'])('rejects unsupported scalar/sequence %j before loading', async text => {
    await expect(ensureBundledTextFonts(document, [text], RENDERER_CONTRACT)).rejects.toThrow(/unsupported/i)
    expect(fonts.load).not.toHaveBeenCalled()
  })

  it('accepts browser-canonical unicode ranges without leading zeroes', async () => {
    const actual = fonts.load.getMockImplementation()!
    fonts.load.mockImplementation(async (spec, text) => (await actual(spec, text)).map(face => ({
      ...face, unicodeRange: face.unicodeRange.replace(/(?:U\+|(?<=-))0+([0-9a-f]+)/gi, (s, n) => s.startsWith('U+') ? `U+${n}` : n),
    }) as FontFace))
    await expect(ensureBundledTextFonts(document, ['Plan café'], RENDERER_CONTRACT)).resolves.toEqual({ fontsReady: true, fontFamilyResolved: true })
  })

  it('does not accept Inter readiness for missing CJK or emoji faces', async () => {
    for (const family of ['Noto Sans SC', 'Noto Emoji']) {
      Object.defineProperty(document, 'fonts', { configurable: true, value: fontSet(family) })
      await expect(ensureBundledTextFonts(document, ['測試 😀'], RENDERER_CONTRACT)).rejects.toThrow(/font.*(missing|loaded)/i)
    }
  })

  it('rejects load failures, wrong ranges and absent FontFaceSet', async () => {
    fonts.load.mockRejectedValueOnce(new Error('font network failed'))
    await expect(ensureBundledTextFonts(document, ['Plan'], RENDERER_CONTRACT)).rejects.toThrow('font network failed')
    fonts.load.mockResolvedValue([])
    await expect(ensureBundledTextFonts(document, ['Plan'], RENDERER_CONTRACT)).rejects.toThrow(/font.*(missing|loaded)/i)
    Object.defineProperty(document, 'fonts', { configurable: true, value: undefined })
    await expect(ensureBundledTextFonts(document, ['Plan'], RENDERER_CONTRACT)).rejects.toThrow(/FontFaceSet/)
  })

  it('rejects missing and undecodable used WOFF2 assets without Web Crypto', async () => {
    // Chrome rejects FontFaceSet.load with a NetworkError when a required face 404s.
    fonts.load.mockRejectedValueOnce(new DOMException('A network error occurred.', 'NetworkError'))
    await expect(ensureBundledTextFonts(document, ['測'], RENDERER_CONTRACT)).rejects.toThrow(/font asset unavailable: @fontsource\/noto-sans-sc\/.*network error/i)
    // A face whose bytes fail to decode is reported in the 'error' state.
    Object.defineProperty(document, 'fonts', { configurable: true, value: fontSet(undefined, 'error') })
    await expect(ensureBundledTextFonts(document, ['測'], RENDERER_CONTRACT)).rejects.toThrow(/font asset unavailable: @fontsource\/noto-sans-sc\//)
    expect(fetch).not.toHaveBeenCalled()
  })

  it('does not accept a matching face that is still loading', async () => {
    Object.defineProperty(document, 'fonts', { configurable: true, value: fontSet(undefined, 'loading') })
    await expect(ensureBundledTextFonts(document, ['Plan'], RENDERER_CONTRACT)).rejects.toThrow(/font subset not loaded/)
  })

  it('waits for FontFaceSet.ready after all requested loads', async () => {
    let resolve!: () => void
    fonts.ready = new Promise<void>(r => { resolve = r })
    let done = false
    const pending = ensureBundledTextFonts(document, ['Plan'], RENDERER_CONTRACT).then(() => { done = true })
    await vi.waitFor(() => expect(fonts.load).toHaveBeenCalled())
    expect(done).toBe(false)
    resolve()
    await pending
    expect(done).toBe(true)
  })

  it('fails same-version contract drift before doing any font work', async () => {
    await expect(ensureBundledTextFonts(document, ['Plan'], { ...RENDERER_CONTRACT, nodeTypeFontPx: 11 })).rejects.toThrow(/contract/i)
    expect(fonts.load).not.toHaveBeenCalled()
  })
})
