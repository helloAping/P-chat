<script setup lang="ts">
/**
 * InspectorPanel — lightweight right-hand context panel.
 *
 * Tabs: 会话 | 项目. Sections use light cards so context is scannable
 * without turning the panel into a heavy dashboard.
 * Collapse is owned by the parent (v-if / width 0).
 */
import { computed, ref, watch } from 'vue'
import { useMessage } from 'naive-ui'
import { state, currentMessages, refreshContextUsage } from '../stores/chat'
import * as api from '../api/client'
import { formatCompactTokens } from '../utils/format'
import { copyText, downloadBlob, downloadFromUrl } from '../utils/clipboard'
import {
  attachmentArtifactFileName,
  attachmentArtifactSourceLabel,
  attachmentArtifactTypeLabel,
  collectConversationAttachments,
  type AttachmentArtifact,
} from '../utils/attachmentArtifacts'
import ToolListDrawer from './ToolListDrawer.vue'
import StyleGenModal from './StyleGenModal.vue'
import {
  Copy, Globe, Folder, FolderOpen, Terminal, Wrench, Sparkles, Hash, X,
  GitBranch, CheckCircle2, AlertCircle, BarChart3, ImageIcon, Film, Volume2,
  FileText, File, ChevronDown, ChevronRight, Download, Clipboard,
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
const sessionAttachmentsExpanded = ref(false)
const SESSION_ATTACHMENT_LIMIT = 5

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

const ctx = computed(() => {
  const d = state.contextInspector?.data
  if (!state.currentID || !d || d.session_id !== state.currentID) return null
  return d
})
const currentTraceId = computed(() => state.currentTraceId || '')
const canOpenProject = computed(() => !!state.activeProjectPath)

const hasContextUsage = computed(() => {
  const d = ctx.value
  return !!d && d.context_window > 0
})

const ctxModel = computed(() => ctx.value?.model || '未加载上下文')
const ctxPct = computed(() => {
  const d = ctx.value
  if (!d || d.context_window <= 0) return 0
  const p = d.context_window_pct
  if (p != null && Number.isFinite(p)) return Math.min(p, 999.9)
  return (d.estimated_tokens / d.context_window) * 100
})
const ctxBarPct = computed(() => Math.min(Math.max(ctxPct.value, 0), 100))
const ctxPctText = computed(() => `${ctxPct.value.toFixed(1)}%`)
const ctxTokensText = computed(() => {
  const d = ctx.value
  if (!d || d.context_window <= 0) return '暂无上下文数据'
  return `${formatCompactTokens(d.estimated_tokens)} / ${formatCompactTokens(d.context_window)}`
})
const ctxLoading = computed(() => !!state.contextInspector?.loading)
const ctxColor = computed(() => {
  const p = ctxPct.value
  if (p >= 80) return 'error'
  if (p >= 60) return 'warning'
  return 'success'
})
const ctxUsageTitle = computed(() => {
  if (!hasContextUsage.value) return '暂无上下文数据，点击刷新'
  return `上下文占用 · ${ctxModel.value} · ${ctxTokensText.value}（${ctxPctText.value}）`
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

const sessionAttachments = computed(() => collectConversationAttachments(currentMessages.value))
const visibleSessionAttachments = computed(() =>
  sessionAttachmentsExpanded.value
    ? sessionAttachments.value
    : sessionAttachments.value.slice(0, SESSION_ATTACHMENT_LIMIT),
)
const hiddenSessionAttachmentCount = computed(() =>
  Math.max(0, sessionAttachments.value.length - SESSION_ATTACHMENT_LIMIT),
)

watch(() => state.currentID, () => {
  sessionAttachmentsExpanded.value = false
})

const agentsStatus = computed(() =>
  state.activeProjectPath ? '已加载（运行时合并）' : '全局指令',
)

const softStatus = computed(() =>
  state.activeProjectPath ? '就绪' : '全局空间',
)

const projectBranch = computed(() => project.value?.branch || '')
const hasProjectWorktreeState = computed(() => typeof project.value?.dirty === 'boolean')
const projectWorktreeLabel = computed(() => {
  if (!hasProjectWorktreeState.value) return ''
  return project.value?.dirty ? '工作区有改动' : '工作区干净'
})
const projectWorktreeKind = computed(() =>
  project.value?.dirty ? 'dirty' : 'clean',
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

function refreshContext() {
  if (!state.currentID) return
  refreshContextUsage(state.currentID)
}

function sessionAttachmentIcon(item: AttachmentArtifact) {
  if (item.kind === 'image') return ImageIcon
  if (item.kind === 'video') return Film
  if (item.kind === 'audio') return Volume2
  if (item.kind === 'text') return FileText
  return File
}

function canPreviewSessionAttachment(item: AttachmentArtifact): boolean {
  return (item.kind === 'image' || item.kind === 'video') && !!item.url
}

function openSessionAttachment(item: AttachmentArtifact) {
  if (!canPreviewSessionAttachment(item) || !item.url) return
  state.lightbox = {
    show: true,
    src: item.url,
    alt: attachmentArtifactFileName(item),
    kind: item.kind === 'video' ? 'video' : 'image',
  }
}

async function copySessionAttachment(item: AttachmentArtifact) {
  const text = item.text || item.url || ''
  if (!text) {
    message.info('没有可复制的内容')
    return
  }
  const ok = await copyText(text)
  message[ok ? 'success' : 'error'](ok ? '已复制' : '复制失败')
}

function downloadSessionAttachment(item: AttachmentArtifact) {
  const name = attachmentArtifactFileName(item)
  if (item.url) {
    downloadFromUrl(item.url, name)
    message.success('已开始下载')
    return
  }
  if (item.text) {
    downloadBlob(new Blob([item.text], { type: item.mime || 'text/plain' }), name)
    message.success('已下载')
    return
  }
  message.info('没有可下载的内容')
}
</script>

<template>
  <aside
    class="inspector"
    :class="{ 'inspector--closed': !props.open }"
    aria-label="检查器"
    :aria-hidden="!props.open"
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

    <Transition name="inspector-tab" mode="out-in">
      <div v-if="tab === 'session'" key="session" class="inspector-body" role="tabpanel">
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
          <div class="section-label">AGENTS.md</div>
          <div class="section-text">{{ agentsStatus }}</div>
        </section>
        <section class="section">
          <div class="section-label">模型 / 上下文</div>
          <button
            type="button"
            class="context-usage-card"
            :class="[`context-usage-card--${ctxColor}`, { 'context-usage-card--loading': ctxLoading }]"
            :disabled="!state.currentID"
            :title="ctxUsageTitle"
            @click="refreshContext"
          >
            <span class="context-usage-head">
              <span class="context-usage-model">
                <BarChart3 :size="14" />
                <span>{{ ctxModel }}</span>
              </span>
              <span v-if="hasContextUsage" class="context-usage-pct">{{ ctxPctText }}</span>
            </span>
            <span class="context-usage-bar" aria-hidden="true">
              <span class="context-usage-fill" :style="{ width: ctxBarPct + '%' }" />
            </span>
            <span class="context-usage-foot">
              <span>{{ ctxTokensText }}</span>
              <span>{{ ctxLoading ? '刷新中' : '点击刷新' }}</span>
            </span>
          </button>
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
        <section class="section section--attachments">
          <div class="section-title-row section-title-row--spaced">
            <div class="section-label">附件列表</div>
            <span v-if="sessionAttachments.length" class="attachment-count">{{ sessionAttachments.length }}</span>
          </div>
          <div v-if="!sessionAttachments.length" class="attachment-empty">当前会话暂无附件</div>
          <div v-else class="session-attachment-list">
            <div
              v-for="item in visibleSessionAttachments"
              :key="item.key"
              class="session-attachment-row"
            >
              <button
                type="button"
                class="session-attachment-main"
                :disabled="!canPreviewSessionAttachment(item)"
                :title="canPreviewSessionAttachment(item) ? '预览附件' : attachmentArtifactFileName(item)"
                @click="openSessionAttachment(item)"
              >
                <span class="session-attachment-icon" :class="`session-attachment-icon--${item.kind}`" aria-hidden="true">
                  <component :is="sessionAttachmentIcon(item)" :size="14" />
                </span>
                <span class="session-attachment-copy">
                  <span class="session-attachment-name">{{ attachmentArtifactFileName(item) }}</span>
                  <span class="session-attachment-meta">
                    {{ attachmentArtifactSourceLabel(item.source) }}
                    <span class="dot">·</span>
                    {{ attachmentArtifactTypeLabel(item) }}
                  </span>
                </span>
              </button>
              <span class="session-attachment-actions">
                <button
                  type="button"
                  class="session-attachment-action"
                  title="复制引用"
                  aria-label="复制引用"
                  @click="copySessionAttachment(item)"
                >
                  <Clipboard :size="12" />
                </button>
                <button
                  v-if="item.url || item.text"
                  type="button"
                  class="session-attachment-action"
                  title="下载附件"
                  aria-label="下载附件"
                  @click="downloadSessionAttachment(item)"
                >
                  <Download :size="12" />
                </button>
              </span>
            </div>
          </div>
          <button
            v-if="hiddenSessionAttachmentCount"
            type="button"
            class="attachment-expand-btn"
            @click="sessionAttachmentsExpanded = !sessionAttachmentsExpanded"
          >
            <component :is="sessionAttachmentsExpanded ? ChevronDown : ChevronRight" :size="12" />
            <span>{{ sessionAttachmentsExpanded ? '收起附件' : `展开其余 ${hiddenSessionAttachmentCount} 个附件` }}</span>
          </button>
        </section>
      </div>

      <div v-else key="project" class="inspector-body" role="tabpanel">
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
          <div v-if="projectBranch || hasProjectWorktreeState" class="project-meta-chips" aria-label="Git 状态">
            <span v-if="projectBranch" class="project-meta-chip project-meta-chip--branch">
              <GitBranch :size="12" />
              <span>{{ projectBranch }}</span>
            </span>
            <span
              v-if="hasProjectWorktreeState"
              class="project-meta-chip"
              :class="`project-meta-chip--${projectWorktreeKind}`"
            >
              <CheckCircle2 v-if="projectWorktreeKind === 'clean'" :size="12" />
              <AlertCircle v-else :size="12" />
              <span>{{ projectWorktreeLabel }}</span>
            </span>
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
    </Transition>
  </aside>
  <ToolListDrawer v-model:show="showToolList" />
  <StyleGenModal v-model:show="showStyleGen" />
</template>

<style scoped>
.inspector {
  width: var(--inspector-width);
  flex: 0 0 auto;
  flex-basis: var(--inspector-width);
  display: flex;
  flex-direction: column;
  min-height: 0;
  overflow: hidden;
  background: var(--surface-2);
  border-left: 1px solid var(--border-subtle);
  opacity: 1;
  transition:
    width var(--dur-slow) var(--ease-in-out),
    flex-basis var(--dur-slow) var(--ease-in-out),
    opacity var(--dur-base) var(--ease-out),
    border-color var(--dur-base) var(--ease-out);
}
.inspector--closed {
  width: 0;
  flex-basis: 0;
  opacity: 0;
  pointer-events: none;
  border-left-color: transparent;
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
.inspector-tab-enter-active,
.inspector-tab-leave-active {
  transition:
    opacity var(--dur-base) var(--ease-out),
    transform var(--dur-base) var(--ease-out);
}
.inspector-tab-enter-from {
  opacity: 0;
  transform: translateY(var(--space-1));
}
.inspector-tab-leave-to {
  opacity: 0;
  transform: translateY(calc(-1 * var(--space-1)));
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
.section-title-row--spaced {
  justify-content: space-between;
  align-items: flex-start;
}
.section-title-row--spaced .section-label {
  margin-bottom: 0;
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
.project-meta-chips {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-1);
  margin-top: var(--space-2);
}
.project-meta-chip {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  min-height: calc(var(--control-height) - var(--space-2));
  padding: 0 var(--space-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-pill);
  background: var(--surface-1);
  color: var(--text-tertiary);
  font-size: 11px;
  line-height: 1;
  white-space: nowrap;
}
.project-meta-chip--branch {
  color: var(--text-secondary);
}
.project-meta-chip--clean {
  color: var(--success-500);
}
.project-meta-chip--dirty {
  color: var(--warn-500);
}
.section-text {
  color: var(--text-secondary);
  font-size: 12.5px;
  line-height: 1.45;
}
.attachment-count {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: calc(var(--space-6) - var(--space-1));
  height: calc(var(--space-6) - var(--space-1));
  padding: 0 var(--space-1);
  border-radius: var(--radius-pill);
  background: var(--surface-3);
  color: var(--text-tertiary);
  font-size: 10.5px;
  font-weight: 600;
  line-height: 1;
  font-variant-numeric: tabular-nums;
}
.attachment-empty {
  margin-top: var(--space-2);
  color: var(--text-tertiary);
  font-size: 12px;
  line-height: 1.45;
}
.session-attachment-list {
  display: grid;
  gap: var(--space-1);
  margin-top: var(--space-2);
}
.session-attachment-row {
  position: relative;
  display: block;
  min-width: 0;
  min-height: calc(var(--control-height) + var(--space-3));
  padding: var(--space-1);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-1);
  transition:
    background var(--dur-fast) var(--ease-out),
    border-color var(--dur-fast) var(--ease-out),
    box-shadow var(--dur-fast) var(--ease-out);
}
.session-attachment-row:hover,
.session-attachment-row:focus-within {
  border-color: var(--border-default);
  background: color-mix(in srgb, var(--surface-1) 82%, var(--surface-3));
  box-shadow: var(--shadow-xs);
}
.session-attachment-main {
  width: 100%;
  min-width: 0;
  min-height: calc(var(--control-height) + var(--space-1));
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
}
.session-attachment-main:not(:disabled) {
  cursor: pointer;
}
.session-attachment-main:disabled {
  cursor: default;
}
.session-attachment-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: var(--space-7);
  height: var(--space-7);
  flex: 0 0 var(--space-7);
  border-radius: var(--radius-sm);
  border: 1px solid var(--border-subtle);
  background: var(--surface-2);
  color: var(--text-secondary);
}
.session-attachment-icon--image,
.session-attachment-icon--video {
  color: var(--brand-600);
}
.session-attachment-icon--audio {
  color: var(--success-500);
}
.session-attachment-copy {
  display: grid;
  min-width: 0;
  gap: calc(var(--space-1) / 2);
}
.session-attachment-name,
.session-attachment-meta {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.session-attachment-name {
  color: var(--text-primary);
  font-size: 12px;
  font-weight: 600;
  line-height: 1.25;
}
.session-attachment-meta {
  color: var(--text-tertiary);
  font-size: 10.5px;
  line-height: 1.25;
}
.session-attachment-actions {
  position: absolute;
  right: var(--space-1);
  top: 50%;
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  padding: var(--space-1);
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, var(--surface-1) 78%, transparent);
  box-shadow: var(--shadow-sm);
  opacity: 0;
  pointer-events: none;
  transform: translateY(-50%) translateX(var(--space-1));
  transition:
    opacity var(--dur-fast) var(--ease-out),
    transform var(--dur-fast) var(--ease-out);
}
.session-attachment-row:hover .session-attachment-actions,
.session-attachment-row:focus-within .session-attachment-actions {
  opacity: 1;
  pointer-events: auto;
  transform: translateY(-50%) translateX(0);
}
.session-attachment-action {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: calc(var(--space-6) + var(--space-1));
  height: calc(var(--space-6) + var(--space-1));
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-overlay);
  color: var(--text-secondary);
  cursor: pointer;
  transition: var(--transition-colors);
}
.session-attachment-action:hover {
  border-color: var(--border-default);
  background: var(--surface-3);
  color: var(--text-primary);
}
@media (hover: none) {
  .session-attachment-actions {
    opacity: 1;
    pointer-events: auto;
    transform: translateY(-50%) translateX(0);
  }
}
.attachment-expand-btn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  margin-top: var(--space-2);
  padding: calc(var(--space-1) / 2) var(--space-1);
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-tertiary);
  font: inherit;
  font-size: 11.5px;
  cursor: pointer;
  transition: var(--transition-colors);
}
.attachment-expand-btn:hover {
  background: var(--surface-3);
  color: var(--text-primary);
}
.context-usage-card {
  width: 100%;
  min-width: 0;
  display: grid;
  gap: var(--space-2);
  padding: var(--space-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-1);
  color: var(--text-secondary);
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition:
    background var(--dur-fast) var(--ease-out),
    border-color var(--dur-fast) var(--ease-out),
    color var(--dur-fast) var(--ease-out);
}
.context-usage-card:hover:not(:disabled) {
  border-color: var(--border-default);
  background: var(--surface-3);
  color: var(--text-primary);
}
.context-usage-card:disabled {
  cursor: not-allowed;
  opacity: 0.58;
}
.context-usage-head,
.context-usage-foot {
  min-width: 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
}
.context-usage-model {
  min-width: 0;
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  color: var(--text-primary);
  font-size: 12.5px;
  font-weight: 600;
}
.context-usage-model span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.context-usage-pct {
  flex: 0 0 auto;
  color: currentColor;
  font-size: 11.5px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}
.context-usage-bar {
  position: relative;
  display: block;
  height: 5px;
  overflow: hidden;
  border-radius: var(--radius-pill);
  background: var(--surface-3);
}
.context-usage-fill {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: currentColor;
  transition: width var(--dur-base) var(--ease-out);
}
.context-usage-foot {
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
}
.context-usage-foot span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.context-usage-card--success { color: var(--success-500); }
.context-usage-card--warning { color: var(--warn-500); }
.context-usage-card--error { color: var(--error-500); }
.context-usage-card--loading .context-usage-fill {
  opacity: 0.72;
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
