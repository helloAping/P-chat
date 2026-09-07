<script setup lang="ts">
import { computed } from 'vue'
import { NButton, NInput } from 'naive-ui'
import { Plus, Trash2 } from './icons'

interface ProviderHeaderRow {
  id: string
  name: string
  value: string
}

const props = defineProps<{
  modelValue: ProviderHeaderRow[]
}>()

const emit = defineEmits<{
  (event: 'update:modelValue', value: ProviderHeaderRow[]): void
}>()

let rowSequence = 0

const headerNamePattern = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/
const exampleHeaders: Array<Omit<ProviderHeaderRow, 'id'>> = [
  { name: 'x-opencode-session', value: '{{conversation_id}}' },
  { name: 'x-pchat-message-id', value: '{{message_id}}' },
  { name: 'x-request-id', value: '{{uuid}}' },
  { name: 'x-snowflake-id', value: '{{snowflake_id}}' },
]

const placeholderGroups = [
  { value: '{{conversation_id}}', label: '当前对话' },
  { value: '{{message_id}}', label: '当前消息' },
  { value: '{{trace_id}}', label: '链路追踪' },
  { value: '{{uuid}}', label: '随机 UUID' },
  { value: '{{snowflake_id}}', label: '雪花 ID' },
  { value: '{{timestamp}}', label: '秒时间戳' },
  { value: '{{timestamp_ms}}', label: '毫秒时间戳' },
]

const duplicateNames = computed(() => {
  const counts = new Map<string, number>()
  for (const row of props.modelValue) {
    const name = row.name.trim().toLowerCase()
    if (name) counts.set(name, (counts.get(name) || 0) + 1)
  }
  return new Set([...counts].filter(([, count]) => count > 1).map(([name]) => name))
})

function createRow(name = '', value = ''): ProviderHeaderRow {
  rowSequence += 1
  return { id: `provider-header-${Date.now()}-${rowSequence}`, name, value }
}

function addRow() {
  emit('update:modelValue', [...props.modelValue, createRow()])
}

function removeRow(index: number) {
  emit('update:modelValue', props.modelValue.filter((_, rowIndex) => rowIndex !== index))
}

function updateRow(index: number, field: 'name' | 'value', value: string) {
  emit('update:modelValue', props.modelValue.map((row, rowIndex) => (
    rowIndex === index ? { ...row, [field]: value } : row
  )))
}

function fillExamples() {
  emit('update:modelValue', exampleHeaders.map(row => createRow(row.name, row.value)))
}

function rowNameInvalid(row: ProviderHeaderRow): boolean {
  const name = row.name.trim()
  if (!name) return row.value.length > 0
  return !headerNamePattern.test(name) || duplicateNames.value.has(name.toLowerCase())
}
</script>

<template>
  <div class="headers-editor">
    <div class="headers-editor-head">
      <div>
        <div class="headers-editor-title">
          <span>自定义请求头</span>
          <span class="headers-count">{{ modelValue.length }}</span>
        </div>
        <p class="headers-editor-description">用于该供应商的模型调用、连接测试、模型列表与媒体生成请求。</p>
      </div>
      <div class="headers-editor-actions">
        <NButton size="tiny" secondary @click="fillExamples">填入示例</NButton>
        <NButton size="tiny" type="primary" ghost @click="addRow">
          <template #icon><Plus :size="12" /></template>
          添加
        </NButton>
      </div>
    </div>

    <div v-if="modelValue.length" class="header-rows">
      <div class="header-columns" aria-hidden="true">
        <span>Header 名称</span>
        <span>值或动态模板</span>
        <span></span>
      </div>
      <div v-for="(row, index) in modelValue" :key="row.id" class="header-row">
        <NInput
          :value="row.name"
          :status="rowNameInvalid(row) ? 'error' : undefined"
          size="small"
          placeholder="x-opencode-session"
          @update:value="updateRow(index, 'name', $event)"
        />
        <NInput
          :value="row.value"
          size="small"
          placeholder="固定值或 {{conversation_id}}"
          @update:value="updateRow(index, 'value', $event)"
        />
        <NButton size="small" quaternary type="error" aria-label="删除请求头" @click="removeRow(index)">
          <template #icon><Trash2 :size="13" /></template>
        </NButton>
      </div>
    </div>
    <button v-else type="button" class="headers-empty" @click="addRow">
      尚未配置额外请求头，点击添加一行
    </button>

    <div class="placeholder-strip">
      <span class="placeholder-lead">动态参数</span>
      <span
        v-for="placeholder in placeholderGroups"
        :key="placeholder.value"
        class="placeholder-chip"
        :title="placeholder.label"
      >{{ placeholder.value }}</span>
    </div>
    <p class="headers-footnote">
      动态值在每次上游请求发出前生成；同一次请求中的多个请求头共享同一个 UUID 与雪花 ID。自定义值会覆盖同名默认请求头。
    </p>
  </div>
</template>

<style scoped>
.headers-editor {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-3);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  background: var(--surface-2);
}

.headers-editor-head,
.headers-editor-title,
.headers-editor-actions,
.placeholder-strip {
  display: flex;
  align-items: center;
}

.headers-editor-head {
  justify-content: space-between;
  gap: var(--space-3);
}

.headers-editor-title {
  gap: var(--space-2);
  color: var(--text-primary);
  font-size: 12.5px;
  font-weight: 600;
}

.headers-count {
  min-width: 20px;
  padding: 1px 6px;
  border-radius: 999px;
  background: var(--surface-3);
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
  text-align: center;
}

.headers-editor-description,
.headers-footnote {
  margin: 3px 0 0;
  color: var(--text-tertiary);
  font-size: 11px;
  line-height: 1.5;
}

.headers-editor-actions {
  gap: var(--space-2);
  flex-shrink: 0;
}

.header-rows {
  display: grid;
  gap: var(--space-2);
}

.header-columns,
.header-row {
  display: grid;
  grid-template-columns: minmax(150px, 0.8fr) minmax(210px, 1.3fr) 30px;
  gap: var(--space-2);
  align-items: center;
}

.header-columns {
  padding: 0 2px;
  color: var(--text-quaternary);
  font-size: 10.5px;
}

.header-row :deep(input) {
  font-family: var(--font-mono);
  font-size: 11.5px;
}

.headers-empty {
  width: 100%;
  padding: 12px;
  border: 1px dashed var(--border-default);
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-tertiary);
  font: inherit;
  font-size: 11.5px;
  cursor: pointer;
  transition: border-color var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out), background var(--dur-fast) var(--ease-out);
}

.headers-empty:hover {
  border-color: var(--brand-300);
  background: var(--surface-1);
  color: var(--brand-600);
}

.placeholder-strip {
  gap: var(--space-1);
  flex-wrap: wrap;
}

.placeholder-lead {
  margin-right: var(--space-1);
  color: var(--text-tertiary);
  font-size: 10.5px;
}

.placeholder-chip {
  padding: 2px 6px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-1);
  color: var(--text-secondary);
  font-family: var(--font-mono);
  font-size: 10px;
  line-height: 1.35;
}

@media (max-width: 720px) {
  .headers-editor-head {
    align-items: flex-start;
    flex-direction: column;
  }

  .header-columns {
    display: none;
  }

  .header-row {
    grid-template-columns: 1fr 30px;
  }

  .header-row > :nth-child(2) {
    grid-column: 1;
  }

  .header-row > :nth-child(3) {
    grid-column: 2;
    grid-row: 1 / span 2;
  }
}
</style>
