<script setup lang="ts">
/**
 * AppModal — the unified modal wrapper for P-Chat.
 *
 * Before PR #7, every dialog in the app used Naive UI's
 * `NModal preset="card"` raw, with each call site hand-rolling
 * width, padding, and header styling. The look drifted from
 * modal to modal, and the close button showed up in some
 * modals but not others. AppModal fixes both:
 *
 *   - Three sizes (sm / md / lg) bound to a width token, so
 *     every "small confirmation" / "mid form" / "large settings
 *     sheet" lands on the same scale.
 *   - Consistent 20px padding, 56px header, 14px radius, and
 *     shadow-lg. Subtle but real polish: borders use
 *     --border-default, headers use --surface-1 (so they
 *     separate visually from the body).
 *   - Header always has a close button (X icon, top-right) so
 *     the user never has to hunt for a way out.
 *   - Esc closes by default (NModal does this for free; we
 *     just don't disable it).
 *   - Click on the mask closes by default; the `maskClosable`
 *     prop lets call sites override (e.g. QuestionModal uses
 *     `false` so an accidental click doesn't drop the
 *     question state).
 *
 * Usage:
 *   <AppModal
 *     v-model:show="showAddProject"
 *     title="添加项目"
 *     size="md"
 *   >
 *     <div class="form">...</div>
 *     <template #footer>
 *       <NButton @click="showAddProject = false">取消</NButton>
 *       <NButton type="primary" @click="onConfirm">添加</NButton>
 *     </template>
 *   </AppModal>
 *
 * The wrapper is built on top of NModal — we keep NModal's
 * teleporting + focus trap + Esc handling, and just style
 * the content. This is intentional: reimplementing focus
 * trap is a recipe for accessibility bugs, and NModal's
 * already battle-tested.
 */
import { NModal, NButton } from 'naive-ui'
import { X as XIcon } from './icons'

type Size = 'sm' | 'md' | 'lg'

const props = withDefaults(defineProps<{
  /** v-model:show — controls visibility. */
  show: boolean
  /** Modal title shown in the header. */
  title?: string
  /** Width preset: sm (400), md (560), lg (720). */
  size?: Size
  /** Whether the user can dismiss by clicking the mask. */
  maskClosable?: boolean
  /** Whether the user can dismiss by pressing Esc. */
  closeOnEsc?: boolean
  /** Whether to render the X close button in the header. */
  closable?: boolean
  /** Show a thin border at the top in the brand color —
   * used for the "你确认要删除吗?" pattern where the
   * intent should be obvious. */
  accentTop?: boolean
  /** Accent color variant. Default = warn (yellow). Pass
   * 'error' for destructive-confirm patterns, 'primary' for
   * informational. */
  accentVariant?: 'primary' | 'warn' | 'error'
}>(), {
  size: 'md',
  maskClosable: true,
  closeOnEsc: true,
  closable: true,
  accentTop: false,
  accentVariant: 'warn',
})

const emit = defineEmits<{
  (e: 'update:show', v: boolean): void
  (e: 'close'): void
}>()

function close() {
  emit('update:show', false)
  emit('close')
}

// Width tokens. The number is px; the actual CSS just sets
// the modal's `style` width, NModal reads it and constrains
// the inner card accordingly.
const sizeMap: Record<Size, number> = {
  sm: 400,
  md: 560,
  lg: 720,
}
</script>

<template>
  <NModal
    :show="show"
    @update:show="(v) => emit('update:show', v)"
    :mask-closable="maskClosable"
    :close-on-esc="closeOnEsc"
    :show-mask="true"
    preset="card"
    :style="{ width: sizeMap[size] + 'px' }"
    :bordered="false"
    :title="undefined"
    :closable="false"
    @close="emit('close')"
  >
    <!-- Custom header: title on the left, optional close
         on the right. Replaces NModal's default title slot
         so we get consistent height / padding / border. -->
    <template #header>
      <div class="app-modal-header">
        <span class="app-modal-title">{{ title }}</span>
        <button
          v-if="closable"
          type="button"
          class="app-modal-close"
          :aria-label="'关闭'"
          @click="close"
        >
          <XIcon :size="16" />
        </button>
      </div>
    </template>

    <div :class="['app-modal-body', { 'app-modal-accent-top': accentTop, [`app-modal-accent-${accentVariant}`]: accentTop }]">
      <slot />
    </div>

    <template #action>
      <div class="app-modal-footer">
        <slot name="footer" />
      </div>
    </template>
  </NModal>
