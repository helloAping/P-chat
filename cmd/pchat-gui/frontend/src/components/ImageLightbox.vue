<script setup lang="ts">
import { onMounted, onUnmounted, watch } from 'vue'
import { state } from '../stores/chat'

function close() {
  state.lightbox = { show: false, src: '', alt: '' }
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
        :src="state.lightbox.src"
        :alt="state.lightbox.alt"
        class="lightbox-img"
        @click.stop
      />
    </div>
  </Transition>
</template>

<style scoped>
.lightbox {
  position: fixed; inset: 0;
  background: rgba(0, 0, 0, 0.9);
  display: flex; align-items: center; justify-content: center;
  z-index: 1000;
  cursor: zoom-out;
}
.lightbox-img {
  max-width: 95vw;
  max-height: 95vh;
  object-fit: contain;
  border-radius: 6px;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.6);
  cursor: default;
}
.close-btn {
  position: absolute; top: 16px; right: 16px;
  width: 40px; height: 40px;
  background: rgba(255, 255, 255, 0.15);
  color: #fff; border: none; border-radius: 50%;
  font-size: 24px; cursor: pointer;
  display: flex; align-items: center; justify-content: center;
}
.close-btn:hover { background: rgba(255, 255, 255, 0.25); }

.fade-enter-active, .fade-leave-active { transition: opacity 0.2s; }
.fade-enter-from, .fade-leave-to { opacity: 0; }
</style>
