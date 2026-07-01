<script setup lang="ts">
// Three bouncing dots — the iMessage-style loading
// indicator. Used inside the assistant message bubble
// while the first content / thinking / tool event is
// still in flight. Pure CSS animation; no JS state.
</script>

<template>
  <span class="loading-dots" aria-label="正在思考">
    <span class="dot" />
    <span class="dot" />
    <span class="dot" />
  </span>
</template>

<style scoped>
.loading-dots {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 1em;
  vertical-align: middle;
}
.dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--text-3);
  animation: loading-bounce 1.2s infinite ease-in-out;
}
.dot:nth-child(1) { animation-delay: 0s; }
.dot:nth-child(2) { animation-delay: 0.15s; }
.dot:nth-child(3) { animation-delay: 0.3s; }
@keyframes loading-bounce {
  0%, 80%, 100% { transform: translateY(0); opacity: 0.5; }
  40%           { transform: translateY(-4px); opacity: 1; }
}
</style>
