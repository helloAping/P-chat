<script setup lang="ts">
/**
 * ModelPicker — a command-palette-style popover for choosing the
 * active model + provider. Tighter and more discoverable than the
 * NSelect it replaces, with the affordances chat users expect:
 *
 *   - Top search field that filters across all models in all
 *     providers.
 *   - List grouped by provider, with a colored dot for the
 *     provider protocol (Anthropic = orange, OpenAI = green,
 *     custom = ai purple) so a glance tells which protocol
 *     the request will use.
 *   - Each row shows: model name, an ⭐ marker for the
 *     default model, context-window size, and capability
 *     indicators (vision / tools).
 *   - Full keyboard navigation: ↑↓ to move, Enter to pick,
 *     Esc to dismiss.
 *
 * Usage:
 *   <ModelPicker
 *     v-model:show="showPicker"
 *     v-model:provider="currentProvider"
 *     v-model:model="currentModel"
 *     :providers="state.providers"
 *   />
 */
import { computed, nextTick, ref, watch } from 'vue'
import { NInput, NPopover, NScrollbar, NTag, NTooltip } from 'naive-ui'
import type { ProviderInfo } from '../api/client'
import { Search as SearchIcon, Eye, Star, Check, Sparkles, X as XIcon } from './icons'

const props = defineProps<{
  show: boolean
  /** Encoded as "<provider>::<model>". The picker emits the
   * same encoding. The two-way binding uses the encoded
   * string to keep a single value flowing through the
   * popover, which simplifies the selection model. */
  provider?: string
  model?: string
  providers: ProviderInfo[]
}>()

const emit = defineEmits<{
  (e: 'update:show', v: boolean): void
  (e: 'update:provider', v: string): void
  (e: 'update:model', v: string): void
  (e: 'select', v: { provider: string; model: string }): void
}>()

// --- Search / filter -------------------------------------------------
const searchQuery = ref('')
const selectedIndex = ref(0)
const listEl = ref<HTMLElement | null>(null)
const searchInputEl = ref<HTMLInputElement | null>(null)

// providerColor: maps the protocol to a stable hue. Custom
// providers (the "cs" proxy) inherit the AI purple so the
// app's brand identity carries through. See TopBar.vue
// for the same logic — the two stay in sync.
function providerColor(p: ProviderInfo): string {
  const proto = (p as any).protocol as string | undefined
  if (proto === 'anthropic') return 'var(--provider-anthropic)'
  if (proto === 'openai') return 'var(--provider-openai)'
  if (p.name === 'cs' || proto === 'openai-compatible') return 'var(--provider-openai-compatible)'
  return 'var(--provider-default)'
}

// Build a flat list of {provider, model, node} entries that we
// both render and keyboard-navigate. Filtering happens here
// once, then the template just maps over the result.
interface ModelEntry {
  provider: ProviderInfo
  model: { name: string; default?: boolean; max_tokens_context?: number; capabilities?: any }
  isDefault: boolean
}

const allEntries = computed<ModelEntry[]>(() => {
  const out: ModelEntry[] = []
  for (const p of props.providers) {
    for (const m of (p.models || [])) {
      out.push({
        provider: p,
        model: m as any,
        isDefault: !!(m as any).default,
      })
    }
  }
  return out
})

// groupedByProvider — the visual structure (header + items per
// provider) used by the template. We pre-flatten after
// filtering so the keyboard nav can use a flat index.
interface Group {
  provider: ProviderInfo
  entries: ModelEntry[]
}
const filteredGroups = computed<Group[]>(() => {
  const q = searchQuery.value.trim().toLowerCase()
  const map = new Map<string, Group>()
  for (const e of allEntries.value) {
    const matches = !q
      || e.provider.name.toLowerCase().includes(q)
      || e.model.name.toLowerCase().includes(q)
      || (e.model as any).display_name?.toLowerCase().includes(q)
    if (!matches) continue
    let g = map.get(e.provider.name)
    if (!g) { g = { provider: e.provider, entries: [] }; map.set(e.provider.name, g) }
    g.entries.push(e)
  }
  return Array.from(map.values())
})

// Flat array used for keyboard nav (so the index in
// `selectedIndex` maps to a single item, not a group+item).
const flatFiltered = computed<ModelEntry[]>(() =>
  filteredGroups.value.flatMap(g => g.entries),
)

// Reset keyboard selection when the search query or visibility
// changes — the user expects the first row highlighted by
// default, not whatever was highlighted when the query
// last changed.
watch([searchQuery, () => props.show], () => {
  selectedIndex.value = 0
})

// When the popover opens, focus the search input so the user
// can start typing immediately. We also reset the query so
// the full list shows by default.
watch(() => props.show, (v) => {
  if (v) {
    searchQuery.value = ''
    selectedIndex.value = 0
    nextTick(() => {
      searchInputEl.value?.focus()
    })
  }
})

