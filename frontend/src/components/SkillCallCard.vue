<script setup lang="ts">
// Skill lifecycle card — same compact timeline family as tools.
// Always names the Skill being invoked and shows its status.
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

const shortError = computed(() => {
  const raw = (props.part.error || '').trim()
  if (!raw) return ''
  const first = raw.split(/\r?\n/)[0].trim()
  return first.length > 72 ? `${first.slice(0, 72)}…` : first
})
</script>

<template>
  <section
    class="skill-card"
    :class="`skill-${part.status}`"
  >
    <span class="skill-icon" aria-hidden="true">
      <Sparkles :size="12" />
    </span>
    <div class="skill-main">
      <div class="skill-row">
        <span class="skill-label">当前调用 Skill</span>
        <span class="skill-name">{{ part.name }}</span>
        <span class="skill-status">
          <component
            :is="statusIcon"
            :size="12"
            :class="{ spin: part.status === 'start' }"
            aria-hidden="true"
          />
          <span>{{ statusLabel }}</span>
        </span>
        <span v-if="scopeLabel" class="skill-scope">{{ scopeLabel }}</span>
      </div>
      <div v-if="part.dependencies?.length" class="skill-deps">
        依赖 {{ part.dependencies.join('、') }}
      </div>
      <div v-if="shortError" class="skill-err-text" :title="part.error">{{ shortError }}</div>
    </div>
  </section>
</template>

<style scoped>
/* Flat timeline row — same family as .tool-card inside
 * .event-timeline. No nested card chrome. */
.skill-card {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  margin: 0;
  padding: var(--space-1) var(--space-2);
  min-height: calc(var(--space-6) - var(--space-1));
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-secondary);
  font-size: 12px;
}
.skill-card.skill-error {
  border-left: 2px solid var(--error-500);
  padding-left: calc(var(--space-2) - 2px);
}

.skill-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 16px;
  width: 16px;
  height: 16px;
  margin-top: 1px;
  border-radius: 50%;
  background: var(--brand-50);
  color: var(--brand-500);
}
.skill-ready .skill-icon {
  background: var(--success-50);
  color: var(--success-500);
}
.skill-error .skill-icon {
  background: var(--error-50);
  color: var(--error-500);
}

.skill-main {
  min-width: 0;
  flex: 1;
}

.skill-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-1) var(--space-2);
  min-width: 0;
}

.skill-label {
  color: var(--text-tertiary);
  font-size: 11px;
  font-weight: 500;
  flex-shrink: 0;
}

.skill-name {
  font-family: var(--font-mono);
  font-size: 12px;
  font-weight: 600;
  color: var(--text-primary);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.skill-status {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  color: var(--text-tertiary);
  font-size: 11px;
  flex-shrink: 0;
}
.skill-ready .skill-status { color: var(--success-500); }
.skill-error .skill-status { color: var(--error-500); }
.skill-start .skill-status { color: var(--brand-500); }

.skill-scope {
  color: var(--text-quaternary);
  font-size: 11px;
  flex-shrink: 0;
}

.skill-deps {
  margin-top: var(--space-1);
  color: var(--text-quaternary);
  font-size: 11px;
  line-height: 1.35;
}

.skill-err-text {
  margin-top: var(--space-1);
  padding-left: var(--space-2);
  border-left: 2px solid var(--error-500);
  color: var(--error-500);
  font-size: 11px;
  line-height: 1.35;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.spin {
  animation: skill-spin 1.1s linear infinite;
}

@keyframes skill-spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}
</style>
