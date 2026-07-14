// content.js — P-Chat content script
//
// Runs in every page. Listens for messages from background.js and
// performs DOM operations (click, type, scroll, snapshot, etc.).
// Maintains a ref-to-element mapping for the current page state.

const REF_ATTR = 'data-pchat-ref';
let refCounter = 0;

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  handleAction(msg)
    .then(r => sendResponse(r))
    .catch(e => sendResponse({ error: e.message || String(e) }));
  return true; // keep channel open for async
});

async function handleAction(msg) {
  switch (msg.action) {
    case 'click':        return doClick(msg.ref);
    case 'type':         return doType(msg.ref, msg.text, msg.clear);
    case 'press_key':    return doPressKey(msg.key);
    case 'scroll':       return doScroll(msg.direction, msg.amount);
    case 'hover':        return doHover(msg.ref);
    case 'select_option': return doSelectOption(msg.ref, msg.values);
    case 'evaluate':     return doEvaluate(msg.expression, msg.ref);
    case 'snapshot':     return doSnapshot();
    case 'find':         return doFind(msg.text, msg.regex);
    case 'drag':         return doDrag(msg.startRef, msg.endRef);
    case 'file_upload':  return doFileUpload(msg.paths);
    default: throw new Error('unknown action: ' + msg.action);
  }
}

function findByRef(ref) {
  return document.querySelector(`[${REF_ATTR}="${ref}"]`);
}

function ensureRef(el) {
  let ref = el.getAttribute(REF_ATTR);
  if (!ref) {
    ref = `e${++refCounter}`;
    el.setAttribute(REF_ATTR, ref);
  }
  return ref;
}

// --- Actions ---

function doClick(ref) {
  const el = findByRef(ref);
  if (!el) return { success: false, error: `element ${ref} not found` };
  el.scrollIntoView({ block: 'center', behavior: 'instant' });
  el.click();
  return { success: true, tag: el.tagName.toLowerCase(), text: (el.textContent || '').trim().slice(0, 80) };
}

function doType(ref, text, clear) {
  const el = findByRef(ref);
  if (!el) return { success: false, error: `element ${ref} not found` };
  el.scrollIntoView({ block: 'center', behavior: 'instant' });
  el.focus();
  if (clear) {
    el.value = '';
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }
  el.value = (el.value || '') + text;
  el.dispatchEvent(new Event('input', { bubbles: true }));
  el.dispatchEvent(new Event('change', { bubbles: true }));
  return { success: true };
}

function doPressKey(key) {
  const keyCode = key.length === 1 ? key.toUpperCase().charCodeAt(0) : 0;
  const init = { key, code: 'Key' + key.toUpperCase(), keyCode, bubbles: true };
  document.activeElement.dispatchEvent(new KeyboardEvent('keydown', init));
  document.activeElement.dispatchEvent(new KeyboardEvent('keyup', init));
  return { success: true, key };
}

function doScroll(direction, amount) {
  const factor = amount === 'half' ? 0.5 : 1;
  const delta = window.innerHeight * factor * (direction === 'up' ? -1 : 1);
  window.scrollBy({ top: delta, behavior: 'smooth' });
  return { success: true };
}

function doHover(ref) {
  const el = findByRef(ref);
  if (!el) return { success: false, error: `element ${ref} not found` };
  el.scrollIntoView({ block: 'center', behavior: 'instant' });
  el.dispatchEvent(new MouseEvent('mouseenter', { bubbles: true }));
  el.dispatchEvent(new MouseEvent('mouseover', { bubbles: true }));
  return { success: true };
}

function doSelectOption(ref, values) {
  const el = findByRef(ref);
  if (!el || el.tagName !== 'SELECT') return { success: false, error: `select element ${ref} not found` };
  const opts = Array.from(el.options);
  values.forEach(v => {
    const opt = opts.find(o => o.value === v);
    if (opt) opt.selected = true;
  });
  el.dispatchEvent(new Event('change', { bubbles: true }));
  return { success: true };
}

function doEvaluate(expression, ref) {
  try {
    const context = ref ? findByRef(ref) : document;
    const fn = new Function('element', 'return (' + expression + ')');
    const result = fn(context);
    return { success: true, result: String(result) };
  } catch (e) {
    return { success: false, error: e.message };
  }
}

function doSnapshot() {
  refCounter = 0;
  const selectors = 'a,button,input,textarea,select,[role="button"],[role="link"],[role="tab"],label';
  const elements = document.querySelectorAll(selectors);
  const nodes = [];
  for (const el of elements) {
    const ref = ensureRef(el);
    const rect = el.getBoundingClientRect();
    if (rect.width === 0 && rect.height === 0) continue;
    nodes.push({
      ref,
      tag: el.tagName.toLowerCase(),
      type: el.getAttribute('type') || '',
      role: el.getAttribute('role') || '',
      text: (el.textContent || '').trim().slice(0, 60),
      placeholder: el.getAttribute('placeholder') || '',
      name: el.getAttribute('name') || '',
      value: el.value != null ? String(el.value).slice(0, 60) : '',
      disabled: !!el.disabled,
      checked: !!el.checked,
    });
  }
  return {
    url: location.href,
    title: document.title,
    nodes,
  };
}

function doFind(text, regex) {
  const re = regex ? new RegExp(regex, 'gi') : null;
  const results = [];
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  let node;
  while ((node = walker.nextNode())) {
    const content = node.nodeValue;
    const match = re ? re.test(content) : (text && content.toLowerCase().includes(text.toLowerCase()));
    if (match) {
      const parent = node.parentElement;
      if (parent) {
        const ref = ensureRef(parent);
        results.push({
          ref,
          tag: parent.tagName.toLowerCase(),
          text: content.trim().slice(0, 100),
        });
      }
    }
    if (results.length >= 50) break;
  }
  return { matches: results };
}

function doDrag(startRef, endRef) {
  const src = findByRef(startRef);
  const dst = findByRef(endRef);
  if (!src || !dst) return { success: false, error: 'start or end element not found' };
  const srcRect = src.getBoundingClientRect();
  const dstRect = dst.getBoundingClientRect();
  const fromX = srcRect.left + srcRect.width / 2;
  const fromY = srcRect.top + srcRect.height / 2;
  const toX = dstRect.left + dstRect.width / 2;
  const toY = dstRect.top + dstRect.height / 2;
  src.dispatchEvent(new MouseEvent('mousedown', { clientX: fromX, clientY: fromY, bubbles: true }));
  document.dispatchEvent(new MouseEvent('mousemove', { clientX: toX, clientY: toY, bubbles: true }));
  document.dispatchEvent(new MouseEvent('mouseup', { clientX: toX, clientY: toY, bubbles: true }));
  return { success: true };
}

function doFileUpload(paths) {
  // File upload requires chrome.fileSystem access (Manifest V3)
  // which is NOT available in standard extensions.
  // Return a descriptive error to the LLM.
  return {
    success: false,
    error: 'File upload from extension is not supported by Chrome Manifest V3 permissions. Use write_file to save files to disk instead.',
  };
}
