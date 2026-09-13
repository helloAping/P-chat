import * as api from '../api/client'
import { dataURLToBlobURL } from '../utils/mediaPreview'
import {
  appendLocalUserMessage,
  appendStreamEvent,
  claimNextQueuedTurn,
  completeQueuedTurn,
  endStream,
  failQueuedTurn,
  generateSessionTitle,
  hasBlockingTurnQueueFailure,
  hasQueuedTurns,
  isActiveStream,
  isSessionWorking,
  loadTurnQueue,
  recoverMissingParts,
  setTurnQueueDraining,
  startStream,
  state,
  stopStream,
} from '../stores/chat'

export type ConversationTurnInput = {
  sessionId: string
  message: string
  clientMsgID: number
  provider?: string
  model?: string
  style?: string
  workMode?: string
  useImageRecognition?: boolean
  subAgentModelEnabled?: boolean
  subAgentProvider?: string
  subAgentModel?: string
  turnModePolicy?: api.TurnModePolicy
  todoMode: 'auto' | 'resume' | 'clear'
  attachments?: api.InlineAttachment[]
  skillContext?: string
  activeSkills?: string[]
  onServerError?: (event: api.StreamEvent) => void
  onFirstEvent?: () => void
}

export type ConversationTurnResult = {
  completed: boolean
  aborted: boolean
  /** True when the server already accepted this client_msg_id (HTTP 409 duplicate). */
  duplicateAccepted?: boolean
}

const drainingSessions = new Set<string>()
const queueClaimRetryDelays = [120, 240, 480, 800]

