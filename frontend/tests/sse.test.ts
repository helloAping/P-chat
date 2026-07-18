import assert from 'node:assert/strict'
import test from 'node:test'

import {
  consumeSSEStream,
  decodeStreamEvent,
  emitStreamEvent,
  parseSSEFrame,
  type StreamEventLike,
} from '../src/api/sse.ts'

function readerFromChunks(chunks: string[]): ReadableStreamDefaultReader<Uint8Array> {
  const encoder = new TextEncoder()
  return new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(encoder.encode(chunk))
      controller.close()
    },
  }).getReader()
}

test('parseSSEFrame joins data lines and reads id as seq', () => {
  assert.deepEqual(
    parseSSEFrame('data: {"type":"content",\ndata: "content":"hi"}\nid: 42'),
    { data: '{"type":"content",\n"content":"hi"}', seq: 42 },
  )
})

test('decodeStreamEvent ignores empty payloads and stamps seq', () => {
  assert.equal(decodeStreamEvent('', 'test'), null)
  assert.equal(decodeStreamEvent('[DONE]', 'test'), null)
  assert.deepEqual(
    decodeStreamEvent<StreamEventLike>('{"type":"done"}', 'test', 7),
    { type: 'done', seq: 7 },
  )
})

test('emitStreamEvent isolates handler errors', () => {
  const warn = console.warn
  console.warn = () => {}
  try {
    assert.doesNotThrow(() => {
      emitStreamEvent({ type: 'content' }, 'test', () => {
        throw new Error('render failed')
      })
    })
  } finally {
    console.warn = warn
  }
})

test('consumeSSEStream handles split frames and skips DONE', async () => {
  const events: StreamEventLike[] = []
  await consumeSSEStream({
    reader: readerFromChunks([
      'data: {"type":"content","content":"he',
      'llo"}\nid: 1\n\n',
      'data: [DONE]\nid: 2\n\n',
      'data: {"type":"done"}\nid: 3\n\n',
    ]),
    label: 'test',
    onEvent: event => events.push(event),
  })

  assert.deepEqual(events, [
    { type: 'content', content: 'hello', seq: 1 },
    { type: 'done', seq: 3 },
  ])
})

test('consumeSSEStream reports the last observed seq when the stream drops', async () => {
  const encoder = new TextEncoder()
  let reads = 0
  let drop: { lastSeq: number; reason: string } | undefined
  const reader = {
    async read() {
      reads += 1
      if (reads === 1) {
        return {
          done: false,
          value: encoder.encode('data: {"type":"content","content":"hi"}\nid: 9\n\n'),
        }
      }
      throw new Error('network gone')
    },
  } as ReadableStreamDefaultReader<Uint8Array>

  await assert.rejects(
    () => consumeSSEStream({
      reader,
      label: 'test',
      onEvent: () => {},
      onStreamDrop: info => { drop = info },
    }),
    /test stream: network gone/,
  )

  assert.deepEqual(drop, { lastSeq: 9, reason: 'network gone' })
})
