package postgres

import (
	"context"
	"regexp"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// TestAnnouncementRepositoryArchiveExpired expects one bulk archive update against the SQL mock.
// It verifies active status, non-null inclusive end-time predicates, and the
// affected count returned to callers from the database result.
func TestAnnouncementRepositoryArchiveExpired(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Register cleanup as soon as the mock database exists. Cleanup errors are
	// discarded because this fixture asserts query behavior and both database wrappers may close it.
	t.Cleanup(func() { _ = db.Close() })

	client := dbent.NewClient(dbent.Driver(sql.OpenDB(dialect.Postgres, db)))
	// The Ent client wraps the same mock database. Release that wrapper during
	// test cleanup even if an expectation fails before the repository call completes.
	t.Cleanup(func() { _ = client.Close() })

	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "announcements" SET "status" = $1, "updated_at" = $2 WHERE ("announcements"."status" = $3 AND "announcements"."ends_at" IS NOT NULL) AND "announcements"."ends_at" <= $4`)).
		WithArgs(site.AnnouncementStatusArchived, sqlmock.AnyArg(), site.AnnouncementStatusActive, now).
		WillReturnResult(sqlmock.NewResult(0, 2))

	repo := NewAnnouncementRepository(client)
	updated, err := repo.ArchiveExpired(context.Background(), now)

	require.NoError(t, err)
	require.EqualValues(t, 2, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAnnouncementListOrder supplies default, recognized, and invalid administrator ordering options.
// It verifies selected storage fields and normalized directions, including the
// complete created_at-descending fallback for an unsupported column and direction.
func TestAnnouncementListOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string                      // named sorting scenario
		params pagination.PaginationParams // supplied ordering request
		wantBy string                      // expected storage field
		want   string                      // expected sort direction
	}{
		{
			name:   "default created_at desc",
			params: pagination.PaginationParams{},
			wantBy: "created_at",
			want:   "desc",
		},
		{
			name: "title asc",
			params: pagination.PaginationParams{
				SortBy:    "title",
				SortOrder: "ASC",
			},
			wantBy: "title",
			want:   "asc",
		},
		{
			name: "status desc",
			params: pagination.PaginationParams{
				SortBy:    "status",
				SortOrder: "desc",
			},
			wantBy: "status",
			want:   "desc",
		},
		{
			name: "invalid falls back",
			params: pagination.PaginationParams{
				SortBy:    "sideways",
				SortOrder: "wat",
			},
			wantBy: "created_at",
			want:   "desc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotBy, gotOrder := announcementListOrder(tt.params)
			if gotBy != tt.wantBy || gotOrder != tt.want {
				t.Fatalf("announcementListOrder(%+v) = (%q, %q), want (%q, %q)", tt.params, gotBy, gotOrder, tt.wantBy, tt.want)
			}
		})
	}
}
