<script setup lang="ts">
// Diagnostics / 内存监控 settings panel.
//
// Rendered as a tab in AppSettingsModal. Three jobs:
//   1. Live memory stats (auto-refresh every 3s) — heap / GC / goroutines
//      / RSS, so a runaway loop (2026-08 OOM) is visible as it happens.
//   2. Tune the heartbeat interval and heap-dump threshold at runtime
//      (backed by GET/PATCH /api/v1/diagnostics/config).
//   3. Download a heap (.prof) or goroutine (.txt) snapshot for
//      `go tool pprof -top` / stack analysis.

import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  NButton, NCollapse, NCollapseItem, NInputNumber, NTag, useMessage,
} from 'naive-ui'
import {
  getMemoryDiagnostics,
  getDiagnosticsConfig,
  updateDiagnosticsConfig,
  downloadDiagnosticsSnapshot,
  previewStorageGC,
  runStorageGC,
  type MemorySnapshot,
  type DiagnosticsConfig,
  type StorageGCReport,
} from '../api/client'
import { loadSessions } from '../stores/chat'

const message = useMessage()

const mem = ref<MemorySnapshot | null>(null)
const config = ref<DiagnosticsConfig | null>(null)
const loadingMem = ref(false)
const loadingCfg = ref(false)
const saving = ref(false)
const downloading = ref<'heap' | 'goroutine' | null>(null)
const gcReport = ref<StorageGCReport | null>(null)
const gcLoading = ref(false)
const gcRunning = ref(false)
const gcSelected = ref<string[]>([])

// Editable draft. `loaded` mirrors the last server snapshot so we can
// compute dirty state (mirrors WebSearchSettings' form/settings split).
const form = ref({ memHeartbeatSec: 30, heapDumpMB: 1024 })
const loaded = ref<{ memHeartbeatSec: number; heapDumpMB: number } | null>(null)

const isDirty = computed(() => {
  const l = loaded.value
  if (!l) return false
  return l.memHeartbeatSec !== form.value.memHeartbeatSec || l.heapDumpMB !== form.value.heapDumpMB
})

// A goroutine count above this is the classic "leaked loop" signal.
const goroutineWarnThreshold = 300
const goroutineStatus = computed(() => {
  if (!mem.value) return null
  const n = mem.value.goroutines
  if (n > goroutineWarnThreshold) {
    return { type: 'error' as const, text: `${n} 个 goroutine — 疑似循环/泄漏，请拉取快照分析` }
  }
  if (n > 80) {
    return { type: 'warning' as const, text: `${n} 个 goroutine — 偏高` }
  }
  return { type: 'success' as const, text: `${n} 个 goroutine — 正常` }
})

const memRows = computed(() => [
  { label: '堆内存 (HeapAlloc)', value: mem.value ? `${mem.value.heap_alloc_mb} MB` : '—' },
  { label: '堆保留 (HeapSys)', value: mem.value ? `${mem.value.heap_sys_mb} MB` : '—' },
  { label: '堆对象数', value: mem.value ? mem.value.heap_objects.toLocaleString() : '—' },
  { label: 'GC 次数', value: mem.value ? mem.value.num_gc.toLocaleString() : '—' },
  { label: '进程 RSS', value: mem.value ? `${mem.value.rss_mb} MB` : '—' },
])

// inFlight guards against overlapping refreshes: if the server is slow and
// the 3s interval fires again, skip rather than queue a second request
// (rapid stacked requests were observed in server access logs).
let inFlight = false
async function refreshMem() {
  if (inFlight) return
  inFlight = true
  try {
    loadingMem.value = true
    mem.value = await getMemoryDiagnostics()
  } catch (e) {
    // Background auto-refresh stays silent; only the first load surfaces an error.
    if (!mem.value) message.error(`拉取内存状态失败: ${(e as Error).message}`)
  } finally {
    inFlight = false
    loadingMem.value = false
  }
}

async function refreshConfig() {
  try {
    loadingCfg.value = true
    config.value = await getDiagnosticsConfig()
    form.value.memHeartbeatSec = config.value.mem_heartbeat_sec
    form.value.heapDumpMB = config.value.heap_dump_mb
    loaded.value = {
      memHeartbeatSec: config.value.mem_heartbeat_sec,
      heapDumpMB: config.value.heap_dump_mb,
    }
  } catch (e) {
    message.error(`拉取监控配置失败: ${(e as Error).message}`)
  } finally {
    loadingCfg.value = false
  }
}

async function saveConfig() {
  try {
    saving.value = true
    config.value = await updateDiagnosticsConfig({
      mem_heartbeat_sec: form.value.memHeartbeatSec,
      heap_dump_mb: form.value.heapDumpMB,
    })
    loaded.value = {
      memHeartbeatSec: config.value.mem_heartbeat_sec,
      heapDumpMB: config.value.heap_dump_mb,
    }
    message.success('监控配置已保存')
  } catch (e) {
    message.error(`保存失败: ${(e as Error).message}`)
  } finally {
    saving.value = false
  }
}

