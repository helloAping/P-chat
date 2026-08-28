<script setup lang="ts">
import { onMounted, onUnmounted, watch } from 'vue'
import { state } from '../stores/chat'

function close() {
  state.lightbox = { show: false, src: '', alt: '', kind: 'image' }
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && state.lightbox.show) close()
}

onMounted(() => window.addEventListener('keydown', onKey))
onUnmounted(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <Transition name="fade">
    <div v-if="state.lightbox.show" class="lightbox" @click="close">
      <button class="close-btn" @click.stop="close" title="关闭 (Esc)">×</button>
      <img
        v-if="state.lightbox.kind === 'image'"
        :src="state.lightbox.src"
        :alt="state.lightbox.alt"
        class="lightbox-media"
        @click.stop
      />
      <video
        v-else-if="state.lightbox.kind === 'video'"
        :src="state.lightbox.src"
        class="lightbox-media"
        controls
        autoplay
        @click.stop
      />
    </div>
  </Transition>
</template>

<style scoped>
.lightbox {
  position: fixed; inset: 0;
  background: rgba(0, 0, 0, 0.88);
  display: flex; align-items: center; justify-content: center;
  z-index: 1000;
  cursor: zoom-out;
}
.lightbox-media {
  max-width: 95vw;
  max-height: 95vh;
  object-fit: contain;
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
  cursor: default;
  background: #000;
}
.close-btn {
  position: absolute; top: var(--space-4); right: var(--space-4);
  width: 40px; height: 40px;
  background: rgba(255, 255, 255, 0.15);
  color: var(--on-brand); border: none; border-radius: var(--radius-pill);
  font-size: 24px; cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  z-index: 1;
  transition: background var(--dur-fast) var(--ease-out),
              transform var(--dur-fast) var(--ease-out);
}
.close-btn:hover {
  background: rgba(255, 255, 255, 0.28);
  transform: scale(1.04);
}

.fade-enter-active {
  transition: opacity var(--dur-slow) var(--ease-out);
}
.fade-leave-active {
  transition: opacity var(--dur-base) var(--ease-in);
}
.fade-enter-from, .fade-leave-to { opacity: 0; }
.fade-enter-active .lightbox-media,
.fade-leave-active .lightbox-media {
  transition: transform var(--dur-slow) var(--ease-out);
}
.fade-leave-active .lightbox-media {
  transition-timing-function: var(--ease-in);
  transition-duration: var(--dur-base);
}
.fade-enter-from .lightbox-media,
.fade-leave-to .lightbox-media {
  transform: scale(0.96);
}
</style>
