---
status: verified
updated: 2026-09-03
---

# Friend pest gameplay evidence

## Implemented boundary

- WebSocket actions are `CATCH_PEST=208`,
  `APPLY_PEST_TO_FRIEND=320`, and `CATCH_PEST_FOR_FRIEND=321`.
- Owner catch targets the authenticated Owner Actor. Friend apply/catch first
  require a current visit lease and current mutual friendship, then call the
  routed Owner Zone over loopback HTTP with dedicated Protobuf messages.
- All pest actions are free: no inventory is consumed, no asset or reward is
  granted, and no task advances. Only the Owner Actor mutates, so this flow has
  no cross-Actor transaction or Saga.
- The development configuration authority enables `pest_id=1`,
  `config_version=1`, modifier `-0.3`, duration `120000ms`. Apply and catch
  settle exact elapsed growth under the previous rates before changing the
  `[start_at_ms, end_at_ms)` effect and recomputing maturity. Fertilizer and
  pest modifiers overlap additively.
- The active PEST checkpoint record stores pest ID, applying
  `source_player_id`, frozen configuration version, modifier, and interval.
  The source Visitor cannot catch its own pest; the Owner and a different
  visit-authorized mutual friend can.
- Owner-Actor terminal results are retained. Same-ID replay returns the first
  result with `replayed=true`, does not increment the Owner version again, and
  emits no duplicate private or public Push.
- One successful Owner mutation emits `PLAYER_STATE_CHANGED` with the private
  `PlotView` to the online Owner and `FRIEND_FARM_CHANGED` with
  `PublicPlotView.pest_active` to every current Visitor, all from the same
  Owner state version.

## Verification commands

Run on Windows from `E:\workspace\supernova-classic-farm`:

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
.\tests\e2e\run-mysql-friend-slice.ps1 -ServicePortOffset 12000
```

Observed results:

- Protobuf lint/generation, the full Go test suite, Go vet, H5 unit tests,
  TypeScript checking, production build, and repository diff whitespace check
  all passed.
- Unit coverage passed for development pest configuration, invalid
  configuration rejection, exact fertilizer/pest interval overlap,
  checkpoint source recovery, applying-source rejection, Owner success and
  failure replay, end-boundary expiry, visit/mutual-friend authorization,
  routed internal HTTP, and Push fanout.
- The live two-phase MySQL run used isolated ports `20080`–`20085`
  (`ServicePortOffset=12000`). Owner player 455 was routed to Zone A; applying
  Visitor player 459 was routed to Zone B. A third mutual friend was present
  during phase 1 as the permitted non-source catcher and second live Visitor.
- Phase 1 logged
  `pest_apply=true`, `pest_replay=true`,
  `pest_source_forbidden=true`, `owner_catch_pest=true`,
  `friend_catch_pest=true`, `pest_owner_push=true`, and
  `pest_all_visitors_push=true`.
- The same phase then passed the existing direct steal, Visitor inventory,
  steal replay, Owner maturity Push, Owner steal Push, Visitor farm Push, and
  persisted-friend-relation checks. Phase 1 completed in 78.75 seconds.

```text
FRIEND create_redeem_list=true visit_heartbeat_exit=true pest_apply=true pest_replay=true pest_source_forbidden=true owner_catch_pest=true friend_catch_pest=true pest_owner_push=true pest_all_visitors_push=true direct_steal=true visitor_inventory=true steal_replay=true owner_maturity_push=true owner_steal_push=true visitor_farm_push=true mysql_relation=true
```

- After the complete stack restarted, the recovery phase restored the Owner
  plot, Visitor inventory, and mutual relation, while rejecting the old
  process-local visit ID. Recovery completed in 0.52 seconds and the wrapper
  reported `mysql_friend_slice_restart_recovery=PASS`.

```text
FRIEND_RECOVERY owner_plot=true visitor_inventory=true relation=true old_visit_invalid=true
RESULT mysql_friend_slice_restart_recovery=PASS
```

## Limitations

- This is protocol-level live evidence, not a browser interaction recording.
  The remaining manual boundary is a 320-CSS-pixel browser smoke for pest
  controls/artwork and visibly synchronized Owner/all-Visitor updates.
- Visit leases, route watch transport, fanout queues, and the configured Gate
  Push endpoint are local prototype mechanisms. Visits do not survive restart;
  Push is best-effort and has no durable replay or cross-Gate delivery.
- The run verifies one local dual-Zone topology and MySQL recovery. It is not
  availability, capacity, weak-network, or 30-million-DAU evidence.
