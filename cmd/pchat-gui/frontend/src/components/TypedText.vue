<script setup lang="ts">
// TypedText — renders `text` directly with a blinking
// caret at the trailing edge while the message is
// being received.
//
// We intentionally do **not** run an artificial
// typewriter animation. The "typewriter" feel comes
// from the natural SSE stream: the LLM emits content
// chunk by chunk over the network, `props.text` grows
// in real time, and the DOM re-renders the new text on
// each chunk. That's the ChatGPT-style streaming
// experience the user expects — text appears as fast
// as the model produces it, with no artificial delay.
//
// The blinking caret (`▍`) is a pure CSS animation on
// a `::after` pseudo-element. It is always present
// while the component is mounted; the parent decides
// when to unmount it (i.e. switch to the static
// markdown render) by passing a different `active`
// value, or by removing the component entirely. We
// keep the `active` prop for API stability with the
// parent, but the component no longer gates
// rendering on it.

import { computed } from 'vue'
import { marked } from 'marked'

const props = withDefaults(defineProps<{
  text: string
  active?: boolean
}>(), {
  active: true,
})

const html = computed(() =>
  marked.parse(props.text || '', { async: false, breaks: true }) as string,
)
</script>

<template>
  <div class="md-body typed-text" v-html="html" />
</template>

<style scoped>
/* The blinking caret at the trailing edge of the text.
 * Pure CSS — the caret is part of every TypedText
 * instance while the bubble is "live" (i.e. the
 * parent is still receiving SSE chunks for it). The
 * parent unmounts the component when the stream is
 * done, at which point the caret disappears with
 * the rest of the element. */
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
