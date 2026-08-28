import assert from 'node:assert/strict'
import test from 'node:test'

import type { MessagePart } from '../src/api/client.ts'
import {
  buildInterruptNotice,
  closeOpenPartsOnInterrupt,
  humanizeStreamDropReason,
  pendingTodoCount,
} from '../src/utils/streamInterrupt.ts'

test('pendingTodoCount only counts unfinished items', () => {
  assert.equal(pendingTodoCount(undefined), 0)
  assert.equal(pendingTodoCount([]), 0)
  assert.equal(pendingTodoCount([
    { status: 'done' },
    { status: 'cancelled' },
    { status: 'pending' },
    { status: 'in_progress' },
  ]), 2)
})

test('buildInterruptNotice names unfinished todos so the user is not left guessing', () => {
  const withTodos = buildInterruptNotice(3, 'no data for 150000ms')
  assert.match(withTodos, /对话连接中断/)
  assert.match(withTodos, /3 项待办未完成/)
  assert.match(withTodos, /继续/)

  const withoutTodos = buildInterruptNotice(0, 'stream closed without done')
  assert.match(withoutTodos, /对话连接中断/)
  assert.match(withoutTodos, /未正常结束/)
  assert.doesNotMatch(withoutTodos, /待办/)
})

test('humanizeStreamDropReason maps transport tokens to readable Chinese', () => {
  assert.equal(humanizeStreamDropReason('no data for 150000ms (turn may be stuck)'), '长时间无数据')
  assert.equal(humanizeStreamDropReason('stream closed without done'), '连接被关闭')
  assert.equal(humanizeStreamDropReason('duplicate_client_message'), '连接中断，已自动续接')
})

test('closeOpenPartsOnInterrupt force-closes running tools and sub-agents', () => {
  const parts: MessagePart[] = [
    { kind: 'tool', name: 'exec_command', status: 'start' },
    { kind: 'tool', name: 'read_file', status: 'ok', result: 'ok' },
    {
      kind: 'sub_agent',
      task: 'explore',
      status: 'start',
      parts: [{ kind: 'tool', name: 'grep', status: 'start' }],
    },
    { kind: 'thinking', text: '…', streaming: true },
  ]

  const closed = closeOpenPartsOnInterrupt(parts)
  assert.equal(closed, 3)
  assert.equal(parts[0].kind === 'tool' && parts[0].status, 'error')
  assert.equal(parts[1].kind === 'tool' && parts[1].status, 'ok')
  assert.equal(parts[2].kind === 'sub_agent' && parts[2].status, 'err')
  assert.equal(
    parts[2].kind === 'sub_agent' && parts[2].parts[0].kind === 'tool' && parts[2].parts[0].status,
    'error',
  )
  assert.equal(parts[3].kind === 'thinking' && parts[3].streaming, false)
})
