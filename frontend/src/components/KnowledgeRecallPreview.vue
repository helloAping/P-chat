<script setup lang="ts">
// KnowledgeRecallPreview — 召回内容预览面板（可复用）。
// 输入问题或关键词，调用 POST /api/v1/knowledge/search 展示召回结果：
// 标题 / 来源 / 命中类型 / 相关性 / 解释 / 内容预览。
// 用于「全局知识库」详情页（当前库 / 全部）。
//
// KnowledgeRecallPreview — reusable recall-preview panel. Runs a query
// against POST /api/v1/knowledge/search and renders hits with title,
// source, match type, score, explanation and a content preview.
import { computed, ref } from 'vue'
import { NButton, NInput, NSelect } from 'naive-ui'
import { Search, Loader2, AlertCircle } from './icons'
import * as api from '../api/client'

const props = defineProps<{
  /** 固定检索范围（设置后不显示范围选择器）/ fixed base scope; hides the scope picker */
  fixedBases?: string[]
  /** 可选检索范围（全局视图用）/ selectable scopes for the global view */
  scopeOptions?: Array<{ label: string; bases: string[] }>
  /** 输入框 placeholder / input placeholder */
  placeholder?: string
}>()

const query = ref('')
const loading = ref(false)
const error = ref('')
const searched = ref(false)
const results = ref<api.KnowledgeSearchResult[]>([])
const stats = ref<api.KnowledgeSearchStats | null>(null)
const activeScope = ref(0)

const scopeSelectOptions = computed(() =>
  (props.scopeOptions || []).map((o, i) => ({ label: o.label, value: i })),
)

const currentBases = computed<string[] | undefined>(() => {
  if (props.fixedBases) return props.fixedBases
  const opt = (props.scopeOptions || [])[activeScope.value]
  return opt ? opt.bases : undefined
})

const matchTypeLabels: Record<string, string> = {
  path: '路径',
  filename: '文件名',
  title: '标题',
  keywords: '关键词',
  overview: '概览',
  l2: '文件',
  content: '正文',
}

function baseLabel(base?: string): string {
  if (!base) return ''
  return base
}

function matchTypeLabel(mt?: string): string {
  if (!mt) return ''
  return matchTypeLabels[mt] || mt
}

async function runSearch() {
  const q = query.value.trim()
  if (!q || loading.value) return
  loading.value = true
  error.value = ''
  try {
    const resp = await api.searchKnowledge(q, 8, currentBases.value)
    results.value = resp.results || []
    stats.value = resp.stats || null
    searched.value = true
  } catch (e: any) {
    error.value = e?.message || String(e)
    results.value = []
    stats.value = null
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="recall-preview">
    <div class="recall-search-row">
      <NSelect
        v-if="scopeSelectOptions.length > 0"
        v-model:value="activeScope"
        :options="scopeSelectOptions"
        size="small"
        class="recall-scope"
      />
      <NInput
        v-model:value="query"
        size="small"
        :placeholder="placeholder || '输入问题或关键词，预览会召回的内容…'"
        clearable
        @keydown.enter="runSearch"
      >
        <template #prefix>
          <Search :size="13" />
        </template>
      </NInput>
      <NButton size="small" type="primary" :loading="loading" :disabled="!query.trim()" @click="runSearch">
        预览
      </NButton>
    </div>

    <div v-if="error" class="recall-status is-error">
      <AlertCircle :size="14" />
      <span>{{ error }}</span>
    </div>

    <div v-else-if="loading" class="recall-status">
      <Loader2 :size="14" class="recall-spin" />
      <span>检索中…</span>
    </div>

    <template v-else-if="searched">
      <div v-if="results.length === 0" class="recall-status">
        <span>没有命中内容。换个关键词试试，或先上传 / 扫描知识文件。</span>
      </div>
      <div v-else class="recall-results">
        <div v-if="stats" class="recall-stats">
          命中 {{ stats.returned ?? results.length }} 条
          <template v-if="stats.bases && stats.bases.length">· 检索库 {{ stats.bases.length }}</template>
          <template v-if="stats.has_more">· 还有更多结果</template>
        </div>
        <div v-for="(r, i) in results" :key="i" class="recall-item">
          <div class="recall-item-head">
            <span class="recall-item-rank">{{ r.rank || i + 1 }}</span>
            <span class="recall-item-title" :title="r.title || r.source">{{ r.title || r.source || '(无标题)' }}</span>
            <span v-if="r.similarity > 0" class="recall-item-score">{{ r.similarity.toFixed(2) }}</span>
          </div>
          <div class="recall-item-meta">
            <span v-if="r.base" class="recall-tag">{{ baseLabel(r.base) }}</span>
            <span v-if="r.source" class="recall-tag recall-tag--path" :title="r.source">{{ r.source }}</span>
            <span v-if="r.match_type" class="recall-tag">命中{{ matchTypeLabel(r.match_type) }}</span>
          </div>
          <div v-if="r.explanation" class="recall-item-explain">{{ r.explanation }}</div>
          <div v-if="r.content" class="recall-item-content">{{ r.content }}</div>
        </div>
      </div>
    </template>

    <div v-else class="recall-status">
      <span>输入后点击「预览」，查看助手实际会召回的知识片段。</span>
    </div>
  </div>
</template>

<style scoped>
.recall-preview {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}

.recall-search-row {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

.recall-scope {
  width: 132px;
  flex-shrink: 0;
}

.recall-search-row :deep(.n-input) {
  flex: 1;
}

.recall-status {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-3);
  font-size: 11.5px;
  color: var(--text-tertiary);
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
}

.recall-status.is-error {
  color: var(--error-500);
  border-color: color-mix(in srgb, var(--error-500) 32%, var(--border-subtle));
}

.recall-spin {
  animation: recall-rotate 1s linear infinite;
}

@keyframes recall-rotate {
  to { transform: rotate(360deg); }
}

.recall-results {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  max-height: 320px;
  overflow-y: auto;
}

.recall-stats {
  font-size: 11px;
  color: var(--text-quaternary);
  padding: 0 var(--space-1);
}

.recall-item {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  padding: var(--space-3);
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
}

.recall-item-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
}

.recall-item-rank {
  flex-shrink: 0;
  min-width: 18px;
  height: 18px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 11px;
  font-family: var(--font-mono);
  color: var(--text-tertiary);
  background: var(--surface-3);
  border-radius: var(--radius-sm);
}

.recall-item-title {
  flex: 1;
  min-width: 0;
  font-size: 12.5px;
  font-weight: 500;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.recall-item-score {
  flex-shrink: 0;
  font-size: 11px;
  font-family: var(--font-mono);
  color: var(--text-tertiary);
}

.recall-item-meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-1);
}

.recall-tag {
  font-size: 11px;
  line-height: 1.5;
  padding: 0 var(--space-2);
  color: var(--text-tertiary);
  background: var(--surface-3);
  border-radius: var(--radius-pill);
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.recall-tag--path {
  font-family: var(--font-mono);
}

.recall-item-explain {
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--text-secondary);
}

.recall-item-content {
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--text-tertiary);
  white-space: pre-wrap;
  word-break: break-word;
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
</style>
