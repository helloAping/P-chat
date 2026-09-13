<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useMessage } from 'naive-ui'
import { state } from '../stores/chat'
import { downloadBlob, downloadFromUrl } from '../utils/clipboard'
import {
  attachmentArtifactFileName,
  attachmentArtifactSourceLabel,
  attachmentArtifactTypeLabel,
  type AttachmentArtifact,
} from '../utils/attachmentArtifacts'
import {
  ChevronDown,
  ChevronRight,
  Download,
  File,
  FileText,
  Film,
  ImageIcon,
  Play,
  Volume2,
} from './icons'

const props = defineProps<{
  assets: AttachmentArtifact[]
}>()

const toast = useMessage()
const expanded = ref(false)
const DISPLAY_LIMIT = 5

const visibleAssets = computed(() =>
  expanded.value ? props.assets : props.assets.slice(0, DISPLAY_LIMIT),
)
const hiddenCount = computed(() => Math.max(0, props.assets.length - DISPLAY_LIMIT))

watch(() => props.assets.map(asset => asset.key).join('|'), () => {
  expanded.value = false
})

function assetIcon(asset: AttachmentArtifact) {
  if (asset.kind === 'image') return ImageIcon
  if (asset.kind === 'video') return Film
  if (asset.kind === 'audio') return Volume2
  if (asset.kind === 'text') return FileText
  return File
}

function canPreview(asset: AttachmentArtifact): boolean {
  return (asset.kind === 'image' || asset.kind === 'video') && !!asset.url
}

function openAsset(asset: AttachmentArtifact) {
  if (!canPreview(asset) || !asset.url) return
  state.lightbox = {
    show: true,
    src: asset.url,
    alt: attachmentArtifactFileName(asset),
    kind: asset.kind === 'video' ? 'video' : 'image',
  }
}

function downloadAsset(asset: AttachmentArtifact) {
  const name = attachmentArtifactFileName(asset)
  if (asset.url) {
    downloadFromUrl(asset.url, name)
    toast.success('已开始下载')
    return
  }
  if (asset.text) {
    downloadBlob(new Blob([asset.text], { type: asset.mime || 'text/plain' }), name)
    toast.success('已下载')
    return
  }
  toast.info('没有可下载的内容')
}

function assetSubtitle(asset: AttachmentArtifact): string {
  const type = attachmentArtifactTypeLabel(asset)
  if (asset.source === 'generation') return type
  return `${attachmentArtifactSourceLabel(asset.source)} · ${type}`
}
</script>

<template>
  <section v-if="assets.length" class="generated-strip" aria-label="本条回复生成的附件">
    <div class="generated-strip-head">
      <span class="generated-strip-title">
        <ImageIcon :size="13" />
        <span>生成附件</span>
      </span>
      <span class="generated-strip-count">{{ assets.length }}</span>
    </div>

    <div class="generated-strip-list">
      <article
        v-for="asset in visibleAssets"
        :key="asset.key"
        class="generated-strip-card"
        :class="`generated-strip-card--${asset.kind}`"
      >
        <button
          v-if="asset.kind === 'image' && asset.url"
          type="button"
          class="generated-strip-preview generated-strip-preview--image"
          :aria-label="`预览 ${attachmentArtifactFileName(asset)}`"
          @click="openAsset(asset)"
        >
          <img :src="asset.url" :alt="attachmentArtifactFileName(asset)" loading="lazy" />
        </button>

        <button
          v-else-if="asset.kind === 'video' && asset.url"
          type="button"
          class="generated-strip-preview generated-strip-preview--video"
          :aria-label="`播放 ${attachmentArtifactFileName(asset)}`"
          @click="openAsset(asset)"
        >
          <video :src="asset.url" muted playsinline preload="metadata" />
          <span class="generated-strip-play" aria-hidden="true">
            <Play :size="20" fill="currentColor" />
          </span>
        </button>

        <div v-else-if="asset.kind === 'audio' && asset.url" class="generated-strip-audio">
          <span class="generated-strip-file-icon" aria-hidden="true">
            <Volume2 :size="18" />
          </span>
          <audio :src="asset.url" controls preload="metadata" />
        </div>

        <div v-else class="generated-strip-file">
          <span class="generated-strip-file-icon" aria-hidden="true">
            <component :is="assetIcon(asset)" :size="18" />
          </span>
          <span class="generated-strip-file-copy">
            <span>{{ attachmentArtifactTypeLabel(asset) }}</span>
            <span>{{ attachmentArtifactSourceLabel(asset.source) }}</span>
          </span>
        </div>

        <button
          v-if="asset.url || asset.text"
          type="button"
          class="generated-strip-download"
          title="下载"
          aria-label="下载"
          @click="downloadAsset(asset)"
        >
          <Download :size="13" />
        </button>

        <footer class="generated-strip-meta">
          <span class="generated-strip-identity">
            <span class="generated-strip-icon" aria-hidden="true">
              <component :is="assetIcon(asset)" :size="13" />
            </span>
            <span class="generated-strip-copy">
              <span class="generated-strip-name" :title="attachmentArtifactFileName(asset)">
                {{ attachmentArtifactFileName(asset) }}
              </span>
              <span class="generated-strip-subtitle">
                {{ assetSubtitle(asset) }}
              </span>
            </span>
          </span>
        </footer>
      </article>
    </div>

    <button
      v-if="hiddenCount"
      type="button"
      class="generated-strip-expand"
      @click="expanded = !expanded"
    >
      <component :is="expanded ? ChevronDown : ChevronRight" :size="12" />
      <span>{{ expanded ? '收起附件' : `展开其余 ${hiddenCount} 个附件` }}</span>
    </button>
  </section>
