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

// TestAnnouncementRepositoryArchiveExpired checks the status and end-time
// predicates used by the bulk archive query.
func TestAnnouncementRepositoryArchiveExpired(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	client := dbent.NewClient(dbent.Driver(sql.OpenDB(dialect.Postgres, db)))
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

func TestAnnouncementListOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		params pagination.PaginationParams
		wantBy string
		want   string
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
