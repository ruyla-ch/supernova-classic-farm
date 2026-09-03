---
status: verified
updated: 2026-09-03
---

# Dev UI and multi-crop port evidence

## Implemented boundary

- The H5 uses a login-first single form and registers only when login returns
  invalid credentials and registration confirms that the account is absent.
  Password validation remains the current 12-character backend rule.
- A tab marker restores a matching HTTP Session after reload. Logout closes an
  active friend visit, disconnects WebSocket, invalidates the HTTP Session, and
  clears local state.
- The trimmed shell exposes account, shop, tasks, inventory, and friends
  drawers. Compendium, mail, pet, gift, pest, offline-visitor, and red-dot
  features were not ported.
- `GET_SHOP` returns 11 crops in stable `crop_id` order and 23 active shop
  entries in stable `shop_entry_id` order. Plant, buy, and sell commands use
  catalog identities and quote versions.
- New planting rounds freeze steal quantity 1, protected Owner yield
  `ceil(base_yield / 2)`, and maximum steals equal to the remaining stealable
  yield. Previously planted rounds keep their checkpoint-frozen fields.
- Newly created in-memory states and MySQL registration checkpoints contain 16
  ordered plots. Checkpoint activation does not backfill a stored four-plot
  player.
- The owner farm renders the authoritative plot count in a four-column layout,
  and owner/friend farms share mature artwork for the 11 crop identities.
  Manual friend-list refresh has separate loading and error state.

## Automated verification

Run on Windows from `E:\workspace\supernova-classic-farm`:

```text
buf lint
buf generate
cd server
go test ./...
go vet ./...
cd ../web
npm run typecheck
npm run build
cd ..
git diff --check
.\tests\e2e\run-mysql-friend-slice.ps1 -ServicePortOffset 10000
```

Observed results:

- Protobuf lint and generation: pass.
- Full Go tests and vet: pass.
- Vue TypeScript check and production build: pass; Vite transformed 139
  modules and produced the production bundle.
- Repository diff whitespace check: pass.
- Dual-Zone MySQL friend mutation phase: pass with players 432 on Zone A and
  433 on Zone B. Create/redeem/list, visit heartbeat/exit, direct steal,
  Visitor inventory credit, replay idempotency, and MySQL relation checks all
  passed.
- Full-stack restart recovery: pass. Owner plot mutation, Visitor inventory,
  and friend relation recovered, while the old visit ID was rejected.
- The E2E used ports 18080–18085 because an independently running development
  stack occupied 8080–8085. The new `ServicePortOffset` option keeps that
  verification isolated without stopping the development stack.

## Limitations

- This repository has no frontend unit-test or browser-test harness. H5
  verification in this change is TypeScript compilation and production build,
  not a browser interaction claim.
- A manual browser run should still check the login/register fallback, Session
  reload, all five drawers, 4/16-plot responsive layout, crop selection, friend
  refresh, and friend-farm artwork at 320 CSS pixels.
