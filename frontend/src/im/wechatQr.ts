import type { WeChatQRSession } from '../api/client'

const wechatQRPollPath = '/api/v1/im/wechat/qr/'

export function isWeChatQRImageSource(raw: unknown): raw is string {
  if (typeof raw !== 'string') return false
  const value = raw.trim()
  if (!value) return false

  const lower = value.toLowerCase()
  if (lower.startsWith('data:image/') || lower.startsWith('blob:')) return true

  if (value.startsWith('/')) {
    return !isWeChatQRPollURL(value)
  }

  try {
    const parsed = new URL(value)
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return false
    return !isWeChatQRPollURL(parsed.pathname)
  } catch {
    return false
  }
}

export function resolveWeChatQRImageSource(session?: WeChatQRSession | null): string {
  const candidates = [session?.qr_url, session?.qr_data]
  for (const candidate of candidates) {
    if (isInlineImage(candidate)) return candidate.trim()
  }
  return ''
}

export function resolveWeChatQRValue(session?: WeChatQRSession | null): string {
  const candidates = [session?.qr_url, session?.qr_data]
  for (const candidate of candidates) {
    const value = stringValue(candidate)
    if (value && !isInlineImage(value) && !isWeChatQRPollURL(value)) return value
  }
  return ''
}

export function hasWeChatQRPayload(session?: WeChatQRSession | null): boolean {
  return !!(
    stringValue(session?.qr_url) ||
    stringValue(session?.qr_data)
  )
}

function isWeChatQRPollURL(path: string): boolean {
  const value = path.trim()
  if (!value) return false
  if (value.startsWith('/')) {
    return value.toLowerCase().startsWith(wechatQRPollPath)
  }
  try {
    const parsed = new URL(value)
    return parsed.pathname.toLowerCase().startsWith(wechatQRPollPath)
  } catch {
    return false
  }
}

function isInlineImage(value: string | undefined | null): value is string {
  if (typeof value !== 'string') return false
  const lower = value.trim().toLowerCase()
  return lower.startsWith('data:image/') || lower.startsWith('blob:')
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}
