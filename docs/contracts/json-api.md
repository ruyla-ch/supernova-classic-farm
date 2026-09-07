# class-mid JSON 协议 v1

状态：当前实现契约。Go 与 Vue 共用；未来 Qt 按同一契约接入。原 Protobuf 不再使用。
默认服务地址 `http://127.0.0.1:8080`，连接地址 `ws://127.0.0.1:8080/ws`。
Vue 开发代理使用 5173 上的同源 `/api` 和 `/ws`。Qt 可直连 8080。

## HTTP

请求/响应均为 UTF-8 JSON；POST 注册登录需 `Content-Type: application/json`。只接受一个 JSON 对象，不允许未定义字段。认证请求最多 4096 字节。

| 方法与路径 | 输入 | 成功响应额外字段 |
|---|---|---|
| GET /healthz | 无 | 无 |
| GET /api/config | 无 | config |
| POST /api/register | username, password | player_id；HTTP 201 |
| POST /api/login | username, password | player_id, token；HTTP 200 |
| POST /api/logout | Authorization: Bearer TOKEN | 无 |

账号：3–32 个 ASCII 小写字母/数字/下划线，首字符为字母。密码：合法 UTF-8，8–128 **字节**，不裁剪空格。成功注册后客户端另调用 login。
密码不通过 URL 传递。Session 24 小时到期；token 不放 URL、不打印，不存客户端持久登录文件。

```json
{"username":"student_a","password":"example-password"}
```

登录响应示意（示例 token 为占位文本）：

```json
{"type":"response","code":"OK","server_time_ms":1800000000000,"player_id":"随机32位十六进制ID","token":"随机64位十六进制凭证"}
```

HTTP 错误：400 INVALID_ARGUMENT、401 INVALID_CREDENTIALS/UNAUTHENTICATED、409 ACCOUNT_EXISTS、429 SERVER_BUSY、503 SERVICE_UNAVAILABLE。
同样含 type、code、message、server_time_ms。客户端按 code 判断，不解析中文 message。

## WebSocket

每一条 WebSocket **文本消息**是一份完整 JSON；不使用二进制帧，不额外加长度头。
连接后 5 秒内必须 AUTH，之后 60 秒无消息断开；客户端每 20 秒发 PING。
认证后服务器绑定玩家 ID，所有业务操作都是自己的农场，不接受 target_player_id。

```json
{"request_id":"auth-0001","action":"AUTH","data":{"token":"登录返回的token"}}
```

AUTH 成功返回 code=OK 和 player_id；随后请求 GET_PLAYER_SNAPSHOT。
request_id 为 8–64 个 `[a-zA-Z0-9:_-]` 字符，建议客户端使用 UUID。

| action | data | 成功结果 |
|---|---|---|
| AUTH | token | player_id |
| PING | {} | snapshot, config |
| GET_PLAYER_SNAPSHOT | {} | snapshot, config |
| GET_SHOP | {} | snapshot, config |
| BUY_SEEDS | quantity：1–100整数 | snapshot, config |
| BUY_FERTILIZER | quantity：1–100整数 | 同上 |
| PLANT | plot_id：1–4 | 同上 |
| APPLY_FERTILIZER | plot_id：1–4 | 同上 |
| HARVEST | plot_id：1–4 | 同上 |
| CLEAN_PLOT | plot_id：1–4 | 同上 |
| SELL_CROP | quantity：1–200整数 | 同上 |
| CLAIM_CHAPTER_REWARD | {} | 同上 |
| GET_MAILBOX | {} | mails；按 mail_id 倒序，含完整正文 |
| READ_MAIL | mail_id：正十进制字符串 | mails；目标邮件已标为已读 |

```json
{"request_id":"plant-0001","action":"PLANT","data":{"plot_id":1}}
```

所有合法游戏响应包含 type=response、原 request_id、原 action、code、server_time_ms。
成功业务响应含完整 snapshot 与 config；失败含 message，不含 snapshot。非法 JSON/二进制消息或非法 request_id 返回 INVALID_ARGUMENT 后断开（此时不保证关联编号）。

```json
{"type":"response","request_id":"plant-0001","action":"PLANT","code":"INSUFFICIENT_ITEMS","message":"物品不足","server_time_ms":1800000000000}
```

