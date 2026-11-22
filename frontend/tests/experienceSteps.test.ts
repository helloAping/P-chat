import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { galleryMedia } from '../src/utils/attachmentArtifacts.ts'
import {
  buildGlobalCommands,
  directShortcut,
  filterGlobalCommands,
  isCommandPaletteShortcut,
} from '../src/utils/globalCommands.ts'
import { buildBookmarkMarks, buildMinimapMarks } from '../src/utils/sessionMinimap.ts'
import { buildTurnSummary, resolveTurnPath, turnFileLabel } from '../src/utils/turnSummary.ts'
import type { Message } from '../src/api/client.ts'

test('command palette shortcut ignores shifted and composing keys', () => {
  assert.equal(isCommandPaletteShortcut({ key: 'k', ctrlKey: true, metaKey: false, altKey: false, shiftKey: false }), true)
  assert.equal(isCommandPaletteShortcut({ key: 'K', ctrlKey: false, metaKey: true, altKey: false, shiftKey: false }), true)
  assert.equal(isCommandPaletteShortcut({ key: 'k', ctrlKey: true, metaKey: false, altKey: false, shiftKey: true }), false)
  assert.equal(isCommandPaletteShortcut({ key: 'k', ctrlKey: true, metaKey: false, altKey: false, shiftKey: false, isComposing: true }), false)
})

test('direct shortcuts are Ctrl+N and Ctrl+B, and ignore composing', () => {
  assert.equal(directShortcut({ key: 'n', ctrlKey: true, metaKey: false, altKey: false, shiftKey: false }), 'new-chat')
  assert.equal(directShortcut({ key: 'b', ctrlKey: false, metaKey: true, altKey: false, shiftKey: false }), 'sidebar')
  assert.equal(directShortcut({ key: 'n', ctrlKey: true, metaKey: false, altKey: false, shiftKey: false, isComposing: true }), null)
  assert.equal(directShortcut({ key: 'k', ctrlKey: true, metaKey: false, altKey: false, shiftKey: false }), null)
})

test('command palette filters and disables continue when there is no prior chat', () => {
  const commands = buildGlobalCommands({
    sidebarCollapsed: false,
    inspectorOpen: true,
    theme: 'dark',
    continueTitle: null,
  })
  assert.equal(commands.find(item => item.id === 'continue')?.enabled, false)
  assert.equal(commands.find(item => item.id === 'sidebar')?.label, '折叠侧栏')
  assert.equal(commands.find(item => item.id === 'theme')?.label, '切换到浅色')
  assert.deepEqual(filterGlobalCommands(commands, '检查').map(item => item.id), ['inspector'])
})

test('bookmark marks stay off the rail when there are no pins', () => {
  assert.deepEqual(buildBookmarkMarks([], [1, 2]), [])
  const marks = buildBookmarkMarks(
    [{ messageId: 9, seq: 4, tip: '这里' }],
    [1, 8],
  )
  assert.equal(marks.length, 1)
  assert.equal(marks[0]?.kind, 'bookmark')
  assert.equal(marks[0]?.messageId, 9)
})

test('turn summary lists written paths and skips dry runs', () => {
  const message: Message = {
    role: 'assistant',
    content: '',
    parts: [
      { kind: 'tool', name: 'edit_file', status: 'ok', args: JSON.stringify({ path: 'frontend/src/App.vue' }) },
      { kind: 'tool', name: 'write_file', status: 'ok', args: JSON.stringify({ path: 'frontend/src/App.vue', dry_run: true }) },
      {
        kind: 'tool',
        name: 'apply_patch',
        status: 'ok',
        args: JSON.stringify({
          patch: '*** Begin Patch\n*** Update File: internal/tool/edit.go\n*** End Patch\n',
          operations: [{ op: 'add', path: 'notes/a.md' }],
        }),
      },
    ],
  }
  const summary = buildTurnSummary(message)
  assert.deepEqual(summary.files, ['frontend/src/App.vue', 'notes/a.md', 'internal/tool/edit.go'])
  assert.equal(summary.filesWritten, 3)
  assert.equal(turnFileLabel('frontend/src/App.vue'), 'App.vue')
  assert.equal(resolveTurnPath('frontend/src/App.vue', 'D:\\develop\\project\\P-chat'), 'D:\\develop\\project\\P-chat\\frontend\\src\\App.vue')
  assert.equal(resolveTurnPath('D:\\tmp\\a.go', 'D:\\develop\\project\\P-chat'), 'D:\\tmp\\a.go')
})

test('minimap ticks only user turns, longer for earlier ones', () => {
  const none: Message[] = [
    { id: 2, role: 'assistant', content: 'ok', parts: [{ kind: 'tool', name: 'read_file', status: 'ok' }] },
  ]
  assert.deepEqual(buildMinimapMarks(none), [])

  const one: Message[] = [
    { id: 1, role: 'user', content: 'hi' },
    { id: 2, role: 'assistant', content: 'ok' },
  ]
  assert.equal(buildMinimapMarks(one).length, 1)
  assert.equal(buildMinimapMarks(one)[0]?.span, 1)

  const messages: Message[] = []
  for (let i = 1; i <= 8; i++) {
    if (i % 2 === 1) messages.push({ id: i, role: 'user', content: `ask ${i}` })
    else {
      messages.push({
        id: i,
        role: 'assistant',
        content: '',
        parts: [{ kind: 'tool', name: 'read_file', status: i === 4 ? 'error' : 'ok' }],
      })
    }
  }
  const marks = buildMinimapMarks(messages)
  assert.deepEqual(marks.map(mark => mark.messageId), [1, 3, 5, 7])
  assert.equal(marks[0]?.tip, 'ask 1')
  assert.equal(marks[0]?.span, 1)
  assert.equal(marks.at(-1)?.span, 0)
  assert.ok((marks[0]?.span ?? 0) > (marks[1]?.span ?? 0))
  assert.equal(marks[0]?.position, 0)
  assert.ok((marks.at(-1)?.position ?? 0) < 1)
})

test('gallery keeps previewable images and playable audio', () => {
  const media = galleryMedia([
    { key: 'img', source: 'generation', kind: 'image', url: 'upl://1' },
    { key: 'no-url', source: 'generation', kind: 'image' },
    { key: 'audio', source: 'generation', kind: 'audio', url: 'upl://2' },
    { key: 'file', source: 'upload', kind: 'file', url: 'upl://3' },
  ])
  assert.deepEqual(media.images.map(item => item.key), ['img'])
  assert.deepEqual(media.audio.map(item => item.key), ['audio'])
})

test('shell wires the four experience surfaces', () => {
  const app = readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8')
  const bubble = readFileSync(new URL('../src/components/MessageBubble.vue', import.meta.url), 'utf8')
  const chat = readFileSync(new URL('../src/components/ChatWindow.vue', import.meta.url), 'utf8')
  const inspector = readFileSync(new URL('../src/components/InspectorPanel.vue', import.meta.url), 'utf8')

  assert.match(app, /GlobalCommandPalette/)
  assert.match(app, /isCommandPaletteShortcut/)
  assert.match(bubble, /openTurnFile/)
  assert.match(bubble, /msg-turn-files/)
  assert.match(chat, /buildMinimapMarks/)
  assert.match(chat, /class="minimap"/)
  assert.match(inspector, /会话画廊/)
  assert.match(inspector, /galleryMedia/)
})
