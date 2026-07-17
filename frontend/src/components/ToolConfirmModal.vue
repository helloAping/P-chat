<script setup lang="ts">
// ToolConfirmModal — sandbox authorisation prompt.
//
// Replaces the inline NModal that used to live in App.vue:172.
// Now the modal is a first-class component (alongside
// QuestionModal and PlanReviewModal) and renders the new
// 2026-07 ConfirmRequest fields: path class, resolved path,
// risk level.
//
// Phase 1: 2 buttons (reject / allow once).
// Phase 2: extends to 4 buttons (allow once / allow path /
// allow session / reject) per the 2026-07 spec.

import { computed } from 'vue'
import { NModal, NButton, NSpace, NTag } from 'naive-ui'
import {
  ShieldAlert, ShieldCheck, FolderOpen, Folder, Globe, Lock,
} from './icons'
import { currentPendingConfirm, submitToolConfirm } from '../stores/chat'

interface Props {}
defineProps<Props>()

// Path-class label and colour mapping. The 2026-07 spec
// separates `external` (no project pinned) from `global`
// (project set, path outside) — the user sees different
// labels even though both end up as "Confirm".
const pathClassMeta = computed(() => {
  const cls = currentPendingConfirm.value?.pathClass
  switch (cls) {
    case 'project':
      return { label: '项目内', type: 'success' as const, Icon: Folder }
    case 'allowed':
      return { label: '白名单', type: 'info' as const, Icon: ShieldCheck }
    case 'global':
      return { label: '项目外', type: 'warning' as const, Icon: Globe }
    case 'external':
      return { label: '全局模式', type: 'warning' as const, Icon: FolderOpen }
    case 'protected':
      return { label: '受保护', type: 'error' as const, Icon: Lock }
    default:
      return null
  }
})

// Risk-level colour drives the modal's title icon and the
// primary button tone. `high` = destructive write / exec /
// dangerous pattern → red. `low` = read → blue.
const riskMeta = computed(() => {
  const r = currentPendingConfirm.value?.riskLevel
  switch (r) {
    case 'high':
      return { label: '高风险', type: 'error' as const, Icon: ShieldAlert }
    case 'medium':
      return { label: '中风险', type: 'warning' as const, Icon: ShieldAlert }
    case 'low':
    default:
      return { label: '低风险', type: 'info' as const, Icon: ShieldCheck }
  }
})

// Title: tool name + short risk label, in the modal header.
const titleText = computed(() => {
  const tool = currentPendingConfirm.value?.toolName
  const r = riskMeta.value
  return tool ? `沙箱请求：${tool}（${r.label}）` : '沙箱请求'
})

// Shorten the args blob for the modal body. Long arg JSON
// (e.g. write_file content) gets a "查看完整" toggle so the
// user can verify the LLM isn't sneaking in something
// unexpected.
import { ref as vueRef } from 'vue'
const argsExpanded = vueRef(false)
const argsPreview = computed(() => {
  const args = currentPendingConfirm.value?.args || ''
  if (argsExpanded.value || args.length <= 240) return args
  return args.slice(0, 240) + `\n... [共 ${args.length} 字符，点击下方按钮展开]`
})
</script>

<template>
  <NModal
    :show="!!currentPendingConfirm"
    preset="card"
    :title="titleText"
    style="width: 520px; max-width: calc(100vw - 32px)"
    :closable="false"
    :mask-closable="false"
  >
    <div class="tcm-body">
      <!-- Top row: tool + risk + path class chips -->
      <div class="tcm-chips">
        <NTag :type="riskMeta.type" size="small" round>
          <template #icon><component :is="riskMeta.Icon" :size="12" /></template>
          {{ riskMeta.label }}
        </NTag>
        <NTag
          v-if="pathClassMeta"
          :type="pathClassMeta.type"
          size="small"
          round
        >
          <template #icon><component :is="pathClassMeta.Icon" :size="12" /></template>
          {{ pathClassMeta.label }}
        </NTag>
      </div>

      <!-- Resolved path (the path the LLM will actually touch,
           after resolveToProjectRoot + filepath.Clean) -->
      <div v-if="currentPendingConfirm?.resolvedPath" class="tcm-path">
        <span class="tcm-label">目标路径</span>
        <code class="tcm-path-value">{{ currentPendingConfirm.resolvedPath }}</code>
      </div>

      <!-- Args blob -->
      <div class="tcm-args">
        <span class="tcm-label">参数</span>
        <pre class="tcm-pre">{{ argsPreview }}</pre>
        <button
          v-if="(currentPendingConfirm?.args?.length || 0) > 240"
          type="button"
          class="tcm-expand"
          @click="argsExpanded = !argsExpanded"
        >
          {{ argsExpanded ? '收起' : '查看完整' }}
        </button>
      </div>

      <!-- Reason (matched dangerous pattern / work_dir escape / etc.) -->
      <div v-if="currentPendingConfirm?.reason" class="tcm-reason">
        <span class="tcm-label">原因</span>
        <span class="tcm-reason-text">{{ currentPendingConfirm.reason }}</span>
      </div>
    </div>

    <template #footer>
      <NSpace justify="end">
        <NButton @click="submitToolConfirm(false)">拒绝</NButton>
        <!-- P2-4 dry-run UX note: we deliberately do
             NOT add a "先干跑" button here. The confirm
             modal is a yes/no gate for an EXECUTION
             that the agent already committed to; adding
             a third path would require either a new
             server endpoint (re-running the tool out
             of band) or a complex state machine to
             re-submit the original LLM turn with
             dry_run=true. Instead the user invokes
             dry-run through a regular prompt: "干跑
             shell_command X" — the LLM calls the
             tool with dry_run=true in args, and the
             ToolCallCard surfaces a "dry-run" chip
             so the user can tell at a glance the
             tool was NOT executed. The path from
             "this looks dangerous" → "let me see
             what it would do" is one prompt away
             and doesn't require new server surface. -->
        <NButton type="primary" @click="submitToolConfirm(true)">允许一次</NButton>
      </NSpace>
    </template>
  </NModal>
</template>

<style scoped>
.tcm-body {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.tcm-chips {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.tcm-label {
  display: block;
  font-size: 11px;
  font-weight: 600;
  color: var(--text-tertiary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  margin-bottom: 4px;
}
.tcm-path {
  display: flex;
  flex-direction: column;
}
.tcm-path-value {
  display: block;
  padding: 8px 10px;
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: 6px;
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--text-primary);
  word-break: break-all;
  user-select: text;
}
.tcm-args {
  display: flex;
  flex-direction: column;
}
.tcm-pre {
  margin: 0;
  padding: 8px 10px;
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: 6px;
  font-family: var(--font-mono);
  font-size: 11.5px;
  color: var(--text-secondary);
  max-height: 160px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-all;
}
.tcm-expand {
  margin-top: 4px;
  align-self: flex-end;
  background: none;
  border: none;
  padding: 2px 4px;
  font-size: 11.5px;
  color: var(--brand-500);
  cursor: pointer;
}
.tcm-expand:hover {
  text-decoration: underline;
}
.tcm-reason {
  display: flex;
  flex-direction: column;
  padding: 8px 10px;
  background: var(--warn-50, rgba(234, 170, 85, 0.12));
  border: 1px dashed var(--warn-500, #EAAA55);
  border-radius: 6px;
}
.tcm-reason-text {
  font-size: 12px;
  color: var(--text-primary);
  font-family: var(--font-mono);
  word-break: break-all;
}
</style>
