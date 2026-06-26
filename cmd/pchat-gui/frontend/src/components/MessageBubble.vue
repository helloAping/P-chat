<script setup lang="ts">
import { computed } from 'vue'
import { NScrollbar, NSpin, NTag } from 'naive-ui'
import { marked } from 'marked'
import type { Message } from '../api/client'
import { state } from '../stores/chat'

const props = defineProps<{ message: Message; streaming?: boolean }>()

const html = computed(() => {
  // system, user, and assistant all render through the markdown
  // pipeline. The role only affects the surrounding bubble chrome.
  if (props.message.role === 'tool') return ''
  const md = marked.parse(props.message.content || '', { async: false, breaks: true })
  return md as string
})

const isSystem = computed(() => props.message.role === 'system')

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

// shortWarnText trims the agent's "attached image" marker down
// to a one-liner. The full text is in the title attr; on the
// bubble we just want "model does not support image" + the
// filename to make it obvious what was rejected.
function shortWarnText(t?: string): string {
  if (!t) return 'image skipped'
  const m = t.match(/\(attached image: ([^,]+)/)
  const name = m ? m[1].trim() : 'image'
  return `${name} · model does not support image input`
}
</script>

<template>
  <div class="msg" :class="message.role + (streaming ? ' streaming' : '')">
    <div class="bubble">
      <div v-if="isSystem" class="system-icon">›</div>
      <div class="bubble-body">
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
        <div v-if="html" class="md-body" v-html="html"></div>
        <NSpin v-if="streaming && !message.content" size="small" />
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
.msg.streaming .bubble { animation: pulse 1.5s infinite; }
@keyframes pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.7; } }
</style>
