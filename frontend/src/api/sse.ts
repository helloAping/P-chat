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
  // idleTimeoutMs bounds the time a read may stay silent. When no
  // bytes arrive for this long, the reader is cancelled, onStreamDrop
  // fires, and an "idle" error is thrown (not retried — the caller
  // recovers missing parts from the server instead). 0 disables.
  idleTimeoutMs?: number
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

export function streamErrorStatus(error: unknown): number | null {
  const message = String((error as any)?.message ?? error ?? '')
  const match = message.match(/\bHTTP\s+(\d{3})\b/)
  if (!match) return null
  const status = Number(match[1])
  return Number.isFinite(status) ? status : null
}

export function shouldRetryStreamError(error: unknown): boolean {
  return streamErrorStatus(error) !== 409
}

// isDuplicateClientMessageError reports whether `error` is the
// "the server already accepted this exact user message" response
// the idempotency check emits as HTTP 409. The retry layer MUST
// treat this as a non-error: the request was accepted the first
// time, the SSE connection just died before any `done` event
// reached the client. The transport surfaces it as a stream drop
// (with reason 'duplicate_client_message') so conversationTurn.ts
// can run the snapshot recovery path — without this, a dropped
// mid-handshake connection used to surface as a hard
// "发送失败: HTTP 409" toast and the user had to re-type.
//
// We match on the JSON `code` substring instead of fully parsing
// the body to keep this synchronous and zero-dependency (the body
// can be large on some proxies). The token is server-defined and
// stable: see internal/server/messages.go's `duplicate_client_message`.
export function isDuplicateClientMessageError(error: unknown): boolean {
  const status = streamErrorStatus(error)
  if (status !== 409) return false
  const message = String((error as any)?.message ?? error ?? '')
  return message.includes('"duplicate_client_message"')
}

// abortableDelay waits for retry backoff without outliving a user stop.
// It returns true when the timer elapsed and false when the signal aborted.
export function abortableDelay(ms: number, signal?: AbortSignal): Promise<boolean> {
  if (signal?.aborted) return Promise.resolve(false)
  return new Promise<boolean>((resolve) => {
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort)
      resolve(true)
    }, ms)
    const onAbort = () => {
      clearTimeout(timer)
      signal?.removeEventListener('abort', onAbort)
      resolve(false)
    }
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

// readWithIdleTimeout races a single reader.read() against an idle
// watchdog. On timeout the reader is cancelled and the promise rejects
// with the idle error; the caller reports the drop and recovers via the
// P0-1 snapshot path instead of spinning forever against a stuck turn.
function readWithIdleTimeout(
  reader: ReadableStreamDefaultReader<Uint8Array>,
  signal: AbortSignal | undefined,
  idleTimeoutMs: number,
  makeIdleError: () => Error,
): Promise<ReadableStreamReadResult<Uint8Array>> {
  return new Promise((resolve, reject) => {
    let settled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const clear = () => {
      if (timer !== undefined) clearTimeout(timer)
    }
    // Named handler so we can removeEventListener on EVERY settle path.
    // A `{ once: true }` listener is only removed when the signal FIRES —
    // on a normally-completing stream (never aborted) it would otherwise
    // stay registered forever, one per read, each pinning its Promise and
    // the resolved Uint8Array chunk. That accumulation is a real leak:
    // a long multi-round SSE turn (thousands of chunks) grew ~28k
    // listeners + Promises + ArrayBuffers in the webview heap (see
    // heap snapshot 2026-08-04, AbortSignal had 10× ~2.8k listeners).
    const onAbort = () => {
      if (settled) return
      settled = true
      clear()
      resolve({ done: true, value: undefined } as ReadableStreamReadResult<Uint8Array>)
    }
    timer = setTimeout(() => {
      if (settled) return
      settled = true
      signal?.removeEventListener('abort', onAbort)
      if (signal?.aborted) return
      reader.cancel().catch(() => {})
      reject(makeIdleError())
    }, idleTimeoutMs)
    reader.read().then(
      (r) => {
        if (settled) return
        settled = true
        clear()
        signal?.removeEventListener('abort', onAbort)
        resolve(r)
      },
      (e) => {
        if (settled) return
        settled = true
        clear()
        signal?.removeEventListener('abort', onAbort)
        reject(e)
      },
    )
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

export async function consumeSSEStream<T extends StreamEventLike>(
  options: SSEConsumerOptions<T>,
): Promise<void> {
  const decoder = new TextDecoder('utf-8')
  let buffer = ''
  let lastSeq = -1
  let done = false
  // Pending idle watchdog for the current read. Created on every
  // loop iteration (not once) so a stream that delivers bytes
  // continuously — e.g. a model in a long reasoning pass — keeps
  // resetting the timer and is never cut off.
  const idleError = () =>
    new Error(`${options.label} stream: no data for ${options.idleTimeoutMs}ms (turn may be stuck)`)

  while (!done) {
    if (options.signal?.aborted) return

    let result: ReadableStreamReadResult<Uint8Array>
    try {
      if (options.idleTimeoutMs && options.idleTimeoutMs > 0) {
        result = await readWithIdleTimeout(
          options.reader,
          options.signal,
          options.idleTimeoutMs,
          idleError,
        )
      } else {
        result = await options.reader.read()
      }
    } catch (error: any) {
      if (options.signal?.aborted) return
      const reason = error?.message || 'read failed'
      if (!options.signal?.aborted && options.onStreamDrop) {
        try {
          options.onStreamDrop({ lastSeq, reason })
        } catch {
          // 恢复回调不能掩盖原始流错误。
          // A recovery callback must not hide the original stream error.
        }
      }
      if (options.idleTimeoutMs && options.idleTimeoutMs > 0 && reason.includes('no data for')) {
        // Idle timeouts are NOT transport errors: the connection is
        // fine, the turn is simply stuck. Reconnect-retry would loop
        // forever against the same stuck turn; the P0-1 recovery path
        // (recoverMissingParts) is the correct way to surface what
        // the server did manage to persist.
        throw error
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
      if (options.signal?.aborted) return
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
      if (event.type === 'done') {
        done = true
        break
      }
    }
  }
}
