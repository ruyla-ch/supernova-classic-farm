---
status: approved_for_implementation
date: 2026-09-03
owner: project-owner
branch: feat/mysql-friend-visit-steal
review_required: true
supersedes: none
related:
  - 2026-09-03-mysql-friend-visit-steal-plan.md
  - 2026-09-03-mysql-http-protobuf-friend-visit-steal-tasks.md
  - ../contracts/websocket-protocol.md
  - ../contracts/data-model.md
  - ../contracts/idempotency-and-errors.md
---

# MySQL + HTTP 好友访问与直调偷菜审核稿

## 1. 审核目标

本文以 `origin/dev-perf-loadtest-20260820@2a45bc5` 的好友农场与直调偷菜
链路为主要参考，并用 `master@fc4e4f6` 对照其演进，再改写成适合本分支的
**MySQL + loopback HTTP + V3 Dirty checkpoint** 方案。本文是实施前审核稿，
未获 owner 确认前不写 Slice 2/3 业务代码，也不修改 accepted ADR。

期望演示路径：

```text
好友列表
-> 进入好友农场
-> 获得只读公开地块快照和短期 visit_id
-> 对成熟且可偷的地块发起偷菜
-> 农场主 Actor 扣减可偷量
-> 访客 Actor 增加作物
-> 双方 MySQL checkpoint 最终可恢复
```

## 2. 对 dev 偷菜链路的结论

可以参考业务逻辑，但不应直接复制完整代码。

该 dev 分支当前实际接线的成功路径是：

```text
H5 STEAL_FRIEND_CROP
-> Gate 按访客 player_id 路由到 Visitor Zone
-> Visitor Zone 通过 Coordinator 定位 Owner Zone
-> Owner Zone 校验 visit lease
-> Owner Actor ApplyStealOnOwner
-> Visitor Actor ApplyVisitorFriendSideEffect
-> Gate 返回双方 patch
```

它仍是**直接 Visitor Zone -> Owner Zone**，没有把旧 Tcaplus
`FriendInteraction` Saga 接入运行链路。相对 `master`，dev 的主要变化是：

- Owner 和 Visitor 修改改为 `markDirty` 后异步刷 checkpoint，更接近本分支
  已接受的 V3 Dirty 语义；
- 增加 `AwaitFriendOwnerCall`，在跨 Zone RPC 时 suspend Visitor Actor
  mailbox，避免一个慢 Owner 调用持续占用 Actor 执行器；
- ENTER/HEARTBEAT 补充 Gate endpoint，并记录离线访问等后续能力；
- Coordinator 路由、Owner visit lease 和 Owner 偷取业务规则基本不变。

dev 值得沿用的部分：

- Action 310/311/312/323 及请求含义；
- 访问前检查互为好友；
- 访问会话使用 16-byte 随机 `visit_id` 和短 TTL；
- Owner Zone 保存访客登记，重启后访问失效；
- 公开快照只投影地块等公开信息；
- 种植时冻结 `steal_quantity`、`max_steal_times` 和
  `protected_owner_yield`；
- 农场主 Actor 串行判断成熟、保底、次数和同一访客是否已偷；
- 直调顺序是 Visitor Zone 调 Owner Zone，Owner 成功后再调用 Visitor
  side-effect。

必须特别说明：dev 和 `master` 当前接线的直调 STEAL
**都没有把作物加入访客背包**；
对应端到端测试也明确断言该路径“不含 visitor inventory credit”。它的
`CommitSteal` 背包入库只存在于未接线的旧 Saga 路径。本分支的产品目标要求
访客真正获得作物，因此只能参考 Owner 规则和调用顺序，Visitor 背包提交必须
在本分支重新实现，不能把 dev 当前行为视为完整答案。

不能复制的部分：

- Tcaplus 好友与互动 Store；
- Gate/Zone/FriendSvr 的 gRPC 与 HMAC wiring；
- `FriendInteraction` Saga、reconciler 和 interaction 表；
- `ReserveSteal`、跨步骤 reservation/receipt 状态机；
- 为互动步骤单独执行的同步 `SaveCAS`；
- dev 的 `AwaitFriendOwnerCall` mailbox suspend：当前方案依赖 Visitor
  mailbox 在容量预检到背包提交之间不被其他命令抢占；若照搬 suspend，
  就必须重新引入 reservation 或恢复后再次校验容量；
