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

// idempotencyTestTx applies migrations in an isolated PostgreSQL instance and rolls back the test
// transaction at cleanup.
func idempotencyTestTx(t *testing.T) *sql.Tx {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("idempotency_contracts"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
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

// ptrTime returns a pointer to the supplied time.
func ptrTime(t time.Time) *time.Time { return &t }
