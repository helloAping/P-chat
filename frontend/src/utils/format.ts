// Shared display formatters for the chat UI. Kept dependency-free
// so any component can import just the piece it needs.

// formatCompactTokens renders a token count in compact human
// form: 800 -> "800", 12_345 -> "12.3K", 200_000 -> "200K",
// 1_000_000 -> "1M", 1_250_000 -> "1.25M". Used by the TopBar
// context badge and any token-density display where "200K / 1M"
// reads better than "200,000 / 1,000,000".
export function formatCompactTokens(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '0'
  if (n < 1000) return String(Math.round(n))
  if (n < 1_000_000) {
    const k = n / 1000
    // >=100k has no meaningful decimal ("200K"); below that keep
    // one decimal ("12.3K") and strip a trailing ".0".
    const s = k >= 100 ? k.toFixed(0) : k.toFixed(1)
    return `${s.replace(/\.0$/, '')}K`
  }
  const m = n / 1_000_000
  const s = (m >= 10 ? m.toFixed(0) : m.toFixed(2)).replace(/\.?0+$/, '')
  return `${s}M`
}

// formatMessageTime renders a Unix-seconds timestamp for a
// message footer. Same-day timestamps collapse to "HH:MM" (the
// chat convention); same-year to "MM-DD HH:MM"; anything older
// than a year keeps a full "YYYY-MM-DD HH:MM" so the date never
// becomes ambiguous. `now` is injectable for tests.
export function formatMessageTime(unixSec: number, now: Date = new Date()): string {
  const d = new Date(unixSec * 1000)
  if (!Number.isFinite(d.getTime())) return ''
  const pad = (x: number) => String(x).padStart(2, '0')
  const hhmm = `${pad(d.getHours())}:${pad(d.getMinutes())}`
  if (d.toDateString() === now.toDateString()) return hhmm
  if (d.getFullYear() === now.getFullYear()) {
    return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${hhmm}`
  }
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${hhmm}`
}
