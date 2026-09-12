import assert from 'node:assert/strict'
import test from 'node:test'

import { messageTextForCopy } from '../src/utils/messageCopy.ts'
import type { Message } from '../src/api/client.ts'

test('messageTextForCopy includes assistant text, tool output, questions and nested sub-agent text', () => {
  const message: Message = {
    role: 'assistant',
    content: '',
    parts: [
      { kind: 'text', text: '主回复第一段' },
      {
        kind: 'tool',
        name: 'read_file',
        status: 'ok',
        result: '文件内容',
      },
      {
        kind: 'question',
        text: JSON.stringify({
          questions: [{ header: 'mode', question: '选择模式' }],
        }),
        name: JSON.stringify({ mode: '构建' }),
        question_status: 'ok',
      },
      {
        kind: 'sub_agent',
        task: '检查细节',
        status: 'ok',
        parts: [
          { kind: 'text', text: '子代理结论' },
          {
            kind: 'tool',
            name: 'grep',
            status: 'error',
            error: '没有匹配',
          },
        ],
      },
      { kind: 'text', text: '主回复第二段' },
    ],
  }

  assert.equal(
    messageTextForCopy(message),
    [
      '主回复第一段',
      '[工具 read_file 结果]',
      '文件内容',
      '[问题回答]',
      'mode: 构建',
      '[子代理: 检查细节]',
      '子代理结论',
      '[工具 grep 错误]',
      '没有匹配',
      '主回复第二段',
    ].join('\n\n'),
  )
})

test('messageTextForCopy falls back to content for legacy assistant messages', () => {
  assert.equal(
    messageTextForCopy({ role: 'assistant', content: '旧消息正文' }),
    '旧消息正文',
  )
})

test('messageTextForCopy skips the hidden successful question tool card', () => {
  const message: Message = {
    role: 'assistant',
    content: '',
    parts: [
      {
        kind: 'tool',
        name: 'question',
        status: 'ok',
        result: JSON.stringify({ answers: { mode: '构建' } }),
      },
      {
        kind: 'question',
        text: JSON.stringify({ questions: [{ header: 'mode', question: '选择模式' }] }),
        name: JSON.stringify({ mode: '构建' }),
        question_status: 'ok',
      },
    ],
  }

  assert.equal(messageTextForCopy(message), '[问题回答]\n\nmode: 构建')
})
