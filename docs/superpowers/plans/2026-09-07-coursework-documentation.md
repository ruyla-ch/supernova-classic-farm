# class-mid Code Explanation and Coursework Documentation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the simplified farm backend easy to explain, provide a complete Qt 6 integration guide, record a grounded code review and test inventory, and prepare a truthful midterm defense guide.

**Architecture:** Preserve the current single Go process, JSON HTTP/WebSocket protocol and three-table MySQL design. Add explanation around existing boundaries, derive all documents from current source and tests, and limit code cleanup to behavior-neutral changes covered by verification.

**Tech Stack:** Go 1.26, MySQL 8, JSON, WebSocket, Vue 3, TypeScript, C++17, Qt 6 Network and WebSockets.

**Spec:** `docs/superpowers/specs/2026-09-07-coursework-documentation-design.md`

## Global Constraints

- Work only on `class-mid`; do not commit, push or merge.
- Do not change existing JSON fields, actions, error codes, gameplay numbers or database format.
- Do not restore Login/Gate/Coordinator, Shard, Actor, Protobuf or old load testing code.
- Comments explain responsibilities and constraints in concise Chinese; do not translate every statement.
- Every factual claim about code, API or tests must be checked against the current repository.

---

### Task 1: Source review and module comments

**Files:**
- Modify: `server/cmd/game/main.go`
- Modify: `server/internal/game/model.go`
- Modify: `server/internal/game/rules.go`
- Modify: `server/internal/game/store.go`
- Modify: `server/internal/game/mysql.go`
- Modify: `server/internal/game/password.go`
- Modify: `server/internal/game/http.go`
- Modify: `server/internal/game/websocket.go`
- Create: `docs/code-review.md`

**Interfaces:**
- Consumes: existing Go behavior and JSON contract.
- Produces: commented modules and a review whose findings are traceable to files.

- [ ] Read every production Go and Vue source file and locate unused declarations with `go vet`, TypeScript build and `rg` references.
- [ ] Record each finding under 已处理、建议保留 or 后续可选, including its behavior impact.
- [ ] Add a package/file responsibility comment and Chinese comments at password, session, identity, transaction, row-lock, ownership and idempotency boundaries.
- [ ] Remove only imports, variables or duplicate statements proven unused; do not alter public JSON or state behavior.
- [ ] Run `gofmt` on all Go files, then `go test ./... -count=1`, `go vet ./...`, `npm.cmd test` and `npm.cmd run build`.

### Task 2: Complete architecture document

**Files:**
- Modify: `docs/architecture.md`

**Interfaces:**
- Consumes: `server/cmd/game`, `server/internal/game`, `web/src/game`, and `docs/contracts/json-api.md`.
- Produces: the architecture source of truth linked from other coursework docs.

- [ ] Describe each runtime component and source file responsibility.
- [ ] Add a Mermaid component diagram for Vue/Qt → HTTP/WS → Server → Store → MySQL.
- [ ] Add Mermaid sequence diagrams for registration/login/AUTH, game mutation and mailbox read.
- [ ] Explain the account, state and mail tables without inventing columns or indexes.
- [ ] Explain row locking, synchronous commits, receipts, session memory and one-WebSocket-per-player behavior.
- [ ] List current limits and why the selected design is appropriate for the midterm scope.

### Task 3: Qt 6 client integration guide

**Files:**
- Create: `docs/qt-client-guide.md`

**Interfaces:**
- Consumes: exact HTTP paths, WebSocket actions, request/response fields and errors from `docs/contracts/json-api.md` and Go models.
- Produces: a Qt 6 implementation guide using `QNetworkAccessManager` and `QWebSocket`.

- [ ] Document Qt 6 CMake modules, suggested client class boundaries and local URLs.
- [ ] Document all five HTTP routes with request headers, bodies, status codes and response fields.
- [ ] Document AUTH, PING, all farm actions, GET_MAILBOX and READ_MAIL in a complete action table.
- [ ] Document `Snapshot`, `Plot`, `Task`, `Config`, `Mail`, `state_version`, `mail_id`, error codes and server time handling.
- [ ] Provide compilable-style C++ snippets for HTTP POST, WebSocket AUTH/commands, reply dispatch, UUID request IDs and heartbeat.
- [ ] Describe reconnection, token expiry, uncertain write reuse, UI update rules and a staged Vue-to-Qt migration checklist.

### Task 4: Test inventory and execution guide

**Files:**
- Create: `docs/testing.md`
- Modify: `docs/evidence/2026-09-07-class-mid.md`
- Modify: `docs/evidence/2026-09-07-class-mid-mailbox.md`

**Interfaces:**
- Consumes: every test function in `server/internal/game/*_test.go` and every `web/tests/*.test.ts` test.
- Produces: exact commands, coverage descriptions, dependencies and known gaps.

- [ ] Enumerate real test names using `rg '^func Test|^test\\('` and map each to the behavior it proves.
- [ ] Separate unit, protocol/integration, sqlmock, opt-in MySQL, frontend and manual browser checks.
- [ ] State the required `CLASS_MID_TEST_MYSQL_DSN` setup without exposing credentials.
- [ ] Record gaps such as no automated Qt tests and no large-scale performance test.
- [ ] Re-run commands before updating evidence; preserve actual tool versions and outcomes only.

### Task 5: Midterm defense guide

**Files:**
- Create: `docs/midterm-defense.md`

**Interfaces:**
- Consumes: architecture, Qt guide, code review and test inventory.
- Produces: a 5–10 minute presentation path with demo and likely questions.

- [ ] Write a minute-by-minute presentation outline and a natural first-person speaking script.
- [ ] Give a deterministic demo sequence: start services, register, login, buy carrot seeds, plant and open/read mail.
- [ ] Link the exact source files/functions to open when explaining data, rules, transaction, authentication and WebSocket dispatch.
- [ ] Add likely teacher questions with concise truthful answers about simplification, database locks, JSON, security, concurrency, Actor removal and Qt migration.
- [ ] Add current limitations and final-version directions without making performance or course-compliance claims.

### Task 6: Navigation and consistency verification

**Files:**
- Modify: `README.md`
- Modify: `docs/README.md`
- Modify: `docs/context/CURRENT.md`

**Interfaces:**
- Consumes: all documents produced above.
- Produces: discoverable links and a verified final workspace.

- [ ] Add direct links for architecture, JSON contract, Qt guide, code review, testing and midterm defense.
- [ ] Search all current docs for stale claims about two tables, generic radish wording, removed services or missing mailbox support.
- [ ] Search new docs for TODO/TBD and confirm every action/error/field matches source.
- [ ] Run fresh final verification: `gofmt -d`, `go test ./... -count=1`, `go vet ./...`, `npm.cmd test`, `npm.cmd run build`, and `git diff --check`.
- [ ] Summarize changed files, review findings, test evidence and remaining limitations without committing or pushing.
