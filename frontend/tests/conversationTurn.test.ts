import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

function readTurnSource(): string {
  return readFileSync(new URL('../src/composables/conversationTurn.ts', import.meta.url), 'utf8')
}

test('conversation turn owns stream dispatch, completion, and recovery', () => {
  const source = readTurnSource()

  assert.match(source, /export async function submitConversationTurn\(input: ConversationTurnInput\): Promise<boolean>/)
  assert.match(source, /startStream\(input\.sessionId, ctrl\)/)
  assert.match(source, /await api\.streamMessagesRetry\(input\.sessionId/)
  assert.match(source, /appendStreamEvent\(input\.sessionId, event\)/)
  assert.match(source, /endStream\(input\.sessionId, ctrl\)/)
  assert.match(source, /recoverMissingParts\(input\.sessionId, drop\.lastSeq, drop\.reason\)/)
  assert.match(source, /if \(drop && !ctrl\.signal\.aborted\)/)
  assert.match(source, /streamReturned = !ctrl\.signal\.aborted/)
  assert.match(source, /return !ctrl\.signal\.aborted/)
})

test('input delegates chat streaming to the conversation turn seam', () => {
  const source = readFileSync(new URL('../src/components/InputArea.vue', import.meta.url), 'utf8')

  assert.match(source, /import \{ drainQueuedConversationTurns, stopConversationTurn, submitConversationTurn \} from '\.\.\/composables\/conversationTurn'/)
  assert.match(source, /await submitConversationTurn\(\{[\s\S]*?sessionId: id/)
  assert.match(source, /stopConversationTurn\(state\.currentID\)/)
})

test('conversation turn drains queued turns after a completed stream', () => {
  const source = readTurnSource()

  assert.match(source, /export async function drainQueuedConversationTurns\(sessionId: string\)/)
  assert.match(source, /isSessionWorking/)
  assert.match(source, /const drainingSessions = new Set<string>\(\)/)
  assert.match(source, /streamReturned && !drainingSessions\.has\(input\.sessionId\)/)
  assert.match(source, /if \(state\.streaming\[sessionId\] \|\| isSessionWorking\(sessionId\)\) return/)
  assert.match(source, /if \(state\.streaming\[sessionId\] \|\| isSessionWorking\(sessionId\)\) break/)
  assert.match(source, /await claimNextQueuedTurnForDrain\(sessionId\)/)
  assert.match(source, /appendLocalUserMessage\(sessionId/)
  assert.match(source, /const completed = await submitConversationTurn/)
  assert.match(source, /if \(!completed\) \{[\s\S]*?await failQueuedTurn\(sessionId, item\.id, 'queued turn was stopped by the user'\)/)
  assert.match(source, /await completeQueuedTurn\(sessionId, item\.id\)/)
  assert.match(source, /await failQueuedTurn\(sessionId, item\.id/)
  assert.match(source, /function isQueueClaimTemporarilyBlocked\(error: unknown\)/)
  assert.match(source, /scheduleQueueDrainRetry\(sessionId\)/)
})
