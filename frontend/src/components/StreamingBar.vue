<script setup lang="ts">
// StreamingBar — thin indeterminate progress bar pinned to the top of
// the chat page. Shows while the current conversation is mid-turn
// (`isStreaming || currentSessionWorking`), giving a glanceable "the
// agent is still working" signal that streamed content alone may not
// convey (long thinking passes / tool runs produce no visible text).
//
// The moving gradient is ONE oversized strip translated with `transform`
// over a repeating background pattern — compositor-only, zero per-frame
// repaint (see frontend-design.md §8.4). Show/hide is a cheap opacity
// transition; the element is fully removed from the DOM when idle.
import { computed } from 'vue'
import { isStreaming, currentSessionWorking } from '../stores/chat'

const active = computed(() => isStreaming.value || currentSessionWorking.value)
</script>

<template>
  <Transition name="stream-bar">
    <div v-if="active" class="stream-bar" role="status" aria-label="对话进行中">
      <div class="stream-bar-sweep" />
    </div>
  </Transition>
</template>

<style scoped>
.stream-bar {
  /* Hairline (intentionally non-4px): a progress bar, not a spacing
   * token. Sits above message content without eating layout space. */
  position: relative;
  height: 3px;
  flex-shrink: 0;
  overflow: hidden;
  z-index: 60;
}
/* Sweep strip: 200% wide, gradient pattern repeating every 100% of the
 * bar, animated with translateX(-50%) = exactly one bar width → a
 * seamless left-to-right color wave on the compositor. */
.stream-bar-sweep {
  position: absolute;
  top: 0;
  bottom: 0;
  left: 0;
  width: 200%;
  background-image: linear-gradient(
    90deg,
    transparent 0%,
    var(--brand-500) 25%,
    var(--ai-500) 50%,
    transparent 75%
  );
  background-size: 50% 100%;
  animation: stream-bar-sweep 1.4s linear infinite;
  will-change: transform;
}
@keyframes stream-bar-sweep {
  from { transform: translateX(0); }
  to   { transform: translateX(-50%); }
}
.stream-bar-enter-active,
.stream-bar-leave-active {
  transition: opacity var(--dur-base) var(--ease-out);
}
.stream-bar-enter-from,
.stream-bar-leave-to {
  opacity: 0;
}
</style>
