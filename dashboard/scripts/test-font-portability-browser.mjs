import { test } from 'node:test'
import assert from 'node:assert/strict'
import { execFile, spawn } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { createServer } from 'vite'

const exec = promisify(execFile)
const root = path.resolve(import.meta.dirname, '..')
const image = 'arbitron:layout-render' // docker build -t arbitron:layout-render .
const coverage = JSON.parse(await readFile(path.join(root, 'src/graph/arranged/fontCoverage.json'), 'utf8'))
const usedSubsets = [
  '@fontsource/inter/files/inter-latin-600-normal.woff2',
  ...[['Noto Sans SC', '測'], ['Noto Emoji', '😀']].map(([family, text]) => {
    const cp = text.codePointAt(0)
    const face = coverage.faces.findLast(face => face.family === family && face.weight === 600 && face.ranges.some(([a, b]) => a <= cp && cp <= b))
    assert.ok(face, `the shipped coverage must identify ${family}'s used subset`)
    return face.file
  }),
]
const pause = () => new Promise(resolve => setTimeout(resolve, 50))
const antialiasTolerance = 32 // Same per-channel tolerance as the production raster reviewer.

// Chromium's pipe transport keeps CDP private and works on Docker Desktop as
// well as Linux: fd 3/4 are forwarded through docker's stdin/stdout, not a port.
// The page origin is http://host.docker.internal:<port> — a plain-HTTP,
// non-localhost origin like a LAN deployment (server.tls.enabled=false), so the
// page is NOT a secure context. No --unsafely-treat-insecure-origin-as-secure.
async function browser(fontConfig, directory) {
  const { stdout: inventory } = await exec('docker', ['run', '--rm', ...fontConfig, '--entrypoint', 'fc-list', image])
  const name = `arbitron-fonts-${path.basename(directory)}-${fontConfig.length ? 'empty' : 'system'}`
  const container = spawn('docker', ['run', '--rm', '-i', '--name', name,
    '--add-host=host.docker.internal:host-gateway', ...fontConfig, '--entrypoint', 'sh', image, '-c',
    'exec chromium-browser --headless --no-sandbox --disable-gpu --no-first-run --no-default-browser-check ' +
    '--remote-debugging-pipe 3<&0 4>&1 >/tmp/chrome.log 2>&1'], { stdio: ['pipe', 'pipe', 'pipe'] })
  let buffer = '', nextID = 0, stderr = ''
  const pending = new Map()
  const listeners = new Set()
  const exited = new Promise(resolve => container.once('exit', resolve))
  container.stderr.on('data', chunk => { stderr += chunk })
  container.stdout.on('data', chunk => {
    buffer += chunk
    for (let end; (end = buffer.indexOf('\0')) !== -1;) {
      const message = JSON.parse(buffer.slice(0, end)); buffer = buffer.slice(end + 1)
      if (message.id) { pending.get(message.id)?.(message); pending.delete(message.id) }
      else listeners.forEach(listener => listener(message))
    }
  })
  const send = (method, params = {}, sessionId) => new Promise((resolve, reject) => {
    const id = ++nextID
    const timeout = setTimeout(() => { pending.delete(id); reject(new Error(`CDP timeout: ${method}; ${stderr}`)) }, 20000)
    pending.set(id, message => { clearTimeout(timeout); message.error ? reject(new Error(JSON.stringify(message.error))) : resolve(message.result) })
    container.stdin.write(JSON.stringify({ id, method, params, sessionId }) + '\0')
  })
  return {
    inventory: inventory.trim().split('\n').filter(Boolean),
    send, listeners,
    async close() {
      if (container.exitCode === null) await exec('docker', ['stop', '--time', '2', name], { timeout: 15000 })
      container.stdin.end()
      await exited
    },
  }
}

