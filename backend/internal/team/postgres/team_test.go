package postgres

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	keypostgres "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/team"
)

func TestTeamRepositoryPreviewInvitationReturnsVerifiedSummary(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := NewTeamRepository(db, keypostgres.NewTeamKeys(db), billingpostgres.NewMemberUsageStore(db, nil))
	now := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour)
	mock.ExpectQuery("SELECT ti.id, ti.email, ti.status, ti.expires_at, t.name").
		WithArgs("token-hash").
		WillReturnRows(invitationPreviewRows(expiresAt, "member@example.com"))

	preview, err := repo.PreviewInvitation(context.Background(), "token-hash", "member@example.com", now)

	require.NoError(t, err)
	require.Equal(t, "词元流动", preview.TeamName)
	require.Equal(t, "喵窝", preview.InviterName)
	require.Equal(t, "owner@example.com", preview.InviterEmail)
	require.Equal(t, expiresAt, preview.ExpiresAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTeamRepositoryPreviewInvitationRejectsDifferentEmail(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := NewTeamRepository(db, keypostgres.NewTeamKeys(db), billingpostgres.NewMemberUsageStore(db, nil))
	now := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT ti.id, ti.email, ti.status, ti.expires_at, t.name").
		WithArgs("token-hash").
		WillReturnRows(invitationPreviewRows(now.Add(time.Hour), "other@example.com"))

	preview, err := repo.PreviewInvitation(context.Background(), "token-hash", "member@example.com", now)

	require.ErrorIs(t, err, team.ErrTeamInvitationEmail)
	require.Nil(t, preview)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTeamRepositoryPreviewInvitationMarksExpiredInvitation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := NewTeamRepository(db, keypostgres.NewTeamKeys(db), billingpostgres.NewMemberUsageStore(db, nil))
	now := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT ti.id, ti.email, ti.status, ti.expires_at, t.name").
		WithArgs("token-hash").
		WillReturnRows(invitationPreviewRows(now.Add(-time.Minute), "member@example.com"))
	mock.ExpectExec("UPDATE team_invitations SET status = 'expired'").
		WithArgs(int64(17), now).
		WillReturnResult(sqlmock.NewResult(0, 1))

	preview, err := repo.PreviewInvitation(context.Background(), "token-hash", "member@example.com", now)

	require.ErrorIs(t, err, team.ErrTeamInvitationExpired)
	require.Nil(t, preview)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTransferTeamOwnershipUsesTwoOrderedUpdates(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() {
		// Register Close after checking the transaction expectations so it does not affect their order.
		mock.ExpectClose()
		require.NoError(t, db.Close())
	}()

	mock.ExpectBegin()
	tx, err := db.Begin()
	require.NoError(t, err)
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE team_memberships SET role = 'member'")).
		WithArgs(int64(7), int64(11), int64(22), now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE team_memberships SET role = 'owner'")).
		WithArgs(int64(7), int64(11), int64(22), now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, transferTeamOwnership(context.Background(), tx, 7, 11, 22, now))
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func invitationPreviewRows(expiresAt time.Time, email string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id",
		"email",
		"status",
		"expires_at",
		"team_name",
		"inviter_name",
		"inviter_email",
	}).AddRow(
		int64(17),
		email,
		"pending",
		expiresAt,
		"词元流动",
		"喵窝",
		"owner@example.com",
	)
}