- 宠物护卫、罚金币、投虫、捉虫、帮忙清理；
- `FarmViewPatch` 广播、Presence Push 和跨 Gate visitor fan-out。

另外，现有代码与文档有三处必须在 Slice 2A 先修正的漂移：

- Action 311 的代码名是 `FARM_HEARTBEAT`，旧计划部分文字写成
  `HEARTBEAT_FRIEND_FARM`；本稿以现有 Protobuf 名 `FARM_HEARTBEAT` 为准；
- 原 Slice 0 声称偷菜冻结字段已写入 checkpoint 草案，但当前实际只有
  `stolen_quantity`，其余字段必须补齐；
- 旧 `module-design-and-flows.md` 仍描述 Saga/Outbox 偷菜流，不是本分支
  实施依据，后续应标注为历史方案而不是照着接线。

原因：本分支已接受普通 V3 命令先改 Actor 内存、标 Dirty、异步刷 MySQL，
并明确接受跨 Actor 崩溃窗口。把上述机制搬过来会重新引入另一套持久化语义，
使当前演示分支变成 Saga/Tcaplus/gRPC 的混合实现。

## 3. 对现有总计划的一个修正

原计划同时写了“`visit_id` 活在访客 Zone”以及“农场主 Zone 登记访客”。
只保存一边都不够：

- 只保存在 Visitor Zone：Owner 无法在偷菜时独立验证访客；
- 只保存在 Owner Zone：Visitor Zone 重启后仍可拿旧 `visit_id` 调 Owner，
  不符合“Zone 重启后旧访问失效”。

建议两边各保留一个轻量内存记录：

- Visitor Zone：`visitor_player_id -> owner_player_id + visit_id`，保证一个访客
  同时只处于一个好友农场，并使 Visitor Zone 重启后旧命令失败；
- Owner Zone：`owner_player_id + visitor_player_id + visit_id -> expires_at`，
  作为 HEARTBEAT、EXIT 和 STEAL 的权威访问租约。

两边都不落 MySQL。Owner 记录自然过期即可，不做可靠双边清理。

## 4. 外部 WebSocket 契约草案

所有 310–323 请求的 `target_player_id` 仍是**访客自己的 player ID**。
这是必要条件：Gate 必须先把命令路由到访客自己的 Owner Zone。被访问的玩家
放在 payload 的 `owner_player_id` 中。

### 4.1 ENTER_FRIEND_FARM（310）

请求：

```proto
message EnterFriendFarmRequest {
  uint64 owner_player_id = 1;
}
```

响应：

```proto
message EnterFriendFarmResponse {
  bytes visit_id = 1;             // exactly 16 bytes
  int64 expires_at_ms = 2;
  FarmVisitSnapshot snapshot = 3;
}
```

规则：

- 拒绝 owner=0 和访问自己；
- FriendSvr 必须确认当前存在一条 `ACTIVE` 互惠关系；
- 同一 `request_id` 的 ENTER 重试返回相同 `visit_id` 并延长 TTL；
- 进入另一人的农场时，Visitor Zone 尽力 EXIT 旧访问，但旧 Owner 不可用不能
  阻塞新 ENTER；
- 建议 TTL 90 秒，H5 每 30 秒发一次 HEARTBEAT。

### 4.2 FARM_HEARTBEAT（311）

请求包含 `owner_player_id` 和 16-byte `visit_id`；响应只返回新的
`expires_at_ms`。只有 HEARTBEAT 延长 TTL，普通读和偷菜不续期。

### 4.3 EXIT_FRIEND_FARM（312）

请求包含 `owner_player_id` 和 `visit_id`，成功响应为空。Owner 删除租约，
Visitor 删除当前访问指针。

未知、伪造和已过期 ID 分别返回 `VISIT_NOT_FOUND` 或 `VISIT_EXPIRED`。

### 4.4 公开快照

```proto
message FarmVisitSnapshot {
  uint64 owner_player_id = 1;
  repeated PublicPlotView plots = 2;
}

message PublicPlotView {
  uint32 plot_id = 1;
  PlotState plot_state = 2;
  uint32 crop_id = 3;
  uint32 crop_item_id = 4;
  int64 planted_at_ms = 5;
  int64 estimated_mature_at_ms = 6;
  uint32 harvestable_quantity = 7;
  uint32 steal_count = 8;
  bool can_steal = 9;
  uint32 steal_quantity = 10;     // 仅 can_steal=true 时大于 0
}
```

