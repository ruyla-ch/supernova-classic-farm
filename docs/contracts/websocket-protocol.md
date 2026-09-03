---
status: accepted
version: 1
date: 2026-07-30
owners:
  - project-owner
related:
  - ../architecture/stateful-zone-v3-architecture.md
  - ../architecture/single-player-vertical-loop-business-architecture.md
  - ../plans/2026-07-30-minimum-websocket-contract-decisions.md
  - idempotency-and-errors.md
---

# WebSocket Protocol V1

## 1. Scope

This contract defines the first H5-to-GateSvr game protocol for:

```text
authenticate
→ load player snapshot and shop
→ buy seeds
→ plant
→ fertilize
→ mature
→ harvest
→ clean plot
→ sell crop
→ update and claim chapter task
```

HTTP registration, login and WS ticket issuance belong in `http-api.md`. Storage and internal Zone RPC formats are separate contracts.

Normative words `MUST`, `MUST NOT`, `SHOULD` and `MAY` retain their usual requirements meaning.

## 2. Transport

- Endpoint uses secure WebSocket (`wss`) in production and may use `ws` locally.
- Every application frame is one complete binary Protobuf `WsEnvelope`.
- One envelope MUST NOT span multiple application messages.
- Maximum decoded WebSocket message size is 64 KiB.
- Protocol version 1 uses `protocol_version = 1` in every envelope.
- Unknown Protobuf fields are ignored and preserved where the selected runtime supports it.
- Published field numbers and enum numbers MUST NOT be reused for a different meaning.
- Browser and server builds MUST generate types from the same accepted `.proto` release.

## 3. Scalar conventions

| Meaning | Protobuf type | Rule |
|---|---|---|
| `player_id` | `uint64` | H5 uses generated `bigint` or decimal string representation, never JavaScript `number` |
| `owner_epoch`, `player_seq` | `uint64` | Monotonic within their defined scope |
| Config and price versions | `uint64` | Opaque equality tokens; H5 does not perform arithmetic |
| Item, crop, plot, chapter and task IDs | `uint32` | Stable configuration or domain IDs |
| Quantity | `uint32` | Must be greater than zero unless the field explicitly says otherwise |
| Coins | `int64` | Must remain non-negative; H5 uses `bigint` or decimal string |
| Time | `int64` | Unix milliseconds in UTC; business decisions use server time only |
| `request_id` | `string` | Client-generated canonical lowercase UUID |
| Hash | `bytes` | Raw bytes, not hexadecimal text |

All display text comes from the versioned client configuration package. IDs and error parameters are the protocol facts.

## 4. Stable enums

### 4.1 `MessageKind`

| Value | Name | Meaning |
|---:|---|---|
| 0 | `MESSAGE_KIND_UNSPECIFIED` | Invalid default |
| 1 | `REQUEST` | Client request |
| 2 | `RESPONSE` | Reply correlated by `request_id` |
| 3 | `PUSH` | Server-initiated state change |

### 4.2 `Action`

| Value | Name |
|---:|---|
| 0 | `ACTION_UNSPECIFIED` |
| 1 | `AUTH` |
| 2 | `PING` |
| 100 | `GET_PLAYER_SNAPSHOT` |
| 101 | `GET_SHOP` |
| 200 | `BUY_SEEDS` |
| 201 | `PLANT` |
| 202 | `APPLY_FERTILIZER` |
| 203 | `HARVEST` |
| 204 | `CLEAN_PLOT` |
| 205 | `SELL_CROP` |
| 206 | `CLAIM_CHAPTER_REWARD` |
| 207 | `BUY_FERTILIZER` |
| 208 | `CATCH_PEST` |
| 300 | `CREATE_FRIEND_CODE` |
| 301 | `REDEEM_FRIEND_CODE` |
| 302 | `LIST_FRIENDS` |
| 310 | `ENTER_FRIEND_FARM` |
| 311 | `FARM_HEARTBEAT` |
| 312 | `EXIT_FRIEND_FARM` |
| 320 | `APPLY_PEST_TO_FRIEND` |
| 321 | `CATCH_PEST_FOR_FRIEND` |
| 323 | `STEAL_FRIEND_CROP` |
| 1000 | `PLAYER_STATE_CHANGED` |
| 1001 | `FRIEND_FARM_CHANGED` |

