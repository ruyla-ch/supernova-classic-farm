---
status: completed
date: 2026-09-03
owner: project-owner
branch: feat/mysql-friend-visit-steal
review_required: true
implements:
  - 2026-09-03-mysql-friend-visit-steal-plan.md
  - 2026-09-03-mysql-http-friend-visit-direct-steal-review.md
references:
  - origin/dev-perf-loadtest-20260820@2a45bc5
  - ../contracts/websocket-protocol.md
  - ../contracts/data-model.md
  - ../contracts/idempotency-and-errors.md
---

# MySQL + HTTP/Protobuf 好友访问与偷菜开发任务

## 当前进度

2026-09-03：T01–T18 已实现并通过全量 Go 测试、vet、H5 typecheck/build。
live 双 Zone MySQL 主路径通过好友、访问、heartbeat、直调偷菜、Visitor
inventory、同 ID replay 和 exit；post-steal 全栈重启恢复也通过。浏览器
320px 手工证据仍是后续验证边界，不阻塞本开发任务完成。

## 1. 目标

在 `feat/mysql-friend-visit-steal` 上完成：

```text
好友列表
-> 进入好友农场
-> 维持 90 秒内存访问租约
-> 查看只读公开地块
-> 偷取成熟作物
-> Owner 地块减少可收获数量
-> Visitor 背包增加作物并推进任务
-> 双方 Dirty checkpoint 最终写入 MySQL
```

权威验收环境为本机双 Zone + MySQL。内存模式只作为快速单元测试环境。

## 2. 已确认的技术边界

- H5 与 Gate：WebSocket + Protobuf `WsEnvelope`。
- Gate 与 Zone：HTTP + Protobuf `WsEnvelope`，沿用当前实现。
- Gate 与 FriendSvr：HTTP + Protobuf `WsEnvelope`，沿用当前实现。
- Visitor Zone 与 FriendSvr、Owner Zone：loopback HTTP + 专用 Protobuf
  request/response。
- Protobuf 只定义消息，不生成或启用 gRPC Service。
- 跨 Zone 路由继续通过 Coordinator `FetchRoute`。
- 普通成功写入继续采用 Actor memory -> Dirty -> 异步 MySQL checkpoint。
- 不引入 Tcaplus、gRPC/HMAC、`FriendInteraction` Saga 或对账器。
- 不实现宠物护卫、投虫、捉虫、帮忙清理、Presence 和 FarmView 广播。

参考 dev 分支的 Visitor -> Owner 直调顺序和 Owner 偷取规则，但不复制两个
缺口：

1. dev 当前直调路径没有给 Visitor 背包增加作物；
2. dev 的 mailbox suspend 会允许容量预检后的背包状态发生变化。

本切片补齐 Visitor 背包和任务提交，并在跨 Owner HTTP 调用期间保持 Visitor
Actor 命令串行，内部调用超时最多 2 秒。

## 3. 协议组织

### T01：补齐外部 WebSocket 消息

修改：

- `proto/classicfarm/v1/ws/ws.proto`
- 生成的 `server/gen/classicfarm/v1/ws/ws.pb.go`
- 生成的 `web/src/gen/classicfarm/v1/ws/ws_pb.ts`

任务：

- 在 `WsEnvelope.oneof` 中加入 310、311、312、323 的请求和响应；
- 定义 `EnterFriendFarmRequest/Response`；
- 定义 `FarmHeartbeatRequest/Response`；
- 定义 `ExitFriendFarmRequest/Response`；
- 定义 `StealFriendCropRequest/Response`；
- 定义 `FarmVisitSnapshot` 和 `PublicPlotView`；
- `StealFriendCropRequest` 必须绑定：
  - `owner_player_id`
  - `visit_id`
  - `plot_id`
  - `expected_crop_item_id`
  - `expected_planted_at_ms`
  - `expected_steal_quantity`
- 不改变已存在的 Action 和 ErrorCode 数值。

完成条件：

- Go 和 TypeScript 生成代码更新；
- Protobuf round-trip 测试覆盖四种新请求；
- 旧客户端消息字段编号不变。

### T02：新增 Zone 间 HTTP Protobuf 消息

新建：

- `proto/classicfarm/v1/zone/friend_visit.proto`

包名建议：

```proto
package classicfarm.zone.v1;
```