</template>

<style scoped>
.generated-strip {
  display: grid;
  gap: var(--space-2);
  margin-top: var(--space-3);
  padding-top: var(--space-2);
  border-top: 1px dashed var(--border-subtle);
}

.generated-strip-head {
  display: flex;
  align-items: center;
  justify-content: flex-start;
  gap: var(--space-2);
  min-width: 0;
}

.generated-strip-title {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  color: var(--text-secondary);
  font-size: 12px;
  font-weight: 600;
  line-height: 1.3;
}

.generated-strip-title svg {
  color: var(--brand-600);
}

.generated-strip-count {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: calc(var(--space-5) - var(--space-1));
  height: calc(var(--space-5) - var(--space-1));
  padding: 0 var(--space-1);
  border-radius: var(--radius-pill);
  background: var(--surface-2);
  color: var(--text-tertiary);
  font-size: 10.5px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  line-height: 1;
}

.generated-strip-list {
  display: flex;
  gap: var(--space-2);
  max-width: 100%;
  min-width: 0;
  overflow-x: auto;
  overscroll-behavior-x: contain;
  padding-bottom: var(--space-1);
}

.generated-strip-card {
  position: relative;
  flex: 0 0 min(calc(var(--space-8) * 5), 46vw);
  min-width: 0;
  display: grid;
  grid-template-rows: minmax(0, 1fr) auto;
  overflow: hidden;
  background: var(--surface-1);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  box-shadow: var(--shadow-sm);
  transition:
    border-color var(--dur-fast) var(--ease-out),
    box-shadow var(--dur-fast) var(--ease-out),
    transform var(--dur-fast) var(--ease-out);
}

.generated-strip-card:hover,
.generated-strip-card:focus-within {
  border-color: var(--border-strong);
  box-shadow: var(--shadow-md);
}

.generated-strip-card:active {
  transform: scale(0.995);
}

.generated-strip-preview {
  position: relative;
  display: block;
  width: 100%;
  min-width: 0;
  aspect-ratio: 16 / 9;
  padding: 0;
  overflow: hidden;
  appearance: none;
  border: 0;
  background: var(--surface-0);
  color: var(--text-primary);
  cursor: zoom-in;
}

.generated-strip-preview--video {
  cursor: pointer;
}

.generated-strip-preview img,
.generated-strip-preview video {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
  background: var(--surface-0);
}

.generated-strip-preview--image img {
  object-fit: contain;
}

