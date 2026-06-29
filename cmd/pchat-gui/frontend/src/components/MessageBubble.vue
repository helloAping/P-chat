<script setup lang="ts">
// MessageBubble renders one Message. The assistant
// message body is built from a flat parts array
// (text + thinking + tool calls + sub-agents), each
// rendered by a dedicated sub-component. User / system
// messages still go through the markdown pipeline.
//
// Streaming model: the chat UI doesn't run its own
// typewriter animation. The "typewriter" effect is
// the natural SSE stream itself — the LLM emits
// content chunk by chunk over the network, the chat
// store appends each chunk to the trailing text part,
// and the DOM re-renders on each tick. The user sees
// the text grow in real time, exactly like ChatGPT.
//
// Concretely:
//   - `TypedText` is a thin wrapper around
//     `marked.parse()` plus a blinking `::after` caret.
//     It renders the text verbatim and updates the
//     DOM as `props.text` grows. No rAF loop, no
//     artificial delay.
//   - The caret is shown only on the *trailing* text
//     part of an actively-streaming message. Earlier
//     text parts (e.g. before a tool call) and post-
//     stream text use the static markdown render.
//   - The placeholder `TypedText` (empty text) is
//     shown before the first SSE event arrives, so
//     the user sees a blinking caret alone in the
//     bubble as soon as they hit send.
import { computed } from 'vue'
import { marked } from 'marked'
import type { Message, MessagePart } from '../api/client'
import { state } from '../stores/chat'
import ThinkingBlock from './ThinkingBlock.vue'
import ToolCallCard from './ToolCallCard.vue'
import SubAgentCard from './SubAgentCard.vue'
import TypedText from './TypedText.vue'

const props = defineProps<{ message: Message; streaming?: boolean }>()

// isLiveTextPart returns true for the trailing text
// part of an actively-streaming message. This is the
// one part that should render through `TypedText`
// (so the caret is visible). All other text parts
// (earlier in the same turn, or after streaming has
// ended) use the static markdown render.
function isLiveTextPart(idx: number, kind: string, parts: MessagePart[] | undefined): boolean {
  if (kind !== 'text') return false
  if (!props.streaming) return false
  if (!parts || parts.length === 0) return false
  // Find the last text part in the array.
  for (let i = parts.length - 1; i >= 0; i--) {
    if (parts[i].kind === 'text') return i === idx
  }
  return false
}

// isLiveThinkingPart mirrors isLiveTextPart for the
// thinking trace. While streaming, the trailing
// thinking part (if any) renders through
// `ThinkingBlock` with its shimmer effect; once
// streaming ends, it falls back to the collapsed
// static view.
function isLiveThinkingPart(idx: number, kind: string, parts: MessagePart[] | undefined): boolean {
  if (kind !== 'thinking') return false
  if (!props.streaming) return false
  if (!parts || parts.length === 0) return false
  for (let i = parts.length - 1; i >= 0; i--) {
    if (parts[i].kind === 'thinking') return i === idx
  }
  return false
}

// The role check is unchanged: tool messages are not
// rendered as bubbles; they live inside the assistant
// message as ToolCallCard parts.
const isSystem = computed(() => props.message.role === 'system')

// For user / system messages, the markdown pipeline
// renders the whole `content` string. For assistant
// messages, we render `parts` instead. (We still
// support legacy assistant messages that have only
// `content` and no `parts` — the server's history
// endpoint returns content-only messages, so the
// fallback is important.)
const assistantHtml = computed(() => '')

const userHtml = computed(() => {
  const md = marked.parse(props.message.content || '', { async: false, breaks: true })
  return md as string
})

