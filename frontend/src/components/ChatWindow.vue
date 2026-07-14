<script setup lang="ts">
import { ref, nextTick, watch, computed, onMounted, onBeforeUnmount } from 'vue'
import { NSpin, useMessage } from 'naive-ui'
import { ArrowDown, MessageSquare } from './icons'
import MessageBubble from './MessageBubble.vue'
import InputArea from './InputArea.vue'
import TodoPanel from './TodoPanel.vue'
// QuestionPanel removed in 2026-07-09 — it duplicated
// QuestionModal's state (answers, multiAnswers) and created a
// race where the user could submit via the inline panel while
// the modal still showed "open" (or vice versa). The modal
// in App.vue is the single source of truth for question UI.
import { state, currentMessages, isStreaming, switchSession, loadMoreMessages, rollbackTo, forkSession, setUIMessageHandler } from '../stores/chat'

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

// Sticky-bottom: track whether the user is "at the bottom"
// (within SCROLL_BOTTOM_THRESHOLD pixels of the bottom edge).
// New content only auto-scrolls when this is true; when the
// user has scrolled up to read history, the viewport stays
// put and a "jump to bottom" button invites them back. The
// old code always scrolled to bottom on any message change,
// which yanked the user away from history the moment any
// SSE event landed, and worse, snapped them to the bottom
// whenever they scrolled to the top to load more history
// (loadMoreMessages → prepend → deep watcher → scrollToBottom).
const SCROLL_BOTTOM_THRESHOLD = 200
const isAtBottom = ref(true)
const showJumpToBottom = computed(() => !isAtBottom.value)

function updateScrollPosition(el: HTMLElement) {
  // scrollTop + clientHeight is the bottom edge of the
  // visible viewport; if it sits within THRESHOLD of
  // scrollHeight the user is "at the bottom". < 0 (overscroll
  // bounce) is clamped to 0.
  const distFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight
  isAtBottom.value = distFromBottom < SCROLL_BOTTOM_THRESHOLD
}

function scrollToBottom() {
  nextTick(() => {
    if (messagesEl.value) {
      messagesEl.value.scrollTo({ top: 99999, behavior: 'smooth' })
    }
  })
}

// jumpToBottom is wired to the floating button. We mark
// isAtBottom=true BEFORE scrolling so the deep watcher
// (which gates on isAtBottom) takes over follow-scrolling
// for any in-flight streaming chunks — otherwise the smooth
// scroll would race with each new chunk's auto-scroll and
// the user would see a stuttering animation.
function jumpToBottom() {
  isAtBottom.value = true
  scrollToBottom()
}

// onScroll is bound to the messages container. Two jobs:
//   1. Refresh isAtBottom so the button can show/hide.
//   2. When the user scrolls within ~80px of the top, kick
//      off the next page load (loadMoreMessages).
async function onScroll(e: Event) {
  if (!state.currentID) return
  const target = e.target as HTMLElement
  updateScrollPosition(target)
  if (target.scrollTop < 80) {
    wasAtTop.value = true
    prevScrollHeight.value = target.scrollHeight
    prevScrollTop.value = target.scrollTop
    await loadMoreMessages(state.currentID)
  }
}

// Length watcher: handles appends (user sent a message) and
// prepends (loadMoreMessages prepended an older page). The
// wasAtTop branch preserves the user's reading position; the
// isAtBottom branch is the sticky-bottom follow; the
// "scrolled up" branch is intentionally a no-op so the user
// can keep reading whatever they were looking at.
watch(() => currentMessages.value.length, (newLen, oldLen) => {
  if (newLen <= (oldLen || 0)) return
  if (wasAtTop.value) {
    nextTick(() => {
      if (!messagesEl.value) return
      const el = messagesEl.value
      const heightDelta = el.scrollHeight - prevScrollHeight.value
      el.scrollTop = prevScrollTop.value + heightDelta
      wasAtTop.value = false
    })
    return
  }
  if (isAtBottom.value) {
    nextTick(() => scrollToBottom())
  }
})

// Deep watcher: handles streaming content changes (text deltas,
// thinking deltas, tool updates). The length doesn't change
// during streaming, so the length watcher above doesn't fire.
// We only auto-scroll when the user is at the bottom — if
// they've scrolled up to read history, leave the viewport
// alone and let the jump-to-bottom button pull them back.
watch(
  () => currentMessages.value,
  () => {
    if (isAtBottom.value) {
      nextTick(() => scrollToBottom())
    }
  },
  { deep: true }
)

