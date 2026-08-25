<script setup lang="ts">
/**
 * BrandLogo — shared P-Chat mark used in the sidebar, top bar, and
 * other compact brand positions. The source image lives in
 * public/favicon.png so the browser favicon and in-app logo stay in
 * sync.
 */
defineProps<{
  /** Pixel size of the rendered square. Default 24. */
  size?: number
  /** Kept for older call sites; the bitmap logo has one visual form. */
  variant?: 'default' | 'mono'
  /** Accessible label. Default 'P-Chat'. */
  alt?: string
}>()
</script>

<template>
  <img
    src="/favicon.png"
    :width="size ?? 24"
    :height="size ?? 24"
    :class="['brand-logo', `brand-logo--${variant ?? 'default'}`]"
    :alt="alt ?? 'P-Chat'"
    decoding="async"
  />
</template>

<style scoped>
.brand-logo {
  display: inline-block;
  vertical-align: middle;
  flex-shrink: 0;
  object-fit: contain;
}
</style>
