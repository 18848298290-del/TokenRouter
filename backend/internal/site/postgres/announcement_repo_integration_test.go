//go:build integration

package postgres

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/site"
	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
)

// TestAnnouncementConcurrentLocalization fetches two editing copies before saving the first one.
// It verifies that the stale copy returns a localization conflict and that
// storage retains the first edit's title and incremented content revision.
func TestAnnouncementConcurrentLocalization(t *testing.T) {
	ctx := context.Background()
	db := postgrescontainer.New(t)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	repo := NewAnnouncementRepository(client)
	en := "en"
	original := &site.Announcement{
		Title: "Notice", Content: "Content", Status: site.AnnouncementStatusActive, NotifyMode: site.AnnouncementNotifyModeSilent,
		Localization: site.AnnouncementLocalization{SourceLocale: &en, Source: site.AnnouncementCopy{Title: "Notice", Content: "Content"}, Revision: 1, SourceRevision: 1},
	}
	require.NoError(t, repo.Create(ctx, original))
	first, err := repo.GetByID(ctx, original.ID)
	require.NoError(t, err)
	second, err := repo.GetByID(ctx, original.ID)
	require.NoError(t, err)
	first.Title = "First"
	first.Localization.Source.Title = first.Title
	first.Localization.Revision++
	require.NoError(t, repo.Update(ctx, first))
	second.Title = "Second"
	second.Localization.Source.Title = second.Title
	second.Localization.Revision++
	require.ErrorIs(t, repo.Update(ctx, second), locale.ErrConflict)
	saved, err := repo.GetByID(ctx, original.ID)
	require.NoError(t, err)
	require.Equal(t, "First", saved.Title)
	require.Equal(t, int64(2), saved.Localization.Revision)
}