Numbers 3–99, 102–199, 209–299, 303–309, 313–319, 322, 324–999 and
1002–1999 are reserved for compatible expansion. Removed values remain
reserved. Actions 300–302, 310–312, 320–321, 323 and 1001 are implemented on
`feat/mysql-friend-visit-steal`; action 208 is the implemented Owner-side pest
counterpart. This branch extension is live-verified but does not change the
accepted midterm boundary on `main`.

## 5. Common envelope

`WsEnvelope` has these logical fields:

| Tag | Field | Type | Presence and meaning |
|---:|---|---|---|
| 1 | `protocol_version` | `uint32` | Required; V1 is `1` |
| 2 | `message_kind` | `MessageKind` | Required |
| 3 | `action` | `Action` | Required and must match payload |
| 4 | `request_id` | `string` | Required for REQUEST/RESPONSE; absent for unsolicited PUSH |
| 5 | `target_player_id` | `uint64` | Required for game requests; absent for AUTH/PING |
| 6 | `state_version` | `StateVersion` | Present on snapshots, pushes, successful writes and Actor-reached business failures |
| 7 | `server_time_ms` | `int64` | Required on every RESPONSE/PUSH |
| 8 | `replayed` | `bool` | True only when returning a stored idempotency result |
| 9 | `error` | `Error` | Present only for failed RESPONSE |
| 10–99 | `payload` | Protobuf `oneof` | Exactly one payload matching kind/action |

`caller_player_id` MUST NOT appear in a request. GateSvr obtains it from the authenticated connection.

For the single-player slice, all game requests MUST set `target_player_id` to the authenticated player. Future friend actions may target another player subject to authorization.

### 5.1 `StateVersion`

| Tag | Field | Type |
|---:|---|---|
| 1 | `owner_epoch` | `uint64` |
| 2 | `player_seq` | `uint64` |

Comparison rules:

1. Higher `owner_epoch` replaces all state from a lower epoch, even if `player_seq` is lower.
2. Within one epoch, a delta with `player_seq = local + 1` is applied.
3. A delta with `player_seq <= local` is a duplicate or historical replay and its state patch is ignored.
4. A delta with `player_seq > local + 1` is a gap; the client pauses deltas and requests a full snapshot.

## 6. Authentication and heartbeat

### 6.1 AUTH

AUTH MUST be the first non-heartbeat request and must arrive within 10 seconds of connection establishment.

`AuthRequest`:

| Field | Type | Rule |
|---|---|---|
| `ws_ticket` | `string` | Required, short-lived and single-use |

Successful `AuthResponse`:

| Field | Type | Rule |
|---|---|---|
| `player_id` | `uint64` | Identity bound to this connection |
| `heartbeat_interval_ms` | `uint32` | V1 default `30000` |
| `client_config_version` | `uint64` | Required client display-config version |
| `client_config_url` | `string` | Immutable HTTP(S) object URL |
| `client_config_sha256` | `bytes` | Hash of exact downloaded bytes |
| `protocol_min` | `uint32` | Minimum accepted protocol |
| `protocol_max` | `uint32` | Maximum accepted protocol |

Authentication consumes the ticket. Reconnect obtains a new ticket through the existing HTTP Session. A new login revokes the old Session and closes the old WebSocket.

Before AUTH succeeds, GateSvr accepts only AUTH and PING. Repeated other actions are a policy violation.

### 6.2 PING

`PingRequest`:

| Field | Type | Meaning |
|---|---|---|
| `ping_id` | `uint64` | Connection-local sequence |
| `client_sent_at_ms` | `int64` | Echoed for RTT measurement; never trusted as business time |

`PingResponse` echoes both fields. The envelope `server_time_ms` supplies the server clock sample. GateSvr handles PING without routing to a Player Actor.

The server may close a connection after two consecutive heartbeat intervals without receiving any frame. Normal business traffic counts as liveness.

## 7. Client configuration and shop

The client-visible configuration is an immutable, versioned static package downloaded over HTTP. Development may serve it from a local static server; production may use a CDN.

The package contains names, descriptions, image URLs and display-only rules. It is not transaction authority. H5 MUST verify the SHA-256 hash and cache by version.

