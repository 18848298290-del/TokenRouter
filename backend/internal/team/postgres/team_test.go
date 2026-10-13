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

// TestTeamRepositoryPreviewInvitationReturnsVerifiedSummary supplies a
// pending row with future expiry and matching email. The returned team,
// inviter and deadline fields match the mocked row and satisfy its query
// expectation.
func TestTeamRepositoryPreviewInvitationReturnsVerifiedSummary(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Close the mock database after assertions; cleanup errors are discarded so
	// the preview result and query expectations remain the test's reported
	// observations.
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

// TestTeamRepositoryPreviewInvitationRejectsDifferentEmail supplies an
// unexpired pending invitation for another mailbox. Preview returns the
// email-mismatch sentinel and nil result after the expected token-hash query.
func TestTeamRepositoryPreviewInvitationRejectsDifferentEmail(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Close the mock database after assertions; cleanup errors are discarded so
	// the preview result and query expectations remain the test's reported
	// observations.
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

// TestTeamRepositoryPreviewInvitationMarksExpiredInvitation supplies a
// pending invitation whose deadline is already past. It observes the expiry
// update request and expired sentinel; the configured update itself succeeds.
func TestTeamRepositoryPreviewInvitationMarksExpiredInvitation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Close the mock database after assertions; cleanup errors are discarded so
	// the preview result and query expectations remain the test's reported
	// observations.
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

// TestTransferTeamOwnershipUsesTwoOrderedUpdates expects old-owner demotion
// before target-member promotion in one mock transaction. Both updates affect
// one row and commit succeeds, protecting SQL call order for the unique-owner
// rule.
func TestTransferTeamOwnershipUsesTwoOrderedUpdates(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() {
		// Register mock closure after transaction expectations are checked, keeping
		// the demotion/promotion/commit ordering separate from the deferred
		// resource cleanup expectation.
		mock.ExpectClose()
		require.NoError(t, db.Close())
	}()

	mock.ExpectBegin()
	tx, err := db.Begin()
	require.NoError(t, err)
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	// Expect demotion before promotion because the partial owner index
	// constrains each statement; a single multirow role swap can encounter the
	// still-existing owner.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE team_memberships SET role = 'member'")).
		WithArgs(int64(7), int64(11), int64(22), now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE team_memberships SET role = 'owner'")).
		WithArgs(int64(7), int64(11), int64(22), now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, transferTeamOwnership(context.Background(), tx, 7, 11, 22, now))
	// Complete the caller-owned mock transaction after the helper performs both
	// ordered updates, confirming that ownership-swap helper leaves commit to
	// its caller.
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

// invitationPreviewRows creates fresh mock rows with a pending invitation and
// supplied email/expiry. Fixed identifiers and team/inviter names match the
// preview scanner's seven-column order; callers choose time/email rejection
// cases separately.
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
