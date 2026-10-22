// The renderer contract's font (contract.json names the complete Inter/Noto package set below).
// Bundled locally — never fetched from a CDN — so the dashboard, the render
// surface and the Go browser renderer all draw identical glyphs.
import '@fontsource/inter/400.css'
import '@fontsource/inter/500.css'
import '@fontsource/inter/600.css'
import '@fontsource/inter/700.css'
import '@fontsource/noto-sans-sc/400.css'
import '@fontsource/noto-sans-sc/500.css'
import '@fontsource/noto-sans-sc/600.css'
import '@fontsource/noto-sans-sc/700.css'
import '@fontsource/noto-emoji/400.css'
import '@fontsource/noto-emoji/500.css'
import '@fontsource/noto-emoji/600.css'
import '@fontsource/noto-emoji/700.css'
import type { RendererContract } from './types'
import contractJSON from './contract.json'
import coverage from './fontCoverage.json'

export const RENDERER_FONT_FAMILIES: readonly string[] = ['Inter', 'Noto Sans SC', 'Noto Emoji']

// Asset integrity is a build-time gate: `npm run build` runs
// build-font-coverage.mjs --check, which hashes every imported WOFF2 against
// fontCoverage.json before Vite fingerprints and embeds them. The runtime gate
// below proves the browser actually loaded each required subset; it needs no
// Web Crypto, so it works on plain-HTTP LAN origins (isSecureContext=false).
type Face = (typeof coverage.faces)[number]
const special = /[\u200d\ufe00-\ufe0f\u{e0100}-\u{e01ef}\u{e0020}-\u{e007f}\u{1f1e6}-\u{1f1ff}\u{1f3fb}-\u{1f3ff}\u20e3]/u
const unsupportedControl = /[\p{Cc}\p{Cf}\p{Cs}]/u
const contains = (face: Face, cp: number) => face.ranges.some(([a, b]) => a <= cp && cp <= b)
const rangeKey = (range: string) => range.split(',').map(part => {
  const [a, b = a] = part.trim().replace(/^U\+/i, '').split('-').map(v => parseInt(v, 16))
  return `${a}-${b}`
}).join(',')
const familyKey = (family: string) => family.replace(/["']/g, '')

// Intl.Segmenter is available in the supported browsers; keep the project's
// ES2020 compilation target without introducing a competing grapheme algorithm.
function graphemes(text: string): string[] {
  const { Segmenter } = Intl as typeof Intl & {
    Segmenter: new (locale: string, options: { granularity: 'grapheme' }) => {
      segment(text: string): Iterable<{ segment: string }>
    }
  }
  return [...new Segmenter('en', { granularity: 'grapheme' }).segment(text)].map(s => s.segment)
}

function requiredFaces(texts: readonly string[], weights: number[]): Map<Face, Set<string>> {
  const needed = new Map<Face, Set<string>>()
  const use = (face: Face, text: string) => {
    if (!needed.has(face)) needed.set(face, new Set())
    needed.get(face)!.add(text)
  }
  // Captions are uppercase in the shared CSS. Load their transformed glyphs too;
  // this port intentionally accepts text, not a second typography/style schema.
  for (const text of new Set(texts.flatMap(t => [t, t.toUpperCase()]))) {
    for (const cluster of graphemes(text)) {
      if (/^[\r\n\t]+$/.test(cluster)) continue
      const chars = [...cluster]
      const cp = chars[0].codePointAt(0)!
      const family = RENDERER_FONT_FAMILIES.find(f => coverage.faces.some(face => face.family === f && contains(face, cp)))
      if (!family) throw new Error(`unsupported bundled-font scalar U+${cp.toString(16).toUpperCase()}`)
      for (const weight of weights) {
        const faces = coverage.faces.filter(face => face.family === family && face.weight === weight).reverse()
        if (special.test(cluster) && chars.length > 1) {
          const face = faces.find(face => contains(face, cp))
          if (!face?.sequences.some(s => s.text === cluster)) throw new Error(`unsupported bundled-font sequence ${JSON.stringify(cluster)}`)
          use(face, cluster)
        } else {
          for (const ch of chars) {
            if (unsupportedControl.test(ch) || special.test(ch)) throw new Error(`unsupported bundled-font control ${JSON.stringify(cluster)}`)
            const face = faces.find(face => contains(face, ch.codePointAt(0)!))
            if (!face) throw new Error(`unsupported bundled-font scalar ${JSON.stringify(ch)}`)
            use(face, ch)
          }
        }
      }
    }
  }
  return needed
}

/** Fail closed on unsupported text, missing/undecodable subsets or a drifted contract. */
export async function ensureBundledTextFonts(doc: Document, texts: readonly string[], contract: RendererContract): Promise<{ fontsReady: boolean; fontFamilyResolved: boolean }> {
  if (!contract || Object.keys(contract).length !== Object.keys(contractJSON).length ||
      Object.entries(contractJSON).some(([key, value]) => contract[key as keyof RendererContract] !== value)) {
    throw new Error('font renderer contract does not match bundled typography')
  }
  const needed = requiredFaces(texts, [...new Set([contract.nodeLabelWeight, contract.nodeTypeWeight, contract.edgeLabelWeight])])
  if (!doc.fonts) throw new Error('FontFaceSet is unavailable')
  for (const [face, text] of needed) {
    let loaded: FontFace[]
    try {
      loaded = await doc.fonts.load(`${face.weight} ${contract.nodeLabelFontPx}px "${face.family}"`, [...text].join(''))
    } catch (error) {
      throw new Error(`font asset unavailable: ${face.file} (${error instanceof Error ? error.message : String(error)})`)
    }
    const matches = loaded.filter(f => familyKey(f.family) === face.family && Number(f.weight) === face.weight &&
        f.style === face.style && rangeKey(f.unicodeRange) === rangeKey(face.unicodeRange))
    // A 404 or an undecodable (corrupt) WOFF2 leaves the browser's face in 'error'.
    if (matches.some(f => f.status === 'loaded')) continue
    if (matches.some(f => f.status === 'error')) throw new Error(`font asset unavailable: ${face.file}`)
    throw new Error(`font subset not loaded: ${face.file}`)
  }
  await doc.fonts.ready
  return { fontsReady: true, fontFamilyResolved: true }
}