明确禁止输出：

- 金币；
- 背包；
- 章节与任务；
- 最近请求结果；
- 可偷配置原始值；
- 其他内部 checkpoint 字段。

Slice 2/3 不实现增量 `FarmViewPatch`。偷菜成功响应携带变化后的单个
`PublicPlotView`，其他情况由 H5 重新 ENTER 获取完整快照。

### 4.5 STEAL_FRIEND_CROP（323）

请求：

```proto
message StealFriendCropRequest {
  uint64 owner_player_id = 1;
  bytes visit_id = 2;
  uint32 plot_id = 3;
  uint32 expected_crop_item_id = 4;
  int64 expected_planted_at_ms = 5;
  uint32 expected_steal_quantity = 6;
}
```

`expected_planted_at_ms` 是本分支对 `master` 的小幅收紧：它把请求绑定到
具体种植轮次，避免同一地块收获后又种了同种作物时，旧页面误偷新一轮作物。
公开的 `steal_quantity` 让 Visitor Actor 能在 Owner 调用前精确预检背包；
Owner 仍必须验证 expected 值与冻结值一致。

响应：

```proto
message StealFriendCropResponse {
  uint32 crop_item_id = 1;
  uint32 stolen_quantity = 2;
  PlayerStatePatch visitor_patch = 3;
  PublicPlotView owner_plot = 4;
}
```

成功响应的 `state_version` 属于访客 Actor，不混入农场主的 `player_seq`。

## 5. 内部 HTTP 链路

本机内部接口继续使用 loopback-only 和现有 ownership headers，不引入 gRPC。
HTTP body 使用专用 Protobuf DTO 和 `application/x-protobuf`；player ID 使用
Protobuf `uint64`，不再定义 JSON player ID DTO。内部消息放在
`proto/classicfarm/v1/zone/friend_visit.proto`，只定义 message，不声明
Protobuf `service`，因此不会生成或启用 gRPC。

### 5.1 FriendSvr

新增：

```text
POST /internal/v1/friends/check
```

输入两个 player ID，输出 `mutual: true|false`。查询
`friend_relations` 的有序 pair 和 `status='ACTIVE'`。接口只允许 loopback。

### 5.2 Visitor Zone 到 Owner Zone

新增四个内部接口：

```text
POST /internal/v1/friend-visits/enter
POST /internal/v1/friend-visits/heartbeat
POST /internal/v1/friend-visits/exit
POST /internal/v1/friend-visits/apply-steal
```

调用前通过 Coordinator `FetchRoute(owner shard)` 获取 Owner endpoint。请求
携带现有 `X-Shard-ID`、`X-Owner-Zone-ID`、`X-Owner-Epoch` 和
`X-Route-Version`。Owner Zone 必须重新校验：

- 请求来自 loopback；
- 路由身份与 owner player 的 shard 一致；
- 本 Zone 当前持有匹配 owner/epoch 的有效 Lease；
- visitor/owner/visit 三元组匹配；
- payload 格式和大小合法。

出现 `NOT_OWNER` 时，Visitor Zone重新 FetchRoute 并仅重试一次。连接超时或
响应丢失返回 `REQUEST_OUTCOME_UNKNOWN`，客户端必须使用同一 `request_id`
重试。

本阶段的 loopback 信任只适合本地演示，不宣称生产安全。共享 secret/HMAC
属于后续生产化，不在本稿实现。

## 6. Player checkpoint 改造

### 6.1 种植时冻结

`CropConfig` 新增：

- `StealQuantity = 1`
- `MaxStealTimes = 2`
- `ProtectedOwnerYield = 1`

每次 PLANT 将其复制到 `Plot` 和 `PlotStateRecord`：

- `steal_quantity`
- `max_steal_times`
- `protected_owner_yield`
- `steal_count`
- `stolen_visitor_player_ids`

`stolen_visitor_player_ids` 最大受 `max_steal_times` 限制，本地配置最多两个，
不是无界列表。CLEAN/下一次 PLANT 会自然清空上一轮记录。

第二章增加任务 ID 7“成功偷菜 1 次”，与 `master` 的开发任务编号保持一致；
只有第二章处于 `IN_PROGRESS` 时成功偷菜才推进，不追溯第一章期间的行为。

