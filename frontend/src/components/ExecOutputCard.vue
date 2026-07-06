<script setup lang="ts">
import { ref, computed } from 'vue'

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
      <span class="exec-arrow">{{ collapsed ? '▶' : '▼' }}</span>
      <span class="exec-title">{{ command || 'exec_command' }}</span>
      <span v-if="elapsed" class="exec-elapsed">{{ elapsed }}</span>
      <span class="exec-actions">
        <button class="exec-btn" title="复制全部输出" @click.stop="copyAll">📋</button>
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
.exec-card {
  margin: 6px 0;
  border: 1px solid var(--border);
  border-radius: 6px;
  overflow: hidden;
  background: #1e1e1e;
  font-family: 'Consolas', 'Courier New', monospace;
}
.exec-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  cursor: pointer;
  background: #2d2d2d;
  user-select: none;
}
.exec-arrow { font-size: 10px; color: #888; }
.exec-title { font-size: 12px; font-weight: 600; color: #ccc; }
.exec-elapsed { font-size: 11px; color: #888; }
.exec-actions { margin-left: auto; display: flex; gap: 4px; }
.exec-btn {
  background: none;
  border: none;
  cursor: pointer;
  font-size: 14px;
  padding: 2px 4px;
  opacity: 0.6;
}
.exec-btn:hover { opacity: 1; }
.exec-body { padding: 0; }
.exec-search { padding: 6px 10px; background: #252525; }
.exec-search-input {
  width: 100%;
  padding: 4px 8px;
  font-size: 12px;
  border: 1px solid #444;
  border-radius: 4px;
  background: #1a1a1a;
  color: #ddd;
  outline: none;
}
.exec-search-input:focus { border-color: var(--accent); }
.exec-output {
  margin: 0;
  padding: 8px 10px;
  font-size: 12px;
  line-height: 1.5;
  color: #ccc;
  max-height: 400px;
  overflow: auto;
}
.exec-output code { font-family: inherit; }
</style>