`GetShopRequest` is empty.

`GetShopResponse`:

| Field | Type | Meaning |
|---|---|---|
| `server_config_version` | `uint64` | Zone snapshot used for this response |
| `entries` | repeated `ShopEntryView` | All active seed, fertilizer, and crop-buyback entries, sorted by `shop_entry_id` |
| `crops` | repeated `CropCatalogEntryView` | Enabled crop presentation and quote mapping, sorted by `crop_id` |

`ShopEntryView`:

| Field | Type |
|---|---|
| `shop_entry_id` | `uint32` |
| `item_id` | `uint32` |
| `unit_price` | `int64` |
| `price_version` | `uint64` |
| `enabled` | `bool` |

`CropCatalogEntryView`:

| Field | Type | Meaning |
|---|---|---|
| `crop_id` | `uint32` | Stable crop identity |
| `name` | `string` | Client display name |
| `seed_item_id` | `uint32` | Seed inventory identity used by `PLANT` |
| `crop_item_id` | `uint32` | Harvested inventory identity used by `SELL_CROP` |
| `maturity_seconds` | `uint64` | Display-only base maturity duration |
| `base_yield` | `uint32` | Display-only base yield |
| `seed_unit_price` | `int64` | Current seed quote copied from the active shop entry |
| `seed_price_version` | `uint64` | Version required by `BUY_SEEDS` |
| `seed_shop_entry_id` | `uint32` | Entry required by `BUY_SEEDS` |
| `sell_unit_price` | `int64` | Current crop buyback quote |
| `sell_price_version` | `uint64` | Version required by `SELL_CROP` |

The catalog is client presentation and command-selection data. The pinned Zone
configuration remains authoritative for planting, prices, growth, yield, and
steal fields.

The client buys a quoted `shop_entry_id`, not a bare item ID. This preserves the identity of a quote when one item later has normal, discount or event entries.

## 8. Player read model

The WebSocket Player Snapshot is a client projection, not the internal Dirty checkpoint. It excludes password/session data, recent idempotency records, Outbox internals, Dirty flags and internal decimal settlement fields.

### 8.1 `PlayerSnapshot`

| Field | Type | Meaning |
|---|---|---|
| `player_id` | `uint64` | Snapshot owner |
| `coin_balance` | `int64` | Current authoritative balance |
| `inventory` | repeated `ItemStackView` | Non-zero stacks only |
| `plots` | repeated `PlotView` | Every plot, stable `plot_id` order |
| `current_chapter` | `ChapterView` | Current chapter and task progress |
| `server_config_version` | `uint64` | Zone config snapshot at read time |

The envelope supplies `state_version` and `server_time_ms`.

`GetPlayerSnapshotRequest` is empty. `GetPlayerSnapshotResponse` contains one `PlayerSnapshot`.

GateSvr MUST register the subscription and buffer changes before requesting the Actor snapshot. After sending the snapshot, it sends buffered deltas newer than the snapshot in sequence order and drops older duplicates.

### 8.2 `ItemStackView`

| Field | Type |
|---|---|
| `item_id` | `uint32` |
| `quantity` | `uint32` |

Zero quantity is represented by removal from inventory, not a zero-valued stack.

### 8.3 `PlotView`

`PlotState` values are `UNSPECIFIED = 0`, `EMPTY = 1`, `GROWING = 2`, `MATURE = 3`, `NEED_CLEANUP = 4`.

| Field | Type | Presence |
|---|---|---|
| `plot_id` | `uint32` | Always |
| `plot_state` | `PlotState` | Always |
| `crop_id` | `uint32` | GROWING/MATURE/NEED_CLEANUP |
| `crop_config_version` | `uint64` | GROWING/MATURE/NEED_CLEANUP |
| `planted_at_ms` | `int64` | GROWING/MATURE/NEED_CLEANUP |
| `estimated_mature_at_ms` | `int64` | GROWING only |
| `harvestable_quantity` | `uint32` | MATURE only |
| `fertilizer_effect` | `EffectView` | Active effect only |
| `pest_effect` | `EffectView` | Active effect only |

H5 derives visual seed/sprout/half-grown stages from server time and display configuration. Those stages are not authoritative state and receive no Push.