.generated-strip-play {
  position: absolute;
  left: 50%;
  top: 50%;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: var(--space-8);
  height: var(--space-8);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-pill);
  background: var(--surface-overlay);
  color: var(--text-primary);
  box-shadow: var(--shadow-md);
  transform: translate(-50%, -50%);
  transition: transform var(--dur-fast) var(--ease-out);
  pointer-events: none;
}

.generated-strip-preview--video:hover .generated-strip-play,
.generated-strip-preview--video:focus-visible .generated-strip-play {
  transform: translate(-50%, -50%) scale(1.06);
}

.generated-strip-audio,
.generated-strip-file {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
  min-height: calc(var(--space-8) * 2);
  padding: var(--space-3);
  background: var(--surface-0);
}

.generated-strip-audio {
  flex-direction: column;
  align-items: stretch;
  justify-content: center;
}

.generated-strip-audio audio {
  width: 100%;
  height: var(--space-8);
}

.generated-strip-file-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: calc(var(--space-8) + var(--space-1));
  height: calc(var(--space-8) + var(--space-1));
  flex: 0 0 auto;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-2);
  color: var(--brand-600);
}

.generated-strip-file-copy {
  display: grid;
  min-width: 0;
  gap: calc(var(--space-1) / 2);
  color: var(--text-tertiary);
  font-size: 11.5px;
  line-height: 1.35;
}

.generated-strip-file-copy span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.generated-strip-file-copy span:first-child {
  color: var(--text-primary);
  font-weight: 600;
}

.generated-strip-download {
  position: absolute;
  right: var(--space-1);
  top: var(--space-1);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: var(--control-height);
  height: var(--control-height);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-overlay);
  color: var(--text-primary);
  box-shadow: var(--shadow-sm);
  cursor: pointer;
  opacity: 0;
  transform: translateY(calc(-1 * var(--space-1)));
  transition:
    opacity var(--dur-fast) var(--ease-out),
    transform var(--dur-fast) var(--ease-out),
    background var(--dur-fast) var(--ease-out),
    border-color var(--dur-fast) var(--ease-out);
}

.generated-strip-download:hover,
.generated-strip-download:focus-visible {
  border-color: var(--brand-100);
  background: color-mix(in srgb, var(--brand-500) 16%, var(--surface-overlay));
}

.generated-strip-card:hover .generated-strip-download,
.generated-strip-card:focus-within .generated-strip-download {
  opacity: 1;
  transform: translateY(0);
}

.generated-strip-meta {
  display: flex;
  align-items: center;
  min-width: 0;
  min-height: calc(var(--control-height) + var(--space-1));
  padding: var(--space-1) var(--space-2);
  border-top: 1px solid var(--border-subtle);
  background: color-mix(in srgb, var(--surface-1) 92%, transparent);
}

.generated-strip-identity {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
}

.generated-strip-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: calc(var(--space-7) - var(--space-1));
  height: calc(var(--space-7) - var(--space-1));
  flex: 0 0 auto;
  border-radius: var(--radius-sm);
  background: var(--brand-50);
  color: var(--brand-600);
}

.generated-strip-copy {
  display: grid;
  gap: calc(var(--space-1) / 2);
  min-width: 0;
}

.generated-strip-name,
.generated-strip-subtitle {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.generated-strip-name {
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 11.5px;
  font-weight: 600;
  line-height: 1.25;
}

.generated-strip-subtitle {
  color: var(--text-tertiary);
  font-size: 10.5px;
  line-height: 1.25;
}

.generated-strip-dot {
  color: var(--text-quaternary);
}

.generated-strip-expand {
  display: inline-flex;
  align-items: center;
  justify-self: start;
  gap: var(--space-1);
  padding: calc(var(--space-1) / 2) var(--space-1);
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-tertiary);
  font: inherit;
  font-size: 11.5px;
  cursor: pointer;
  transition: var(--transition-colors);
}

.generated-strip-expand:hover {
  background: var(--surface-3);
  color: var(--text-primary);
}

@media (hover: none) {
  .generated-strip-download {
    opacity: 1;
    transform: translateY(0);
    pointer-events: auto;
  }
}

@media (max-width: 560px) {
  .generated-strip-card {
    flex-basis: min(calc(var(--space-8) * 3.5), 72vw);
  }
}
</style>
