<script setup lang="ts">
/**
 * BookmarkSettings — global bookmark browser inside app settings.
 * Groups pins as project, then conversation, then the pinned line.
 */
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { useMessage } from 'naive-ui'
import * as api from '../api/client'
import { jumpToBookmarkedMessage, state } from '../stores/chat'
import { copyText } from '../utils/clipboard'

const message = useMessage()
const items = ref<api.BookmarkIndexItem[]>([])
const loading = ref(false)
const rangeFrom = reactive<Record<string, string>>({})
const rangeTo = reactive<Record<string, string>>({})
const noteDraft = reactive<Record<string, string>>({})

interface ConversationGroup {
  id: string
  title: string
  items: api.BookmarkIndexItem[]
}

interface ProjectGroup {
  path: string
  label: string
  conversations: ConversationGroup[]
}

function projectLabel(path: string): string {
  if (!path) return '全局会话'
  return state.projects.find(project => project.path === path)?.name
    || path.split(/[/\\]/).filter(Boolean).pop()
    || path
}

const groups = computed<ProjectGroup[]>(() => {
  const byProject = new Map<string, ProjectGroup>()
  for (const item of items.value) {
    const path = item.project_path || ''
    let project = byProject.get(path)
    if (!project) {
      project = { path, label: projectLabel(path), conversations: [] }
      byProject.set(path, project)
    }
    let conversation = project.conversations.find(entry => entry.id === item.conversation_id)
    if (!conversation) {
      conversation = { id: item.conversation_id, title: item.title || '未命名对话', items: [] }
      project.conversations.push(conversation)
    }
    conversation.items.push(item)
  }
  const list = [...byProject.values()]
  list.sort((a, b) => {
    if (!a.path) return -1
    if (!b.path) return 1
    return a.label.localeCompare(b.label, 'zh')
  })
  return list
})

async function load() {
  loading.value = true
  try {
    const resp = await api.listAllBookmarks()
    items.value = resp.bookmarks || []
    for (const item of items.value) {
      noteDraft[noteKey(item)] = item.note || ''
    }
  } catch (err: unknown) {
    items.value = []
    const text = err instanceof Error ? err.message : '书签加载失败'
    message.error(text)
  } finally {
    loading.value = false
  }
}

async function openItem(item: api.BookmarkIndexItem) {
  try {
    await jumpToBookmarkedMessage(item.conversation_id, item.message_id, item.project_path || '')
    const close = (window as { closeAppSettings?: () => void }).closeAppSettings
    close?.()
  } catch (err: unknown) {
    const text = err instanceof Error ? err.message : '无法打开书签'
    message.error(text)
  }
}

function noteKey(item: api.BookmarkIndexItem): string {
  return `${item.conversation_id}:${item.message_id}`
}

async function saveNote(item: api.BookmarkIndexItem) {
  const note = (noteDraft[noteKey(item)] || '').trim()
  if ([...note].length > 200) {
    message.error('备注最多 200 字')
    return
  }
  try {
    await api.updateBookmarkNote(item.conversation_id, item.message_id, note)
    item.note = note
    noteDraft[noteKey(item)] = note
  } catch (err: unknown) {
    message.error(err instanceof Error ? err.message : '备注保存失败')
  }
}

function orderedPins(conversation: ConversationGroup): api.BookmarkIndexItem[] {
  return [...conversation.items].sort((a, b) => (a.seq || 0) - (b.seq || 0))
}

async function rangeMarkdown(conversation: ConversationGroup): Promise<string> {
  const pins = orderedPins(conversation).filter(item => (item.seq || 0) > 0)
  if (pins.length < 2) throw new Error('至少需要两条带序号的书签')
  const from = Number(rangeFrom[conversation.id] || pins[0].seq)
  const to = Number(rangeTo[conversation.id] || pins[pins.length - 1].seq)
  if (!Number.isInteger(from) || !Number.isInteger(to) || from > to) {
    throw new Error('请从较早的书签选到较晚的书签')
  }
  const path = `/api/v1/sessions/${encodeURIComponent(conversation.id)}/export?format=md&from_seq=${from}&to_seq=${to}`
  const url = await api.waitForAPIURL(path)
  const resp = await fetch(url)
  if (!resp.ok) throw new Error((await resp.text()) || '导出失败')
  return resp.text()
}

