export const MAX_SESSION_TAGS = 8
export const MAX_SESSION_TAG_RUNES = 16

export function normalizeSessionTags(input: string[]): { tags: string[]; error?: string } {
  const seen = new Set<string>()
  const tags: string[] = []
  for (const raw of input) {
    const tag = raw.trim()
    if (!tag) continue
    if (/[\r\n\t]/.test(tag)) return { tags: [], error: '标签不能包含换行' }
    if ([...tag].length > MAX_SESSION_TAG_RUNES) {
      return { tags: [], error: `标签不能超过 ${MAX_SESSION_TAG_RUNES} 个字` }
    }
    if (seen.has(tag)) continue
    seen.add(tag)
    tags.push(tag)
  }
  if (tags.length > MAX_SESSION_TAGS) {
    return { tags: [], error: `最多 ${MAX_SESSION_TAGS} 个标签` }
  }
  return { tags }
}
