---
status: active
date: 2026-09-03
scope: MySQL friend Slice 1 live dual-Zone gate
---

# MySQL friend Slice 1 live evidence

## Environment

- Local MySQL Community Server 8.4.11.
- Migration versions 1–6 present; `friend_codes` and `friend_relations` exist.
- Coordinator in `static-dual-zone` mode with Zone A on 8082 and Zone B on
  8084.
- LoginSvr, GateSvr and MySQL-only FriendSvr on 8085.

## Reproduction

From the repository root:

```powershell
.\dev.ps1 -Action migrate
.\tests\e2e\run-mysql-friend-slice.ps1
```

The runner builds isolated binaries, starts all six backend processes, creates
fresh accounts until two players belong to different Owners, exercises the
friend protocol, queries the committed relation in MySQL, and stops all
processes.

## Observed result

The 2026-09-03 run passed with player 409 on Zone A and player 410 on Zone B:

- Action 300 generated one 32-character friend code.
- Action 301 created one reciprocal `ACTIVE` relation.
- Action 302 returned the other account from both players.
- Repeating Action 301 returned `newly_created=false`.
- MySQL contained exactly one ordered relation for the pair.
- FriendSvr successfully resolved and called both different Owner Zones for
  retry-safe task credit.
- The Windows `MySQL84` service was then restarted. After it returned to
  Running, querying as the application user still returned exactly one
  `ACTIVE` relation for players 409 and 410.

After adding the E2E, `go test ./...`, `go vet ./...`, and
`npm run typecheck` passed.

## Task-credit validation boundary

- The live accounts were still in chapter one. Cross-Zone credit calls were
  accepted; chapter-two task mutation and retry idempotency are covered by
  `social_task_credit_test.go`, not by this live run.
