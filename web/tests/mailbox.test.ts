import { test } from 'node:test'
import assert from 'node:assert/strict'
import { unreadCount, replaceMailbox } from '../src/game/mailbox.ts'
import type { Mail } from '../src/game/types.ts'

test('counts unread mail and replaces local list with server response', () => {
  const first: Mail[] = [{ mail_id: '1', title: '欢迎', content: '正文', is_read: false, created_at_ms: 1 }]
  assert.equal(unreadCount(first), 1)
  const returned: Mail[] = [{ ...first[0], is_read: true }]
  assert.deepEqual(replaceMailbox(returned), returned)
  assert.notEqual(replaceMailbox(returned), returned)
})
