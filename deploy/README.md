# Deploy

Local development supports MySQL 8.4 through Docker or a locally installed
server. The root `README.md` contains the complete setup guide.

From the repository root:

```powershell
Copy-Item .env.example .env
.\dev.ps1 -Action migrate
```

For an existing local MySQL instance:

```powershell
.\deploy\migrate.ps1 -Mode Local
```

`-Mode Docker` requires a running Compose MySQL service. The default
`-Mode Auto` uses that service when it is running and otherwise uses the local
`mysql` client.

The migrations create:

- the migration ledger;
- durable accounts and Session generations;
- HTTP Session rows;
- the V3 `player_checkpoints` envelope and deterministic Protobuf blob;
- 4096 local development `shard_fences` rows for exact Owner/epoch checks;
- `shard_migration_progress` rows for open or abandoned Shard handoffs.

Registration commits the account, initial Player checkpoint and first Session
in one MySQL transaction. The local single-database transaction is permitted by
the accepted HTTP contract; it is not the production cross-shard provisioning
design.

Set `MYSQL_DSN` for LoginSvr, ZoneSvr, and FriendSvr to enable the durable path. Zone then
loads Player Actors from checkpoints and asynchronously writes Dirty
`BUY_SEEDS` mutations using checkpoint-revision CAS and the local Fence. When
the DSN is absent, LoginSvr and ZoneSvr use development-only in-memory
adapters; FriendSvr intentionally does not start.

The SQL-backed path has mocked-SQL coverage and owner-run MySQL 8.4.11 evidence
for registration, Actor activation, idempotent `BUY_SEEDS`, Dirty flush and
fresh-process recovery at `player_seq=1`. Stale-owner rejection and production
Fence advancement remain outside that evidence.
