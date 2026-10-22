import { test } from 'node:test'
import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtemp, rm, access } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { createServer } from 'vite'

const exec = promisify(execFile)
const root = path.resolve(import.meta.dirname, '..')

test('real Chrome uses shared font classes, wrap widths and invariant runtime boxes', async t => {
  let browser
  for (const candidate of [process.env.ARBITRON_LAYOUT_BROWSERPATH, '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/usr/bin/chromium', '/usr/bin/google-chrome'].filter(Boolean)) {
    try { await access(candidate); browser = candidate; break } catch { /* try next installed browser */ }
  }
  if (!browser) {
    if (process.env.ARBITRON_REQUIRE_BROWSER === '1') throw new Error('Chrome/Chromium is required')
    return t.skip('Chrome/Chromium unavailable')
  }
  const profile = await mkdtemp(path.join(tmpdir(), 'arbitron-browser-fonts-'))
  const server = await createServer({ root, server: { port: 0, host: '127.0.0.1' }, plugins: [{
    name: 'arranged-test-fixture',
    configureServer(vite) {
      vite.middlewares.use('/__arranged-test', async (_req, res) => {
        res.setHeader('Content-Type', 'text/html')
        res.end(await vite.transformIndexHtml('/__arranged-test', '<html><body><div id="app" style="width:1200px;height:1200px"></div><pre id="result">pending</pre><script type="module" src="/tests/arranged-fixture.tsx"></script></body></html>'))
      })
    },
  }] })
  try {
    await server.listen()
    const address = server.httpServer.address()
    const { stdout } = await exec(browser, ['--headless=new', '--no-first-run', '--no-default-browser-check', `--user-data-dir=${profile}`, '--disable-gpu', '--disable-background-timer-throttling', '--disable-renderer-backgrounding', '--run-all-compositor-stages-before-draw', '--dump-dom', '--virtual-time-budget=20000', `http://127.0.0.1:${address.port}/__arranged-test`], { timeout: 60000, maxBuffer: 4 * 1024 * 1024 })
    const match = stdout.match(/<pre id="result">(.*?)<\/pre>/s)
    assert.ok(match, stdout)
    const result = JSON.parse(match[1].replaceAll('&quot;', '"').replaceAll('&amp;', '&').replaceAll('&lt;', '<').replaceAll('&gt;', '>'))
    assert.equal(result.error, undefined, result.error && `${result.error}\n${result.details}`)
    assert.equal(result.ready, true)
    assert.equal(result.border, '2px')
    assert.equal(result.label.boxSizing, 'border-box')
    assert.equal(result.label.wrap, 'anywhere')
    assert.equal(result.label.whiteSpace, 'pre-wrap')
    assert.equal(result.caption.whiteSpace, 'pre-wrap')
    assert.equal(result.caption.maxWidth, '240px')
    assert.equal(result.caption.wrap, 'anywhere')
    assert.ok(result.captionLines > 1, 'captions must wrap like their measurement request')
    assert.deepEqual(result.before.label, result.measuredLabel)
    assert.deepEqual(result.before.caption, result.measuredCaption)
    // The domain's ceilPx adds one px of content slack, plus 6px padding each side.
    assert.equal(result.before.edge.w - 12, Math.ceil(result.measuredEdge.w) + 1)
    assert.ok(result.before.edge.h - 6 >= result.measuredEdge.h)
    for (const snapshot of result.snapshots) {
      assert.deepEqual(snapshot.boxes, result.before)
      assert.equal(snapshot.opacity, '1')
      assert.equal(snapshot.path, 'M 174,840 L 174,900 L 295,900 M 305,900 L 534,900 L 534,840')
    }
  } finally {
    await server.close()
    await rm(profile, { recursive: true, force: true })
  }
})
