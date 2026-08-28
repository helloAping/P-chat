import assert from 'node:assert/strict'
import test from 'node:test'

import type { MessagePart, SubAgentPart } from '../src/api/client.ts'
import { insertAsyncSubAgentAfterTaskTools } from '../src/utils/subAgentOrder.ts'

test('insertAsyncSubAgentAfterTaskTools inserts cards after task tools in task call order', () => {
  const parts: MessagePart[] = [
    { kind: 'thinking', text: 'launch work' },
    { kind: 'tool', name: 'task', status: 'ok', args: `{"description":"first task"}`, result: 'sub-agent launched in background: task_id=task-a, status=running' },
    { kind: 'tool', name: 'task', status: 'ok', args: `{"description":"second task"}`, result: 'sub-agent launched in background: task_id=task-b, status=running' },
    { kind: 'thinking', text: 'wait for completion' },
    { kind: 'tool', name: 'task_wait', status: 'ok' },
  ]

  insertAsyncSubAgentAfterTaskTools(parts, asyncSub('task-b', 'second task'))
  insertAsyncSubAgentAfterTaskTools(parts, asyncSub('task-a', 'first task'))

  assert.deepEqual(parts.map(partLabel), [
    'thinking',
    'tool:task',
    'tool:task',
    'sub:task-a',
    'sub:task-b',
    'thinking',
    'tool:task_wait',
  ])
})

test('insertAsyncSubAgentAfterTaskTools falls back to appending when no task tool exists', () => {
  const parts: MessagePart[] = [{ kind: 'thinking', text: 'no task yet' }]

  insertAsyncSubAgentAfterTaskTools(parts, asyncSub('task-a', 'first task'))

  assert.deepEqual(parts.map(partLabel), ['thinking', 'sub:task-a'])
})

function asyncSub(taskId: string, task: string): SubAgentPart {
  return {
    kind: 'sub_agent',
    task,
    taskId,
    runMode: 'async',
    status: 'start',
    parts: [],
  }
}

function partLabel(part: MessagePart): string {
  if (part.kind === 'tool') return `tool:${part.name}`
  if (part.kind === 'sub_agent') return `sub:${part.taskId || part.task}`
  return part.kind
}
