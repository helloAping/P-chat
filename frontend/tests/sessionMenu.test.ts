import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

function readSidebar(): string {
  return readFileSync(new URL('../src/components/SessionSidebar.vue', import.meta.url), 'utf8')
}

function readStyles(): string {
  return readFileSync(new URL('../src/style.css', import.meta.url), 'utf8')
}

test('session action menu reuses the shared app-action-menu layout', () => {
  const source = readSidebar()

  assert.match(source, /class: 'app-action-menu'/)
  assert.match(source, /:menu-props="actionMenuProps"/)
  assert.match(source, /size="small"/)
  assert.match(source, /function actionOption\(/)
  assert.match(source, /class: 'app-action-item'/)
  assert.match(source, /actionOption\('export', '导出对话', FileText\)/)
  assert.match(source, /title="导出对话"/)
  assert.match(source, /exportFormat === 'pdf'/)
  assert.match(source, /exportFormat === 'html'/)
  assert.match(source, /actionOption\('delete', '归档', Archive, \{ props: \{ class: 'app-action-danger' \} \}\)/)
})

test('app-action-menu pins icon and label on one flex row', () => {
  const css = readStyles()

  assert.match(css, /\.app-action-menu\.n-dropdown-menu \{[\s\S]*--n-option-icon-prefix-width: 0px/)
  assert.match(css, /\.app-action-menu \.n-dropdown-option \.n-dropdown-option-body \{[\s\S]*display: flex !important/)
  assert.match(css, /\.app-action-menu \.n-dropdown-option \.n-dropdown-option-body \{[\s\S]*flex-direction: row !important/)
  assert.match(css, /\.app-action-item \{[\s\S]*display: flex[\s\S]*flex-direction: row/)
  assert.match(css, /\.app-action-menu \.n-dropdown-option \.n-dropdown-option-body__suffix \{[\s\S]*display: none !important/)
  assert.match(css, /\.app-action-menu \.n-dropdown-divider \{[\s\S]*background-color: var\(--border-default\)/)
})
