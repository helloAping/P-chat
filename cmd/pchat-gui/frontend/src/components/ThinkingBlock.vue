<script setup lang="ts">
// ThinkingBlock renders a `<details>` block for the
// thinking trace of an assistant turn. While the part is
// streaming, the body fills up character-by-character
// (typewriter). The animation continues until the full
// text has been revealed; we do NOT snap to the full
// text when `part.streaming` flips false — the parent
// will swap to a static render when the `done` event
// fires.
//
// Why the animation is independent of `streaming`:
// the LLM may finish emitting in 200ms; the user needs
// to actually see the text appear, not jump from
// "loading dots" to "fully-rendered text".

import { computed, ref, watch, onUnmounted } from 'vue'
import type { ThinkingPart } from '../api/client'

const props = withDefaults(defineProps<{
  part: ThinkingPart
  defaultOpen?: boolean
  minDuration?: number
  maxDuration?: number
}>(), {
  minDuration: 400,
  maxDuration: 2500,
})

const emit = defineEmits<{ (e: 'done'): void }>()

const open = ref(!!props.defaultOpen)

// graphemes split by code point / grapheme cluster, so
// CJK characters never get sliced mid-character.
const graphemes = computed(() => splitGraphemes(props.part.text || ''))
const total = computed(() => graphemes.value.length)

const displayedIdx = ref(props.part.streaming ? 0 : total.value)
const displayed = computed(() =>
  graphemes.value.slice(0, displayedIdx.value).join(''),
)

let raf = 0
let lastT = 0
let carry = 0
let doneEmitted = false

const effectiveSpeed = computed(() => {
  const len = total.value
  if (len === 0) return 120
  const baseSpeed = 120 // chars/sec for thinking (a bit faster than prose)
  const minLen = (props.minDuration / 1000) * baseSpeed
  const maxLen = (props.maxDuration / 1000) * baseSpeed
  if (len <= minLen) return baseSpeed
  if (len >= maxLen) return len / (props.maxDuration / 1000)
  return baseSpeed
})

function step(t: number) {
  if (displayedIdx.value >= total.value) {
    raf = 0
    if (!doneEmitted && total.value > 0) {
      doneEmitted = true
      emit('done')
    }
    return
  }
  const dt = lastT ? t - lastT : 0
  lastT = t
  carry += (dt / 1000) * effectiveSpeed.value
  const n = Math.floor(carry)
  if (n > 0) {
    displayedIdx.value = Math.min(total.value, displayedIdx.value + n)
    carry -= n
  }
  if (displayedIdx.value >= total.value) {
    raf = 0
    if (!doneEmitted) {
      doneEmitted = true
      emit('done')
    }
    return
  }
  raf = requestAnimationFrame(step)
}

let safetyTimer = 0

function startAnim() {
  if (raf) return
  if (displayedIdx.value >= total.value) {
    if (!doneEmitted && total.value > 0) {
      doneEmitted = true
      emit('done')
    }
    return
  }
  lastT = 0
  carry = 0
  raf = requestAnimationFrame(step)
  if (safetyTimer) clearTimeout(safetyTimer)
  safetyTimer = window.setTimeout(() => {
    if (doneEmitted) return
    if (displayedIdx.value < total.value) {
      displayedIdx.value = total.value
    }
    if (raf) { cancelAnimationFrame(raf); raf = 0 }
    if (!doneEmitted && total.value > 0) {
      doneEmitted = true
      emit('done')
    }
  }, props.maxDuration + 1000)
}

function stopAnim() {
  if (raf) {
    cancelAnimationFrame(raf)
    raf = 0
  }
  if (safetyTimer) {
    clearTimeout(safetyTimer)
    safetyTimer = 0
  }
}

watch(total, (next) => {
  if (next === 0) {
    displayedIdx.value = 0
    doneEmitted = false
    stopAnim()
    return
  }
  if (displayedIdx.value > next) {
    displayedIdx.value = next
  }
  if (doneEmitted && displayedIdx.value < next) {
    doneEmitted = false
  }
  // Always start the animation when the text grows —
  // don't gate it on `part.streaming`, because the
  // animation is independent of the streaming flag
  // (it plays out to the full text regardless).
  startAnim()
}, { immediate: true })

watch(() => props.part.streaming, (v) => {
  // streaming=false is NOT a stop signal — let the
  // animation play out to the end. streaming=true
  // just kicks the loop in.
  if (v) startAnim()
}, { immediate: true })

// Initial kick: same belt-and-braces as TypedText.
if (displayedIdx.value < total.value && !raf) {
  startAnim()
}

onUnmounted(stopAnim)

const html = computed(() => {
  const text = displayed.value
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
})

function splitGraphemes(s: string): string[] {
  if (typeof Intl !== 'undefined' && (Intl as any).Segmenter) {
    const seg = new (Intl as any).Segmenter(undefined, { granularity: 'grapheme' })
    return Array.from(seg.segment(s), (x: any) => x.segment)
  }
  return Array.from(s)
}
</script>

<template>
  <details
    class="thinking-block"
    :class="{ streaming: part.streaming }"
    :open="open"
  >
    <summary @click.prevent="open = !open">
      <span class="caret">{{ open ? '▾' : '▸' }}</span>
      <span class="label">
        <template v-if="part.streaming">思考中…</template>
        <template v-else>思考过程</template>
      </span>
      <span class="meta" v-if="part.text">{{ part.text.length }} 字</span>
    </summary>
    <pre class="thinking-body" v-if="open" v-html="html" />
  </details>
</template>

<style scoped>
.thinking-block {
  background: var(--bg-2);
  border: 1px solid var(--border-2);
  border-left: 3px solid var(--text-4);
  border-radius: 6px;
  margin: 6px 0;
  font-size: 12.5px;
  color: var(--text-2);
  overflow: hidden;
}
.thinking-block.streaming {
  /* The "currently streaming" visual: a soft shimmer
   * sweep across the left border. Doubles as a
   * loading indicator. */
  background-image: linear-gradient(
    90deg,
    var(--bg-2) 0%,
    var(--bg-3) 50%,
    var(--bg-2) 100%
  );
  background-size: 200% 100%;
  animation: shimmer 1.8s linear infinite;
  border-left-color: var(--accent);
}
@keyframes shimmer {
  0%   { background-position: 100% 0; }
  100% { background-position: -100% 0; }
}
.thinking-block > summary {
  list-style: none;
  cursor: pointer;
  padding: 5px 10px;
  display: flex;
  align-items: center;
  gap: 6px;
  user-select: none;
  font-weight: 500;
}
.thinking-block > summary::-webkit-details-marker { display: none; }
.caret { color: var(--text-3); font-size: 10px; }
.label { color: var(--text-2); }
.meta { margin-left: auto; color: var(--text-4); font-size: 11px; }
.thinking-body {
  margin: 0;
  padding: 8px 12px;
  border-top: 1px dashed var(--border-2);
  white-space: pre-wrap;
  word-wrap: break-word;
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.55;
  color: var(--text-3);
  max-height: 360px;
  overflow: auto;
}
</style>