已有 checkpoint 中正在生长的旧作物没有冻结值。兼容规则是：三个冻结值任一
为 0 时该轮 `can_steal=false`；不根据新配置回填旧作物，避免伪造“种植时已
冻结”的历史。重新种植后即可偷。

这些字段位于 Protobuf checkpoint blob，因此不新增 SQL 表和
`000007` migration；需要更新 Protobuf、校验、Record/Load 和生成代码。

### 6.2 CanSteal

农场主 Actor 内的唯一权威判断：

```text
plot exists
AND state == MATURE
AND crop identity == expected item + expected planted_at
AND steal_quantity > 0
AND steal_count < max_steal_times
AND visitor not in stolen_visitor_player_ids
AND base_yield - stolen_quantity - steal_quantity >= protected_owner_yield
```

成功后在同一个 Owner Actor mailbox 中：

```text
steal_count += 1
stolen_quantity += steal_quantity
append visitor_player_id
player_seq += 1
checkpoint_revision += 1
append owner idempotency result
mark Dirty
```

Owner 后续 HARVEST 仍得到 `base_yield - stolen_quantity`，不得低于保底值。

## 7. 直调偷菜顺序

建议沿用 dev 的“Visitor 先调 Owner、Owner 成功后再提交 Visitor”顺序，但
不接入 dev/`master` 中未接线 Saga 的 reservation，也暂不照搬 dev 的 mailbox
suspend；本阶段在访客 Actor mailbox 内做一次完整串行命令，允许 mailbox 在
Owner HTTP 调用期间最多阻塞一个短超时：

```text
Gate
-> Visitor Zone（按 target_player_id 路由）
-> 校验 Visitor 当前 visit 指针
-> FriendSvr 再确认互为好友
-> 进入 Visitor Actor mailbox
   -> 查 visitor RecentResults；命中则 replay
   -> 预检背包种类/堆叠容量
   -> FetchRoute(owner) 并 HTTP ApplyStealOnOwner
      -> Owner 校验 Route + visit
      -> 进入 Owner Actor mailbox
      -> 查 owner RecentResults；命中则 replay
      -> materialize maturity + CanSteal
      -> 更新地块 + owner receipt + Dirty
      -> 返回 item/quantity/owner_plot
   -> Visitor 增加背包、推进偷菜任务
   -> visitor player_seq/revision++，保存原响应，Dirty
-> Gate 返回
```

为什么先做访客容量预检：原总计划把“满仓”放在 Owner 成功之后，会制造一个
本可避免的确定性丢失。把整个流程放在访客 mailbox 内，其他访客命令不能在
预检后抢占背包容量，因此满仓会在调用 Owner 前失败。这样无需 reservation，
又保留同玩家串行语义。

代价：Owner HTTP 最多会占用访客 mailbox 一个内部超时时间。建议内部调用
超时 2 秒，整个 Gate 命令超时保持现有上限。

## 8. 幂等与失败窗口

不建 `FriendInteraction` 表。复用每个 Actor checkpoint 中已有的
`RecentResults`，保留规则仍是最多 100 条且 24 小时。

Owner checkpoint 的幂等键：

```text
(caller_player_id=visitor, request_id)
```

Visitor checkpoint 使用同一个 `request_id` 保存最终客户端响应。相同 ID 但
owner/plot/crop-cycle 指纹不同返回 `REQUEST_ID_CONFLICT`。

关键失败情形：

1. Owner 尚未应用就超时：重试同一 request ID。
2. Owner 已应用但响应丢失：Owner receipt 重放相同 item/quantity，Visitor
   再提交一次。
3. Owner 已应用、Visitor 在更新背包前崩溃：若客户端重试，Owner replay 后
   Visitor 可补上；若永不重试，作物可能扣了但访客未得到。这是已接受窗口。
4. Visitor 已返回但 Dirty 尚未刷盘就异常退出：访客物品可能回滚。
5. Owner 已返回但 Dirty 尚未刷盘就异常退出：Owner 偷取记录可能回滚，而
   Visitor 可能已获得物品。
6. request result 超过 24 小时或被 100 条上限淘汰：同一访客同一作物轮次仍
   被 `stolen_visitor_player_ids` 阻止重复扣取，但不保证重放原成功响应。

第 3–5 项是 V3 Dirty + 无 Saga 的真实代价，必须写入最终 evidence，不能把
它描述成强一致跨玩家事务。

