<script setup lang="ts">
/**
 * BrandLogo — the P-Chat mark. A rounded "speech bubble" shape
 * with a stylized "P" cut out of the upper-left interior. Used in
 * the sidebar header, top bar, empty state, and any other place a
 * brand mark is needed.
 *
 * Two variants:
 *   - default: filled bubble in --brand-500, P in white. For
 *     light backgrounds and as a stand-alone mark.
 *   - mono: bubble in currentColor (inherits parent text color),
 *     P in --bg. For use on colored/gradient surfaces where the
 *     brand color is already set by the parent (e.g. on top of a
 *     brand-500 button background).
 *
 * The logo is rendered as a single inline SVG so it picks up the
 * current font-size / theme tokens without an extra HTTP request.
 */
defineProps<{
  /** Pixel size of the rendered square. Default 24. */
  size?: number
  /** Visual variant. See file header. Default 'default'. */
  variant?: 'default' | 'mono'
  /** Accessible label. Default 'P-Chat'. */
  alt?: string
}>()
</script>

<template>
  <svg
    :width="size ?? 24"
    :height="size ?? 24"
    viewBox="0 0 32 32"
    fill="none"
    xmlns="http://www.w3.org/2000/svg"
    :class="['brand-logo', `brand-logo--${variant ?? 'default'}`]"
    role="img"
    :aria-label="alt ?? 'P-Chat'"
  >
    <!-- Speech-bubble: rounded rect with a small tail at the
         bottom-left interior. fill is driven by CSS so theme
         switches cascade through without re-rendering the SVG. -->
    <path
      d="M8 4 H24 A4 4 0 0 1 28 8 V20 A4 4 0 0 1 24 24 H17 L11 28 V24 H8 A4 4 0 0 1 4 20 V8 A4 4 0 0 1 8 4 Z"
      class="bubble"
    />
    <!-- P letter: solid bowl (no inner cutout) so it stays crisp
         at 16px and below where the inner cutout would alias. -->
    <path
      d="M11 8 H17 A4 4 0 0 1 17 16 H13.5 V20 H11 Z"
      class="letter"
    />
  </svg>
</template>

<style scoped>
.brand-logo {
  display: inline-block;
  vertical-align: middle;
  flex-shrink: 0;
}

/* Default: brand-colored bubble, white letter. */
.brand-logo--default .bubble { fill: var(--brand-500); }
.brand-logo--default .letter { fill: #ffffff; }

/* Mono: bubble inherits currentColor, letter is the surface
 * background — for use on top of a colored area where the parent
 * already sets a text color (e.g. on a brand button). */
.brand-logo--mono .bubble { fill: currentColor; }
.brand-logo--mono .letter { fill: var(--surface-0); }
</style>