watch(() => state.currentID, () => {
  // A new session is a fresh slate: assume the user wants to
  // land on the latest message.
  isAtBottom.value = true
  nextTick(() => scrollToBottom())
})

onMounted(() => {
  scrollToBottom()
  // Bridge Naive UI's useMessage() to the chat store. The store
  // can't call useMessage() itself (no component context), so it
  // publishes errors here and we surface them as toasts. Without
  // this, store-side errors like "rollback failed: id not set"
  // would only appear in the console and the user would see a
  // dialog that closes without effect.
  setUIMessageHandler((m) => {
    if (m.kind === 'error') message.error(m.text)
    else message.info(m.text)
  })
})

onBeforeUnmount(() => {
  // Unregister so the store doesn't keep a reference to a torn-
  // down component's message instance.
  setUIMessageHandler(null)
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

// messageKey produces a stable Vue :key for a message in the
// v-for list. We prefer seq (the per-conversation logical
// position, stable across rollback/undo) and fall back to
// id (the physical row id) for older messages that haven't
// been backfilled. The trailing `i` + content fingerprint is
// a last-resort fallback for any pre-id streaming message
// (only relevant during the first few ms of a user turn
// before the server returns the row id).
//
// The fallback content fingerprint is needed so a streaming
// message whose id changes mid-turn (e.g. id is filled in
// after the first SSE event) doesn't trigger Vue to
// unmount+remount the bubble on every event.
function messageKey(m: any, i: number): string | number {
  if (m.seq != null && m.seq > 0) return m.seq
  if (m.id != null) return `id-${m.id}`
  return `tmp-${i}-${(m.content || '').length}-${m.role}`
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
          :key="messageKey(m, i)"
          :message="m"
          :streaming="isStreaming && i === currentMessages.length - 1 && m.role === 'assistant'"
          @rollback="handleRollback(i)"
          @fork="handleFork(i)"
        />
      </div>
    </div>
    <TodoPanel />
    <InputArea />
    <!-- Floating "jump to latest" button. Shown when the user
         has scrolled up away from the bottom; clicking it
         smooth-scrolls back to the latest message. Sits
         absolutely above the input/todo area so it doesn't
         shift the message list when it appears or hides. -->
    <Transition name="jump-btn">
      <button
        v-if="showJumpToBottom"
        class="jump-to-bottom"
        type="button"
        aria-label="跳到最新消息"
        title="跳到最新消息"
        @click="jumpToBottom"
      >
        <ArrowDown :size="18" />
      </button>
    </Transition>
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
  /* Anchor for the absolutely-positioned jump-to-bottom
   * button. Without this, the button would position itself
   * against the nearest other positioned ancestor and could
   * drift outside the chat column. */
  position: relative;
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

/* Floating "jump to latest" button. Anchored to .chat-main's
 * right edge, hovering above the input area. The 130px bottom
 * clears the collapsed TodoPanel (36px) + the typical input
 * area (~82px) + a small visual margin. When the rollback
 * banner is showing or advanced inputs are expanded the input
 * area grows and the button visually sits a bit closer to the
 * top edge of the input — acceptable trade-off vs. measuring
 * the input height from JS. */
.jump-to-bottom {
  position: absolute;
  right: 16px;
  bottom: 130px;
  width: 36px;
  height: 36px;
  border-radius: 50%;
  background: var(--bg-2);
  border: 1px solid var(--border-default);
  color: var(--text-2);
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.18);
  z-index: 10;
  transition: background 0.15s var(--ease-out, ease), color 0.15s var(--ease-out, ease), transform 0.15s var(--ease-out, ease);
}
.jump-to-bottom:hover {
  background: var(--bg-3);
  color: var(--text-1);
  transform: translateY(-1px);
}
.jump-to-bottom:active {
  transform: translateY(0);
}
/* Fade in from below when the user first scrolls up, fade out
 * when they scroll back to the bottom. */
.jump-btn-enter-active,
.jump-btn-leave-active {
  transition: opacity 0.2s var(--ease-out, ease), transform 0.2s var(--ease-out, ease);
}
.jump-btn-enter-from,
.jump-btn-leave-to {
  opacity: 0;
  transform: translateY(8px);
}
</style>
