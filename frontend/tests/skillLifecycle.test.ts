import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const read = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')

test('Skill slash commands send names instead of browser-loaded instructions', () => {
  const input = read('../src/components/InputArea.vue')
  const turn = read('../src/composables/conversationTurn.ts')
  const client = read('../src/api/client.ts')

  assert.match(input, /pendingActiveSkills = \[parsed\.name\]/)
  assert.doesNotMatch(input, /api\.getSkill\(parsed\.name/)
  assert.match(input, /active_skills: activeSkills/)
  assert.match(turn, /active_skills: input\.activeSkills/)
  assert.match(client, /active_skills\?: string\[\]/)
})

test('Skill lifecycle has a structured part and an explicit visible name', () => {
  const store = read('../src/stores/chat.ts')
  const card = read('../src/components/SkillCallCard.vue')
  const bubble = read('../src/components/MessageBubble.vue')
  const client = read('../src/api/client.ts')

  assert.match(store, /case 'skill'/)
  assert.match(store, /kind: 'skill'/)
  assert.match(card, /当前调用 Skill：<span>\{\{ part\.name \}\}<\/span>/)
  assert.match(client, /status: 'start' \| 'ready' \| 'error'/)
  assert.match(bubble, /<SkillCallCard v-else-if="entry\.part\.kind === 'skill'"/)
})