## 完整状态

| 字段 | 类型/规则 |
|---|---|
| player_id | 字符串 |
| state_version | 十进制字符串；成功写操作递增，Qt 可转无符号64位，Vue 比较 BigInt |
| coins, seeds, fertilizer, crops | 非负整数 |
| plots | 地块数组 |
| chapter | 当前章节编号 |
| tasks | 当前章节任务数组 |

地块：`plot_id` 整数，`status` 为 EMPTY/GROWING/MATURE/NEED_CLEANUP，`planted_at_ms`、`mature_at_ms` 为 Unix 毫秒整数，`fertilized` 布尔。空地两个时间为 0。
任务：`action`、`label` 字符串，`current`、`target` 整数。
config：seed_price=2、fertilizer_price=2、crop_price=5、growth_seconds=100、fertilizer_seconds=30、yield=3、capacity=200。
价格、数量与时间由后端权威计算；前端不能提交修改后的余额。

成熟状态从时间推导，读取不增加 state_version；因此同版本快照也可刷新地块成熟显示。拒绝比当前版本更小的快照。

## 失败与重试

业务错误：INVALID_ARGUMENT、INSUFFICIENT_COINS、INSUFFICIENT_ITEMS、PLOT_STATE_CONFLICT、CROP_NOT_MATURE、WAREHOUSE_FULL、CHAPTER_NOT_CLAIMABLE、REQUEST_ID_CONFLICT。
连接会话失效：UNAUTHENTICATED，重新登录。
存储暂时不可用/提交结果不明：SERVICE_UNAVAILABLE，保留原 request_id 和参数，恢复后重试。

只有成功写操作保留去重记录，最近 100 条随存档持久化。相同编号相同参数返回当前状态，不重复扣款；不同参数报 REQUEST_ID_CONFLICT。
明确业务失败未执行，可修正条件后发新编号。超时/断线不可当成业务失败，不自动产生新的写请求编号。
Vue 按玩家在 sessionStorage 保存未确认指令；退出/重新登录后从该账号恢复，同一页暂时阻止其他写操作。
服务端重启丢失 token，但 MySQL 状态与去重记录保留；内存模式重启全部丢失。
第一版不提供主动事件推送，以操作响应、心跳和重新拉快照刷新。

## 邮箱

`mail_id` 使用十进制字符串，避免 JavaScript 读取 MySQL `BIGINT UNSIGNED` 时丢失精度。客户端不能提交 `player_id`；服务端始终使用当前认证连接绑定的玩家。非法编号返回 `INVALID_ARGUMENT`，邮件不存在或不属于当前玩家统一返回 `MAIL_NOT_FOUND`。

```json
{"type":"response","request_id":"mailbox-0001","action":"GET_MAILBOX","code":"OK","server_time_ms":1800000000000,"mails":[{"mail_id":"1","title":"欢迎来到经典农场","content":"欢迎来到经典农场！快去种下你的第一颗胡萝卜吧。","is_read":false,"created_at_ms":1800000000000}]}
```

新注册玩家会在注册事务中收到一封欢迎邮件，已有账号不补发。当前邮箱没有附件、删除、过期、分页和主动推送。

## Qt 组员接入步骤

1. QNetworkAccessManager POST JSON 注册和登录，QJsonDocument 解析 token/player_id。
2. QWebSocket.open 连接 /ws；sendTextMessage 发 AUTH，收到 binaryMessageReceived 不是本协议，正常读取 textMessageReceived。
3. AUTH 成功请求快照；按钮调用对应 action，使用 QUuid 产生 request_id。
4. 根据 request_id 匹配响应，按 snapshot 刷新界面；QTimer 发心跳，显示服务端时间修正后的倒计时。
5. 断线重新连接和认证，token 失效则重新登录。未确认写请求保存原编号，勿重复提交新编号。

多人合作先联调“登录→AUTH→快照”，再接购买和种植。接口变更先同步文档和样例，再分别改 Go/Qt；客户端不直接连接 MySQL。
局域网联调时 GAME_ADDR 设为指定局域网地址或 0.0.0.0:8080，Qt 填服务器电脑 IP；只开放实际需要的端口。当前默认 loopback，未部署公网 TLS。
