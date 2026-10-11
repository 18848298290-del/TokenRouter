# Database migrations

The SQL files in this directory are embedded by [`migrations.go`](migrations.go) and executed by [`infra/postgres.ApplyMigrations`](../internal/infra/postgres/migrations_runner.go) before the application starts serving requests.

Each migration is identified by its filename and a SHA-256 checksum. Keep applied files unchanged; schema or data corrections require a new migration. Migrations run forward, so rolling back an application binary leaves the database schema in place.

Name new files `<next_number>_<snake_case_description>.sql`. Use the `_notx.sql` suffix for concurrent index migrations.

The engineering documentation defines the current workflow:

- [Execution, checksums and transaction modes](../../docs/operations/deployment_and_migrations.md#migration_execution).
- [Adding migrations and importing upstream changes](../../docs/operations/deployment_and_migrations.md#新增与同步迁移).
- [Upgrade and recovery](../../docs/operations/deployment_and_migrations.md#升级与恢复).
- [Test layers and local verification](../../docs/operations/development_workflow.md#验证策略).
- [Version-specific upgrade requirements](../../docs/operations/upgrade_notes.md).
