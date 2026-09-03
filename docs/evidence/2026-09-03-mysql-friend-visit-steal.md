---
status: verified_with_limits
date: 2026-09-03
branch: feat/mysql-friend-visit-steal
environment: Windows local dual-Zone MySQL
---

# MySQL 好友访问与直调偷菜证据

## Verified implementation

The branch uses:

```text
H5 -> WebSocket/Protobuf -> Gate
-> HTTP/Protobuf -> Visitor Zone
-> HTTP/Protobuf -> FriendSvr / Owner Zone
-> Visitor and Owner Player Actors
-> asynchronous Dirty MySQL checkpoints
```

No gRPC Service, Tcaplus Store, `FriendInteraction` Saga or reconciler was
added.

Implemented behavior:

- mutual-friend check from the MySQL `friend_relations` authority;
- 16-byte process-local visit IDs with a 90-second Owner lease;
- Visitor and Owner in-memory visit registries;
- cross-Zone ENTER, HEARTBEAT and EXIT;
- public plot projection without private Player state;
- frozen steal quantity, maximum count and protected Owner yield;
- one steal per Visitor per crop round;
- Owner and Visitor checkpoint idempotency keyed by the WS request ID;
- Owner plot deduction followed by Visitor inventory and task submission;
- H5 friend-farm view, heartbeat, exit and steal controls.

## Automated verification

Commands:

```text
buf lint
buf generate
cd server
go test ./...
go vet ./...
cd ../web
npm run typecheck
npm run build
```

All completed successfully after the final integration.

Unit coverage includes:

- visit create/replay/refresh/expiry/forgery/exit;
- valid empty Protobuf HTTP responses;
- Owner steal rules and same-Visitor rejection;
- Visitor inventory credit and chapter-two task progress;
- same-request replay without a second Owner call;
- checkpoint round-trip of frozen fields and steal history;
- Gate and Zone package regression.

## Live dual-Zone MySQL result

Command:

```powershell
.\tests\e2e\run-mysql-friend-slice.ps1
```

Observed final two-phase passing run:

```text
PLAYERS first=426/zone-a second=428/zone-b
FRIEND create_redeem_list=true
visit_heartbeat_exit=true
direct_steal=true
visitor_inventory=true
steal_replay=true
mysql_relation=true
PASS TestMySQLFriendSlice (77.84s)
RESULT mysql_friend_slice_e2e=PASS

FRIEND_RECOVERY owner_plot=true
visitor_inventory=true
relation=true
old_visit_invalid=true
PASS TestMySQLFriendSlice (0.67s)
RESULT mysql_friend_slice_restart_recovery=PASS
```

This proves one live cross-Zone path through create/redeem/list, mature public
farm entry, heartbeat, direct Owner steal, Visitor inventory credit, same-ID
replay and exit. The script then stopped the complete stack, started fresh
Coordinator/Login/two Zones/Friend/Gate processes, logged both accounts in
again, recovered the Owner reduced harvestable quantity and Visitor inventory
from MySQL, retained the relation, and rejected the pre-restart active visit ID.

## Defects found during live verification

1. The first extended test waited for best-effort Owner maturity Push and
   timed out. The test now waits for the configured growth interval and lets
   ENTER materialize the authoritative Owner Actor state.
2. Successful `ExitVisitorResponse` is an empty Protobuf message whose valid
   encoding is zero bytes. The shared HTTP client initially rejected it as an
   invalid empty body. The client now permits zero-byte Protobuf responses and
   has a regression test.
3. A global two-second HTTP client timeout was too broad. General visit calls
   now use five seconds; only the Owner steal call made while holding the
   Visitor mailbox has the reviewed two-second deadline.

## Limitations

- Live accounts begin in chapter one, so chapter-two steal-task mutation is
  covered by unit tests rather than this live run.
- H5 typecheck and production build pass; a manual/browser interaction and
  320-CSS-pixel screenshot check have not yet been recorded.
- The accepted no-Saga failure windows remain: abnormal failure may persist
  only one side of the Owner/Visitor Dirty mutations.
- Visit records are intentionally non-durable; either relevant Zone restart
  invalidates the old visit and requires ENTER again.