async function onDownload(kind: 'heap' | 'goroutine') {
  if (!config.value?.snapshot_enabled) {
    message.warning('诊断快照默认关闭；请在可信本机启动前设置 PC_DIAGNOSTICS_SNAPSHOTS=1')
    return
  }
  downloading.value = kind
  try {
    downloadDiagnosticsSnapshot(kind)
    message.success(kind === 'heap' ? '已拉取堆快照 (.prof)，可用 go tool pprof -top 分析' : '已拉取 goroutine 快照 (.txt)')
  } finally {
    downloading.value = null
  }
}

function formatBytes(n: number): string {
  if (!n) return '0 B'
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

async function refreshGC() {
  gcLoading.value = true
  try {
    gcReport.value = await previewStorageGC()
    const reclaim = (gcReport.value.items || [])
      .filter(item => item.id !== 'vacuum' && (item.count > 0 || item.bytes > 0))
      .map(item => item.id)
    gcSelected.value = reclaim.length ? [...reclaim, 'vacuum'] : []
  } catch (e) {
    message.error(`扫描存储失败: ${(e as Error).message}`)
  } finally {
    gcLoading.value = false
  }
}

async function runGC() {
  if (gcSelected.value.length === 0) return
  gcRunning.value = true
  try {
    const report = await runStorageGC(gcSelected.value)
    gcReport.value = report
    const saved = formatBytes(report.total_bytes)
    if (report.vacuum_error) {
      message.warning(`已回收 ${saved}，压缩文件失败：${report.vacuum_error}`)
    } else {
      message.success(report.vacuumed ? `已回收 ${saved}，并压缩了数据库文件` : `已回收 ${saved}`)
    }
    await loadSessions()
    await refreshGC()
  } catch (e) {
    message.error(`回收失败: ${(e as Error).message}`)
  } finally {
    gcRunning.value = false
  }
}
let timer: number | undefined
onMounted(() => {
  refreshMem()
  refreshConfig()
  void refreshGC()
  timer = window.setInterval(() => {
    // Skip polling while the document is hidden (e.g. a background
    // conversation) — a monitor panel doesn't need live data then, and
    // background requests add load for nothing.
    if (document.hidden) return
    refreshMem()
  }, 3000)
})
onBeforeUnmount(() => {
  if (timer) window.clearInterval(timer)
})
</script>

<template>
  <div class="diagnostics-body">
    <div class="settings-card">
      <div class="card-head">
        <span class="card-title">存储清理</span>
        <NButton size="tiny" quaternary :loading="gcLoading" @click="refreshGC">
          重新扫描
        </NButton>
      </div>
      <p class="form-hint gc-lead">
        回收的是库里的重复大字段，不是把聊天记录整段删掉。用户原文和助手最终回复会留下；每个对话最近 40 条的工具输出和思考过程也会留下。
      </p>
      <label v-for="item in gcReport?.items || []" :key="item.id" class="gc-row">
        <input
          v-model="gcSelected"
          type="checkbox"
          :value="item.id"
          :disabled="gcRunning || (item.id !== 'vacuum' && item.count === 0 && item.bytes === 0)"
        />
        <span class="gc-copy">
          <span class="gc-label">{{ item.label }}</span>
          <span class="gc-detail">{{ item.detail }}</span>
        </span>
        <span class="gc-stat">
          <template v-if="item.id === 'vacuum'">压缩文件</template>
          <template v-else>{{ item.count }} 项 · {{ formatBytes(item.bytes) }}</template>
        </span>
      </label>
      <div class="save-row">
        <NButton
          size="small"
          type="primary"
          :loading="gcRunning"
          :disabled="gcLoading || gcSelected.length === 0"
          @click="runGC"
        >
          开始回收
        </NButton>
        <span class="form-hint">
          预计可回收 {{ formatBytes(gcReport?.total_bytes || 0) }}。回收后重新打开对话即可看到结果。
        </span>
      </div>
    </div>

    <!-- Live memory status -->
    <div class="settings-card">
      <div class="card-head">
        <span class="card-title">实时内存</span>
        <NButton size="tiny" quaternary :loading="loadingMem" @click="refreshMem">
          刷新
        </NButton>
      </div>
      <NTag
        v-if="goroutineStatus"
        size="small"
        :type="goroutineStatus.type"
        class="goroutine-tag"
      >
        {{ goroutineStatus.text }}
      </NTag>
      <div class="mem-grid">
        <div v-for="row in memRows" :key="row.label" class="mem-cell">
          <div class="mem-label">{{ row.label }}</div>
          <div class="mem-value">{{ row.value }}</div>
        </div>
      </div>
      <div class="snapshot-row">
        <NButton
          size="small"
          type="primary"
          :loading="downloading === 'heap'"
          :disabled="downloading !== null || !config?.snapshot_enabled"
          @click="onDownload('heap')"
        >
          拉取堆快照 (.prof)
        </NButton>
        <NButton
          size="small"
          :loading="downloading === 'goroutine'"
          :disabled="downloading !== null || !config?.snapshot_enabled"
          @click="onDownload('goroutine')"
        >
          拉取 goroutine 快照 (.txt)
        </NButton>
        <span class="snapshot-hint">
          <template v-if="config?.snapshot_enabled">
            仅允许本机下载；堆快照可用 <code>go tool pprof -top &lt;文件&gt;</code> 分析
          </template>
          <template v-else>
            默认关闭。仅在可信本机启动前设置 <code>PC_DIAGNOSTICS_SNAPSHOTS=1</code>；快照可能包含敏感运行时信息。
          </template>
        </span>
      </div>
    </div>

    <!-- Monitor config -->
    <div class="settings-card">
      <div class="card-head">
        <span class="card-title">监控配置</span>
        <NTag v-if="config" size="small" :type="config.pprof_enabled ? 'success' : 'default'">
          pprof {{ config.pprof_enabled ? '已开启' : '已关闭 (PC_PPROF=1 开启)' }}
        </NTag>
        <NTag v-if="config" size="small" :type="config.snapshot_enabled ? 'warning' : 'default'">
          快照下载 {{ config.snapshot_enabled ? '已显式开启' : '默认关闭' }}
        </NTag>
      </div>

      <NCollapse default-expanded-names="monitor" class="settings-collapse settings-collapse--inset">
        <NCollapseItem title="内存监控" name="monitor">
          <div class="form-grid">
            <div class="form-row">
              <div class="form-label">心跳日志间隔 (秒)</div>
              <NInputNumber
                v-model:value="form.memHeartbeatSec"
                :min="0"
                :max="3600"
                size="small"
                style="width: 120px"
              />
              <div class="form-hint">每 N 秒在 server-debug.log 打印一行内存/goroutine；0 = 关闭</div>
            </div>

            <div class="form-row">
              <div class="form-label">堆自动 dump 阈值 (MB)</div>
              <NInputNumber
                v-model:value="form.heapDumpMB"
                :min="0"
                :max="102400"
                size="small"
                style="width: 120px"
              />
              <div class="form-hint">堆超过该值时自动写 heap-&lt;时间戳&gt;.prof；0 = 关闭</div>
            </div>

            <div v-if="config" class="form-row">
              <div class="form-label">dump 目录</div>
              <div class="status-val code-val">{{ config.heap_dump_dir }}</div>
            </div>
          </div>
        </NCollapseItem>
      </NCollapse>

      <div class="save-row">
        <NButton
          type="primary"
          size="small"
          :disabled="!isDirty || saving"
          :loading="saving"
          @click="saveConfig"
        >
          保存
        </NButton>
        <span class="save-hint" :class="{ dirty: isDirty }">
          {{ isDirty ? '有未保存的改动' : '已同步' }}
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.diagnostics-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

.gc-lead {
  margin: 0 0 var(--space-3);
}

.gc-row {
  display: grid;
  grid-template-columns: 16px minmax(0, 1fr) auto;
  gap: var(--space-3);
  align-items: start;
  padding: var(--space-2) 0;
  border-top: 1px solid var(--border-subtle);
  color: var(--text-primary);
}

.gc-row input {
  margin-top: 3px;
}

.gc-copy {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.gc-label {
  font-size: 12.5px;
  font-weight: 600;
}

.gc-detail {
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--text-tertiary);
}

.gc-stat {
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  color: var(--text-secondary);
  white-space: nowrap;
}

.settings-card {
  background: var(--surface-1);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  padding: 14px 16px;
}

.card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--space-3);
}

