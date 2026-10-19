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
  channel: string
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
  channel?: string
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

const cached = new Map<boolean, UpdateInfo>()
const checking = new Map<boolean, Promise<UpdateInfo | null>>()

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
    channel: raw.channel || 'stable',
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
  cached.clear()
}

export async function checkUpdate(force = false, includePrerelease = false): Promise<UpdateInfo | null> {
  if (!force && cached.has(includePrerelease)) return cached.get(includePrerelease) || null
  const pending = checking.get(includePrerelease)
  if (pending) return pending

  const request = (async () => {
    try {
      const base = await waitForDirectBackend()
      const query = includePrerelease ? '?include_prerelease=1' : ''
      const res = await fetch(`${base}/api/v1/updates/check${query}`, { headers: { Accept: 'application/json' } })
      if (!res.ok) return null
      const info = normalizeUpdateInfo(await res.json())
      cached.set(includePrerelease, info)
      return info
    } catch {
      return null
    } finally {
      checking.delete(includePrerelease)
    }
  })()
  checking.set(includePrerelease, request)
  return request
}

export async function downloadUpdate(includePrerelease: boolean, expectedVersion: string): Promise<UpdateDownloadResult> {
  if (!expectedVersion) throw new Error('请先检查更新版本')
  const base = await waitForDirectBackend()
  const res = await fetch(`${base}/api/v1/updates/download`, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({ include_prerelease: includePrerelease, expected_version: expectedVersion }),
  })
  if (res.status === 409) throw new UpdateVersionChangedError(await readError(res))
  if (!res.ok) throw new Error(await readError(res))

  const result = normalizeDownloadResult(await res.json())
  cached.set(includePrerelease, result)
  return result
}

export class UpdateVersionChangedError extends Error {}

export async function installDownloadedUpdate(update: Pick<UpdateDownloadResult, 'downloadPath' | 'sha256'>): Promise<void> {
  if (!update.downloadPath) throw new Error('更新包路径为空')
  const { InstallUpdate } = await import('../../wailsjs/go/main/App')
  await InstallUpdate(update.downloadPath, update.sha256 || '')
}
