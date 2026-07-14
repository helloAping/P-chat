// popup.js — Extension popup UI logic

const statusRow = document.getElementById('status-row');
const statusEl = document.getElementById('status');
const serverURLEl = document.getElementById('server-url');
const browserIDEl = document.getElementById('browser-id');
const reconnectBtn = document.getElementById('reconnect-btn');
const saveBtn = document.getElementById('save-btn');
const detectBtn = document.getElementById('detect-btn');

async function init() {
  const data = await chrome.storage.local.get(['serverURL', 'browserID']);
  if (data.browserID) {
    browserIDEl.textContent = data.browserID;
  }
  serverURLEl.value = data.serverURL || '';
  // If no URL stored yet, try to auto-detect
  if (!data.serverURL) {
    await detectServer();
  }
  refreshStatus();
}

// Auto-detect the server by probing /health on common ports.
async function detectServer() {
  const ports = [8960, 14712, 9960, 18960, 8961, 8962];
  statusEl.textContent = '探测中…';
  statusRow.className = 'row status-reconnecting';

  for (const port of ports) {
    try {
      const resp = await fetch(`http://127.0.0.1:${port}/api/v1/health`, {
        signal: AbortSignal.timeout(1000),
      });
      if (resp.ok) {
        const url = `http://127.0.0.1:${port}`;
        serverURLEl.value = url;
        await chrome.storage.local.set({ serverURL: url });
        statusEl.textContent = `发现服务器: ${port}`;
        statusRow.className = 'row status-connected';
        return;
      }
    } catch { /* try next */ }
  }
  statusEl.textContent = '未找到服务器，请手动输入地址';
  statusRow.className = 'row status-disconnected';
  serverURLEl.value = 'http://127.0.0.1:8960';
}

async function refreshStatus() {
  chrome.runtime.sendMessage({ type: 'get-status' }, (resp) => {
    statusRow.className = 'row status-unknown';
    if (!resp) {
      statusEl.textContent = '未知';
      return;
    }
    if (resp.connected) {
      statusEl.textContent = '已连接';
      statusRow.className = 'row status-connected';
      if (resp.browserID) browserIDEl.textContent = resp.browserID;
    } else if (resp.disabled || (resp.lastStatusCode >= 4000)) {
      const code = resp.lastStatusCode || '';
      statusEl.textContent = code === 403
        ? '服务器未启用浏览器控制'
        : `连接被拒绝 (code: ${code})`;
      statusRow.className = 'row status-disabled';
    } else if (resp.reconnecting) {
      statusEl.textContent = resp.lastStatusCode === 1006
        ? '无法连接服务器，正在重连…'
        : '重连中…';
      statusRow.className = 'row status-reconnecting';
    } else if (resp.lastStatusCode === 1006) {
      statusEl.textContent = '无法连接服务器';
      statusRow.className = 'row status-disconnected';
    } else {
      statusEl.textContent = '未连接';
      statusRow.className = 'row status-disconnected';
    }
  });
}

reconnectBtn.addEventListener('click', () => {
  chrome.runtime.sendMessage({ type: 'reconnect' }, () => {
    setTimeout(refreshStatus, 1500);
  });
});

saveBtn.addEventListener('click', () => {
  const url = serverURLEl.value.trim();
  if (!url) return;
  chrome.runtime.sendMessage({ type: 'set-server-url', url }, () => {
    setTimeout(refreshStatus, 1500);
  });
});

if (detectBtn) {
  detectBtn.addEventListener('click', async () => {
    await detectServer();
    refreshStatus();
  });
}

chrome.runtime.onMessage.addListener((msg) => {
  if (msg.type === 'status-update') {
    refreshStatus();
  }
});

init();

