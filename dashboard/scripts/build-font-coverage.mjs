import { createHash } from 'node:crypto'
import { readFile, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { create } from 'fontkit'

const digest = bytes => createHash('sha256').update(bytes).digest('hex')
const manifestFile = 'src/graph/arranged/fontCoverage.json'
const special = /[\u200d\ufe00-\ufe0f\u{e0100}-\u{e01ef}\u{e0020}-\u{e007f}\u{1f1e6}-\u{1f1ff}\u{1f3fb}-\u{1f3ff}\u20e3]/u

function rangesOf(points) {
  const ranges = []
  for (const cp of [...new Set(points)].sort((a, b) => a - b)) {
    const last = ranges.at(-1)
    if (last && last[1] + 1 === cp) last[1] = cp
    else ranges.push([cp, cp])
  }
  return ranges
}

function cssRanges(value) {
  return value.split(',').map(part => {
    const [a, b = a] = part.trim().replace(/^U\+/i, '').split('-')
    return [parseInt(a, 16), parseInt(b, 16)]
  })
}

function coverageGlyphs(coverage) {
  if (coverage.version === 1) return coverage.glyphs
  return coverage.rangeRecords.flatMap(r => Array.from({ length: r.end - r.start + 1 }, (_, i) => r.start + i))
}

// Enumerate actual cmap variation records and GSUB ligatures, then retain only
// sequences which fontkit shapes to visible, non-.notdef glyphs. Scalar presence
// alone cannot prove a ZWJ/variation sequence.
function shapedSequences(font, allowed) {
  const encodings = new Map()
  const add = (id, text) => {
    if (!encodings.has(id)) encodings.set(id, new Set())
    encodings.get(id).add(text)
  }
  for (const cp of font.characterSet) {
    if (allowed(cp)) add(font.glyphForCodePoint(cp).id, String.fromCodePoint(cp))
  }
  const candidates = new Set()
  for (const table of font.cmap.tables) {
    if (table.table.version !== 14) continue
    for (const record of table.table.varSelectors.toArray()) {
      for (const range of record.defaultUVS ?? []) {
        for (let cp = range.startUnicodeValue; cp <= range.startUnicodeValue + range.additionalCount; cp++) {
          if (!allowed(cp)) continue
          const text = String.fromCodePoint(cp, record.varSelector)
          candidates.add(text)
          add(font.glyphForCodePoint(cp).id, text)
        }
      }
      for (const mapping of record.nonDefaultUVS ?? []) {
        if (!allowed(mapping.unicodeValue)) continue
        const text = String.fromCodePoint(mapping.unicodeValue, record.varSelector)
        candidates.add(text)
        add(mapping.glyphID, text)
      }
    }
  }
  const rules = []
  const collect = (type, sub) => {
    if (type === 7) return collect(sub.lookupType, sub.extension)
    if (type !== 4) return
    coverageGlyphs(sub.coverage).forEach((first, i) => {
      for (const lig of sub.ligatureSets.get(i)) rules.push({ output: lig.glyph, inputs: [first, ...lig.components] })
    })
  }
  for (const lookup of font.GSUB?.lookupList.toArray() ?? []) {
    for (const sub of lookup.subTables) collect(lookup.lookupType, sub)
  }
  const byOutput = new Map()
  for (const rule of rules) {
    if (!byOutput.has(rule.output)) byOutput.set(rule.output, [])
    byOutput.get(rule.output).push(rule)
  }
  function texts(id, visiting = new Set()) {
    if (visiting.has(id)) return []
    const next = new Set([...visiting, id])
    const result = new Set(encodings.get(id) ?? [])
    for (const rule of byOutput.get(id) ?? []) {
      let products = ['']
      for (const input of rule.inputs) products = products.flatMap(a => texts(input, next).map(b => a + b))
      for (const text of products) result.add(text)
    }
    return [...result]
  }
  for (const id of byOutput.keys()) {
    for (const text of texts(id)) if ([...text].length > 1 && special.test(text)) candidates.add(text)
  }
  return [...candidates].sort().flatMap(text => {
    const run = font.layout(text)
    const visible = run.glyphs.filter(g => Number.isFinite(g.bbox.minX) && g.bbox.maxX > g.bbox.minX)
    if (visible.length !== 1 || run.glyphs.some(g => g.id === 0)) return []
    return [{ text, glyphs: visible.map(g => g.id), advance: run.advanceWidth }]
  })
}

async function sourceMetadata(root) {
  const source = await readFile(path.join(root, 'src/graph/arranged/fonts.ts'), 'utf8')
  const imports = [...source.matchAll(/import\s+['"](@fontsource\/[^'"]+\.css)['"]/g)].map(m => m[1]).sort()
  const expected = ['inter', 'noto-sans-sc', 'noto-emoji'].flatMap(pkg =>
    [400, 500, 600, 700].map(weight => `@fontsource/${pkg}/${weight}.css`)).sort()
  if (JSON.stringify(imports) !== JSON.stringify(expected)) throw new Error('font coverage: expected the exact 12 contract CSS imports')
  const packages = {}
  const stylesheets = []
  const faces = []
  for (const file of imports) {
    const pkg = file.split('/').slice(0, 2).join('/')
    const meta = JSON.parse(await readFile(path.join(root, 'node_modules', pkg, 'package.json'), 'utf8'))
    packages[pkg] = meta.version
    const css = await readFile(path.join(root, 'node_modules', file), 'utf8')
    stylesheets.push({ file, sha256: digest(css) })
    for (const match of css.matchAll(/@font-face\s*\{([^}]+)\}/g)) {
      const block = match[1]
      const prop = name => block.match(new RegExp(`${name}:\\s*([^;]+)`))?.[1].trim()
      const assets = []
      for (const url of block.matchAll(/url\(([^)]+)\)/g)) {
        const asset = path.posix.join(path.posix.dirname(file), url[1].replace(/['"]/g, ''))
        assets.push({ file: asset, sha256: digest(await readFile(path.join(root, 'node_modules', asset))) })
      }
      const primary = assets.find(a => a.file.endsWith('.woff2'))
      if (!primary) throw new Error(`font coverage: no WOFF2 in ${file}`)
      faces.push({ family: prop('font-family').replace(/['"]/g, ''), weight: Number(prop('font-weight')), style: prop('font-style'), unicodeRange: prop('unicode-range'), file: primary.file, sha256: primary.sha256, assets })
    }
  }
  return { packages, stylesheets, faces }
}

export async function buildCoverage(root) {
  const metadata = await sourceMetadata(root)
  const faces = []
  for (const face of metadata.faces) {
    const font = create(await readFile(path.join(root, 'node_modules', face.file)))
    const css = cssRanges(face.unicodeRange)
    const allowed = cp => css.some(([a, b]) => a <= cp && cp <= b)
    faces.push({ ...face, ranges: rangesOf(font.characterSet.filter(allowed)), sequences: shapedSequences(font, allowed) })
  }
  const body = { ...metadata, faces }
  return { fingerprint: digest(JSON.stringify(body)), ...body }
}

export async function verifyCoverage(root, coverage) {
  const { fingerprint, ...body } = coverage
  if (digest(JSON.stringify(body)) !== fingerprint) throw new Error('font coverage fingerprint mismatch')
  const metadata = await sourceMetadata(root)
  const recorded = { packages: coverage.packages, stylesheets: coverage.stylesheets, faces: coverage.faces.map(({ ranges: _ranges, sequences: _sequences, ...face }) => face) }
  if (JSON.stringify(metadata) !== JSON.stringify(recorded)) throw new Error('font coverage asset hash/metadata mismatch; regenerate after reviewing font changes')
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const root = path.resolve(import.meta.dirname, '..')
  if (process.argv.includes('--check')) {
    await verifyCoverage(root, JSON.parse(await readFile(path.join(root, manifestFile), 'utf8')))
  } else {
    await writeFile(path.join(root, manifestFile), JSON.stringify(await buildCoverage(root)) + '\n')
  }
}
