// cdp-nav.mjs — navigate the P-Chat GPU probe tab to a given mode via CDP.
// Usage: node cdp-nav.mjs <mode>   (current | optimized | idle)
const mode = process.argv[2]
if (!mode) { console.error('usage: node cdp-nav.mjs <current|optimized|idle>'); process.exit(1) }

const list = await (await fetch('http://127.0.0.1:9222/json')).json()
const page = list.find(t => t.type === 'page' && t.url.includes('stream-test.html'))
if (!page) { console.error('probe tab not found'); process.exit(1) }

const ws = new WebSocket(page.webSocketDebuggerUrl)
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej })
let id = 0
const pending = new Map()
ws.onmessage = (ev) => {
  const msg = JSON.parse(ev.data)
  if (msg.id && pending.has(msg.id)) { pending.get(msg.id)(msg); pending.delete(msg.id) }
}
const send = (method, params = {}) => new Promise((res) => {
  const mid = ++id; pending.set(mid, res)
  ws.send(JSON.stringify({ id: mid, method, params }))
})
const url = 'file:///D:/develop/project/P-chat/scripts/gpu-probe/stream-test.html?mode=' + mode
await send('Page.navigate', { url })
await new Promise(r => setTimeout(r, 3000))   // let page boot the streaming loop
ws.close()
console.log('navigated to mode=' + mode)
