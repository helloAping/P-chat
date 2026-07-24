// export.ts — frontend export utilities. The actual
// rendering moved to pchat-server (see
// internal/export + the GET /api/v1/sessions/:id/export
// HTTP handler). This file keeps two helpers the UI
// still needs:
//
//   - suggestFilename: build a download filename from
//     a session title + format
//   - dedupeFilename: append -2, -3, ... to avoid
//     rapid double-click collisions
//
// The previous version of this file held the in-browser
// markdown / JSON / HTML renderers. Those were removed
// when the export path moved server-side: the SPA can
// no longer see the raw attachment bytes (the chat store
// swaps base64 data URLs out for blob: object URLs at
// runtime, which die when the user switches sessions
// or refreshes), so client-side rendering produced
// incomplete / broken output. The server reads straight
// from the memory store and returns a self-contained
// file.

export type ExportFormat = 'markdown' | 'json'

/** Suggest a filename for the download based on session
 *  title + format. Strips characters that some
 *  filesystems (NTFS, APFS) reject. */
export function suggestFilename(
  sessionTitle: string,
  format: ExportFormat,
): string {
  const safe = (sessionTitle || 'pchat-session')
    .replace(/[\\/:*?"<>|]/g, '-')
    .replace(/\s+/g, '-')
    .slice(0, 60)
  const stamp = new Date().toISOString().slice(0, 10)
  return `${safe}-${stamp}.${format === 'markdown' ? 'md' : format}`
}

/** Make a unique filename by appending -2, -3, ... when
 *  the candidate already exists. Exposed so callers can
 *  dedup against a directory listing without
 *  re-implementing the loop. Returns `candidate`
 *  unchanged when the path is free. */
export function dedupeFilename(candidate: string, exists: (path: string) => boolean): string {
  if (!exists(candidate)) return candidate
  const dot = candidate.lastIndexOf('.')
  const stem = dot > 0 ? candidate.slice(0, dot) : candidate
  const ext = dot > 0 ? candidate.slice(dot) : ''
  for (let n = 2; n < 1000; n++) {
    const next = `${stem}-${n}${ext}`
    if (!exists(next)) return next
  }
  // Pathological: 1000 collisions. Fall back to a
  // timestamp suffix rather than throwing — the user
  // still gets a downloadable file.
  return `${stem}-${Date.now()}${ext}`
}
