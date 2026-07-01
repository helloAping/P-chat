<script setup lang="ts">
// TypedText — renders text as raw plain-text via direct DOM
// `textContent` assignment during streaming. No `marked.parse()`
// is called while the stream is active — that would re-parse
// the full accumulated text on every delta, causing O(n²) CPU
// cost and making the DOM appear to "freeze" during fast
// streaming.
//
// Instead, we watch `props.text` and write it directly to a
// `<pre>` element's `textContent`. The browser handles text
// layout natively (no virtual DOM diff, no innerHTML rebuild).
//
// When streaming ends, the parent (MessageBubble) unmounts us
// and switches to the static markdown render (`marked.parse()`
// once, after the full text is known). The `active` prop is
// kept for API compatibility but is no longer used internally
// — the parent controls lifecycle.

import { ref, watch } from 'vue'

const props = withDefaults(defineProps<{
  text: string
  active?: boolean
}>(), {
  active: true,
})

const el = ref<HTMLElement | null>(null)

// Direct textContent write — no markdown, no v-html, no
// virtual DOM diffing. On each SSE delta this is O(1) work
// for the browser (append N chars to the existing text
// run). The trailing `▍` caret is pure CSS on `::after`.
const update = (t: string) => {
  if (el.value) el.value.textContent = t
}

watch(() => props.text, update, { immediate: true })
</script>

<template>
  <pre ref="el" class="typed-text">{{ text }}</pre>
</template>

<style scoped>
.typed-text {
  margin: 0;
  padding: 0;
  background: transparent;
  border: none;
  font-family: inherit;
  font-size: inherit;
  line-height: inherit;
  color: inherit;
  white-space: pre-wrap;
  word-break: break-word;
  overflow-wrap: break-word;
}
/* The blinking caret at the trailing edge of the text.
 * Pure CSS — part of every TypedText while the bubble
 * is "live". The parent unmounts the component when
 * streaming ends, at which point the caret disappears
 * and the static markdown render takes over. */
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
