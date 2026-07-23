// background.js — P-Chat Browser Bridge Service Worker
//
// Connects to the pchat-server via WebSocket, receives JSON-RPC
// commands, forwards them to chrome.tabs or content.js, and
// returns structured results.

const DEFAULT_SERVER = 'ws://127.0.0.1:15150/api/v1/browser/ws';
const PROTOCOL_VERSION = '3';
const RECONNECT_BASE_MS = 3000;
const RECONNECT_MAX_MS = 30000;
const HEARTBEAT_INTERVAL_MS = 30000;

// Debug helper (visible in edge://extensions → service worker → inspect)
function dbg(...args) {
  const ts = new Date().toISOString().substring(11, 23);
  console.log(`[pchat-ext ${ts}]`, ...args);
}

let ws = null;
let browserID = null; // assigned by server on first hello; persisted
let reconnectDelay = RECONNECT_BASE_MS;
let reconnectTimer = null;
let heartbeatTimer = null;
let reconnectAttempts = 0;
let lastStatusCode = 0; // WebSocket close code of last failure
let permanentFailure = false;
let preferredTabId = null; // server/GUI selected control target tab

let updateRequired = false;
let updateMessage = '';
dbg('Service worker started');

// --- Storage helpers ---

// --- URL helpers ---

// normalizeServerURL converts HTTP(S) URLs (entered by users in the
// popup) into proper WebSocket URLs. Also appends /api/v1/browser/ws
// if the path is missing. Supports:
//
//   http://127.0.0.1:15150            → ws://127.0.0.1:15150/api/v1/browser/ws
//   http://127.0.0.1:15150/api/v1/... → ws://127.0.0.1:15150/api/v1/browser/ws
//   ws://127.0.0.1:15150/api/v1/...   → (unchanged)
function normalizeServerURL(raw) {
  try {
    const url = new URL(raw);
    if (url.protocol === 'http:') url.protocol = 'ws:';
    if (url.protocol === 'https:') url.protocol = 'wss:';
    if (!url.pathname.includes('/api/v1/browser/ws')) {
      url.pathname = '/api/v1/browser/ws';
    }
    return url.toString();
  } catch {
    return raw;
  }
}

async function getServerURL() {
  const data = await chrome.storage.local.get('serverURL');
  const raw = data.serverURL || DEFAULT_SERVER;
  return normalizeServerURL(raw);
}

async function setServerURL(url) {
  // Always store the raw input (user may edit it later).
  // normalizeServerURL handles the conversion on connect.
  await chrome.storage.local.set({ serverURL: url });
}

async function getBrowserID() {
  const data = await chrome.storage.local.get('browserID');
  return data.browserID || '';
}

async function setBrowserID(id) {
  browserID = id;
  await chrome.storage.local.set({ browserID: id });
}

// --- Status broadcasting ---

function broadcastStatus(status) {
  dbg('broadcastStatus:', JSON.stringify(status));
  chrome.runtime.sendMessage({ type: 'status-update', status }).catch(() => {});
}

// --- WebSocket connection ---