不要使用路径 `classicfarm/v1/internal`，避免生成的 Go import path 触发
`internal` 目录可见性限制。

定义：

- `CheckMutualFriendRequest/Response`
- `EnterVisitorRequest/Response`
- `HeartbeatVisitorRequest/Response`
- `ExitVisitorRequest/Response`
- `ApplyStealRequest/Response`

约定：

- player ID 使用 Protobuf `uint64`；
- `visit_id` 使用 `bytes`，长度固定 16；
- `request_id` 原样传递 WS request ID；
- 成功 body 使用 `application/x-protobuf`；
- 业务失败由 Protobuf response 中的 `classicfarm.ws.v1.Error` 表达；
- 非 loopback、消息损坏、超限和 ownership 冲突继续使用 HTTP 状态码；
- 不在该 proto 中声明 `service`，不生成 gRPC stub。

完成条件：

- Zone/FriendSvr HTTP client 与 handler 共用同一组生成 DTO；
- 不新增 JSON player ID DTO；
- 不新增 gRPC 依赖或监听端口。

## 4. Player checkpoint 与规则

### T03：补齐冻结偷取字段

修改：

- `proto/classicfarm/v1/data/data_model.proto`
- `server/internal/player/config.go`
- `server/internal/player/state.go`
- `server/internal/player/checkpoint.go`
- `server/internal/player/plant.go`
- `server/internal/player/clean_plot.go`
- 相关测试和生成代码

在 `CropConfig`、内存 `Plot` 和 `PlotStateRecord` 中加入：

- `steal_quantity`
- `max_steal_times`
- `protected_owner_yield`
- `steal_count`
- `stolen_visitor_player_ids`

开发配置：

```text
steal_quantity = 1
max_steal_times = 2
protected_owner_yield = 1
```

规则：

- PLANT 把三个配置值冻结到地块；
- 下一轮 PLANT 前地块必须是 EMPTY，因此从空值开始；
- CLEAN 清空全部偷取字段和访客列表；
- 旧 checkpoint 任一冻结配置为零时，该轮作物不可偷；
- `stolen_visitor_player_ids` 数量不得超过 `max_steal_times`；
- checkpoint 加载拒绝非法计数、产量下溢和重复 visitor ID；
- 不新增 SQL 列或 migration，字段保存在 checkpoint Protobuf blob。

完成条件：

- 新种作物 checkpoint round-trip 保持所有冻结值；
- 旧 checkpoint 可以加载但 `can_steal=false`；
- CLEAN 后所有字段归零；
- 现有 PLANT/HARVEST/CLEAN 测试通过。

### T04：第二章偷菜任务

修改：

- `server/internal/player/config.go`
- `server/internal/player/state.go`
- 任务推进辅助代码和测试
- H5 任务名称映射

任务：

- 第二章加入任务 ID 7“成功偷菜 1 次”；
- 只有第二章 `IN_PROGRESS` 时成功提交 Visitor 背包才推进；
- Owner 已扣减但 Visitor 未提交时不得推进；
- 相同 request ID replay 不重复推进。

完成条件：

- 首次成功从 0 推进到 1；
- replay 仍为 1；
- 第一章和非 `IN_PROGRESS` 状态不推进。

## 5. Slice 2：只读访问

### T05：FriendSvr 互惠关系检查

修改：

- `server/internal/friend/store.go`
- `server/internal/friend/mysql_store.go`
- `server/internal/friend/handler.go`
- `server/cmd/friend/main.go`
- 对应单元测试

任务：

- `Store` 增加 `CheckMutual(ctx, playerA, playerB)`；
- 规范化为 `(player_low_id, player_high_id)`；
- 查询 `friend_relations.status='ACTIVE'`；
- 增加 loopback-only：

```text
POST /internal/v1/friends/check
Content-Type: application/x-protobuf
```

- 拒绝 0、自己检查、损坏消息和非 loopback 请求。

完成条件：

- ACTIVE 返回 `mutual=true`；
- 不存在返回 `mutual=false`，不是服务错误；
- MySQL 错误返回可识别的服务错误；
- 无额外好友表 migration。

### T06：实现双边内存访问登记

新建：

- `server/internal/visit/registry.go`
- `server/internal/visit/registry_test.go`

实现两个索引：

