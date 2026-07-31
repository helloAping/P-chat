import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

function readStoreSource(): string {
  return readFileSync(new URL('../src/stores/chat.ts', import.meta.url), 'utf8')
}

function readInputAreaSource(): string {
  return readFileSync(new URL('../src/components/InputArea.vue', import.meta.url), 'utf8')
}

test('rollback keeps a local fallback for undo banner and input refill', () => {
  const source = readStoreSource()

  assert.match(source, /export async function rollbackTo\(sessionId: string, messageIndex: number\)/)
  assert.match(source, /const localDeleted = msgs\.slice\(messageIndex\)/)
  assert.match(source, /result\.deleted_messages\?\.length/)
  assert.match(source, /result\.deleted_count > 0 \? localDeleted/)
  assert.match(source, /state\.rollbackUndo\[sessionId\] = \{[\s\S]*messages: deletedMessages/)
  assert.match(source, /\[\.\.\.deletedMessages\]\.reverse\(\)\.find\(m => m\.role === 'user'\)/)
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
