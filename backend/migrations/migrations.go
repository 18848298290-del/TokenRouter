package migrations

import "embed"

// migrations.go embeds SQL migration scripts for database initialization and
// upgrades.
// Deployment carries migration SQL inside the application binary. Bootstrap and
// maintenance pass this embedded filesystem to the PostgreSQL runner, which
// orders migration filenames and checks stored checksums before applying the
// scripts required by the running version.

// FS embeds the migration scripts used by database initialization and upgrade
// runners, making the SQL available independently of the deployment's working directory.
//
// Migration names:
//   - Use zero-padded numeric prefixes to determine execution order.
//   - Use NNN_description.sql, such as 001_init.sql or 002_add_users.sql.
//   - Write descriptions in lowercase words separated by underscores.
//
// Migration requirements:
//   - Make statements safe to execute repeatedly.
//   - Use IF NOT EXISTS or IF EXISTS where applicable.
//   - Keep applied files unchanged; the runner validates their checksums.
//
// Example migration:
//
//	-- 001_init.sql
//	CREATE TABLE IF NOT EXISTS users (
//	    id BIGSERIAL PRIMARY KEY,
//	    email VARCHAR(255) NOT NULL UNIQUE,
//	    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
//	);
//
//go:embed *.sql
var FS embed.FS
