import assert from 'node:assert/strict'
import test from 'node:test'

import type { Message } from '../src/api/client.ts'
import {
  collectConversationAttachments,
  collectMessageGeneratedAttachments,
} from '../src/utils/attachmentArtifacts.ts'

function toolResult(assets: unknown[]) {
  return JSON.stringify({ assets })
}

test('message generated attachments include top-level and nested sub-agent assets', () => {
  const message: Message = {
    id: 42,
    role: 'assistant',
    content: '',
    parts: [
      {
        kind: 'tool',
        name: 'generate_video',
        status: 'ok',
        tool_id: 'tool-video',
        result: toolResult([
          {
            id: 'video-1',
            kind: 'video',
            mime_type: 'video/mp4',
            name: 'demo.mp4',
            url: '/api/v1/generated/video-1',
          },
        ]),
      },
      {
        kind: 'sub_agent',
        task: '生成配图',
        status: 'ok',
        parts: [
          {
            kind: 'tool',
            name: 'generate_image',
            status: 'ok',
            tool_id: 'tool-image',
            result: toolResult([
              {
                id: 'image-1',
                kind: 'image',
                mime_type: 'image/png',
                name: 'cover.png',
                url: '/api/v1/generated/image-1',
              },
            ]),
          },
        ],
      },
    ],
  }

  const assets = collectMessageGeneratedAttachments(message)

  assert.equal(assets.length, 2)
  assert.deepEqual(assets.map(asset => asset.name), ['demo.mp4', 'cover.png'])
  assert.deepEqual(assets.map(asset => asset.toolName), ['generate_video', 'generate_image'])
  assert.deepEqual(assets.map(asset => asset.messageId), [42, 42])
})

test('message generated attachments deduplicate repeated generated URLs', () => {
  const message: Message = {
    id: 7,
    role: 'assistant',
    content: '',
    parts: [
      {
        kind: 'tool',
        name: 'generate_video',
        status: 'ok',
        tool_id: 'tool-a',
        result: toolResult([
          {
            id: 'asset-a',
            kind: 'video',
            name: 'first.mp4',
            url: '/api/v1/generated/shared',
          },
        ]),
      },
      {
        kind: 'tool',
        name: 'generate_video',
        status: 'ok',
        tool_id: 'tool-b',
        result: toolResult([
          {
            id: 'asset-b',
            kind: 'video',
            name: 'second.mp4',
            url: '/api/v1/generated/shared',
          },
        ]),
      },
    ],
  }

  const assets = collectMessageGeneratedAttachments(message)

  assert.equal(assets.length, 1)
  assert.equal(assets[0].name, 'first.mp4')
})

test('message generated attachments do not include user uploads', () => {
  const message: Message = {
    id: 9,
    role: 'assistant',
    content: '',
    attachments: [
      {
        type: 'image_url',
        upload_id: 'upload-1',
        url: '/api/v1/uploads/upload-1',
        name: 'input.png',
        kind: 'image',
        mime: 'image/png',
      },
    ],
    parts: [
      {
        kind: 'tool',
        name: 'generate_image',
        status: 'ok',
        tool_id: 'tool-image',
        result: toolResult([
          {
            id: 'image-2',
            kind: 'image',
            name: 'output.png',
            url: '/api/v1/generated/image-2',
          },
        ]),
      },
    ],
  }

  const messageAssets = collectMessageGeneratedAttachments(message)
  const conversationAssets = collectConversationAttachments([message])

  assert.deepEqual(messageAssets.map(asset => asset.name), ['output.png'])
  assert.deepEqual(conversationAssets.map(asset => asset.name), ['input.png', 'output.png'])
})
