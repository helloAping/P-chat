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
  type MemorySnapshot,
  type DiagnosticsConfig,
} from '../api/client'

const message = useMessage()

const mem = ref<MemorySnapshot | null>(null)
const config = ref<DiagnosticsConfig | null>(null)
const loadingMem = ref(false)
const loadingCfg = ref(false)
const saving = ref(false)
const downloading = ref<'heap' | 'goroutine' | null>(null)

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

async function refreshMem() {
  try {
    loadingMem.value = true
    mem.value = await getMemoryDiagnostics()
  } catch (e) {
    // Background auto-refresh stays silent; only the first load surfaces an error.
    if (!mem.value) message.error(`拉取内存状态失败: ${(e as Error).message}`)
  } finally {
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
  downloading.value = kind
  try {
    downloadDiagnosticsSnapshot(kind)
    message.success(kind === 'heap' ? '已拉取堆快照 (.prof)，可用 go tool pprof -top 分析' : '已拉取 goroutine 快照 (.txt)')
  } finally {
    downloading.value = null
  }
}

let timer: number | undefined
onMounted(() => {
  refreshMem()
  refreshConfig()
  timer = window.setInterval(refreshMem, 3000)
})
onBeforeUnmount(() => {
  if (timer) window.clearInterval(timer)
})
</script>

<template>
  <div class="diagnostics-body">
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
          :disabled="downloading !== null"
          @click="onDownload('heap')"
        >
          拉取堆快照 (.prof)
        </NButton>
        <NButton
          size="small"
          :loading="downloading === 'goroutine'"
          :disabled="downloading !== null"
          @click="onDownload('goroutine')"
        >
          拉取 goroutine 快照 (.txt)
        </NButton>
        <span class="snapshot-hint">
          堆快照可用 <code>go tool pprof -top &lt;文件&gt;</code> 查看内存占用最高的方法
        </span>
      </div>
    </div>

    <!-- Monitor config -->
    <div class="settings-card">
      <div class="card-head">
        <span class="card-title">监控配置</span>
        <NTag v-if="config" size="small" :type="config.pprof_enabled ? 'success' : 'default'">
          pprof {{ config.pprof_enabled ? '已开启' : '已关闭 (PC_PPROF=0)' }}
        </NTag>
      </div>

      <NCollapse default-expanded-names="monitor" class="form-collapse">
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
  gap: 14px;
}

.settings-card {
  background: var(--bg-2);
  border: 1px solid var(--border-color, rgba(128, 128, 128, 0.2));
  border-radius: 10px;
  padding: 14px 16px;
}

.card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}

.card-title {
  font-weight: 600;
  font-size: 13px;
}

.goroutine-tag {
  margin-bottom: 10px;
}

.mem-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 10px 16px;
  margin-bottom: 14px;
}

.mem-cell {
  background: var(--bg-1, rgba(128, 128, 128, 0.08));
  border-radius: 8px;
  padding: 8px 10px;
}

.mem-label {
  font-size: 11px;
  opacity: 0.65;
}

.mem-value {
  font-size: 15px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  margin-top: 2px;
}

.snapshot-row {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.snapshot-hint {
  font-size: 11px;
  opacity: 0.6;
}

.snapshot-hint code {
  background: var(--bg-1, rgba(128, 128, 128, 0.12));
  padding: 1px 4px;
  border-radius: 4px;
}

.form-collapse {
  margin-top: 4px;
}

.form-grid {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.form-row {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.form-label {
  width: 150px;
  flex-shrink: 0;
  font-size: 12px;
}

.form-hint {
  font-size: 11px;
  opacity: 0.6;
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
  font-size: 11px;
  opacity: 0.5;
}

.save-hint.dirty {
  opacity: 1;
  color: var(--accent);
}
</style>