// --- Selection --------------------------------------------------------
// On select, we emit two updates (provider + model separately
// so the parent's v-model:provider / v-model:model can react
// independently) and a combined `select` event for handlers
// that just want the pair.
function pickEntry(e: ModelEntry) {
  emit('update:provider', e.provider.name)
  emit('update:model', e.model.name)
  emit('select', { provider: e.provider.name, model: e.model.name })
  emit('update:show', false)
}

// --- Keyboard navigation ----------------------------------------------
function onKeyDown(ev: KeyboardEvent) {
  const total = flatFiltered.value.length
  if (ev.key === 'ArrowDown') {
    ev.preventDefault()
    if (total > 0) selectedIndex.value = (selectedIndex.value + 1) % total
  } else if (ev.key === 'ArrowUp') {
    ev.preventDefault()
    if (total > 0) selectedIndex.value = (selectedIndex.value - 1 + total) % total
  } else if (ev.key === 'Enter') {
    ev.preventDefault()
    const e = flatFiltered.value[selectedIndex.value]
    if (e) pickEntry(e)
  } else if (ev.key === 'Escape') {
    ev.preventDefault()
    emit('update:show', false)
  }
}

// Helper: format a context-window size for the meta line.
// 8192 → "8K", 32768 → "32K", 200000 → "200K", 1000000 → "1M".
function fmtContext(n?: number): string | null {
  if (!n) return null
  if (n >= 1_000_000) return `${Math.round(n / 100_000) / 10}M`
  if (n >= 1_000) return `${Math.round(n / 1_000)}K`
  return `${n}`
}

// Track the currently-selected entry so the popover can show
// a check next to it.
const currentKey = computed(() => `${props.provider}::${props.model}`)
function isCurrent(e: ModelEntry) {
  return `${e.provider.name}::${e.model.name}` === currentKey.value
}
</script>

<template>
  <NPopover
    :show="show"
    trigger="manual"
    placement="top-start"
    :raw="false"
    :show-arrow="false"
    style="padding: 0; background: transparent; box-shadow: none;"
    @clickoutside="emit('update:show', false)"
  >
    <template #trigger>
      <slot name="trigger" />
    </template>

    <div class="model-picker" @keydown="onKeyDown">
      <!-- Search field at the top, focused on open. -->
      <div class="picker-search">
        <SearchIcon :size="14" class="picker-search-icon" />
        <input
          ref="searchInputEl"
          v-model="searchQuery"
          class="picker-search-input"
          type="text"
          placeholder="搜索模型或提供商..."
          @keydown.stop="onKeyDown"
        />
        <button
          v-if="searchQuery"
          type="button"
          class="picker-search-clear"
          aria-label="清空搜索"
          @click="searchQuery = ''"
        >
          <XIcon :size="12" />
        </button>
      </div>

      <!-- Model list, grouped by provider. -->
      <NScrollbar style="max-height: 320px;">
        <div ref="listEl" class="picker-list">
          <div v-if="filteredGroups.length === 0" class="picker-empty">
            <Sparkles :size="20" class="picker-empty-icon" />
            <div>没有匹配的模型</div>
          </div>

          <div v-for="g in filteredGroups" :key="g.provider.name" class="picker-group">
            <div class="picker-group-header">
              <span class="picker-group-dot" :style="{ background: providerColor(g.provider) }" />
              <span class="picker-group-name">{{ g.provider.name }}</span>
              <span class="picker-group-count">{{ g.entries.length }}</span>
            </div>
            <button
              v-for="e in g.entries"
              :key="`${g.provider.name}::${e.model.name}`"
              type="button"
              class="picker-item"
              :class="{
                'picker-item--active': isCurrent(e),
                'picker-item--focused': flatFiltered[selectedIndex] === e,
              }"
              @click="pickEntry(e)"
              @mouseenter="selectedIndex = flatFiltered.indexOf(e)"
            >
              <div class="picker-item-main">
                <span class="picker-item-name">
                  {{ (e.model as any).display_name || e.model.name }}
                  <Star v-if="e.isDefault" :size="11" class="picker-item-default" />
                </span>
                <span v-if="e.model.name !== (e.model as any).display_name" class="picker-item-id">
                  {{ e.model.name }}
                </span>
              </div>
              <div class="picker-item-meta">
                <NTag v-if="e.model.capabilities?.supports_vision" size="tiny" :bordered="false" class="picker-cap-tag picker-cap-vision">
                  <template #icon><Eye :size="10" /></template>
                  视觉
                </NTag>
                <span v-if="fmtContext(e.model.max_tokens_context)" class="picker-context">
                  {{ fmtContext(e.model.max_tokens_context) }}
                </span>
                <Check v-if="isCurrent(e)" :size="14" class="picker-item-check" />
              </div>
            </button>
          </div>
        </div>
      </NScrollbar>

      <!-- Footer hint: keyboard shortcuts + provider count. -->
      <div class="picker-footer">
        <span><kbd>↑</kbd><kbd>↓</kbd> 切换</span>
        <span><kbd>Enter</kbd> 选中</span>
        <span><kbd>Esc</kbd> 关闭</span>
        <span class="picker-footer-spacer" />
        <span class="picker-footer-count">{{ allEntries.length }} 个模型</span>
      </div>
    </div>
  </NPopover>
