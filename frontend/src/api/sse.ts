export interface StreamEventLike {
  type?: string
  seq?: number
  sub_agent?: boolean
  sub_agent_task?: string
  [key: string]: unknown
}

export interface SSEConsumerOptions<T extends StreamEventLike> {
  reader: ReadableStreamDefaultReader<Uint8Array>
  signal?: AbortSignal
  label: string
  onEvent: (ev: T) => void
  onStreamDrop?: (drop: { lastSeq: number; reason: string }) => void
}

export function parseSSEFrame(block: string): { data: string; seq?: number } {
  let data = ''
  let seq: number | undefined
  for (const line of block.split('\n')) {
    if (line.startsWith('data:')) {
      if (data.length > 0) data += '\n'
      data += line.slice(5).trimStart()
    } else if (line.startsWith('id:')) {
      const raw = line.slice(3).trim()
      const parsed = Number(raw)
      if (raw && Number.isFinite(parsed)) seq = parsed
    }
  }
  return { data: data.trim(), seq }
}

export function decodeStreamEvent<T extends StreamEventLike>(
  data: string,
  label: string,
  seq?: number,
): T | null {
  const payload = data.trim()
  if (!payload || payload === '[DONE]') return null
  try {
    const event = JSON.parse(payload) as T
    if (seq !== undefined) event.seq = seq
    return event
  } catch {
    console.warn(`${label} SSE parse error`, 'raw:', payload.slice(0, 200))
    return null
  }
}

export function emitStreamEvent<T extends StreamEventLike>(
  event: T,
  label: string,
  onEvent: (ev: T) => void,
): void {
  try {
    onEvent(event)
  } catch (inner) {
    console.warn(`[${label}] event handler threw, continuing:`, inner)
  }
}

export async function consumeSSEStream<T extends StreamEventLike>(
  options: SSEConsumerOptions<T>,
): Promise<void> {
  const decoder = new TextDecoder('utf-8')
  let buffer = ''
  let lastSeq = -1
  let done = false

  while (!done) {
    let result: ReadableStreamReadResult<Uint8Array>
    try {
      result = await options.reader.read()
    } catch (error: any) {
      const reason = error?.message || 'read failed'
      if (!options.signal?.aborted && options.onStreamDrop) {
        try {
          options.onStreamDrop({ lastSeq, reason })
        } catch {
          // 恢复回调不能掩盖原始流错误。
          // A recovery callback must not hide the original stream error.
        }
      }
      throw new Error(`${options.label} stream: ${reason}`)
    }

    done = result.done
    if (result.value) {
      buffer += decoder.decode(result.value, { stream: true })
    }

    let frameEnd: number
    while ((frameEnd = buffer.indexOf('\n\n')) >= 0) {
      const block = buffer.slice(0, frameEnd)
      buffer = buffer.slice(frameEnd + 2)
      const frame = parseSSEFrame(block)
      const event = decodeStreamEvent<T>(frame.data, options.label, frame.seq)
      if (!event) continue
      if (typeof event.seq === 'number') {
        if (import.meta.env?.DEV && event.seq <= lastSeq) {
          console.warn(
            `[${options.label}] non-monotonic seq: prev=${lastSeq} now=${event.seq} type=${event.type}`,
          )
        }
        lastSeq = event.seq
        if (import.meta.env?.DEV) {
          console.debug(
            `[${options.label}] seq=${event.seq} type=${event.type}` +
            (event.sub_agent ? ` sub=${event.sub_agent_task ?? ''}` : ''),
          )
        }
      }

      emitStreamEvent(event, options.label, options.onEvent)
    }
  }
}