async function page(chrome, url, fault) {
  const { targetId } = await chrome.send('Target.createTarget', { url: 'about:blank' })
  const { sessionId } = await chrome.send('Target.attachToTarget', { targetId, flatten: true })
  const send = (method, params) => chrome.send(method, params, sessionId)
  const evaluate = async expression => {
    const result = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })
    assert.equal(result.exceptionDetails, undefined, JSON.stringify(result.exceptionDetails))
    return result.result.value
  }
  let intercepted = 0, responseError
  const faultedURLs = new Set()
  const responses = []
  const onRequest = message => {
    if (message.sessionId !== sessionId || message.method !== 'Fetch.requestPaused') return
    const { requestId } = message.params
    responses.push((async () => {
      const stage = await evaluate('document.documentElement.dataset.portabilityStage')
      if (stage !== fault.stage) {
        await send('Fetch.continueRequest', { requestId })
        return
      }
      intercepted++
      const requested = new URL(message.params.request.url).pathname
      faultedURLs.add(requested)
      if (fault.kind === 'missing') {
        await send('Fetch.fulfillRequest', { requestId, responseCode: 404, body: '' })
      } else {
        const bytes = await readFile(path.join(root, 'node_modules', fault.file.replace(/\.woff2$/, path.extname(requested))))
        bytes[0] ^= 1
        await send('Fetch.fulfillRequest', { requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: `font/${path.extname(requested).slice(1)}` }], body: bytes.toString('base64') })
      }
    })().catch(error => { responseError = error }))
  }
  try {
    // Every page starts with a cold HTTP cache so the fault always meets the
    // browser's own request instead of a cached response from an earlier page.
    await send('Network.enable')
    await send('Network.setCacheDisabled', { cacheDisabled: true })
    if (fault) {
      chrome.listeners.add(onRequest)
      // The runtime gate makes no fetch of its own: fault the browser's real
      // @font-face requests for the used subset. Fontsource declares
      // `src: url(.woff2), url(.woff)`, so a face is only unavailable when both
      // of its sources fail; the pattern covers the .woff2 and its .woff fallback.
      await send('Fetch.enable', { patterns: [{ urlPattern: `*${fault.file.replace(/\.woff2$/, '.woff')}*`, resourceType: 'Font' }] })
    }
    await send('Emulation.setDeviceMetricsOverride', { width: 1200, height: 1200, deviceScaleFactor: 1, mobile: false })
    await send('Page.navigate', { url })
    let result
    for (let i = 0; i < 600; i++) {
      if (responseError) throw responseError
      const text = await evaluate('document.querySelector("#result")?.textContent')
      if (text && text !== 'pending') { result = JSON.parse(text); break }
      await pause()
    }
    assert.deepEqual(await evaluate('({ secure: isSecureContext, subtle: typeof crypto.subtle, protocol: location.protocol, host: location.hostname })'),
      { secure: false, subtle: 'undefined', protocol: 'http:', host: 'host.docker.internal' }, 'the gate must run on an insecure (plain-HTTP LAN) origin')
    assert.ok(result, `measure/render did not finish (${JSON.stringify(fault) ?? 'baseline'}, intercepted ${intercepted} requests): ${await evaluate('document.body.innerText')}`)
    await Promise.all(responses)
    if (responseError) throw responseError
    if (fault) {
      assert.ok(intercepted > 0, `${fault.kind} control must affect the browser's real font request for ${fault.file} at ${fault.stage}`)
      assert.ok([...faultedURLs].some(u => u.endsWith(fault.file)) && [...faultedURLs].every(u => /\.woff2?$/.test(u)),
        `the fault must hit the subset's real WOFF2 request (and only font files): ${[...faultedURLs]}`)
    }
    if (result.error) return result

    // The CSS family declaration is not proof: ask Blink which fonts actually
    // supplied glyphs to every rendered node label/caption and branch label.
    await send('DOM.enable')
    await send('CSS.enable')
    const { root: document } = await send('DOM.getDocument')
    const { nodeIds } = await send('DOM.querySelectorAll', { nodeId: document.nodeId, selector: '[data-node-text], [data-edge-label]' })
    assert.equal(nodeIds.length, 8, 'all three real nodes and both branch labels must be drawn')
    const fonts = []
    for (const nodeId of nodeIds) {
      const used = (await send('CSS.getPlatformFontsForNode', { nodeId })).fonts.filter(font => font.glyphCount > 0)
      assert.ok(used.length, 'each nonempty text must have actual glyphs')
      assert.ok(used.every(font => font.isCustomFont), `system fallback supplied glyphs: ${JSON.stringify(used)}`)
      fonts.push(used.map(({ familyName, postScriptName, glyphCount }) => ({ familyName, postScriptName, glyphCount })).sort((a, b) => a.postScriptName.localeCompare(b.postScriptName)))
    }
    // Blink exposes the font file's internal (weight-specific) names, not its
    // CSS @font-face alias. Noto Sans SC's shipped static files name 'Thin'.
    for (const prefix of ['Inter-', 'NotoSansSCThin-', 'NotoEmoji-']) {
      assert.ok(fonts.flat().some(font => font.postScriptName.startsWith(prefix)), `${prefix} must supply actual glyphs: ${JSON.stringify(fonts)}`)
    }

    // Read the compositor PNG too: declarations and Range boxes alone could
    // still be green with invisible text. Sample inside every full line range.
    const { data } = await send('Page.captureScreenshot', { format: 'png' })
    const ink = await evaluate(`(async () => {
      const image = new Image(); image.src = 'data:image/png;base64,${data}'; await image.decode()
      const canvas = document.createElement('canvas'); canvas.width=image.width; canvas.height=image.height
      const ctx=canvas.getContext('2d'); ctx.drawImage(image,0,0)
      const report=${JSON.stringify(result.report)}
      const textElements=[...document.querySelectorAll('[data-arranged-node]')].flatMap(node=>[
        node.querySelector('[data-node-text="label"]'), node.querySelector('[data-node-text="type"]')
      ]).concat([...document.querySelectorAll('[data-edge-label]')])
      return [...report.nodes.flatMap(n=>[n.label,n.type]),...report.edges.flatMap(e=>e.label?[e.label]:[])].map((text,index) => text.glyphs.map(box => {
        const rgb=getComputedStyle(textElements[index]).color.match(/[\\d.]+/g).slice(0,3).map(Number)
        const x=Math.floor(box.x*report.zoom),y=Math.floor(box.y*report.zoom)
        const w=Math.ceil(box.w*report.zoom),h=Math.ceil(box.h*report.zoom)
        const pixels=ctx.getImageData(x,y,w,h).data; let count=0
        for(let i=0;i<pixels.length;i+=4) if(rgb.every((value,channel)=>Math.abs(value-pixels[i+channel])<=${antialiasTolerance})) count++
        return count
      }))
    })()`)
    assert.ok(ink.length >= 8 && ink.every(lines => lines.length && lines.every(count => count > 0)), `every rendered line must have text ink: ${JSON.stringify(ink)}`)
    return { ...result, fonts }
  } finally {
    chrome.listeners.delete(onRequest)
    await chrome.send('Target.closeTarget', { targetId })
  }
}

