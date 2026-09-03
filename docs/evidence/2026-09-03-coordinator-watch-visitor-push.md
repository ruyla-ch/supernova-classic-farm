---
status: verified
updated: 2026-09-03
---

# Coordinator route watch and visitor farm Push evidence

## Implemented boundary

- The single-node Coordinator remains the online authority for the committed
  4096-entry `routing.Map`. In MySQL mode it rebuilds ACTIVE ownership from
  `shard_fences` plus open migration progress after restart.
- A shared Go `coordinatorclient` synchronously loads a complete route snapshot
  before Gate or Zone serves, then follows `map_version` through capped
  loopback HTTP long polling. Gate command routing, Zone ownership validation,
  and Visitor-Zone Owner lookup read immutable local snapshots.
- A `NOT_OWNER` response forces one synchronous full resync before retrying the
  same request ID. A disconnected watcher cannot extend leases locally; cached
  routes become unusable when their committed lease expires.
- Owner Actor farm mutations capture immutable private `PlotView` and public
  `PublicPlotView` projections inside the mailbox. A bounded two-worker
  dispatcher sends `PLAYER_STATE_CHANGED` to the online Owner and
  `FRIEND_FARM_CHANGED` to current in-memory visit leases from the same Owner
  version. Visitor steal uses the distinct `FRIEND_STEAL` reason.
- The friend Push carries the exact visit ID and Owner state version separately
  from the Visitor envelope. The H5 ignores replaced/expired visits and
  equal/older Owner versions, allows sequence gaps caused by private Owner
  mutations, and atomically merges sorted plot upserts.
- Owner visit registration now precedes public snapshot capture. This removes
  the snapshot-to-registration missed-update window; exact-ID rollback removes
  a failed enter without deleting a concurrent replacement visit.

## Automated verification

Run from `E:\workspace\supernova-classic-farm`:

```text
buf lint
buf generate
cd server
go test ./...
go vet ./...
cd ../web
npm test
npm run typecheck
npm run build
cd ..
git diff --check
.\tests\e2e\run-mysql-friend-slice.ps1 -ServicePortOffset 10000
```

Observed results:

- Coordinator watch timeout/update, atomic cache replacement, synchronous
  resync, expired-route rejection, Gate adaptation, and Zone callback tests:
  pass.
- Owner maturity Push, public mutation/steal event deduplication, active versus
  expired visitor fanout, malformed friend Push rejection, visit rollback, and
  frontend version/merge tests: pass.
- Full Go tests and vet, Protobuf lint/generation, H5 tests/typecheck/build, and
  diff whitespace check: pass.
- Dual-Zone MySQL mutation phase: pass with Owner player 450 on Zone B and
  Visitor player 454 on Zone A. The run observed the Owner
  `PLAYER_STATE_CHANGED/MATURED` Push, the Owner
  `PLAYER_STATE_CHANGED/FRIEND_STEAL` Push, and the Visitor
  `FRIEND_FARM_CHANGED` Push after the same steal.
- Full-stack restart recovery: pass for Owner plot, Visitor inventory, and
  friend relation; the old process-local visit ID was rejected.

## Limitations

- HTTP long polling transfers a complete ShardMap on change. It validates the
  local subscription/cache semantics but is not a production transport or
  three-node consensus implementation.
- The prototype has one configured loopback Gate Push endpoint. It does not
  implement a distributed player-to-Gate connection directory, cross-Gate
  retry, durable Push replay, or production authentication for internal Push.
- Visit leases and fanout queues are process-local. Push is best-effort; queue
  saturation drops the newest public event, and H5 reconnect/visit re-entry
  recovers through a fresh snapshot.
- The live proof is protocol-level. Manual browser confirmation of visible
  friend-farm maturity/steal updates remains useful.
