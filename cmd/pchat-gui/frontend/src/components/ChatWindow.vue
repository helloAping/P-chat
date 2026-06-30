<script setup lang="ts">
import { ref, nextTick, watch, computed, onMounted, onBeforeUnmount } from 'vue'
import { NScrollbar, NSpin, NSpace, NButton, NInput, NTooltip, useMessage, NIcon } from 'naive-ui'
import MessageBubble from './MessageBubble.vue'
import InputArea from './InputArea.vue'
import TodoPanel from './TodoPanel.vue'
import QuestionPanel from './QuestionPanel.vue'
import { state, currentMessages, isStreaming, switchSession, loadMoreMessages } from '../stores/chat'
import * as api from '../api/client'

const messagesEl = ref<HTMLElement | null>(null)
const message = useMessage()

// Scroll position bookkeeping for the infinite-scroll
// history loader. We compare the scrollTop before and after
// prepending a new (older) page so the user's view doesn't
// jump — without this, prepending shifts every existing
// message downward and the user's reading position is lost.
const wasAtTop = ref(false)
const prevScrollHeight = ref(0)
const prevScrollTop = ref(0)

function scrollToBottom() {
  nextTick(() => {
    if (messagesEl.value) {
      messagesEl.value.scrollTo({ top: 99999, behavior: 'smooth' })
    }
  })
}

// onScroll is bound to the NScrollbar's underlying
// scrollable element (Naive UI's NScrollbar ref points to
// the outer wrapper, but the actual scroll listener must
// attach to the inner .n-scrollbar-container). When the user
// scrolls within ~80px of the top, we kick off the next
// page load — but only if has_more is true.
async function onScroll(e: Event) {
  if (!state.currentID) return
  const target = e.target as HTMLElement
  if (target.scrollTop < 80) {
    wasAtTop.value = true
    prevScrollHeight.value = target.scrollHeight
    prevScrollTop.value = target.scrollTop
    await loadMoreMessages(state.currentID)
  }
}

// Restore the user's view after a history page has been
// prepended. We hold the scrollTop constant by setting it
// to (oldScrollTop + (newHeight - oldHeight)) — same
// technique GitHub uses for its issue list.
watch(() => currentMessages.value.length, (newLen, oldLen) => {
  if (!wasAtTop.value || newLen <= (oldLen || 0)) return
  nextTick(() => {
    if (!messagesEl.value) return
    const el = messagesEl.value
    const heightDelta = el.scrollHeight - prevScrollHeight.value
    el.scrollTop = prevScrollTop.value + heightDelta
    wasAtTop.value = false
  })
})

watch(() => currentMessages.value, () => scrollToBottom(), { deep: true })
watch(() => state.currentID, () => scrollToBottom())

onMounted(async () => {
  scrollToBottom()
  // Attach the scroll listener to the actual scrollable
  // element. NScrollbar's :native-scrollbar="false" path
  // wraps the content in a custom scrollbar; the inner
  // container is what receives scroll events.
  await nextTick()
  const scroller = messagesEl.value?.querySelector('.n-scrollbar-container') as HTMLElement | null
  if (scroller) {
    scroller.addEventListener('scroll', onScroll, { passive: true })
    ;(messagesEl.value as any).__scroller = scroller
  }
})

onBeforeUnmount(() => {
  const scroller = (messagesEl.value as any)?.__scroller as HTMLElement | undefined
  if (scroller) scroller.removeEventListener('scroll', onScroll)
})

async function onOpenExplorer() {
  if (!state.activeProjectPath) return
  try { await api.openExplorer(state.activeProjectPath) } catch { /* ignore */ }
}

async function onOpenTerminal() {
  if (!state.activeProjectPath) return
  try { await api.openTerminal(state.activeProjectPath) } catch { /* ignore */ }
}
</script>

<template>
  <main class="chat-main">
    <div class="chat-header">
      <div class="header-title">
        {{ state.sessions.find(s => s.id === state.currentID)?.title || 'P-Chat' }}
      </div>
      <div v-if="state.activeProjectPath" class="header-actions">
        <NButton size="tiny" quaternary @click="onOpenExplorer" title="打开资源管理器">📂</NButton>
        <NButton size="tiny" quaternary @click="onOpenTerminal" title="打开终端">🖥</NButton>
      </div>
    </div>
    <NScrollbar ref="messagesEl" class="messages-scroll" :native-scrollbar="false">
      <div class="messages">
        <div v-if="currentMessages.length === 0" class="empty">
          <div class="empty-icon">💬</div>
          <div class="empty-title">开始一个新对话吧</div>
          <div class="empty-hint">输入 /help 查看可用命令 · 拖入或粘贴文件可作为附件</div>
        </div>
        <div
          v-if="state.sessionPaging[state.currentID]?.loading"
          class="history-loading"
        >加载更早的消息…</div>
        <MessageBubble
          v-for="(m, i) in currentMessages"
          :key="i"
          :message="m"
          :streaming="isStreaming && i === currentMessages.length - 1 && m.role === 'assistant'"
        />
      </div>
    </NScrollbar>
    <QuestionPanel />
    <TodoPanel />
    <InputArea />
  </main>
</template>

<style scoped>
.chat-main {
  flex: 1;
  display: flex;
  flex-direction: column;
  background: var(--bg);
  min-width: 0;
}
.chat-header {
  height: 48px;
  padding: 0 16px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  border-bottom: 1px solid var(--border);
  background: var(--bg-2);
  flex-shrink: 0;
}
.header-title { font-weight: 500; font-size: 14px; }
.header-actions { display: flex; align-items: center; gap: 2px; }
.messages-scroll { flex: 1; min-height: 0; }
.messages {
  padding: 12px 0;
  min-height: 100%;
}
.history-loading {
  text-align: center;
  font-size: 12px;
  color: var(--text-4);
  padding: 8px 0;
  opacity: 0.7;
}
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: var(--text-3);
  gap: 8px;
  padding-top: 120px;
  text-align: center;
}
.empty-icon { font-size: 48px; }
.empty-title { font-size: 15px; color: var(--text-2); }
.empty-hint { font-size: 12px; color: var(--text-4); }
</style>
