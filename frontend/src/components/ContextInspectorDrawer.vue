<script setup lang="ts">
// P2-3 context inspector drawer. Slides out from the
// right edge of the chat area and shows:
//   - Top: a progress bar for context utilisation,
//     colour-coded against the same thresholds
//     tryAutoCompact uses (> 60% yellow, > 80% red).
//   - Middle: a scrollable list of every message in
//     the conversation with role badge + token
//     estimate + preview. Tool rows get a distinct
//     background so the user can see which entries
//     are eating the context window.
//   - Bottom: the compressed summary (if any) in a
//     collapsible block — the user can see what
//     tryAutoCompact replaced the older history with.
//
// All numbers are LABELLED "估算" because the wire
// response uses an heuristic tokenizer, not the real
// LLM tokenizer (which we'd need a vendored BPE to
// replicate, and that adds 5+ MB for marginal UX
// gain). The display is consistent with what the
// agent uses to decide when to compact, so the bar
// lines up with the agent's "context_warn" events.
import { computed, watch } from 'vue'
import { NDrawer, NDrawerContent, NProgress, NTag, NSpin, NButton, NIcon, NCollapse, NCollapseItem, NEmpty } from 'naive-ui'
import { state, loadContextInspector, closeContextInspector } from '../stores/chat'

// `currentSessionId` is read directly from the chat
// store so the drawer stays bound to the active
// session even if the user switches. The store also
// gates close/open via the `open` flag — we only
// call the API when the drawer transitions from
// closed to open (and on manual "刷新" clicks).
const currentSessionId = computed(() => state.currentID)
const inspector = computed(() => state.contextInspector)

const visible = computed({
  get: () => !!inspector.value?.open,
  set: (v: boolean) => { if (!v) closeContextInspector() },
})

// Re-fetch when the user opens the drawer or clicks
// refresh. The store keeps a cached payload, so the
// re-fetch is the only "live" path.
function refresh() {
  if (currentSessionId.value) loadContextInspector(currentSessionId.value)
}

// Colour thresholds mirror tryAutoCompact in the
// agent: < 60% green (plenty of room), 60-80% yellow
// (warning), > 80% red (agent will start compressing).
const utilColor = computed(() => {
  const p = inspector.value?.data?.utilization_pct ?? 0
  if (p >= 80) return 'error'
  if (p >= 60) return 'warning'
  return 'success'
})

// Compact 4-digit formatter — 12,345 / 1,234,567
function fmt(n: number): string {
  return (n ?? 0).toLocaleString('en-US')
}

const roleLabel = (role: string): string => {
  switch (role) {
    case 'user': return '用户'
    case 'assistant': return '助手'
    case 'tool': return '工具'
    case 'system': return '系统'
    default: return role || '?'
  }
}

const roleType = (role: string): 'success' | 'info' | 'warning' | 'default' => {
  switch (role) {
    case 'user': return 'info'
    case 'assistant': return 'success'
    case 'tool': return 'warning'
    default: return 'default'
  }
}
</script>

