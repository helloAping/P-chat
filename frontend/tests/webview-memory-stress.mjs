import { spawn } from 'node:child_process'
import assert from 'node:assert/strict'
import { mkdir, rm } from 'node:fs/promises'
import { request } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const edgePath = process.env.PCHAT_EDGE_PATH
  || 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe'
const port = Number(process.env.PCHAT_CDP_PORT || 9227)
const rounds = Number(process.env.PCHAT_STRESS_ROUNDS || 600)
const maxDOMNodes = Number(process.env.PCHAT_MAX_DOM_NODES || 2_500)
const maxHeapGrowth = Number(process.env.PCHAT_MAX_HEAP_GROWTH || 20 * 1024 * 1024)
const profileDir = join(tmpdir(), `pchat-webview-memory-${process.pid}`)

function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms))
}

function getJSON(url) {
  return new Promise((resolve, reject) => {
    request(url, response => {
      let body = ''
      response.setEncoding('utf8')
      response.on('data', chunk => { body += chunk })
      response.on('end', () => {
        try { resolve(JSON.parse(body)) } catch (error) { reject(error) }
      })
    }).on('error', reject).end()
  })
}

async function waitForPage() {
  const endpoint = `http://127.0.0.1:${port}/json/list`
  for (let i = 0; i < 100; i++) {
    try {
      const pages = await getJSON(endpoint)
      const page = pages.find(item =>
        item.type === 'page'
        && item.url?.startsWith('http://127.0.0.1:5173/app/')
        && item.webSocketDebuggerUrl,
      )
      if (page) return page
    } catch {
      // Edge has not opened the DevTools endpoint yet.
    }
    await sleep(100)
  }
  throw new Error(`timed out waiting for Edge DevTools on ${endpoint}`)
}

function openCDP(url) {
  const ws = new WebSocket(url)
  let id = 0
  const pending = new Map()
  ws.addEventListener('message', event => {
    const message = JSON.parse(String(event.data))
    const waiter = pending.get(message.id)
    if (!waiter) return
    pending.delete(message.id)
    if (message.error) waiter.reject(new Error(`${waiter.method}: ${message.error.message}`))
    else waiter.resolve(message.result)
  })
  return new Promise((resolve, reject) => {
    ws.addEventListener('open', () => resolve({
      send(method, params = {}) {
        const requestID = ++id
        ws.send(JSON.stringify({ id: requestID, method, params }))
        return new Promise((resolveRequest, rejectRequest) => {
          pending.set(requestID, { method, resolve: resolveRequest, reject: rejectRequest })
        })
      },
      close() { ws.close() },
    }))
    ws.addEventListener('error', reject, { once: true })
  })
}

async function evaluate(cdp, expression) {
  const response = await cdp.send('Runtime.evaluate', {
    expression,
    awaitPromise: true,
    returnByValue: true,
  })
  if (response.exceptionDetails) {
    throw new Error(response.exceptionDetails.text || 'Runtime.evaluate failed')
  }
  return response.result.value
}

async function metrics(cdp) {
  const heap = await cdp.send('Runtime.getHeapUsage')
  const page = await evaluate(cdp, `({
    domNodes: document.getElementsByTagName('*').length,
    usedJSHeap: performance.memory?.usedJSHeapSize ?? null,
    totalJSHeap: performance.memory?.totalJSHeapSize ?? null,
  })`)
  return { ...page, usedSize: heap.usedSize, totalSize: heap.totalSize }
}

async function waitForVueApp(cdp) {
  for (let i = 0; i < 100; i++) {
    const ready = await evaluate(cdp, `Boolean(
      document.querySelector('.chat-main')
      && window.__pchatDebug
    )`)
    if (ready) return
    await sleep(100)
  }
  throw new Error('timed out waiting for the Vue chat application')
}

async function waitForRenderSettled(cdp) {
  await evaluate(cdp, `new Promise(resolve => {
    let frames = 0
    const tick = () => {
      frames += 1
      if (frames >= 4) {
        setTimeout(resolve, 50)
        return
      }
      requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
  })`)
}

async function main() {
  await mkdir(profileDir, { recursive: true })
  const edge = spawn(edgePath, [
    `--remote-debugging-port=${port}`,
    '--remote-allow-origins=*',
    '--enable-precise-memory-info',
    '--headless=new',
    '--disable-gpu',
    '--no-first-run',
    '--no-default-browser-check',
    `--user-data-dir=${profileDir}`,
    'http://127.0.0.1:5173/app/',
  ], { stdio: 'ignore', windowsHide: true })

  try {
    const page = await waitForPage()
    const cdp = await openCDP(page.webSocketDebuggerUrl)
    await cdp.send('Runtime.enable')
    await waitForVueApp(cdp)
    await waitForRenderSettled(cdp)
    const before = await metrics(cdp)
    const after = await evaluate(cdp, `
      (async () => {
        const chat = await import('/app/src/stores/chat.ts')
        const id = 'webview-memory-stress'
        chat.state.currentID = id
        chat.state.sessions = [{ id, title: 'Memory stress', created_at: 0, updated_at: 0 }]
        chat.state.sessionMeta[id] = { style: 'off', workMode: 'coding', provider: '', model: '', title: 'Memory stress' }
        chat.state.sessionMessages[id] = []
        chat.startStream(id, new AbortController())
        for (let i = 0; i < ${rounds}; i++) {
          chat.appendStreamEvent(id, { type: 'thinking', thinking: 'reasoning token '.repeat(16) + i })
          chat.appendStreamEvent(id, {
            type: 'tool', tool_id: 'call_' + i, tool_name: 'exec_command',
            tool_status: 'start', tool_args: JSON.stringify({ command: 'echo ' + i }),
          })
          chat.appendStreamEvent(id, {
            type: 'tool', tool_id: 'call_' + i, tool_name: 'exec_command',
            tool_status: 'ok', tool_result: 'ok ' + i,
          })
        }
        const message = chat.state.sessionMessages[id].at(-1)
        return { parts: message?.parts?.length ?? 0, messages: chat.state.sessionMessages[id].length }
      })()
    `)
    await waitForRenderSettled(cdp)
    const during = await metrics(cdp)
    await cdp.send('HeapProfiler.enable')
    await cdp.send('HeapProfiler.collectGarbage')
    const afterGC = await metrics(cdp)
    cdp.close()

    const report = {
      rounds,
      limits: { maxDOMNodes, maxHeapGrowth },
      before,
      after,
      during,
      afterGC,
      growth: {
        domNodes: afterGC.domNodes - before.domNodes,
        usedSize: afterGC.usedSize - before.usedSize,
      },
    }
    console.log(JSON.stringify(report, null, 2))

    assert.equal(after.parts, rounds * 2, 'the replay must preserve every part in store state')
    assert.ok(
      afterGC.domNodes <= maxDOMNodes,
      `live DOM grew to ${afterGC.domNodes} nodes; expected at most ${maxDOMNodes}`,
    )
    assert.ok(
      report.growth.usedSize <= maxHeapGrowth,
      `post-GC JS heap grew by ${report.growth.usedSize} bytes; expected at most ${maxHeapGrowth}`,
    )
  } finally {
    edge.kill()
    if (edge.exitCode === null) {
      await new Promise(resolve => edge.once('exit', resolve))
    }
    await rm(profileDir, { recursive: true, force: true })
  }
}

main().catch(error => {
  console.error(error.stack || error)
  process.exitCode = 1
})
