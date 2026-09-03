import assert from 'node:assert/strict'
import test from 'node:test'
import { isNewerStateVersion, mergePublicPlotUpserts } from './friend-farm'

test('friend farm versions allow gaps but reject stale versions', () => {
  const current = { ownerEpoch: 2n, playerSeq: 10n }
  assert.equal(isNewerStateVersion({ ownerEpoch: 2n, playerSeq: 10n }, current), false)
  assert.equal(isNewerStateVersion({ ownerEpoch: 2n, playerSeq: 9n }, current), false)
  assert.equal(isNewerStateVersion({ ownerEpoch: 2n, playerSeq: 15n }, current), true)
  assert.equal(isNewerStateVersion({ ownerEpoch: 3n, playerSeq: 1n }, current), true)
  assert.equal(isNewerStateVersion({ ownerEpoch: 1n, playerSeq: 99n }, current), false)
})

test('friend farm plot upserts replace atomically and sort by plot ID', () => {
  const original = [
    { plotId: 3, value: 'old-three' },
    { plotId: 1, value: 'one' },
  ]
  const merged = mergePublicPlotUpserts(original, [
    { plotId: 3, value: 'new-three' },
    { plotId: 2, value: 'two' },
  ])
  assert.deepEqual(merged, [
    { plotId: 1, value: 'one' },
    { plotId: 2, value: 'two' },
    { plotId: 3, value: 'new-three' },
  ])
  assert.equal(original[0].value, 'old-three')
})