## 9. 代码改造边界

协议与生成代码：

- `proto/classicfarm/v1/ws/ws.proto`
- `proto/classicfarm/v1/data/data_model.proto`
- `server/gen/...`、`web/src/gen/...`

FriendSvr：

- `server/internal/friend/store.go`：增加 `CheckMutual`
- `server/internal/friend/mysql_store.go`：有序 pair 查询
- `server/internal/friend/handler.go`：loopback relation-check

访问编排：

- 新建 `server/internal/visit/registry.go`
- 新建 `server/internal/visit/service.go`
- 新建 `server/internal/visit/http_clients.go`
- `server/cmd/zone/main.go`：构造访问服务
- `server/cmd/zone/handler.go`：分派 310/311/312/323
- 新建 `server/cmd/zone/friend_visit_http.go`：Owner 内部接口

Player Actor：

- `server/internal/player/config.go`
- `server/internal/player/state.go`
- `server/internal/player/checkpoint.go`
- `server/internal/player/plant.go`
- `server/internal/player/harvest.go`
- 新建 `server/internal/player/public_farm.go`
- 新建 `server/internal/player/friend_steal.go`

Gate：

- `server/internal/gateway/gateway.go`：300–302 继续走 FriendSvr；
  310–312/323 改为按访客自己路由到 Zone
- 不增加新的 Gate 到 Owner 直连
- 必须先让 Zone 明确处理 310–312/323，再放开 Gate tuple 校验；当前
  `Runtime.Handle` 对未识别 Action 的尾部分支存在回落风险，不能出现 Gate
  已放行而 Zone 尚未完成分派的中间提交

H5：

- `web/src/lib/ws.ts`
- `web/src/App.vue`
- `web/src/components/FriendsPanel.vue`：启用“进入农场”
- 新建 `web/src/components/FriendFarmDashboard.vue`
- HEARTBEAT 定时器和退出清理

## 10. 分片实施与验收

### Slice 2A：契约和公开投影

- 补齐 310–312 Protobuf payload；
- 实现 visitor-safe `FarmVisitSnapshot`；
- 单测证明快照不含金币、背包和任务；
- 旧 checkpoint 作物默认为不可偷。

### Slice 2B：访问租约和跨 Zone HTTP

- FriendSvr mutual check；
- 双边内存 visit registry；
- ENTER/HEARTBEAT/EXIT；
- 非好友、访问自己、伪造 ID、过期 ID 测试；
- Zone A 的访客实测进入 Zone B 的好友农场；
- 任一相关 Zone 重启后旧访问不可继续使用。

### Slice 3A：Owner 偷取规则

- 冻结配置和 checkpoint 字段；
- CanSteal、同访客一次、最大次数、Owner 保底；
- HARVEST 使用扣偷后的数量；
- Owner request receipt 和相同请求 replay。

### Slice 3B：Visitor 提交和 H5

- 访客 mailbox 容量预检；
- Owner 直调后增加背包并推进任务；
- H5 成熟地块“偷菜”按钮、成功后的单地块刷新；
- 同一 request ID 不重复扣 Owner、不重复加 Visitor；
- 两账号分属不同 Zone 的 MySQL E2E；
- 停止并重启应用后检查双方 checkpoint；
- 记录上述无 Saga 崩溃窗口。

每个 slice 必须通过：

```text
buf lint
go test ./...
go vet ./...
npm run typecheck
```

涉及 live 路径时另跑双 Zone + MySQL 专项 E2E。单玩家
buy/plant/fertilize/harvest/sell/claim/clean 回归必须保持通过。

## 11. 审核时需要 owner 明确确认

1. 是否接受“双边内存 visit registry”，而不是只存 Visitor Zone？
2. 是否接受在访客 mailbox 内等待最多 2 秒 Owner HTTP，以避免满仓竞态？
3. 是否接受 `expected_planted_at_ms`，用它防止旧页面误偷同种新作物？
4. 是否接受不做 Saga，并把 Owner/Visitor Dirty 崩溃不一致写入证据？
5. 是否接受已有旧作物不可偷，必须重新种植才能获得冻结偷取配置？
6. 是否接受 Slice 3 只返回变化后的单个公开地块，不做访客广播和
   `FarmViewPatch`？

Owner 于 2026-09-03 要求按配套开发任务文档开始实施，以上六点据此确认。
本文仍不是生产架构 ADR。
