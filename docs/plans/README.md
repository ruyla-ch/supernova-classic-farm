# Plans

Store bounded execution plans here. A plan should state:

- Goal.
- Scope and non-goals.
- Relevant context and decisions.
- Steps.
- Verification.
- Stop conditions and risks.

Plans describe intended work. `docs/context/CURRENT.md` describes the actual current state.

## Current state

`2026-09-03-mysql-friend-visit-steal-plan.md` 是 **已完成** 的计划：在
`feat/mysql-friend-visit-steal` 上用 MySQL + 双 Zone 做加好友、进入农场和
直调偷菜。`main` 仍是中期基线，不执行该计划。 Slice 0 已完成；Slice 1
的 cross-Zone MySQL 双账号主路径和 MySQL 服务重启恢复已通过。Slice 2–3
的 HTTP/Protobuf 访问、直调偷菜、Visitor 背包提交和 H5 页面已实现；live
跨 Zone 主路径和 post-steal 完整服务重启恢复均已通过。

`2026-09-03-mysql-http-friend-visit-direct-steal-review.md` 是 Slice 2/3 的
实施前审核稿：以 `origin/dev-perf-loadtest-20260820` 为主要参考，只保留
访问租约、公开投影、冻结可偷参数与 Visitor→Owner 直调业务规则，明确改写为
当前分支的 MySQL + loopback HTTP + Dirty checkpoint，不带入 Tcaplus、gRPC
或 Saga。

`2026-09-03-mysql-http-protobuf-friend-visit-steal-tasks.md` 是上述审核稿的
可执行开发任务清单。它把工作拆成 T01–T18，明确 WebSocket 和内部 HTTP 都
使用 Protobuf message、但不声明 gRPC Service；文末八项边界已确认，T01–T18
均已完成。浏览器 320px 手工证据仍是后续验证边界。

`2026-08-03-remaining-roadmap-and-iterations.md` 是面向 2026-08-21 答辩的
中文后续路线图与迭代表。用它排期；每一轮大改前再单独写有边界的执行计划。

`2026-08-03-ws-ticket-restart-boundary-plan.md` 是已完成的 R1：冻结短命
WS Ticket/CSRF 重启丢失边界（ADR-0010）。

`2026-08-03-r3-single-instance-performance-baseline-plan.md` 是进行中的 R3：
建立双 Zone + MySQL 单机协议端到端基线；第一阶段实现快照读路径压测工具，
后续补 Actor、Push 和 Dirty 指标。

`2026-08-03-static-dual-zone-routing-plan.md` is the completed bounded plan for
versioned Rendezvous bootstrap placement, two local Zone processes, Gate
RouteCache and one same-request `NOT_OWNER` recovery. Its inactive-Shard
migration successor is listed below; active migration and multi-Zone MySQL
fencing remain follow-up work.

`2026-08-03-manual-inactive-shard-migration-plan.md` is the completed successor
that proves epoch-incrementing handoff and stale Gate-cache recovery only for a
Shard with no active Actor. Active-state movement remains blocked until MySQL
Dirty/Fence transfer exists.

`2026-08-03-static-dual-zone-mysql-fence-plan.md` is the completed static
MySQL successor. It transactionally converts only the original epoch-one local
Fences to the static Zone A/B assignment and deliberately disables
MySQL-backed moves until Fence CAS and final Dirty drain exist.

`2026-08-03-active-shard-mysql-migration-plan.md` is the completed active
handoff successor: final Actor flush, PREPARING Fence CAS, target checkpoint
preparation and a post-migration write are verified.

`2026-08-03-coordinator-preparing-recovery-plan.md` is the completed successor
that persists migration progress, hydrates routes from fences after
Coordinator restart, and exposes fail-closed inspect/continue/abandon
controls.

`2026-07-31-v3-first-stage-implementation-plan.md` is the completed bounded plan for the 2026-08-02 authenticated H5-to-Player-Actor command milestone. Its scope stopped at one correlated `GET_PLAYER_SNAPSHOT` path plus the minimum failure evidence; the delivered implementation subsequently expanded through the complete single-player loop without changing that plan's historical acceptance boundary.

`2026-07-27-30m-dau-architecture-strategy-and-open-questions-plan.md` is a superseded V1 plan and must not be executed as the current architecture.

Open-question plans track unresolved work. When an item is resolved, link its architecture, module, contract, ADR, or evidence output instead of copying the full conclusion into the plan.
