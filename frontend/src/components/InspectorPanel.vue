<script setup lang="ts">
/**
 * InspectorPanel — lightweight right-hand context panel.
 *
 * Tabs: 会话 | 项目. Sections use light cards so context is scannable
 * without turning the panel into a heavy dashboard.
 * Collapse is owned by the parent (v-if / width 0).
 */
import { computed, ref } from 'vue'
import { useMessage } from 'naive-ui'
import { state, currentMessages } from '../stores/chat'
import * as api from '../api/client'
import { formatCompactTokens } from '../utils/format'
import { copyText } from '../utils/clipboard'
import ToolListDrawer from './ToolListDrawer.vue'
import StyleGenModal from './StyleGenModal.vue'
import {
  Copy, Globe, Folder, FolderOpen, Terminal, Wrench, Sparkles, Hash, X,
} from './icons'

const props = defineProps<{
  open: boolean
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const message = useMessage()
const tab = ref<'session' | 'project'>('session')
const showToolList = ref(false)
const showStyleGen = ref(false)

const currentSession = computed(() =>
  state.sessions.find(s => s.id === state.currentID) || null,
)

const project = computed(() => {
  if (!state.activeProjectPath) return null
  return state.projects.find(p => p.path === state.activeProjectPath) || null
})

const projectName = computed(() => {
  if (!state.activeProjectPath) return '全局空间'
  return project.value?.name || state.activeProjectPath
})

const ctx = computed(() => state.contextInspector?.data || null)
const currentTraceId = computed(() => state.currentTraceId || '')
const canOpenProject = computed(() => !!state.activeProjectPath)

const ctxSummary = computed(() => {
  const d = ctx.value
  if (!d) return '暂无上下文数据'
  const model = d.model || '未知模型'
  return `${model} · ${formatCompactTokens(d.estimated_tokens)} / ${formatCompactTokens(d.context_window)}`
})

const idSnippet = computed(() => {
  const id = state.currentID
  if (!id) return '—'
  return id.length > 12 ? `${id.slice(0, 8)}…` : id
})

const toolStats = computed(() => {
  let ok = 0
  let err = 0
  let running = 0
  for (const msg of currentMessages.value) {
    for (const part of msg.parts || []) {
      if (part.kind !== 'tool') continue
      if (part.status === 'ok' || part.status === 'warn') ok++
      else if (part.status === 'error' || part.status === 'blocked') err++
      else if (part.status === 'start') running++
    }
  }
  return { ok, err, running, total: ok + err + running }
})

const agentsStatus = computed(() =>
  state.activeProjectPath ? '已加载（运行时合并）' : '全局指令',
)

const softStatus = computed(() =>
  state.activeProjectPath ? '就绪' : '全局空间',
)

async function copyPath() {
  const path = state.activeProjectPath
  if (!path) {
    message.info('当前为全局空间，无项目路径')
    return
  }
  try {
    await navigator.clipboard.writeText(path)
    message.success('已复制路径')
  } catch {
    message.error('复制失败')
  }
}

async function copyTrace() {
  const id = currentTraceId.value
  if (!id) {
    message.info('当前没有 trace id（最近一次请求尚未产生事件）')
    return
  }
  const ok = await copyText(id)
  if (ok) {
    message.success(`已复制 trace id: ${id}`)
  } else {
    message.error('复制失败，请手动选择')
  }
}

async function openExplorer() {
  if (!state.activeProjectPath) return
  try {
    await api.openExplorer(state.activeProjectPath)
  } catch {
    message.error('打开目录失败')
  }
}

async function openTerminal() {
  if (!state.activeProjectPath) return
  try {
    await api.openTerminal(state.activeProjectPath)
  } catch {
    message.error('打开终端失败')
  }
}
</script>

<template>
  <aside
    v-show="props.open"
    class="inspector"
    aria-label="检查器"
  >
    <div class="inspector-tabs">
      <div class="tab-list" role="tablist" aria-label="检查器视图">
        <button
          type="button"
          class="tab"
          :class="{ 'tab--active': tab === 'session' }"
          role="tab"
          :aria-selected="tab === 'session'"
          @click="tab = 'session'"
        >
          会话
        </button>
        <button
          type="button"
          class="tab"
          :class="{ 'tab--active': tab === 'project' }"
          role="tab"
          :aria-selected="tab === 'project'"
          @click="tab = 'project'"
        >
          项目
        </button>
      </div>
      <button
        type="button"
        class="inspector-close"
        title="收起检查器"
        aria-label="收起检查器"
        @click="emit('close')"
      >
        <X :size="14" />
      </button>
    </div>

    <div v-if="tab === 'session'" class="inspector-body" role="tabpanel">
      <section class="section">
        <div class="section-label">当前会话</div>
        <div class="section-title">{{ currentSession?.title || '未选择会话' }}</div>
        <div class="section-meta">ID {{ idSnippet }}</div>
      </section>
      <section class="section">
        <div class="section-label">会话操作</div>
        <div class="panel-action-grid">
          <button
            type="button"
            class="panel-action-btn"
            :disabled="!currentTraceId"
            title="复制当前请求 Trace"
            @click="copyTrace"
          >
            <Hash :size="14" />
            <span>复制 Trace</span>
          </button>
          <button
            type="button"
            class="panel-action-btn"
            title="查看工具列表"
            @click="showToolList = true"
          >
            <Wrench :size="14" />
            <span>工具列表</span>
          </button>
          <button
            type="button"
            class="panel-action-btn"
            :disabled="!state.currentID"
            title="从当前对话生成风格"
            @click="showStyleGen = true"
          >
            <Sparkles :size="14" />
            <span>生成风格</span>
          </button>
        </div>
      </section>
      <section class="section">
        <div class="section-label">项目信息</div>
        <div class="section-title-row">
          <Globe v-if="!state.activeProjectPath" :size="15" />
          <Folder v-else :size="15" />
          <span class="section-title">{{ projectName }}</span>
        </div>
        <div v-if="state.activeProjectPath" class="section-meta mono">
          <span class="path-text">{{ state.activeProjectPath }}</span>
          <button
            type="button"
            class="path-copy-btn"
            title="复制项目路径"
            aria-label="复制项目路径"
            @click="copyPath"
          >
            <Copy :size="13" />
          </button>
        </div>
        <div class="status-pill">{{ softStatus }}</div>
        <div v-if="state.activeProjectPath" class="project-action-row" aria-label="项目快捷操作">
          <button
            type="button"
            class="panel-action-btn"
            :disabled="!canOpenProject"
            title="打开项目目录"
            @click="openExplorer"
          >
            <FolderOpen :size="14" />
            <span>打开目录</span>
          </button>
          <button
            type="button"
            class="panel-action-btn"
            :disabled="!canOpenProject"
            title="在项目目录打开终端"
            @click="openTerminal"
          >
            <Terminal :size="14" />
            <span>打开终端</span>
          </button>
        </div>
      </section>
      <section class="section">
        <div class="section-label">AGENTS.md</div>
        <div class="section-text">{{ agentsStatus }}</div>
      </section>
      <section class="section">
        <div class="section-label">模型 / 上下文</div>
        <div class="section-text">{{ ctxSummary }}</div>
      </section>
      <section v-if="toolStats.total" class="section">
        <div class="section-label">工具调用</div>
        <div class="section-text">
          成功 {{ toolStats.ok }}
          <span class="dot">·</span>
          失败 {{ toolStats.err }}
          <span class="dot">·</span>
          进行中 {{ toolStats.running }}
        </div>
      </section>
    </div>

    <div v-else class="inspector-body" role="tabpanel">
      <section class="section">
        <div class="section-label">当前项目</div>
        <div class="section-title-row">
          <Globe v-if="!state.activeProjectPath" :size="15" />
          <Folder v-else :size="15" />
          <span class="section-title">{{ projectName }}</span>
        </div>
        <div v-if="state.activeProjectPath" class="section-meta mono">
          <span class="path-text">{{ state.activeProjectPath }}</span>
          <button
            type="button"
            class="path-copy-btn"
            title="复制项目路径"
            aria-label="复制项目路径"
            @click="copyPath"
          >
            <Copy :size="13" />
          </button>
        </div>
        <div class="status-pill">{{ softStatus }}</div>
        <div v-if="state.activeProjectPath" class="project-action-row" aria-label="项目快捷操作">
          <button
            type="button"
            class="panel-action-btn"
            :disabled="!canOpenProject"
            title="打开项目目录"
            @click="openExplorer"
          >
            <FolderOpen :size="14" />
            <span>打开目录</span>
          </button>
          <button
            type="button"
            class="panel-action-btn"
            :disabled="!canOpenProject"
            title="在项目目录打开终端"
            @click="openTerminal"
          >
            <Terminal :size="14" />
            <span>打开终端</span>
          </button>
        </div>
      </section>
      <section class="section">
        <div class="section-label">AGENTS.md</div>
        <div class="section-text">{{ agentsStatus }}</div>
      </section>
      <section v-if="toolStats.total" class="section">
        <div class="section-label">工具统计（当前会话）</div>
        <div class="section-text">
          成功 {{ toolStats.ok }}
          <span class="dot">·</span>
          失败 {{ toolStats.err }}
          <span class="dot">·</span>
          进行中 {{ toolStats.running }}
        </div>
      </section>
    </div>
  </aside>
  <ToolListDrawer v-model:show="showToolList" />
  <StyleGenModal v-model:show="showStyleGen" />
</template>

<style scoped>
.inspector {
  width: var(--inspector-width);
  flex: 0 0 var(--inspector-width);
  display: flex;
  flex-direction: column;
  min-height: 0;
  overflow: hidden;
  background: var(--surface-2);
  border-left: 1px solid var(--border-subtle);
}
.inspector-tabs {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-height: calc(var(--control-height) + var(--space-3));
  padding: 0 var(--space-3);
  border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0;
}
.tab-list {
  min-width: 0;
  flex: 1;
  display: flex;
  align-items: stretch;
  gap: var(--space-5);
}
.tab {
  position: relative;
  flex: 0 0 auto;
  min-width: calc(var(--space-8) + var(--space-3));
  height: calc(var(--control-height) + var(--space-3));
  border: none;
  border-radius: 0;
  background: transparent;
  color: var(--text-secondary);
  font-size: 12.5px;
  font-weight: 500;
  cursor: pointer;
  transition: var(--transition-colors);
}
.tab:hover {
  color: var(--text-primary);
}
.tab::after {
  content: '';
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  height: 2px;
  border-radius: var(--radius-pill);
  background: transparent;
  transition: var(--transition-colors);
}
.tab--active {
  color: var(--brand-600);
  font-weight: 700;
}
.tab--active::after {
  background: var(--brand-500);
}
.inspector-close {
  width: var(--control-height);
  height: var(--control-height);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 var(--control-height);
  border: 1px solid transparent;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;
  transition: var(--transition-colors);
}
.inspector-close:hover {
  background: var(--surface-2);
  color: var(--text-primary);
}
.inspector-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-3);
}
.section {
  padding: var(--space-3);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  background: color-mix(in srgb, var(--surface-1) 88%, transparent);
  box-shadow: var(--shadow-xs);
}
.section-label {
  margin-bottom: var(--space-2);
  color: var(--text-tertiary);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0;
  text-transform: none;
}
.section-title {
  color: var(--text-primary);
  font-size: 13.5px;
  font-weight: 600;
  line-height: 1.35;
  word-break: break-word;
}
.section-title-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  color: var(--text-primary);
}
.section-title-row .section-title {
  min-width: 0;
}
.section-meta {
  margin-top: var(--space-1);
  color: var(--text-tertiary);
  font-size: 11.5px;
  line-height: 1.4;
  word-break: break-all;
}
.section-meta.mono {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.path-text {
  min-width: 0;
  flex: 1;
}
.path-copy-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: var(--control-height);
  height: var(--control-height);
  flex: 0 0 var(--control-height);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-1);
  color: var(--text-secondary);
  cursor: pointer;
  transition: var(--transition-colors);
}
.path-copy-btn:hover {
  border-color: var(--border-default);
  background: var(--surface-3);
  color: var(--text-primary);
}
.section-meta.mono,
.mono {
  font-family: var(--font-mono);
}
.section-text {
  color: var(--text-secondary);
  font-size: 12.5px;
  line-height: 1.45;
}
.status-pill {
  display: inline-flex;
  margin-top: var(--space-2);
  padding: 2px 8px;
  border-radius: var(--radius-pill, 9999px);
  border: 1px solid var(--border-subtle);
  background: color-mix(in srgb, var(--brand-50) 62%, var(--surface-1));
  color: var(--text-secondary);
  font-size: 11px;
  font-weight: 500;
}
.panel-action-grid,
.project-action-row {
  display: grid;
  grid-template-columns: 1fr;
  gap: var(--space-2);
}
.project-action-row {
  margin-top: var(--space-3);
}
.panel-action-btn {
  min-width: 0;
  min-height: var(--control-height);
  display: inline-flex;
  align-items: center;
  justify-content: flex-start;
  gap: var(--space-2);
  padding: 0 var(--space-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-1);
  color: var(--text-secondary);
  font: inherit;
  font-size: 12px;
  line-height: 1;
  cursor: pointer;
  transition: var(--transition-colors);
}
.panel-action-btn:hover:not(:disabled) {
  border-color: var(--border-default);
  background: var(--surface-3);
  color: var(--text-primary);
}
.panel-action-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.panel-action-btn svg {
  flex: 0 0 auto;
}
.panel-action-btn span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dot {
  margin: 0 4px;
  color: var(--text-quaternary);
}
@media (max-width: 1040px) {
  .inspector {
    border-left-color: transparent;
  }
}
</style>
