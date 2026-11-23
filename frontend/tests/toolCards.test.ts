import assert from 'node:assert/strict'
import test from 'node:test'
import type { MessagePart } from '../src/api/client.ts'
import { findOpenToolCard, settleOpenToolCards } from '../src/utils/toolCards.ts'

test('a later start reuses the preview card even when another tool is newer', () => {
  const parts: MessagePart[] = [
    { kind: 'tool', name: 'exec_command', tool_id: 'a', args: '{"command":"git blame -L 381,438"}', status: 'start' },
    { kind: 'tool', name: 'exec_command', tool_id: 'b', args: '{"command":"git blame -L 439,500"}', status: 'start' },
  ]
  const card = findOpenToolCard(parts, 'a', 'exec_command')
  assert.equal(card?.tool_id, 'a')
  if (card) card.args = '{"command":"git blame -L 381,438 internal/service/software.go"}'
  assert.equal(parts.length, 2)
  assert.equal(parts[0].kind === 'tool' && parts[0].status, 'start')
})

test('a finished call closes the preview and drops the duplicate spinner', () => {
  const parts: MessagePart[] = [
    { kind: 'tool', name: 'grep', tool_id: 'g', args: '{"pattern":"func "}', status: 'start' },
    { kind: 'tool', name: 'exec_command', tool_id: 'e', args: 'partial', status: 'start' },
    { kind: 'tool', name: 'grep', tool_id: 'g', args: '{"pattern":"func ","path":"internal/service/software.go"}', status: 'start' },
  ]
  const settled = settleOpenToolCards(parts, {
    tool_id: 'g',
    tool_name: 'grep',
    tool_status: 'ok',
    tool_elapsed: '2ms',
  }, (card) => {
    card.status = 'ok'
    card.elapsed = '2ms'
  })
  assert.equal(settled, true)
  assert.equal(parts.length, 2)
  assert.equal(parts[0].kind === 'tool' && parts[0].status, 'ok')
  assert.equal(parts[1].kind === 'tool' && parts[1].tool_id, 'e')
  assert.equal(parts[1].kind === 'tool' && parts[1].status, 'start')
})
