<script setup lang="ts">
// P2-5 race mode view. Renders N panes side-by-side,
// each a self-contained MessageList for one forked
// session. Streams run in parallel; the user sees all
// N answers stream in live. When every pane reaches
// `done`, the bottom shows the "🏆 选这个" button row.
//
// Layout:
//   - Desktop: CSS grid with N equal columns, separated
//     by a thin border. Each pane has its own vertical
//     scroll; the outer container does NOT scroll. This
//     matches the design intent of "compare side by side".
//   - Mobile: N stacked rows, one above the other. CSS
//     grid collapses to a single column below 720px so
//     the 3-pane layout doesn't become unreadable on a
//     phone. The plan explicitly mentions this fallback.
//
// Why not just open N MessageBubbles in a flexbox row?
// CSS grid keeps the column widths perfectly equal and
// the gutters consistent; flexbox would let tall
// content push siblings off the screen.
import { computed } from 'vue'
import { NButton, NEmpty, NTag } from 'naive-ui'
import { Trophy as TrophyIcon, X as XIcon } from 'lucide-vue-next'
import { state, pickWinner, cancelRace } from '../stores/chat'
import MessageBubble from './MessageBubble.vue'

const race = computed(() => state.race)

// statusLabel translates the per-pane status into a
// short tag for the pane header. "pending" → "排队中",
// "streaming" → "生成中", etc. Keeps the JSX below
// readable.
function statusLabel(s: string): string {
  switch (s) {
    case 'pending': return '排队中'
    case 'streaming': return '生成中'
    case 'complete': return '已完成'
    case 'error': return '失败'
    case 'cancelled': return '已取消'
    default: return s
  }
}
function statusType(s: string): 'default' | 'info' | 'success' | 'warning' | 'error' {
  switch (s) {
    case 'streaming': return 'info'
    case 'complete': return 'success'
    case 'error': return 'error'
    case 'cancelled': return 'warning'
    default: return 'default'
  }
}

// isMobile is a one-shot viewport check. The grid template
// uses `repeat(auto-fit, minmax(...))` so a manual width
// calculation isn't needed — but we still want to fall
// back to a single column below 720px so the panes
// remain readable.
const isMobile = computed(() => typeof window !== 'undefined' && window.innerWidth < 720)

// gridTemplate renders `repeat(N, 1fr)` on desktop and
// `1fr` (single column) on mobile. CSS variable so the
// template can use it inline.
const gridTemplate = computed(() => {
  if (!race.value) return '1fr'
  const n = race.value.panes.length
  return isMobile.value ? '1fr' : `repeat(${n}, 1fr)`
})

async function onPickWinner(paneId: string) {
  try {
    await pickWinner(paneId)
  } catch (e: any) {
    // Best-effort surface; pickWinner already logs.
    console.error('[race] pick failed:', e)
  }
}

function onCancel() {
  cancelRace()
}
</script>

<template>
  <div v-if="race" class="race-view" :style="{ '--cols': gridTemplate }">
    <div class="race-toolbar">
      <span class="race-title">多模型对比 ({{ race.panes.length }})</span>
      <NButton size="tiny" quaternary @click="onCancel" title="取消所有 pane">
        <template #icon>
          <XIcon :size="14" />
        </template>
        取消
      </NButton>
    </div>

    <div class="race-grid">
      <div
        v-for="(pane, i) in race.panes"
        :key="i"
        class="pane"
      >
        <header class="pane-header">
          <span class="pane-model">
            {{ pane.provider }} / {{ pane.model }}
          </span>
          <NTag size="small" :type="statusType(pane.status)" :bordered="false">
            {{ statusLabel(pane.status) }}
          </NTag>
        </header>

        <div class="pane-messages">
          <NEmpty
            v-if="!state.sessionMessages[pane.paneId] || state.sessionMessages[pane.paneId].length === 0"
            description="等待响应…"
            class="pane-empty"
          />
          <template v-else>
            <MessageBubble
              v-for="(m, j) in state.sessionMessages[pane.paneId]"
              :key="`${pane.paneId}-${j}`"
              :message="m"
              :streaming="pane.status === 'streaming' && j === state.sessionMessages[pane.paneId].length - 1 && m.role === 'assistant'"
            />
          </template>
          <div v-if="pane.status === 'error' && pane.error" class="pane-error">
            {{ pane.error }}
          </div>
        </div>

        <footer v-if="race.status === 'complete'" class="pane-footer">
          <NButton
            type="primary"
            size="small"
            @click="onPickWinner(pane.paneId)"
            :title="`将 ${pane.provider} / ${pane.model} 提升为新主线`"
          >
            <template #icon>
              <TrophyIcon :size="14" />
            </template>
            🏆 选这个
          </NButton>
        </footer>
      </div>
    </div>
  </div>
</template>

<style scoped>
.race-view {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: var(--bg-1, transparent);
}

.race-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 16px;
  border-bottom: 1px solid var(--border);
  background: var(--surface-1, transparent);
}
.race-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-1);
}

.race-grid {
  display: grid;
  grid-template-columns: var(--cols);
  flex: 1;
  min-height: 0;
  gap: 0;
}

.pane {
  display: flex;
  flex-direction: column;
  border-right: 1px solid var(--border);
  min-width: 0;
  min-height: 0;
}
.pane:last-child {
  border-right: none;
}

.pane-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--border);
  background: var(--surface-1, transparent);
}
.pane-model {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-1);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pane-messages {
  flex: 1;
  overflow-y: auto;
  padding: 12px 12px;
  min-height: 0;
}
.pane-empty {
  padding: 32px 0;
  opacity: 0.6;
}
.pane-error {
  padding: 12px;
  margin: 8px 0;
  font-size: 12px;
  color: var(--error-text, #b91c1c);
  background: var(--error-bg, #fef2f2);
  border: 1px solid var(--error-border, #fecaca);
  border-radius: 6px;
}

.pane-footer {
  padding: 12px;
  border-top: 1px solid var(--border);
  display: flex;
  justify-content: center;
  background: var(--surface-1, transparent);
}
</style>