```text
Visitor: visitor_player_id -> owner_player_id + visit_id
Owner:   owner_player_id + visitor_player_id + visit_id -> expires_at
```

规则：

- `visit_id` 使用加密安全随机 16 bytes；
- TTL 90 秒；
- H5 每 30 秒 Heartbeat；
- ENTER 同 request ID replay 返回相同 visit ID；
- 同一访客进入另一农场时替换 Visitor 指针；
- Heartbeat 只延长 Owner 租约；
- 普通快照和 STEAL 不续期；
- 进程重启自然丢失全部租约；
- 过期记录允许惰性删除，不需要持久化清理任务。

完成条件：

- fake clock 覆盖创建、续期、过期、退出、伪造 ID 和 replay；
- 并发访问无 data race；
- registry 不访问 MySQL。

### T07：Owner 公开快照

新建：

- `server/internal/player/public_farm.go`
- `server/internal/player/public_farm_test.go`

任务：

- 在 Owner Actor mailbox 中 materialize 到期成熟状态；
- 按 `plot_id` 稳定输出 `PublicPlotView`；
- 计算当前 `harvestable_quantity`；
- 通过唯一 `CanSteal` 规则计算全局 `can_steal`；
- `can_steal=false` 时对外 `steal_quantity=0`。

公开快照禁止包含：

- 金币；
- 背包；
- 章节和任务；
- RecentResults；
- PendingOutbox；
- per-visitor 偷取名单；
- 内部配置原始值。

完成条件：

- 白名单字段测试通过；
- 四地块顺序稳定；
- 旧 checkpoint 作物不可偷；
- 读取快照不会错误推进任务。

### T08：Owner Zone 访问 HTTP handler

新建：

- `server/cmd/zone/friend_visit_http.go`

修改：

- `server/cmd/zone/main.go`

提供：

```text
POST /internal/v1/friend-visits/enter
POST /internal/v1/friend-visits/heartbeat
POST /internal/v1/friend-visits/exit
```

每个请求必须：

- 仅允许 loopback；
- 限制 body 大小；
- 解码专用 Protobuf DTO；
- 校验 owner player 对应 shard；
- 校验 `X-Shard-ID`、`X-Owner-Zone-ID`、`X-Owner-Epoch`、
  `X-Route-Version`；
- 进入 shard read gate；
- 通过本 Zone ownership Lease；
- 再操作 Owner registry 或 Actor。

完成条件：

- wrong Owner 返回 `409 NOT_OWNER` 和新路由提示；
- 无效/过期 visit 映射到 705/706；
- ENTER 在公开快照失败时不留下有效租约。

### T09：Visitor 访问服务和 HTTP client

新建：

- `server/internal/visit/service.go`
- `server/internal/visit/http_clients.go`
- 对应测试

任务：

- 通过 FriendSvr HTTP 检查互为好友；
- 通过 Coordinator `FetchRoute(owner shard)` 找 Owner Zone；
- 携带 committed ownership headers 调 Owner；
- NOT_OWNER 后重新 FetchRoute 并仅重试一次；
- ENTER 成功后写 Visitor registry；
- 切换农场时尽力退出旧 Owner，失败不阻塞新 ENTER；
- Heartbeat/Exit 同时校验 Visitor 和 Owner 两侧记录。

完成条件：

- 非好友和访问自己在 Owner 调用前被拒绝；
- Zone A Visitor 可访问 Zone B Owner；
- Visitor 或 Owner Zone 重启后旧 visit 不可使用。

### T10：Zone 与 Gate 接通 310–312

修改：

- `server/cmd/zone/handler.go`
- `server/internal/gateway/gateway.go`
- 相关 adapter 和测试

实施顺序：

1. Zone 先明确分派 310–312；
2. 增加 Zone handler 测试，证明不会回落为普通快照；
3. Gate `validateRequestTuple` 再放行 310–312；
4. Gate 按 `target_player_id=visitor` 路由到 Visitor Zone；
5. 300–302 保持走 FriendSvr。

完成条件：

- Gate 不直接路由到 Owner；
- caller 必须等于 `target_player_id`；
- 响应 action/request ID 相关性检查保持生效；
- 旧普通命令路由测试不回归。

## 6. Slice 3：直调偷菜

### T11：Owner Actor 应用偷取

新建：

