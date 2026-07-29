import assert from 'node:assert/strict'
import test from 'node:test'

import {
  hasWeChatQRPayload,
  isWeChatQRImageSource,
  resolveWeChatQRImageSource,
  resolveWeChatQRValue,
} from '../src/im/wechatQr.ts'

test('wechat QR image source accepts renderable image URLs', () => {
  assert.equal(isWeChatQRImageSource('data:image/png;base64,abc'), true)
  assert.equal(isWeChatQRImageSource('blob:http://wails.localhost/abc'), true)
  assert.equal(isWeChatQRImageSource('https://weixin.qq.com/x/abc'), true)
  assert.equal(isWeChatQRImageSource('/assets/wechat-qr.png'), true)
})

test('wechat QR image source rejects poll API URLs and raw tokens', () => {
  assert.equal(isWeChatQRImageSource('/api/v1/im/wechat/qr/166d71181df61fcbd986615c3654e08a'), false)
  assert.equal(isWeChatQRImageSource('http://wails.localhost/api/v1/im/wechat/qr/166d71181df61fcbd986615c3654e08a'), false)
  assert.equal(isWeChatQRImageSource('166d71181df61fcbd986615c3654e08a'), false)
})

test('resolveWeChatQRImageSource only returns renderable session image sources', () => {
  assert.equal(
    resolveWeChatQRImageSource({
      id: 'qr-1',
      status: 'waiting',
      qr_url: '/api/v1/im/wechat/qr/qr-1',
      poll_after_ms: 2000,
    }),
    '',
  )
  assert.equal(
    resolveWeChatQRImageSource({
      id: 'qr-2',
      status: 'waiting',
      qr_data: 'https://liteapp.weixin.qq.com/q/abc123?bot_type=3',
      poll_after_ms: 2000,
    }),
    '',
  )
})

test('resolveWeChatQRValue falls back to QR payload for local rendering', () => {
  assert.equal(
    resolveWeChatQRValue({
      id: '53f7ce2205234ff125302921d25a6701',
      status: 'waiting',
      qr_url: 'https://liteapp.weixin.qq.com/q/abc123?bot_type=3',
      poll_after_ms: 2000,
    }),
    'https://liteapp.weixin.qq.com/q/abc123?bot_type=3',
  )
  assert.equal(
    resolveWeChatQRValue({
      id: 'qr-2',
      status: 'waiting',
      qrcode: 'qr-login-token',
      qr_data: 'qr-login-payload',
      poll_after_ms: 2000,
    }),
    'qr-login-payload',
  )
})

test('hasWeChatQRPayload reports QR data even when it is not image-renderable', () => {
  assert.equal(hasWeChatQRPayload({ id: 'qr-1', status: 'waiting', qr_data: 'raw-token', poll_after_ms: 2000 }), true)
  assert.equal(hasWeChatQRPayload({ id: '', status: 'waiting', poll_after_ms: 2000 }), false)
})