async function copyRange(conversation: ConversationGroup) {
  try {
    const text = await rangeMarkdown(conversation)
    const ok = await copyText(text)
    if (!ok) throw new Error('复制失败')
    message.success('已复制这段 Markdown')
  } catch (err: unknown) {
    message.error(err instanceof Error ? err.message : '导出失败')
  }
}

async function downloadRange(conversation: ConversationGroup) {
  try {
    const text = await rangeMarkdown(conversation)
    const blob = new Blob([text], { type: 'text/markdown;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `${conversation.title || 'bookmarks'}.md`
    link.click()
    URL.revokeObjectURL(url)
  } catch (err: unknown) {
    message.error(err instanceof Error ? err.message : '导出失败')
  }
}

onMounted(() => {
  void load()
  window.addEventListener('pchat:bookmarks-changed', load)
})
onUnmounted(() => {
  window.removeEventListener('pchat:bookmarks-changed', load)
})
</script>

<template>
  <div class="bookmark-settings">
    <div v-if="loading && !items.length" class="bookmark-empty">正在读取书签…</div>
    <div v-else-if="!groups.length" class="bookmark-empty">还没有书签。在消息上点书签后，会按项目收在这里。</div>
    <section v-for="project in groups" :key="project.path || 'global'" class="bookmark-project">
      <h3 class="bookmark-project-title">{{ project.label }}</h3>
      <div v-for="conversation in project.conversations" :key="conversation.id" class="bookmark-conversation">
        <div class="bookmark-conversation-title">{{ conversation.title }}</div>
        <div
          v-for="item in conversation.items"
          :key="`${item.conversation_id}-${item.message_id}`"
          class="bookmark-line"
        >
          <button type="button" class="bookmark-open" @click="openItem(item)">
            {{ item.snippet || '附件' }}
          </button>
          <input
            v-model="noteDraft[`${item.conversation_id}:${item.message_id}`]"
            class="bookmark-note"
            maxlength="200"
            placeholder="备注"
            @change="saveNote(item)"
          />
        </div>
        <div v-if="conversation.items.length >= 2" class="bookmark-range">
          <span>从这条到那条</span>
          <select v-model="rangeFrom[conversation.id]">
            <option v-for="item in orderedPins(conversation)" :key="`from-${item.message_id}`" :value="String(item.seq || '')">
              {{ item.snippet || '附件' }}
            </option>
          </select>
          <select v-model="rangeTo[conversation.id]">
            <option v-for="item in orderedPins(conversation)" :key="`to-${item.message_id}`" :value="String(item.seq || '')">
              {{ item.snippet || '附件' }}
            </option>
          </select>
          <button type="button" @click="copyRange(conversation)">复制</button>
          <button type="button" @click="downloadRange(conversation)">下载</button>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.bookmark-settings {
  max-width: 720px;
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.bookmark-empty {
  color: var(--text-tertiary);
  font-size: 13px;
}
.bookmark-project-title {
  margin: 0 0 var(--space-2);
  padding-bottom: var(--space-2);
  border-bottom: 1px solid var(--border-subtle);
  color: var(--text-secondary);
  font-size: 13px;
  font-weight: 600;
}
.bookmark-conversation {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  margin: 0 0 var(--space-3) var(--space-3);
}
.bookmark-conversation-title {
  color: var(--text-primary);
  font-size: 13px;
  font-weight: 600;
}
.bookmark-line {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.bookmark-open {
  flex: 1;
  min-width: 0;
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border: none;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--text-secondary);
  font: inherit;
  font-size: 13px;
  line-height: 1.5;
  text-align: left;
  cursor: pointer;
}
.bookmark-open:hover {
  background: var(--surface-2);
  color: var(--text-primary);
}
.bookmark-note {
  width: 160px;
  border: none;
  background: transparent;
  color: var(--text-secondary);
  font: inherit;
  font-size: 12px;
}
.bookmark-note:focus {
  outline: none;
  color: var(--text-primary);
}
.bookmark-range {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
  color: var(--text-tertiary);
  font-size: 12px;
}
.bookmark-range select,
.bookmark-range button {
  border: none;
  background: var(--surface-2);
  color: var(--text-secondary);
  font: inherit;
  font-size: 12px;
  border-radius: var(--radius-md);
  padding: 4px 8px;
  cursor: pointer;
}
</style>
