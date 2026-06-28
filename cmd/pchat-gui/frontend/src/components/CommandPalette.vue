<script setup lang="ts">
import { computed } from 'vue'

export interface CmdSpec {
  name: string
  description: string
  args?: string
  group: string
  web_safe: boolean
}

const props = defineProps<{
  commands: CmdSpec[]
  filter: string
  selectedIndex: number
}>()

const emit = defineEmits<{
  select: [cmd: CmdSpec]
}>()

const GROUP_LABELS: Record<string, string> = {
  session: '会话',
  config: '配置',
  info: '信息',
  danger: '危险',
}

const filtered = computed(() => {
  if (!props.filter) return props.commands
  const q = props.filter.toLowerCase()
  return props.commands.filter(c =>
    c.name.toLowerCase().includes(q) ||
    c.description.toLowerCase().includes(q),
  )
})
</script>

<template>
  <div v-if="filtered.length" class="cmd-palette">
    <div
      v-for="(c, i) in filtered"
      :key="c.name"
      class="cmd-item"
      :class="{ active: i === selectedIndex }"
      @mousedown.prevent="emit('select', c)"
    >
      <span class="cmd-name">/{{ c.name }}<span v-if="c.args" class="cmd-args"> {{ c.args }}</span></span>
      <span class="cmd-desc">{{ c.description }}</span>
      <span class="cmd-group" v-if="GROUP_LABELS[c.group]">{{ GROUP_LABELS[c.group] }}</span>
    </div>
  </div>
  <div v-else-if="filter && props.commands.length" class="cmd-palette">
    <div class="cmd-item no-match">没有匹配的命令</div>
  </div>
</template>

<style scoped>
.cmd-palette {
  position: absolute;
  bottom: 100%;
  left: 0;
  right: 0;
  margin-bottom: 4px;
  background: var(--bg-1);
  border: 1px solid var(--border);
  border-radius: 8px;
  box-shadow: 0 -4px 16px rgba(0,0,0,0.15);
  max-height: 240px;
  overflow-y: auto;
  z-index: 100;
}
.cmd-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 7px 12px;
  cursor: pointer;
  font-size: 13px;
  border-bottom: 1px solid var(--bg-3);
}
.cmd-item:last-child { border-bottom: none; }
.cmd-item:hover, .cmd-item.active {
  background: var(--accent);
  color: var(--on-accent);
}
.cmd-item.no-match {
  color: var(--text-4);
  cursor: default;
}
.cmd-item.no-match:hover, .cmd-item.no-match.active {
  background: transparent;
  color: var(--text-4);
}
.cmd-name {
  font-family: monospace;
  font-weight: 600;
  white-space: nowrap;
  flex-shrink: 0;
}
.cmd-args {
  font-weight: 400;
  opacity: 0.7;
}
.cmd-desc {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-2);
}
.cmd-item:hover .cmd-desc,
.cmd-item.active .cmd-desc {
  color: inherit;
}
.cmd-group {
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 4px;
  background: var(--bg-3);
  color: var(--text-4);
  flex-shrink: 0;
}
</style>
