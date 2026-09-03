---
status: completed
date: 2026-09-03
owner: project-owner
branch: feat/mysql-friend-visit-steal
related:
  - ../context/CURRENT.md
  - ../contracts/websocket-protocol.md
  - ../contracts/data-model.md
  - ../contracts/idempotency-and-errors.md
  - 2026-08-03-static-dual-zone-mysql-fence-plan.md
  - 2026-09-03-mysql-http-friend-visit-direct-steal-review.md
---

# MySQL 好友访问与直调偷菜计划

## Goal

在 `main` 的单玩家闭环和静态双 Zone + MySQL 基线上，做一个**可演示、可重启**的最小社交切片：

```text
两个账号互加好友
-> 访客进入对方农场，看到公开地块
-> 对成熟作物 STEAL_FRIEND_CROP
-> 访客背包增加作物，农场主地块偷取计数增加
```

权威验收环境是 **双 Zone + MySQL**。内存模式只用于无库开发对照，不能单独作为完成标准。

`main` 分支保持中期答辩基线，本计划只在 `feat/mysql-friend-visit-steal` 上执行。

## Why this shape

`master` 上的好友实现绑了 Tcaplus、内部 gRPC/HMAC、以及一度存在的
`FriendInteraction` Saga。那些选择解决的是 3000 万 DAU 与崩溃窗口，不是本切片的演示目标。

本切片从 `master` **只借鉴**：

- WebSocket Action 编号与命令形状（300/301/302/310/311/312/323）；
- 互为好友才可访问；
- 公开快照不含金币、背包、任务；
- 种植时冻结可偷参数；
- 偷菜后期的**访客 Zone → 农场主 Actor 同步直调**，不是 phase 5 的 Saga 表和对账器。

本切片**重写**存储和 Zone 间调用：MySQL 表 + `main` 已有的 loopback HTTP，不引入 Tcaplus，不把 Gate↔Zone 整迁到 gRPC。

## Safety boundary

- 单玩家 `player_seq=8` 命令、Dirty、Fence、双 Zone 路由语义不得回退。
- 好友关系的权威不在 Player checkpoint 里，而在独立 MySQL 表；列表可以是同一张表上的查询，不必先做可修复投影。
- 普通单玩家命令仍是 `validate → 改 Actor 内存 → Dirty → 异步刷盘`。
- 偷菜跨两个 Actor：农场主地块变化以农场主 Actor 串行结果为准；访客背包在访客 Actor 收到农场主成功回包后再改。两个进程之间没有 Saga。农场主已成功、访客进程在写背包前崩溃时，作物可能已从地块扣走但未进入访客背包。原型接受这条窗口，与 V3 承认异常退出可能丢掉未刷 Dirty 同类，**不得**为此加 `FriendInteraction` 表。
- 访问会话只活在内存（访客 Zone 的 `visit_id`）。Zone 重启后旧 `visit_id` 失效，H5 重新 `ENTER`。

## Scope by slice

每片独立提交、独立验收。未通过不得开下一片。

### Slice 0 — 契约草案（仍为 proposed，直到本计划被 owner 口头确认可写代码）

状态（2026-09-03）：已完成。Owner 已确认继续开发；Action、payload、
错误码和 MySQL 关系模型以 proposed extension 形式写入现有契约，未改变
`main` 的 accepted 中期边界。

1. 在 `websocket-protocol.md` 增加好友 Action 与 payload 草案，编号与 `master` 对齐以免对照时错位：

   | Value | Name |
   |---:|---|
   | 300 | `CREATE_FRIEND_CODE` |
   | 301 | `REDEEM_FRIEND_CODE` |
   | 302 | `LIST_FRIENDS` |
   | 310 | `ENTER_FRIEND_FARM` |
   | 311 | `HEARTBEAT_FRIEND_FARM` |
   | 312 | `EXIT_FRIEND_FARM` |
   | 323 | `STEAL_FRIEND_CROP` |

   本切片不实现 320–322（投虫 / 捉虫 / 帮忙清）。
2. 在 `data-model.md` 增加 MySQL 好友表与 checkpoint 地块偷取字段草案。
3. 不改 ADR；不把本切片写成生产好友架构。

