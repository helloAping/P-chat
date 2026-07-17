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
// tool list is per-server, not per-session. Multiple
// sessions share the same registry; pinning the list to
// one session would mean re-fetching on every switch.
//
// Refresh: on mount and every time the drawer
// re-opens. The 5s server-side polling means a new
// YAML is registered within 5s; the user can also pull
// the manual "刷新" button to skip the wait.
import { computed, onMounted, ref, watch } from 'vue'
import { NDrawer, NDrawerContent, NTag, NSpin, NButton, NEmpty, NIcon, NList, NListItem, NThing, NScrollbar, useMessage } from 'naive-ui'
import { RefreshCw as RefreshIcon, Wrench as WrenchIcon, FileCode as FileCodeIcon } from 'lucide-vue-next'
import { listTools, type Tool } from '../api/client'

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

async function refresh() {
  loading.value = true
  error.value = null
  try {
    tools.value = await listTools()
  } catch (e: any) {
    error.value = e?.message || '加载失败'
    toast.error('加载工具列表失败')
  } finally {
    loading.value = false
  }
}

// Re-fetch whenever the drawer transitions from closed
// to open. The 5s server-side polling means we don't
// strictly need to re-fetch on every open (the cached
// list is usually still valid), but the cost is one
// HTTP call and the benefit is a fresh view after the
// user just edited a YAML.
watch(() => props.show, (open) => {
  if (open) refresh()
})
onMounted(refresh)

// builtInCount / dynamicCount drive the section
// headers. Cheap computeds so Vue can cache the
// re-render.
const builtInCount = computed(() => tools.value.filter(t => !t.dynamic).length)
const dynamicCount = computed(() => tools.value.filter(t => t.dynamic).length)
const builtinTools = computed(() => tools.value.filter(t => !t.dynamic))
const dynamicTools = computed(() => tools.value.filter(t => t.dynamic))
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

        <!-- Dynamic section: tools loaded from
             ~/.p-chat/tools/*.yaml. Each row has a
             "自定义" badge and a "查看源码" expander
             that shows the YAML path (the file content
             itself isn't shipped to the client — the
             user opens it in their own editor). -->
        <section v-if="dynamicTools.length > 0" class="section">
          <header class="section-header">
            <span class="section-title">自定义工具</span>
            <NTag size="small" type="warning" :bordered="false">
              {{ dynamicCount }}
            </NTag>
          </header>
          <NList>
            <NListItem v-for="t in dynamicTools" :key="t.name">
              <NThing>
                <template #header>
                  <span class="tool-name">{{ t.name }}</span>
                  <NTag size="tiny" type="info" :bordered="false" class="badge">自定义</NTag>
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
                <p class="source-hint">
                  在你的编辑器中打开该路径查看 / 编辑 YAML。
                  保存后 5 秒内自动生效。
                </p>
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
