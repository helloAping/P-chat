<script setup lang="ts">
import { computed } from 'vue'
import { NButton, NInput, NPopover } from 'naive-ui'
import { HelpCircle, Plus, Trash2 } from './icons'

interface ProviderHeaderRow {
  id: string
  name: string
  value: string
}

interface HeaderPlaceholder {
  value: string
  label: string
  description: string
  scope: string
  usage: string
}

interface HeaderPlaceholderGroup {
  label: string
  items: HeaderPlaceholder[]
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
  { name: 'x-session-id', value: '{{session_id}}' },
  { name: 'x-message-id', value: '{{message_id}}' },
  { name: 'x-request-id', value: '{{uuid}}' },
  { name: 'x-snowflake-id', value: '{{snowflake_id}}' },
]

const placeholderGroups: HeaderPlaceholderGroup[] = [
  {
    label: '会话与链路',
    items: [
      {
        value: '{{conversation_id}}',
        label: '对话 ID',
        description: '当前对话的稳定标识，同一对话的多轮模型调用保持不变。无会话上下文的请求可能为空。',
        scope: '整个对话',
        usage: '适合需要在多轮请求中保持一致的会话标识。',
      },
      {
        value: '{{session_id}}',
        label: '会话 ID（别名）',
        description: 'conversation_id 的兼容别名，两者始终展开为相同值。无会话上下文的请求可能为空。',
        scope: '整个对话',
        usage: '用于上游接口以 session_id 命名会话标识的场景。',
      },
      {
        value: '{{message_id}}',
        label: '消息 ID',
        description: '触发当前调用的消息标识；不同消息通常不同，连接测试或模型列表请求可能为空。',
        scope: '当前消息',
        usage: '适合关联一轮用户消息与对应的上游请求。',
      },
      {
        value: '{{trace_id}}',
        label: 'Trace ID',
        description: '当前调用链路的追踪标识；未创建追踪上下文时可能为空。',
        scope: '当前请求链路',
        usage: '适合串联应用日志、代理日志与上游服务日志。',
      },
    ],
  },
  {
    label: '请求级生成',
    items: [
      {
        value: '{{uuid}}',
        label: '随机 UUID',
        description: '每次上游 HTTP 请求生成一个新的标准 UUID；同一请求的多个请求头共享该值。',
        scope: '单次上游请求',
        usage: '适合作为 x-request-id，不适合作为需要跨轮稳定的会话 ID。',
      },
      {
        value: '{{snowflake_id}}',
        label: '雪花 ID',
        description: '每次上游 HTTP 请求生成一个本地 64 位雪花 ID；同一请求的多个请求头共享该值。',
        scope: '单次上游请求',
        usage: '适合需要纯数字请求标识的上游接口。',
      },
      {
        value: '{{timestamp}}',
        label: '秒时间戳',
        description: '请求发出前生成的 Unix 秒级时间戳。',
        scope: '单次上游请求',
        usage: '适合上游鉴权、审计或请求时效校验。',
      },
      {
        value: '{{timestamp_ms}}',
        label: '毫秒时间戳',
        description: '请求发出前生成的 Unix 毫秒级时间戳。',
        scope: '单次上游请求',
        usage: '适合需要毫秒精度的鉴权、排序或审计场景。',
      },
    ],
  },
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
  const existingNames = new Set(props.modelValue.map(row => row.name.trim().toLowerCase()))
  const missingExamples = exampleHeaders
    .filter(row => !existingNames.has(row.name.toLowerCase()))
    .map(row => createRow(row.name, row.value))
  emit('update:modelValue', [...props.modelValue, ...missingExamples])
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
          <NPopover trigger="hover" placement="right-start" :delay="180">
            <template #trigger>
              <button
                type="button"
                class="headers-help-btn"
                title="查看动态参数说明"
                aria-label="查看动态参数说明"
              >
                <HelpCircle :size="13" />
              </button>
            </template>
            <div class="placeholder-popover">
              <div class="placeholder-popover-title">动态参数</div>
              <p>可在请求头值中使用，发送上游请求前自动展开。</p>
              <div v-for="group in placeholderGroups" :key="group.label" class="placeholder-popover-group">
                <div class="placeholder-group-label">{{ group.label }}</div>
                <div class="placeholder-popover-items">
                  <div
                    v-for="placeholder in group.items"
                    :key="placeholder.value"
                    class="placeholder-popover-item"
                  >
                    <code>{{ placeholder.value }}</code>
                    <span class="placeholder-popover-item-label">{{ placeholder.label }}</span>
                    <span class="placeholder-popover-item-desc">{{ placeholder.description }}</span>
                    <span class="placeholder-popover-item-usage">
                      稳定范围：{{ placeholder.scope }} · 适用场景：{{ placeholder.usage }}
                    </span>
                  </div>
                </div>
              </div>
            </div>
          </NPopover>
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
          placeholder="x-session-id"
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

    <p class="headers-footnote">
      模板会在上游请求发出前展开；自定义值会覆盖同名默认请求头。
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
.headers-help-btn {
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

.headers-help-btn {
  justify-content: center;
  width: calc(var(--space-6) - var(--space-1));
  height: calc(var(--space-6) - var(--space-1));
  padding: 0;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-1);
  color: var(--text-tertiary);
  cursor: help;
  transition: var(--transition-colors);
}

.headers-help-btn:hover {
  border-color: var(--border-default);
  background: var(--surface-3);
  color: var(--brand-600);
}

.headers-editor-description,
.headers-footnote {
  margin: var(--space-1) 0 0;
  color: var(--text-tertiary);
  font-size: 11.5px;
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
  padding: 0 var(--space-1);
  color: var(--text-quaternary);
  font-size: 11.5px;
}

.header-row :deep(input) {
  font-family: var(--font-mono);
  font-size: 11.5px;
}

.headers-empty {
  width: 100%;
  padding: var(--space-3);
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
  border-color: var(--brand-500);
  background: var(--surface-1);
  color: var(--brand-600);
}

.headers-empty:active {
  background: var(--surface-3);
}

.headers-empty:focus-visible {
  outline: 2px solid var(--brand-500);
  outline-offset: var(--space-1);
}

.placeholder-popover {
  display: grid;
  gap: var(--space-2);
  max-width: calc(var(--space-8) * 11);
  color: var(--text-secondary);
  font-size: 11.5px;
  line-height: 1.5;
}

.placeholder-popover-title {
  color: var(--text-primary);
  font-size: 12.5px;
  font-weight: 700;
}

.placeholder-popover p {
  margin: 0;
  color: var(--text-tertiary);
}

.placeholder-popover-group {
  display: grid;
  gap: var(--space-1);
}

.placeholder-group-label {
  color: var(--text-tertiary);
  font-size: 11.5px;
  font-weight: 600;
}

.placeholder-popover-items {
  display: grid;
  gap: var(--space-1);
}

.placeholder-popover-item {
  display: grid;
  grid-template-columns: minmax(calc(var(--space-8) * 3), max-content) minmax(calc(var(--space-8) * 2), max-content) minmax(0, 1fr);
  gap: var(--space-2);
  align-items: start;
  padding: var(--space-1) var(--space-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-1);
}

.placeholder-popover-item code {
  color: var(--text-secondary);
  font-family: var(--font-mono);
  font-size: 11.5px;
  white-space: nowrap;
}

.placeholder-popover-item-label {
  color: var(--text-primary);
  font-weight: 600;
  white-space: nowrap;
}

.placeholder-popover-item-desc {
  min-width: 0;
  color: var(--text-secondary);
}

.placeholder-popover-item-usage {
  grid-column: 1 / -1;
  color: var(--text-tertiary);
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

  .placeholder-popover {
    max-width: calc(var(--space-8) * 8);
  }

  .placeholder-popover-item {
    grid-template-columns: 1fr;
    gap: var(--space-1);
  }
}
</style>
