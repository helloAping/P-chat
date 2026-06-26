<script setup lang="ts">
import { onMounted, ref } from 'vue'
import {
  NConfigProvider, NMessageProvider, NDialogProvider, NNotificationProvider,
  darkTheme,
} from 'naive-ui'
import SessionSidebar from './components/SessionSidebar.vue'
import ChatWindow from './components/ChatWindow.vue'
import AppSettingsModal from './components/AppSettingsModal.vue'
import ImageLightbox from './components/ImageLightbox.vue'
import { state, loadSessions } from './stores/chat'

const showAppSettings = ref(false)

onMounted(async () => {
  // Expose a global close handle for AppSettingsModal so it can
  // dismiss itself without prop-drilling.
  ;(window as any).closeAppSettings = () => { showAppSettings.value = false }
  // Expose an open handle too, in case something other than the
  // sidebar needs it.
  ;(window as any).openAppSettings = () => { showAppSettings.value = true }
  try {
    await loadSessions()
  } catch (e) {
    console.error('init failed', e)
  }
})
</script>

<template>
  <NConfigProvider :theme="darkTheme">
    <NMessageProvider>
      <NDialogProvider>
        <NNotificationProvider>
          <div class="app">
            <SessionSidebar @open-settings="showAppSettings = true" />
            <ChatWindow />
            <ImageLightbox />
            <AppSettingsModal v-if="showAppSettings" />
          </div>
        </NNotificationProvider>
      </NDialogProvider>
    </NMessageProvider>
  </NConfigProvider>
</template>

<style scoped>
.app {
  display: flex;
  height: 100vh;
  width: 100vw;
  background: var(--bg);
}
</style>
