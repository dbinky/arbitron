import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFile, mkdtemp, mkdir, copyFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { buildCoverage, verifyCoverage } from './build-font-coverage.mjs'

const root = path.resolve(import.meta.dirname, '..')

test('coverage comes from the exact CSS imports, cmap and shaping, deterministically', async () => {
  const coverage = await buildCoverage(root)
  assert.match(coverage.fingerprint, /^[a-f0-9]{64}$/)
  assert.equal(coverage.packages['@fontsource/noto-sans-sc'], '5.3.1')
  assert.equal(coverage.packages['@fontsource/noto-emoji'], '5.3.2')
  assert.ok(coverage.faces.length > 400)
  for (const family of ['Inter', 'Noto Sans SC', 'Noto Emoji']) {
    for (const weight of [400, 500, 600, 700]) {
      assert.ok(coverage.faces.some(f => f.family === family && f.weight === weight))
    }
  }
  const covers = (family, cp) => coverage.faces.some(f => f.family === family && f.ranges.some(([a, b]) => a <= cp && cp <= b))
  assert.ok(covers('Inter', 'é'.codePointAt(0)))
  assert.ok(covers('Noto Sans SC', '測'.codePointAt(0)))
  assert.ok(covers('Noto Emoji', '😀'.codePointAt(0)))
  assert.equal(covers('Noto Sans SC', 0x10ffff), false)
  const sequence = coverage.faces.find(f => f.family === 'Noto Emoji' && f.weight === 600 && f.sequences.some(s => s.text === '👩‍💻'))
  assert.ok(sequence, 'ZWJ coverage must come from actual ligature shaping')
  assert.ok(sequence.sequences.find(s => s.text === '👩‍💻').glyphs.every(id => id > 0))
  assert.ok(!coverage.faces.some(f => f.sequences.some(s => s.text === 'A‍😀')))
  await verifyCoverage(root, coverage)
  assert.deepEqual(await buildCoverage(root), coverage)
  assert.deepEqual(JSON.parse(await readFile(path.join(root, 'src/graph/arranged/fontCoverage.json'), 'utf8')), coverage)
})

test('production build verifies committed coverage and redistributes exact font licenses', async () => {
  const pkg = JSON.parse(await readFile(path.join(root, 'package.json'), 'utf8'))
  assert.ok(pkg.scripts.build.startsWith('node scripts/build-font-coverage.mjs --check &&'))
  for (const name of ['inter', 'noto-sans-sc', 'noto-emoji']) {
    assert.equal(await readFile(path.join(root, 'public/licenses', `fontsource-${name}.txt`), 'utf8'), await readFile(path.join(root, 'node_modules/@fontsource', name, 'LICENSE'), 'utf8'))
  }
})

test('asset verification rejects missing subsets, byte drift and metadata drift', async () => {
  const coverage = JSON.parse(await readFile(path.join(root, 'src/graph/arranged/fontCoverage.json'), 'utf8'))
  const tmp = await mkdtemp(path.join(tmpdir(), 'arbitron-fonts-'))
  try {
    // Mirror exactly the selected imported face paths, not an ignored fake loader.
    for (const face of coverage.faces) {
      for (const asset of face.assets) {
        const dest = path.join(tmp, 'node_modules', asset.file)
        await mkdir(path.dirname(dest), { recursive: true })
        await copyFile(path.join(root, 'node_modules', asset.file), dest)
      }
    }
    for (const [pkg] of Object.entries(coverage.packages)) {
      await copyFile(path.join(root, 'node_modules', pkg, 'package.json'), path.join(tmp, 'node_modules', pkg, 'package.json'))
    }
    for (const css of coverage.stylesheets) {
      await copyFile(path.join(root, 'node_modules', css.file), path.join(tmp, 'node_modules', css.file))
    }
    await mkdir(path.join(tmp, 'src/graph/arranged'), { recursive: true })
    await copyFile(path.join(root, 'src/graph/arranged/fonts.ts'), path.join(tmp, 'src/graph/arranged/fonts.ts'))
    await verifyCoverage(tmp, coverage)
    const face = coverage.faces.find(f => f.family === 'Noto Sans SC')
    const file = path.join(tmp, 'node_modules', face.file)
    const bytes = await readFile(file)
    await rm(file)
    await assert.rejects(verifyCoverage(tmp, coverage), /ENOENT|missing/i)
    await writeFile(file, Buffer.concat([bytes, Buffer.from('changed')]))
    await assert.rejects(verifyCoverage(tmp, coverage), /hash|fingerprint/i)
    await writeFile(file, bytes)
    const forged = structuredClone(coverage)
    forged.faces[0].weight = 900
    await assert.rejects(verifyCoverage(tmp, forged), /metadata|fingerprint/i)
  } finally {
    await rm(tmp, { recursive: true, force: true })
  }
})
