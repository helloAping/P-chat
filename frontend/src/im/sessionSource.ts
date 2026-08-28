export interface SessionSource {
  platform: string
  label: string
}

const SOURCE_LABELS: Record<string, string> = {
  wechat: '微信',
  wecom: '企微',
  work_wechat: '企微',
  enterprise_wechat: '企微',
  feishu: '飞书',
  lark: '飞书',
  telegram: 'TG',
  qq: 'QQ',
}

export function sessionSourceFromID(id: string): SessionSource | null {
  const parts = id.split(':')
  if (parts.length < 2 || parts[0] !== 'im') return null
  const platform = parts[1].trim().toLowerCase()
  if (!platform) return null
  return {
    platform,
    label: SOURCE_LABELS[platform] || platform,
  }
}

export function displaySessionTitle(title: string, id: string): string {
  const value = title.trim() || '(无标题)'
  const source = sessionSourceFromID(id)
  if (!source) return value
  const stripped = stripSourcePrefix(value, source.label)
  return stripped || value
}

function stripSourcePrefix(value: string, label: string): string {
  for (const prefix of [`${label} · `, `${label} `, `${label}:`, `${label}：`, `${label}-`, `${label}_`]) {
    if (value.startsWith(prefix)) return value.slice(prefix.length).trim()
  }
  return value
}