- `server/internal/player/friend_steal.go`
- `server/internal/player/friend_steal_test.go`

Owner 权威条件：

```text
plot exists
AND state == MATURE
AND crop_item_id == expected_crop_item_id
AND planted_at_ms == expected_planted_at_ms
AND steal_quantity == expected_steal_quantity
AND steal_quantity > 0
AND steal_count < max_steal_times
AND visitor not in stolen_visitor_player_ids
AND base_yield - stolen_quantity - steal_quantity >= protected_owner_yield
```

成功 mutation：

```text
steal_count++
stolen_quantity += steal_quantity
append visitor_player_id
player_seq++
checkpoint_revision++
append Owner RecentResults
mark Dirty
```

幂等：

- key 为 `(visitor_player_id, request_id)`；
- fingerprint 包含 owner、plot、crop item、planted time、quantity；
- 相同 key/相同 fingerprint replay 原结果；
- 相同 key/不同 fingerprint 返回 `REQUEST_ID_CONFLICT`；
- Owner response 保存 item、quantity 和变化后的公开地块。

完成条件：

- 覆盖未成熟、旧页面、同访客重复、次数上限和 Owner 保底；
- 两名不同访客可在配置上限内分别偷一次；
- Owner replay 不重复扣减；
- HARVEST 使用 `base_yield - stolen_quantity`。

### T12：Owner apply-steal HTTP handler

扩展：

- `server/cmd/zone/friend_visit_http.go`

增加：

```text
POST /internal/v1/friend-visits/apply-steal
Content-Type: application/x-protobuf
```

调用顺序：

```text
HTTP 基础校验
-> ownership + shard gate
-> Owner registry 校验 visit lease
-> Owner Actor ApplySteal
-> Protobuf response
```

完成条件：

- 无有效租约时绝不激活或修改 Owner Actor；
- Owner 已应用但 HTTP 响应丢失时，相同 request ID 可 replay；
- HTTP transport error 与业务拒绝可以区分。

### T13：Visitor Actor 完成背包和任务

新建或扩展：

- `server/internal/player/friend_steal.go`
- Visitor 编排测试

完整顺序：

```text
Visitor Actor mailbox
-> 查 Visitor RecentResults
-> 校验 Visitor registry
-> 再检查互为好友
-> 预检 inventory type/stack capacity
-> 最多等待 2 秒 Owner apply-steal HTTP
-> Owner 成功后增加 inventory
-> 推进任务 7
-> player_seq/checkpoint_revision++
-> 保存 Visitor 最终 response
-> mark Dirty
```

限制：

- 本片不 suspend mailbox；
- Owner 已成功、Visitor 提交前崩溃时依赖客户端使用同一 request ID 重试；
- 客户端永不重试时允许 Owner 已扣而 Visitor 未得；
- Dirty 未刷盘异常退出时双方可能只恢复一侧；
- 不做补偿和后台 reconcile。

完成条件：

- Visitor patch 包含 `inventory_upserts` 和 task patch；
- 同 request ID replay 不重复增加背包或任务；
- 满仓在 Owner 调用前失败；
- Owner timeout 返回 `REQUEST_OUTCOME_UNKNOWN` 且标记 retryable。

### T14：Gate 放行 323

修改：

- `server/internal/gateway/gateway.go`
- 相关测试

实施顺序与 T10 相同：Zone 先支持 323，Gate 后放行。

完成条件：

- steal 仍路由到 Visitor Zone；
- Gate 不调用 Owner Zone；
- 相同 WS request ID 原样传递；
- 未认证、冒用 target、payload 不匹配均拒绝。

## 7. H5

### T15：WebSocket client 与访问状态

修改：

- `web/src/lib/ws.ts`
- `web/src/App.vue`

任务：

- 增加 Enter、Heartbeat、Exit、Steal API；
- 保存 owner ID、visit ID、过期时间和公开快照；
- 每 30 秒发送 Heartbeat；
- `VISIT_NOT_FOUND`/`VISIT_EXPIRED` 时清理本地访问状态；
- 切换好友、断开连接和退出农场时停止定时器；
- `REQUEST_OUTCOME_UNKNOWN` 使用同一 request ID 由用户重试。

完成条件：

- 不产生重复 Heartbeat timer；
- 断线后不继续发送旧 visit；
- 自己农场状态和好友农场状态不混用。

