// Shared markdown render cache (LRU-ish).
//
// The markdown pipeline (`marked.parse`) is O(text length) and Vue
// re-evaluates `v-html` expressions any time any reactive dep in the
// component ticks. MessageBubble and SubAgentCard both render text parts
// this way, so they share ONE cache keyed by full text content, capped to
// bound memory for very long sessions.
//
// Note: this cache helps *static* parts (same text re-rendered across
// ticks). A *growing* streaming part must NOT route through here per
// delta — that's O(n²) over the stream. Live parts use a textContent
// writer instead (see TypedText / SubAgentCard's live-text path).

import { marked } from 'marked'

const MD_CACHE_MAX = 256
const MD_CACHE_MAX_BYTES = 2 * 1024 * 1024
const MD_CACHE_ENTRY_MAX_BYTES = 64 * 1024
const mdCache = new Map<string, string>()
let mdCacheBytes = 0

// JavaScript strings are UTF-16, so this is a conservative accounting
// approximation for the cache's retained strings.
function cacheCost(text: string, html: string): number {
  return (text.length + html.length) * 2
}

export function renderMarkdown(text: string): string {
  if (!text) return ''
  const cached = mdCache.get(text)
  if (cached !== undefined) {
    // Touch: move to end of Map to mark as recently used.
    mdCache.delete(text)
    mdCache.set(text, cached)
    return cached
  }
  const html = marked.parse(text, { async: false, breaks: true }) as string
  if (cacheCost(text, html) > MD_CACHE_ENTRY_MAX_BYTES) return html
  mdCache.set(text, html)
  mdCacheBytes += cacheCost(text, html)
  while (mdCache.size > MD_CACHE_MAX || mdCacheBytes > MD_CACHE_MAX_BYTES) {
    const oldest = mdCache.keys().next().value
    if (oldest === undefined) break
    const oldestHTML = mdCache.get(oldest)
    if (oldestHTML !== undefined) mdCacheBytes -= cacheCost(oldest, oldestHTML)
    mdCache.delete(oldest)
  }
  return html
}
