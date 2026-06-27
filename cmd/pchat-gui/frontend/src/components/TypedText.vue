<script setup lang="ts">
// TypedText — renders `text` with a typewriter animation while
// `active` is true. When active flips false (streaming ended,
// user closed the session, or the message was loaded from
// history), the displayed text snaps to the full value and
// no further animation runs.
//
// Why this lives in its own component instead of a directive
// or composable:
//   - The text is rendered through `marked.parse()` for
//     markdown support. The animation has to operate on the
//     plain-text string *before* it hits marked, otherwise
//     each tick would re-parse and re-allocate the entire
//     HTML body. Composing marked + animation in one place
//     keeps the markdown rendering stable (the displayed
//     string only grows, the AST is rebuilt at most once
//     per tick).
//   - The `v-html` target is a fresh per-instance element,
//     so there's no shared DOM to manage.
//
// Animation policy:
//   - Default speed: 80 chars/sec. Slower than typical
//     reading speed so the user can follow along; faster
//     than the cursor blink so it doesn't feel sluggish.
//     The speed is per-character, not per-byte — CJK and
//     other multi-byte characters each count as 1.
//   - The displayed value never exceeds the full text.
//     If chunks arrive faster than the animation can show
//     them, the trailing tail is held back until the
//     animation catches up.
//   - When `active` flips false, displayed snaps to full
//     immediately (no awkward mid-character stop).
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
}>(), {
  speed: 80,
})

const displayed = ref(props.active ? '' : props.text)

let raf = 0
let lastT = 0
let carry = 0

function step(t: number) {
  if (!props.active) {
    displayed.value = props.text
    raf = 0
    return
  }
  if (displayed.value.length >= props.text.length) {
    raf = 0
    return
  }
  const dt = lastT ? t - lastT : 0
  lastT = t
  // Carry accumulates fractional chars from previous frames
  // so a slow frame doesn't drop a character. floor() means
  // we always round down — better to fall slightly behind
  // than to occasionally flash the next character early.
  carry += (dt / 1000) * props.speed
  const n = Math.floor(carry)
  if (n > 0) {
    displayed.value = props.text.slice(0, displayed.value.length + n)
    carry -= n
  }
  raf = requestAnimationFrame(step)
}

function start() {
  if (raf) return
  lastT = 0
  carry = 0
  raf = requestAnimationFrame(step)
}

function stop() {
  if (raf) {
    cancelAnimationFrame(raf)
    raf = 0
  }
}

watch(() => props.text, (next) => {
  if (!props.active) {
    displayed.value = next
    stop()
    return
  }
  if (displayed.value.length > next.length) {
    // Text shrank (defensive: streaming reset). Snap.
    displayed.value = next
  }
  start()
})

watch(() => props.active, (isActive) => {
  if (!isActive) {
    displayed.value = props.text
    stop()
  } else {
    start()
  }
})

onUnmounted(stop)

const html = computed(() =>
  marked.parse(displayed.value || '', { async: false, breaks: true }) as string,
)
</script>

<template>
  <div class="md-body typed-text" v-html="html" />
</template>

<style scoped>
/* The blinking caret on the trailing character while typing.
 * The animation only runs while the bubble is still
 * streaming; once the last character lands, the
 * `active` prop flips and the caret disappears (no need
 * for an explicit stopped class — the parent
 * MessageBubble stops passing the active flag, and the
 * `<TypedText>` instance is short-lived after the bubble
 * stops streaming anyway). */
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
