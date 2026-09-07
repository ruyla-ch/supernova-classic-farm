# class-mid 中期答辩讲解指南

这份文档用于准备讲解，不要求逐字背诵。答辩时先说明需求和取舍，再演示主链路，最后打开关键代码。

## 8 分钟安排

| 时间 | 内容 |
|---|---|
| 0:00–0:50 | 项目目标与中期范围 |
| 0:50–2:00 | 单服务器架构 |
| 2:00–3:00 | 状态模型与三张表 |
| 3:00–4:20 | 注册登录与 WebSocket 身份 |
| 4:20–5:50 | 游戏规则、行锁和请求去重 |
| 5:50–6:40 | 邮箱功能 |
| 6:40–7:30 | 演示与测试 |
| 7:30–8:00 | 当前不足和后续计划 |

## 开场讲稿

> 我的课设是经典农场。中期目标是完成一个稳定、可运行、容易解释的多人农场后端。每个玩家有四块地，目前只种胡萝卜，支持注册登录、购买、种植、施肥、成熟、收获、出售、章节任务和邮箱。
>
> 我把原项目的分布式结构简化成一个 Go 游戏服务器，去掉独立登录服、网关、协调器、分片路由、Actor 和 Protobuf。HTTP 和 WebSocket 由同一进程提供，数据同步写入 MySQL。系统仍然保留身份、并发、事务、状态恢复和前后端协议这些核心问题，同时代码量适合中期讲解。

## 架构怎么讲

打开 [架构图](architecture.md)。

> 注册、登录和注销是短请求，使用 HTTP JSON；进入游戏后需要持续操作和心跳，使用 WebSocket JSON。服务端通过 Store 接口隔离业务和存储，本地可以用 MemoryStore，正式演示用 MySQLStore。未来 Vue 换成 Qt 时，只需复用 JSON 合同。

强调只有 `server/cmd/game` 一个后端入口，没有 Zone、Shard 或服务间 RPC。

## 数据怎么讲

打开 [model.go](../server/internal/game/model.go)，按 `State → Plot → Task → Mail → Command/Response` 讲。

> State 是玩家完整农场存档，包含金币、物品、四块地和任务。每块地保存成熟时间，服务端在读取时推导成熟状态，不需要后台任务每秒更新数据库。
>
> 数据库有三张表：accounts 保存账号和密码哈希；states 每个玩家一行 JSON 存档；mails 每封邮件一行。邮件分表后，标记已读不需要重写农场存档。

可只读展示：

```sql
SHOW TABLES LIKE 'class_mid_%';
SELECT player_id, username FROM class_mid_accounts LIMIT 5;
SELECT player_id, JSON_EXTRACT(state_json, '$.coins') AS coins FROM class_mid_states LIMIT 5;
SELECT mail_id, player_id, title, is_read FROM class_mid_mails ORDER BY mail_id DESC LIMIT 5;
```

不要展示数据库密码和完整 token。

## 注册登录怎么讲

打开 [http.go](../server/internal/game/http.go) 的 `register`、`login`、`identity`，再打开 [password.go](../server/internal/game/password.go)。

> 注册时严格解析 JSON，校验账号密码，用 PBKDF2 和随机盐保存密码哈希。MySQL 在一个事务中创建账号、初始农场和欢迎邮件，任一步失败都回滚。
>
> 登录成功生成随机 token，Session 在内存保存 24 小时。WebSocket 第一条消息必须是 AUTH，此后连接绑定到该玩家。游戏指令不接受客户端提交 player_id，所以修改 JSON 也不能操作别人账号。

服务重启后 Session 消失，需要重新登录；农场和邮件在 MySQL 中不会丢失。

## 游戏操作与并发怎么讲

打开 [rules.go](../server/internal/game/rules.go) 的 `Engine.Execute`、`apply`，再打开 [mysql.go](../server/internal/game/mysql.go) 的 `Update`。

> WebSocket 收到 PLANT 后，从认证连接得到玩家 ID。MySQL 事务用 SELECT FOR UPDATE 锁定该玩家的状态行，在副本上检查种子和地块，成功后写回并提交。
>
> 同一玩家的并发操作排队，不同玩家锁不同的行，可以并行。失败不会覆盖原存档，数据库提交成功后才返回 OK。
>
> 成功写请求保存 request_id 和参数指纹。断线后用同一个编号和参数重试不会重复扣款；同一个编号换参数会返回 REQUEST_ID_CONFLICT。

不要说“锁整个数据库”，锁的是当前玩家在 `class_mid_states` 中的一行。

