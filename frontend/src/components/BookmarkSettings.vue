<script setup lang="ts">
/**
 * BookmarkSettings — global bookmark browser inside app settings.
 * Groups pins as project, then conversation, then the pinned line.
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useMessage } from 'naive-ui'
import * as api from '../api/client'
import { jumpToBookmarkedMessage, state } from '../stores/chat'

const message = useMessage()
const items = ref<api.BookmarkIndexItem[]>([])
const loading = ref(false)

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
        <button
          v-for="item in conversation.items"
          :key="`${item.conversation_id}-${item.message_id}`"
          type="button"
          class="bookmark-line"
          @click="openItem(item)"
        >
          {{ item.snippet || '附件' }}
        </button>
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
  display: block;
  width: 100%;
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
.bookmark-line:hover {
  background: var(--surface-2);
  color: var(--text-primary);
}
</style>