.card-title {
  font-weight: 600;
  font-size: 13px;
  color: var(--text-primary);
}

.goroutine-tag {
  margin-bottom: var(--space-3);
}

.mem-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: var(--space-3) var(--space-4);
  margin-bottom: var(--space-4);
}

.mem-cell {
  background: var(--surface-2);
  border-radius: var(--radius-md);
  padding: var(--space-2) var(--space-3);
}

.mem-label {
  font-size: 11.5px;
  color: var(--text-tertiary);
}

.mem-value {
  font-size: 15px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  margin-top: 2px;
  color: var(--text-primary);
}

.snapshot-row {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex-wrap: wrap;
}

.snapshot-hint {
  font-size: 11.5px;
  color: var(--text-tertiary);
}

.snapshot-hint code {
  background: var(--surface-2);
  padding: 1px 4px;
  border-radius: var(--radius-sm);
  font-family: var(--font-mono);
}

.form-grid {
  display: flex;
  flex-direction: column;
  gap: 0;
}

.form-row {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  padding: 10px 0;
  border-top: 1px solid var(--border-subtle);
}

.form-row:first-child {
  border-top: none;
  padding-top: 2px;
}

.form-label {
  width: 150px;
  flex-shrink: 0;
  font-size: 12.5px;
  font-weight: 500;
  color: var(--text-primary);
}

.form-hint {
  font-size: 11.5px;
  color: var(--text-tertiary);
  line-height: 1.5;
}

.status-val {
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

.code-val {
  font-family: var(--mono-font, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace);
  font-size: 11px;
  word-break: break-all;
}

.save-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 12px;
}

.save-hint {
  font-size: 11.5px;
  color: var(--text-tertiary);
}

.save-hint.dirty {
  color: var(--brand-600);
}
</style>
