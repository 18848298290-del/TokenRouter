//go:build integration

package app

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	identitypg "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	moderationpg "github.com/TokenFlux/TokenRouter/internal/moderation/postgres"
)

// Cyber warning persistence can also disable its acting user and retain media
// in the same SQL transaction. The scenario checks a committed warning and then
// installs a failing warning-update trigger to verify user, warning, and media
// rollback together.

// TestModerationTransaction records a Cyber warning that reaches its auto-ban
// threshold. It checks committed status and media, then injects an update
// failure and checks the next transaction retains the active user with no
// warning or media rows.
func TestModerationTransaction(t *testing.T) {
	f := newDatabaseFixture(t)
	ctx := context.Background()
	repo := moderationpg.NewContentModerationRepository(f.db, func(tx *sql.Tx) moderationpg.UserStatusTx { return identitypg.NewRiskStatusParticipant(tx) })
	u, e := f.client.User.Create().SetEmail("test-fixture@example.test").SetPasswordHash("fixture").Save(ctx)
	require.NoError(t, e)
	t.Run("ban-and-warning-commit", func(t *testing.T) {
		w := &moderation.ContentModerationCyberWarning{RequestID: "test-success", UserID: &u.ID, WarningText: "cyber_policy", Media: []moderation.ContentModerationMedia{{Source: "user", MIMEType: "image/png", ByteSize: 5, SnapshotStatus: "ready", Content: []byte("image")}}}
		banned, e := repo.CreateCyberWarningAndApplyUserBan(ctx, w, moderation.ContentModerationCyberWarningPolicy{AutoBanEnabled: true, BanThreshold: 1, WindowHours: 24})
		require.NoError(t, e)
		require.True(t, banned)
		var status string
		require.NoError(t, f.db.QueryRowContext(ctx, "SELECT status FROM users WHERE id=$1", u.ID).Scan(&status))
		require.Equal(t, "disabled", status)
		require.Positive(t, w.ID)
		require.True(t, w.AutoBanned)
		var media []byte
		require.NoError(t, f.db.QueryRowContext(ctx, "SELECT content FROM content_moderation_media WHERE cyber_warning_id=$1", w.ID).Scan(&media))
		require.Equal(t, []byte("image"), media)
	})
	t.Run("policy-update-failure-rolls-back-user-and-warning", func(t *testing.T) {
		_, e := f.db.ExecContext(ctx, "UPDATE users SET status='active' WHERE id=$1", u.ID)
		require.NoError(t, e)
		// Fail the warning update after the transaction has begun its ban path.
		// This exposes whether user status and inserted warning media roll back with that failure.
		_, e = f.db.ExecContext(ctx, `CREATE FUNCTION test_reject_warning_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.request_id='test-rollback' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END $$; CREATE TRIGGER test_reject_warning_update BEFORE UPDATE ON content_moderation_cyber_warnings FOR EACH ROW EXECUTE FUNCTION test_reject_warning_update();`)
		require.NoError(t, e)
		w := &moderation.ContentModerationCyberWarning{RequestID: "test-rollback", UserID: &u.ID, WarningText: "cyber_policy", Media: []moderation.ContentModerationMedia{{Source: "user", MIMEType: "image/png", ByteSize: 5, SnapshotStatus: "ready", Content: []byte("image")}}}
		_, e = repo.CreateCyberWarningAndApplyUserBan(ctx, w, moderation.ContentModerationCyberWarningPolicy{AutoBanEnabled: true, BanThreshold: 1, WindowHours: 24})
		require.Error(t, e)
		var status string
		require.NoError(t, f.db.QueryRowContext(ctx, "SELECT status FROM users WHERE id=$1", u.ID).Scan(&status))
		require.Equal(t, "active", status)
		var n int
		require.NoError(t, f.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM content_moderation_cyber_warnings WHERE request_id='test-rollback'").Scan(&n))
		require.Zero(t, n)
		require.NoError(t, f.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM content_moderation_media WHERE cyber_warning_id=$1", w.ID).Scan(&n))
		require.Zero(t, n)
	})
}
