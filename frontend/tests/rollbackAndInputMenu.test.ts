import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

function readStoreSource(): string {
  return readFileSync(new URL('../src/stores/chat.ts', import.meta.url), 'utf8')
}

function readInputAreaSource(): string {
  return readFileSync(new URL('../src/components/InputArea.vue', import.meta.url), 'utf8')
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

test('input send ignores duplicate sends while current session streams', () => {
  const source = readInputAreaSource()
  const match = source.match(/async function send\(\) \{([\s\S]*?)\n  if \(isSlashLine\(\)\)/)
  assert.ok(match, 'send() should exist and reach slash-command handling')

  assert.match(match[1], /if \(isStreaming\.value\) \{[\s\S]*?return[\s\S]*?\}/)
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
