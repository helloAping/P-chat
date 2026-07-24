<script setup lang="ts">
// P3-2 tool list drawer. Slides out from the right edge
// of the chat area and shows every tool the LLM has
// access to — built-ins and the user's dynamic YAML
// tools alike. Powers the user's mental model of "what
// can the assistant do right now?" and surfaces the
// source YAML for dynamic tools so they can audit what
// they actually wrote.
//
// State: kept local (not in the chat store) because the
// drawer owns its form inputs and recent trial results. The
// data request is scoped to the active session so project-level
// tools can appear without leaking into other projects.
//
// Refresh: on mount, every time the drawer re-opens, and when
// the active session changes. The global watcher still polls
// every 5s; project tools are scanned on demand from the active
// project's .p-chat/tools directory.
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { NDrawer, NDrawerContent, NTag, NButton, NEmpty, NIcon, NList, NListItem, NThing, NInput, NInputNumber, NSwitch, NSelect, useMessage } from 'naive-ui'
import { RefreshCw as RefreshIcon, Wrench as WrenchIcon, FileCode as FileCodeIcon, AlertTriangle as AlertTriangleIcon, Play as PlayIcon, FlaskConical as FlaskIcon } from 'lucide-vue-next'
import { listToolsDetailed, trialTool, type Tool, type ToolLoadDiagnostic, type ToolTrialResponse } from '../api/client'
import { state } from '../stores/chat'

const props = defineProps<{
  show: boolean
}>()

const emit = defineEmits<{
  (e: 'update:show', v: boolean): void
}>()

// visible is a v-model wrapper. The parent passes
// `show` and we forward close events back via emit.
const visible = computed({
  get: () => props.show,
  set: (v: boolean) => emit('update:show', v),
})

const tools = ref<Tool[]>([])
const diagnostics = ref<ToolLoadDiagnostic[]>([])
const loading = ref(false)
const error = ref<string | null>(null)

// toast is the Naive UI message API. The createMessage
// call is wrapped in useMessage() so this component can
// surface its own toasts without going through a global
// setup() injection point.
const toast = useMessage()

// openSource is the path of the YAML whose source the
// user has expanded. Stored in component state because
// it's pure UI bookkeeping — the server doesn't care.
const openSource = ref<string | null>(null)
const trialArgs = reactive<Record<string, Record<string, any>>>({})
const trialLoading = ref<Record<string, boolean>>({})
const trialResults = ref<Record<string, ToolTrialResponse>>({})

function schemaProps(t: Tool): Array<{ key: string; schema: any; required: boolean }> {
  const props = t.parameters?.properties || {}
  const required = new Set<string>(Array.isArray(t.parameters?.required) ? t.parameters.required : [])
  return Object.keys(props).map(key => ({ key, schema: props[key] || {}, required: required.has(key) }))
}

function ensureArgs(t: Tool) {
  if (!trialArgs[t.name]) trialArgs[t.name] = {}
  for (const p of schemaProps(t)) {
    if (trialArgs[t.name][p.key] !== undefined) continue
    if (p.schema?.type === 'boolean') trialArgs[t.name][p.key] = false
    else if (p.schema?.type === 'number' || p.schema?.type === 'integer') trialArgs[t.name][p.key] = null
    else trialArgs[t.name][p.key] = ''
  }
  return trialArgs[t.name]
}

function selectOptions(schema: any) {
  if (!Array.isArray(schema?.enum)) return []
  return schema.enum.map((v: any) => ({ label: String(v), value: v }))
}

async function runTrial(t: Tool, dryRun: boolean) {
  const args = { ...ensureArgs(t) }
  trialLoading.value = { ...trialLoading.value, [t.name]: true }
  try {
    const res = await trialTool(t.name, args, dryRun, state.currentID || undefined)
    trialResults.value = { ...trialResults.value, [t.name]: res }
    if (res.status === 'error') toast.error(dryRun ? '试运行失败' : '执行失败')
    else toast.success(dryRun ? '试运行完成' : '执行完成')
  } catch (e: any) {
    trialResults.value = {
      ...trialResults.value,
      [t.name]: {
        name: t.name,
        args: JSON.stringify(args),
        dry_run: dryRun,
        status: 'error',
        result: e?.message || '调用失败',
        error: e?.message || '调用失败',
        elapsed: '',
      },
    }
    toast.error('调用工具失败')
  } finally {
    trialLoading.value = { ...trialLoading.value, [t.name]: false }
  }
}

