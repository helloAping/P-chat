import type { MessagePart } from '../api/client'

export type TodoStatusLike = { status?: string }

export function pendingTodoCount(todos: TodoStatusLike[] | undefined): number {
  if (!todos || todos.length === 0) return 0
  let n = 0
  for (const item of todos) {
    if (item.status === 'pending' || item.status === 'in_progress') n++
  }
  return n
}

export function humanizeStreamDropReason(reason: string): string {
  const text = (reason || '').trim()
  if (!text) return '连接中断'
  if (text.includes('no data for')) return '长时间无数据'
  if (text.includes('without done') || /stream closed/i.test(text)) return '连接被关闭'
  if (text === 'duplicate_client_message') return '连接中断，已自动续接'
  if (/failed to fetch|network/i.test(text)) return '网络中断'
  return text.length > 80 ? '传输中断' : text
}

export function buildInterruptNotice(pendingTodos: number, reason: string): string {
  const why = humanizeStreamDropReason(reason)
  if (pendingTodos > 0) {
    return `\n\n⚠ 对话连接中断（${why}）。当前还有 ${pendingTodos} 项待办未完成，任务已暂停。请发送「继续」以恢复执行。\n`
  }
  return `\n\n⚠ 对话连接中断（${why}）。本轮未正常结束，可重新发送或点「重答」。\n`
}

export function closeOpenPartsOnInterrupt(parts: MessagePart[] | undefined): number {
  if (!parts || parts.length === 0) return 0
  let closed = 0
  const walk = (items: MessagePart[]) => {
    for (const part of items) {
      if (part.kind === 'tool' && part.status === 'start') {
        part.status = 'error'
        if (!part.error) part.error = '对话中断，工具未完成'
        closed++
      } else if (part.kind === 'skill' && part.status === 'start') {
        part.status = 'error'
        if (!part.error) part.error = '对话中断，Skill 加载未完成'
        closed++
      } else if (part.kind === 'sub_agent' && part.status === 'start') {
        part.status = 'err'
        closed++
      } else if (part.kind === 'question' && (!part.question_status || part.question_status === 'open')) {
        part.question_status = 'error'
        closed++
      }
      if (part.kind === 'thinking' && part.streaming) {
        part.streaming = false
      }
      if (part.kind === 'sub_agent') walk(part.parts)
    }
  }
  walk(parts)
  return closed
}