`EffectView` contains `effect_instance_id` as UUID string, `effect_item_id`, `effect_config_version`, `start_at_ms`, `end_at_ms` and optional `source_player_id`. Internal decimal modifiers are not sent; display text comes from client config.

### 8.4 `ChapterView`

`ChapterStatus` values are `UNSPECIFIED = 0`, `IN_PROGRESS = 1`, `CLAIMABLE = 2`, `CLAIMED = 3`.

| Field | Type |
|---|---|
| `chapter_id` | `uint32` |
| `status` | `ChapterStatus` |
| `tasks` | repeated `TaskProgressView` |

`TaskProgressView` contains `task_id`, `current_value`, `target_value` and `completed`. Progress values use `uint32`.

## 9. State patches

Successful writes and state-change Pushes use one common replacement patch:

`PlayerStatePatch`:

| Field | Type | Apply rule |
|---|---|---|
| `coin_balance` | optional `int64` | Replace when present |
| `inventory_upserts` | repeated `ItemStackView` | Replace stack by `item_id` |
| `inventory_removed_item_ids` | repeated `uint32` | Remove these stacks |
| `plot_upserts` | repeated `PlotView` | Replace plot by `plot_id` |
| `current_chapter` | optional `ChapterView` | Replace whole current chapter |

Absent fields mean unchanged. The client applies a patch only after the `state_version` ordering checks in section 5.1.

The command response is the authoritative patch for the initiating connection. Other subscribers receive the same state change as PUSH. GateSvr SHOULD suppress a duplicate Push to the initiating connection; clients MUST still tolerate one by version deduplication.

## 10. Write commands

All write requests require a new UUID for a new intent. Automatic retry uses the same UUID and identical semantic payload.

All successful writes:

```text
pin server_now and one Zone config snapshot
→ validate against current Actor state
→ atomically apply player/farm/task changes
→ player_seq increases exactly once
→ save idempotency result and any Outbox
→ mark Dirty
→ return receipt + PlayerStatePatch
```

### 10.1 BUY_SEEDS

`BuySeedsRequest`:

| Field | Type |
|---|---|
| `shop_entry_id` | `uint32` |
| `quantity` | `uint32` |
| `expected_price_version` | `uint64` |

Server derives item and price from the pinned configuration. Client-supplied price is forbidden.

`BuySeedsResponse`:

| Field | Type |
|---|---|
| `shop_entry_id` | `uint32` |
| `item_id` | `uint32` |
| `quantity` | `uint32` |
| `unit_price` | `int64` |
| `total_price` | `int64` |
| `patch` | `PlayerStatePatch` |

The patch contains coin balance, changed seed stack and current chapter.

### 10.2 BUY_FERTILIZER

`BuyFertilizerRequest` and `BuyFertilizerResponse` have the same fields as
`BUY_SEEDS`. Quantity is 1 through 50 and the per-item stack limit is 300.
The pinned shop entry must identify a fertilizer. The server derives the item
and price, deducts `unit_price × quantity`, and returns the coin balance,
changed fertilizer stack and current chapter in the patch. This command does
not advance the chapter's seed-purchase task.

### 10.3 PLANT

`PlantRequest` contains `plot_id` and `seed_item_id`.

Server maps the seed item to the crop and freezes crop configuration fields. The client does not submit `crop_id`, growth rate, maturity, yield or timestamps.

`PlantResponse` contains `consumed_seed_item_id` and a patch with the changed seed stack/removal, changed plot and current chapter.

### 10.4 APPLY_FERTILIZER

`ApplyFertilizerRequest` contains `plot_id` and `fertilizer_item_id`.

The Actor settles growth to server time under the old rate before applying the new effect. Active fertilizer causes the whole command to fail without consuming the item.

`ApplyFertilizerResponse` contains `consumed_fertilizer_item_id`, `effect_instance_id` and a patch with inventory, plot and current chapter.

### 10.5 HARVEST

`HarvestRequest` contains `plot_id`.

Harvest is all-or-nothing. If the complete yield cannot fit, no crop is added, the plot remains MATURE and task progress is unchanged.

`HarvestResponse` contains `crop_item_id`, `harvested_quantity` and a patch with inventory, the NEED_CLEANUP plot and current chapter.

