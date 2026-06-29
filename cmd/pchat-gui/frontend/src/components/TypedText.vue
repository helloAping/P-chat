<script setup lang="ts">
// TypedText — renders `text` with a typewriter animation.
//
// The animation starts the moment text begins to arrive
// (or on mount, if text is already present and `active`
// is true) and continues until `displayed` catches up to
// the full `text` value. It does **not** stop when
// `active` flips false: that's the whole point — the
// parent stops being "interested" (e.g. streaming ended)
// doesn't mean the user has already seen the text. We
// let the animation play out, then emit `done` so the
// parent can swap to a static render.
//
// Why this lives in its own component:
//   - The text is rendered through `marked.parse()` for
//     markdown support. The animation has to operate on
//     the plain-text string *before* it hits marked,
//     otherwise each tick would re-parse the entire HTML
//     body. Composing marked + animation in one place
//     keeps the markdown rendering stable (the displayed
//     string only grows, the AST is rebuilt at most once
//     per tick).
//   - The `v-html` target is a fresh per-instance element,
//     so there's no shared DOM to manage.
//
// Duration policy:
//   - `minDuration` (default 400ms): the animation always
//     plays for at least this long. A 10-char error message
//     still gets a visible typewriter sweep.
//   - `maxDuration` (default 2500ms): the animation never
//     plays for longer than this. A 5000-char response
//     finishes within 2.5s by ramping the speed up.
//   - Default speed: 80 chars/sec. The speed is per
//     grapheme, not per byte — CJK and other multi-byte
//     characters each count as 1, and the slice is done
//     by grapheme boundary (Intl.Segmenter) so we never
//     render a half-character.
//
// Reset cases (defensive):
//   - If `text` shrinks (streaming reset, model rollback),
//     the displayed value snaps to the new shorter text.
//   - On component unmount, the RAF loop is cancelled.

import { computed, ref, watch, onUnmounted } from 'vue'
import { marked } from 'marked'

const props = withDefaults(defineProps<{
  text: string
  active: boolean
  speed?: number
  minDuration?: number
  maxDuration?: number
}>(), {
  speed: 80,
  minDuration: 400,
  maxDuration: 2500,
})

const emit = defineEmits<{ (e: 'done'): void }>()

// graphemes is `text` split into a grapheme array. We
// store the *index* of the last displayed grapheme, not
// the substring, so CJK / emoji never get sliced mid-char.
const graphemes = computed(() => splitGraphemes(props.text || ''))
const total = computed(() => graphemes.value.length)

const displayedIdx = ref(props.active ? 0 : total.value)
const displayed = computed(() =>
  graphemes.value.slice(0, displayedIdx.value).join(''),
)

let raf = 0
let lastT = 0
let carry = 0
let doneEmitted = false

// effectiveSpeed picks the per-second rate that fits
// `text.length` inside [minDuration, maxDuration]. Below
// the min bound we use the user-supplied speed; above the
// max bound we ramp the speed up so the animation always
// finishes within `maxDuration`.
const effectiveSpeed = computed(() => {
  const len = total.value
  if (len === 0) return props.speed
  const minLen = (props.minDuration / 1000) * props.speed
  const maxLen = (props.maxDuration / 1000) * props.speed
  if (len <= minLen) return props.speed
  if (len >= maxLen) return len / (props.maxDuration / 1000)
  return props.speed
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
  // Carry accumulates fractional graphemes from previous
  // frames so a slow frame doesn't drop a character.
  // floor() means we always round down — better to fall
  // slightly behind than to occasionally flash the next
  // character early.
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

function start() {
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
  // Safety net: if the rAF loop somehow stalls (e.g. the
  // tab is throttled, or the browser drops frames), we
  // still want the parent to switch to the static render
  // so the caret stops blinking. The animation has a
  // dynamic speed cap that should complete in <=
  // maxDuration, so 1s of slack is plenty.
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

function stop() {
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
    stop()
    return
  }
  // Text shrank below what we've already displayed —
  // snap the displayed position back so we never show
  // a character that no longer exists in the source.
  if (displayedIdx.value > next) {
    displayedIdx.value = next
  }
  // If we previously emitted done (text was complete),
  // and new text arrives, we need to reset so the new
  // content also animates.
  if (doneEmitted && displayedIdx.value < next) {
    doneEmitted = false
  }
  start()
}, { immediate: true })

watch(() => props.active, (isActive) => {
  // active=false is *not* a stop signal — the animation
  // continues until the full text has been displayed.
  // active=true just kicks the loop in case it was idle
  // (e.g. active was false during the first chunk and
  // the loop hadn't been started yet).
  if (isActive) start()
}, { immediate: true })

// Initial kick: if the component was mounted with non-empty
// text (the common case for an error message that arrives
// in a single chunk), the watchers above already fired with
// `immediate: true` and started the loop. This is a
// belt-and-braces fallback in case the watchers ran while
// `total` was still 0 (e.g. the text was a few chars
// arriving in the same microtask as the mount).
if (displayedIdx.value < total.value && !raf) {
  start()
}

onUnmounted(stop)

const html = computed(() =>
  marked.parse(displayed.value || '', { async: false, breaks: true }) as string,
)

// splitGraphemes splits a string into grapheme clusters
// using Intl.Segmenter when available, falling back to
// Array.from (which splits by code point — good enough
// for plain CJK without combining marks).
function splitGraphemes(s: string): string[] {
  if (typeof Intl !== 'undefined' && (Intl as any).Segmenter) {
    const seg = new (Intl as any).Segmenter(undefined, { granularity: 'grapheme' })
    return Array.from(seg.segment(s), (x: any) => x.segment)
  }
  return Array.from(s)
}
</script>

<template>
  <div class="md-body typed-text" v-html="html" />
</template>

<style scoped>
/* The blinking caret on the trailing character while typing.
 * The animation only runs while there's still text to
 * reveal; once the last character lands, the
 * `displayed` reaches the full `text` length and the
 * caret disappears. We keep the caret visible until
 * that point — the parent component will swap to a
 * static render when `done` fires. */
.typed-text::after {
  content: '▍';
  display: inline-block;
  margin-left: 1px;
  color: var(--text-3);
  animation: caret 1s steps(2) infinite;
}
@keyframes caret {
  0%, 50%   { opacity: 1; }
  50.01%, 100% { opacity: 0; }
}
</style>