// submitConversationTurn 集中一个聊天回合的流生命周期。
// submitConversationTurn owns one chat turn's streaming lifecycle.
export async function submitConversationTurn(input: ConversationTurnInput): Promise<ConversationTurnResult> {
  const ctrl = new AbortController()
  startStream(input.sessionId, ctrl)

  type DeltaField = 'content' | 'thinking'
  type PendingDelta = { event: api.StreamEvent; field: DeltaField; chunks: string[] }
  const pendingDeltas: PendingDelta[] = []
  let deltaFrame: number | null = null
  let sawDone = false

  const applyEvent = (event: api.StreamEvent) => {
    if (!isActiveStream(input.sessionId, ctrl)) return
    if (event.type === 'done') sawDone = true
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

  const deferredDrop: { current: { lastSeq: number; reason: string } | null } = { current: null }
  let streamCompleted = false
  let duplicateAccepted = false
  try {
    await api.streamMessagesRetry(input.sessionId, {
      message: input.message,
      client_msg_id: input.clientMsgID,
      provider: input.provider,
      model: input.model,
	      style: input.style,
	      workMode: input.workMode,
	      useImageRecognition: input.useImageRecognition,
	      subAgentModelEnabled: input.subAgentModelEnabled,
	      subAgentProvider: input.subAgentProvider,
	      subAgentModel: input.subAgentModel,
	      turnModePolicy: input.turnModePolicy,
	      todo_mode: input.todoMode,
      attachments: input.attachments,
      signal: ctrl.signal,
      skill_context: input.skillContext,
      active_skills: input.activeSkills,
      onStreamDrop: (drop) => {
        deferredDrop.current = drop
        // Idempotent accept: the user row was already persisted; the SSE
        // just never delivered `done`. Treat as success so queue drain
        // completes instead of fail→retry→409 looping.
        if (drop.reason === 'duplicate_client_message') {
          duplicateAccepted = true
        }
      },
      onEvent: enqueueEvent,
    })
    streamCompleted = (sawDone || duplicateAccepted) && !ctrl.signal.aborted
    return {
      completed: streamCompleted,
      aborted: ctrl.signal.aborted,
      duplicateAccepted,
    }
  } finally {
    flushPendingDeltas()
    endStream(input.sessionId, ctrl)
    const drop = deferredDrop.current
    if (drop && !ctrl.signal.aborted) {
      recoverMissingParts(input.sessionId, drop.lastSeq, drop.reason).catch((error) => {
        console.warn('[stream] recovery failed:', error)
      })
    } else if (ctrl.signal.aborted) {
      // User-initiated stop: notify the server so the turn's
      // session lock releases promptly (frozen renderers keep the
      // TCP connection open, so the server would otherwise hold it
      // until MaxTurnSeconds).
      api.cancelStream(input.sessionId)
    }
    if (streamCompleted && !drainingSessions.has(input.sessionId)) {
      void drainQueuedConversationTurns(input.sessionId).catch((error) => {
        console.warn('[turn-queue] drain failed:', error)
      })
    }
  }
}

export async function drainQueuedConversationTurns(sessionId: string): Promise<void> {
  if (!sessionId || drainingSessions.has(sessionId)) return
  if (state.turnQueueEditing[sessionId]) return
  if (state.streaming[sessionId] || isSessionWorking(sessionId)) return
  if (hasBlockingTurnQueueFailure(sessionId)) return
  if (state.pendingQuestion[sessionId]) return
  if ((state.pendingConfirm[sessionId] || []).length > 0) return
  if (state.pendingPlanText[sessionId]) return

  drainingSessions.add(sessionId)
  setTurnQueueDraining(sessionId, true)
  try {
    await loadTurnQueue(sessionId)
    while (hasQueuedTurns(sessionId) && !hasBlockingTurnQueueFailure(sessionId)) {
      if (state.turnQueueEditing[sessionId]) break
      if (state.streaming[sessionId] || isSessionWorking(sessionId)) break
      if (state.pendingQuestion[sessionId]) break
      if ((state.pendingConfirm[sessionId] || []).length > 0) break
      if (state.pendingPlanText[sessionId]) break

      const item = await claimNextQueuedTurnForDrain(sessionId)
      if (!item) break
      const payload = item.payload
      if (!payload?.message || !payload.client_msg_id) {
        await failQueuedTurn(sessionId, item.id, 'queued payload is missing message or client_msg_id')
        break
      }

      appendLocalUserMessage(sessionId, {
        id: payload.client_msg_id,
        role: 'user',
        content: payload.message,
        created_at: Date.now() / 1000,
        attachments: bubbleAttachmentsFromQueuedPayload(payload.attachments),
      })

      try {
        const result = await submitConversationTurn({
          sessionId,
          message: payload.message,
          clientMsgID: payload.client_msg_id,
          provider: payload.provider,
          model: payload.model,
          style: payload.style,
          workMode: payload.work_mode,
          useImageRecognition: payload.use_image_recognition,
          subAgentModelEnabled: !!payload.sub_agent_model_enabled,
          subAgentProvider: payload.sub_agent_provider || '',
          subAgentModel: payload.sub_agent_model || '',
          turnModePolicy: payload.turn_mode_policy,
          todoMode: payload.todo_mode || 'auto',
          attachments: payload.attachments,
          skillContext: payload.skill_context || undefined,
          activeSkills: payload.active_skills || undefined,
        })
        // When duplicate_client_message is accepted, drain must complete the
        // queue item instead of failing (avoids fail->retry->409 loops).
        if (!result.completed && !result.duplicateAccepted) {
          await failQueuedTurn(
            sessionId,
            item.id,
            result.aborted ? 'queued turn was stopped by the user' : 'queued turn did not finish',
          )
          break
        }
        await completeQueuedTurn(sessionId, item.id)
        if (!result.aborted) {
          void generateSessionTitle(sessionId, payload.message).catch(() => {})
        }
      } catch (e: any) {
        await failQueuedTurn(sessionId, item.id, e?.message || String(e))
        break
      }
      await loadTurnQueue(sessionId)
    }
  } finally {
    setTurnQueueDraining(sessionId, false)
    drainingSessions.delete(sessionId)
  }
}

async function claimNextQueuedTurnForDrain(sessionId: string): Promise<api.TurnQueueItem | null> {
  for (let attempt = 0; attempt <= queueClaimRetryDelays.length; attempt++) {
    if (state.turnQueueEditing[sessionId]) return null
    try {
      return await claimNextQueuedTurn(sessionId)
    } catch (e: any) {
      if (isQueueClaimFailedHead(e)) {
        await loadTurnQueue(sessionId)
        return null
      }
      if (!isQueueClaimTemporarilyBlocked(e)) throw e
      if (attempt >= queueClaimRetryDelays.length) {
        await loadTurnQueue(sessionId)
        scheduleQueueDrainRetry(sessionId)
        return null
      }
      await delay(queueClaimRetryDelays[attempt])
    }
  }
  return null
}

function isQueueClaimTemporarilyBlocked(error: unknown): boolean {
  const message = String((error as any)?.message || error)
  return message.includes('HTTP 409') && (
    message.includes('already being processed') ||
    message.includes('already running')
  )
}

function isQueueClaimFailedHead(error: unknown): boolean {
  const message = String((error as any)?.message || error)
  return message.includes('HTTP 409') && message.includes('failed item')
}

function delay(ms: number): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, ms))
}

function scheduleQueueDrainRetry(sessionId: string) {
  window.setTimeout(() => {
    void drainQueuedConversationTurns(sessionId).catch((error) => {
      console.warn('[turn-queue] delayed drain failed:', error)
    })
  }, 1000)
}

function bubbleAttachmentsFromQueuedPayload(attachments?: api.InlineAttachment[]): api.MessageAttachment[] | undefined {
  if (!attachments?.length) return undefined
  const out: api.MessageAttachment[] = []
  for (const att of attachments) {
    if (att.type === 'image_url' || att.type === 'audio_url' || att.type === 'video_url') {
      out.push({
        type: att.type,
        url: dataURLToBlobURL(att.upload_id ? api.uploadURL(att.upload_id) : att.url),
        name: att.name,
        kind: att.kind,
        mime: att.mime,
      })
      continue
    }
    out.push({
      type: 'text',
      text: att.text,
      url: att.upload_id ? api.uploadURL(att.upload_id) : undefined,
      name: att.name,
      kind: att.kind,
      mime: att.mime,
    })
  }
  return out
}

// stopConversationTurn 为输入区和其他触发点提供统一停止入口。
// stopConversationTurn provides one stop entry point for all UI triggers.
export function stopConversationTurn(sessionId: string): void {
  stopStream(sessionId)
}
