import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

function readClientSource(): string {
  return readFileSync(new URL('../src/api/client.ts', import.meta.url), 'utf8')
}

function readSettingsSource(): string {
  return readFileSync(new URL('../src/components/AppSettingsModal.vue', import.meta.url), 'utf8')
}

test('provider test client sends an optional model to one endpoint', () => {
  const source = readClientSource()

	assert.match(source, /export interface ProviderTestResult/)
	assert.match(source, /export const testProvider = \(provider: string, model\?: string\)/)
	assert.match(source, /providers\/\$\{encodeURIComponent\(provider\)\}\/test/)
	assert.match(source, /body: JSON\.stringify\(model \? \{ model \} : \{\}\)/)
})

test('provider settings exposes default-model and per-model test actions', () => {
	const source = readSettingsSource()

	assert.match(source, /data-testid="test-provider-default"/)
	assert.match(source, /@click="onTestProvider\(\)"/)
	assert.match(source, /:data-testid="`test-model-\$\{m\.name\}`"/)
	assert.match(source, /@click="onTestProvider\(m\.name\)"/)
	assert.match(source, /await api\.testProvider\(providerName, model\)/)
})

test('provider settings uses protocol plus Base URL and per-model endpoint suffixes', () => {
	const settings = readSettingsSource()
	const client = readClientSource()

	assert.match(settings, /<label class="settings-form-label">Base URL/)
	assert.match(settings, /base_url: newBaseURL\.value\.trim\(\)/)
	assert.match(settings, /API 端点后缀/)
	assert.match(settings, /api_endpoint: editModelType\.value === 'llm'/)
	assert.match(settings, /return protocol === 'anthropic' \? '\/messages' : '\/chat\/completions'/)
	assert.doesNotMatch(settings, /厂商预设|vendorOptions|newVendor|editVendor|probeUpstreamModels/)
	assert.match(client, /export interface AddProviderRequest \{[\s\S]*?base_url: string/)
	assert.match(client, /export interface AddModelRequest \{[\s\S]*?api_endpoint\?: string/)
	assert.doesNotMatch(client, /export interface AddProviderRequest \{[\s\S]*?vendor\??:/)
})

test('fetching an upstream model opens the regular model editor before persistence', () => {
	const settings = readSettingsSource()
	const client = readClientSource()

	assert.match(settings, />\s*获取模型\s*</)
	assert.match(settings, /await api\.fetchUpstreamModels\(selected\.value\.name\)/)
	assert.match(settings, /function onImportUpstreamModel[\s\S]*?editModelName\.value = model\.id[\s\S]*?showAddModel\.value = true/)
	assert.doesNotMatch(settings, /async function onImportUpstreamModel/)
	assert.match(client, /export const fetchUpstreamModels/)
})
