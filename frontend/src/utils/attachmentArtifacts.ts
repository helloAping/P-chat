import type { Message, MessageAttachment, MessagePart, ToolPart } from '../api/client'

export type AttachmentArtifactKind = 'image' | 'video' | 'audio' | 'text' | 'file' | 'image_not_supported'
export type ToolGeneratedAssetKind = Exclude<AttachmentArtifactKind, 'image_not_supported'>
export type AttachmentArtifactSource = 'upload' | 'generation' | 'browser_screenshot'

export interface AttachmentArtifact {
  key: string
  source: AttachmentArtifactSource
  kind: AttachmentArtifactKind
  url?: string
  text?: string
  name?: string
  mime?: string
  uploadId?: string
  messageId?: number
  toolId?: string
  toolName?: string
}

export interface ToolGeneratedAsset {
  id?: string
  kind: ToolGeneratedAssetKind
  mime_type?: string
  mime?: string
  name?: string
  url?: string
  text?: string
  source: Exclude<AttachmentArtifactSource, 'upload'>
}

const imageExtensions = new Set([
  'avif', 'bmp', 'gif', 'heic', 'ico', 'jpeg', 'jpg', 'png', 'svg', 'tif', 'tiff', 'webp',
])
const videoExtensions = new Set(['avi', 'm4v', 'mkv', 'mov', 'mp4', 'mpeg', 'mpg', 'webm'])
const audioExtensions = new Set(['aac', 'flac', 'm4a', 'mp3', 'ogg', 'opus', 'pcm', 'wav', 'wma'])
const textExtensions = new Set(['css', 'csv', 'go', 'html', 'js', 'json', 'log', 'md', 'py', 'rs', 'sql', 'ts', 'tsx', 'txt', 'vue', 'xml', 'yaml', 'yml'])

