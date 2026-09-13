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

test('top bar collapse button opens the session navigation', () => {
  const source = readFileSync(new URL('../src/components/TopBar.vue', import.meta.url), 'utf8')

  assert.match(source, /class="collapse-btn"[\s\S]*?@click="toggleSidebar"/)
  assert.match(source, /:aria-label="props\.collapsed \? '展开侧边栏' : '收起侧边栏'"/)
  assert.doesNotMatch(source, /<BrandLogo|class="brand"/)
})

test('sidebar search stays scoped to the active project', () => {
  const sidebar = readFileSync(new URL('../src/components/SessionSidebar.vue', import.meta.url), 'utf8')
  const client = readFileSync(new URL('../src/api/client.ts', import.meta.url), 'utf8')

  assert.match(sidebar, /api\.searchMessages\(q,\s*15,\s*state\.activeProjectPath\)/)
  assert.match(client, /projectPath\?: string/)
  assert.match(client, /params\.set\('project_path',\s*projectPath\)/)
})
