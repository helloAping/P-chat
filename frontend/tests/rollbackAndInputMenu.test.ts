import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { getMediaContextTarget, markMediaContextTarget } from '../src/utils/mediaContext.ts'

function readStoreSource(): string {
  return readFileSync(new URL('../src/stores/chat.ts', import.meta.url), 'utf8')
}

function readInputAreaSource(): string {
  return readFileSync(new URL('../src/components/InputArea.vue', import.meta.url), 'utf8')
}

function readClientSource(): string {
  return readFileSync(new URL('../src/api/client.ts', import.meta.url), 'utf8')
}

function readConversationTurnSource(): string {
  return readFileSync(new URL('../src/composables/conversationTurn.ts', import.meta.url), 'utf8')
}

function readMessageBubbleSource(): string {
  return readFileSync(new URL('../src/components/MessageBubble.vue', import.meta.url), 'utf8')
}

function readLightboxSource(): string {
  return readFileSync(new URL('../src/components/ImageLightbox.vue', import.meta.url), 'utf8')
}

function readGeneratedAssetStripSource(): string {
  return readFileSync(new URL('../src/components/GeneratedAssetStrip.vue', import.meta.url), 'utf8')
}

function readToolCallCardSource(): string {
  return readFileSync(new URL('../src/components/ToolCallCard.vue', import.meta.url), 'utf8')
}

test('message right-click copy preserves and copies the selected text', () => {
  const source = readMessageBubbleSource()

  assert.match(source, /const messageContextMenuSelection = ref\(''\)/)
  assert.match(source, /messageContextMenuSelection\.value = window\.getSelection\(\)\?\.toString\(\) \?\? ''/)
  assert.match(source, /key: 'copy-selection'/)
  assert.match(source, /await copyText\(messageContextMenuSelection\.value\)/)
  assert.match(source, /key: 'copy-message'/)
  assert.match(source, /await copyEntireMessage\(\)/)
})

test('message right-click copy targets one media item before the whole message', () => {
  const bubble = readMessageBubbleSource()
  const strip = readGeneratedAssetStripSource()
  const toolCard = readToolCallCardSource()

  assert.match(bubble, /const mediaTarget = getMediaContextTarget\(e\)/)
  assert.match(bubble, /messageContextMenuTarget\.value = \{ kind: 'media', media: mediaTarget \}/)
  assert.match(bubble, /key: 'copy-media'/)
  assert.match(bubble, /key: 'copy-media-reference'/)
  assert.match(bubble, /key: 'download-media'/)
  assert.match(bubble, /await copyContextMedia\(target\.media\)/)
  assert.match(bubble, /@contextmenu="markMessageAttachmentContextTarget\(\$event, a\)"/)
  assert.match(strip, /@contextmenu="markMediaContextTarget\(\$event, asset\)"/)
  assert.match(toolCard, /@contextmenu="markToolMediaContextTarget\(\$event, asset\)"/)
})

test('media context registry keeps the exact item attached to the bubbling event', () => {
  const event = new Event('contextmenu')
  const image = { kind: 'image' as const, url: '/api/v1/uploads/one.png', name: 'one.png' }

  markMediaContextTarget(event, image)

  assert.equal(getMediaContextTarget(event), image)
  assert.equal(getMediaContextTarget(new Event('contextmenu')), undefined)
})

