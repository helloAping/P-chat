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

test('new session action reuses the current project blank draft before posting', () => {
  const source = readFileSync(new URL('../src/stores/chat.ts', import.meta.url), 'utf8')
  const match = source.match(/export async function createSession\(\): Promise<string> \{([\s\S]*?)\n\}/)
  assert.ok(match, 'createSession should exist')
  const body = match[1]
  const reuseIndex = body.indexOf('const reusableBlank = state.sessions.find(isBlankSessionRecord)')
  const postIndex = body.indexOf('api.createSession(buildCreateSessionOptions())')

  assert.ok(reuseIndex >= 0, 'createSession should look for a reusable blank session first')
  assert.ok(postIndex >= 0, 'createSession should still POST when no blank session exists')
  assert.ok(reuseIndex < postIndex, 'blank reuse must happen before creating a new session')
  assert.match(body, /await switchSession\(reusableBlank\.id\)/)
})
