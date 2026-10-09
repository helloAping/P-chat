import type { Message } from '../api/client'
import type { PendingAttachment } from '../stores/chat'

// 初始 HTTP 拒绝意味着用户消息从未入库；只撤销这一回合的本地气泡。
// An initial HTTP rejection means the user turn was never persisted; remove only this local turn.
export function removeUnacceptedLocalTurn(messages: Message[], clientMsgID: number): boolean {
  const index = messages.findIndex(message => message.role === 'user' && message.id === clientMsgID)
  if (index < 0) return false
  const user = messages[index]
  for (const attachment of user.attachments || []) {
    if (attachment.url?.startsWith('blob:')) URL.revokeObjectURL(attachment.url)
  }
  const next = messages[index + 1]
  if (next?.role === 'assistant' && !next.id && !next.content && (!next.parts || next.parts.length === 0)) {
    messages.splice(index + 1, 1)
  }
  messages.splice(index, 1)
  return true
}

// 清空待发附件时本地 blob URL 已释放；恢复草稿时重建预览并保留 upload_id。
// Clearing staged attachments revokes local blob URLs; rebuild previews while retaining upload IDs.
export function restoreRejectedAttachments(
  staged: PendingAttachment[],
  current: PendingAttachment[],
  invalidDetail = '',
): PendingAttachment[] {
  const restored = staged.map((attachment) => {
    const copy = { ...attachment }
    if (copy._file && copy._blobURL) {
      try {
        const previewURL = URL.createObjectURL(copy._file)
        copy._blobURL = previewURL
        copy._previewURL = previewURL
      } catch {
        copy._blobURL = undefined
        copy._previewURL = copy._dataURL || ''
      }
    }
    if (invalidDetail.includes(`"${copy.name}"`) || invalidDetail.includes(`“${copy.name}”`)) {
      copy._error = true
      copy._errorMessage = '文件损坏或格式无效，请重新上传'
    }
    return copy
  })
  return [...restored, ...current.filter(attachment => !staged.includes(attachment))]
}

export function mergeRejectedDraft(sentText: string, currentText: string): string {
  if (!sentText) return currentText
  return currentText ? `${sentText}\n${currentText}` : sentText
}
