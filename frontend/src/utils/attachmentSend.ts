import type { PendingAttachment } from '../stores/chat'

export type AttachmentSendPlan =
  | { status: 'ready'; attachments: PendingAttachment[]; omitted: PendingAttachment[] }
  | { status: 'blocked' | 'cancelled'; failed: PendingAttachment[] }

// Keep failed documents out of the model context only after explicit consent.
// 仅在用户明确同意后，才从本次发送中排除上传失败的文档。
export async function planAttachmentSend(
  attachments: PendingAttachment[],
  hasText: boolean,
  confirmOmission: (failed: PendingAttachment[]) => Promise<boolean>,
): Promise<AttachmentSendPlan> {
  const failed = attachments.filter(a => a.kind === 'file' && (a._error || !a.id))
  if (failed.length === 0) return { status: 'ready', attachments, omitted: [] }
  if (!hasText) return { status: 'blocked', failed }
  if (!await confirmOmission(failed)) return { status: 'cancelled', failed }
  return { status: 'ready', attachments: attachments.filter(a => !failed.includes(a)), omitted: failed }
}
