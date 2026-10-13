//go:build integration

package apikey_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// A user with one remaining credential slot can receive overlapping creation
// requests through separate PostgreSQL connections. The case starts two writers
// together and checks both their results and the persisted count, using a
// dedicated migrated database that permits committed writes. Disabled-key
// counting and soft deletion are checked in separate sequential cases.
//
// A start channel holds both workers until their error slots are allocated.
// Each worker writes one indexed result, and the WaitGroup joins both writes
// before assertions inspect those results. Test cleanup deletes keys before the
// user after workers have ended, and the operation context supplies a finite
// budget for blocked database calls.

// TestAPIKeyRepository_CreateSerializesLastAvailableSlot releases two
// PostgreSQL creators against a user limit of one. It expects one success, one
// limit error, and exactly one persisted credential.
func TestAPIKeyRepository_CreateSerializesLastAvailableSlot(t *testing.T) {
	integrationDB, integrationEntClient := keyDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	email := fmt.Sprintf("api-key-limit-concurrency-%d@example.com", time.Now().UnixNano())
	user, err := integrationEntClient.User.Create().
		SetEmail(email).
		SetPasswordHash("test-password-hash").
		SetRole(identity.RoleUser).
		SetStatus(billing.StatusActive).
		SetAPIKeyLimit(1).
		Save(ctx)
	require.NoError(t, err)
	// Remove dependent keys before their owner after both workers have joined;
	// cleanup uses a fresh context so the work deadline does not cancel fixture
	// removal.
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM api_keys WHERE user_id = $1", user.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", user.ID)
	})

	repo := newKeyStoreFixture(integrationEntClient, integrationDB)
	// Hold both creation attempts at the same gate. Separate indexed error slots
	// and a join let the test inspect completed results after releasing the
	// writers.
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for index := range errs {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			errs[index] = repo.Create(ctx, newLimitedAPIKey(
				user.ID,
				fmt.Sprintf("sk-limit-concurrent-%d-%d", time.Now().UnixNano(), index),
				apikey.StatusAPIKeyActive,
			))
		}(index)
	}
	// Release both writers before joining them so owner-row serialization can
	// decide the available slot while assertions wait for every database result.
	close(start)
	wg.Wait()

	var succeeded, limited int
	for _, createErr := range errs {
		switch {
		case createErr == nil:
			succeeded++
		case errors.Is(createErr, apikey.ErrAPIKeyLimitReached):
			limited++
		default:
			require.NoError(t, createErr)
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, limited)

	count, err := repo.CountByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
}
