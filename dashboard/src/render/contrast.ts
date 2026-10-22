// WCAG 2.x contrast for the render surface. Pure colour maths plus one DOM
// helper that resolves the colour actually behind a text element.

export interface RGBA {
  r: number // 0..255
  g: number
  b: number
  a: number // 0..1
}

const OPAQUE_WHITE: RGBA = { r: 255, g: 255, b: 255, a: 1 }

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(hi, Math.max(lo, v))
}

function channel(token: string): number {
  const t = token.trim()
  return t.endsWith('%') ? clamp((parseFloat(t) / 100) * 255, 0, 255) : clamp(parseFloat(t), 0, 255)
}

function alpha(token: string | undefined): number {
  if (token === undefined) return 1
  const t = token.trim()
  return t.endsWith('%') ? clamp(parseFloat(t) / 100, 0, 1) : clamp(parseFloat(t), 0, 1)
}

/** Parses #rgb, #rgba, #rrggbb, #rrggbbaa, rgb()/rgba() (comma or space syntax) and 'transparent'. */
export function parseColor(css: string): RGBA | null {
  const s = css.trim().toLowerCase()
  if (s === 'transparent') return { r: 0, g: 0, b: 0, a: 0 }
  const hex = /^#([0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})$/.exec(s)
  if (hex) {
    let h = hex[1]
    if (h.length <= 4) h = h.split('').map((c) => c + c).join('')
    const n = (i: number) => parseInt(h.slice(i, i + 2), 16)
    return { r: n(0), g: n(2), b: n(4), a: h.length === 8 ? n(6) / 255 : 1 }
  }
  const fn = /^rgba?\((.*)\)$/.exec(s)
  if (!fn) return null
  const slices = fn[1].split('/')
  if (slices.length > 2) return null
  const [rgbPart, slashAlpha] = slices
  const comma = rgbPart.includes(',')
  const parts = comma ? rgbPart.split(',') : rgbPart.trim().split(/\s+/)
  if (parts.length < 3 || parts.length > (comma && slashAlpha === undefined ? 4 : 3)) return null
  const number = /^[-+]?(?:\d*\.\d+|\d+\.?\d*)(?:e[-+]?\d+)?%?$/i
  if (![...parts, ...(slashAlpha === undefined ? [] : [slashAlpha])].every(t => number.test(t.trim()) && Number.isFinite(parseFloat(t)))) return null
  const color = { r: channel(parts[0]), g: channel(parts[1]), b: channel(parts[2]), a: alpha(slashAlpha ?? parts[3]) }
  return [color.r, color.g, color.b, color.a].some(Number.isNaN) ? null : color
}

/** Source-over compositing of top onto bottom. */
export function composite(top: RGBA, bottom: RGBA): RGBA {
  const a = top.a + bottom.a * (1 - top.a)
  if (a === 0) return { r: 0, g: 0, b: 0, a: 0 }
  const mix = (t: number, b: number) => (t * top.a + b * bottom.a * (1 - top.a)) / a
  return { r: mix(top.r, bottom.r), g: mix(top.g, bottom.g), b: mix(top.b, bottom.b), a }
}

/** WCAG relative luminance of an opaque colour. */
export function relativeLuminance(c: RGBA): number {
  const lin = (v: number) => {
    const s = v / 255
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * lin(c.r) + 0.7152 * lin(c.g) + 0.0722 * lin(c.b)
}

/** WCAG contrast ratio (1..21); a translucent foreground is composited over bg first. */
export function contrastRatio(fg: RGBA, bg: RGBA): number {
  const opaqueBg = bg.a < 1 ? composite(bg, OPAQUE_WHITE) : bg
  const opaqueFg = fg.a < 1 ? composite(fg, opaqueBg) : fg
  const l1 = relativeLuminance(opaqueFg)
  const l2 = relativeLuminance(opaqueBg)
  return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05)
}

/**
 * The colour painted behind el: walks el and its ancestors, compositing each
 * translucent background-color until an opaque one is reached; the browser's
 * default canvas (white) terminates the walk.
 */
export function effectiveBackground(el: Element, win: Window): RGBA {
  const layers: RGBA[] = []
  for (let node: Element | null = el; node; node = node.parentElement) {
    const bg = parseColor(win.getComputedStyle(node).backgroundColor)
    if (!bg || bg.a === 0) continue
    layers.push(bg)
    if (bg.a >= 1) break
  }
  let out = OPAQUE_WHITE
  for (let i = layers.length - 1; i >= 0; i--) out = composite(layers[i], out)
  return out
}

/** Contrast of el's computed text colour against its effective background. */
export function textContrast(el: Element, win: Window): number {
  const fg = parseColor(win.getComputedStyle(el).color) ?? { r: 0, g: 0, b: 0, a: 1 }
  return contrastRatio(fg, effectiveBackground(el, win))
}
