export interface UpdateInfo {
  current: string
  latest: string
  hasUpdate: boolean
  url: string
  body: string
  publishedAt: string
}

let cached: UpdateInfo | null = null
let checking = false

export async function checkUpdate(): Promise<UpdateInfo | null> {
  if (cached) return cached
  if (checking) return null
  checking = true
  try {
    const res = await fetch(
      `https://api.github.com/repos/${__GITHUB_REPO__}/releases/latest`,
      { headers: { Accept: 'application/vnd.github+json' } },
    )
    if (!res.ok) return null
    const release = await res.json()
    const latest = (release.tag_name || '').replace(/^v/, '')
    const current = __APP_VERSION__
    const hasUpdate = compareVersions(latest, current) > 0
    cached = {
      current,
      latest: release.tag_name || latest,
      hasUpdate,
      url: release.html_url || `https://github.com/${__GITHUB_REPO__}/releases`,
      body: (release.body || '').slice(0, 500),
      publishedAt: release.published_at || '',
    }
    return cached
  } catch {
    return null
  } finally {
    checking = false
  }
}

function compareVersions(a: string, b: string): number {
  const ap = a.split('.').map(Number)
  const bp = b.split('.').map(Number)
  for (let i = 0; i < Math.max(ap.length, bp.length); i++) {
    const av = ap[i] || 0
    const bv = bp[i] || 0
    if (av > bv) return 1
    if (av < bv) return -1
  }
  return 0
}
