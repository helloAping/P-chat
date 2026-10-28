<script setup lang="ts">
import { ref, nextTick, watch, computed, onMounted, onBeforeUnmount } from 'vue'
import { useMessage } from 'naive-ui'
import {
  ArrowDown, ArrowUp, MessageSquare, Loader2,
  Code2, FlaskConical, ImageIcon, Lightbulb,
} from './icons'
import MessageBubble from './MessageBubble.vue'
import InputArea from './InputArea.vue'
import TodoPanel from './TodoPanel.vue'
import SubAgentJobsPanel from './SubAgentJobsPanel.vue'
import StreamingBar from './StreamingBar.vue'
import QuestionModal from './QuestionModal.vue'
// QuestionPanel removed in 2026-07-09 — it duplicated
// QuestionModal's state (answers, multiAnswers) and created a
// race where the user could submit via the inline panel while
// the modal still showed "open" (or vice versa). The modal
// in App.vue is the single source of truth for question UI.
import { state, currentMessages, isStreaming, switchSession, loadMoreMessages, rollbackTo, forkSession, setUIMessageHandler, currentRecoveryBanner, refreshLatestMessages, setSessionBackgroundHookMerging, completeSearchJump, prefillComposer, registerProjectAndOpen, addAttachments, currentMeta, activeProjectName } from '../stores/chat'
import { EMPTY_GUIDES, sceneById, type SceneId } from '../utils/scenePresets'
import { inspectDataTransfer } from '../utils/dropActions'
import {
  styleGreeting,
  isStyleGreetingHidden,
  setStyleGreetingHidden,
} from '../utils/styleGreetings'
import * as api from '../api/client'

// isRecoveringCurrent mirrors state.isRecovering[currentID].
// Watched reactively by the recovery-in-progress banner
// template below.
const isRecoveringCurrent = computed(() => !!state.isRecovering[state.currentID])

const messagesEl = ref<HTMLElement | null>(null)
const message = useMessage()

const emptyGuides = EMPTY_GUIDES
const emptyGuideIcon: Record<string, typeof MessageSquare> = {
  explain: MessageSquare,
  'find-bug': Code2,
  'write-test': FlaskConical,
  sketch: ImageIcon,
}

async function onEmptyGuideClick(guideId: string) {
  if (!state.currentID) return
  const guide = EMPTY_GUIDES.find(item => item.id === guideId)
  if (!guide) return
  try {
    await applyGuideScene(guide.scene)
    prefillComposer(guide.prompt)
  } catch (e: any) {
    message.error(`无法填入引导：${e?.message || e}`)
  }
}

async function applyGuideScene(sceneId: SceneId) {
  const id = state.currentID
  if (!id) return
  const scene = sceneById(sceneId)
  const meta = state.sessionMeta[id] || {}
  const patch: Parameters<typeof api.updateSessionMeta>[1] = {}
  if ((meta.workMode || state.globalWorkMode || 'coding') !== scene.workMode) {
    patch.work_mode = scene.workMode
  }
  const currentTurn = meta.turn_mode_policy || (meta.plan_mode ? 'plan' : 'build')
  if (currentTurn !== scene.turnMode) {
    patch.turn_mode_policy = scene.turnMode
    patch.plan_mode = scene.turnMode === 'plan'
  }
  if (Object.keys(patch).length === 0) return
  const resp = await api.updateSessionMeta(id, patch)
  state.sessionMeta[id] = {
    ...meta,
    workMode: resp.work_mode ?? patch.work_mode ?? meta.workMode,
    turn_mode_policy: resp.turn_mode_policy ?? patch.turn_mode_policy ?? meta.turn_mode_policy,
    plan_mode: resp.plan_mode ?? patch.plan_mode ?? meta.plan_mode,
  }
  const session = state.sessions.find(s => s.id === id)
  if (session) {
    if (patch.work_mode) session.work_mode = state.sessionMeta[id].workMode
    if (patch.turn_mode_policy) {
      session.turn_mode_policy = state.sessionMeta[id].turn_mode_policy
      session.plan_mode = state.sessionMeta[id].plan_mode
    }
  }
}