async function connect() {
  dbg('connect() called');
  if (ws && (ws.readyState === WebSocket.CONNECTING || ws.readyState === WebSocket.OPEN)) {
    dbg('Already connected, skipping');
    return;
  }

  const url = await getServerURL();
  dbg('Got URL from storage:', url);
  broadcastStatus({ connecting: true, url });

  try {
    dbg('Creating WebSocket...');
    ws = new WebSocket(url);
    dbg('WebSocket object created');
  } catch (e) {
    dbg('WebSocket constructor error:', e);
    broadcastStatus({ error: e.message });
    scheduleReconnect();
    return;
  }

  ws.onopen = async () => {
    dbg('WebSocket opened successfully');
    reconnectDelay = RECONNECT_BASE_MS;
    reconnectAttempts = 0;
    lastStatusCode = 0;
    permanentFailure = false;
    broadcastStatus({ connected: true, url });
    await sendHello();
    startHeartbeat();
  };

  ws.onmessage = async (event) => {
    let msg;
    try {
      msg = JSON.parse(event.data);
      dbg('onmessage received:', msg);
    } catch {
      dbg('onmessage parse error, data:', event.data);
      return;
    }
    // Hello response from server
    if (msg.type === 'hello-ok') {
      dbg('Received hello-ok, browser_id:', msg.browser_id);
      await setBrowserID(msg.browser_id);
      updateRequired = !!msg.update_required;
      updateMessage = msg.update_message || '';
      if (updateRequired) {
        broadcastStatus({ updateRequired, updateMessage });
      }
      return;
    }
    // JSON-RPC request from server
    if (msg.id != null && msg.method) {
      try {
        const result = await handleCommand(msg.method, msg.params || {});
        sendResponse(msg.id, result);
      } catch (e) {
        sendError(msg.id, -1, e.message || String(e));
      }
    }
  };

  ws.onclose = (event) => {
    dbg('WebSocket closed, code:', event.code, 'reason:', event.reason);
    ws = null;
    stopHeartbeat();
    lastStatusCode = event.code || 0;
    // 1006 = abnormal closure (connection refused / DNS failure)
    // 403 = server returned 403 Forbidden during upgrade (browser control disabled)
    // 1006 is transient (server might come back), but we track it for status display.
    // Codes 4000-4999 are application-level, server-side rejections.
    const permanent = event.code >= 4000 && event.code <= 4999;
    permanentFailure = permanent;
    broadcastStatus({
      disconnected: true,
      permanent: permanent,
      code: event.code,
      reason: event.reason || '',
    });
    if (!permanent) {
      scheduleReconnect();
    }
  };

  ws.onerror = (e) => {
    dbg('WebSocket error event');
    broadcastStatus({ error: 'WebSocket error' });
    // onclose fires immediately after onerror with the actual code/reason
  };
}

function disconnect() {
  cancelReconnect();
  stopHeartbeat();
  if (ws) {
    ws.close(1000, 'user disconnect');
    ws = null;
  }
  broadcastStatus({ disconnected: true });
}

function scheduleReconnect() {
  cancelReconnect();
  reconnectAttempts++;
  const delay = Math.min(reconnectDelay * Math.pow(2, reconnectAttempts - 1), RECONNECT_MAX_MS);
  reconnectTimer = setTimeout(connect, delay);
}

function cancelReconnect() {
  if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
}

function startHeartbeat() {
  stopHeartbeat();
  heartbeatTimer = setInterval(() => {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send('{}'); // keepalive ping (empty JSON — parsed but ignored by server)
    }
  }, HEARTBEAT_INTERVAL_MS);
}

function stopHeartbeat() {
  if (heartbeatTimer) { clearInterval(heartbeatTimer); heartbeatTimer = null; }
}

// --- Hello handshake ---

async function sendHello() {
  browserID = await getBrowserID();
  const tabs = await chrome.tabs.query({});
  const manifest = chrome.runtime.getManifest();
  const msg = {
    method: 'hello',
    params: {
      browser_name: getBrowserName(),
      tabs_count: tabs.length,
      extension_version: manifest.version || '',
      protocol_version: PROTOCOL_VERSION,
      id: browserID || '',
    },
  };
  ws.send(JSON.stringify(msg));
}

function getBrowserName() {
  const ua = navigator.userAgent;
  if (ua.includes('Edg/')) return 'Edge ' + (ua.match(/Edg\/(\d+)/) || ['', '?'])[1];
  if (ua.includes('Chrome/')) return 'Chrome ' + (ua.match(/Chrome\/(\d+)/) || ['', '?'])[1];
  return 'Chromium';
}

// --- Response helpers ---

