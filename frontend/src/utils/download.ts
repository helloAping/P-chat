/**
 * Desktop-friendly download helpers.
 *
 * Browser `<a download>` triggers WebView2's built-in downloads
 * flyout — we cannot style that chrome. On Wails we write via
 * SaveDownloadFile (native save dialog) and surface a compact
 * in-app dock instead.
 */

import { useDownloadDock } from '../composables/useDownloadDock'

function basename(path: string): string {
  const parts = path.split(/[/\\]/)
  return parts[parts.length - 1] || path
}

async function blobToBase64(blob: Blob): Promise<string> {
  const buffer = await blob.arrayBuffer()
  const bytes = new Uint8Array(buffer)
  const chunk = 0x8000
  let binary = ''
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk))
  }
  return btoa(binary)
}

function browserDownloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  setTimeout(() => URL.revokeObjectURL(url), 0)
}

function browserDownloadUrl(url: string, filename: string): void {
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
}

/**
 * Save a blob. Returns the saved path on desktop, "" on browser
 * fallback, or null when the user cancelled the native dialog.
 */
export async function saveBlob(blob: Blob, filename: string): Promise<string | null> {
  try {
    const b64 = await blobToBase64(blob)
    const { SaveDownloadFile } = await import('../../wailsjs/go/main/App')
    const path = await SaveDownloadFile(filename, b64)
    if (path) {
      useDownloadDock().pushDownload(basename(path) || filename, path)
      return path
    }
    return null
  } catch {
    browserDownloadBlob(blob, filename)
    return ''
  }
}

export async function saveFromUrl(url: string, filename: string): Promise<string | null> {
  try {
    const res = await fetch(url)
    if (!res.ok) throw new Error(`fetch ${res.status}`)
    const blob = await res.blob()
    return saveBlob(blob, filename)
  } catch {
    browserDownloadUrl(url, filename)
    return ''
  }
}

/** Reveal a saved path in the OS file manager (best-effort). */
export async function revealInFolder(path: string): Promise<void> {
  try {
    const { OpenExplorer } = await import('../../wailsjs/go/main/App')
    await OpenExplorer(path)
  } catch {
    // Browser / binding missing — no-op.
  }
}

/** Legacy sync wrappers used by existing call sites. */
export function downloadBlob(blob: Blob, filename: string): void {
  void saveBlob(blob, filename)
}

export function downloadFromUrl(url: string, filename: string): void {
  void saveFromUrl(url, filename)
}
