import { test } from 'node:test'
import assert from 'node:assert/strict'
import { savePending, loadPending, clearPending } from '../src/game/pending.ts'

test('unconfirmed request survives re-login and is isolated per player', () => {
  const values = new Map<string, string>()
  const storage = { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => values.set(key, value), removeItem: (key: string) => values.delete(key) } as unknown as Storage
  const request = { request_id: 'original-request', action: 'BUY_SEEDS', data: { quantity: 3 } }
  savePending(storage, 'alice', request)
  assert.equal(loadPending(storage, 'bob'), undefined)
  assert.deepEqual(loadPending(storage, 'alice'), request)
  clearPending(storage, 'alice')
  assert.equal(loadPending(storage, 'alice'), undefined)
})