test('bundled glyphs and measured/rendered widths survive different system-font inventories', { timeout: 240000 }, async () => {
  // Deliberately no skip: this gate needs the real Chromium image and Docker.
  const directory = await mkdtemp(path.join(tmpdir(), 'arbitron-portable-'))
  const config = path.join(directory, 'fonts.conf')
  await writeFile(config, '<?xml version="1.0"?><!DOCTYPE fontconfig SYSTEM "urn:fontconfig:fonts.dtd"><fontconfig><include>/etc/fonts/fonts.conf</include><selectfont><rejectfont><glob>*</glob></rejectfont></selectfont></fontconfig>')
  const html = (await readFile(path.join(root, 'render.html'), 'utf8'))
    .replace('/src/render/main.tsx', '/tests/font-portability-fixture.tsx')
    .replace('</body>', '<pre id="result">pending</pre></body>')
  const server = await createServer({ root, server: { port: 0, host: '0.0.0.0', allowedHosts: ['host.docker.internal'] }, plugins: [{
    name: 'font-portability-fixture',
    configureServer(vite) {
      vite.middlewares.use('/__font-portability', async (_req, res) => {
        res.setHeader('Content-Type', 'text/html')
        res.end(await vite.transformIndexHtml('/__font-portability', html))
      })
    },
  }] })
  const browsers = []
  try {
    await server.listen()
    const port = server.httpServer.address().port
    const url = `http://host.docker.internal:${port}/__font-portability`
    browsers.push(await browser([], directory))
    browsers.push(await browser(['--mount', `type=bind,source=${config},target=/tmp/fonts.conf,readonly`, '-e', 'FONTCONFIG_FILE=/tmp/fonts.conf'], directory))
    assert.ok(browsers[0].inventory.length > 0, 'control browser needs installed system fonts')
    console.log(`verified font inventories: ${browsers[0].inventory.length} installed faces vs ${browsers[1].inventory.length}`)
    assert.equal(browsers[1].inventory.length, 0, 'isolated browser must have NO system fonts')
    const baselines = []
    for (const chrome of browsers) {
      const baseline = await page(chrome, url)
      assert.equal(baseline.error, undefined, baseline.error)
      assert.equal(baseline.measured.fontsReady, true)
      assert.ok(baseline.measured.items.label.width > 0 && baseline.measured.items.label.lines > 1)
      assert.equal(baseline.report.fontsReady, true)
      assert.equal(baseline.report.fontFamilyResolved, true)
      assert.equal(baseline.report.nodes.length, 3)
      assert.equal(baseline.report.edges.length, 3)
      assert.ok(baseline.report.nodes.every(node => node.label.content.includes('Café é 測試 😀 👩‍💻 ☠︎') && node.label.visible && !node.label.clipped && !node.label.occluded))
      assert.ok(baseline.report.edges.every(edge => edge.visible && edge.hasArrow && edge.segments.length > 0))
      assert.equal(baseline.report.states.length, 7)
      assert.ok(baseline.report.states.every(state => state.nodeBoxesStable && state.labelsReadable), JSON.stringify(baseline.report.states))
      baselines.push(baseline)
      for (const stage of ['measure', 'render']) {
        for (const text of ['scalar', 'variation', 'zwj']) {
          const failed = await page(chrome, `${url}?text=${text}&stage=${stage}`)
          assert.equal(failed.stage, stage)
          assert.match(failed.error, /unsupported bundled-font/)
          assert.equal(failed.measured, undefined)
          assert.equal(failed.report, undefined)
        }
        // A face already loaded while measuring is never re-requested by the same
        // document, so render-stage faults replay the baseline measurement in a
        // fresh page whose first font request happens while rendering.
        const faultURL = stage === 'render' ? `${url}?measured=${encodeURIComponent(JSON.stringify(baseline.measured))}` : url
        for (const fault of [
          ...usedSubsets.map(file => ({ kind: 'missing', file, stage })),
          { kind: 'changed', file: usedSubsets[0], stage },
        ]) {
          const failed = await page(chrome, faultURL, fault)
          // 404 and undecodable bytes both leave the browser's face in error.
          assert.ok(failed.error?.includes(`font asset unavailable: ${fault.file}`), `${JSON.stringify(fault)}: ${failed.error}`)
          assert.equal(failed.stage, stage)
          assert.equal(failed.measured, undefined)
          assert.equal(failed.report, undefined)
        }
      }
    }
    assert.deepEqual(baselines[1].measured, baselines[0].measured, 'actual fractional metrics must not depend on system fonts')
    assert.deepEqual(baselines[1].report, baselines[0].report, 'actual rendered glyph boxes/routes must not depend on system fonts')
    assert.deepEqual(baselines[1].fonts, baselines[0].fonts, 'the same bundled faces must supply every actual glyph')
  } finally {
    try {
      await Promise.all(browsers.map(chrome => chrome.close()))
    } finally {
      await server.close()
      await rm(directory, { recursive: true, force: true })
    }
  }
})
