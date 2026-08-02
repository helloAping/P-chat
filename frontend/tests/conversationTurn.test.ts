import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

function readTurnSource(): string {
  return readFileSync(new URL('../src/composables/conversationTurn.ts', import.meta.url), 'utf8')
}

test('conversation turn owns stream dispatch, completion, and recovery', () => {
  const source = readTurnSource()

  assert.match(source, /export async function submitConversationTurn/)
  assert.match(source, /startStream\(input\.sessionId, ctrl\)/)
  assert.match(source, /await api\.streamMessagesRetry\(input\.sessionId/)
  assert.match(source, /appendStreamEvent\(input\.sessionId, event\)/)
  assert.match(source, /endStream\(input\.sessionId, ctrl\)/)
  assert.match(source, /recoverMissingParts\(input\.sessionId, drop\.lastSeq, drop\.reason\)/)
})

test('input delegates chat streaming to the conversation turn seam', () => {
  const source = readFileSync(new URL('../src/components/InputArea.vue', import.meta.url), 'utf8')

  assert.match(source, /import \{ stopConversationTurn, submitConversationTurn \} from '\.\.\/composables\/conversationTurn'/)
  assert.match(source, /await submitConversationTurn\(\{[\s\S]*?sessionId: id/)
  assert.match(source, /stopConversationTurn\(state\.currentID\)/)
})