const needsSetup = computed(() => (state.providers || []).length === 0)
const dropOverChat = ref(false)
const greetingHidden = ref(isStyleGreetingHidden())
const greetingText = computed(() =>
  styleGreeting(currentMeta.value.style, activeProjectName.value),
)
const greetingKey = computed(() =>
  `${currentMeta.value.style || 'off'}|${activeProjectName.value}|${greetingText.value}`,
)

function hideGreeting() {
  setStyleGreetingHidden(true)
  greetingHidden.value = true
}

function openFirstRun() {
  ;(window as any).openFirstRunWizard?.()
}

function onChatDragOver(e: DragEvent) {
  if (!e.dataTransfer?.types || !Array.from(e.dataTransfer.types).includes('Files')) return
  e.preventDefault()
  dropOverChat.value = true
}

function onChatDragLeave() {
  dropOverChat.value = false
}

async function onChatDrop(e: DragEvent) {
  e.preventDefault()
  dropOverChat.value = false
  const inspected = await inspectDataTransfer(e.dataTransfer)
  if (inspected.kind === 'folder') {
    if (!inspected.folderPath) {
      message.info(`检测到文件夹「${inspected.folderName}」，请用侧栏添加项目并选取目录。`, { duration: 4500 })
      ;(window as any).openAddProject?.()
      return
    }
    try {
      await registerProjectAndOpen(inspected.folderPath, inspected.folderName)
      message.success(`已打开项目「${inspected.folderName || inspected.folderPath}」`)
    } catch (err: any) {
      message.error(`添加项目失败：${err?.message || err}`)
    }
    return
  }
  if (inspected.kind !== 'files' || inspected.files.length === 0) return
  try {
    await addAttachments(inspected.files)
    const allImages = inspected.files.every(f => (f.type || '').startsWith('image/'))
    prefillComposer(allImages ? '请看看这些图片，并简要说明内容。' : '请看看这些附件，并告诉我要点。')
    message.success(`已添加 ${inspected.files.length} 个附件`)
  } catch (err: any) {
    message.error(`上传失败：${err?.message || err}`)
  }
}

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

// P1-4: 锚定 FAB (jump-to-user-message) state.
//
// distanceFromBottom is the raw pixel distance (positive
// when scrolled up). The anchor FAB shows when this
// exceeds ANCHOR_THRESHOLD (50px) — far enough that the
// user has clearly moved away from the latest message,
// but not so far that the existing SCROLL_BOTTOM_THRESHOLD
// (200px, used for the jump-to-bottom button) is also
// redundant. The 50px value is the user's "redundancy
// threshold" — at <= 50px the user can scroll back
// themselves and a button would be noise.
//
// visibleMsgIds is the set of message ids currently in
// the viewport. Recomputed on every scroll event via
// querySelectorAll('[data-msg-id]'); the Set construction
// is O(visible_messages), not O(all_messages), so even
// very long sessions (1000s of messages) only pay for
// what's actually on screen. We pair distanceFromBottom
// with hasArchivedInView to decide whether the anchor
// FAB should render — the user must BOTH be scrolled
// up AND have an archived (◀ N/M ▶ paginated) reply
// in the viewport for the FAB to make sense.
const ANCHOR_THRESHOLD = 50
const distanceFromBottom = ref(0)
const visibleMsgIds = ref<Set<number>>(new Set())

const hasArchivedInView = computed(() => {
  const ids = visibleMsgIds.value
  if (ids.size === 0) return false
  // Linear scan over the session messages looking for
  // an archived (is_archived=true) row whose id is in
  // the visible set. O(N) over all session messages;
  // fine for sessions under a few hundred rows (the
  // common case). For very long sessions the dedup
  // in loadReplies keeps the per-bubble cost O(visible)
  // — we could later maintain a precomputed Set of
  // archived ids if profiling shows this is hot.
  const sid = state.currentID
  const msgs = sid ? state.sessionMessages[sid] : undefined
  if (!msgs) return false
  for (const m of msgs) {
    if (m.is_archived && m.id != null && ids.has(m.id)) return true
  }
  return false
})

