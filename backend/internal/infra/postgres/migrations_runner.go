package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// Application startup applies embedded SQL in filename order before serving
// traffic, and existing filename checksums keep released migrations
// identifiable across upgrades. One reserved SQL session holds the migration
// advisory lock and runs both standalone statements and transactions, so a
// one-connection pool can complete migration work without acquiring another
// connection for its transactions.
//
// An ordinary file commits its SQL and checksum record in one transaction. A
// _notx.sql file applies permitted concurrent-index statements separately and
// records its checksum only afterward, so an interrupted file can leave
// earlier statements committed. Those files use repeatable statements, with
// named repairs removing invalid indexes left by interrupted construction.
// Completed previous files remain applied after later failure; cleanup
// attempts session unlock under an independent budget before returning the
// reserved connection.

// Tracking DDL, stable session-lock identity, and paired migration/index names
// govern startup serialization, ledger recording, and targeted retry preparation.
const (
	// schemaMigrationsTableDDL records each applied filename, its trimmed-content SHA256,
	// and its application timestamp.
	schemaMigrationsTableDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	filename   TEXT PRIMARY KEY,
	checksum   TEXT NOT NULL,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`

	// atlasSchemaRevisionsTableDDL creates Atlas-compatible baseline storage before
	// startup aligns an empty revision table to the latest release SQL file.
	atlasSchemaRevisionsTableDDL = `
CREATE TABLE IF NOT EXISTS atlas_schema_revisions (
	version TEXT PRIMARY KEY,
	description TEXT NOT NULL,
	type INTEGER NOT NULL,
	applied INTEGER NOT NULL DEFAULT 0,
	total INTEGER NOT NULL DEFAULT 0,
	executed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	execution_time BIGINT NOT NULL DEFAULT 0,
	error TEXT NULL,
	error_stmt TEXT NULL,
	hash TEXT NOT NULL DEFAULT '',
	partial_hashes TEXT[] NULL,
	operator_version TEXT NULL
);
`

	// migrationsAdvisoryLockID serializes migrations across application instances.
	// Keep this value stable and distinct from other advisory locks in the database.
	migrationsAdvisoryLockID                     int64 = 694208311321144027
	migrationsLockRetryInterval                        = 500 * time.Millisecond
	nonTransactionalMigrationSuffix                    = "_notx.sql"
	paymentOrdersOutTradeNoUniqueMigration             = "120_enforce_payment_orders_out_trade_no_unique_notx.sql"
	paymentOrdersOutTradeNoUniqueIndex                 = "paymentorder_out_trade_no_unique"
	schedulerOutboxPendingDedupKeyMigration            = "165_scheduler_outbox_pending_dedup_key_index_notx.sql"
	schedulerOutboxPendingDedupKeyIndex                = "idx_scheduler_outbox_pending_dedup_key"
	latestAPIKeyIPIndexMigration                       = "201_add_usage_logs_api_key_latest_ip_index_notx.sql"
	latestAPIKeyIPIndex                                = "idx_usage_logs_api_key_latest_ip"
	usageLogsUpstreamModelMismatchIndexMigration       = "195_add_usage_log_upstream_model_mismatch_index_notx.sql"
	usageLogsUpstreamModelMismatchIndex                = "idx_usage_logs_upstream_model_mismatch_created_at"
	usageLogsEffectiveModelIndexesMigration            = "226_add_usage_log_effective_model_indexes_notx.sql"
	usageLogsEffectiveRequestedModelIndex              = "idx_usage_logs_effective_requested_model_created"
	usageLogsEffectiveUpstreamModelIndex               = "idx_usage_logs_effective_upstream_model_created"
	usageLogsUpstreamRequestIDIndexMigration           = "267_add_usage_log_upstream_request_id_index_notx.sql"
	usageLogsUpstreamRequestIDIndex                    = "idx_usage_logs_upstream_request_id"
	auditLogsRequestIDIndexMigration                   = "296_audit_request_id_index_notx.sql"
	auditLogsRequestIDIndex                            = "idx_audit_logs_request_id_created_at"
)

// migrationChecksumCompatibilityRules accepts known historical file versions.
// Both the database and current file checksum must belong to the named migration's
// allowlist, including when a historical file version is restored.
var migrationChecksumCompatibilityRules = map[string]migrationChecksumCompatibilityRule{
	"054_drop_legacy_cache_columns.sql":                       newMigrationChecksumCompatibilityRule("82de761156e03876653e7a6a4eee883cd927847036f779b0b9f34c42a8af7a7d", "182c193f3359946cf094090cd9e57d5c3fd9abaffbc1e8fc378646b8a6fa12b4"),
	"061_add_usage_log_request_type.sql":                      newMigrationChecksumCompatibilityRule("66207e7aa5dd0429c2e2c0fabdaf79783ff157fa0af2e81adff2ee03790ec65c", "08a248652cbab7cfde147fc6ef8cda464f2477674e20b718312faa252e0481c0", "222b4a09c797c22e5922b6b172327c824f5463aaa8760e4f621bc5c22e2be0f3"),
	"109_auth_identity_compat_backfill.sql":                   newMigrationChecksumCompatibilityRule("0580b4602d85435edf9aca1633db580bb3932f26517f75134106f80275ec2ace", "551e498aa5616d2d91096e9d72cf9fb36e418ee22eacc557f8811cadbc9e20ee"),
	"110_pending_auth_and_provider_default_grants.sql":        newMigrationChecksumCompatibilityRule("32cf87ee787b1bb36b5c691367c96eee37518fa3eed6f3322cf68795e3745279", "e3d1f433be2b564cfbdc549adf98fce13c5c7b363ebc20fd05b765d0563b0925"),
	"112_add_payment_order_provider_key_snapshot.sql":         newMigrationChecksumCompatibilityRule("b75f8f56d39455682787696a3d92ad25b055444ca328fb7fca9a460a15d68d99", "ffd3e8a2c9295fa9cbefefd629a78268877e5b51bc970a82d9b3f46ec4ebd15e"),
	"115_auth_identity_legacy_external_backfill.sql":          newMigrationChecksumCompatibilityRule("022aadd97bb53e755f0cf7a3a957e0cb1a1353b0c39ec4de3234acd2871fd04f", "4cf39e508be9fd1a5aa41610cbbebeb80385c9adda45bf78a706de9db4f1385f"),
	"116_auth_identity_legacy_external_safety_reports.sql":    newMigrationChecksumCompatibilityRule("07edb09fa8d04ffb172b0621e3c22f4d1757d20a24ae267b3b36b087ab72d488", "f7757bd929ac67ffb08ce69fa4cf20fad39dbff9d5a5085fb2adabb7607e5877"),
	"118_wechat_dual_mode_and_auth_source_defaults.sql":       newMigrationChecksumCompatibilityRule("b54194d7a3e4fbf710e0a3590d22a2fe7966804c487052a356e0b55f53ef96b0", "e0cdf835d6c688d64100f483d31bc02ac9ebad414bf1837af239a84bf75b8227", "a38243ca0a72c3a01c0a92b7986423054d6133c0399441f853b99802852720fb"),
	"119_enforce_payment_orders_out_trade_no_unique.sql":      newMigrationChecksumCompatibilityRule("0bbe809ae48a9d811dabda1ba1c74955bd71c4a9cc610f9128816818dfa6c11e", "ebd2c67cce0116393fb4f1b5d5116a67c6aceb73820dfb5133d1ff6f36d72d34"),
	"120_enforce_payment_orders_out_trade_no_unique_notx.sql": newMigrationChecksumCompatibilityRule("34aadc0db59a4e390f92a12b73bd74642d9724f33124f73638ae00089ea5e074", "e77921f79d539bc24575cb9c16cbe566d2b23ce816190343d0a7568f6a3fcf61", "707431450603e70a43ce9fbd61e0c12fa67da4875158ccefabacea069587ab22", "04b082b5a239c525154fe9185d324ee2b05ff90da9297e10dba19f9be79aa59a"),
	"123_fix_legacy_auth_source_grant_on_signup_defaults.sql": newMigrationChecksumCompatibilityRule("2ce43c2cd89e9f9e1febd34a407ed9e84d177386c5544b6f02c1f58a21129f57", "6cd33422f215dcd1f486ab6f35c0ea5805d9ca69bb25906d94bc649156657145"),
	"180_batch_image_foundation.sql":                          newMigrationChecksumCompatibilityRule("d902b70982025ec519749faf058aab7631e82c3f48167b9a4ae4db718eb72cce", "82da85b5d98e67a0507647b873a40373e84538e4adafdeed6767c0ac8b6570b2"),
	"183_batch_image_pricing_snapshot.sql":                    newMigrationChecksumCompatibilityRule("4012af3e43636cb6af22e0176d59d1fcc70615c0f310194329461ae462c4fbd6", "96d915c9b7a6941ae99039e0ff3f1a61481eb9bddd933d11c6fadb2274554e87"),
}

// migrationChecksumCompatibilityRule stores designated, historical, and
// effective checksum memberships.
type migrationChecksumCompatibilityRule struct {
	// acceptedChecksums combines fileChecksum and acceptedDBChecksum for both
	// sides of compatibility decisions; the separate fields remain available
	// for fixture inspection of designated and historical inputs.
	fileChecksum       string              // constructor-designated current file checksum
	acceptedDBChecksum map[string]struct{} // constructor-supplied historical checksum set
	acceptedChecksums  map[string]struct{} // combined two-sided compatibility memberships
}

// migrationConnection keeps migration statements and transactions on the selected SQL resource.
type migrationConnection interface {
	// ExecContext executes a statement on the selected migration or lock
	// session. Callers choose transaction mode before invocation and retain the
	// same session across lock ownership and related migration work.
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	// QueryContext returns rows from the selected migration session for recovery
	// checks. Callers close successful rows after iteration and inspect row
	// errors before treating duplicate diagnostics as complete.
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	// QueryRowContext creates a single-row result on the selected session for
	// scanning. The caller retains session identity across advisory acquisition,
	// migration operations, and release; Scan obtains execution errors.
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	// BeginTx opens a transaction on the selected migration session. Callers
	// commit SQL and its tracking record together or roll back that file after
	// failure, then retain the session for later files.
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// advisoryLockConnection supplies lock operations on one database session.
type advisoryLockConnection interface {
	// QueryRowContext creates a single-row result on the selected session for
	// scanning. The caller retains session identity across advisory acquisition,
	// migration operations, and release; Scan obtains execution errors.
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	// ExecContext executes a statement on the selected migration or lock
	// session. Callers choose transaction mode before invocation and retain the
	// same session across lock ownership and related migration work.
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ApplyMigrations applies fsys SQL files under a migration lock on one
// reserved session, rejecting nil db. Nonempty files are trimmed and hashed;
// an already recorded filename is skipped only when its checksum matches or
// both checksums belong to that named compatibility rule.
//
// Ordinary SQL and its record use one transaction, with rollback attempted
// after execution or commit failure. Nontransactional files execute
// statements separately and retain completed earlier statements if later work
// fails; callers keep them repeatable. Earlier successful files and bootstrap
// metadata remain across later failure. Lock cleanup uses a separate
// five-second attempt and suppresses unlock/connection-close errors; a commit
// error is not proof of server rollback. A failed unlock can leave its
// session lock held after the reserved connection returns to the pool.
func ApplyMigrations(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	if db == nil {
		return errors.New("nil sql db")
	}

	// Reserve one session for lock acquisition, statements, and BeginTx. This
	// keeps session-lock ownership aligned and lets a one-connection pool complete transactions.
	lockConn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migrations lock connection: %w", err)
	}
	defer func() { _ = lockConn.Close() }()
	if err := pgAdvisoryLock(ctx, lockConn); err != nil {
		return err
	}
	defer func() {
		// Startup cancellation should still permit a cleanup attempt. Use an independent
		// budget before returning the reserved session, while unlock failures remain suppressed.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = pgAdvisoryUnlock(unlockCtx, lockConn)
	}()

	if _, err := lockConn.ExecContext(ctx, schemaMigrationsTableDDL); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	// Prepare Atlas metadata before ordinary migration transactions. Its latest-FS
	// baseline is compatibility metadata and may remain if later SQL application fails.
	if err := ensureAtlasBaselineAligned(ctx, lockConn, fsys); err != nil {
		return err
	}

	// Zero-padded prefixes such as 001_init.sql determine execution order.
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(files)

	for _, name := range files {
		contentBytes, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		content := strings.TrimSpace(string(contentBytes))
		if content == "" {
			continue
		}

		// Hash trimmed content to detect edits to already-applied migrations.
		sum := sha256.Sum256([]byte(content))
		checksum := hex.EncodeToString(sum[:])

		var existing string
		rowErr := lockConn.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE filename = $1", name).Scan(&existing)
		if rowErr == nil {
			if existing != checksum {
				// Accept a known historical pair only through this filename's rule, so an
				// unrelated edit still blocks startup. Skipping leaves the stored tracking
				// checksum in place rather than rewriting the applied file's history.
				if isMigrationChecksumCompatible(name, existing, checksum) {
					continue
				}
				// Schema changes belong in new migration files.
				return fmt.Errorf(
					"migration %s checksum mismatch (db=%s file=%s)\n"+
						"This means the migration file was modified after being applied to the database.\n"+
						"Solutions:\n"+
						"  1. Revert to original: git log --oneline -- migrations/%s && git checkout <commit> -- migrations/%s\n"+
						"  2. For new changes, create a new migration file instead of modifying existing ones\n"+
						"Note: Modifying applied migrations breaks the immutability principle and can cause inconsistencies across environments",
					name, existing, checksum, name, name,
				)
			}
			continue
		}
		if !errors.Is(rowErr, sql.ErrNoRows) {
			return fmt.Errorf("check migration %s: %w", name, rowErr)
		}

		nonTx, err := validateMigrationExecutionMode(name, content)
		if err != nil {
			return fmt.Errorf("validate migration %s: %w", name, err)
		}

		if nonTx {
			if err := prepareNonTransactionalMigration(ctx, lockConn, name); err != nil {
				return fmt.Errorf("prepare migration %s: %w", name, err)
			}

			// Send concurrent-index statements individually because a combined Exec creates
			// an implicit transaction. Record this file only after all standalone statements succeed.
			statements := splitSQLStatements(content)
			for i, stmt := range statements {
				trimmed := strings.TrimSpace(stmt)
				if trimmed == "" {
					continue
				}
				if stripSQLLineComment(trimmed) == "" {
					continue
				}
				if _, err := lockConn.ExecContext(ctx, trimmed); err != nil {
					return fmt.Errorf("apply migration %s (non-tx statement %d): %w", name, i+1, err)
				}
			}
			if _, err := lockConn.ExecContext(ctx, "INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)", name, checksum); err != nil {
				return fmt.Errorf("record migration %s (non-tx): %w", name, err)
			}
			continue
		}

		// Keep this file's SQL and tracking row in one transaction. Rollback on
		// either execution failure prevents the ledger from declaring an unapplied file complete.
		tx, err := lockConn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}

		if _, err := tx.ExecContext(ctx, content); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}

		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)", name, checksum); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}

		if err := tx.Commit(); err != nil {
			// database/sql can already consider this transaction done after Commit
			// fails. Attempt cleanup while returning the commit error; this
			// rollback call does not establish the server's final transaction outcome.
			_ = tx.Rollback()
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}

	return nil
}

// prepareNonTransactionalMigration applies filename-specific recovery before
// standalone migration statements execute. Known index names remove invalid
// remnants, the payment migration checks duplicates first, and unrelated
// filenames return success without repair.
//
// The effective-model migration handles its two indexes in the listed order.
// A successful earlier removal remains if a later check or drop fails,
// because preparation runs outside a file transaction. Retrying checks each
// index again; an absent or valid index needs no drop before construction.
func prepareNonTransactionalMigration(ctx context.Context, db migrationConnection, name string) error {
	switch name {
	case paymentOrdersOutTradeNoUniqueMigration:
		return preparePaymentOrdersOutTradeNoUniqueMigration(ctx, db)
	case schedulerOutboxPendingDedupKeyMigration:
		return dropInvalidIndexIfPresent(ctx, db, schedulerOutboxPendingDedupKeyIndex)
	case latestAPIKeyIPIndexMigration:
		return dropInvalidIndexIfPresent(ctx, db, latestAPIKeyIPIndex)
	case usageLogsUpstreamModelMismatchIndexMigration:
		return dropInvalidIndexIfPresent(ctx, db, usageLogsUpstreamModelMismatchIndex)
	case usageLogsEffectiveModelIndexesMigration:
		for _, indexName := range []string{usageLogsEffectiveRequestedModelIndex, usageLogsEffectiveUpstreamModelIndex} {
			if err := dropInvalidIndexIfPresent(ctx, db, indexName); err != nil {
				return err
			}
		}
		return nil
	case usageLogsUpstreamRequestIDIndexMigration:
		return dropInvalidIndexIfPresent(ctx, db, usageLogsUpstreamRequestIDIndex)
	case auditLogsRequestIDIndexMigration:
		return dropInvalidIndexIfPresent(ctx, db, auditLogsRequestIDIndex)
	default:
		return nil
	}
}

// preparePaymentOrdersOutTradeNoUniqueMigration queries duplicate nonempty
// trade numbers before changing the payment unique index. Query failures
// return a precheck error, and any duplicate sample stops recovery with
// remediation details.
//
// Only an empty sample reaches the invalid-index check and conditional drop
// on the caller's session. This orders duplicate diagnostics before index
// removal; a valid or absent index needs no drop. Callers resolve duplicate
// data before retrying the later standalone construction statements.
func preparePaymentOrdersOutTradeNoUniqueMigration(ctx context.Context, db migrationConnection) error {
	duplicates, err := findDuplicatePaymentOrderOutTradeNos(ctx, db)
	if err != nil {
		return fmt.Errorf("precheck duplicate out_trade_no: %w", err)
	}
	if len(duplicates) > 0 {
		return fmt.Errorf(
			"duplicate out_trade_no values block %s; remediate duplicates before retrying: %s",
			paymentOrdersOutTradeNoUniqueMigration,
			strings.Join(duplicates, ", "),
		)
	}

	return dropInvalidIndexIfPresent(ctx, db, paymentOrdersOutTradeNoUniqueIndex)
}

// dropInvalidIndexIfPresent removes a named public-schema invalid index with
// standalone DROP INDEX CONCURRENTLY. Absent or valid indexes remain; callers
// supply a trusted index identifier and a connection outside a transaction,
// since the name is interpolated into SQL.
func dropInvalidIndexIfPresent(ctx context.Context, db migrationConnection, indexName string) error {
	invalid, err := indexIsInvalid(ctx, db, indexName)
	if err != nil {
		return fmt.Errorf("check invalid index %s: %w", indexName, err)
	}
	if !invalid {
		return nil
	}

	// A failed concurrent creation can leave an invalid named index that makes
	// IF NOT EXISTS skip repair. Remove that remnant before retrying its creation.
	if _, err := db.ExecContext(ctx, fmt.Sprintf("DROP INDEX CONCURRENTLY IF EXISTS %s", indexName)); err != nil {
		return fmt.Errorf("drop invalid index %s: %w", indexName, err)
	}
	return nil
}

// findDuplicatePaymentOrderOutTradeNos returns at most five nonempty
// duplicated trade numbers with counts, ordered by frequency then value. It
// closes acquired rows and discards Close errors; query, scan, and iteration
// failures return diagnostics before index construction.
func findDuplicatePaymentOrderOutTradeNos(ctx context.Context, db migrationConnection) ([]string, error) {
	// Bound the duplicate sample before formatting the startup failure, so
	// remediation diagnostics remain short even when many trade numbers collide.
	rows, err := db.QueryContext(ctx, `
		SELECT out_trade_no, COUNT(*) AS duplicate_count
		FROM payment_orders
		WHERE out_trade_no <> ''
		GROUP BY out_trade_no
		HAVING COUNT(*) > 1
		ORDER BY duplicate_count DESC, out_trade_no
		LIMIT 5
	`)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	duplicates := make([]string, 0, 5)
	for rows.Next() {
		var outTradeNo string
		var duplicateCount int
		if err := rows.Scan(&outTradeNo, &duplicateCount); err != nil {
			return nil, err
		}
		duplicates = append(duplicates, fmt.Sprintf("%s (count=%d)", outTradeNo, duplicateCount))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return duplicates, nil
}

// indexIsInvalid reports whether a public-schema index with indexName has
// indisvalid false. It returns the query error unchanged; absent and valid
// indexes both produce false for retry-preparation callers.
func indexIsInvalid(ctx context.Context, db migrationConnection, indexName string) (bool, error) {
	var invalid bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_class idx
			JOIN pg_namespace ns ON ns.oid = idx.relnamespace
			JOIN pg_index i ON i.indexrelid = idx.oid
			WHERE ns.nspname = 'public'
			  AND idx.relname = $1
			  AND NOT i.indisvalid
		)
	`, indexName).Scan(&invalid)
	return invalid, err
}