async function refresh() {
  loading.value = true
  error.value = null
  try {
    const res = await listToolsDetailed(state.currentID || undefined)
    tools.value = res.tools
    diagnostics.value = res.diagnostics
  } catch (e: any) {
    error.value = e?.message || '加载失败'
    toast.error('加载工具列表失败')
  } finally {
    loading.value = false
  }
}

// Re-fetch whenever the drawer transitions from closed
// to open. Project-level tools depend on the current
// session's project_path, so switching sessions while the
// drawer is open should refresh the effective tool view.
watch(() => props.show, (open) => {
  if (open) refresh()
})
watch(() => state.currentID, () => {
  trialResults.value = {}
  openSource.value = null
  if (props.show) refresh()
})
onMounted(refresh)

// builtInCount / dynamicCount drive the section
// headers. Cheap computeds so Vue can cache the
// re-render.
const builtInCount = computed(() => tools.value.filter(t => t.scope === 'builtin' || !t.dynamic).length)
const globalCount = computed(() => tools.value.filter(t => t.scope === 'global').length)
const projectCount = computed(() => tools.value.filter(t => t.scope === 'project').length)
const builtinTools = computed(() => tools.value.filter(t => t.scope === 'builtin' || !t.dynamic))
const globalTools = computed(() => tools.value.filter(t => t.scope === 'global'))
const projectTools = computed(() => tools.value.filter(t => t.scope === 'project'))
const errorDiagnostics = computed(() => diagnostics.value.filter(d => d.status === 'error'))

function scopeLabel(scope?: string) {
  if (scope === 'project') return '项目'
  if (scope === 'global') return '全局'
  return '内置'
}
</script>

