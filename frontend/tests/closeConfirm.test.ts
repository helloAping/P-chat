import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

function readCloseConfirmSource(): string {
  return readFileSync(new URL('../src/components/CloseConfirmModal.vue', import.meta.url), 'utf8')
}

function readSettingsSource(): string {
  return readFileSync(new URL('../src/components/AppSettingsModal.vue', import.meta.url), 'utf8')
}

test('close confirm defaults to tray and offers no-more-reminders', () => {
  const source = readCloseConfirmSource()

  // "收缩到托盘" defaults to checked unless the backend explicitly says exit.
  assert.match(source, /const minimizeToTray = ref\(true\)/)
  assert.match(source, /minimizeToTray\.value = payload\.default_action !== 'exit'/)
  // New "不再提醒" checkbox.
  assert.match(source, /const noMoreReminders = ref\(false\)/)
  assert.match(source, /不再提醒/)
  // Checking no-more calls the in-memory SetNoMoreConfirm binding — NOT a
  // config PATCH (no persistence; the flag dies with the process).
  assert.match(source, /app\.SetNoMoreConfirm\(\)/)
  assert.doesNotMatch(source, /api\.updateSystemConfig/)
})

test('settings window behavior keeps only the close behavior toggle', () => {
  const source = readSettingsSource()

  assert.match(source, /const sysCloseBehavior = ref<'exit' \| 'tray'>\('exit'\)/)
  assert.doesNotMatch(source, /sysCloseConfirmSkip/)
  assert.doesNotMatch(source, /关闭时不再弹窗确认/)
})
