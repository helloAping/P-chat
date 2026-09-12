<script setup lang="ts">
import { onMounted, onUnmounted, watch } from 'vue'
import { state } from '../stores/chat'
import { X } from './icons'

let previousBodyOverflow = ''

function close() {
  state.lightbox = { show: false, src: '', alt: '', kind: 'image' }
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && state.lightbox.show) close()
}

onMounted(() => window.addEventListener('keydown', onKey))
onUnmounted(() => {
  window.removeEventListener('keydown', onKey)
  document.body.style.overflow = previousBodyOverflow
})

watch(
  () => state.lightbox.show,
  (show) => {
    if (show) {
      previousBodyOverflow = document.body.style.overflow
      document.body.style.overflow = 'hidden'
    } else {
      document.body.style.overflow = previousBodyOverflow
    }
  },
)
</script>

<template>
  <Transition name="fade">
    <div
      v-if="state.lightbox.show"
      class="lightbox"
      role="dialog"
      aria-modal="true"
      :aria-label="state.lightbox.kind === 'video' ? '视频全屏预览' : '图片全屏预览'"
      @click="close"
    >
      <button class="close-btn" type="button" @click.stop="close" title="关闭 (Esc)" aria-label="关闭预览">
        <X :size="20" />
      </button>
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
        :title="state.lightbox.alt"
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
  background: var(--media-backdrop);
  backdrop-filter: blur(var(--glass-blur));
  -webkit-backdrop-filter: blur(var(--glass-blur));
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
  background: var(--media-canvas);
}
.close-btn {
  position: absolute; top: var(--space-4); right: var(--space-4);
  width: 40px; height: 40px;
  background: var(--media-control);
  color: var(--on-brand); border: none; border-radius: var(--radius-pill);
  font-size: 24px; cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  z-index: 1;
  transition: background var(--dur-fast) var(--ease-out),
              transform var(--dur-fast) var(--ease-out);
}
.close-btn:hover {
  background: var(--media-control-hover);
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