### T16：好友农场页面

修改：

- `web/src/components/FriendsPanel.vue`

新建：

- `web/src/components/FriendFarmDashboard.vue`

任务：

- 好友列表增加“进入农场”；
- 展示公开地块、成熟状态和预计成熟时间；
- 只在 `can_steal=true` 时启用偷菜；
- 成功后使用 response 的 `owner_plot` 更新单个地块；
- Visitor patch 合并进自己的背包和任务；
- 不实现实时 visitor fan-out。

完成条件：

- 320 CSS px 无横向溢出；
- 键盘和触摸都能触发；
- 连续点击期间按钮禁用；
- 错误提示区分 visit 失效、不可偷、满仓和未知结果。

## 8. 自动化验证

### T17：单元与协议测试

每个任务就近增加测试，至少覆盖：

- Protobuf round-trip；
- checkpoint round-trip 和非法值拒绝；
- registry fake-clock；
- FriendSvr mutual MySQL mock；
- Owner public projection 白名单；
- Owner CanSteal 和幂等 replay；
- Visitor 满仓预检、背包提交和任务推进；
- Gate/Zone action tuple 与路由；
- HTTP NOT_OWNER 单次刷新重试。

每片执行：

```text
buf lint
buf generate
go test ./...
go vet ./...
cd web
npm run typecheck
```

触及页面时增加：

```text
npm run build
```

### T18：双 Zone + MySQL E2E

扩展：

- `server/test/e2e/mysql_friend_slice_test.go`
- `tests/e2e/run-mysql-friend-slice.ps1`

场景：

1. 注册直到两个账号分属不同 Zone；
2. 生成并兑换好友码；
3. Owner 买种、种植并等待成熟；
4. Visitor ENTER 并看到可偷地块；
5. Heartbeat 后租约继续有效；
6. Visitor STEAL；
7. 断言 Owner `stolen_quantity/steal_count`；
8. 断言 Visitor 背包和任务 7；
9. 同 request ID replay，双方不重复变化；
10. EXIT 后偷菜被拒绝；
11. 停止并重启服务；
12. 重新登录后验证双方 checkpoint 和好友关系；
13. 旧 visit ID 必须失效。

证据写入：

- `docs/evidence/2026-09-03-mysql-friend-visit-steal.md`

## 9. 建议提交顺序

每项通过局部测试后再提交：

1. `proto: add HTTP protobuf friend visit contracts`（T01–T02）
2. `player: freeze steal configuration in checkpoints`（T03–T04）
3. `friend: add mutual relation check`（T05）
4. `visit: add read-only cross-zone farm visits`（T06–T10）
5. `player: add idempotent owner crop steal`（T11–T12）
6. `visit: credit visitor inventory after direct steal`（T13–T14）
7. `web: add friend farm visit and steal UI`（T15–T16）
8. `test: prove dual-zone mysql friend steal recovery`（T17–T18）

禁止在同一提交中混入 generated code 以外的无关格式化或历史文件清理。

## 10. 停止条件

出现以下任一情况立即停止扩展并先评审：

- 需要引入 gRPC Service、Tcaplus SDK 或 `FriendInteraction` 表；
- 需要保证 Owner/Visitor 强事务一致才能继续演示；
- 单玩家 owner loop、Fence、Dirty 或双 Zone 路由回归；
- Gate 已放行新 Action，但 Zone 尚未明确处理；
- checkpoint 兼容需要重写既有玩家数据；
- 为解决未知结果开始实现后台补偿或对账器。

## 11. 审核确认

开始编码前，请 owner 确认：

- [x] 接受双边内存 visit registry；
- [x] 接受 Visitor mailbox 最多等待 2 秒 Owner HTTP；
- [x] 接受 `expected_planted_at_ms` 绑定种植轮次；
- [x] 接受无 Saga 的 Dirty 崩溃不一致窗口；
- [x] 接受旧 checkpoint 当前作物不可偷；
- [x] 接受 Slice 3 只返回变化后的单地块，不做实时广播；
- [x] 接受内部 HTTP body 使用 Protobuf，而不是 JSON；
- [x] 接受暂不采用 dev 的 mailbox suspend。

Owner 于 2026-09-03 明确要求开始开发，以上边界据此批准实施。
