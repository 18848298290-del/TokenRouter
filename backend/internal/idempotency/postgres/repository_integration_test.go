//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// TestIdempotencyRepo_CreateProcessing_CompeteSameKey inserts a claim and then
// retries its scope/key pair with a different fingerprint. PostgreSQL must assign
// the first record an ID and decline ownership for the second insert.
func TestIdempotencyRepo_CreateProcessing_CompeteSameKey(t *testing.T) {
	tx := idempotencyTestTx(t)
	repo := NewIdempotencyRepository(tx)
	ctx := context.Background()

	now := time.Now().UTC()
	record := &idempotency.IdempotencyRecord{
		Scope:              uniqueIdempotencyValue(t, "idem-scope-create"),
		IdempotencyKeyHash: hashedTestValue(t, "idem-hash"),
		RequestFingerprint: hashedTestValue(t, "idem-fp"),
		Status:             idempotency.IdempotencyStatusProcessing,
		LockedUntil:        ptrTime(now.Add(30 * time.Second)),
		ExpiresAt:          now.Add(24 * time.Hour),
	}
	owner, err := repo.CreateProcessing(ctx, record)
	require.NoError(t, err)
	require.True(t, owner)
	require.NotZero(t, record.ID)

	duplicate := &idempotency.IdempotencyRecord{
		Scope:              record.Scope,
		IdempotencyKeyHash: record.IdempotencyKeyHash,
		RequestFingerprint: hashedTestValue(t, "idem-fp-other"),
		Status:             idempotency.IdempotencyStatusProcessing,
		LockedUntil:        ptrTime(now.Add(30 * time.Second)),
		ExpiresAt:          now.Add(24 * time.Hour),
	}
	owner, err = repo.CreateProcessing(ctx, duplicate)
	require.NoError(t, err)
	require.False(t, owner, "same scope+key hash should be de-duplicated")
}

// TestIdempotencyRepo_TryReclaim_StatusAndLockWindow retries failed records before
// and after their backoff deadline. An expired lock must return the record to
// processing with a future lock, while an active backoff must reject reclaim.
func TestIdempotencyRepo_TryReclaim_StatusAndLockWindow(t *testing.T) {
	tx := idempotencyTestTx(t)
	repo := NewIdempotencyRepository(tx)
	ctx := context.Background()

	now := time.Now().UTC()
	record := &idempotency.IdempotencyRecord{
		Scope:              uniqueIdempotencyValue(t, "idem-scope-reclaim"),
		IdempotencyKeyHash: hashedTestValue(t, "idem-hash-reclaim"),
		RequestFingerprint: hashedTestValue(t, "idem-fp-reclaim"),
		Status:             idempotency.IdempotencyStatusProcessing,
		LockedUntil:        ptrTime(now.Add(10 * time.Second)),
		ExpiresAt:          now.Add(24 * time.Hour),
	}
	owner, err := repo.CreateProcessing(ctx, record)
	require.NoError(t, err)
	require.True(t, owner)

	require.NoError(t, repo.MarkFailedRetryable(
		ctx,
		record.ID,
		"RETRYABLE_FAILURE",
		now.Add(-2*time.Second),
		now.Add(24*time.Hour),
	))

	newLockedUntil := now.Add(20 * time.Second)
	reclaimed, err := repo.TryReclaim(
		ctx,
		record.ID,
		idempotency.IdempotencyStatusFailedRetryable,
		now,
		newLockedUntil,
		now.Add(24*time.Hour),
	)
	require.NoError(t, err)
	require.True(t, reclaimed, "failed_retryable + expired lock should allow reclaim")

	got, err := repo.GetByScopeAndKeyHash(ctx, record.Scope, record.IdempotencyKeyHash)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, idempotency.IdempotencyStatusProcessing, got.Status)
	require.NotNil(t, got.LockedUntil)
	require.True(t, got.LockedUntil.After(now))

	require.NoError(t, repo.MarkFailedRetryable(
		ctx,
		record.ID,
		"RETRYABLE_FAILURE",
		now.Add(20*time.Second),
		now.Add(24*time.Hour),
	))

	reclaimed, err = repo.TryReclaim(
		ctx,
		record.ID,
		idempotency.IdempotencyStatusFailedRetryable,
		now,
		now.Add(40*time.Second),
		now.Add(24*time.Hour),
	)
	require.NoError(t, err)
	require.False(t, reclaimed, "within lock window should not reclaim")
}