### 10.6 CLEAN_PLOT

`CleanPlotRequest` contains `plot_id`.

V1 self-cleaning consumes no item, grants no reward and advances no task. A
`NEED_CLEANUP` plot may be cleaned before its chapter reward is claimed.

`CleanPlotResponse` contains a patch with the EMPTY plot.

### 10.7 CATCH_PEST

`CatchPestRequest` contains `plot_id`. It targets the authenticated Owner's
Actor and is valid only for a `GROWING` plot with a pest active at pinned
server time.

Before removal, the Actor settles growth exactly through server time under the
old fertilizer/pest rates. It then clears the pest and recomputes
`estimated_mature_at_ms`. Catching is free: it consumes no inventory, grants
no item, coin or reward, and advances no task.

`CatchPestResponse` contains a patch with the changed private `PlotView`.

### 10.8 SELL_CROP

`SellCropRequest` contains:

| Field | Type | Rule |
|---|---|---|
| `crop_item_id` | `uint32` | Required |
| `expected_price_version` | `uint64` | Required |
| `amount` | `oneof` | Exactly one of `quantity` or `sell_all = true` |

For `sell_all`, the Actor resolves the current full stack quantity at execution time. A replay returns the first resolved quantity and result.

`SellCropResponse` contains `crop_item_id`, `sold_quantity`, `unit_price`, `total_price` and a patch with inventory, coin balance and current chapter.

### 10.9 CLAIM_CHAPTER_REWARD

`ClaimChapterRewardRequest` contains `chapter_id`. Explicit chapter identity prevents a stale screen from accidentally claiming a later chapter.

`ClaimChapterRewardResponse`:

| Field | Type |
|---|---|
| `chapter_id` | `uint32` |
| `coin_granted` | `int64` |
| `items_added_to_inventory` | repeated `ItemStackView` |
| `items_pending_mail` | repeated `ItemStackView` |
| `patch` | `PlayerStatePatch` |

`items_pending_mail` means a pending `CreateRewardMail` Outbox event was recorded atomically with the Actor's reward-claim state. It MUST NOT claim that the Mail Service has already created or delivered the mail. Under V3 asynchronous Dirty writeback, the event becomes database-durable only after the checkpoint and relational Outbox row commit; an acknowledged but unflushed claim and event may roll back together after abnormal Zone loss.

## 11. Push

The accepted V1 owner loop uses `PLAYER_STATE_CHANGED`. The implemented friend
branch extension adds `FRIEND_FARM_CHANGED` without changing the accepted
`main` boundary.

`PlayerStateChangedPush`:

| Field | Type | Meaning |
|---|---|---|
| `reason` | `StateChangeReason` | `BUY_SEEDS`, `BUY_FERTILIZER`, `PLANT`, `APPLY_FERTILIZER`, `MATURED`, `HARVEST`, `CLEAN_PLOT`, `SELL_CROP`, `CLAIM_CHAPTER_REWARD`, `FRIEND_STEAL`, `APPLY_PEST_TO_FRIEND`, `CATCH_PEST_FOR_FRIEND`, `CATCH_PEST` |
| `caused_by_request_id` | optional `string` | Present for command-caused changes |
| `patch` | `PlayerStatePatch` | Authoritative delta |

Natural maturity:

```text
settle growth to authoritative maturity time
→ plot becomes MATURE
→ effects end
→ player_seq++
→ mark Dirty
→ PUSH changed PlotView
```

If several plots mature while activating an offline Actor, the Actor processes them in stable `plot_id` order. The first snapshot request receives the final snapshot after activation settlement, not intermediate Pushes.

## 12. Concurrency and routing

- A connection may have multiple in-flight requests.
- `request_id` correlates responses; clients MUST NOT rely on response arrival order.
- One Player Actor serializes all commands in Mailbox order.
- GateSvr routes by `target_player_id`.
- On internal `NOT_OWNER`, GateSvr refreshes committed ShardMap and retries with the same `request_id`.
- `NOT_OWNER` is not exposed as a normal client business error.
- Commands do not carry global `expected_player_seq`; each command revalidates its own business preconditions.

## 13. Reconnect and resynchronization

Reconnect sequence:

