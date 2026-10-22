import { test } from 'node:test'
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { access, mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { createServer } from 'vite'

const root = path.resolve(import.meta.dirname, '..')
test('real render surface reports actual DOM evidence and runtime mutations', async t => {
  let browser
  for (const candidate of [process.env.ARBITRON_LAYOUT_BROWSERPATH, '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/usr/bin/chromium', '/usr/bin/google-chrome'].filter(Boolean)) {
    try { await access(candidate); browser = candidate; break } catch { /* next installed browser */ }
  }
  if (!browser) {
    if (process.env.ARBITRON_REQUIRE_BROWSER === '1') throw new Error('Chrome/Chromium is required')
    return t.skip('Chrome/Chromium unavailable')
  }
  const RENDER_TOKEN = '0123456789abcdef0123456789abcdef'
  const MEASURE_TOKEN = 'abcdef0123456789abcdef0123456789'
  const BAD_TOKEN = '11111111111111111111111111111111'
  const profile = await mkdtemp(path.join(tmpdir(), 'arbitron-render-surface-'))
  const server = await createServer({ root, server: { port: 0, host: '127.0.0.1' }, plugins: [{
    name: 'render-surface-fixture',
    configureServer(vite) {
      vite.middlewares.use('/__render/jobs', async (req, res) => {
        const { fixtureGeometry, fixtureWorkflow } = await vite.ssrLoadModule('/src/graph/arranged/testFixtures.ts')
        const geometry = fixtureGeometry()
        geometry.nodes[1].text = 'Café 測試 😀'
        geometry.edges[1].text.box.w = 40
        const jobs = {
          [`/${RENDER_TOKEN}.json`]: { mode: 'render', revision: 'rev-page', attempt: 2, geometry, workflowNodeTypes: Object.fromEntries(fixtureWorkflow().nodes.map(n => [n.id, n.type])), states: ['pending', 'running', 'completed', 'failed', 'waiting', 'skipped', 'selected'] },
          [`/${MEASURE_TOKEN}.json`]: { mode: 'measure', request: { contract: geometry.contract, items: [{ key: 'actual:page:label', text: '測試 Café 😀 '.repeat(6), style: 'node_label', maxWidth: 80 }] } },
          [`/${BAD_TOKEN}.json`]: { mode: 'render', revision: 'rev-page', attempt: 2, geometry, workflowNodeTypes: {}, states: [] },
        }
        res.setHeader('Content-Type', 'application/json')
        if (!Object.hasOwn(jobs, req.url)) { res.statusCode = 404; res.end('{}'); return }
        res.end(JSON.stringify(jobs[req.url]))
      })
      vite.middlewares.use('/__surface-test', async (_req, res) => {
        res.setHeader('Content-Type', 'text/html')
        res.end(await vite.transformIndexHtml('/__surface-test', '<html><body style="margin:0"><div id="app"></div><pre id="result">pending</pre><script type="module" src="/tests/render-surface-fixture.tsx"></script></body></html>'))
      })
    },
  }] })
  let chrome, socket
  try {
    await server.listen()
    chrome = spawn(browser, ['--headless=new', '--no-first-run', '--no-default-browser-check', `--user-data-dir=${profile}`, '--remote-debugging-port=0', '--disable-gpu', '--disable-background-timer-throttling', '--disable-renderer-backgrounding', 'about:blank'], { stdio: 'ignore' })
    let port
    for (let i = 0; i < 200; i++) {
      try { port = Number((await readFile(path.join(profile, 'DevToolsActivePort'), 'utf8')).split('\n')[0]); break } catch { await new Promise(resolve => setTimeout(resolve, 50)) }
    }
    assert.ok(port, 'Chrome debugging port did not start')
    const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json()
    socket = new WebSocket(targets.find(t => t.type === 'page').webSocketDebuggerUrl)
    await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject })
    let nextID = 0
    const pending = new Map()
    socket.onmessage = event => { const message = JSON.parse(event.data); if (message.id) { pending.get(message.id)?.(message); pending.delete(message.id) } }
    const send = (method, params = {}) => new Promise((resolve, reject) => {
      const id = ++nextID
      pending.set(id, message => message.error ? reject(new Error(JSON.stringify(message.error))) : resolve(message.result))
      socket.send(JSON.stringify({ id, method, params }))
    })
    await send('Page.navigate', { url: `http://127.0.0.1:${server.httpServer.address().port}/__surface-test` })
    // Real time/ResizeObserver/animation frames, not Chrome's virtual-time mode,
    // which can starve React Flow's measured handle initialization.
    let result
    for (let i = 0; i < 1200; i++) {
      const value = await send('Runtime.evaluate', { expression: 'document.querySelector("#result")?.textContent', returnByValue: true })
      const text = value.result?.value
      if (text && text !== 'pending') { result = JSON.parse(text); break }
      await new Promise(resolve => setTimeout(resolve, 50))
    }
    assert.ok(result, 'surface did not finish in 60 seconds')
    assert.equal(result.error, undefined, result.error && `${result.error}\n${result.details}`)
    assert.equal(result.ready, true)
    assert.ok(result.assertions >= 20, `only ${result.assertions} sink assertions ran`)
    async function page(search) {
      await send('Page.navigate', { url: `http://127.0.0.1:${server.httpServer.address().port}/render.html${search}` })
      // Wait for a new document before polling its globals (not the old ready page).
      for (let i = 0; i < 1200; i++) {
        const value = await send('Runtime.evaluate', { expression: `location.search === ${JSON.stringify(search)} && ['ready','error'].includes(window.__ARBITRON_RENDER_STATE__) ? JSON.stringify({ state: window.__ARBITRON_RENDER_STATE__, error: window.__ARBITRON_RENDER_ERROR__, report: window.__ARBITRON_RENDER_REPORT__, measured: window.__ARBITRON_MEASURE_RESULT__, probing: document.documentElement.hasAttribute('data-render-probing') }) : null`, returnByValue: true })
        if (value.result?.value) return JSON.parse(value.result.value)
        await new Promise(resolve => setTimeout(resolve, 50))
      }
      throw new Error('render.html did not publish terminal globals')
    }
    const measured = await page(`?job=${MEASURE_TOKEN}`)
    assert.equal(measured.state, 'ready', measured.error)
    assert.equal(measured.measured.fontsReady, true)
    assert.ok(measured.measured.items['actual:page:label'].width > 0 && measured.measured.items['actual:page:label'].width <= 80)
    assert.ok(measured.measured.items['actual:page:label'].lines > 1)
    const rendered = await page(`?job=${RENDER_TOKEN}`)
    assert.equal(rendered.state, 'ready', rendered.error)
    assert.equal(rendered.report.nodes[1].label.content, 'Café 測試 😀')
    assert.ok(rendered.report.nodes.every(n => n.label.visible && !n.label.clipped && !n.label.occluded))
    assert.ok(rendered.report.edges.every(e => e.hasArrow && e.visible && e.segments.length > 0))
    assert.ok(rendered.report.states.every(s => s.nodeBoxesStable && s.labelsReadable), JSON.stringify(rendered.report.states))
    assert.equal(rendered.probing, false)
    for (const search of ['', '?job=../../etc/passwd', '?job=ffffffffffffffffffffffffffffffff', `?job=${BAD_TOKEN}`]) {
      const failed = await page(search)
      assert.equal(failed.state, 'error')
      assert.ok(failed.error)
      assert.equal(failed.report, undefined)
      assert.equal(failed.measured, undefined)
    }
  } finally {
    socket?.close()
    if (chrome && chrome.exitCode === null) { const exited = new Promise(resolve => chrome.once('exit', resolve)); chrome.kill(); await exited }
    await server.close()
    await rm(profile, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 })
  }
})
