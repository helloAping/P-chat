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
