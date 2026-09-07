# class-mid Agent Guide

本分支是单服务器农场课设，与 main 的历史分布式架构独立。

先读 docs/README.md、docs/context/PROJECT.md、docs/context/CURRENT.md；接口以 docs/contracts/json-api.md 为准。

- 用户负责后端，组员后续使用 C++/Qt 客户端。当前 Vue 是可运行演示客户端。
- 只启动 server/cmd/game；HTTP 与 WebSocket 都使用 JSON。
- 不恢复 Login/Gate/Coordinator、Shard、Actor、Protobuf 或旧压测平台。
- 业务规则保持简短、可讲解；重要调整先提出理由，用户理解取舍后才标记 accepted。
- MySQL 使用 class_mid_* 新表，不修改 main 旧表；不得覆盖用户 .env 或打印、提交密码和 token。
- 每个修改先检查、计划、验证；有行为变更运行相关 Go 测试，前端修改运行 build。
- 将真实测试结果写入 docs/evidence/；未验证的功能不能声称通过。
- AI 辅助使用记录如实保留；不伪造独立开发、测量或课程合规结论。
- 只在 class-mid 工作，不自动提交、推送或合并 main。