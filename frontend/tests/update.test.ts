import assert from 'node:assert/strict'
import test from 'node:test'
import {
  checkUpdate,
  clearUpdateCache,
  downloadUpdate,
  UpdateVersionChangedError,
} from '../src/api/update.ts'

test('update checks keep stable and Beta selections separate and lock the download version', async () => {
  const originalFetch = globalThis.fetch
  const calls: Array<{ url: string; body?: string }> = []
  clearUpdateCache()
  globalThis.fetch = async (input, init) => {
    const url = String(input)
    calls.push({ url, body: init?.body?.toString() })
    if (url.endsWith('/download')) {
      return new Response(JSON.stringify({
        current: '1.0.12', latest: '1.0.13.beta', channel: 'beta', has_update: true,
        download_path: 'C:/updates/beta.zip', file_name: 'beta.zip', sha256: 'abc',
      }), { status: 200 })
    }
    const beta = url.includes('include_prerelease=1')
    return new Response(JSON.stringify({
      current: '1.0.12', latest: beta ? '1.0.13.beta' : '1.0.13',
      channel: beta ? 'beta' : 'stable', has_update: true,
    }), { status: 200 })
  }

  try {
    assert.equal((await checkUpdate(false, false))?.latest, '1.0.13')
    assert.equal((await checkUpdate(false, true))?.latest, '1.0.13.beta')
    assert.equal((await checkUpdate(false, true))?.latest, '1.0.13.beta')
    assert.equal(calls.length, 2)
    assert.equal(calls[0].url, '/api/v1/updates/check')
    assert.equal(calls[1].url, '/api/v1/updates/check?include_prerelease=1')

    const downloaded = await downloadUpdate(true, '1.0.13.beta')
    assert.equal(downloaded.latest, '1.0.13.beta')
    assert.deepEqual(JSON.parse(calls[2].body || ''), {
      include_prerelease: true,
      expected_version: '1.0.13.beta',
    })
  } finally {
    globalThis.fetch = originalFetch
    clearUpdateCache()
  }
})

test('a changed update version is surfaced as a conflict', async () => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = async () => new Response('{"error":"latest update version changed"}', { status: 409 })
  try {
    await assert.rejects(() => downloadUpdate(true, '1.0.13.beta'), UpdateVersionChangedError)
  } finally {
    globalThis.fetch = originalFetch
  }
})