### Slice 1 — 加好友，不进农场

状态（2026-09-03）：已完成。player 409 / Zone A
与 player 410 / Zone B 实测通过生成、兑换、双方列表、重复兑换幂等、MySQL
关系查询，以及双方 Owner Zone 任务调用；Windows `MySQL84` 服务重启后关系
仍在。完整 Go 测试、vet 和 H5 typecheck 通过。live 账号仍在第一章，因此
第二章任务值推进由单元测试覆盖。证据见
`../evidence/2026-09-03-mysql-friend-slice-1-live.md`。

**存储（新 migration，建议 `000006_friends.up.sql`）：**

- `friend_codes`：`player_id` 当前有效分享码，唯一。
- `friend_relations`：有序对 `(player_low, player_high)` 一条互惠关系，状态 `ACTIVE`，创建时间；好友上限 100（按每人当前 ACTIVE 边计数）。

**进程：** 增加最小 `FriendSvr`（本机 HTTP，例如 `:8085`）。Gate 把 300–302 转到 FriendSvr，不进 Actor。关系写成功后，FriendSvr 对双方 Owner Zone 发一次幂等「加好友任务推进」（失败可重试，不得留下半边 `ACTIVE` 关系）。

**验收：**

- 兑换成功后双方 `LIST_FRIENDS` 可见；
- 重复兑换同一码幂等；
- 超过 100 拒绝；
- 非互惠半写入不可观察；
- MySQL 重启后关系仍在；
- 现有单玩家 E2E 仍通过。

### Slice 2 — 进入农场（只读公开快照）

状态（2026-09-03）：已实现。HTTP/Protobuf mutual check、Visitor/Owner
双边内存 registry、公开 Actor 快照、ENTER/HEARTBEAT/EXIT 和 Gate 路由均已
接通；live 双 Zone MySQL 路径通过。

命令走**访客自己的 Zone**（与 `master` 相同：Gate 不直连农场主）：

1. 访客 Zone 问 FriendSvr：是否互为好友。
2. 访客 Zone 按 Coordinator/Gate 已有路由找到农场主 Zone。
3. 农场主 Zone 在农场主 Actor 邮箱里投影公开快照并登记内存访客。
4. 返回 `visit_id`、过期时间和公开地块（作物、成熟、`can_steal`）。禁止下发金币、背包、任务、机会数。

`HEARTBEAT` 续期；`EXIT` 或过期注销。伪造 `visit_id`、非好友、过期一律拒绝。

农场主 Zone 重启或 Shard 迁移后，旧 visit 失效；H5 再 `ENTER` 换完整快照。

**本片不做** `FarmViewPatch`。偷菜或种植后若要刷新，重新 `ENTER`。

**验收：** 非好友进不去；两个玩家分属 Zone A/B 时可以进；重启后必须重新 ENTER。

### Slice 3 — 直调偷菜 + 最小 H5

状态（2026-09-03）：已实现。Owner 冻结规则、同访客每轮一次、Owner/Visitor
双 checkpoint 幂等、Visitor 背包与任务提交以及 H5 好友农场页面已接通。
全量 Go 测试、vet、H5 typecheck/build 和 live 跨 Zone 偷取/replay 通过。

种植时在地块上冻结：`base_yield`、`steal_quantity`、`max_steal_times`、`protected_owner_yield`。开发配置可先用 `steal_quantity=1`、`max_steal_times=2`、`protected_owner_yield=1`。

成功路径：

```text
访客 REQUEST STEAL_FRIEND_CROP
-> 访客 Zone 校验 visit / 互为好友 / 请求幂等
-> HTTP 调农场主 Zone ApplyStealOnOwner（进入农场主 Actor 邮箱）
-> 农场主：成熟、次数、保底、同一访客本轮未偷过 → 扣可偷额度，Dirty，返回数量与作物
-> 访客 Actor：仓库可容纳则加背包并推进偷菜任务，否则整请求失败
   （若农场主已成功而访客加背包失败：见 Safety boundary，记录为已知限制，本切片不补偿）
```