test('message hover toolbar keeps primary actions and merges secondary actions into one menu', () => {
  const source = readMessageBubbleSource()

  assert.match(source, /const messageActionMenuOptions = computed<DropdownOption\[\]>/)
  assert.match(source, /<NDropdown[\s\S]*?:options="messageActionMenuOptions"[\s\S]*?@select="onMessageActionMenuSelect"/)
  assert.match(source, /messageActionOption\('fork',/)
  assert.match(source, /messageActionOption\('rollback',/)
  assert.doesNotMatch(source, /v-if="canFork"[\s\S]*?class="bubble-action-btn bubble-action-pulse"/)
  assert.doesNotMatch(source, /v-if="canRollback"[\s\S]*?class="bubble-action-btn bubble-action-rollback"/)
  assert.match(source, /class="attachment-action-bar"/)
  assert.match(source, /class="attachment-action-bar attachment-action-bar--image"/)
  assert.match(source, /'bubble--attachments': hasAttachments/)
  assert.match(source, /'user-message-caption': message\.role === 'user' && hasAttachments/)
  assert.doesNotMatch(source, /attach-action-label/)
  assert.doesNotMatch(source, /bubble-actions--media/)
  assert.match(source, /class="message-footer"/)
  assert.match(source, /\.message-footer\[data-role="user"\]\s*\{\s*justify-content: flex-end/)
  const toolbarRule = source.match(/\.bubble-actions\s*\{([^}]*)\}/)
  assert.ok(toolbarRule, 'bubble actions CSS rule should exist')
  assert.doesNotMatch(toolbarRule[1], /position:\s*absolute/)
  assert.match(source, /\.attachment-action-bar--image\s*\{[\s\S]*?top: var\(--space-2\)/)
  assert.match(source, /\.msg\.user \.bubble\.bubble--attachments\s*\{[\s\S]*?background: transparent[\s\S]*?padding: 0/)
  assert.match(source, /grid-template-columns: repeat\(auto-fit, minmax\(/)
  assert.match(source, /\.msg\.user \.bubble-body \.attach-action-btn\s*\{[\s\S]*?color: var\(--text-primary\)/)
})

test('media attachments use semantic thumbnails and open the full-screen viewer', () => {
  const source = readMessageBubbleSource()
  const lightbox = readLightboxSource()

  // The wire type may be the generic `text` fallback; visual rendering must
  // still recognise media from kind, MIME, or the filename extension.
  assert.match(source, /function attachmentVisualKind\(attachment: MessageAttachment\)/)
  assert.match(source, /attachment\.mime\?\.startsWith\('image\/'\)/)
  assert.match(source, /attachment\.mime\?\.startsWith\('video\/'\)/)
  assert.match(source, /attachmentVisualKind\(a\) === 'image'/)
  assert.match(source, /attachmentVisualKind\(a\) === 'video'/)
  assert.match(source, /openLightbox\(a\.url, a\.name \|\| 'video', 'video'\)/)
  assert.match(source, /class="media-thumbnail media-thumbnail--image"/)
  assert.match(source, /class="media-thumbnail media-thumbnail--video"/)

  // Non-media attachments keep a dedicated information card rather than
  // borrowing the media thumbnail treatment.
  assert.match(source, /class="attachment-file-card"/)
  assert.match(lightbox, /state\.lightbox\.kind === 'video'/)
})

test('rollback restores text and attachments as one composer draft', () => {
  const source = readStoreSource()
  const input = readInputAreaSource()

  assert.match(source, /export async function rollbackTo\(sessionId: string, messageIndex: number\)/)
  assert.match(source, /const localDeleted = msgs\.slice\(messageIndex\)/)
  assert.match(source, /result\.deleted_messages\?\.length/)
  assert.match(source, /result\.deleted_count > 0 \? localDeleted/)
  assert.match(source, /state\.rollbackUndo\[sessionId\] = \{[\s\S]*messages: deletedMessages,[\s\S]*displayMessages: localDeleted/)
  assert.match(source, /rollbackDraftAttachments\(msg, deletedMessages\)/)
  assert.match(source, /pendingAttachmentFromMessage\(attachment, rollbackSource\)/)
  assert.match(source, /state\.pendingAttachments\[sessionId\] = restoredAttachments/)
  assert.match(source, /state\.pendingInput\[sessionId\] = msg\.content \|\| ''/)
  assert.match(source, /state\.pendingInputRevision\[sessionId\]/)
  assert.match(input, /currentPendingInputRevision\.value/)
  assert.match(input, /inputText\.value = currentPendingInput\.value/)
})

test('undo rollback removes only recalled attachments and preserves edited text', () => {
  const store = readStoreSource()
  const input = readInputAreaSource()

  assert.match(store, /attachment\._rollbackSource === draft\.source/)
  assert.match(store, /kept\.push\(attachment\)/)
  assert.match(store, /msgs\.splice\(undo\.fromIndex, 0, \.\.\.undo\.displayMessages\)/)
  assert.match(input, /inputText\.value === draft\.text/)
  assert.match(input, /if \(shouldClearInjectedText\) inputText\.value = ''/)
  assert.match(input, /@click="onUndoRollback"/)
  assert.doesNotMatch(store, /function dismissRollback\(sessionId: string\) \{[\s\S]*?clearAttachments\(sessionId\)/)
})

test('composer keeps durable upload ids for rollback restoration', () => {
  const source = readInputAreaSource()

  assert.match(source, /upload_id: a\.id \|\| undefined/)
  assert.match(source, /attachment\.upload_id = uploadID/)
})

test('input send queues messages while current session streams', () => {
  const source = readInputAreaSource()
  const match = source.match(/async function send\(\) \{([\s\S]*?)\n  if \(isSlashLine\(\)\)/)
  assert.ok(match, 'send() should exist and reach slash-command handling')

  assert.doesNotMatch(match[1], /if \(isStreaming\.value\) \{[\s\S]*?return[\s\S]*?\}/)
  assert.match(source, /const currentConversationBusy = computed\(\(\) =>[\s\S]*?!!state\.turnQueueDraining\[state\.currentID\]/)
  assert.match(source, /currentSessionWorking\.value/)
  assert.match(source, /const shouldQueue = currentConversationBusy\.value \|\| currentTurnQueue\.value\.length > 0/)
  assert.match(source, /await enqueueTurnQueue\(id, turnPayload\)/)
  assert.match(source, /消息已加入队列/)
  assert.match(source, /:title="currentConversationBusy \? '加入队列 \(Enter\)' : '发送 \(Enter\)'"/)
})

test('input confirms and clears unfinished todos before sending a new message', () => {
  const source = readInputAreaSource()

  assert.match(source, /useDialog\(\)/)
  assert.match(source, /hasUnfinishedTodos/)
  assert.match(source, /await api\.clearTodos\(id\)/)
  assert.match(source, /state\.sessionTodos\[id\] = \[\]/)
	assert.match(source, /if \(!selectedMode\) return/)
	assert.match(source, /todoMode = selectedMode/)
	assert.match(source, /return 'resume'/)
	assert.match(source, /return 'clear'/)
  assert.match(source, /sendPreflightSessions\.has\(preflightSessionID\)/)
  assert.match(source, /state\.currentID !== preflightSessionID/)
})

test('initial session load preserves messages created while history is in flight', () => {
  const source = readStoreSource()

  assert.match(
    source,
    /const liveMessages = \(state\.sessionMessages\[id\] as Message\[\] \| undefined\) \?\? \[\][\s\S]*?state\.sessionMessages\[id\] = \[\.\.\.history, \.\.\.liveMessages\]/,
  )
})

test('turn queue view only shows waiting or failed items', () => {
  const source = readStoreSource()

  const match = source.match(/export const currentTurnQueue = computed\(\(\) =>([\s\S]*?)\n\)/)
  assert.ok(match, 'currentTurnQueue computed should exist')
  assert.match(match[1], /item\.status === 'queued'/)
  assert.match(match[1], /item\.status === 'failed'/)
  assert.doesNotMatch(match[1], /item\.status === 'running'/)
})

test('queued messages can be edited before they are claimed', () => {
  const client = readClientSource()
  const store = readStoreSource()
  const input = readInputAreaSource()
  const conversationTurn = readConversationTurnSource()

  assert.match(client, /export const editTurnQueueItem = \(sessionId: string, queueId: number, message: string\)/)
  assert.match(client, /method: 'PATCH', body: JSON\.stringify\(\{ message \}\)/)
  assert.match(store, /export async function editQueuedTurn\(sessionId: string, queueId: number, message: string\)/)
  assert.match(store, /turnQueueEditing: \{\} as Record<string, boolean>/)
  assert.match(store, /export function setTurnQueueEditing\(sessionId: string, editing: boolean\)/)
  assert.match(input, /function startEditingQueuedTurn\(item: api\.TurnQueueItem\)/)
  assert.match(input, /if \(item\.status !== 'queued'\) return/)
  assert.doesNotMatch(input, /item\.status !== 'queued' \|\| queueDraining\.value/)
  assert.match(input, /setTurnQueueEditing\(item\.session_id, true\)/)
  assert.match(input, /await editQueuedTurn\(sessionID, editingQueueId\.value, messageText\)/)
  assert.match(input, /aria-label="编辑排队消息"/)
  assert.match(input, /aria-label="保存排队消息"/)
  assert.match(conversationTurn, /if \(state\.turnQueueEditing\[sessionId\]\) return/)
  assert.match(conversationTurn, /while \(hasQueuedTurns\(sessionId\)[\s\S]*?if \(state\.turnQueueEditing\[sessionId\]\) break[\s\S]*?claimNextQueuedTurnForDrain/)
})

test('session working state includes background work for queue gating', () => {
  const source = readStoreSource()

  assert.match(source, /export function isSessionWorking\(id: string\): boolean/)
  assert.match(source, /state\.sessionBackgroundSubAgentJobs\[id\]/)
  assert.match(source, /state\.sessionBackgroundHookMerging\[id\]/)
  assert.match(source, /state\.isRecovering\[id\]/)
  assert.match(source, /export const currentSessionWorking = computed\(\(\) => isSessionWorking\(state\.currentID\)\)/)
})

test('input textarea has a manual right-click edit menu with feedback actions', () => {
  const source = readInputAreaSource()

  assert.match(source, /const inputContextMenuOptions: DropdownOption\[\]/)
  assert.match(source, /key: 'copy', label: '复制'/)
  assert.match(source, /key: 'cut', label: '剪切'/)
  assert.match(source, /key: 'paste', label: '粘贴'/)
  assert.match(source, /key: 'select_all', label: '全选'/)
  assert.match(source, /@contextmenu="onInputContextMenu"/)
  assert.match(source, /message\.success\('复制成功'\)/)
  assert.match(source, /message\.success\('粘贴成功'\)/)
  assert.match(source, /message\.success\('剪切成功'\)/)
})
