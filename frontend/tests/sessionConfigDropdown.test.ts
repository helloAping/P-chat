import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const source = readFileSync(new URL('../src/components/InputArea.vue', import.meta.url), 'utf8')

test('session style and knowledge base use compact dropdown pickers', () => {
  assert.match(source, /:options="styleDropdownOptions"[\s\S]*?@select="pickStyle"[\s\S]*?data-testid="session-style-dropdown"[\s\S]*?\{\{ currentStyleLabel \}\}/)
  assert.match(source, /:options="kbDropdownOptions"[\s\S]*?@select="pickKB"[\s\S]*?data-testid="session-knowledge-dropdown"[\s\S]*?\{\{ currentKBLabel \}\}/)
  assert.match(source, /styleOptions\.value\.map\(option => \(\{ label: option\.label, key: option\.value \}\)\)/)
  assert.match(source, /kbOptions\.value\.map\(option => \(\{ label: option\.label, key: option\.value \}\)\)/)
  assert.match(source, /v-if="enabledKBCount === 0" class="session-config-hint"/)
})