function cleanPath(value: string | undefined): string {
  return (value || '').split(/[?#]/)[0].trim()
}

export function artifactExtension(value: { name?: string; url?: string }): string {
  const candidates = [cleanPath(value.name), cleanPath(value.url)]
  for (const candidate of candidates) {
    const lower = candidate.toLowerCase()
    const lastDot = lower.lastIndexOf('.')
    if (lastDot >= 0 && lastDot < lower.length - 1) {
      return lower.slice(lastDot + 1)
    }
  }
  return ''
}

function kindFromHints(
  rawKind: string | undefined,
  mime: string | undefined,
  name: string | undefined,
  url: string | undefined,
  hasText: boolean,
): AttachmentArtifactKind {
  const kind = (rawKind || '').toLowerCase()
  if (kind === 'image_not_supported') return 'image_not_supported'
  if (kind === 'image' || kind === 'video' || kind === 'audio' || kind === 'text' || kind === 'file') return kind

  const normalizedMime = (mime || '').toLowerCase()
  if (normalizedMime.startsWith('image/')) return 'image'
  if (normalizedMime.startsWith('video/')) return 'video'
  if (normalizedMime.startsWith('audio/')) return 'audio'
  if (normalizedMime.startsWith('text/')) return 'text'

  const extension = artifactExtension({ name, url })
  if (imageExtensions.has(extension)) return 'image'
  if (videoExtensions.has(extension)) return 'video'
  if (audioExtensions.has(extension)) return 'audio'
  if (textExtensions.has(extension)) return 'text'

  return hasText ? 'text' : 'file'
}

export function attachmentVisualKind(attachment: MessageAttachment): AttachmentArtifactKind {
  const hinted = kindFromHints(
    attachment.kind,
    attachment.mime,
    attachment.name,
    attachment.url,
    !!attachment.text,
  )
  if (hinted !== 'file') return hinted
  if (attachment.type === 'image_url') return 'image'
  if (attachment.type === 'video_url') return 'video'
  if (attachment.type === 'audio_url') return 'audio'
  return hinted
}

export function isAssetURL(url: string | undefined): url is string {
  if (!url) return false
  return url.startsWith('/api/v1/generated/')
    || url.startsWith('/api/v1/uploads/')
    || url.startsWith('data:')
    || url.startsWith('blob:')
    || url.startsWith('http://')
    || url.startsWith('https://')
}

export function attachmentArtifactTypeLabel(item: { kind: AttachmentArtifactKind; name?: string; mime?: string; mime_type?: string; url?: string }): string {
  if ((item.kind === 'image' || item.kind === 'video') && !item.url) return '预览不可用'
  if (item.kind === 'image') return '图片'
  if (item.kind === 'video') return '视频'
  if (item.kind === 'audio') return '音频'
  if (item.kind === 'image_not_supported') return '图片不可用'

  const extension = artifactExtension(item)
  const labels: Record<string, string> = {
    pdf: 'PDF 文档',
    doc: 'Word 文档',
    docx: 'Word 文档',
    ppt: '演示文稿',
    pptx: '演示文稿',
    xls: '电子表格',
    xlsx: '电子表格',
    csv: '电子表格',
    md: 'Markdown 文档',
    txt: '文本附件',
    json: 'JSON 文件',
    zip: '压缩文件',
    rar: '压缩文件',
    '7z': '压缩文件',
  }
  if (labels[extension]) return labels[extension]
  if (extension) return `${extension.toUpperCase()} 文件`
  return item.kind === 'text' ? '文本附件' : '文件附件'
}

export function attachmentArtifactSourceLabel(source: AttachmentArtifactSource): string {
  if (source === 'upload') return '用户上传'
  if (source === 'browser_screenshot') return '浏览器截图'
  return '工具生成'
}

function extensionForKind(kind: AttachmentArtifactKind, mime?: string, name?: string, url?: string): string {
  const extension = artifactExtension({ name, url })
  if (extension) return `.${extension}`
  const normalizedMime = (mime || '').toLowerCase()
  if (normalizedMime === 'image/jpeg') return '.jpg'
  if (normalizedMime === 'image/png') return '.png'
  if (normalizedMime === 'image/webp') return '.webp'
  if (normalizedMime === 'video/webm') return '.webm'
  if (normalizedMime === 'video/quicktime') return '.mov'
  if (normalizedMime === 'audio/wav') return '.wav'
  if (normalizedMime === 'audio/ogg') return '.ogg'
  if (normalizedMime === 'application/json') return '.json'
  if (normalizedMime === 'application/pdf') return '.pdf'
  if (kind === 'image') return '.png'
  if (kind === 'video') return '.mp4'
  if (kind === 'audio') return '.mp3'
  if (kind === 'text') return '.txt'
  return '.bin'
}

export function attachmentArtifactFileName(item: {
  id?: string
  kind: AttachmentArtifactKind
  source?: AttachmentArtifactSource
  mime?: string
  mime_type?: string
  name?: string
  url?: string
  uploadId?: string
  toolId?: string
}): string {
  const name = item.name?.trim()
  if (name) return name
  const identity = (item.id || item.uploadId || item.toolId || 'result').replace(/[^a-z0-9]/gi, '').slice(0, 8) || 'result'
  const base = item.source === 'browser_screenshot'
    ? 'pchat-screenshot'
    : item.source === 'upload'
      ? 'pchat-upload'
      : `pchat-${item.kind}`
  return `${base}-${identity}${extensionForKind(item.kind, item.mime_type || item.mime, item.name, item.url)}`
}

function pushUnique(out: AttachmentArtifact[], seen: Set<string>, item: AttachmentArtifact) {
  if (seen.has(item.key)) return
  seen.add(item.key)
  out.push(item)
}

function attachmentKey(attachment: MessageAttachment, messageId: number | undefined, index: number): string {
  if (attachment.upload_id) return `upload:${attachment.upload_id}`
  if (attachment.url) return `upload-url:${attachment.url}`
  return `upload-text:${messageId || 'local'}:${index}:${attachment.name || ''}:${(attachment.text || '').slice(0, 80)}`
}

export function attachmentArtifactFromMessageAttachment(
  attachment: MessageAttachment,
  messageId: number | undefined,
  index: number,
): AttachmentArtifact {
  return {
    key: attachmentKey(attachment, messageId, index),
    source: 'upload',
    kind: attachmentVisualKind(attachment),
    url: attachment.url,
    text: attachment.text,
    name: attachment.name,
    mime: attachment.mime,
    uploadId: attachment.upload_id,
    messageId,
  }
}

function normalizeToolAsset(asset: any, source: Exclude<AttachmentArtifactSource, 'upload'>): ToolGeneratedAsset | null {
  if (!asset || typeof asset !== 'object') return null
  const url = typeof asset.url === 'string' ? asset.url : undefined
  const text = typeof asset.text === 'string' ? asset.text : undefined
  if (url && !isAssetURL(url)) return null
  if (!url && !text) return null
  const name = typeof asset.name === 'string' ? asset.name : undefined
  const mime = typeof asset.mime_type === 'string'
    ? asset.mime_type
    : typeof asset.mime === 'string'
      ? asset.mime
      : undefined
  const hinted = kindFromHints(
    typeof asset.kind === 'string' ? asset.kind : undefined,
    mime,
    name,
    url,
    !!text,
  )
  const kind: ToolGeneratedAssetKind = hinted === 'image_not_supported' ? 'file' : hinted
  return {
    id: typeof asset.id === 'string' ? asset.id : undefined,
    kind,
    mime_type: typeof asset.mime_type === 'string' ? asset.mime_type : undefined,
    mime: typeof asset.mime === 'string' ? asset.mime : undefined,
    name,
    url,
    text,
    source,
  }
}

export function toolGeneratedAssetsFromPart(part: ToolPart): ToolGeneratedAsset[] {
  const result = part.result
  if (!result) return []
  const isBrowserScreenshot = part.name === 'browser_screenshot'
  if (!part.name.startsWith('generate_') && !isBrowserScreenshot) return []
  const source: Exclude<AttachmentArtifactSource, 'upload'> = isBrowserScreenshot ? 'browser_screenshot' : 'generation'

  try {
    const parsed = JSON.parse(result)
    if (Array.isArray(parsed?.assets)) {
      return parsed.assets
        .map((asset: any) => normalizeToolAsset(asset, source))
        .filter((asset: ToolGeneratedAsset | null): asset is ToolGeneratedAsset => !!asset)
    }
    if (isBrowserScreenshot && typeof parsed?.image === 'string' && isAssetURL(parsed.image)) {
      return [{
        kind: 'image',
        mime_type: 'image/jpeg',
        name: 'browser-screenshot.jpg',
        url: parsed.image,
        source,
      }]
    }
  } catch {
    // Legacy raw screenshot result.
  }

  if (isBrowserScreenshot && isAssetURL(result)) {
    const mime = result.startsWith('data:') ? result.slice(5, result.indexOf(';')) : 'image/jpeg'
    return [{
      kind: 'image',
      mime_type: mime || 'image/jpeg',
      name: 'browser-screenshot.jpg',
      url: result,
      source,
    }]
  }
  return []
}

function walkParts(parts: MessagePart[] | undefined, messageId: number | undefined, out: AttachmentArtifact[], seen: Set<string>) {
  if (!parts?.length) return
  for (const part of parts) {
    if (part.kind === 'tool') {
      toolGeneratedAssetsFromPart(part).forEach((asset, index) => {
        const key = asset.url
          ? `${asset.source}:${asset.url}`
          : `${asset.source}:${part.tool_id || part.id || part.name}:${asset.id || asset.name || index}`
        pushUnique(out, seen, {
          key,
          source: asset.source,
          kind: asset.kind,
          url: asset.url,
          text: asset.text,
          name: asset.name,
          mime: asset.mime_type || asset.mime,
          messageId,
          toolId: part.tool_id || part.id,
          toolName: part.name,
        })
      })
    } else if (part.kind === 'sub_agent') {
      walkParts(part.parts, messageId, out, seen)
    }
  }
}

export function collectConversationAttachments(messages: Message[]): AttachmentArtifact[] {
  const out: AttachmentArtifact[] = []
  const seen = new Set<string>()
  for (const message of messages) {
    ;(message.attachments || []).forEach((attachment, index) => {
      pushUnique(out, seen, attachmentArtifactFromMessageAttachment(attachment, message.id, index))
    })
    walkParts(message.parts, message.id, out, seen)
  }
  return out
}

export function collectMessageGeneratedAttachments(message: Message): AttachmentArtifact[] {
  const out: AttachmentArtifact[] = []
  const seen = new Set<string>()
  walkParts(message.parts, message.id, out, seen)
  return out
}