```text
HTTP Session obtains new one-time ws_ticket
→ open WebSocket
→ AUTH
→ ensure client configuration version is cached
→ GET_PLAYER_SNAPSHOT
→ GET_SHOP when opening shop
```

The client replaces its complete local player view when:

- no local snapshot exists;
- `owner_epoch` changes;
- `player_seq` has a gap;
- local state application fails validation.

There is no V1 persistent delta replay endpoint.

## 14. Connection closing

| Close code | Meaning |
|---:|---|
| 1000 | Normal shutdown |
| 1002 | Malformed Protobuf or invalid envelope tuple |
| 1009 | Message exceeds 64 KiB |
| 1011 | Fatal server connection error |
| 4401 | AUTH timeout, invalid ticket or expired Session |
| 4406 | Unsupported protocol version |
| 4409 | Session revoked by a newer login |
| 4429 | Persistent abuse or connection-level rate violation |

Normal business errors such as insufficient coins, full inventory, immature crop, active fertilizer or changed price return a failed RESPONSE and keep the connection open.

## 15. Implemented MySQL friend extension

This section is implemented and live-verified on
`feat/mysql-friend-visit-steal`; it does not change the accepted midterm
boundary on `main`.

- `CREATE_FRIEND_CODE`, `REDEEM_FRIEND_CODE`, and `LIST_FRIENDS` set
  `target_player_id` to the authenticated caller.
- `CreateFriendCodeRequest` and `ListFriendsRequest` are empty.
  `CreateFriendCodeResponse` contains `code`, `created_at_ms`, and
  `expires_at_ms`.
- `RedeemFriendCodeRequest` contains `code`.
  `RedeemFriendCodeResponse` contains the peer `FriendView` and
  `newly_created`.
- `FriendView` contains only `player_id`, `account_name`, and relationship
  `created_at_ms`.
- Friend codes are opaque ASCII strings, expire after 24 hours, and a new code
  replaces the caller's prior current code. A player cannot redeem their own
  code.
- One unordered player pair has at most one active relationship. Redeeming an
  already-active relationship succeeds with `newly_created=false`.
- `LIST_FRIENDS` returns at most 100 active friends ordered by
  `(created_at_ms, player_id)`.
- Gate forwards actions 300–302 to FriendSvr over loopback HTTP using the
  original Protobuf envelope and a trusted caller header. FriendSvr never
  trusts a caller identity inside the payload.
- Actions 310–312, 320–321, and 323 set `target_player_id` to the
  authenticated Visitor; the payload carries the farm Owner ID. Gate routes
  them to the Visitor's Zone.
- `ENTER_FRIEND_FARM` carries `owner_player_id` and returns a 16-byte
  `visit_id`, `expires_at_ms`, and `FarmVisitSnapshot`.
- `FARM_HEARTBEAT` and `EXIT_FRIEND_FARM` carry `owner_player_id` plus the
  16-byte `visit_id`. Heartbeat returns the renewed expiry.
- `FarmVisitSnapshot` contains `owner_player_id`, `owner_state_version`, and
  ordered `PublicPlotView` values. It excludes Owner coins, inventory, chapter,
  tasks, idempotency results and per-visitor steal history.
- `PublicPlotView` contains `plot_id`, `plot_state`, `crop_id`, `crop_item_id`,
  `planted_at_ms`, `estimated_mature_at_ms`, `harvestable_quantity`,
  `steal_count`, `can_steal`, `steal_quantity`, and `pest_active`. It exposes
  only whether a pest is active; pest identity, source, frozen modifier and
  interval remain private.
- `APPLY_PEST_TO_FRIEND` carries `owner_player_id`, the exact 16-byte
  `visit_id`, `plot_id`, and `pest_id`. `CATCH_PEST_FOR_FRIEND` carries the
  same fields except `pest_id`. Both set envelope `target_player_id` to the
  authenticated Visitor, require a current visit lease and a current mutual
  friendship, then route to the farm Owner's current Zone.
- Successful friend apply/catch returns the changed Owner `PublicPlotView`.
  Its envelope has no `state_version`: the only mutated business aggregate is
  the Owner Actor, and the Owner version is carried by the resulting private
  and public Pushes.