function sendResponse(id, result) {
  if (!ws || ws.readyState !== WebSocket.OPEN) return;
  ws.send(JSON.stringify({ jsonrpc: '2.0', id, result }));
}

function sendError(id, code, message) {
  if (!ws || ws.readyState !== WebSocket.OPEN) return;
  ws.send(JSON.stringify({ jsonrpc: '2.0', id, error: { code, message } }));
}

// --- Command routing ---

async function handleCommand(method, params) {
  // browser/tabs manages the tab list itself; other commands need a target tab.
  const tabID = method === 'browser/tabs' ? null : await getTargetTabID(params || {});
  switch (method) {
    case 'browser/navigate':    return cmdNavigate(params, tabID);
    case 'browser/click':       return cmdClick(params, tabID);
    case 'browser/type':        return cmdType(params, tabID);
    case 'browser/press_key':   return cmdPressKey(params, tabID);
    case 'browser/scroll':      return cmdScroll(params, tabID);
    case 'browser/hover':       return cmdHover(params, tabID);
    case 'browser/select_option': return cmdSelectOption(params, tabID);
    case 'browser/evaluate':    return cmdEvaluate(params, tabID);
    case 'browser/snapshot':    return cmdSnapshot(params, tabID);
    case 'browser/screenshot':  return cmdScreenshot(params, tabID);
    case 'browser/find':        return cmdFind(params, tabID);
    case 'browser/tabs':        return cmdTabs(params);
    case 'browser/drag':        return cmdDrag(params, tabID);
    case 'browser/file_upload': return cmdFileUpload(params, tabID);
    case 'browser/extract':     return cmdExtract(params, tabID);
    default: throw new Error('unknown method: ' + method);
  }
}

// Resolve which tab browser_* tools should control.
// Priority: explicit params.tab_id → preferredTabId (GUI/tool selected)
// → currently active browser tab. If preferred tab was closed, fall back.
async function getTargetTabID(params) {
  const explicit = params && params.tab_id != null ? Number(params.tab_id) : null;
  if (explicit != null && !Number.isNaN(explicit)) {
    try {
      await chrome.tabs.get(explicit);
      preferredTabId = explicit;
      return explicit;
    } catch {
      throw new Error('tab not found: ' + explicit);
    }
  }

  if (preferredTabId != null) {
    try {
      await chrome.tabs.get(preferredTabId);
      return preferredTabId;
    } catch {
      preferredTabId = null;
    }
  }

  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (!tab) throw new Error('no active tab');
  preferredTabId = tab.id;
  return tab.id;
}

async function getActiveTabID() {
  return getTargetTabID({});
}

// --- Command implementations ---

async function cmdNavigate(params, tabID) {
  const url = params.url;
  if (!url) throw new Error('url is required');
  await chrome.tabs.update(tabID, { url });
  // Wait for navigation to settle
  await new Promise(r => setTimeout(r, 1500));
  const tab = await chrome.tabs.get(tabID);
  return { success: true, url: tab.url, title: tab.title };
}

async function cmdClick(params, tabID) {
  const ref = params.ref;
  if (!ref) throw new Error('ref is required');
  const result = await sendToContent(tabID, { action: 'click', ref });
  return result;
}

async function cmdType(params, tabID) {
  const { ref, text, clear } = params;
  if (!ref || text == null) throw new Error('ref and text are required');
  return await sendToContent(tabID, { action: 'type', ref, text, clear: !!clear });
}

async function cmdPressKey(params, tabID) {
  const { key } = params;
  if (!key) throw new Error('key is required');
  return await sendToContent(tabID, { action: 'press_key', key });
}

async function cmdScroll(params, tabID) {
  const { direction } = params;
  const amount = params.amount || 'page';
  return await sendToContent(tabID, { action: 'scroll', direction, amount });
}

async function cmdHover(params, tabID) {
  const { ref } = params;
  if (!ref) throw new Error('ref is required');
  return await sendToContent(tabID, { action: 'hover', ref });
}