<template>
  <NDrawer v-model:show="visible" :width="500" placement="right">
    <NDrawerContent title="可用工具" :native-scrollbar="false">
      <template #header>
        <div class="drawer-header">
          <NIcon :size="16" class="header-icon">
            <WrenchIcon />
          </NIcon>
          <span>可用工具</span>
          <NButton
            size="tiny"
            quaternary
            @click="refresh"
            :loading="loading"
            title="刷新"
          >
            <template #icon>
              <NIcon><RefreshIcon /></NIcon>
            </template>
            刷新
          </NButton>
        </div>
      </template>

      <div v-if="error" class="error-box">
        {{ error }}
      </div>

      <NEmpty
        v-else-if="!loading && tools.length === 0"
        description="还没有任何工具"
        class="empty"
      />

      <div v-else class="tool-list">
        <section v-if="errorDiagnostics.length > 0" class="section">
          <header class="section-header">
            <span class="section-title">加载诊断</span>
            <NTag size="small" type="error" :bordered="false">
              {{ errorDiagnostics.length }}
            </NTag>
          </header>
          <NList>
            <NListItem v-for="d in errorDiagnostics" :key="d.source">
              <NThing>
                <template #header>
                  <span class="tool-name">{{ d.name || '未加载工具' }}</span>
                  <NTag size="tiny" type="error" :bordered="false" class="badge">YAML 错误</NTag>
                </template>
                <template #description>
                  <div class="diagnostic-body">
                    <div class="diagnostic-error">
                      <NIcon :size="13"><AlertTriangleIcon /></NIcon>
                      <span>{{ d.error || '加载失败' }}</span>
                      <NTag size="tiny" :bordered="false">{{ scopeLabel(d.scope) }}</NTag>
                    </div>
                    <code>{{ d.source }}</code>
                    <p v-if="d.mod_at" class="source-hint">最后修改：{{ d.mod_at }}</p>
                  </div>
                </template>
              </NThing>
            </NListItem>
          </NList>
        </section>

        <!-- Built-in section: the agent's stock tools
             (exec_command, read_file, write_file, etc.).
             Listed first because they're what most users
             interact with; the dynamic section is the
             opt-in extension. -->
        <section v-if="builtinTools.length > 0" class="section">
          <header class="section-header">
            <span class="section-title">内置工具</span>
            <NTag size="small" :bordered="false">{{ builtInCount }}</NTag>
          </header>
          <NList>
            <NListItem v-for="t in builtinTools" :key="t.name">
              <NThing>
                <template #header>
                  <span class="tool-name">{{ t.name }}</span>
                </template>
                <template #description>{{ t.description }}</template>
              </NThing>
            </NListItem>
          </NList>
        </section>

        <section v-if="globalTools.length > 0" class="section">
          <header class="section-header">
            <span class="section-title">全局自定义工具</span>
            <NTag size="small" type="warning" :bordered="false">
              {{ globalCount }}
            </NTag>
          </header>
          <NList>
            <NListItem v-for="t in globalTools" :key="t.name">
              <NThing>
                <template #header>
                  <span class="tool-name">{{ t.name }}</span>
                  <NTag size="tiny" type="info" :bordered="false" class="badge">全局</NTag>
                </template>
                <template #description>{{ t.description }}</template>
                <template #header-extra>
                  <NButton
                    v-if="t.source"
                    size="tiny"
                    quaternary
                    @click="openSource = openSource === t.source ? null : t.source"
                  >
                    <template #icon>
                      <NIcon><FileCodeIcon /></NIcon>
                    </template>
                    {{ openSource === t.source ? '收起' : '查看源码' }}
                  </NButton>
                </template>
              </NThing>
              <div v-if="openSource === t.source && t.source" class="source-box">
                <code>{{ t.source }}</code>
                <p class="source-hint">保存后 5 秒内自动生效。</p>
              </div>
              <div class="trial-box">
                <div class="trial-header">
                  <span>试运行</span>
                  <NTag v-if="trialResults[t.name]?.dry_run" size="tiny" type="info" :bordered="false">dry-run</NTag>
                </div>
                <div v-if="schemaProps(t).length > 0" class="trial-form">
                  <label v-for="p in schemaProps(t)" :key="p.key" class="trial-field">
                    <span>{{ p.key }}<strong v-if="p.required">*</strong></span>
                    <NSwitch v-if="p.schema?.type === 'boolean'" v-model:value="ensureArgs(t)[p.key]" size="small" />
                    <NInputNumber v-else-if="p.schema?.type === 'number' || p.schema?.type === 'integer'" v-model:value="ensureArgs(t)[p.key]" size="small" :show-button="false" />
                    <NSelect v-else-if="Array.isArray(p.schema?.enum)" v-model:value="ensureArgs(t)[p.key]" :options="selectOptions(p.schema)" size="small" />
                    <NInput v-else v-model:value="ensureArgs(t)[p.key]" size="small" :placeholder="p.schema?.description || p.key" />
                  </label>
                </div>
                <p v-else class="source-hint">此工具没有参数。</p>
                <div class="trial-actions">
                  <NButton size="tiny" secondary :loading="trialLoading[t.name]" @click="runTrial(t, true)">
                    <template #icon><NIcon><FlaskIcon /></NIcon></template>
                    干跑
                  </NButton>
                  <NButton size="tiny" type="primary" secondary :loading="trialLoading[t.name]" @click="runTrial(t, false)">
                    <template #icon><NIcon><PlayIcon /></NIcon></template>
                    执行
                  </NButton>
                </div>
                <div v-if="trialResults[t.name]" class="trial-result" :class="'status-' + trialResults[t.name].status">
                  <div class="trial-result-head">
                    <span>{{ trialResults[t.name].status === 'error' ? '错误' : '结果' }}</span>
                    <span v-if="trialResults[t.name].elapsed">{{ trialResults[t.name].elapsed }}</span>
                  </div>
                  <pre>{{ trialResults[t.name].result }}</pre>
                </div>
              </div>
            </NListItem>
          </NList>
        </section>

        <section v-if="projectTools.length > 0" class="section">
          <header class="section-header">
            <span class="section-title">项目自定义工具</span>
            <NTag size="small" type="success" :bordered="false">
              {{ projectCount }}
            </NTag>
          </header>
          <NList>
            <NListItem v-for="t in projectTools" :key="t.name">
              <NThing>
                <template #header>
                  <span class="tool-name">{{ t.name }}</span>
                  <NTag size="tiny" type="success" :bordered="false" class="badge">项目</NTag>
                </template>
                <template #description>{{ t.description }}</template>
                <template #header-extra>
                  <NButton
                    v-if="t.source"
                    size="tiny"
                    quaternary
                    @click="openSource = openSource === t.source ? null : t.source"
                  >
                    <template #icon>
                      <NIcon><FileCodeIcon /></NIcon>
                    </template>
                    {{ openSource === t.source ? '收起' : '查看源码' }}
                  </NButton>
                </template>
              </NThing>
              <div v-if="openSource === t.source && t.source" class="source-box">
                <code>{{ t.source }}</code>
                <p class="source-hint">项目工具只在当前项目会话中生效，保存后刷新即可看到变化。</p>
              </div>
              <div class="trial-box">
                <div class="trial-header">
                  <span>试运行</span>
                  <NTag v-if="trialResults[t.name]?.dry_run" size="tiny" type="info" :bordered="false">dry-run</NTag>
                </div>
                <div v-if="schemaProps(t).length > 0" class="trial-form">
                  <label v-for="p in schemaProps(t)" :key="p.key" class="trial-field">
                    <span>{{ p.key }}<strong v-if="p.required">*</strong></span>
                    <NSwitch v-if="p.schema?.type === 'boolean'" v-model:value="ensureArgs(t)[p.key]" size="small" />
                    <NInputNumber v-else-if="p.schema?.type === 'number' || p.schema?.type === 'integer'" v-model:value="ensureArgs(t)[p.key]" size="small" :show-button="false" />
                    <NSelect v-else-if="Array.isArray(p.schema?.enum)" v-model:value="ensureArgs(t)[p.key]" :options="selectOptions(p.schema)" size="small" />
                    <NInput v-else v-model:value="ensureArgs(t)[p.key]" size="small" :placeholder="p.schema?.description || p.key" />
                  </label>
                </div>
                <p v-else class="source-hint">此工具没有参数。</p>
                <div class="trial-actions">
                  <NButton size="tiny" secondary :loading="trialLoading[t.name]" @click="runTrial(t, true)">
                    <template #icon><NIcon><FlaskIcon /></NIcon></template>
                    干跑
                  </NButton>
                  <NButton size="tiny" type="primary" secondary :loading="trialLoading[t.name]" @click="runTrial(t, false)">
                    <template #icon><NIcon><PlayIcon /></NIcon></template>
                    执行
                  </NButton>
                </div>
                <div v-if="trialResults[t.name]" class="trial-result" :class="'status-' + trialResults[t.name].status">
                  <div class="trial-result-head">
                    <span>{{ trialResults[t.name].status === 'error' ? '错误' : '结果' }}</span>
                    <span v-if="trialResults[t.name].elapsed">{{ trialResults[t.name].elapsed }}</span>
                  </div>
                  <pre>{{ trialResults[t.name].result }}</pre>
                </div>
              </div>
            </NListItem>
          </NList>
        </section>
      </div>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.drawer-header {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
}
.header-icon {
  color: var(--accent);
}
.tool-list {
  padding: 0 0 24px 0;
}
.section {
  margin-bottom: 24px;
}
.section-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 4px 8px 4px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-2);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.section-title {
  flex: 1;
}
.tool-name {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
  font-weight: 600;
  color: var(--text-1);
}
.badge {
  margin-left: 6px;
}
.source-box {
  margin-top: 8px;
  padding: 10px 12px;
  background: var(--bg-2);
  border: 1px solid var(--border);
  border-radius: 6px;
}
.source-box code {
  display: block;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  color: var(--text-1);
  word-break: break-all;
}
.source-hint {
  margin: 6px 0 0 0;
  font-size: 11px;
  color: var(--text-2);
}
.trial-box {
  margin-top: 10px;
  padding: 10px 12px;
  background: var(--bg-2);
  border: 1px solid var(--border);
  border-radius: 6px;
}
.trial-header,
.trial-actions,
.trial-result-head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.trial-header {
  justify-content: space-between;
  margin-bottom: 8px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-1);
}
.trial-form {
  display: grid;
  gap: 8px;
}
.trial-field {
  display: grid;
  grid-template-columns: minmax(90px, 0.42fr) minmax(0, 1fr);
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.trial-field > span {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  color: var(--text-2);
  min-width: 0;
  overflow-wrap: anywhere;
}
.trial-field strong {
  color: var(--danger, #dc2626);
}
.trial-actions {
  justify-content: flex-end;
  margin-top: 10px;
}
.trial-result {
  margin-top: 10px;
  border: 1px solid var(--border);
  border-radius: 6px;
  overflow: hidden;
  background: var(--bg-1);
}
.trial-result.status-error {
  border-color: var(--danger, #dc2626);
}
.trial-result-head {
  justify-content: space-between;
  padding: 6px 8px;
  border-bottom: 1px solid var(--border);
  font-size: 11px;
  color: var(--text-2);
}
.trial-result pre {
  margin: 0;
  padding: 8px;
  max-height: 180px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  line-height: 1.45;
  color: var(--text-1);
}
.diagnostic-body {
  display: grid;
  gap: 6px;
  min-width: 0;
}
.diagnostic-body code {
  display: block;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  color: var(--text-1);
  word-break: break-all;
}
.diagnostic-error {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--danger, #dc2626);
  font-size: 12px;
  line-height: 1.45;
}
.error-box {
  padding: 12px 16px;
  background: var(--error-bg, #fef2f2);
  border: 1px solid var(--error-border, #fecaca);
  border-radius: 6px;
  color: var(--error-text, #b91c1c);
  font-size: 12px;
}
.empty {
  padding: 48px 0;
}
</style>
