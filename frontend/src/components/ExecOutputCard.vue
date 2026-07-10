<script setup lang="ts">
import { ref, computed } from 'vue'
import { Clipboard, ChevronRight, ChevronDown } from './icons'

const props = defineProps<{ content: string; command?: string; elapsed?: string }>()

const collapsed = ref(false)
const searchText = ref('')

const lines = computed(() => props.content.split('\n'))

const filtered = computed(() => {
  if (!searchText.value) return lines.value
  const q = searchText.value.toLowerCase()
  return lines.value.filter(l => l.toLowerCase().includes(q))
})

function copyAll() {
  navigator.clipboard.writeText(props.content)
}
</script>

<template>
  <div class="exec-card">
    <div class="exec-header" @click="collapsed = !collapsed">
      <component :is="collapsed ? ChevronRight : ChevronDown" :size="12" class="exec-arrow" />
      <span class="exec-title">{{ command || 'exec_command' }}</span>
      <span v-if="elapsed" class="exec-elapsed">{{ elapsed }}</span>
      <span class="exec-actions">
        <button class="exec-btn" title="复制全部输出" aria-label="复制全部输出" @click.stop="copyAll">
          <Clipboard :size="12" />
        </button>
      </span>
    </div>
    <div v-show="!collapsed" class="exec-body">
      <div class="exec-search" v-if="lines.length > 20">
        <input v-model="searchText" placeholder="搜索输出..." class="exec-search-input" />
      </div>
      <pre class="exec-output"><code v-for="(line, i) in filtered" :key="i">{{ line }}<br /></code></pre>
    </div>
  </div>
</template>

<style scoped>
/* Terminal-style card. Unlike the chat's other cards
 * (ToolCall, SubAgent, Question) the body is intentionally
 * darker than its surroundings — a terminal panel.
 *
 * We use the same design tokens (--surface-*, --border-*)
 * as the rest of the chat but pick a darker shade so the
 * contrast reads as "this is a code panel". The light
 * theme variant stays light to match its surroundings;
 * users on light themes get a light panel, dark themes a
 * dark panel. */
.exec-card {
  margin: 6px 0;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  overflow: hidden;
  background: var(--surface-0);
  font-family: var(--font-mono);
}
.exec-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  cursor: pointer;
  background: var(--surface-1);
  border-bottom: 1px solid var(--border-subtle);
  user-select: none;
  color: var(--text-secondary);
}
.exec-arrow { color: var(--text-tertiary); display: inline-flex; }
.exec-title { font-size: 12px; font-weight: 600; color: var(--text-primary); font-family: var(--font-mono); }
.exec-elapsed { font-size: 11px; color: var(--text-tertiary); font-variant-numeric: tabular-nums; }
.exec-actions { margin-left: auto; display: flex; gap: 4px; }
.exec-btn {
  background: none;
  border: none;
  cursor: pointer;
  padding: 2px 4px;
  color: var(--text-tertiary);
  display: inline-flex;
  align-items: center;
  border-radius: 3px;
  transition: color var(--dur-fast) var(--ease-out), background var(--dur-fast) var(--ease-out);
}
.exec-btn:hover {
  color: var(--text-primary);
  background: var(--surface-3);
}
.exec-body { padding: 0; }
.exec-search {
  padding: 6px 12px;
  background: var(--surface-1);
  border-bottom: 1px solid var(--border-subtle);
}
.exec-search-input {
  width: 100%;
  padding: 4px 8px;
  font-size: 12px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-input);
  color: var(--text-primary);
  outline: none;
  font-family: var(--font-mono);
  transition: border-color var(--dur-fast) var(--ease-out);
}
.exec-search-input:focus { border-color: var(--brand-500); }
.exec-output {
  margin: 0;
  padding: 8px 12px;
  font-size: 12px;
  line-height: 1.5;
  color: var(--text-primary);
  max-height: 400px;
  overflow: auto;
  font-family: var(--font-mono);
}
.exec-output code { font-family: inherit; }
</style>
