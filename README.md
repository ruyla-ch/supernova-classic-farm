# Supernova Classic Farm

一个 Vue 3 H5 + Go 后端的经典农场项目。当前可运行版本包含：

- 注册、登录、Session 恢复和退出登录；
- 11 种作物、16 块农田、商店、仓库和章节任务；
- 种植、施肥、成长、成熟、收获、出售和清理；
- 好友码、好友列表、访问农场、偷菜、投虫和捉虫；
- Gate、双 Zone、Coordinator 路由缓存、Player Actor 和 MySQL Dirty 写回；
- 农场主与访客的实时地块变化推送。

项目默认在 Windows PowerShell 下开发和演示。当前架构、已验证能力和限制见
`docs/context/CURRENT.md`。

## 1. 环境要求

必须安装：

- Go 1.26；
- Node.js 20.19+ 或 22.12+；
- npm；
- PowerShell 7 或 Windows PowerShell 5.1；
- MySQL 8.4，或者 Docker Desktop。

仅在修改 Protobuf 时需要安装 Buf CLI。可用以下命令检查：

```powershell
go version
node --version
npm --version
buf --version
docker version
mysql --version
```

## 2. 安装依赖

在仓库根目录执行：

```powershell
.\dev.ps1 -Action install
```

也可以分别安装：

```powershell
cd server
go mod download
cd ..\web
npm install
cd ..
```

## 3. 配置 `.env`

复制示例文件：

```powershell
Copy-Item .env.example .env
```

至少填写以下 MySQL 配置：

```dotenv
MYSQL_HOST=127.0.0.1
MYSQL_PORT=3306
MYSQL_DATABASE=classicfarm
MYSQL_USER=classicfarm
MYSQL_PASSWORD=你的应用数据库密码
```

不要提交 `.env`。启动脚本会用这些字段构造 Go MySQL DSN；密码包含特殊字符时
会自动进行 URL 转义。也可以通过 `.\start-servers.ps1 -MySQLDSN "..."` 显式传入
完整 DSN。

## 4. 准备数据库

### 方式 A：使用 Docker Desktop

在 `.env` 中设置 `MYSQL_PASSWORD`。如需自定义容器 root 密码，再设置
`MYSQL_ROOT_PASSWORD`。

启动 MySQL 并执行全部迁移：

```powershell
.\dev.ps1 -Action migrate
```

停止容器：

```powershell
.\dev.ps1 -Action down
```

Docker 数据保存在 `mysql-data` volume。已有 volume 创建后，再修改 `.env`
不会自动修改数据库账号密码；应在 MySQL 中执行 `ALTER USER`，或者明确确认不需要
旧数据后手动删除 volume。

### 方式 B：使用本机 MySQL

先以管理员账号登录：

```powershell
mysql -u root -p
```

创建数据库和仅限本机使用的应用账号。把示例密码替换成与 `.env` 相同的密码：

```sql
CREATE DATABASE IF NOT EXISTS classicfarm
  CHARACTER SET utf8mb4
  COLLATE utf8mb4_0900_ai_ci;

CREATE USER IF NOT EXISTS 'classicfarm'@'localhost'
  IDENTIFIED BY '替换为应用数据库密码';
CREATE USER IF NOT EXISTS 'classicfarm'@'127.0.0.1'
  IDENTIFIED BY '替换为应用数据库密码';

GRANT ALL PRIVILEGES ON classicfarm.* TO 'classicfarm'@'localhost';
GRANT ALL PRIVILEGES ON classicfarm.* TO 'classicfarm'@'127.0.0.1';
FLUSH PRIVILEGES;
```

验证账号：

```powershell
mysql -h 127.0.0.1 -P 3306 -u classicfarm -p classicfarm -e "SELECT 1"
```

然后使用本机客户端执行迁移：

```powershell
.\deploy\migrate.ps1 -Mode Local
```

迁移脚本按版本顺序执行 `deploy/migrations/*.up.sql`，已记录在
`schema_migrations` 中的版本会跳过。

## 5. 启动项目

### 启动后端

在仓库根目录运行：

```powershell
.\start-servers.ps1
```

默认启动 MySQL 模式和双 Zone：

- LoginSvr：`http://127.0.0.1:8080`
- GateSvr：`ws://127.0.0.1:8081/ws`
- Zone A：`http://127.0.0.1:8082`
- Coordinator：`http://127.0.0.1:8083`
- Zone B：`http://127.0.0.1:8084`
- FriendSvr：`http://127.0.0.1:8085`

首次启动会下载 Go 模块并编译五个服务，可能需要几分钟。看到所有服务的
`[ready]` 后再打开前端。

只启动单 Zone：

```powershell
.\start-servers.ps1 -SingleZone
```

不连接 MySQL的内存模式：

```powershell
.\start-servers.ps1 -InMemory
```

内存模式不启动 FriendSvr，因此好友码、好友关系和好友农场玩法不可用；完整功能演示
应使用 MySQL 模式。

按 `Ctrl+C` 会停止该脚本启动的全部后端进程。

### 启动前端

另开一个 PowerShell：

```powershell
cd web
npm install
npm run dev
```

浏览器访问 `http://localhost:5173`。页面支持直接注册或登录；首次使用请选择一个
未注册账号并设置密码。

## 6. 修改协议

协议源文件位于 `proto/classicfarm/v1/`。修改后在仓库根目录执行：

```powershell
buf lint
buf generate
```

Go 会生成全部服务端协议；Web 只生成浏览器实际使用的 HTTP 和 WebSocket 协议。
不要手工编辑 `server/gen/` 或 `web/src/gen/`。

## 7. 验证

常用回归命令：

```powershell
cd server
go test ./...
go vet ./...

cd ..\web
npm test
npm run typecheck
npm run build
```

MySQL 好友、访问、偷菜、虫害和重启恢复 E2E：

```powershell
cd ..
.\tests\e2e\run-mysql-friend-slice.ps1
```

E2E 会占用 8080–8085。如果开发服务正在运行，可使用偏移端口：

```powershell
.\tests\e2e\run-mysql-friend-slice.ps1 -ServicePortOffset 12000
```

## 8. 常见问题

`Access denied for user 'classicfarm'`：

- 确认 `.env` 密码与 MySQL 中该账号密码一致；
- 确认同时检查了 `classicfarm@localhost` 和
  `classicfarm@127.0.0.1`；
- 执行 `SHOW GRANTS FOR 'classicfarm'@'localhost';` 检查授权；
- Docker 已有 volume 时，修改 `.env` 不会重建账号。

`Unknown database 'classicfarm'`：

- 先创建数据库，再执行迁移；
- 检查 `.env` 的 `MYSQL_DATABASE`。

`mysql client was not found`：

- 将 MySQL `bin` 目录加入 `PATH`；
- 或使用 Docker 模式；
- 本机常见路径为
  `C:\Program Files\MySQL\MySQL Server 8.4\bin\mysql.exe`。

`port 808x is already in use`：

- 停止旧的 `start-servers.ps1`；
- 用 `Get-NetTCPConnection -LocalPort 8080` 查找占用进程；
- E2E 使用 `-ServicePortOffset` 避免与开发服务冲突。

数据库端口检查：

```powershell
Test-NetConnection 127.0.0.1 -Port 3306
```

## 9. 文档

- `docs/README.md`：文档地图和事实来源；
- `docs/context/PROJECT.md`：稳定项目边界；
- `docs/context/CURRENT.md`：当前实现、限制和下一步；
- `docs/architecture/`：架构设计；
- `docs/contracts/`：HTTP、WebSocket、数据和幂等契约；
- `docs/evidence/`：测试和实测证据。