const showAnchorFAB = computed(() =>
  distanceFromBottom.value > ANCHOR_THRESHOLD && hasArchivedInView.value
)

function updateScrollPosition(el: HTMLElement) {
  // scrollTop + clientHeight is the bottom edge of the
  // visible viewport; if it sits within THRESHOLD of
  // scrollHeight the user is "at the bottom". < 0 (overscroll
  // bounce) is clamped to 0.
  const distFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight
  distanceFromBottom.value = distFromBottom
  isAtBottom.value = distFromBottom < SCROLL_BOTTOM_THRESHOLD
  // P1-4: refresh the visible-msg set on every scroll
  // event. O(visible) — no full DOM walk, no expensive
  // selectors. The data-msg-id attribute is set on
  // every MessageBubble's root element so we can map
  // DOM nodes back to message ids without maintaining
  // a side index.
  const newSet = new Set<number>()
  const nodes = el.querySelectorAll('[data-msg-id]')
  const viewportTop = 0
  const viewportBottom = el.clientHeight
  nodes.forEach((node) => {
    const rect = (node as HTMLElement).getBoundingClientRect()
    // Skip nodes that are completely above or below
    // the viewport (rare with the CSS layout but
    // possible for huge bubbles). This keeps the set
    // truly "visible" rather than "in the DOM".
    if (rect.bottom <= viewportTop || rect.top >= viewportBottom) return
    const idAttr = node.getAttribute('data-msg-id')
    if (idAttr) {
      const n = Number(idAttr)
      if (Number.isFinite(n) && n > 0) newSet.add(n)
    }
  })
  visibleMsgIds.value = newSet
}

// scrollToUserMsg scrolls the user message associated
// with the currently-visible archived reply back into
// the viewport. Used by the P1-4 锚定 FAB.
//
// Algorithm:
//   1. Find the first archived (is_archived=true) message
//      id in the visible-msg set.
//   2. Read its regen_group_id (= user message id).
//   3. DOM-query `[data-msg-id="<user>"]`.
//   4. If the user message is currently visible, do
//      nothing (the user can see the context already).
//   5. Otherwise scrollIntoView({block: 'center'}).
//
// We pick the FIRST archived sibling (not "the
// most-recently-toggled one") because the anchor FAB is
// a generic helper — any archived reply the user is
// currently looking at should be backed by a visible
// user message.
function scrollToUserMsg() {
  const sid = state.currentID
  const msgs = sid ? state.sessionMessages[sid] : undefined
  if (!msgs) return
  let userMsgId: number | null = null
  for (const id of visibleMsgIds.value) {
    const m = msgs.find((x) => x.id === id)
    if (m && m.is_archived && m.regen_group_id) {
      userMsgId = Number(m.regen_group_id)
      break
    }
  }
  if (!userMsgId || !Number.isFinite(userMsgId) || userMsgId <= 0) return
  const el = messagesEl.value?.querySelector(
    `[data-msg-id="${userMsgId}"]`,
  ) as HTMLElement | null
  if (!el) return
  const rect = el.getBoundingClientRect()
  // Skip the scroll if the user message is already in
  // the viewport. The FAB's visible-button check is
  // coarse (it just looks for any archived reply in
  // view), so this fine-grained check is what keeps
  // the click from being a no-op for the user.
  const elTop = rect.top
  const elBottom = rect.bottom
  const viewportHeight = window.innerHeight || 0
  if (elTop >= 0 && elBottom <= viewportHeight) return
  el.scrollIntoView({ behavior: 'smooth', block: 'center' })
}

let scrollFrame: number | null = null
let pendingSmoothScroll = false

