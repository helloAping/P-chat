import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

function readStoreSource(): string {
  return readFileSync(new URL('../src/stores/chat.ts', import.meta.url), 'utf8')
}

function readInputAreaSource(): string {
  return readFileSync(new URL('../src/components/InputArea.vue', import.meta.url), 'utf8')
}

function readClientSource(): string {
  return readFileSync(new URL('../src/api/client.ts', import.meta.url), 'utf8')
}

function readConversationTurnSource(): string {
  return readFileSync(new URL('../src/composables/conversationTurn.ts', import.meta.url), 'utf8')
}

function readMessageBubbleSource(): string {
  return readFileSync(new URL('../src/components/MessageBubble.vue', import.meta.url), 'utf8')
}

test('message right-click copy preserves and copies the selected text', () => {
  const source = readMessageBubbleSource()

  assert.match(source, /const messageContextMenuSelection = ref\(''\)/)
  assert.match(source, /messageContextMenuSelection\.value = window\.getSelection\(\)\?\.toString\(\) \?\? ''/)
  assert.match(source, /await copyText\(messageContextMenuSelection\.value\)/)
  assert.match(source, /await copyEntireMessage\(\)/)
})

test('rollback keeps a local fallback for undo banner and input refill', () => {
  const source = readStoreSource()

  assert.match(source, /export async function rollbackTo\(sessionId: string, messageIndex: number\)/)
  assert.match(source, /const localDeleted = msgs\.slice\(messageIndex\)/)
  assert.match(source, /result\.deleted_messages\?\.length/)
  assert.match(source, /result\.deleted_count > 0 \? localDeleted/)
  assert.match(source, /state\.rollbackUndo\[sessionId\] = \{[\s\S]*messages: deletedMessages/)
  assert.match(source, /\[\.\.\.deletedMessages\]\.reverse\(\)\.find\(m => m\.role === 'user'\)/)
})

test('input send queues messages while current session streams', () => {
  const source = readInputAreaSource()
  const match = source.match(/async function send\(\) \{([\s\S]*?)\n  if \(isSlashLine\(\)\)/)
  assert.ok(match, 'send() should exist and reach slash-command handling')

  assert.doesNotMatch(match[1], /if \(isStreaming\.value\) \{[\s\S]*?return[\s\S]*?\}/)
  assert.match(source, /const currentConversationBusy = computed\(\(\) =>[\s\S]*?!!state\.turnQueueDraining\[state\.currentID\]/)
  assert.match(source, /currentSessionWorking\.value/)
  assert.match(source, /const shouldQueue = currentConversationBusy\.value \|\| currentTurnQueue\.value\.length > 0/)
  assert.match(source, /await enqueueTurnQueue\(id, turnPayload\)/)
  assert.match(source, /消息已加入队列/)
  assert.match(source, /:title="currentConversationBusy \? '加入队列 \(Enter\)' : '发送 \(Enter\)'"/)
})

test('input confirms and clears unfinished todos before sending a new message', () => {
  const source = readInputAreaSource()

  assert.match(source, /useDialog\(\)/)
  assert.match(source, /hasUnfinishedTodos/)
  assert.match(source, /await api\.clearTodos\(id\)/)
  assert.match(source, /state\.sessionTodos\[id\] = \[\]/)
	assert.match(source, /if \(!selectedMode\) return/)
	assert.match(source, /todoMode = selectedMode/)
	assert.match(source, /return 'resume'/)
	assert.match(source, /return 'clear'/)
  assert.match(source, /sendPreflightSessions\.has\(preflightSessionID\)/)
  assert.match(source, /state\.currentID !== preflightSessionID/)
})

test('initial session load preserves messages created while history is in flight', () => {
  const source = readStoreSource()

  assert.match(
    source,
    /const liveMessages = \(state\.sessionMessages\[id\] as Message\[\] \| undefined\) \?\? \[\][\s\S]*?state\.sessionMessages\[id\] = \[\.\.\.history, \.\.\.liveMessages\]/,
  )
})

test('turn queue view only shows waiting or failed items', () => {
  const source = readStoreSource()

  const match = source.match(/export const currentTurnQueue = computed\(\(\) =>([\s\S]*?)\n\)/)
  assert.ok(match, 'currentTurnQueue computed should exist')
  assert.match(match[1], /item\.status === 'queued'/)
  assert.match(match[1], /item\.status === 'failed'/)
  assert.doesNotMatch(match[1], /item\.status === 'running'/)
})

test('queued messages can be edited before they are claimed', () => {
  const client = readClientSource()
  const store = readStoreSource()
  const input = readInputAreaSource()
  const conversationTurn = readConversationTurnSource()

  assert.match(client, /export const editTurnQueueItem = \(sessionId: string, queueId: number, message: string\)/)
  assert.match(client, /method: 'PATCH', body: JSON\.stringify\(\{ message \}\)/)
  assert.match(store, /export async function editQueuedTurn\(sessionId: string, queueId: number, message: string\)/)
  assert.match(store, /turnQueueEditing: \{\} as Record<string, boolean>/)
  assert.match(store, /export function setTurnQueueEditing\(sessionId: string, editing: boolean\)/)
  assert.match(input, /function startEditingQueuedTurn\(item: api\.TurnQueueItem\)/)
  assert.match(input, /if \(item\.status !== 'queued'\) return/)
  assert.doesNotMatch(input, /item\.status !== 'queued' \|\| queueDraining\.value/)
  assert.match(input, /setTurnQueueEditing\(item\.session_id, true\)/)
  assert.match(input, /await editQueuedTurn\(sessionID, editingQueueId\.value, messageText\)/)
  assert.match(input, /aria-label="编辑排队消息"/)
  assert.match(input, /aria-label="保存排队消息"/)
  assert.match(conversationTurn, /if \(state\.turnQueueEditing\[sessionId\]\) return/)
  assert.match(conversationTurn, /while \(hasQueuedTurns\(sessionId\)[\s\S]*?if \(state\.turnQueueEditing\[sessionId\]\) break[\s\S]*?claimNextQueuedTurnForDrain/)
})

test('session working state includes background work for queue gating', () => {
  const source = readStoreSource()

  assert.match(source, /export function isSessionWorking\(id: string\): boolean/)
  assert.match(source, /state\.sessionBackgroundSubAgentJobs\[id\]/)
  assert.match(source, /state\.sessionBackgroundHookMerging\[id\]/)
  assert.match(source, /state\.isRecovering\[id\]/)
  assert.match(source, /export const currentSessionWorking = computed\(\(\) => isSessionWorking\(state\.currentID\)\)/)
})

test('input textarea has a manual right-click edit menu with feedback actions', () => {
  const source = readInputAreaSource()

  assert.match(source, /const inputContextMenuOptions: DropdownOption\[\]/)
  assert.match(source, /key: 'copy', label: '复制'/)
  assert.match(source, /key: 'cut', label: '剪切'/)
  assert.match(source, /key: 'paste', label: '粘贴'/)
  assert.match(source, /key: 'select_all', label: '全选'/)
  assert.match(source, /@contextmenu="onInputContextMenu"/)
  assert.match(source, /message\.success\('复制成功'\)/)
  assert.match(source, /message\.success\('粘贴成功'\)/)
  assert.match(source, /message\.success\('剪切成功'\)/)
})
