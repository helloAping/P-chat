import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import {
  byteSizeInputMinimum,
  bytesToUnitValue,
  preferredByteSizeUnit,
  unitValueToBytes,
} from '../src/utils/byteSize.ts'

const settings = readFileSync(new URL('../src/components/AppSettingsModal.vue', import.meta.url), 'utf8')
const input = readFileSync(new URL('../src/components/InputArea.vue', import.meta.url), 'utf8')
const store = readFileSync(new URL('../src/stores/chat.ts', import.meta.url), 'utf8')

test('model editor exposes context presets and multi-select input capabilities', () => {
  assert.match(settings, /label: '256K', value: 256_000/)
  assert.match(settings, /label: '1M', value: 1_000_000/)
  assert.match(settings, /v-model:value="editModelCapabilities"[\s\S]*?multiple/)
  assert.match(settings, /supports_vision: editModelCapabilities\.value\.includes\('image'\)/)
})

test('system and session settings expose independent media capabilities', () => {
  assert.match(settings, /kind: 'image'[\s\S]*?kind: 'video'[\s\S]*?kind: 'audio'/)
  assert.match(settings, /patch\.recognition = \{ routes: sysRecognition\.value \}/)
  assert.match(input, /enabled_recognition_capabilities: value/)
  assert.match(input, /<span>媒体识别<\/span>[\s\S]*?data-testid="session-recognition-dropdown"/)
})

test('media generation uses app defaults with per-session hard switches', () => {
  assert.match(settings, /value: 'media_generation'/)
  assert.match(settings, /operation: 'text_to_image'[\s\S]*?operation: 'image_to_video'[\s\S]*?operation: 'text_to_speech'/)
  assert.match(settings, /patch\.generation = \{[\s\S]*?defaults: generationDefaults/)
  assert.match(input, /enabled_generation_operations: value/)
  assert.match(input, /关闭后，[\s\S]*?工具内部也会直接拒绝/)
  assert.match(input, /会话只选择能力；每项能力直接使用应用设置中的默认模型/)
  assert.doesNotMatch(input, /generation_model_overrides|generation_prompt_assist|能力路由|提示词处理/)
})

test('session media multi-selects share one compact grid row and show counts by their labels', () => {
  assert.match(input, /session-config-options--stacked \{[\s\S]*?flex-wrap: nowrap/)
  assert.match(input, /<span>媒体识别<\/span>\s*<span v-if="enabledRecognitionCapabilities\.length" class="session-config-count">/)
  assert.match(input, /<span>媒体生成<\/span>\s*<span v-if="enabledGenerationOperations\.length" class="session-config-count">/)
  assert.match(input, /data-testid="session-recognition-dropdown"[\s\S]*?opt-pick-label/)
  assert.match(input, /data-testid="session-generation-dropdown"[\s\S]*?opt-pick-label/)
  assert.doesNotMatch(input, /session-config-row--media-recognition|opt-pick--wide/)
  assert.match(input, /\.session-config-count \{[\s\S]*?border-radius: var\(--radius-pill\)/)
})

test('media limit editor supports readable units without changing the byte API', () => {
  assert.equal(preferredByteSizeUnit(10 * 1024 * 1024), 'MB')
  assert.equal(bytesToUnitValue(10 * 1024 * 1024, 'MB'), 10)
  assert.equal(unitValueToBytes(1.5, 'MB'), 1_572_864)
  assert.equal(unitValueToBytes(512, 'KB'), 524_288)
  assert.equal(byteSizeInputMinimum('GB'), 0.001)
  assert.match(settings, /byteSizeUnitOptions/)
  assert.match(settings, /onRecognitionSizeUpdate\(capability\.kind, value\)/)
  assert.match(settings, /patch\.recognition = \{ routes: sysRecognition\.value \}/)
})

test('media size input and unit picker are not nested in one native label', () => {
  assert.match(
    settings,
    /<div class="recognition-route-field">\s*<span>最大文件<\/span>\s*<div class="media-size-input">/,
  )
  assert.doesNotMatch(
    settings,
    /<label class="recognition-route-field">\s*<span>最大文件<\/span>/,
  )
})

test('new sessions inherit style only when an active session exists', () => {
  assert.match(store, /const inheritedStyle = state\.currentID[\s\S]*?\? \(state\.sessionMeta\[state\.currentID\]\?\.style \|\| 'off'\)[\s\S]*?: 'off'/)
  assert.match(store, /style: inheritedStyle/)
})