async function cmdSelectOption(params, tabID) {
  const { ref, values } = params;
  if (!ref || !values) throw new Error('ref and values are required');
  return await sendToContent(tabID, { action: 'select_option', ref, values });
}

async function cmdEvaluate(params, tabID) {
  const { expression } = params;
  if (!expression) throw new Error('expression is required');
  const results = await chrome.scripting.executeScript({
    target: { tabId: tabID },
    world: "ISOLATED",
    func: (expr) => {
      try {
        const fn = new Function('return (' + expr + ')');
        const r = fn();
        if (r == null) return { success: true, result: String(r) };
        if (typeof r === 'object' && !(r instanceof HTMLElement)) {
          return { success: true, result: JSON.stringify(r, null, 2) };
        }
        return { success: true, result: String(r) };
      } catch (e) {
        return { success: false, error: e.message };
      }
    },
    args: [expression],
  });
  return results[0].result;
}

async function cmdExtract(params, tabID) {
  const results = await chrome.scripting.executeScript({
    target: { tabId: tabID },
    world: "ISOLATED",
    func: () => {
      const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
      const lines = [];
      let node;
      while ((node = walker.nextNode())) {
        const text = node.textContent.trim();
        if (!text) continue;
        const parent = node.parentElement;
        if (!parent) continue;
        const rect = parent.getBoundingClientRect();
        if (rect.width === 0 && rect.height === 0) continue;
        const style = getComputedStyle(parent);
        if (style.display === 'none' || style.visibility === 'hidden' || style.opacity === '0') continue;
        lines.push(text.slice(0, 300));
        if (lines.length >= 300) break;
      }
      return {
        url: location.href,
        title: document.title,
        visible_text: lines.join('\n'),
      };
    },
  });
  return results[0].result;
}

async function cmdSnapshot(params, tabID) {
  return await sendToContent(tabID, { action: 'snapshot' });
}

async function cmdScreenshot(params, tabID) {
  // captureVisibleTab only captures the visible tab of a window.
  // Activate the preferred/target tab first so multi-tab control is correct.
  try {
    const tab = await chrome.tabs.get(tabID);
    if (!tab.active) {
      await chrome.tabs.update(tabID, { active: true });
      await new Promise(r => setTimeout(r, 150));
    }
  } catch (e) {
    throw new Error('screenshot target tab invalid: ' + (e.message || e));
  }
  const quality = 80;
  const dataUrl = await chrome.tabs.captureVisibleTab(null, {
    format: 'jpeg',
    quality,
  });
  return { image: dataUrl, tab_id: tabID };
}

async function cmdFind(params, tabID) {
  const { text, regex } = params;
  if (!text && !regex) throw new Error('text or regex is required');
  return await sendToContent(tabID, { action: 'find', text, regex });
}

async function cmdDrag(params, tabID) {
  const { start_ref, end_ref } = params;
  if (!start_ref || !end_ref) throw new Error('start_ref and end_ref are required');
  return await sendToContent(tabID, { action: 'drag', startRef: start_ref, endRef: end_ref });
}

async function cmdFileUpload(params, tabID) {
  const { paths } = params;
  if (!paths || !paths.length) throw new Error('paths is required');
  return await sendToContent(tabID, { action: 'file_upload', paths });
}

