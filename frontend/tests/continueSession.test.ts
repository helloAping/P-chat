import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { pickContinueSession, type ContinueSessionInput } from '../src/utils/continueSession.ts'

const NOW = Date.parse('2026-11-04T12:00:00+08:00')

function session(partial: Partial<ContinueSessionInput> & Pick<ContinueSessionInput, 'id'>): ContinueSessionInput {
  return {
    title: partial.title ?? '标题',
    updated_at: partial.updated_at ?? 0,
    conversation_state: partial.conversation_state ?? 'active',
    has_user_messages: partial.has_user_messages ?? true,
    user_message_count: partial.user_message_count ?? 1,
    pending_turn_count: partial.pending_turn_count ?? 0,
    id: partial.id,
  }
}

test('picks the newest real session other than the open draft', () => {
  const pick = pickContinueSession([
    session({ id: 'blank', title: '(新会话)', updated_at: 200, conversation_state: 'blank', has_user_messages: false, user_message_count: 0 }),
    session({ id: 'older', title: '解释项目', updated_at: 100 }),
    session({ id: 'newer', title: '消息气泡的复制反馈', updated_at: 180 }),
  ], 'blank', NOW)

  assert.equal(pick?.id, 'newer')
  assert.equal(pick?.title, '消息气泡的复制反馈')
})

test('skips other blank drafts and the current session', () => {
  const pick = pickContinueSession([
    session({ id: 'current', title: '当前', updated_at: 300 }),
    session({ id: 'draft', title: '新会话', updated_at: 250, conversation_state: 'blank', has_user_messages: false, user_message_count: 0 }),
    session({ id: 'keep', title: '找 bug', updated_at: 100 }),
  ], 'current', NOW)

  assert.equal(pick?.id, 'keep')
})

test('returns null when the project has only drafts', () => {
  const pick = pickContinueSession([
    session({ id: 'a', title: '(无标题)', updated_at: 10, conversation_state: 'blank', has_user_messages: false, user_message_count: 0 }),
  ], 'a', NOW)
  assert.equal(pick, null)
})

test('placeholder titles become 未命名对话 and IM prefixes are stripped', () => {
  const unnamed = pickContinueSession([
    session({ id: 'blank', title: '', updated_at: 2, conversation_state: 'blank', has_user_messages: false, user_message_count: 0 }),
    session({ id: 'plain', title: '(无标题)', updated_at: 1 }),
  ], 'blank', NOW)
  assert.equal(unnamed?.title, '未命名对话')

  const im = pickContinueSession([
    session({ id: 'im:wechat:u:1', title: '微信 · 张三', updated_at: 1 }),
  ], 'blank', NOW)
  assert.equal(im?.title, '张三')
})

test('relative time uses today, yesterday, and weekday', () => {
  const today = Math.floor(Date.parse('2026-11-04T09:30:00+08:00') / 1000)
  const yesterday = Math.floor(Date.parse('2026-11-03T21:00:00+08:00') / 1000)
  const monday = Math.floor(Date.parse('2026-11-02T08:00:00+08:00') / 1000)

  assert.match(pickContinueSession([session({ id: 't', updated_at: today })], '', NOW)?.when || '', /^今天 /)
  assert.equal(pickContinueSession([session({ id: 'y', updated_at: yesterday })], '', NOW)?.when, '昨天')
  assert.equal(pickContinueSession([session({ id: 'm', updated_at: monday })], '', NOW)?.when, '周一')
})

test('empty chat renders the continue card ahead of guide cards', () => {
  const source = readFileSync(new URL('../src/components/ChatWindow.vue', import.meta.url), 'utf8')
  assert.match(source, /pickContinueSession/)
  assert.match(source, /empty-guide--continue/)
  assert.match(source, /继续：\{\{ continueSession\.title \}\}/)
  assert.match(source, /switchSession\(continueSession\.value\.id\)/)
})