<template>
  <NDrawer
    v-model:show="visible"
    :width="420"
    placement="right"
    :native-scrollbar="false"
  >
    <NDrawerContent
      title="上下文"
      :native-scrollbar="false"
      closable
    >
      <div v-if="!currentSessionId" class="ctx-empty">
        <NEmpty description="未选择会话" />
      </div>
      <div v-else-if="inspector?.loading && !inspector.data" class="ctx-loading">
        <NSpin size="medium" />
        <span class="ctx-loading-text">正在估算 token…</span>
      </div>
      <div v-else-if="inspector?.error" class="ctx-error">
        <NEmpty :description="inspector.error">
          <template #extra>
            <NButton size="small" @click="refresh">重试</NButton>
          </template>
        </NEmpty>
      </div>
      <div v-else-if="inspector?.data" class="ctx-body">
        <!-- Top: utilisation bar -->
        <div class="ctx-util">
          <div class="ctx-util-header">
            <span class="ctx-util-label">估算使用率</span>
            <span class="ctx-util-pct" :class="utilColor">
              {{ inspector.data.utilization_pct.toFixed(1) }}%
            </span>
          </div>
          <NProgress
            type="line"
            :percentage="Math.min(inspector.data.utilization_pct, 100)"
            :status="utilColor === 'error' ? 'error' : (utilColor === 'warning' ? 'warning' : 'success')"
            :show-indicator="false"
            :height="8"
            :border-radius="4"
          />
          <div class="ctx-util-stats">
            <span>{{ fmt(inspector.data.estimated_tokens) }} / {{ fmt(inspector.data.usable_tokens) }} tokens</span>
            <span class="ctx-util-model">{{ inspector.data.model }}</span>
          </div>
        </div>

        <!-- Middle: per-message breakdown -->
        <div class="ctx-messages-header">
          <span>消息明细</span>
          <NButton text size="tiny" @click="refresh">刷新</NButton>
        </div>
        <div class="ctx-messages">
          <div
            v-for="(m, i) in inspector.data.messages"
            :key="i"
            class="ctx-msg"
            :class="{ 'is-tool': m.is_tool_result }"
          >
            <div class="ctx-msg-head">
              <NTag :type="roleType(m.role)" size="small" round :bordered="false">
                {{ roleLabel(m.role) }}
              </NTag>
              <span class="ctx-msg-tokens">{{ fmt(m.tokens) }}</span>
            </div>
            <div class="ctx-msg-preview">{{ m.preview || '(空)' }}</div>
          </div>
          <NEmpty v-if="!inspector.data.messages.length" description="还没有消息" />
        </div>

        <!-- Bottom: compressed summary (if any) -->
        <NCollapse v-if="inspector.data.compressed_summary" class="ctx-summary">
          <NCollapseItem :title="`已压缩的历史摘要`" name="summary">
            <pre class="ctx-summary-pre">{{ inspector.data.compressed_summary }}</pre>
          </NCollapseItem>
        </NCollapse>
      </div>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.ctx-empty,
.ctx-loading,
.ctx-error {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 200px;
  flex-direction: column;
  gap: 8px;
}
.ctx-loading-text { color: var(--text-tertiary); font-size: 12px; }

.ctx-body { display: flex; flex-direction: column; gap: 16px; }

.ctx-util { display: flex; flex-direction: column; gap: 6px; }
.ctx-util-header { display: flex; align-items: baseline; justify-content: space-between; }
.ctx-util-label { color: var(--text-secondary); font-size: 12px; }
.ctx-util-pct { font-size: 18px; font-weight: 600; font-variant-numeric: tabular-nums; }
.ctx-util-pct.success { color: var(--success-500, #10b981); }
.ctx-util-pct.warning { color: var(--warn-500, #f59e0b); }
.ctx-util-pct.error { color: var(--error-500, #ef4444); }
.ctx-util-stats {
  display: flex;
  justify-content: space-between;
  font-size: 11px;
  color: var(--text-tertiary);
  font-variant-numeric: tabular-nums;
}
.ctx-util-model { font-family: var(--font-mono); }

.ctx-messages-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.ctx-messages { display: flex; flex-direction: column; gap: 4px; }

.ctx-msg {
  background: var(--surface-1);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm, 4px);
  padding: 6px 8px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.ctx-msg.is-tool {
  background: color-mix(in srgb, var(--warn-500, #f59e0b) 6%, var(--surface-1));
  border-color: color-mix(in srgb, var(--warn-500, #f59e0b) 24%, var(--border-subtle));
}
.ctx-msg-head { display: flex; justify-content: space-between; align-items: center; }
.ctx-msg-tokens { font-size: 11px; color: var(--text-tertiary); font-variant-numeric: tabular-nums; }
.ctx-msg-preview {
  font-size: 12px;
  color: var(--text-secondary);
  line-height: 1.4;
  font-family: var(--font-mono);
  word-break: break-all;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.ctx-summary { margin-top: 4px; }
.ctx-summary-pre {
  font-size: 11.5px;
  line-height: 1.45;
  font-family: var(--font-mono);
  color: var(--text-secondary);
  white-space: pre-wrap;
  word-break: break-word;
  margin: 0;
}
</style>
