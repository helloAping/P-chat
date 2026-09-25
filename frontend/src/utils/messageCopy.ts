import type { Message, MessageAttachment, MessagePart } from '../api/client'

function normalizeBlock(value: unknown): string {
  if (typeof value !== 'string') return ''
  return value.replace(/\r\n/g, '\n').replace(/^\n+|\n+$/g, '')
}

function joinBlocks(blocks: string[]): string {
  return blocks.map(normalizeBlock).filter(block => block.trim()).join('\n\n')
}

function parseJSON(value: string | undefined): unknown {
  if (!value) return null
  try {
    return JSON.parse(value)
  } catch {
    return null
  }
}

function formatUnknown(value: unknown): string {
  if (typeof value === 'string') return value
  if (value == null) return ''
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

function plainObjectEntries(value: unknown): Array<[string, unknown]> {
  if (!value || Array.isArray(value) || typeof value !== 'object') return []
  return Object.entries(value as Record<string, unknown>)
}

function questionTextForCopy(part: Extract<MessagePart, { kind: 'question' }>): string {
  const answerEntries = plainObjectEntries(parseJSON(part.name))
  if (answerEntries.length > 0) {
    return joinBlocks([
      '[问题回答]',
      answerEntries
        .map(([key, value]) => `${key}: ${formatUnknown(value)}`)
        .filter(line => line.trim())
        .join('\n'),
    ])
  }

  const parsed = parseJSON(part.text)
  const questions = Array.isArray((parsed as any)?.questions) ? (parsed as any).questions : []
  if (questions.length > 0) {
    return joinBlocks([
      '[问题]',
      questions
        .map((q: any) => {
          const header = formatUnknown(q?.header)
          const text = formatUnknown(q?.question)
          if (header && text) return `${header}: ${text}`
          return header || text
        })
        .filter((line: string) => line.trim())
        .join('\n'),
    ])
  }

  return part.text ? joinBlocks(['[问题]', part.text]) : ''
}

function toolTextForCopy(part: Extract<MessagePart, { kind: 'tool' }>): string {
  if (part.name === 'question' && !part.error) return ''
  const name = part.name || part.id || 'tool'
  const blocks: string[] = []
  if (part.result) {
    blocks.push(`[工具 ${name} 结果]`, part.result)
    if (part.result_truncated) blocks.push('[结果已截断，可在工具卡中查看完整输出]')
  }
  if (part.error) {
    blocks.push(`[工具 ${name} 错误]`, part.error)
  }
  return joinBlocks(blocks)
}

function skillTextForCopy(part: Extract<MessagePart, { kind: 'skill' }>): string {
  if (part.error) return joinBlocks([`[Skill ${part.name} 错误]`, part.error])
  return ''
}

function subAgentTextForCopy(part: Extract<MessagePart, { kind: 'sub_agent' }>): string {
  const nested = partsTextForCopy(part.parts)
  const failure = part.failureReason ? joinBlocks(['[子代理失败原因]', part.failureReason]) : ''
  return joinBlocks([
    `[子代理: ${part.task || part.agentType || '未命名子任务'}]`,
    nested,
    failure,
  ])
}

export function partsTextForCopy(parts: MessagePart[] | undefined): string {
  if (!parts?.length) return ''
  const blocks: string[] = []
  for (const part of parts) {
    switch (part.kind) {
      case 'text':
        blocks.push(part.text || '')
        break
      case 'tool':
        blocks.push(toolTextForCopy(part))
        break
      case 'sub_agent':
        blocks.push(subAgentTextForCopy(part))
        break
      case 'question':
        blocks.push(questionTextForCopy(part))
        break
      case 'skill':
        blocks.push(skillTextForCopy(part))
        break
    }
  }
  return joinBlocks(blocks)
}

function attachmentTextForCopy(attachments: MessageAttachment[] | undefined): string {
  if (!attachments?.length) return ''
  const blocks = attachments
    .filter(attachment => attachment.text && attachment.type === 'text')
    .map(attachment => {
      const name = attachment.name?.trim()
      return joinBlocks([name ? `[附件: ${name}]` : '[附件]', attachment.text || ''])
    })
  return joinBlocks(blocks)
}

export function messageTextForCopy(message: Message): string {
  const body = message.role === 'assistant' && message.parts?.length
    ? partsTextForCopy(message.parts)
    : message.content || ''
  const attachments = attachmentTextForCopy(message.attachments)
  return joinBlocks([body, attachments])
}
