import type { Message } from '../api/client.ts'

// dedupMessagesByKey folds a freshly-loaded page into the
// existing in-memory list, dropping rows we already have.
// It cross-links both identities because optimistic local
// rows are created with id only, while the later server row
// has both id and seq.
export function dedupMessagesByKey(existing: Message[], incoming: Message[]): Message[] {
  if (incoming.length === 0) return existing
  const merged: Message[] = []
  const byID = new Map<number, number>()
  const bySeq = new Map<number, number>()

  const remember = (m: Message, index: number) => {
    if (m.id != null && m.id > 0) byID.set(m.id, index)
    if (m.seq != null && m.seq > 0) bySeq.set(m.seq, index)
  }
  const forget = (m: Message) => {
    if (m.id != null && m.id > 0) byID.delete(m.id)
    if (m.seq != null && m.seq > 0) bySeq.delete(m.seq)
  }
  const upsert = (m: Message) => {
    let index: number | undefined
    if (m.id != null && m.id > 0) index = byID.get(m.id)
    if (index == null && m.seq != null && m.seq > 0) index = bySeq.get(m.seq)
    if (index == null) {
      merged.push(m)
      remember(m, merged.length - 1)
      return
    }
    forget(merged[index])
    merged[index] = m
    remember(m, index)
  }
  const sortKey = (m: Message): number => {
    if (m.seq != null && m.seq > 0) return m.seq
    if (m.id != null && m.id > 0) return Number.MAX_SAFE_INTEGER / 2 + m.id
    return Number.MAX_SAFE_INTEGER
  }
  for (const m of existing) upsert(m)
  for (const m of incoming) upsert(m)
  return merged.sort((a, b) => sortKey(a) - sortKey(b))
}
