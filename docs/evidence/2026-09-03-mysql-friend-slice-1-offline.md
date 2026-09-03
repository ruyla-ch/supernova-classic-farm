---
status: active
date: 2026-09-03
scope: MySQL friend Slice 0 and Slice 1 offline gate
---

# MySQL friend Slice 1 offline evidence

## Implemented boundary

- Actions 300–302 and their Go/TypeScript Protobuf types.
- Proposed WS, error/idempotency and data-model contract extensions.
- Migration `000006_friends.up.sql`.
- MySQL-only FriendSvr (`127.0.0.1:8085`) with create, redeem and list.
- Gate routes friend commands to FriendSvr over loopback Protobuf HTTP without
  resolving a Player Shard or calling Zone.
- After a successful redeem, FriendSvr credits both Owner Zones through
  Coordinator route lookup and `POST /internal/v1/players/{id}/friend-task-credit`.
  The Actor update is retry-safe and does not bump `player_seq`.
- `deploy/migrate.ps1` applies `*.up.sql` through Docker Compose when Docker is
  present, otherwise through the local `mysql` client and `.env` fields.
- The H5 farm dashboard can generate a code, redeem a code, and list friends.

## Reproducible checks

Run from the repository root:

```powershell
buf lint
cd server
go test ./...
go vet ./...
cd ../web
npm run typecheck
cd ..
.\start-servers.ps1 -RunSeconds 2
```

Observed on 2026-09-03:

- `buf lint`: pass.
- `go test ./...`: pass.
- `go vet ./...`: pass.
- `npm run typecheck`: pass.
- memory-mode default dual-Zone startup: five backend processes Ready and
  stopped normally after two seconds; parallel build completed in 11 seconds
  on this run.

## Limitations

- No live two-account create/redeem/list E2E was run. Local migration now has a
  mysql-client fallback; credential success is still environment-dependent.
- Friend task credit is implemented and covered by unit tests; live two-account
  credit through dual Zone + MySQL is not yet evidenced.
- Visit, heartbeat, exit and steal are reserved but not implemented.
- FriendSvr intentionally has no in-memory authority; without `MYSQL_DSN`,
  existing single-player services run and friend actions are unavailable.
