// Clipboard + download helpers shared by MessageBubble and the
// code-block / attachment toolbars. Centralised so the
// per-attachment kind switch (image vs audio vs file) lives in
// one place and the various copy/download buttons in the UI all
// behave the same way.
//
// Browser notes:
//   - navigator.clipboard.write / writeText require a secure
//     context (https / localhost / file://). In the Wails
//     desktop app the webview origin is `wails.localhost`,
//     which Chromium treats as a secure context — the API
//     works there. In a plain http://127.0.0.1:port dev server
//     the API is also available because loopback is a
//     trustworthy origin.
//   - Image clipboard write uses ClipboardItem with a PNG
//     blob. Audio/video fall back to download only —
//     browsers don't expose audio/video to the clipboard
//     API in a useful way.
//   - File download is implemented with a synthetic <a
//     download> + click + revokeObjectURL. Works for any
//     blob: / data: URL.

// copyText copies a plain text string to the system
// clipboard. Falls back to a temporary <textarea> + execCommand
// when the async Clipboard API is unavailable (older
// WebViews, non-secure contexts).
export async function copyText(text: string): Promise<boolean> {
  if (!text) return false
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch { /* fall through to legacy path */ }

  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.top = '0'
    ta.style.left = '0'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}

// copyImageToClipboard copies a binary image to the
// system clipboard. The image bytes are passed as a Blob
// (typically image/png or image/jpeg). Returns true on
// success. Returns false if the browser refuses — e.g. the
// ClipboardItem image type isn't supported on the
// platform. Callers should fall back to downloadImage() in
// that case.
export async function copyImageToClipboard(blob: Blob): Promise<boolean> {
  try {
    // ClipboardItem is the modern API; the Wails WebView2
    // build (Chromium-based) supports it.
    // @ts-ignore — ClipboardItem is in lib.dom but the
    // generic ClipboardItem ctor + write promise type is
    // missing in some configs.
    const item = new (window.ClipboardItem || (window as any).ClipboardItem)({
      [blob.type || 'image/png']: blob,
    })
    // @ts-ignore — see above.
    await navigator.clipboard.write([item])
    return true
  } catch {
    return false
  }
}

// downloadBlob triggers a browser download for the given
// blob. `filename` is what the user sees in their downloads
// folder; we synthesise the extension from `mime` when the
// caller doesn't supply one.
export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  // Revoke the object URL on the next tick. Synchronous
  // revoke can race with the browser's download dispatch
  // on some engines.
  setTimeout(() => URL.revokeObjectURL(url), 0)
}

// downloadFromUrl is the data-URL equivalent of
// downloadBlob. Use it for already-base64 payloads (the
// chat attachments are mostly data: URLs).
export function downloadFromUrl(url: string, filename: string): void {
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
}

// fetchAsBlob fetches a URL and resolves to a Blob. Used
// to materialise data: URLs into a Blob when the caller
// wants to call the Clipboard API (which needs a Blob
// rather than a string).
export async function fetchAsBlob(url: string): Promise<Blob> {
  const res = await fetch(url)
  if (!res.ok) throw new Error(`fetch ${url}: ${res.status}`)
  return res.blob()
}

// extensionForMime returns a sensible file extension for
// the given MIME type, falling back to `.bin` for unknown
// types. Used by the download path when the caller doesn't
// have a filename handy.
export function extensionForMime(mime: string): string {
  switch (mime) {
    case 'image/png': return '.png'
    case 'image/jpeg': return '.jpg'
    case 'image/gif': return '.gif'
    case 'image/webp': return '.webp'
    case 'image/bmp': return '.bmp'
    case 'image/svg+xml': return '.svg'
    case 'audio/mpeg': return '.mp3'
    case 'audio/wav': return '.wav'
    case 'audio/ogg': return '.ogg'
    case 'audio/mp4': return '.m4a'
    case 'audio/flac': return '.flac'
    case 'video/mp4': return '.mp4'
    case 'video/webm': return '.webm'
    case 'video/quicktime': return '.mov'
    case 'video/x-matroska': return '.mkv'
    case 'text/plain': return '.txt'
    case 'text/markdown': return '.md'
    case 'text/html': return '.html'
    case 'application/json': return '.json'
    case 'application/pdf': return '.pdf'
    default: return '.bin'
  }
}
