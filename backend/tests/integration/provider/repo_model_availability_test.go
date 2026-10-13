package provider_test

import (
	"context"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Configured model availability is diagnosed from provider configuration even
// while runtime windows block scheduling. Captured Ent SQL checks the requested
// group, platform, status, and scheduling predicates, and excludes the
// transient fields that would hide configured candidates.

// TestListModelAvailabilityCandidates_GroupQueryIgnoresTransientState captures
// a group-scoped configured-candidate query using an empty mock result. Its
// WHERE clause includes configuration predicates and excludes expiry, overload,
// rate-limit, and temporary-pause filtering.
func TestListModelAvailabilityCandidates_GroupQueryIgnoresTransientState(t *testing.T) {
	var capturedSQL string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(captureEntQueryMatcher{actual: &capturedSQL}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })
	repo := newProviderStoreContract(client, db, nil)

	mock.ExpectQuery("model availability candidates").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	groupID := int64(42)
	providers, err := repo.ListModelAvailabilityCandidates(
		context.Background(),
		&groupID,
		[]string{capability.PlatformAnthropic},
		false,
	)
	require.NoError(t, err)
	require.Empty(t, providers)
	require.NoError(t, mock.ExpectationsWereMet())

	normalized := normalizeSQLWhitespace(capturedSQL)
	_, whereClause, found := strings.Cut(normalized, " WHERE ")
	require.True(t, found, "expected WHERE clause in query: %s", normalized)
	whereClause, _, _ = strings.Cut(whereClause, " ORDER BY ")
	for _, configuredPredicate := range []string{"group_id", "status", "schedulable", "platform"} {
		require.Contains(t, whereClause, configuredPredicate)
	}
	for _, transientPredicate := range []string{
		"rate_limit_reset_at",
		"overload_until",
		"temp_unschedulable_until",
		"expires_at",
		"auto_pause_on_expired",
	} {
		require.NotContains(t, whereClause, transientPredicate, "configured-state diagnosis must not filter transient predicate %q", transientPredicate)
	}
}
