import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

function readChatStore(): string {
  return readFileSync(new URL('../src/stores/chat.ts', import.meta.url), 'utf8')
}

function readChatWindow(): string {
  return readFileSync(new URL('../src/components/ChatWindow.vue', import.meta.url), 'utf8')
}

test('switchSession shows a loading veil before swapping transcripts', () => {
  const source = readChatStore()
  assert.match(source, /viewLoading:\s*false/)
  assert.match(source, /acquireViewLoad\('正在加载对话…'\)/)
  assert.match(source, /VIEW_LOAD_MIN_CACHED_MS/)
  assert.match(source, /id === state\.currentID && state\.sessionMessages\[id\] && !state\.viewLoading/)
})

test('project switch holds the loading veil across session hydrate', () => {
  const source = readChatStore()
  const match = source.match(/export async function setActiveProject\(path: string\) \{([\s\S]*?)\n\}/)
  assert.ok(match, 'setActiveProject should exist')
  const body = match[1]
  assert.match(body, /if \(path === state\.activeProjectPath\) return/)
  assert.match(body, /acquireViewLoad\('正在切换项目…'\)/)
  assert.match(body, /releaseViewLoad\(startedAt, VIEW_LOAD_MIN_PROJECT_MS\)/)
  assert.equal(body.includes('.abort()'), false)
  assert.equal(body.includes('delete state.streaming'), false)
})

test('chat pane reveals with overlay instead of empty-state flash', () => {
  const source = readChatWindow()
  assert.match(source, /class="messages-pane"/)
  assert.match(source, /messages--switching/)
  assert.match(source, /v-if="state\.viewLoading"/)
  assert.match(source, /v-if="!state\.viewLoading && currentMessages\.length === 0"/)
  assert.match(source, /view-loading-spin/)
})
