# class-mid 代码 Review

审查日期：2026-09-07。范围包括当前 Go 服务、Vue 客户端、PowerShell 脚本、测试和文档。本轮保持业务行为与 JSON 接口不变。

## 已处理

- `replaceMailbox` 原来接收从未读取的旧邮件列表。已精简为 `replaceMailbox(returned)`，服务器返回仍是邮箱状态的唯一来源，现有测试覆盖复制行为。
- 为生产 Go 文件补充中文模块职责，并在注册事务、密码哈希、Session、身份绑定、行锁、请求去重和邮件归属处解释原因。
- 新增架构、Qt、测试和答辩文档入口，清除“只能读源码才能理解系统”的文档缺口。

## 建议保留

- `main.go` 的两处 `app.Close()` 分别覆盖异常返回和正常退出时先关闭 WebSocket，避免长连接阻塞 shutdown。
- MemoryStore 修改前复制状态切片，成功后才覆盖原值，可防止失败操作留下部分修改，也避免调用者修改内部数据。
- 邮件与农场状态分表可避免标记已读时重写整个 `state_json`。
- HTTP 负责短时注册登录，WebSocket 负责游戏长连接和心跳，两种传输职责清楚。
- MemoryStore 与 MySQLStore 表面重复，但它支持免数据库演示以及快速、稳定的规则测试。

## 后续可选

- `App.vue` 当前集中展示所有界面。若 Vue 继续扩展，可拆成 LoginPanel、FarmPanel、MailboxPanel；Qt 重构不依赖这项工作。
- 启动脚本可在构建前检查 8080 并显示占用进程 PID，改善端口冲突提示。
- `mails` 使用 `omitempty`，空列表可能省略。若以后允许删空邮箱，可把协议改为始终返回 `mails: []`。
- `READ_MAIL` 目前按 `RowsAffected()==1` 判断成功。部分 MySQL 配置对已经为已读的邮件可能返回 0；若最终版需要自动重试，可将它改成明确幂等并增加存在性判断。
- `errorResponse` 每次构造错误消息 map，规模很小且不影响课设；若错误继续增加，可提升为包级只读表。

## 结论

当前目录没有 Login/Gate/Coordinator/Shard/Actor/Protobuf 的运行依赖。后端的主要复杂度集中在必要的状态规则、事务和网络恢复。除已经处理的 helper 参数外，没有发现适合无风险大批删除的生产代码。