// TestIdempotencyRepo_StatusTransition_ToSucceeded completes an inserted
// processing record with a stored response. Its reloaded state must be succeeded,
// retain the exact status and body, and release the processing lock.
func TestIdempotencyRepo_StatusTransition_ToSucceeded(t *testing.T) {
	tx := idempotencyTestTx(t)
	repo := NewIdempotencyRepository(tx)
	ctx := context.Background()

	now := time.Now().UTC()
	record := &idempotency.IdempotencyRecord{
		Scope:              uniqueIdempotencyValue(t, "idem-scope-success"),
		IdempotencyKeyHash: hashedTestValue(t, "idem-hash-success"),
		RequestFingerprint: hashedTestValue(t, "idem-fp-success"),
		Status:             idempotency.IdempotencyStatusProcessing,
		LockedUntil:        ptrTime(now.Add(10 * time.Second)),
		ExpiresAt:          now.Add(24 * time.Hour),
	}
	owner, err := repo.CreateProcessing(ctx, record)
	require.NoError(t, err)
	require.True(t, owner)

	require.NoError(t, repo.MarkSucceeded(ctx, record.ID, 200, `{"ok":true}`, now.Add(24*time.Hour)))

	got, err := repo.GetByScopeAndKeyHash(ctx, record.Scope, record.IdempotencyKeyHash)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, idempotency.IdempotencyStatusSucceeded, got.Status)
	require.NotNil(t, got.ResponseStatus)
	require.Equal(t, 200, *got.ResponseStatus)
	require.NotNil(t, got.ResponseBody)
	require.Equal(t, `{"ok":true}`, *got.ResponseBody)
	require.Nil(t, got.LockedUntil)
}

// idempotencyTestTx returns a transaction inside a fresh migrated PostgreSQL test
// container. Setup uses a two-minute context, and cleanup rolls back the test
// transaction before closing its pool and terminating the container, keeping
// each repository scenario's state isolated from subsequent tests. Container
// removal gets a fresh thirty-second context even when startup fails.
func idempotencyTestTx(t *testing.T) *sql.Tx {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("idempotency_contracts"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	// Register any created container before startup errors can stop this test.
	// Transaction and pool cleanup registered below runs before container removal.
	if container != nil {
		t.Cleanup(func() {
			// Setup or test cancellation must not cancel Docker removal. Start the
			// bounded cleanup context when this callback runs.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := container.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate PostgreSQL container: %v", err)
			}
		})
	}
	require.NoError(t, err)
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, postgresinfra.ApplyMigrations(ctx, db, migrations.FS))
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tx.Rollback()) })
	return tx
}

// uniqueIdempotencyValue combines a fixture prefix with the normalized test name.
// Slashes and spaces become underscores. Scope construction and hashedTestValue
// use this same deterministic representation to identify their fixture's named scenario.
func uniqueIdempotencyValue(t *testing.T, prefix string) string {
	t.Helper()
	safeName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	return fmt.Sprintf("%s-%s", prefix, safeName)
}

// hashedTestValue hashes the test name and prefix into 64 hex characters for VARCHAR(64) columns.
func hashedTestValue(t *testing.T, prefix string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(uniqueIdempotencyValue(t, prefix)))
	return hex.EncodeToString(sum[:])
}

// ptrTime returns a pointer holding the supplied fixture timestamp value.
func ptrTime(t time.Time) *time.Time { return &t }