- The current development configuration authority defines only `pest_id=1`:
  `config_version=1`, modifier `-0.3`, duration `120000ms`, enabled. Apply is
  valid only on `GROWING` with no active pest. It settles the old rate exactly
  to server time, creates a frozen `[start_at_ms, end_at_ms)` PEST effect with
  `source_player_id`, then recomputes maturity. Fertilizer and pest modifiers
  are additive over their exact overlapping intervals.
- Owner and friend catch are valid only while the plot is `GROWING` and the
  pest remains active after settlement to server time. Catch then clears the
  effect and recomputes maturity. The Visitor who applied the current pest
  cannot catch it; the Owner or a different currently authorized mutual
  friend can.
- All three pest actions are free: they consume no item or inventory, grant no
  item, coin or reward, and advance no task.
- Invalid/zero fields return `INVALID_ARGUMENT`. Visit and friendship failures
  return `VISIT_NOT_FOUND`/`VISIT_EXPIRED` or `NOT_MUTUAL_FRIEND`.
  Owner-Actor validation returns `PLOT_NOT_FOUND`, `PLOT_STATE_CONFLICT`,
  `PEST_ALREADY_ACTIVE`, `PEST_NOT_ACTIVE`, `PEST_SOURCE_FORBIDDEN`,
  `CONFIG_ENTRY_DISABLED`, or retryable `CONFIG_UNAVAILABLE` as applicable.
  At the exact pest end boundary, settlement expires the effect and catch
  deterministically returns `PEST_NOT_ACTIVE` without changing `player_seq`.
- `STEAL_FRIEND_CROP` carries `owner_player_id`, `visit_id`, `plot_id`,
  `expected_crop_item_id`, `expected_planted_at_ms`, and
  `expected_steal_quantity`.
- Successful steal returns the item and quantity, a Visitor `PlayerStatePatch`,
  and the changed Owner `PublicPlotView`. Its envelope `state_version` belongs
  only to the Visitor Actor.
- Visit leases last 90 seconds and are process-local. Heartbeat renews them;
  restart invalidates them.
- Owner public-plot mutations emit `FRIEND_FARM_CHANGED` best-effort to each
  current visit lease. Its envelope targets the Visitor but has no envelope
  `state_version`; the payload carries `owner_player_id`, the exact 16-byte
  `visit_id`, `owner_state_version`, and ordered changed `PublicPlotView`
  values.
- The H5 applies a friend-farm Push only when Owner and visit ID still match
  the current visit. It ignores equal or older Owner versions. A higher
  `player_seq` need not be contiguous because private Owner mutations can
  advance the Player Actor without producing a public farm change.
- Every plot mutation also emits `PLAYER_STATE_CHANGED` to the online Owner
  using the private `PlotView` projection and the same Owner state version.
  Pest mutations use reasons `APPLY_PEST_TO_FRIEND`,
  `CATCH_PEST_FOR_FRIEND`, or `CATCH_PEST`. The same event fans out a
  `FRIEND_FARM_CHANGED` public projection to every current visit lease.
  Replayed or failed friend steals and pest actions emit neither a duplicate
  Owner Push nor a duplicate Visitor Push. Owner command responses and their
  Push may race, so the H5 treats an equal state version as an already-applied
  duplicate.
- Owner-side visit registration precedes snapshot capture. Therefore a Push
  that races before the ENTER response may be ignored by the H5, but the
  returned snapshot is captured after registration and covers that change.
- Zone-to-Zone and Zone-to-Friend calls use loopback HTTP with dedicated
  Protobuf messages. This extension does not declare or use gRPC services.

## 16. Validation checklist

Implementation tests MUST prove:

1. binary Protobuf round-trip between generated Go and TypeScript types;
2. AUTH timeout, single-use ticket and duplicate-login closure;
3. request/response correlation with concurrent in-flight requests;
4. all command payloads reject missing, zero or illegal fields;
5. no client field can set price, balance, maturity, yield or task progress;
6. state patch application, duplicate suppression, gap recovery and epoch replacement;
7. snapshot subscription buffering has no snapshot-to-Push race;
8. 64 KiB enforcement occurs before unbounded allocation;
9. PING remains at GateSvr and never activates an Actor;
10. every business failure preserves connection and returns the documented error behavior.
