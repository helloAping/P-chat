import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

function readSetActiveProjectBody(): string {
  const source = readFileSync(new URL('../src/stores/chat.ts', import.meta.url), 'utf8')
  const match = source.match(/export async function setActiveProject\(path: string\) \{([\s\S]*?)\n\}/)
  assert.ok(match, 'setActiveProject should exist')
  return match[1]
}

test('project switch does not abort or delete active streams', () => {
  const body = readSetActiveProjectBody()

  assert.equal(body.includes('.abort()'), false)
  assert.equal(body.includes('delete state.streaming'), false)
  assert.equal(body.includes('state.streaming ='), false)
})

test('collapsed top bar brand opens the session navigation', () => {
  const source = readFileSync(new URL('../src/components/TopBar.vue', import.meta.url), 'utf8')

  assert.match(source, /v-if="props\.collapsed"[\s\S]*?@click="toggleSidebar"/)
  assert.match(source, /aria-label="打开会话列表"/)
})