</template>

<style scoped>
/* --- Header ---------------------------------------------------------- */
.app-modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0;
  /* NModal gives the header a min-height via its preset="card"
   * default (44px). We want a bit more breathing room for
   * the 16px title. We can't override the preset padding, so
   * we just give the title + close button some extra room
   * via the inner content. */
  width: 100%;
  padding-right: 4px;
}
.app-modal-title {
  font-size: 16px;
  font-weight: 600;
  color: var(--text-primary);
  letter-spacing: -0.01em;
  line-height: 1.3;
}
.app-modal-close {
  background: transparent;
  border: none;
  color: var(--text-tertiary);
  cursor: pointer;
  padding: 6px;
  border-radius: var(--radius-sm);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  transition: background var(--dur-fast) var(--ease-out),
              color var(--dur-fast) var(--ease-out);
}
.app-modal-close:hover {
  background: var(--surface-3);
  color: var(--text-primary);
}

/* --- Body ------------------------------------------------------------- */
.app-modal-body {
  /* The NModal preset="card" already gives 20px padding; we
   * can stay thin here and just set a top border to separate
   * from the header. */
  font-size: 14px;
  color: var(--text-primary);
  position: relative;
}
.app-modal-body :deep(p) { margin: 0 0 12px; }
.app-modal-body :deep(p:last-child) { margin-bottom: 0; }

/* Accent-top variants: a 3px bar across the top of the
 * body content (below the header). Used for "are you sure
 * you want to delete?" dialogs where the destructive intent
 * should be obvious. */
.app-modal-accent-top {
  border-top: 1px solid var(--border-subtle);
  padding-top: 16px;
}
.app-modal-accent-top::before {
  content: '';
  position: absolute;
  top: -1px;
  left: 0;
  right: 0;
  height: 3px;
  background: var(--brand-500);
}
.app-modal-accent-warn::before { background: var(--warn-500); }
.app-modal-accent-error::before { background: var(--error-500); }
.app-modal-accent-primary::before { background: var(--brand-500); }

/* --- Footer ------------------------------------------------------------ */
.app-modal-footer {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  /* The footer slot inside NModal's preset="card" already
   * gives some padding; we keep that and just control the
   * button order (cancel on the left, primary on the right). */
  width: 100%;
}
</style>

<style>
/* NModal's preset="card" applies a default border-radius
 * and box-shadow that we want to override with our design
 * tokens. We use a non-scoped rule so the deep selector
 * actually reaches NModal's inner card (it's in the
 * teleported subtree, outside the scoped boundary). */
.n-modal-container .n-card {
  border-radius: var(--radius-lg) !important;
  box-shadow: var(--shadow-lg) !important;
  background: var(--surface-1) !important;
  border: 1px solid var(--border-default) !important;
}
.n-modal-container .n-card__content,
.n-modal-container .n-card__action {
  padding: 16px 20px !important;
  background: var(--surface-1) !important;
}
.n-modal-container .n-card__content {
  /* The body is the variable-height area. Cap at a sane
   * value so a very long form scrolls instead of
   * running off-screen. */
  max-height: min(70vh, 600px);
  overflow-y: auto;
}
.n-modal-container .n-card__action {
  border-top: 1px solid var(--border-subtle) !important;
}
.n-modal-mask {
  background: var(--surface-overlay) !important;
  backdrop-filter: blur(4px) !important;
}
</style>