## 邮箱怎么讲

打开 [websocket.go](../server/internal/game/websocket.go) 中 `GET_MAILBOX`、`READ_MAIL`，以及 `mysql.go` 的邮件方法。

> 新玩家注册时收到欢迎邮件。打开邮箱实时发送 GET_MAILBOX，点击未读邮件发送 READ_MAIL。更新 SQL 同时匹配 mail_id 和认证玩家的 player_id，不能标记别人的邮件。

当前不做附件、删除、分页、群发和推送。

## 现场演示

后端：

```powershell
cd F:\workspace\supernova-classic-farm
powershell -NoProfile -ExecutionPolicy Bypass -File .\start-servers.ps1
```

前端另开窗口：

```powershell
cd F:\workspace\supernova-classic-farm\web
npm.cmd run dev
```

演示顺序：

1. 注册新账号并登录。
2. 指出初始 10 金币、0 种子、1 肥料和 4 块地。
3. 买 3 颗胡萝卜种子，金币变为 4，任务变为 3/3。
4. 在 1 号地种植，说明肥料会缩短 30 秒。
5. 打开邮箱，展示未读欢迎邮件。
6. 点击邮件，关闭并重新打开，展示已读状态已保存。

不要现场等待 100 秒。答辩前可准备一个已成熟账号，或用测试说明收获链路。

## 测试怎么讲

打开 [测试说明](testing.md)。

> 测试分为业务规则、HTTP/WebSocket、内存存储、sqlmock 事务、真实 MySQL 和 Vue 测试。并发测试同时发起 20 次购买，余额不能变负；真实 MySQL 测试换一个连接验证农场、去重记录和邮件可以恢复，然后清理测试数据。

如实说明尚无 Qt 自动化测试和大规模性能测试。

## 常见追问

### 为什么不用 Actor？

当前每次写操作都在 MySQL 事务中用行锁串行化同一玩家。Actor 还要处理生命周期、内存状态和异步落库，对四块地的中期版本复杂度过高。

### 单服务器能连接多个玩家吗？

可以。每个玩家有自己的 WebSocket，Go 并发处理连接，MySQL 不同玩家锁不同状态行。每个账号只保留最新的一条连接。

### 为什么 HTTP 和 WebSocket 都要用？

注册登录低频且一问一答，适合 HTTP；游戏需要长连接和心跳，适合 WebSocket。

### 为什么用 JSON？

Go、Vue、Qt 都能直接处理，抓包和调试容易。代价是体积和类型约束弱于 Protobuf，因此服务端严格校验字段与参数。

### 为什么存档用 JSON？

当前状态小且整体读写，JSON 减少了业务表和映射代码。以后需要复杂查询时可拆出背包或地块表。

### 如何防止重复扣钱？

写请求带 request_id，服务端在玩家事务内检查最近成功请求。同编号同参数不再次执行。

### 两个请求同时买种子会怎样？

第一个事务锁住玩家行，第二个等待；第一个提交后第二个读取最新余额，因此不会基于同一份旧余额扣款。

### 密码如何保存？

使用 PBKDF2-HMAC-SHA256、随机盐、600000 次迭代和常量时间比较。公网部署还需要 TLS 和更完整限流。

### Qt 如何对接？

QNetworkAccessManager 调注册登录，QWebSocket 发送相同 JSON。AUTH 后获取快照，并按 request_id 匹配响应。详见 [Qt 接入指南](qt-client-guide.md)。

### 为什么删除 Gate、LoginSvr 和 Coordinator？

中期只有一个服务实例。拆分会引入内部 RPC、服务发现和部署问题，却不增加当前玩法，所以改成单进程中的清晰模块。

## 当前不足和最终版方向

> 当前 Session 在内存，单进程没有高可用，作物和数值固定，邮箱功能最小，Qt 客户端尚未完成。最终阶段优先完成 Qt 界面和联调，再根据时间增加作物配置或任务，不重新引入当前不需要的分布式结构。

## 必须能独立解释的代码

1. `NewState` 为什么创建四块地。
2. `apply` 如何检查购买、种植、施肥、收获和容量。
3. `Engine.Execute` 如何计算指纹与去重。
4. `MySQLStore.Update` 的事务、行锁、回滚和提交。
5. `register/login/identity` 的密码与 Session 链路。
6. WebSocket 为什么只使用连接绑定的 playerID。
7. 邮件 SQL 为什么同时匹配 mail_id 和 player_id。
8. Qt 为什么把 state_version 和 mail_id 当字符串。
