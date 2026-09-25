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
const app = readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8')
const toolCard = readFileSync(new URL('../src/components/ToolCallCard.vue', import.meta.url), 'utf8')
const attachmentArtifacts = readFileSync(new URL('../src/utils/attachmentArtifacts.ts', import.meta.url), 'utf8')

test('model editor exposes context presets and multi-select input capabilities', () => {
  assert.match(settings, /label: '256K', value: 256_000/)
  assert.match(settings, /label: '1M', value: 1_000_000/)
  assert.match(settings, /NCheckboxGroup v-model:value="editModelCapabilities"/)
  assert.match(settings, /capability-option-grid--llm/)
  assert.match(settings, /supports_vision: editModelCapabilities\.value\.includes\('image'\)/)
  assert.match(settings, /媒体识别能力（可选）/)
  assert.match(settings, /默认：仅文本/)
  assert.match(settings, /图片识别[\s\S]*?视频识别[\s\S]*?音频识别/)
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

test('session generation capabilities refresh after application configuration changes', () => {
  assert.match(store, /generationConfigVersion:\s*0/)
  assert.match(input, /watch\(\(\) => state\.generationConfigVersion,\s*\(\) => void loadGenerationOptions\(\)\)/)
  assert.match(settings, /function notifyGenerationConfigChanged\(\)[\s\S]*?chatState\.generationConfigVersion \+= 1/)
  assert.match(settings, /async function saveSystemConfig\(\)[\s\S]*?notifyGenerationConfigChanged\(\)/)
  assert.match(settings, /async function onSaveModel\(\)[\s\S]*?notifyGenerationConfigChanged\(\)/)
})

test('media model capabilities use a shared API editor with per-operation overrides', () => {
  assert.match(settings, /operations\[operation\] = normalizeGenerationOperationOverride\(operation\)/)
  assert.match(settings, /api: \{[\s\S]*?endpoint: sharedAPI\.endpoint/)
  assert.match(settings, /<strong>模型 API<\/strong>/)
  assert.match(settings, /v-model:value="editGenerationAPI\.endpoint"/)
  assert.match(settings, /const editGenerationOperationAPIs/)
  assert.match(settings, /v-for="operation in editGenerationOperations"/)
  assert.match(settings, /按能力覆盖端点/)
  assert.match(settings, /updateGenerationOperationAPI/)
  assert.match(settings, /validateMediaGenerationForm/)
  assert.doesNotMatch(settings, /editGenerationConfigs/)
  assert.match(settings, /validateGenerationAPIConfig\('媒体生成', editGenerationAPI\.value, true\)/)
  assert.match(settings, /默认共享模型 API/)
  assert.match(settings, /function defaultGenerationAPIConfig/)
  assert.match(settings, /endpointDefaultsForSelectedProvider/)
  assert.match(settings, /syncGenerationAPIWithDefaults/)
  assert.match(settings, /完整请求：[\s\S]*?generationEndpointPreview/)
  assert.match(settings, /completeEndpointPreview\(editGenerationAPI\.value\.endpoint\)/)
  assert.match(settings, /defaultGenerationEndpoint|defaultGenerationQueryEndpoint/)
})

test('async media endpoint editor explains task ids and flags a missing task collection path', () => {
  assert.match(settings, /function hasTaskIDPlaceholder\(/)
  assert.match(settings, /\{task_id\}.*\{id\}/s)
  assert.match(settings, /任务 ID 来自创建接口的响应，无需手动填写/)
  assert.match(settings, /generationEndpointConsistencyWarning/)
  assert.match(settings, /创建端点可能缺少任务集合路径/)
})

test('generated media cards expose a readable file identity, preview, and download actions', () => {
  assert.match(toolCard, /function toolMediaAssetFileName\(/)
  assert.match(toolCard, /class="generated-asset-preview"[\s\S]*?@click="openToolMediaAsset\(asset\)"/)
  assert.match(toolCard, /class="generated-asset-footer"[\s\S]*?class="generated-asset-name"/)
  assert.match(toolCard, /class="generated-asset-actions"[\s\S]*?Maximize2[\s\S]*?Download/)
  assert.match(toolCard, /class="generated-asset-file"/)
  assert.match(toolCard, /@click="downloadToolMediaAsset\(asset\)"/)
  assert.match(attachmentArtifacts, /export function toolGeneratedAssetsFromPart\(part: ToolPart\): ToolGeneratedAsset\[\]/)
  assert.match(attachmentArtifacts, /kind === 'image' \|\| kind === 'video' \|\| kind === 'audio' \|\| kind === 'text' \|\| kind === 'file'/)
})

test('browser screenshots use the same durable media asset card as generated output', () => {
  assert.match(toolCard, /const isBrowserScreenshot = computed\(\(\) => props\.part\.name === 'browser_screenshot'\)/)
  assert.match(toolCard, /const toolMediaAssets = computed<ToolGeneratedAsset\[\]>\(\(\) => toolGeneratedAssetsFromPart\(props\.part\)\)/)
  assert.match(attachmentArtifacts, /source: Exclude<AttachmentArtifactSource, 'upload'> = isBrowserScreenshot \? 'browser_screenshot' : 'generation'/)
  assert.match(attachmentArtifacts, /if \(source === 'browser_screenshot'\) return '浏览器截图'/)
  assert.doesNotMatch(toolCard, /class="tool-screenshot"/)
})

test('all Naive UI selects use the session picker visual language', () => {
  assert.match(app, /Select: \{[\s\S]*?InternalSelection: \{[\s\S]*?color: 'var\(--surface-2\)'/)
  assert.match(app, /border: '1px solid var\(--border-subtle\)'/)
  assert.match(app, /borderRadius: 'var\(--radius-sm\)'/)
  assert.match(app, /InternalSelectMenu: \{[\s\S]*?optionColorPending: 'var\(--surface-3\)'/)
})

test('session media multi-selects share one row and summarize selections as tags', () => {
  assert.match(input, /session-config-options--stacked \{[\s\S]*?flex-wrap: nowrap/)
  assert.match(input, /data-testid="session-recognition-dropdown"[\s\S]*?\{\{ recognitionCapabilityFirstLabel \}\}[\s\S]*?\{\{ enabledRecognitionCapabilities\.length \}\}/)
  assert.match(input, /data-testid="session-generation-dropdown"[\s\S]*?\{\{ generationCapabilityFirstLabel \}\}[\s\S]*?\{\{ enabledGenerationOperations\.length \}\}/)
  assert.match(input, /\.opt-pick-tags \{[\s\S]*?\.opt-pick-tag--count \{/)
  assert.doesNotMatch(input, /session-config-row--media-recognition|opt-pick--wide|session-config-count/)
})

test('session configuration keeps all guidance inside help popovers', () => {
  assert.match(input, /如果没有可选能力，请先到该页面配置至少一种能力/)
  assert.match(input, /如果显示“不可用”，请先到“应用设置 > 系统 > 媒体生成”/)
  assert.doesNotMatch(input, /session-config-hint/)
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
