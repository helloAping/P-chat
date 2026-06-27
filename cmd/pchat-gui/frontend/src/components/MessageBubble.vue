<script setup lang="ts">
// MessageBubble renders one Message. The assistant
// message body is built from a flat parts array
// (text + thinking + tool calls + sub-agents), each
// rendered by a dedicated sub-component. User / system
// messages still go through the markdown pipeline.
//
// Loading: while the assistant is streaming and no
// content has arrived yet, the bubble shows three
// bouncing dots (iMessage-style).
import { computed } from 'vue'
import { marked } from 'marked'
import type { Message, MessagePart } from '../api/client'
import { state } from '../stores/chat'
import ThinkingBlock from './ThinkingBlock.vue'
import ToolCallCard from './ToolCallCard.vue'
import SubAgentCard from './SubAgentCard.vue'
import LoadingDots from './LoadingDots.vue'

const props = defineProps<{ message: Message; streaming?: boolean }>()

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
  if (props.message.role === 'tool') return ''
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

// Show the loading-dots placeholder when the
// assistant is streaming but no content / thinking /
// tool part has arrived yet. (Without this, the user
// sees nothing for the first ~1-3 seconds of a chat.)
const showLoadingDots = computed(() => {
  return props.streaming === true
      && props.message.role === 'assistant'
      && !props.message.content
      && (!props.message.parts || props.message.parts.length === 0)
})

// Token usage badge — only show if we have it.
const hasTokens = computed(() =>
  props.message.role === 'assistant'
  && (props.message.tokens_in || props.message.tokens_out)
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

        <!-- User / system: markdown of `content` -->
        <div
          v-if="message.role === 'user' || message.role === 'system'"
          class="md-body"
          v-html="userHtml"
        />

        <!-- Assistant: parts-driven render.
             Falls back to markdown of `content` for
             messages loaded from history (no parts
             were persisted server-side). -->
        <template v-if="message.role === 'assistant'">
          <template v-if="message.parts && message.parts.length">
            <template v-for="(p, i) in message.parts" :key="i">
              <ThinkingBlock
                v-if="p.kind === 'thinking'"
                :part="p"
                :default-open="streaming && p.kind === 'thinking' && p.streaming && i === message.parts.length - 1"
              />
              <ToolCallCard v-else-if="p.kind === 'tool'" :part="p" />
              <SubAgentCard v-else-if="p.kind === 'sub_agent'" :part="p" />
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
          <LoadingDots v-if="showLoadingDots" />
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
</style>
