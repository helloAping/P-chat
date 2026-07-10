<script setup lang="ts">
import { ref, nextTick, watch, computed, onMounted, onBeforeUnmount } from 'vue'
import { NSpin, useMessage } from 'naive-ui'
import { MessageSquare } from './icons'
import MessageBubble from './MessageBubble.vue'
import InputArea from './InputArea.vue'
import TodoPanel from './TodoPanel.vue'
// QuestionPanel removed in 2026-07-09 — it duplicated
// QuestionModal's state (answers, multiAnswers) and created a
// race where the user could submit via the inline panel while
// the modal still showed "open" (or vice versa). The modal
// in App.vue is the single source of truth for question UI.
import { state, currentMessages, isStreaming, switchSession, loadMoreMessages, rollbackTo, forkSession } from '../stores/chat'

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

// onScroll is bound to the messages container. When the user
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

onMounted(() => {
  scrollToBottom()
})

onBeforeUnmount(() => {
  // No custom scroll listener to remove — onScroll is bound
  // by Vue's @scroll on the template element, which Vue
  // auto-cleans on unmount.
})

function handleRollback(index: number) {
  if (!state.currentID) return
  rollbackTo(state.currentID, index)
}

async function handleFork(index: number) {
  if (!state.currentID) return
  // Fork at the assistant reply that follows the user message so
  // the forked conversation ends with a complete exchange instead
  // of a dangling user message.
  const msgs = state.sessionMessages[state.currentID]
  const asstIdx = index + 1
  const targetIdx = (msgs && asstIdx < msgs.length && msgs[asstIdx].role === 'assistant')
    ? asstIdx : index
  message.info('正在创建分支对话...')
  try {
    await forkSession(state.currentID, targetIdx)
    message.success('已创建分支对话')
  } catch (e) {
    console.error('fork failed:', e)
    message.error('创建分支对话失败')
  }
}
</script>

<template>
  <main class="chat-main">
    <!-- Plain scrollable container. We don't use NScrollbar
         here because its :native-scrollbar="false" path wraps
         the content in a custom-scrollbar div that conflicts
         with the parent flex layout: the inner
         .n-scrollbar-container gets its height from the slot
         content, not the parent, so it can't reliably fill
         the available space — the input area below used to
         drift up and overlap the message list. A plain
         `overflow-y: auto` on a flex: 1 / min-height: 0
         container is the most reliable pattern for this
         layout: the browser's native scrollbar works in
         every rendering mode (no race with NScrollbar's
         scroll-listener attach in onMounted) and the input
         below stays pinned to the bottom of the viewport
         even when the message list is short. -->
    <div
      ref="messagesEl"
      class="messages-scroll"
      @scroll="onScroll"
    >
      <div class="messages">
        <div v-if="currentMessages.length === 0" class="empty">
          <div class="empty-icon">
            <MessageSquare :size="48" />
          </div>
          <div class="empty-title">开始一个新对话吧</div>
          <div class="empty-hint">输入 /help 查看可用命令 · 拖入或粘贴文件可作为附件</div>
        </div>
        <div
          v-if="state.sessionPaging[state.currentID]?.loading"
          class="history-loading"
        >加载更早的消息…</div>
        <MessageBubble
          v-for="(m, i) in currentMessages"
          :key="m.id ?? `tmp-${i}-${(m.content || '').length}-${m.role}`"
          :message="m"
          :streaming="isStreaming && i === currentMessages.length - 1 && m.role === 'assistant'"
          @rollback="handleRollback(i)"
          @fork="handleFork(i)"
        />
      </div>
    </div>
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
  /* `height: 0` here is the magic ingredient that makes the
   * messages-scroll child actually shrink when the viewport
   * is short. Without it the flex parent can grow to fit its
   * tallest child and the input-area gets pushed off the
   * bottom. With it, flex children with min-height: 0 can
   * shrink below their content size and the input-area
   * below is guaranteed to stay at the bottom. */
  min-height: 0;
  overflow: hidden;
}
.messages-scroll {
  /* `flex: 1` makes the message list grow to fill the space
   * above the input. `min-height: 0` lets it shrink below
   * its content size (required for overflow to work in a
   * flex column). `overflow-y: auto` gives the browser a
   * native scrollbar when the message list overflows.
   * `position: relative` so the input area's floating UI
   * (e.g. the model picker, command palette) can anchor to
   * it as a positioning context if needed in the future. */
  flex: 1 1 0;
  min-height: 0;
  overflow-y: auto;
  overflow-x: hidden;
  position: relative;
  /* Pretty scrollbar: thin in Chromium / WebKit, falls
   * back to the default in Firefox. Keeps the message list
   * from being eaten by a chunky native scrollbar on
   * Windows. */
  scrollbar-width: thin;
  scrollbar-color: var(--border-default) transparent;
}
.messages-scroll::-webkit-scrollbar {
  width: 8px;
}
.messages-scroll::-webkit-scrollbar-track {
  background: transparent;
}
.messages-scroll::-webkit-scrollbar-thumb {
  background: var(--border-default);
  border-radius: 4px;
}
.messages-scroll::-webkit-scrollbar-thumb:hover {
  background: var(--text-quaternary);
}
.messages {
  /* No min-height: 100% here — that breaks overflow because
   * the parent doesn't have a defined height for it to
   * resolve against. The empty state is vertically centred
   * via flex on `.empty` so the page still looks balanced
   * when there are no messages. */
  padding: 12px 0;
  display: flex;
  flex-direction: column;
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
.empty-icon { font-size: 48px; color: var(--text-quaternary); display: inline-flex; }
.empty-title { font-size: 15px; color: var(--text-2); }
.empty-hint { font-size: 12px; color: var(--text-4); }
</style>