// ensureAtlasBaselineAligned leaves absent legacy migration tracking or a
// nonempty Atlas revision table unchanged. Otherwise it creates missing Atlas
// storage and inserts the latest available SQL filename/hash as one baseline
// row, using the caller's selected session.
//
// The baseline describes the release migration FS and does not prove each SQL
// file has already executed. Table creation and insertion occur as separate
// statements before ordinary file transactions; errors retain earlier
// successful metadata changes, which a later startup checks again.
func ensureAtlasBaselineAligned(ctx context.Context, db migrationConnection, fsys fs.FS) error {
	hasLegacy, err := tableExists(ctx, db, "schema_migrations")
	if err != nil {
		return fmt.Errorf("check schema_migrations: %w", err)
	}
	if !hasLegacy {
		return nil
	}

	hasAtlas, err := tableExists(ctx, db, "atlas_schema_revisions")
	if err != nil {
		return fmt.Errorf("check atlas_schema_revisions: %w", err)
	}
	if !hasAtlas {
		if _, err := db.ExecContext(ctx, atlasSchemaRevisionsTableDDL); err != nil {
			return fmt.Errorf("create atlas_schema_revisions: %w", err)
		}
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM atlas_schema_revisions").Scan(&count); err != nil {
		return fmt.Errorf("count atlas_schema_revisions: %w", err)
	}
	if count > 0 {
		return nil
	}

	version, description, hash, err := latestMigrationBaseline(fsys)
	if err != nil {
		return fmt.Errorf("atlas baseline version: %w", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO atlas_schema_revisions (version, description, type, applied, total, executed_at, execution_time, hash)
		VALUES ($1, $2, $3, 0, 0, NOW(), 0, $4)
	`, version, description, 1, hash); err != nil {
		return fmt.Errorf("insert atlas baseline: %w", err)
	}
	return nil
}

// tableExists reports whether the named table is present in PostgreSQL's
// public schema.
func tableExists(ctx context.Context, db migrationConnection, tableName string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = $1
		)
	`, tableName).Scan(&exists)
	return exists, err
}

// latestMigrationBaseline returns the last sorted SQL filename without its
// suffix as version and description, plus trimmed-content SHA-256. Empty
// filesystems use baseline and empty hash; listing or reading failures return
// errors.
func latestMigrationBaseline(fsys fs.FS) (string, string, string, error) {
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return "", "", "", err
	}
	if len(files) == 0 {
		return "baseline", "baseline", "", nil
	}
	sort.Strings(files)
	name := files[len(files)-1]
	contentBytes, err := fs.ReadFile(fsys, name)
	if err != nil {
		return "", "", "", err
	}
	content := strings.TrimSpace(string(contentBytes))
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])
	version := strings.TrimSuffix(name, ".sql")
	return version, version, hash, nil
}

