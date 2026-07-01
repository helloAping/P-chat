<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { NModal, NScrollbar, NSpin, NButton, NCard, useMessage } from 'naive-ui'
import * as api from '../api/client'
import type { TokenStat } from '../api/client'

const show = defineModel<boolean>('show', { default: false })
const message = useMessage()
const stats = ref<TokenStat[]>([])
const loading = ref(false)

const totalIn = computed(() => stats.value.reduce((sum, s) => sum + s.tokens_in, 0))
const totalOut = computed(() => stats.value.reduce((sum, s) => sum + s.tokens_out, 0))
const totalSessions = computed(() => stats.value.length)

function fmtTokens(n: number) {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return String(n)
}

function formatTime(t: number) {
  const d = new Date(t * 1000)
  return d.toLocaleDateString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

async function loadStats() {
  loading.value = true
  try {
    const r = await api.fetchTokenStats()
    stats.value = r.stats
  } catch (e: any) {
    message.error('加载失败: ' + e.message)
  }
  loading.value = false
}

onMounted(loadStats)
</script>

<template>
  <NModal v-model:show="show" preset="card" title="Token 用量统计" style="width: 560px; max-height: 80vh">
    <template #header-extra>
      <NButton size="tiny" @click="loadStats">刷新</NButton>
    </template>
    <NSpin :show="loading" size="small">
      <div class="token-dash">
        <div class="summary-row">
          <NCard size="small">
            <div class="summary-item">
              <span class="summary-label">总会话</span>
              <span class="summary-value">{{ totalSessions }}</span>
            </div>
          </NCard>
          <NCard size="small">
            <div class="summary-item">
              <span class="summary-label">总输入</span>
              <span class="summary-value">{{ fmtTokens(totalIn) }}</span>
            </div>
          </NCard>
          <NCard size="small">
            <div class="summary-item">
              <span class="summary-label">总输出</span>
              <span class="summary-value">{{ fmtTokens(totalOut) }}</span>
            </div>
          </NCard>
        </div>
        <NScrollbar style="max-height: 350px">
          <div v-if="stats.length === 0 && !loading" class="empty">
            暂无数据。完成一次对话后即可看到统计。
          </div>
          <div v-for="s in stats" :key="s.conversation_id" class="stat-row">
            <div class="stat-title">{{ s.conversation_title || '(无标题)' }}</div>
            <div class="stat-meta">
              <span>消息 {{ s.msg_count }} 条</span>
              <span>{{ fmtTokens(s.tokens_in) }}↓ / {{ fmtTokens(s.tokens_out) }}↑</span>
              <span class="stat-time">{{ formatTime(s.updated_at) }}</span>
            </div>
          </div>
        </NScrollbar>
      </div>
    </NSpin>
  </NModal>
</template>

<style scoped>
.token-dash { padding: 4px 0; }
.summary-row { display: flex; gap: 8px; margin-bottom: 16px; }
.summary-item { text-align: center; }
.summary-label { font-size: 12px; color: var(--text-3); display: block; }
.summary-value { font-size: 20px; font-weight: 700; }
.stat-row {
  padding: 10px 0;
  border-bottom: 1px solid var(--border-2);
}
.stat-row:last-child { border-bottom: none; }
.stat-title { font-size: 13px; font-weight: 500; }
.stat-meta { display: flex; gap: 16px; margin-top: 4px; font-size: 12px; color: var(--text-3); }
.stat-time { color: var(--text-4); margin-left: auto; }
.empty { text-align: center; padding: 32px; color: var(--text-4); font-size: 13px; }
</style>