// Coalesce stream-driven follow scrolling to one layout operation
// per frame. Smooth animation is reserved for an explicit user
// action; starting one animation for every token causes the visible
// flashing reported during long tool-heavy tasks.
function scrollToBottom(smooth = false) {
  pendingSmoothScroll ||= smooth
  if (scrollFrame !== null) return
  scrollFrame = requestAnimationFrame(() => {
    scrollFrame = null
    const useSmooth = pendingSmoothScroll
    pendingSmoothScroll = false
    nextTick(() => {
      const el = messagesEl.value
      if (!el) return
      if (useSmooth) {
        el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
      } else {
        el.scrollTop = el.scrollHeight
      }
    })
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
  scrollToBottom(true)
}

function locateOpenQuestion() {
  const el = messagesEl.value?.querySelector('.question-card.status-open') as HTMLElement | null
  if (!el) {
    jumpToBottom()
    return
  }
  el.scrollIntoView({ behavior: 'smooth', block: 'center' })
}

// 搜索定位只在 Vue 完成本轮 DOM 更新后解析，成功/失败都回传 store。
// Search navigation resolves only after Vue has applied the current DOM patch;
// both success and failure settle the store promise so the sidebar never drops
// an asynchronous error silently.
watch(() => state.searchJumpTarget, async (target) => {
  if (!target || target.sessionID !== state.currentID) return
  await nextTick()
  if (state.searchJumpTarget?.requestID !== target.requestID) return
  const el = messagesEl.value?.querySelector(
    `[data-msg-id="${target.messageID}"]`,
  ) as HTMLElement | null
  if (!el) {
    completeSearchJump(target.requestID, '消息已加载，但页面中没有对应的可见气泡')
    return
  }
  el.scrollIntoView({ behavior: 'smooth', block: 'center' })
  // 重复点击同一结果时重启单次动画；时长仅由 CSS token 控制。
  // Restart the one-shot animation for repeated hits; CSS owns its duration.
  el.classList.remove('search-hit-flash')
  void el.offsetWidth
  el.classList.add('search-hit-flash')
  el.addEventListener('animationend', () => el.classList.remove('search-hit-flash'), { once: true })
  completeSearchJump(target.requestID)
})

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
    scrollToBottom()
  }
})

// streamRevision changes once per applied stream event. Watching it
// avoids a deep traversal through every message and nested part on
// each token while preserving sticky-bottom behavior.
watch(
  () => state.streamRevision[state.currentID] || 0,
  () => {
    if (isAtBottom.value) {
      scrollToBottom()
    }
  },
)

watch(() => state.currentID, () => {
  // A new session is a fresh slate: assume the user wants to
  // land on the latest message.
  isAtBottom.value = true
  scrollToBottom()
})