拒绝：非成熟、次数用尽、满仓、visit 无效、非好友。同一 `request_id` 不重复扣地块、不加作物。

H5：好友码/列表、进入对方农场、成熟地块上的偷按钮。不重做 `master` 的完整游戏壳。

### Slice 4 — 双账号 MySQL E2E

状态（2026-09-03）：已完成。player 426/Zone A 与 player 428/Zone B 实测
通过好友、访问、heartbeat、直调偷菜、Visitor inventory、同 ID replay 和
exit；随后完整重启六个服务，双方 checkpoint 与关系恢复且旧 visit 失效。
证据见
`../evidence/2026-09-03-mysql-friend-visit-steal.md`。

`start-servers.ps1`（默认双 Zone）+ `MYSQL_DSN`，两个账号落到不同 Owner，走完加好友 → 进入 → 种/成熟 → 偷 → 停进程再启，关系、地块偷次数、访客背包一致。证据写入 `docs/evidence/2026-09-03-mysql-friend-visit-steal.md`。

## Implementation notes

- 启动脚本默认双 Zone（本分支已改）；FriendSvr 在 Slice 1 接入 `start-servers.ps1`。
- Zone 间与 FriendSvr 调用保持回环 HTTP，身份用本机共享 secret 或现有内部约定即可，不新开 gRPC 工程。
- 对照 `master` 时只读，禁止把 Tcaplus store、`server/internal/interaction` Saga、HMAC interceptor 整文件拷进本分支。
- 优先对照 `master` 的直调偷菜，而不是 `docs/evidence/2026-08-06-friend-phase-5-steal-saga.md`。

## Verification

每片至少：

- `go test ./...`、`go vet ./...`；
- 触及 H5 时 `cd web && npm run typecheck`（若本分支 typecheck 仍指向空配置则先修到能检查 `web/src`）；
- 单玩家闭环冒烟不回归。

Slice 4 另加双 Zone MySQL 双账号路径。浏览器双窗口可作为 owner 手工补充，不阻塞 Slice 4 的协议 E2E。

## Non-goals

- Tcaplus、kind、动态 Zone、Coordinator 迁移 worker。
- 把现有 Gate↔Zone 命令/Push 迁到 gRPC。
- `FriendInteraction` Saga、对账 ticker、三崩溃窗口补偿。
- 投虫、捉虫、帮忙清理、宠物护卫。
- MailSvr、礼物、红点、分享链接首友奖励。
- 公开地块增量 `FarmViewPatch`（可在本计划完成后再开后续计划）。
- 生产级跨 Gate 访问、Visitor Presence 广播、好友容量声明。

## Stop conditions

- 单玩家 MySQL 重启恢复或双 Zone 路由 E2E 变红：先修回归，不加好友代码。
- 实现开始依赖 Tcaplus SDK、新的 gRPC runtime 或 interaction 状态机：停下来回到本计划边界。
- 偷菜需要「农场主与访客同时提交或回滚」才能演示：停下来，不要偷偷加 Saga；改为文档化限制或缩小演示脚本。

## Risks

| Risk | Handling |
|---|---|
| 从 `master` 顺手拷入传输层 | Slice 0 写明 HTTP-only；code review 拒绝 gRPC 整迁 |
| 好友表与 checkpoint 双写不一致 | 关系只在 FriendSvr/MySQL；Actor 只收幂等任务推进 |
| 跨 Zone 偷菜超时 | 访客侧记 `REQUEST_OUTCOME_UNKNOWN`，保留 `request_id`；不自动补偿农场主 |
| 两玩家被路由到同一 Zone | E2E 显式选分属 A/B 的 `player_id`，或注册直到分片不同 |

## Open questions (do not block Slice 0–1)

1. FriendSvr 是否必须独立进程，还是 Slice 1 先挂在 Login 上、Slice 4 前再拆？默认独立进程，端口 `:8085`。
2. 访客加背包失败时是否在农场主侧做一次尽力补偿调用？默认不做，写入证据限制。
3. 第二章任务文案是否沿用 `master` 的加好友/偷菜？默认沿用开发章节配置，不新开 ConfigSvr。