async function cmdTabs(params) {
  const { action } = params;
  switch (action) {
    case 'list': {
      const tabs = await chrome.tabs.query({});
      // Ensure preferred still exists.
      if (preferredTabId != null) {
        try { await chrome.tabs.get(preferredTabId); }
        catch { preferredTabId = null; }
      }
      return {
        preferred_tab_id: preferredTabId,
        tabs: tabs.map(t => ({
          id: t.id,
          index: t.index,
          window_id: t.windowId,
          title: t.title,
          url: t.url,
          active: t.active,
          preferred: preferredTabId != null && t.id === preferredTabId,
        })),
      };
    }
    case 'new': {
      const tab = await chrome.tabs.create({ url: params.url || 'about:blank' });
      preferredTabId = tab.id;
      return { index: tab.index, id: tab.id, preferred_tab_id: preferredTabId };
    }
    case 'close': {
      const target = await resolveTabFromParams(params);
      const closedId = target.id;
      await chrome.tabs.remove(closedId);
      if (preferredTabId === closedId) preferredTabId = null;
      return { success: true, closed_id: closedId, preferred_tab_id: preferredTabId };
    }
    case 'select': {
      const target = await resolveTabFromParams(params);
      await chrome.tabs.update(target.id, { active: true });
      preferredTabId = target.id;
      return {
        success: true,
        id: target.id,
        index: target.index,
        title: target.title,
        url: target.url,
        preferred_tab_id: preferredTabId,
      };
    }
    default:
      throw new Error('unknown tabs action: ' + action);
  }
}

// Resolve a tab by tab_id, index, or preferred/active fallback.
async function resolveTabFromParams(params) {
  if (params.tab_id != null) {
    const id = Number(params.tab_id);
    const tab = await chrome.tabs.get(id);
    if (!tab) throw new Error('tab not found: ' + id);
    return tab;
  }
  if (params.index != null) {
    const tabs = await chrome.tabs.query({});
    const target = tabs.find(t => t.index === params.index);
    if (!target) throw new Error('tab not found at index ' + params.index);
    return target;
  }
  // Default to preferred/active target.
  const id = await getTargetTabID(params);
  return await chrome.tabs.get(id);
}

// --- Content script bridge ---

function sendToContent(tabID, message) {
  return new Promise((resolve, reject) => {
    chrome.tabs.sendMessage(tabID, message, (response) => {
      if (chrome.runtime.lastError) {
        reject(new Error(chrome.runtime.lastError.message));
        return;
      }
      if (!response) {
        reject(new Error('no response from content script (reload page)'));
        return;
      }
      if (response.error) {
        reject(new Error(response.error));
      } else {
        resolve(response);
      }
    });
  });
}

// --- Extension message handling (from popup) ---

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  dbg('Received message:', msg.type);
  
  if (msg.type === 'get-status') {
    const connected = !!(ws && ws.readyState === WebSocket.OPEN);
    const response = {
      connected,
      browserID,
      preferredTabId,
      serverURL: null,
      reconnecting: !connected && !!reconnectTimer,
      disabled: permanentFailure,
      updateRequired,
      updateMessage,
      lastStatusCode,
      url: null, // will be filled from storage
    };
    dbg('get-status response:', JSON.stringify(response));
    sendResponse(response);
    return true;
  }
  
  if (msg.type === 'set-server-url') {
    dbg('set-server-url:', msg.url);
    (async () => {
      await setServerURL(msg.url);
      dbg('URL saved to storage');
      permanentFailure = false;
      lastStatusCode = 0;
      disconnect();
      dbg('Calling connect() after disconnect');
      connect();
      sendResponse({ ok: true });
    })();
    return true;
  }
  
  if (msg.type === 'reconnect') {
    dbg('reconnect requested');
    permanentFailure = false;
    lastStatusCode = 0;
    disconnect();
    dbg('Calling connect() after disconnect');
    connect();
    sendResponse({ ok: true });
    return true;
  }
});


// Clear preferred target when the controlled tab is closed.
chrome.tabs.onRemoved.addListener((tabId) => {
  if (preferredTabId === tabId) {
    preferredTabId = null;
  }
});
// --- Auto-connect on service worker start ---
// Chrome MV3 wakes the service worker on events; we reconnect on startup.
connect();

// Keep service worker alive for the WebSocket connection.
chrome.alarms.create('keepalive', { periodInMinutes: 0.45 });
chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === 'keepalive') {
    // Reconnect if we're dropped
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      connect();
    }
  }
});