watch(() => state.viewLoading, (loading) => {
  if (loading) return
  isAtBottom.value = true
  scrollToBottom()
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
  const searchTarget = state.searchJumpTarget
  if (searchTarget) {
    completeSearchJump(searchTarget.requestID, '聊天视图已关闭，未能完成消息定位')
  }
  // Unregister so the store doesn't keep a reference to a torn-
  // down component's message instance.
  setUIMessageHandler(null)
  if (scrollFrame !== null) cancelAnimationFrame(scrollFrame)
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

async function handleSubAgentJobTerminal() {
  if (!state.currentID) return
  const sessionID = state.currentID
  setSessionBackgroundHookMerging(sessionID, true)
  try {
    await refreshLatestMessages(sessionID)
  } finally {
    setSessionBackgroundHookMerging(sessionID, false)
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
  <main
    class="chat-main"
    :class="{ 'chat-main--drop': dropOverChat }"
    @dragover="onChatDragOver"
    @dragleave="onChatDragLeave"
    @drop="onChatDrop"
  >
    <!-- Conversation-in-progress loading bar. Thin indeterminate
         strip at the top of the chat page; visible from the moment
         a turn starts until it ends (isStreaming || sessionWorking). -->
    <StreamingBar />
    <!-- P0-1: transient banner shown when the recovery
         flow successfully merged server-side parts into
         the trailing assistant message. currentRecoveryBanner
         auto-clears after 3s (driven by the chat store). -->
    <Transition name="recovery-banner">
      <div
        v-if="currentRecoveryBanner"
        class="recovery-banner"
        :class="{ 'recovery-banner--interrupt': currentRecoveryBanner.kind === 'interrupt' }"
        :key="currentRecoveryBanner.shownAt"
      >
        <span class="recovery-icon">{{ currentRecoveryBanner.kind === 'interrupt' ? '⚠' : '📥' }}</span>
        <span class="recovery-text">
          {{ currentRecoveryBanner.kind === 'interrupt' ? '对话已中断' : `已恢复 ${currentRecoveryBanner.recovered} 条消息` }}
        </span>
        <span class="recovery-reason">{{ currentRecoveryBanner.reason }}</span>
      </div>
    </Transition>
    <!-- P0-1: in-progress banner. Shown the moment the
         onStreamDrop callback fires and the
         recoverMissingParts async work starts; cleared
         when the snapshot fetch resolves (whether or
         not any parts actually merged). Distinct from
         the success banner above so the user knows the
         system is doing work, not hung. -->
    <Transition name="recovery-banner">
      <div
        v-if="isRecoveringCurrent && !currentRecoveryBanner"
        class="recovery-banner recovery-banner--pending"
      >
        <span class="recovery-icon">⚡</span>
        <span class="recovery-text">正在补齐未送达消息…</span>
      </div>
    </Transition>
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
    <div class="messages-pane">
      <div
        ref="messagesEl"
        class="messages-scroll"
        :aria-busy="state.viewLoading ? 'true' : undefined"
        @scroll="onScroll"
      >
      <div class="messages" :class="{ 'messages--switching': state.viewLoading }">
        <div v-if="!state.viewLoading && currentMessages.length === 0" class="empty">
          <div class="empty-icon">
            <MessageSquare :size="40" />
          </div>
          <div class="empty-title">开始一个新对话吧</div>
          <Transition name="greet-fade" mode="out-in">
            <p
              v-if="!greetingHidden"
              :key="greetingKey"
              class="empty-greeting"
            >
              <span>{{ greetingText }}</span>
              <button
                type="button"
                class="empty-greeting-hide"
                title="不再显示问候"
                aria-label="不再显示问候"
                @click="hideGreeting"
              >×</button>
            </p>
          </Transition>
          <div class="empty-hint">点一张卡片填入提示，确认后再发送 · 也可拖入文件夹开项目，或拖入附件</div>
          <button
            v-if="needsSetup"
            type="button"
            class="empty-setup"
            @click="openFirstRun"
          >
            还没有模型，点此 60 秒完成配置
          </button>
          <div class="empty-guides" role="list">
            <button
              v-for="guide in emptyGuides"
              :key="guide.id"
              type="button"
              class="empty-guide"
              role="listitem"
              :title="guide.description"
              @click="onEmptyGuideClick(guide.id)"
            >
              <component :is="emptyGuideIcon[guide.id] || Lightbulb" :size="16" class="empty-guide-icon" />
              <span class="empty-guide-title">{{ guide.title }}</span>
              <span class="empty-guide-desc">{{ guide.description }}</span>
            </button>
          </div>
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
      <Transition name="view-loading">
        <div
          v-if="state.viewLoading"
          class="view-loading"
          role="status"
          aria-live="polite"
        >
          <Loader2 :size="18" class="view-loading-spin" />
          <span class="view-loading-hint">{{ state.viewLoadingHint || '正在加载对话…' }}</span>
        </div>
      </Transition>
    </div>
    <QuestionModal @locate-question="locateOpenQuestion" />
    <TodoPanel />
    <SubAgentJobsPanel
      :session-id="state.currentID"
      @job-terminal="handleSubAgentJobTerminal"
      @locate-result="jumpToBottom"
    />
    <InputArea />
    <!-- P1-4: 锚定 FAB (jump-to-user-message). Shown
         when the user is scrolled up beyond the 50px
         "redundancy threshold" AND has an archived
         (◀ N/M ▶ paginated) reply in the viewport. The
         button sits stacked above the jump-to-bottom
         button (bottom: 64px) so the two never
         overlap. Click scrolls the user message of
         the first archived reply in view back into
         the viewport — see scrollToUserMsg. -->
    <Transition name="anchor-btn">
      <button
        v-if="showAnchorFAB"
        class="anchor-fab"
        type="button"
        aria-label="跳到用户消息"
        title="跳到用户消息"
        @click="scrollToUserMsg"
      >
        <ArrowUp :size="16" />
      </button>
    </Transition>
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
/* P0-1 recovery banner. Sits at the top of the chat
 * main area and animates in/out via Vue's <Transition>.
 * Uses brand accent for the icon and a soft surface
 * background so it reads as "informational" rather
 * than "error". */
.recovery-banner {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  background: var(--surface-2);
  border-bottom: 1px solid var(--border-subtle);
  font-size: 12.5px;
  color: var(--text-secondary);
  flex-shrink: 0;
}
/* P0-1 in-progress variant: subtle pulse + warmer hue so
   the user can tell this is "system is doing work",
   not "system finished and recovered 0 messages" (which
   the success banner above also covers). */
.recovery-banner--pending {
  background: var(--surface-2);
  border-bottom-color: var(--brand-500);
}
.recovery-banner--pending .recovery-icon {
  animation: recovery-pulse 1.4s ease-in-out infinite;
  display: inline-block;
}
.recovery-banner--interrupt {
  border-bottom-color: var(--warn-500);
}
@keyframes recovery-pulse {
  0%, 100% { opacity: 0.5; transform: scale(0.95); }
  50%      { opacity: 1.0; transform: scale(1.05); }
}
.recovery-icon { font-size: 14px; }
.recovery-text { color: var(--text-primary); font-weight: 500; }
.recovery-reason {
  margin-left: auto;
  color: var(--text-tertiary);
  font-size: 11px;
  font-family: var(--font-mono);
}
.recovery-banner-enter-from {
  opacity: 0;
  transform: translateY(-6px);
}
.recovery-banner-leave-to {
  opacity: 0;
}
.recovery-banner-enter-active {
  transition: opacity var(--dur-base) var(--ease-out),
              transform var(--dur-base) var(--ease-out);
}
.recovery-banner-leave-active {
  transition: opacity var(--dur-fast) var(--ease-in),
              transform var(--dur-fast) var(--ease-in);
}
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
.messages-pane {
  flex: 1 1 0;
  min-height: 0;
  position: relative;
  display: flex;
  flex-direction: column;
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
  border-radius: var(--radius-pill);
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
  padding: var(--space-3) 0;
  display: flex;
  flex-direction: column;
  transition: opacity var(--dur-slow) var(--ease-out),
              transform var(--dur-slow) var(--ease-out);
}
.messages--switching {
  opacity: 0;
  transform: translateY(8px);
  pointer-events: none;
}

/* Session/project switch veil. Covers the transcript so the
 * previous conversation does not hard-cut into the next one.
 * Opacity-only enter/leave; the spinner is compositor-only
 * transform (see frontend-design.md §8.4). */
.view-loading {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--space-3);
  background: var(--surface-0);
  color: var(--text-tertiary);
  z-index: 4;
}
.view-loading-hint {
  font-size: 12.5px;
  color: var(--text-tertiary);
}
.view-loading-spin {
  color: var(--brand-500);
  animation: view-load-spin 0.85s linear infinite;
}
@keyframes view-load-spin {
  to { transform: rotate(360deg); }
}
.view-loading-enter-active {
  transition: opacity var(--dur-fast) var(--ease-out);
}
.view-loading-leave-active {
  transition: opacity var(--dur-slow) var(--ease-out);
}
.view-loading-enter-from,
.view-loading-leave-to {
  opacity: 0;
}

/* Messages are virtualized via CSS `content-visibility: auto`:
 * the browser skips layout/paint for bubbles outside the
 * viewport while keeping them in the DOM (so scroll position,
 * infinite scroll, and the anchor FAB keep working unchanged).
 * With the store's 300-message cap this bounds render cost the
 * same way a JS virtual list would, without touching the
 * scroll bookkeeping that JS virtualization would break.
 *
 * `contain-intrinsic-size` gives the browser a fallback size
 * for skipped content so scrollbar/scrollHeight estimates stay
 * sane before a bubble is first laid out. */
.messages > * {
  content-visibility: auto;
  contain-intrinsic-size: auto 220px;
}
.history-loading {
  text-align: center;
  font-size: 12px;
  color: var(--text-4);
  padding: var(--space-2) 0;
  opacity: 0.7;
}
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: var(--text-3);
  gap: var(--space-2);
  padding: calc(var(--space-8) * 2) var(--space-4) var(--space-8);
  text-align: center;
}
.empty-icon { color: var(--text-quaternary); display: inline-flex; }
.empty-title { font-size: 15px; color: var(--text-2); }
.empty-greeting {
  display: inline-flex;
  align-items: flex-start;
  gap: var(--space-1);
  max-width: 28rem;
  margin: 0;
  padding: 6px 10px;
  border-radius: var(--radius-md);
  background: color-mix(in srgb, var(--brand-500) 7%, var(--surface-2));
  border: 1px solid color-mix(in srgb, var(--brand-500) 16%, var(--border-subtle));
  color: var(--text-2);
  font-size: 13px;
  line-height: 1.45;
  text-align: left;
}
.empty-greeting-hide {
  flex-shrink: 0;
  margin-top: -1px;
  padding: 0 2px;
  border: none;
  background: transparent;
  color: var(--text-4);
  cursor: pointer;
  font-size: 14px;
  line-height: 1;
}
.empty-greeting-hide:hover { color: var(--text-2); }
.greet-fade-enter-active,
.greet-fade-leave-active {
  transition: opacity var(--dur-base) var(--ease-out), transform var(--dur-base) var(--ease-out);
}
.greet-fade-enter-from,
.greet-fade-leave-to {
  opacity: 0;
  transform: translateY(4px);
}
.empty-hint { font-size: 12px; color: var(--text-4); max-width: 28rem; }
.empty-setup {
  margin-top: var(--space-1);
  padding: 6px 12px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  background: var(--surface-2);
  color: var(--text-2);
  font-size: 12px;
  cursor: pointer;
}
.empty-setup:hover {
  border-color: color-mix(in srgb, var(--brand-500) 28%, var(--border-default));
  background: color-mix(in srgb, var(--brand-500) 8%, var(--surface-2));
}
.chat-main--drop {
  outline: 1px dashed color-mix(in srgb, var(--brand-500) 40%, var(--border-default));
  outline-offset: -6px;
}
.empty-guides {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 11.5rem));
  gap: var(--space-2);
  margin-top: var(--space-3);
  width: min(100%, 24.5rem);
  text-align: left;
}
.empty-guide {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
  min-height: 4.5rem;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  background: var(--surface-2);
  color: var(--text-3);
  cursor: pointer;
  transition:
    background var(--dur-fast) var(--ease-out),
    border-color var(--dur-fast) var(--ease-out),
    color var(--dur-fast) var(--ease-out);
}
.empty-guide:hover {
  color: var(--text-2);
  border-color: color-mix(in srgb, var(--brand-500) 28%, var(--border-default));
  background: color-mix(in srgb, var(--brand-500) 8%, var(--surface-2));
}
.empty-guide-icon {
  color: var(--text-4);
  margin-bottom: 2px;
}
.empty-guide:hover .empty-guide-icon {
  color: var(--text-3);
}
.empty-guide-title {
  font-size: 13px;
  font-weight: 500;
  color: var(--text-2);
}
.empty-guide-desc {
  font-size: 11px;
  line-height: 1.35;
  color: var(--text-4);
}
@media (max-width: 560px) {
  .empty-guides {
    grid-template-columns: 1fr;
    width: min(100%, 16rem);
  }
}

