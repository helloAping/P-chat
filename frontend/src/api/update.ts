const BASE = ''

export const SOFTWARE_RELEASE_PAGE = 'http://www.08ms.cn/software/p-chat/'

export interface UpdateArtifact {
  artifact_id?: number
  kind?: string
  platform?: string
  arch?: string
  from_version?: string
  version?: string
  file_name?: string
  url?: string
  size?: number
  sha256?: string
}

export interface UpdateInfo {
  current: string
  latest: string
  hasUpdate: boolean
  installable: boolean
  url: string
  body: string
  publishedAt: string
  artifact?: UpdateArtifact
  full?: UpdateArtifact
  patch?: UpdateArtifact
}

export interface UpdateDownloadResult extends UpdateInfo {
  downloadPath: string
  fileName: string
  size: number
  sha256: string
}

interface RawUpdateInfo {
  current?: string
  latest?: string
  has_update?: boolean
  installable?: boolean
  url?: string
  release_notes?: string
  published_at?: string
  artifact?: UpdateArtifact
  full?: UpdateArtifact
  patch?: UpdateArtifact
}

interface RawDownloadResult extends RawUpdateInfo {
  download_path?: string
  file_name?: string
  size?: number
  sha256?: string
}

let cached: UpdateInfo | null = null
let checking = false

function directBackendURL(): string {
  if (typeof window === 'undefined') return BASE
  const injected = (window as any).__PCHAT_BACKEND__
  if (typeof injected === 'string' && injected) return injected
  return BASE
}

async function waitForDirectBackend(): Promise<string> {
  const initial = directBackendURL()
  if (initial && initial !== BASE) return initial
  if (typeof window === 'undefined') return BASE

  const getBackendURL = (window as any).go?.main?.App?.GetBackendURL
  if (typeof getBackendURL !== 'function') return initial

  for (let i = 0; i < 50; i++) {
    const url = directBackendURL()
    if (url && url !== BASE) return url
    try {
      const resolved = await getBackendURL()
      if (typeof resolved === 'string' && resolved) return resolved
    } catch {
      return directBackendURL()
    }
    await new Promise<void>(resolve => setTimeout(resolve, 100))
  }
  return directBackendURL()
}

function normalizeUpdateInfo(raw: RawUpdateInfo): UpdateInfo {
  const artifact = raw.artifact
  return {
    current: raw.current || __APP_VERSION__,
    latest: raw.latest || __APP_VERSION__,
    hasUpdate: !!raw.has_update,
    installable: !!raw.installable,
    url: artifact?.url || raw.patch?.url || raw.full?.url || raw.url || '',
    body: raw.release_notes || '',
    publishedAt: raw.published_at || '',
    artifact,
    full: raw.full,
    patch: raw.patch,
  }
}

function normalizeDownloadResult(raw: RawDownloadResult): UpdateDownloadResult {
  return {
    ...normalizeUpdateInfo(raw),
    downloadPath: raw.download_path || '',
    fileName: raw.file_name || '',
    size: raw.size || 0,
    sha256: raw.sha256 || raw.artifact?.sha256 || '',
  }
}

async function readError(res: Response): Promise<string> {
  try {
    const text = await res.text()
    if (text) {
      try {
        const raw = JSON.parse(text)
        if (typeof raw?.error === 'string') return raw.error
      } catch {
        return text
      }
    }
  } catch {
    // ignore
  }
  return `${res.status} ${res.statusText}`.trim()
}

export function clearUpdateCache() {
  cached = null
}

export async function checkUpdate(force = false): Promise<UpdateInfo | null> {
  if (cached && !force) return cached
  if (checking) return null
  checking = true
  try {
    const base = await waitForDirectBackend()
    const res = await fetch(`${base}/api/v1/updates/check`, { headers: { Accept: 'application/json' } })
    if (!res.ok) return null
    cached = normalizeUpdateInfo(await res.json())
    return cached
  } catch {
    return null
  } finally {
    checking = false
  }
}

export async function downloadUpdate(): Promise<UpdateDownloadResult> {
  const base = await waitForDirectBackend()
  const res = await fetch(`${base}/api/v1/updates/download`, {
    method: 'POST',
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) throw new Error(await readError(res))

  const result = normalizeDownloadResult(await res.json())
  cached = result
  return result
}

export async function installDownloadedUpdate(update: Pick<UpdateDownloadResult, 'downloadPath' | 'sha256'>): Promise<void> {
  if (!update.downloadPath) throw new Error('更新包路径为空')
  const { InstallUpdate } = await import('../../wailsjs/go/main/App')
  await InstallUpdate(update.downloadPath, update.sha256 || '')
}
