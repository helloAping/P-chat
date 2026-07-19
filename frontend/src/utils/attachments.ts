// attachments.ts — resolve MessageAttachment.url variants to a
// canonical "data:" URL for export.
//
// Why this exists: the chat store swaps base64 data URLs out
// for blob: object URLs at runtime (see stores/chat.ts
// convertAndStripScreenshots) to keep WebView2's DOM cache
// small. blob: URLs are scoped to the current page and die
// when the user switches sessions or refreshes, so they
// can't be carried into an exported markdown file. The
// export pipeline normalises every variant back to a data:
// URL right before rendering, so the resulting .md is fully
// self-contained.
//
// Public surface:
//   - isDataURL / isBlobURL: cheap string predicates
//   - blobToDataURL: browser-only fetch + FileReader
//   - resolveAttachments: bulk-resolve a MessageAttachment[]
//     into ResolvedAttachment[], tolerating per-item failures
//   - isPlaceholderScreenshot: detect the "[截图已省略]" string
//     the store inserts when an attachment is evicted to save
//     memory; export should render a marker, not the literal
//     placeholder text.

import type { MessageAttachment } from '../api/client.ts'

/** The literal string the chat store inserts when an
 *  attachment has been evicted from the in-memory cache to
 *  free up space. Exports should render a brief marker
 *  ("截图已省略") rather than dumping the literal text into
 *  the markdown. */
export const PLACEHOLDER_SCREENSHOT = '[截图已省略]'

export interface ResolvedAttachment {
  /** Always a data: URL on success. Falls back to the
   *  original URL (blob: or https:) on fetch failure so
   *  the export still emits *something* and downstream
   *  tooling can detect the broken link. */
  url: string
  name: string
  mime: string
  kind: string
  type: MessageAttachment['type']
  /** True when the original URL was a blob: that we
   *  successfully converted. Lets callers skip the
   *  conversion on a second pass (idempotent). */
  resolvedFromBlob: boolean
}

export function isDataURL(url: string | undefined | null): boolean {
  return !!url && url.startsWith('data:')
}

export function isBlobURL(url: string | undefined | null): boolean {
  return !!url && url.startsWith('blob:')
}

export function isPlaceholderScreenshot(url: string | undefined | null): boolean {
  return url === PLACEHOLDER_SCREENSHOT
}

/** Fetch a blob: URL and read it back as a data: URL.
 *  Browser-only (relies on FileReader + URL.createObjectURL
 *  resolution). Returns the empty string on failure; the
 *  caller should keep the original URL in that case so the
 *  export still surfaces the (broken) link. */
export async function blobToDataURL(blobURL: string): Promise<string> {
  const resp = await fetch(blobURL)
  if (!resp.ok) return ''
  const blob = await resp.blob()
  return new Promise<string>((resolve) => {
    const reader = new FileReader()
    reader.onload = () => resolve(typeof reader.result === 'string' ? reader.result : '')
    reader.onerror = () => resolve('')
    reader.readAsDataURL(blob)
  })
}

/** Resolve a single MessageAttachment to a ResolvedAttachment.
 *  - data: URLs pass through untouched
 *  - blob: URLs are converted to data: URLs (resolvedFromBlob = true)
 *  - https:// and unknown schemes: kept verbatim (resolvedFromBlob = false)
 *  - placeholders ([截图已省略]): kept verbatim so the markdown
 *    renderer can special-case them later
 *  - text attachments: pass through with the text in `url` so
 *    the renderer can read it consistently
 *
 *  Errors are swallowed: a single broken attachment must not
 *  fail the whole export. */
export async function resolveAttachment(att: MessageAttachment): Promise<ResolvedAttachment> {
  const base = {
    name: att.name || '',
    mime: att.mime || '',
    kind: att.kind || 'file',
    type: att.type,
    resolvedFromBlob: false,
  }
  if (att.type === 'text') {
    return { ...base, url: att.text || '' }
  }
  const u = att.url
  if (!u) return { ...base, url: '' }
  if (isPlaceholderScreenshot(u)) return { ...base, url: u }
  if (isDataURL(u)) return { ...base, url: u }
  if (isBlobURL(u)) {
    const data = await blobToDataURL(u)
    if (data) return { ...base, url: data, resolvedFromBlob: true }
    return { ...base, url: u }
  }
  return { ...base, url: u }
}

/** Bulk resolver. Iterates sequentially (a single bad blob
 *  fetch shouldn't be amplified into N parallel hangs) but
 *  bounded by the input length so it always terminates.
 *  Empty input → empty output. */
export async function resolveAttachments(atts: MessageAttachment[] | undefined | null): Promise<ResolvedAttachment[]> {
  if (!atts || atts.length === 0) return []
  const out: ResolvedAttachment[] = []
  for (const a of atts) {
    out.push(await resolveAttachment(a))
  }
  return out
}