// Attachments (images / files) — only used by user
// messages today, but kept general.
function openLightbox(src: string, alt: string) {
  state.lightbox = { show: true, src, alt }
}
function thumbText(kind?: string) {
  switch (kind) {
    case 'image': return '🖼'
    case 'audio': return '🔊'
    case 'text':  return '📝'
    default:      return '📄'
  }
}
function shortWarnText(t?: string): string {
  if (!t) return 'image skipped'
  const m = t.match(/\(attached image: ([^,]+)/)
  const name = m ? m[1].trim() : 'image'
  return `${name} · model does not support image input`
}

// showTypewriterPlaceholder is the "ready to receive
// SSE" indicator: a blinking caret (`▍`) sitting alone
// in the bubble while the assistant message has no
// parts yet. It replaces the old three-bouncing-dots
// loader — the typewriter cursor *is* the loader, and
// the natural SSE stream takes over the moment the
// first content chunk arrives.
const showTypewriterPlaceholder = computed(() => {
  if (props.streaming !== true) return false
  if (props.message.role !== 'assistant') return false
  const parts = props.message.parts
  if (!parts || parts.length === 0) return true
  return !parts.some(p =>
    p.kind === 'text' || p.kind === 'thinking' ||
    p.kind === 'tool' || p.kind === 'sub_agent',
  )
})

const statusLines = computed(() => {
  const m = props.message as any
  if (!m._statusText || !m._statusText.length) return []
  // Show only the last 5 lines to keep the bar compact.
  const lines = m._statusText as string[]
  return lines.slice(-5)
})

// Token usage badge — only show if we have it.
const hasTokens = computed(() =>
  props.message.role === 'assistant'
  && (props.message.tokens_in || props.message.tokens_out)
)

// "Image not supported" — when the LLM rejects the
// user's image with the "this model does not support
// image input" error, the chat store tags the trailing
// user message with `visionUnsupported: true`. We
// surface a dedicated chip under the attachments so
// the user can see *why* the image was ignored, even
// after the toast disappears.
const showVisionWarn = computed(() =>
  props.message.role === 'user' && props.message.visionUnsupported === true,
)
</script>

<template>
  <div class="msg" :class="message.role + (streaming && message.role === 'assistant' ? ' streaming' : '')">
    <div class="bubble">
      <div v-if="isSystem" class="system-icon">›</div>
      <div class="bubble-body">
        <!-- Attachments (user / tool) -->
        <div v-if="message.attachments && message.attachments.length" class="attachments">
          <template v-for="(a, i) in message.attachments" :key="i">
            <img
              v-if="a.type === 'image_url' && a.url"
              class="msg-image"
              :src="a.url"
              :alt="a.name || 'image'"
              loading="lazy"
              @click="openLightbox(a.url, a.name || 'image')"
            />
            <div
              v-else-if="a.type === 'text' && a.kind === 'image_not_supported'"
              class="msg-image-warn"
              :title="a.text"
            >
              <span class="warn-icon">⚠</span>
              <span class="warn-text">{{ shortWarnText(a.text) }}</span>
            </div>
            <div v-else-if="a.type === 'text'" class="msg-file" :title="a.text">
              {{ thumbText(a.kind) }} {{ a.name || '文件' }}
            </div>
          </template>
        </div>

        <!-- Vision-not-supported warning chip. Shown when the
             LLM rejected the user's image with the "this
             model does not support image input" error. The
             chat store sets `message.visionUnsupported: true`
             on the trailing user message when that error
             arrives. Sits just below the attachments / text
             so the user can see *why* the image was ignored. -->
        <div v-if="showVisionWarn" class="vision-warn">
          <span class="warn-icon">⚠</span>
          <span class="warn-text">图片未处理：当前模型不支持图片输入</span>
          <span class="warn-hint">切换到支持视觉的模型（如 gpt-4o / claude-3.5+）后重新发送</span>
        </div>

        <!-- User / system / tool: markdown of `content` -->
        <div
          v-if="message.role === 'user' || message.role === 'system' || message.role === 'tool'"
          class="md-body"
          v-html="userHtml"
        />

        <!-- Assistant: parts-driven render.
             Falls back to markdown of `content` for
             messages loaded from history (no parts
             were persisted server-side).
             The trailing text/thinking part of an
             actively-streaming message renders through
             `TypedText` / `ThinkingBlock` (with the
             blinking caret on the text part) so the
             user sees the SSE stream arrive in real
             time. All other parts — earlier text in
             the same turn, post-stream text, tools,
             sub-agents — render statically. -->
        <template v-if="message.role === 'assistant'">
          <!-- Live status bar during streaming -->
          <div v-if="statusLines.length" class="stream-status">
            <div v-for="(line, i) in statusLines" :key="i" class="status-line">{{ line }}</div>
          </div>
          <template v-if="message.parts && message.parts.length">
            <template v-for="(p, i) in message.parts" :key="i">
              <ThinkingBlock
                v-if="p.kind === 'thinking' && isLiveThinkingPart(i, p.kind, message.parts)"
                :part="p"
                :default-open="true"
              />
              <details
                v-else-if="p.kind === 'thinking'"
                class="thinking-block"
                :class="{ streaming: p.streaming }"
              >
                <summary>
                  <span class="caret">▸</span>
                  <span class="label">思考过程</span>
                  <span class="meta" v-if="p.text">{{ p.text.length }} 字</span>
                </summary>
                <pre class="thinking-body">{{ p.text }}</pre>
              </details>
              <ToolCallCard v-else-if="p.kind === 'tool'" :part="p" />
              <SubAgentCard v-else-if="p.kind === 'sub_agent'" :part="p" />
              <TypedText
                v-else-if="p.kind === 'text' && isLiveTextPart(i, p.kind, message.parts)"
                :text="p.text || ''"
                :active="true"
              />
              <div
                v-else-if="p.kind === 'text'"
                class="md-body"
                v-html="marked.parse(p.text || '', { async: false, breaks: true })"
              />
            </template>
          </template>
          <template v-else-if="message.content">
            <div class="md-body" v-html="userHtml"></div>
          </template>
          <!-- Streaming placeholder: a blinking caret
               alone in the bubble, ready to receive the
               first SSE content chunk. Visible only
               while streaming and the message has no
               renderable parts yet. -->
          <TypedText
            v-else-if="showTypewriterPlaceholder"
            :text="''"
            :active="true"
          />
        </template>

        <!-- Footer for assistant: tokens / elapsed -->
        <div v-if="hasTokens" class="msg-meta">
          <span v-if="message.tokens_in || message.tokens_out">
            {{ message.tokens_in || 0 }}↓ / {{ message.tokens_out || 0 }}↑
          </span>
          <span v-if="message.elapsed" class="msg-elapsed">· {{ message.elapsed }}</span>
          <span v-if="message.model" class="msg-model">· {{ message.model }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.msg {
  display: flex;
  margin: 6px 16px;
}
.msg.user { justify-content: flex-end; }
.msg.assistant { justify-content: flex-start; }
.msg.tool { justify-content: flex-start; }
.bubble {
  max-width: 80%;
  padding: 8px 12px;
  border-radius: 10px;
  word-wrap: break-word;
  overflow-wrap: break-word;
}
.msg.user .bubble {
  background: var(--accent);
  color: var(--on-accent);
}
.msg.assistant .bubble {
  background: var(--bg-3);
  color: var(--text);
}
.msg.tool .bubble {
  background: var(--bg-2);
  color: var(--text-2);
  font-family: ui-monospace, Menlo, monospace;
  font-size: 12px;
  border: 1px solid var(--border);
}
.msg.system {
  justify-content: center;
  margin: 4px 16px;
}
.msg.system .bubble {
  background: transparent;
  color: var(--text-2);
  font-size: 12px;
  max-width: 90%;
  display: flex; align-items: flex-start; gap: 6px;
  border-left: 2px solid var(--border-2);
  padding: 4px 8px 4px 8px;
}
.system-icon {
  color: var(--text-4);
  font-family: ui-monospace, Menlo, monospace;
  font-weight: 700;
  flex-shrink: 0;
  line-height: 1.6;
}
.bubble-body { min-width: 0; flex: 1; }
.attachments {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 6px;
}
.msg-image {
  max-width: 100%;
  max-height: 240px;
  border-radius: 6px;
  cursor: zoom-in;
  background: var(--bg);
}
.msg-image:hover { opacity: 0.92; }
.msg-file {
  background: var(--bg-3);
  border: 1px solid var(--border-2);
  border-radius: 4px;
  padding: 2px 6px;
  font-size: 12px;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.msg-image-warn {
  display: inline-flex; align-items: center; gap: 6px;
  background: var(--warn-soft);
  border: 1px dashed var(--warn);
  border-radius: 6px;
  padding: 4px 8px;
  font-size: 12px;
  color: var(--text-2);
  max-width: 100%;
}
.warn-icon { color: var(--warn); }
.warn-text { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

/* Full-width "image not supported" warning that sits under
 * a user message's attachments. Used when the LLM rejected
 * the image with a vision_unsupported error — the inline
 * msg-image-warn chip is per-attachment; this is the
 * higher-level "the whole image was skipped" notice that
 * the chat store tags on the message itself. */
.vision-warn {
  display: flex; align-items: center; gap: 6px;
  flex-wrap: wrap;
  background: var(--warn-soft);
  border: 1px dashed var(--warn);
  border-radius: 6px;
  padding: 6px 10px;
  font-size: 12px;
  color: var(--text-2);
  margin: 6px 0 4px 0;
}
.vision-warn .warn-icon { color: var(--warn); font-size: 14px; }
.vision-warn .warn-text { color: var(--text); font-weight: 500; }
.vision-warn .warn-hint { color: var(--text-3); font-size: 11px; }

/* The pulsing animation. The bubble's border stays
 * solid; only its opacity breathes. While the new
 * three-bouncing-dots placeholder is showing, this
 * animation is suppressed (the dots are lively
 * enough on their own). */
.msg.streaming .bubble { animation: pulse 1.5s infinite; }
@keyframes pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.85; } }

.msg-meta {
  margin-top: 6px;
  padding-top: 4px;
  border-top: 1px dashed var(--border-2);
  font-size: 11px;
  color: var(--text-4);
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}
.msg-elapsed, .msg-model { color: var(--text-4); }
.stream-status {
  margin-bottom: 6px;
  padding: 4px 10px;
  background: var(--bg-3);
  border-radius: 6px;
  font-size: 11px;
  color: var(--text-3);
  line-height: 1.6;
}
.status-line {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