/* Floating "jump to latest" button. Anchored to .chat-main's
 * right edge, hovering above the input area. The computed bottom
 * clears collapsed Todo (~36px) + queue summary strip (~32px) +
 * typical input (~82px) + a small visual margin. When docks
 * expand the button sits closer to the input top — acceptable
 * trade-off vs. measuring the composer height from JS. */
.jump-to-bottom {
  position: absolute;
  right: var(--space-4);
  bottom: calc(var(--space-8) * 5 + var(--space-2));
  width: calc(var(--control-height) + var(--space-2));
  height: calc(var(--control-height) + var(--space-2));
  border-radius: var(--radius-pill);
  background: var(--bg-2);
  border: 1px solid var(--border-default);
  color: var(--text-2);
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  box-shadow: var(--shadow-md);
  z-index: 10;
  transition: var(--transition-colors),
              transform var(--dur-fast) var(--ease-out);
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
.jump-btn-enter-active {
  transition: opacity var(--dur-base) var(--ease-out),
              transform var(--dur-base) var(--ease-out);
}
.jump-btn-leave-active {
  transition: opacity var(--dur-fast) var(--ease-in),
              transform var(--dur-fast) var(--ease-in);
}
.jump-btn-enter-from,
.jump-btn-leave-to {
  opacity: 0;
  transform: translateY(8px);
}

/* P1-4: 锚定 FAB. Sits stacked above the jump-to-bottom
 * button so the two never overlap. Same
 * size + shape + surface as the jump-to-bottom button
 * (visually consistent floating-button row) but uses
 * ArrowUp — pointing up to "the user message that
 * prompted this archived reply". The brand-tinted
 * background is the P1-4 visual hook (matches the
 * pager's color story). The brand-50 surface is subtle
 * enough to not compete with the message content but
 * distinct enough that the user notices the button
 * appeared. */
.anchor-fab {
  position: absolute;
  right: var(--space-4);
  bottom: calc(var(--space-8) * 6 + var(--space-6));
  width: calc(var(--control-height) + var(--space-2));
  height: calc(var(--control-height) + var(--space-2));
  border-radius: var(--radius-pill);
  background: var(--brand-50);
  border: 1px solid var(--brand-100);
  color: var(--brand-700);
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  box-shadow: var(--shadow-md);
  z-index: 10;
  transition: var(--transition-colors),
              transform var(--dur-fast) var(--ease-out);
}
.anchor-fab:hover {
  background: var(--brand-100);
  color: var(--brand-700);
  transform: translateY(-1px);
}
.anchor-fab:active {
  transform: translateY(0);
}
.anchor-fab:focus-visible {
  outline: 2px solid var(--brand-500);
  outline-offset: 2px;
}
.anchor-btn-enter-active {
  transition: opacity var(--dur-base) var(--ease-out),
              transform var(--dur-base) var(--ease-out);
}
.anchor-btn-leave-active {
  transition: opacity var(--dur-fast) var(--ease-in),
              transform var(--dur-fast) var(--ease-in);
}
.anchor-btn-enter-from,
.anchor-btn-leave-to {
  opacity: 0;
  transform: translateY(8px);
}

/* 搜索命中高亮是单次动画，颜色与时长都使用新设计 token。
   Search highlighting is one-shot and reads both color and duration from
   current design tokens. The attribute lives on MessageBubble's root. */
:deep(.search-hit-flash) {
  outline: 2px solid var(--brand-500);
  outline-offset: 2px;
  border-radius: var(--radius-lg);
  animation: search-hit-fade var(--dur-highlight) var(--ease-out) forwards;
}
@keyframes search-hit-fade {
  0% { outline-color: var(--brand-500); }
  70% { outline-color: var(--brand-500); }
  100% { outline-color: transparent; }
}
</style>
