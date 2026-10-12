package migrations

import "embed"

// FS embeds all SQL migration files in this directory.
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
