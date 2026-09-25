import type { AttachmentArtifactKind } from './attachmentArtifacts'

// 媒体子组件在原生 contextmenu 事件冒泡前登记实际目标。
// Media children register the exact target before the native contextmenu event bubbles.
export interface MediaContextTarget {
  kind: AttachmentArtifactKind
  url?: string
  text?: string
  name?: string
  mime?: string
}

const targetByEvent = new WeakMap<Event, MediaContextTarget>()

export function markMediaContextTarget(event: Event, target: MediaContextTarget): void {
  targetByEvent.set(event, target)
}

export function getMediaContextTarget(event: Event): MediaContextTarget | undefined {
  return targetByEvent.get(event)
}
