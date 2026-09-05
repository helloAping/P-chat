<script setup lang="ts">
// Skill 生命周期卡片始终明确展示实际调用名称。
// The Skill lifecycle card always names the Skill being invoked.
import { computed } from 'vue'
import type { SkillPart } from '../api/client'
import { Check, Loader2, Sparkles, XCircle } from './icons'

const props = defineProps<{ part: SkillPart }>()

const statusLabel = computed(() => {
  if (props.part.status === 'ready') return '已就绪'
  if (props.part.status === 'error') return '加载失败'
  return '加载中…'
})

const statusIcon = computed(() => {
  if (props.part.status === 'ready') return Check
  if (props.part.status === 'error') return XCircle
  return Loader2
})

const scopeLabel = computed(() => {
  const labels: Record<string, string> = {
    project_managed: '项目托管',
    project_standard: '项目标准目录',
    global_managed: 'P-Chat 全局',
    user_standard: '用户标准目录',
  }
  return props.part.scope ? labels[props.part.scope] || props.part.scope : ''
})
</script>

<template>
  <section class="skill-card" :class="`skill-${part.status}`">
    <div class="skill-icon" aria-hidden="true">
      <Sparkles :size="14" />
    </div>
    <div class="skill-content">
      <div class="skill-title">当前调用 Skill：<span>{{ part.name }}</span></div>
      <div class="skill-meta">
        <component
          :is="statusIcon"
          :size="14"
          :class="{ spin: part.status === 'start' }"
          aria-hidden="true"
        />
        <span>{{ statusLabel }}</span>
        <span v-if="scopeLabel" class="skill-separator">·</span>
        <span v-if="scopeLabel">{{ scopeLabel }}</span>
        <span v-if="part.dependencies?.length" class="skill-separator">·</span>
        <span v-if="part.dependencies?.length">依赖 {{ part.dependencies.join('、') }}</span>
      </div>
      <div v-if="part.error" class="skill-error">{{ part.error }}</div>
    </div>
  </section>
</template>

<style scoped>
.skill-card {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  margin: var(--space-2) 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  background: var(--surface-1);
  color: var(--text-secondary);
}

.skill-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 var(--space-7);
  width: var(--space-7);
  height: var(--space-7);
  border-radius: var(--radius-sm);
  background: var(--brand-50);
  color: var(--brand-500);
}

.skill-content {
  min-width: 0;
  padding-top: var(--space-1);
}

.skill-title {
  color: var(--text-primary);
  font-size: 12px;
  font-weight: 600;
  line-height: 1.4;
}

.skill-title span {
  font-family: var(--font-mono);
  color: var(--brand-500);
}

.skill-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-1);
  margin-top: var(--space-1);
  color: var(--text-tertiary);
  font-size: 11px;
  line-height: 1.4;
}

.skill-ready .skill-meta { color: var(--success-500); }
.skill-error .skill-meta,
.skill-error { color: var(--error-500); }
.skill-separator { color: var(--text-quaternary); }

.spin {
  animation: skill-spin var(--dur-slow) linear infinite;
}

@keyframes skill-spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}
</style>
