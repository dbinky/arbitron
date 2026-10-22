import { test } from 'node:test'
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { access, mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { createServer } from 'vite'

const root = path.resolve(import.meta.dirname, '..')
const pause = () => new Promise(resolve => setTimeout(resolve, 50))
// Whole pixels inside the fixture's 10px down-facing arrow, outside its 2px shaft.
// PNG coordinates address top-left corners, not pixel centres.
const arrowInteriorPixels = [-3, 2].flatMap(dx => [-9, -8, -7].map(dy => ({ dx, dy })))
test('two real ArrangedGraphs retain their own visible arrows after first unmounts', { timeout: 90000 }, async t => {
  let browser
  for (const candidate of [process.env.ARBITRON_LAYOUT_BROWSERPATH, '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/usr/bin/chromium', '/usr/bin/google-chrome'].filter(Boolean)) {
    try { await access(candidate); browser = candidate; break } catch { /* next installed browser */ }
  }
  if (!browser) {
    if (process.env.ARBITRON_REQUIRE_BROWSER === '1') throw new Error('Chrome/Chromium is required')
    return t.skip('Chrome/Chromium unavailable')
  }
  const profile = await mkdtemp(path.join(tmpdir(), 'arbitron-markers-'))
  const server = await createServer({ root, server: { port: 0, host: '127.0.0.1' }, plugins: [{
    name: 'arranged-markers-fixture',
    configureServer(vite) {
      vite.middlewares.use('/__markers-test', async (_req, res) => {
        res.setHeader('Content-Type', 'text/html')
        res.end(await vite.transformIndexHtml('/__markers-test', '<html><body style="margin:0"><div id="app"></div><script type="module" src="/tests/arranged-markers-fixture.tsx"></script></body></html>'))
      })
    },
  }] })
  let chrome, socket
  try {
    await server.listen()
    chrome = spawn(browser, ['--headless=new', '--no-first-run', '--no-default-browser-check', `--user-data-dir=${profile}`, '--remote-debugging-port=0', '--disable-gpu', '--disable-background-timer-throttling', '--disable-renderer-backgrounding', 'about:blank'], { stdio: 'ignore' })
    let port
    for (let i = 0; i < 200; i++) {
      try { port = Number((await readFile(path.join(profile, 'DevToolsActivePort'), 'utf8')).split('\n')[0]); break } catch { await pause() }
    }
    assert.ok(port, 'Chrome debugging port did not start')
    const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json()
    socket = new WebSocket(targets.find(target => target.type === 'page').webSocketDebuggerUrl)
    await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject })
    let nextID = 0
    const pending = new Map()
    socket.onmessage = event => {
      const message = JSON.parse(event.data)
      if (message.id) { pending.get(message.id)?.(message); pending.delete(message.id) }
    }
    const send = (method, params = {}) => new Promise((resolve, reject) => {
      const id = ++nextID
      const timeout = setTimeout(() => { pending.delete(id); reject(new Error(`CDP timeout: ${method}`)) }, 10000)
      pending.set(id, message => { clearTimeout(timeout); message.error ? reject(new Error(JSON.stringify(message.error))) : resolve(message.result) })
      socket.send(JSON.stringify({ id, method, params }))
    })
    const evaluate = async expression => {
      const value = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })
      assert.equal(value.exceptionDetails, undefined, JSON.stringify(value.exceptionDetails))
      return value.result.value
    }
    async function waitFor(expression) {
      for (let i = 0; i < 600; i++) { if (await evaluate(expression)) return; await pause() }
      assert.fail(`browser did not reach sink: ${expression}; ${await evaluate('document.body.innerText')}`)
    }
    await send('Emulation.setDeviceMetricsOverride', { width: 1000, height: 500, deviceScaleFactor: 1, mobile: false })
    await send('Page.navigate', { url: `http://127.0.0.1:${server.httpServer.address().port}/__markers-test` })
    // Real compositor frames allow React Flow to measure its own handles.
    await waitFor('document.querySelectorAll("[data-edge-path]").length === 6')
    async function snapshot(names) {
      const scenes = await evaluate(`(async () => {
        const { expected } = await import('/tests/arranged-markers-fixture.tsx')
        return ${JSON.stringify(names)}.map(name => {
          const section = document.querySelector('[data-graph="' + name + '"]')
          return { name, edges: expected[name].map(edge => {
            const path = section.querySelector('[data-edge-path="' + edge.ordinal + '"]')
            const ref = path.getAttribute('marker-end')
            const id = ref?.slice(5, -1)
            const marker = document.getElementById(id)
            const end = edge.points.at(-1)
            const tip = new DOMPoint(end.x, end.y).matrixTransform(path.getScreenCTM())
            return { id, own: marker != null && path.parentElement.contains(marker), color: marker?.querySelector('path')?.getAttribute('fill'),
              expectedColor: edge.color, d: path.getAttribute('d'), expectedD: edge.points.map((p,i) => (i ? 'L ' : 'M ') + p.x + ',' + p.y).join(' '), tip: { x: tip.x, y: tip.y } }
          }) }
        })
      })()`)
      const ids = await evaluate('[...document.querySelectorAll("marker")].map(marker => marker.id)')
      assert.equal(ids.length, scenes.flatMap(scene => scene.edges).length)
      assert.equal(new Set(ids).size, ids.length, 'every graph/ordinal needs a unique marker')
      for (const scene of scenes) for (const edge of scene.edges) {
        assert.ok(edge.own, `${scene.name} path must resolve to its own marker`)
        assert.ok(edge.id.startsWith('graph-') && edge.id.endsWith(`-arranged-arrow-${scene.edges.indexOf(edge)}`))
        assert.equal(edge.color, edge.expectedColor)
        assert.equal(edge.d, edge.expectedD, 'the supplied persisted route must reach the SVG sink')
      }
      const { data } = await send('Page.captureScreenshot', { format: 'png' })
      // Sample off-shaft arrow interiors in actual PNGs, not marker declarations.
      const ink = await evaluate(`(async () => {
        const image = new Image(); image.src = 'data:image/png;base64,${data}'; await image.decode()
        const canvas = document.createElement('canvas'); canvas.width=image.width; canvas.height=image.height
        const ctx=canvas.getContext('2d'); ctx.drawImage(image,0,0)
        return ${JSON.stringify(scenes.map(scene => scene.edges[0]))}.map(edge => {
          const rgb=edge.expectedColor.slice(1).match(/../g).map(v=>parseInt(v,16)); let count=0
          for(const { dx, dy } of ${JSON.stringify(arrowInteriorPixels)}) {
            const pixel=ctx.getImageData(Math.round(edge.tip.x+dx),Math.round(edge.tip.y+dy),1,1).data
            if(rgb.every((v,i)=>Math.abs(v-pixel[i])<=8)) count++
          }
          return count
        })
      })()`)
      ink.forEach((count, i) => assert.equal(count, arrowInteriorPixels.length, `${names[i]} ordinal 0 arrow must have visible ${scenes[i].edges[0].expectedColor} ink, got ${count} pixels`))
      return scenes
    }
    const before = await snapshot(['first', 'second'])
    assert.notEqual(before[0].edges[0].color, before[1].edges[0].color)
    await evaluate('document.querySelector("#remove-first").click()')
    await waitFor('document.querySelectorAll("[data-edge-path]").length === 3 && !document.querySelector("[data-graph=first]")')
    const after = await snapshot(['second'])
    assert.deepEqual(after[0], before[1], 'unmounting the first graph must preserve second graph identity/route/arrow')
  } finally {
    socket?.close()
    if (chrome && chrome.exitCode === null) {
      const exited = new Promise(resolve => chrome.once('exit', resolve))
      const killTimeout = setTimeout(() => chrome.kill('SIGKILL'), 3000)
      chrome.kill()
      await exited
      clearTimeout(killTimeout)
    }
    await server.close()
    await rm(profile, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 })
  }
})
