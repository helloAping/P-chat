/** Shared project-path helpers for add-project forms and drag-drop. */

export function isAbsolutePath(path: string): boolean {
  const value = path.trim()
  return /^[a-zA-Z]:[\\/]/.test(value) || value.startsWith('\\\\') || value.startsWith('/')
}

export function normalizeProjectPath(path: string): string {
  return path.trim().replace(/[\\/]+$/, '').replace(/\//g, '\\').toLowerCase()
}

export function sameProjectPath(a: string, b: string): boolean {
  return normalizeProjectPath(a) === normalizeProjectPath(b)
}

export function basenameFromPath(path: string): string {
  const parts = path.trim().split(/[\\/]+/).filter(Boolean)
  return parts[parts.length - 1] || '新项目'
}

/** Chromium / WebView2 often expose the absolute path on local File drops. */
export function absolutePathFromFile(file: File | null | undefined): string {
  if (!file) return ''
  const anyFile = file as File & { path?: string }
  const path = (anyFile.path || '').trim()
  return isAbsolutePath(path) ? path : ''
}
