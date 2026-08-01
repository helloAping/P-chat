import * as api from '../api/client'
import { appendStreamEvent, endStream, isActiveStream, recoverMissingParts, startStream, stopStream } from '../stores/chat'

export type ConversationTurnInput = {
  sessionId: string
  message: string
  clientMsgID: number
  provider?: string
  model?: string
  style?: string
  workMode?: string
  todoMode: 'auto' | 'resume' | 'clear'
  attachments?: api.InlineAttachment[]
  skillContext?: string
  onServerError?: (event: api.StreamEvent) => void
  onFirstEvent?: () => void
}

// submitConversationTurn 集中一个聊天回合的流生命周期。
// submitConversationTurn owns one chat turn's streaming lifecycle.
export async function submitConversationTurn(input: ConversationTurnInput): Promise<void> {
  const ctrl = new AbortController()
  startStream(input.sessionId, ctrl)

  type DeltaField = 'content' | 'thinking'
  type PendingDelta = { event: api.StreamEvent; field: DeltaField; chunks: string[] }
  const pendingDeltas: PendingDelta[] = []
  let deltaFrame: number | null = null

  const applyEvent = (event: api.StreamEvent) => {
    if (!isActiveStream(input.sessionId, ctrl)) return
    input.onFirstEvent?.()
    if (event.type === 'error' && event.error) input.onServerError?.(event)
    appendStreamEvent(input.sessionId, event)
  }

  const flushPendingDeltas = () => {
    if (deltaFrame !== null) {
      cancelAnimationFrame(deltaFrame)
      deltaFrame = null
    }
    if (!isActiveStream(input.sessionId, ctrl)) {
      pendingDeltas.length = 0
      return
    }
    for (const pending of pendingDeltas.splice(0)) {
      applyEvent({ ...pending.event, [pending.field]: pending.chunks.join('') })
    }
  }

  const enqueueEvent = (event: api.StreamEvent) => {
    if (!isActiveStream(input.sessionId, ctrl)) return
    const field: DeltaField | null = event.type === 'content' && event.content
      ? 'content'
      : event.type === 'thinking' && event.thinking
        ? 'thinking'
        : null
    if (!field) {
      flushPendingDeltas()
      applyEvent(event)
      return
    }

    const last = pendingDeltas[pendingDeltas.length - 1]
    if (
      last
      && last.field === field
      && last.event.sub_agent === event.sub_agent
      && last.event.sub_agent_task === event.sub_agent_task
    ) {
      last.event = { ...last.event, ...event }
      last.chunks.push(event[field] || '')
    } else {
      pendingDeltas.push({ event, field, chunks: [event[field] || ''] })
    }
    if (deltaFrame === null) {
      deltaFrame = requestAnimationFrame(() => {
        deltaFrame = null
        flushPendingDeltas()
      })
    }
  }

  let streamSucceeded = false
  const deferredDrop: { current: { lastSeq: number; reason: string } | null } = { current: null }
  try {
    await api.streamMessagesRetry(input.sessionId, {
      message: input.message,
      client_msg_id: input.clientMsgID,
      provider: input.provider,
      model: input.model,
      style: input.style,
      workMode: input.workMode,
      todo_mode: input.todoMode,
      attachments: input.attachments,
      signal: ctrl.signal,
      skill_context: input.skillContext,
      onStreamDrop: (drop) => {
        deferredDrop.current = drop
      },
      onEvent: enqueueEvent,
    })
    streamSucceeded = true
  } finally {
    flushPendingDeltas()
    endStream(input.sessionId, ctrl)
    const drop = deferredDrop.current
    if (!streamSucceeded && drop && !ctrl.signal.aborted) {
      recoverMissingParts(input.sessionId, drop.lastSeq, drop.reason).catch((error) => {
        console.warn('[stream] recovery failed:', error)
      })
    }
  }
}

// stopConversationTurn 为输入区和其他触发点提供统一停止入口。
// stopConversationTurn provides one stop entry point for all UI triggers.
export function stopConversationTurn(sessionId: string): void {
  stopStream(sessionId)
}
