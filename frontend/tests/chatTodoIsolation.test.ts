import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

function readChatStore(): string {
  return readFileSync(new URL('../src/stores/chat.ts', import.meta.url), 'utf8')
}

test('todo_write updates the main todo panel only for top-level tool events', () => {
  const source = readChatStore()

  assert.match(
    source,
    /ev\.tool_name === 'todo_write' && ev\.tool_status === 'ok' && !ev\.sub_agent/,
    'sub-agent todo_write events must stay private to the nested card',
  )
  assert.match(source, /state\.sessionTodos\[id\] = todos/)
})
