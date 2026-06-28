<script setup lang="ts">
import { ref, nextTick, watch, computed, onMounted } from 'vue'
import { NScrollbar, NSpin, NSpace, NButton, NInput, NTooltip, useMessage, NIcon } from 'naive-ui'
import MessageBubble from './MessageBubble.vue'
import InputArea from './InputArea.vue'
import TodoPanel from './TodoPanel.vue'
import { state, currentMessages, isStreaming, switchSession } from '../stores/chat'

const messagesEl = ref<HTMLElement | null>(null)
const message = useMessage()

function scrollToBottom() {
  nextTick(() => {
    if (messagesEl.value) {
      messagesEl.value.scrollTo({ top: 99999, behavior: 'smooth' })
    }
  })
}

watch(() => currentMessages.value, () => scrollToBottom(), { deep: true })
watch(() => state.currentID, () => scrollToBottom())

onMounted(() => scrollToBottom())
</script>

<template>
  <main class="chat-main">
    <div class="chat-header">
      <div class="header-title">
        {{ state.sessions.find(s => s.id === state.currentID)?.title || 'P-Chat' }}
      </div>
    </div>
    <TodoPanel />
    <NScrollbar ref="messagesEl" class="messages-scroll" :native-scrollbar="false">
      <div class="messages">
        <div v-if="currentMessages.length === 0" class="empty">
          <div class="empty-icon">💬</div>
          <div class="empty-title">开始一个新对话吧</div>
          <div class="empty-hint">输入 /help 查看可用命令 · 拖入或粘贴文件可作为附件</div>
        </div>
        <MessageBubble
          v-for="(m, i) in currentMessages"
          :key="i"
          :message="m"
          :streaming="isStreaming && i === currentMessages.length - 1 && m.role === 'assistant'"
        />
      </div>
    </NScrollbar>
    <InputArea />
  </main>
</template>

<style scoped>
.chat-main {
  flex: 1;
  display: flex;
  flex-direction: column;
  background: var(--bg);
  min-width: 0;
}
.chat-header {
  height: 48px;
  padding: 0 16px;
  display: flex;
  align-items: center;
  border-bottom: 1px solid var(--border);
  background: var(--bg-2);
  flex-shrink: 0;
}
.header-title { font-weight: 500; font-size: 14px; }
.messages-scroll { flex: 1; min-height: 0; }
.messages {
  padding: 12px 0;
  min-height: 100%;
}
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: var(--text-3);
  gap: 8px;
  padding-top: 120px;
  text-align: center;
}
.empty-icon { font-size: 48px; }
.empty-title { font-size: 15px; color: var(--text-2); }
.empty-hint { font-size: 12px; color: var(--text-4); }
</style>