</template>

<style scoped>
.model-picker {
  width: 380px;
  background: var(--surface-1);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
  overflow: hidden;
  display: flex;
  flex-direction: column;
  /* Outline so the popover is focusable as a single
   * keyboard-nav target — the underlying NPopover manages
   * focus inside the search input. */
  outline: none;
}

/* --- Search field ----------------------------------------------------- */
.picker-search {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 10px;
  border-bottom: 1px solid var(--border-subtle);
  background: var(--surface-1);
}
.picker-search-icon {
  color: var(--text-tertiary);
  flex-shrink: 0;
}
.picker-search-input {
  flex: 1;
  border: none;
  outline: none;
  background: transparent;
  color: var(--text-primary);
  font-family: var(--font-sans);
  font-size: 13px;
  padding: 2px 0;
  min-width: 0;
}
.picker-search-input::placeholder {
  color: var(--text-quaternary);
}
.picker-search-clear {
  background: none;
  border: none;
  color: var(--text-tertiary);
  cursor: pointer;
  padding: 2px;
  border-radius: 3px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.picker-search-clear:hover { color: var(--text-primary); background: var(--surface-3); }

/* --- List ------------------------------------------------------------- */
.picker-list {
  padding: 4px;
}
.picker-group { margin-bottom: 4px; }
.picker-group-header {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 8px 4px;
  font-size: 10.5px;
  font-weight: 600;
  color: var(--text-tertiary);
  text-transform: uppercase;
  letter-spacing: 0.06em;
  user-select: none;
}
.picker-group-dot {
  width: 6px; height: 6px;
  border-radius: 50%;
  flex-shrink: 0;
}
.picker-group-name { flex: 1; }
.picker-group-count {
  font-size: 10px;
  color: var(--text-quaternary);
  font-weight: 500;
  letter-spacing: 0;
  text-transform: none;
}

.picker-item {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  background: transparent;
  border: none;
  border-radius: var(--radius-sm);
  color: var(--text-primary);
  cursor: pointer;
  text-align: left;
  font-family: var(--font-sans);
  transition: background var(--dur-fast) var(--ease-out);
}
.picker-item--focused { background: var(--surface-3); }
.picker-item--active { background: var(--brand-50); color: var(--brand-600); }
.picker-item--active.picker-item--focused { background: var(--brand-100); }

.picker-item-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.picker-item-name {
  font-size: 13px;
  font-weight: 500;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.picker-item-default {
  color: var(--brand-500);
  flex-shrink: 0;
}
.picker-item-id {
  font-size: 11px;
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.picker-item-meta {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}
.picker-context {
  font-size: 10.5px;
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
}
.picker-cap-tag {
  font-size: 10px !important;
  height: 18px !important;
  padding: 0 4px !important;
  background: var(--surface-2) !important;
  color: var(--text-tertiary) !important;
}
.picker-cap-vision {
  background: var(--ai-50) !important;
  color: var(--ai-600) !important;
}
.picker-item-check {
  color: var(--brand-500);
  flex-shrink: 0;
}

/* --- Empty state ----------------------------------------------------- */
.picker-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  padding: 32px 16px;
  color: var(--text-tertiary);
  font-size: 12.5px;
}
.picker-empty-icon { color: var(--text-quaternary); }

/* --- Footer ----------------------------------------------------------- */
.picker-footer {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 6px 10px;
  border-top: 1px solid var(--border-subtle);
  background: var(--surface-1);
  font-size: 10.5px;
  color: var(--text-tertiary);
  user-select: none;
}
.picker-footer kbd {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 16px;
  height: 16px;
  padding: 0 4px;
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: 3px;
  font-family: var(--font-mono);
  font-size: 9.5px;
  color: var(--text-secondary);
  margin-right: 2px;
}
.picker-footer-spacer { flex: 1; }
.picker-footer-count {
  color: var(--text-quaternary);
  font-variant-numeric: tabular-nums;
}
</style>

<style>
/* NPopover wraps its content in an internal card which
 * adds default padding and an arrow. We strip both via
 * the `style` prop on the NPopover, but the wrapping
 * element still gets a default width. The rule below
 * sets the popover's content wrapper to display: contents
 * so the styled .model-picker is the only visible child
 * and our width / shadow actually apply. */
.n-popover:not(.n-tooltip) .n-popover__content {
  background: transparent !important;
  padding: 0 !important;
}
</style>