// checksumSet returns a new membership map containing each supplied checksum
// once.
func checksumSet(values ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

// newMigrationChecksumCompatibilityRule retains the designated file checksum
// and creates historical and combined membership maps. Its combined set
// accepts both designated and additional values for either database or
// current-file comparison, including restored historical files.
func newMigrationChecksumCompatibilityRule(fileChecksum string, acceptedDBChecksums ...string) migrationChecksumCompatibilityRule {
	return migrationChecksumCompatibilityRule{
		fileChecksum:       fileChecksum,
		acceptedDBChecksum: checksumSet(acceptedDBChecksums...),
		acceptedChecksums:  checksumSet(append([]string{fileChecksum}, acceptedDBChecksums...)...),
	}
}

// isMigrationChecksumCompatible reports whether name has a rule accepting
// both supplied checksums. Either unknown value or an unlisted filename
// returns false; accepted historical file/database versions can match in
// either direction.
func isMigrationChecksumCompatible(name, dbChecksum, fileChecksum string) bool {
	rule, ok := migrationChecksumCompatibilityRules[name]
	if !ok {
		return false
	}
	_, dbOK := rule.acceptedChecksums[dbChecksum]
	if !dbOK {
		return false
	}
	_, fileOK := rule.acceptedChecksums[fileChecksum]
	return fileOK
}

// validateMigrationExecutionMode selects _notx.sql by normalized filename and
// checks its restricted concurrent-index format. Ordinary files reject any
// CONCURRENTLY substring; nontransactional files reject transaction-control
// substrings and check each retained fragment for create/drop, index,
// concurrent, and required guard text.
//
// Validation uses uppercased substring checks, semicolon splitting, and
// line-comment stripping rather than SQL parsing. Authors keep input within
// this simple format; keywords or separators inside literals/comments can
// affect classification and token presence alone does not validate SQL
// syntax. Empty or line-comment-only fragments pass when the whole-content
// keyword checks also pass.
func validateMigrationExecutionMode(name, content string) (bool, error) {
	normalizedName := strings.ToLower(strings.TrimSpace(name))
	// Whole-content keyword checks precede per-statement line-comment removal.
	// Authors keep concurrent and transaction-control words out of unrelated
	// comments or literals because those words still affect this lexical validation.
	upperContent := strings.ToUpper(content)
	nonTx := strings.HasSuffix(normalizedName, nonTransactionalMigrationSuffix)

	if !nonTx {
		if strings.Contains(upperContent, "CONCURRENTLY") {
			return false, errors.New("CONCURRENTLY statements must be placed in *_notx.sql migrations")
		}
		return false, nil
	}

	if strings.Contains(upperContent, "BEGIN") || strings.Contains(upperContent, "COMMIT") || strings.Contains(upperContent, "ROLLBACK") {
		return false, errors.New("*_notx.sql must not contain transaction control statements (BEGIN/COMMIT/ROLLBACK)")
	}

	statements := splitSQLStatements(content)
	for _, stmt := range statements {
		normalizedStmt := strings.ToUpper(stripSQLLineComment(strings.TrimSpace(stmt)))
		if normalizedStmt == "" {
			continue
		}

		if strings.Contains(normalizedStmt, "CONCURRENTLY") {
			isCreateIndex := strings.Contains(normalizedStmt, "CREATE") && strings.Contains(normalizedStmt, "INDEX")
			isDropIndex := strings.Contains(normalizedStmt, "DROP") && strings.Contains(normalizedStmt, "INDEX")
			if !isCreateIndex && !isDropIndex {
				return false, errors.New("*_notx.sql currently only supports CREATE/DROP INDEX CONCURRENTLY statements")
			}
			if isCreateIndex && !strings.Contains(normalizedStmt, "IF NOT EXISTS") {
				return false, errors.New("CREATE INDEX CONCURRENTLY in *_notx.sql must include IF NOT EXISTS for idempotency")
			}
			if isDropIndex && !strings.Contains(normalizedStmt, "IF EXISTS") {
				return false, errors.New("DROP INDEX CONCURRENTLY in *_notx.sql must include IF EXISTS for idempotency")
			}
			continue
		}

		return false, errors.New("*_notx.sql must not mix non-CONCURRENTLY SQL statements")
	}

	return true, nil
}

// splitSQLStatements divides content at every semicolon and omits
// whitespace-only fragments. It preserves remaining fragment text, so callers
// use restricted SQL that contains no quoted or procedural semicolons
// requiring parser-aware splitting.
func splitSQLStatements(content string) []string {
	parts := strings.Split(content, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

// stripSQLLineComment removes each line's suffix beginning with its first
// double hyphen and trims the joined result. It is a lexical helper for
// restricted migration SQL, so double hyphens inside quoted text are also
// removed.
func stripSQLLineComment(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if before, _, ok := strings.Cut(line, "--"); ok {
			lines[i] = before
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// pgAdvisoryLock tries the fixed migration lock immediately and polls at the
// configured interval while busy. Query errors or context cancellation stop
// waiting; callers supply the reserved session that will execute migrations
// and later unlock, because session identity owns this lock.
func pgAdvisoryLock(ctx context.Context, db advisoryLockConnection) error {
	ticker := time.NewTicker(migrationsLockRetryInterval)
	defer ticker.Stop()

	for {
		var locked bool
		if err := db.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", migrationsAdvisoryLockID).Scan(&locked); err != nil {
			return fmt.Errorf("acquire migrations lock: %w", err)
		}
		if locked {
			return nil
		}
		// Another startup owns the session lock. Wait between attempts while
		// allowing the caller's startup budget to stop migration admission cleanly.
		select {
		case <-ctx.Done():
			return fmt.Errorf("acquire migrations lock: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// pgAdvisoryUnlock attempts release of the fixed migration lock on the
// caller's selected session. SQL execution errors return a diagnostic, while
// the returned result is not inspected to verify a matching lock was actually
// held.
func pgAdvisoryUnlock(ctx context.Context, db advisoryLockConnection) error {
	_, err := db.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", migrationsAdvisoryLockID)
	if err != nil {
		return fmt.Errorf("release migrations lock: %w", err)
	}
	return nil
}
