# class-mid 代码讲解与课程文档设计

状态：负责人已确认，已实施。

## 目标

在不改变当前业务接口和单服务器架构的前提下，提高 Go 后端代码的可讲解性，为 C++/Qt 前端组员提供完整接入依据，并形成代码审查、测试说明和中期答辩材料。

## 范围

- 为 `server/cmd/game` 和 `server/internal/game` 增加简短中文模块注释与关键边界注释。
- 扩充项目架构文档，覆盖组件、数据、登录、游戏、邮箱、并发和持久化链路。
- 新建 C++/Qt 客户端接入文档，完整描述现有 HTTP JSON、WebSocket JSON、字段、响应、错误和恢复策略。
- 审查当前 Go、Vue、脚本和文档中的冗余，记录结论并只删除能够证明不影响行为的明显冗余。
- 汇总现有自动化测试、真实 MySQL 测试和浏览器冒烟测试。
- 新建中期答辩讲解文档，给出讲解顺序、演示步骤和常见问答。

不新增业务功能，不恢复 Login、Gate、Coordinator、Shard、Actor 或 Protobuf，不改变已发布的 JSON 字段和业务数值。

## 代码注释原则

每个 Go 文件顶部说明该模块负责什么、依赖什么以及不负责什么。导出的核心类型和函数说明调用者能观察到的行为。以下边界使用中文注释：注册事务、MySQL 行锁、邮件归属、WebSocket 身份绑定、请求去重、密码存储和 Session 生命周期。

显而易见的赋值、循环和字段不逐行注释。注释解释“为什么”和约束，避免把代码机械翻译为中文。Vue 仅保留实时查询、服务器权威状态替换和未确认请求恢复等非显然逻辑的注释。

## 架构文档

`docs/architecture.md` 作为当前架构真相，包含：

1. Vue/Qt、HTTP、WebSocket、单 Go 进程和 MySQL 的组件图。
2. 各源码文件职责和推荐阅读顺序。
3. 注册、登录、WebSocket AUTH、游戏写操作和邮箱操作的时序图。
4. `class_mid_accounts`、`class_mid_states`、`class_mid_mails` 的职责与关系。
5. `SELECT ... FOR UPDATE`、同步提交、请求去重和单连接约束。
6. 当前明确限制及其适合课设的原因。

## C++/Qt 接入文档

新建 `docs/qt-client-guide.md`，面向没有参与后端开发的前端组员。文档使用 Qt 6，建议模块为 Network 与 WebSockets，示例使用 `QNetworkAccessManager`、`QNetworkRequest`、`QWebSocket`、`QJsonDocument`、`QJsonObject`、`QTimer` 和 `QUuid`。

文档逐项说明全部 HTTP 路径和 WebSocket action，包括请求示例、成功响应、业务错误及 UI 应如何处理。明确 `state_version` 和 `mail_id` 按字符串读取，token 只放内存，客户端不连接 MySQL、不提交玩家 ID、不计算权威价格。给出从登录到 AUTH、快照、操作、心跳、断线恢复的最小可复用代码骨架，以及按阶段替换 Vue 的联调顺序。

## Review 与精简规则

新建 `docs/code-review.md`，按“已处理、建议保留、后续可选”记录发现，并给每项写明证据和影响。

可直接处理的冗余限于：未使用的导入、变量、重复小逻辑、与当前文档矛盾的陈旧描述，以及能够由现有测试覆盖的无行为变化重构。涉及接口、存档格式、业务规则、错误码、脚本使用方式或目录结构的修改只记录建议，不在本轮删除。

## 测试文档

新建 `docs/testing.md`，从源码读取真实测试名称并说明每项验证目标、运行命令、外部依赖和测试边界。内容覆盖：

- 业务规则和请求去重。
- HTTP 注册登录与输入校验。
- WebSocket 认证、游戏操作和邮箱权限。
- 内存存储复制与邮件归属。
- sqlmock 事务提交、回滚和 SQL 权限条件。
- opt-in 真实 MySQL 重连与持久化。
- Vue 未确认请求、邮箱状态、TypeScript 和生产构建。
- 浏览器人工冒烟测试。

不把尚未运行的测试写成已通过，不使用测试数量代替覆盖范围说明。

## 中期答辩文档

新建 `docs/midterm-defense.md`，面向负责人现场讲解，包含：

- 5–10 分钟建议时间分配。
- 从需求简化到最终架构的讲解主线。
- 注册、登录、种植和邮箱演示步骤。
- 推荐现场打开的代码文件和关键函数。
- 数据库、事务、WebSocket、JSON、密码哈希、并发和 Qt 对接常见追问。
- 当前不足与后续计划。

讲稿使用第一人称提示，但不声称没有使用 AI，也不虚构性能、并发量、测试结果或个人贡献。

## 导航与验收

更新 `docs/README.md` 和根 `README.md`，让架构、Qt、Review、测试和答辩文档均可直接找到。

验收条件：Go 文件通过 `gofmt`；`go test ./... -count=1` 与 `go vet ./...` 通过；`npm.cmd test` 与 `npm.cmd run build` 通过；文档不存在 TODO/TBD，占用的接口名、字段和数值与 `docs/contracts/json-api.md` 及源码一致；`git diff --check` 无错误。
