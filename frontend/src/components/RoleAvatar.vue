<script setup lang="ts">
/**
 * RoleAvatar — a 32px circular avatar that identifies a chat
 * message's role. The shape, color, and inner glyph together
 * communicate who's talking at a glance.
 *
 *   - assistant:  BrandLogo (the chat-bubble + P mark) on
 *                 --ai-500 (purple) so the AI identity is
 *                 distinct from the brand color used for
 *                 user messages. PR #1 reserved --ai-* for
 *                 exactly this purpose; PR #5 is the first
 *                 consumer.
 *
 *   - user:       "我" (the Chinese first-person pronoun) on
 *                 --brand-500. Using text instead of an icon
 *                 reinforces the personal "this is me"
 *                 feeling; brand color ties it to the user's
 *                 bubble in the chat column.
 *
 *   - system:     Info icon on --surface-2. Neutral; doesn't
 *                 draw the eye away from the content but
 *                 still tells the user the message isn't from
 *                 the AI or them.
 *
 *   - tool:       (Optional fourth role, used for some
 *                 server-rendered tool result messages.)
 *                 Wrench icon on --surface-2.
 *
 * The component takes a `role` prop and a `size` prop (default
 * 32). All visual states come from the design tokens, so
 * theme switches cascade without code changes.
 */
import { computed } from 'vue'
import BrandLogo from './BrandLogo.vue'
import { Info, Wrench } from './icons'

type Role = 'user' | 'assistant' | 'system' | 'tool'

const props = withDefaults(defineProps<{
  role: Role
  size?: number
}>(), { size: 32 })

// Inner content is one of: a text glyph (user), the brand
// mark (assistant), or a lucide icon (system / tool). Storing
// the component ref + props in a computed lets the template
// render via <component :is="..."> without per-role template
// branches.
const inner = computed(() => {
  switch (props.role) {
    case 'assistant':
      // The BrandLogo on a purple background reads as
      // "the AI brand mark" — distinct from the blue
      // user bubble. The logo is rendered at 65% of the
      // avatar size so the bubble padding shows.
      return { kind: 'brand' as const, size: Math.round(props.size * 0.62) }
    case 'user':
      return { kind: 'text' as const, text: '我' }
    case 'system':
      return { kind: 'icon' as const, Icon: Info, size: Math.round(props.size * 0.5) }
    case 'tool':
      return { kind: 'icon' as const, Icon: Wrench, size: Math.round(props.size * 0.5) }
  }
})
</script>

<template>
  <div
    class="role-avatar"
    :class="`role-avatar--${role}`"
    :style="{ width: size + 'px', height: size + 'px' }"
    :aria-label="role === 'user' ? '用户' : role === 'assistant' ? 'AI 助手' : role === 'system' ? '系统' : '工具'"
  >
    <BrandLogo v-if="inner.kind === 'brand'" :size="inner.size" />
    <span v-else-if="inner.kind === 'text'" class="role-avatar-text">{{ inner.text }}</span>
    <component v-else :is="inner.Icon" :size="inner.size" class="role-avatar-icon" />
  </div>
</template>

<style scoped>
.role-avatar {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  flex-shrink: 0;
  font-weight: 600;
  user-select: none;
  /* Subtle ring helps the avatar separate from
   * adjacent surfaces in the chat column without
   * needing a heavy border. */
  box-shadow: 0 0 0 1px var(--border-subtle);
}

/* Per-role background + foreground. The text color
 * is locked to white for assistant (high-contrast on
 * purple) and brand (high-contrast on brand-500). */
.role-avatar--assistant {
  background: var(--ai-500);
  color: #ffffff;
}
.role-avatar--user {
  background: var(--brand-500);
  color: #ffffff;
}
.role-avatar--system {
  background: var(--surface-2);
  color: var(--text-secondary);
}
.role-avatar--tool {
  background: var(--surface-2);
  color: var(--text-secondary);
}

.role-avatar-text {
  font-size: 13px;
  line-height: 1;
  /* "我" is a square-shaped Chinese character; default
   * sans rendering fits the 32px circle cleanly. */
  font-family: var(--font-sans);
}
.role-avatar-icon {
  color: inherit;
  flex-shrink: 0;
}
</style>
