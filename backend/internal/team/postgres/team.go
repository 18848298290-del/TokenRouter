package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/team"
	usagequery "github.com/TokenFlux/TokenRouter/internal/usage/postgres/query"
)

// team.go persists team membership, invitation, and ownership changes with
// their credential lifecycle updates.
// A current team membership is constrained per account, and a team has one
// current owner. Creation and invitation acceptance serialize account
// participation before their related writes, while owner transfer demotes
// before promotion so its two statements respect the partial unique-owner
// index.
//
// Membership departure, rejoin and tenant teardown pass their SQL transaction
// to Key participants, keeping credential lifecycle changes with related
// membership/review records. Optional reporting selects shared readers or raw
// helpers, and displayed quota windows normalize through billing without
// writing those resets. Context readback follows commits on selected
// operations, so returned read errors can follow saved state. Expiry markers
// and old-review cancellation have separate ignored-error paths; their
// attempted writes do not establish successful persistence, and owned
// transactions still report other statement/commit failures.

// The compile-time assertion keeps this concrete SQL adapter aligned with the
// team lifecycle and authorized-report methods required by the service
// repository interface.
var _ team.TeamRepository = (*TeamRepository)(nil)

// TeamRepository coordinates SQL team lifecycle transactions, view
// normalization and retained Key/usage/report participants.
type TeamRepository struct {
	// calendar supplies retained calendar location rules for member views/raw
	// reports. NewTeamRepository keeps its first pointer;
	// NewTeamRepositoryWithReports owns a value copy retaining location/clock
	// references.
	calendar *timezone.Calendar
	// keys is retained for standalone credential calls and transaction
	// participation. Its InTx methods use the supplied SQL transaction and leave
	// completion to this repository.
	keys TeamKeys
	// usage is retained for selected member quota resets. The current billing
	// adapter writes on its own bound database handle; this repository delegates
	// its errors directly.
	usage MemberUsage
	// reports optionally supplies shared totals/trends. Nil selects raw helpers;
	// a configured reader's error is returned directly, and usage-log listing
	// always uses the raw helper.
	reports UsageReports
	// db is the caller-owned shared SQL handle for reads and owned transactions.
	// Assembly closes it after team and participant callers have stopped.
	db *sql.DB
}

// UsageReports supplies shared scoped team totals and actor trends after
// service authorization.
type UsageReports interface {
	// GetTeamUsageSummary returns totals for the supplied caller-authorized
	// team/range/filters. The report reader owns raw/preaggregated source
	// selection; errors remain visible because TeamRepository does not retry
	// them through its raw helper.
	GetTeamUsageSummary(context.Context, int64, team.TeamUsageQuery) (*team.TeamUsageSummary, error)
	// ListTeamMemberUsageSeries returns scoped current and departed actor trends
	// for an authorized team. The shared reader chooses its data source and
	// retains result storage; caller scope is established before this
	// delegation.
	ListTeamMemberUsageSeries(context.Context, int64, team.TeamUsageQuery) ([]team.TeamMemberUsageSeries, error)
}

// TeamRowScanner decodes the fixed SQL membership column sequence into caller
// destinations.
type TeamRowScanner interface {
	// Scan decodes the current SQL row into the supplied ordered destinations.
	// The caller supplies matching column count/types and manages row/result
	// lifetime; decode errors can leave destinations partially populated before
	// returning.
	Scan(dest ...any) error
}

// TeamKeys supplies standalone credential administration and participation in
// caller-owned membership transactions.
type TeamKeys interface {
	// ListTeamKeyStrings returns full nondeleted team credentials used for
	// authentication invalidation. The caller authorizes its selected team
	// lifecycle operation first; read failures remain errors for TeamRepository
	// and service publication handling.
	ListTeamKeyStrings(context.Context, int64) ([]string, error)
	// ListTeamKeys returns credential metadata for a caller-authorized team and
	// optional positive creator filter. It includes full Key text for service
	// masking, while related display fields and ordering belong to the Key
	// adapter.
	ListTeamKeys(context.Context, int64, *int64) ([]team.TeamAPIKeyItem, error)
	// DisableTeamKey sets owner disablement and disabled status on a matching
	// team credential. Caller authorization precedes optional creator filtering;
	// successful result returns the stored full credential for subsequent
	// service invalidation.
	DisableTeamKey(context.Context, int64, int64, *int64) (string, error)
	// EnableTeamKey clears owner disablement and activates a matching team
	// credential. Caller authorization permits clearing the independent owner
	// lock; successful result returns its full credential for subsequent
	// authentication invalidation.
	EnableTeamKey(context.Context, int64, int64, *int64) (string, error)
	// DeleteTeamKey returns the prior full credential after selected team Key
	// deletion. Its implementation owns locking/tombstone transaction
	// completion; callers establish team/creator scope before invoking it and
	// invalidate after success.
	DeleteTeamKey(context.Context, int64, int64, *int64) (string, error)
	// DisableMemberInTx disables the current member's nondeleted team
	// credentials through the supplied transaction. The membership repository
	// owns commit/rollback and supplies the shared lifecycle instant, so this
	// participant does not independently complete its write.
	DisableMemberInTx(context.Context, *sql.Tx, int64, int64, time.Time) error
	// DisableHistoricalMemberInTx disables that member's team credentials
	// created before the supplied rejoin instant. Equal timestamps remain
	// outside that historical predicate; the membership repository owns
	// transaction completion after invitation state is also written.
	DisableHistoricalMemberInTx(context.Context, *sql.Tx, int64, int64, time.Time) error
	// DisableTeamInTx disables all nondeleted team credentials in the supplied
	// transaction. Teardown supplies its shared lifecycle instant and owns
	// commit/rollback after team, membership and pending-review state are also
	// changed.
	DisableTeamInTx(context.Context, *sql.Tx, int64, time.Time) error
}

// MemberUsage supplies billing-owned calendar usage resets for selected
// ordinary team memberships.
type MemberUsage interface {
	// ResetMemberUsage clears selected current ordinary-member usage windows
	// with the supplied instant. Caller ownership and period selection precede
	// delegation; the billing adapter owns its bound database statement,
	// calendar rules and missing-membership result.
	ResetMemberUsage(context.Context, int64, int64, bool, bool, bool, time.Time) error
}

// NewTeamRepositoryWithReports retains the caller-owned database, Key/usage
// participants and shared report reader. It copies the calendar value but
// retains its location/clock references; nil reports select raw-query helpers
// and assembly closes shared resources after callers stop.
func NewTeamRepositoryWithReports(db *sql.DB, keys TeamKeys, usage MemberUsage, calendar timezone.Calendar, reports UsageReports) team.TeamRepository {
	return &TeamRepository{db: db, keys: keys, usage: usage, calendar: &calendar, reports: reports}
}

// NewTeamRepository retains the caller-owned database and Key/usage
// participants for SQL team operations. Only the first optional calendar
// pointer is used and remains shared; absent/nil calendar selects time.Local,
// and reports use raw-query helpers.
func NewTeamRepository(db *sql.DB, keys TeamKeys, usage MemberUsage, calendar ...*timezone.Calendar) team.TeamRepository {
	repo := &TeamRepository{db: db, keys: keys, usage: usage}
	if len(calendar) > 0 {
		repo.calendar = calendar[0]
	}
	return repo
}

// Create stores a new active team and owner membership, then returns owner
// context. Caller authorization and normalized name/ceiling precede this
// method; existing current membership rejects before either new row is
// inserted.
//
// The transaction takes the owner-user advisory lock before checking
// occupancy, then inserts team and owner records together and commits.
// Membership/commit constraint failures use the shared error mapper, and
// deferred rollback is attempted on exits with its error discarded. Context
// lookup occurs on the bound database after commit, so its error can follow a
// successfully created team; no user active-status check is added here.
func (r *TeamRepository) Create(ctx context.Context, name string, ownerUserID int64, memberLimit int) (*team.TeamContext, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// Attempt rollback on any exit before transaction completion, discarding
	// cleanup errors so the occupancy, insertion or commit failure remains this
	// creation's result.
	defer func() { _ = tx.Rollback() }()
	// Serialize account creation with invitation admission for the same user
	// before occupancy checking, then keep team/owner inserts in this
	// transaction until it commits.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, ownerUserID); err != nil {
		return nil, err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM team_memberships WHERE user_id = $1 AND left_at IS NULL)`, ownerUserID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, team.ErrTeamAlreadyJoined
	}
	var teamID int64
	if err = tx.QueryRowContext(ctx, `
		INSERT INTO teams (name, status, member_limit, created_at, updated_at)
		VALUES ($1, 'active', $2, NOW(), NOW()) RETURNING id`, name, memberLimit).Scan(&teamID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO team_memberships (team_id, user_id, role, joined_at, created_at, updated_at)
		VALUES ($1, $2, 'owner', NOW(), NOW(), NOW())`, teamID, ownerUserID); err != nil {
		return nil, MapTeamConstraintError(err)
	}
	// Commit the new team and owner together before context readback; that later
	// bound-database read can fail after both records have already been saved.
	if err = tx.Commit(); err != nil {
		return nil, MapTeamConstraintError(err)
	}
	return r.GetContextByTeamID(ctx, teamID)
}

// GetContextByUserID selects the requested account's current membership and
// undeleted team/actor/owner records. Team and account active status are not
// filtered; service authorization decides whether a suspended context permits
// its requested action.
func (r *TeamRepository) GetContextByUserID(ctx context.Context, userID int64) (*team.TeamContext, error) {
	return r.scanContext(ctx, `WHERE current_membership.user_id = $1 AND current_membership.left_at IS NULL`, userID)
}

// GetContextByTeamID selects owner-as-actor context for an undeleted team and
// undeleted actor/owner accounts. The caller authorizes
// administration/readback separately; no active-team/account filter is added
// and missing context returns team-not-found.
func (r *TeamRepository) GetContextByTeamID(ctx context.Context, teamID int64) (*team.TeamContext, error) {
	return r.scanContext(ctx, `WHERE t.id = $1 AND current_membership.role = 'owner'`, teamID)
}

// scanContext reads one team, acting membership and owner observation with
// the caller's fixed WHERE fragment and bound identity. Callers supply a
// trusted fragment; current memberships and undeleted team/accounts are
// required, while their active status is not filtered.
//
// One statement counts ordinary current memberships and fills separately
// allocated actor/owner records. No rows returns nil/team-not-found; another
// scan failure returns the possibly partial allocated context with its error.
// Successful scans normalize each membership's returned calendar quota
// windows using separate time.Now observations, without persisting resets.
// Service clock injection is not used for these view timestamps.
func (r *TeamRepository) scanContext(ctx context.Context, where string, arg int64) (*team.TeamContext, error) {
	// Read one joined context for the selected acting membership and current
	// owner; the fixed query fragments keep this row selection within the
	// requested account or team.
	query := `
		SELECT t.id, t.name, t.status, t.member_limit,
		       t.default_daily_limit_usd, t.default_weekly_limit_usd, t.default_monthly_limit_usd,
		       t.created_at, t.updated_at,
		       (SELECT COUNT(*) FROM team_memberships cm WHERE cm.team_id = t.id AND cm.left_at IS NULL AND cm.role = 'member') AS member_count,
		       current_membership.id, current_membership.team_id, current_membership.user_id,
		       actor_user.email, actor_user.username, current_membership.role,
		       current_membership.daily_limit_usd, current_membership.weekly_limit_usd, current_membership.monthly_limit_usd,
		       current_membership.daily_usage_usd, current_membership.weekly_usage_usd, current_membership.monthly_usage_usd,
		       current_membership.daily_window_start, current_membership.weekly_window_start, current_membership.monthly_window_start,
		       current_membership.joined_at, actor_user.last_active_at,
		       owner_membership.id, owner_membership.team_id, owner_membership.user_id,
		       owner_user.email, owner_user.username, owner_membership.role,
		       owner_membership.daily_limit_usd, owner_membership.weekly_limit_usd, owner_membership.monthly_limit_usd,
		       owner_membership.daily_usage_usd, owner_membership.weekly_usage_usd, owner_membership.monthly_usage_usd,
		       owner_membership.daily_window_start, owner_membership.weekly_window_start, owner_membership.monthly_window_start,
		       owner_membership.joined_at, owner_user.last_active_at
		FROM teams t
		JOIN team_memberships current_membership ON current_membership.team_id = t.id AND current_membership.left_at IS NULL
		JOIN users actor_user ON actor_user.id = current_membership.user_id AND actor_user.deleted_at IS NULL
		JOIN team_memberships owner_membership ON owner_membership.team_id = t.id AND owner_membership.left_at IS NULL AND owner_membership.role = 'owner'
		JOIN users owner_user ON owner_user.id = owner_membership.user_id AND owner_user.deleted_at IS NULL
		` + where + ` AND t.deleted_at IS NULL
		LIMIT 1`
	row := r.db.QueryRowContext(ctx, query, arg)
	teamCtx := &team.TeamContext{Team: &team.Team{}, Membership: &team.TeamMembership{}, Owner: &team.TeamMembership{}}
	err := row.Scan(
		&teamCtx.Team.ID, &teamCtx.Team.Name, &teamCtx.Team.Status, &teamCtx.Team.MemberLimit,
		&teamCtx.Team.DefaultDailyLimitUSD, &teamCtx.Team.DefaultWeeklyLimitUSD, &teamCtx.Team.DefaultMonthlyLimitUSD,
		&teamCtx.Team.CreatedAt, &teamCtx.Team.UpdatedAt, &teamCtx.Team.MemberCount,
		&teamCtx.Membership.ID, &teamCtx.Membership.TeamID, &teamCtx.Membership.UserID, &teamCtx.Membership.Email, &teamCtx.Membership.Username, &teamCtx.Membership.Role,
		&teamCtx.Membership.DailyLimitUSD, &teamCtx.Membership.WeeklyLimitUSD, &teamCtx.Membership.MonthlyLimitUSD,
		&teamCtx.Membership.DailyUsageUSD, &teamCtx.Membership.WeeklyUsageUSD, &teamCtx.Membership.MonthlyUsageUSD,
		&teamCtx.Membership.DailyWindowStart, &teamCtx.Membership.WeeklyWindowStart, &teamCtx.Membership.MonthlyWindowStart,
		&teamCtx.Membership.JoinedAt, &teamCtx.Membership.LastActiveAt,
		&teamCtx.Owner.ID, &teamCtx.Owner.TeamID, &teamCtx.Owner.UserID, &teamCtx.Owner.Email, &teamCtx.Owner.Username, &teamCtx.Owner.Role,
		&teamCtx.Owner.DailyLimitUSD, &teamCtx.Owner.WeeklyLimitUSD, &teamCtx.Owner.MonthlyLimitUSD,
		&teamCtx.Owner.DailyUsageUSD, &teamCtx.Owner.WeeklyUsageUSD, &teamCtx.Owner.MonthlyUsageUSD,
		&teamCtx.Owner.DailyWindowStart, &teamCtx.Owner.WeeklyWindowStart, &teamCtx.Owner.MonthlyWindowStart,
		&teamCtx.Owner.JoinedAt, &teamCtx.Owner.LastActiveAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, team.ErrTeamNotFound
	}
	if err == nil {
		r.normalizeMemberWindows(teamCtx.Membership, time.Now())
		r.normalizeMemberWindows(teamCtx.Owner, time.Now())
	}
	return teamCtx, err
}

// UpdateName replaces an undeleted team's name and update timestamp in one
// statement. Callers validate/authorize first; execution errors propagate and
// the companion affected count selects team-not-found when zero through
// RequireTeamAffected.
func (r *TeamRepository) UpdateName(ctx context.Context, teamID int64, name string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE teams SET name = $2, updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL`, teamID, name)
	return RequireTeamAffected(result, err)
}

// SetDefaultMemberLimits replaces the undeleted team's complete future-join
// USD triple. Callers validate/authorize first; current membership limits are
// not rewritten, and execution/zero affected outcomes use RequireTeamAffected
// before any service readback.
func (r *TeamRepository) SetDefaultMemberLimits(ctx context.Context, teamID int64, daily, weekly, monthly float64) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE teams SET default_daily_limit_usd = $2, default_weekly_limit_usd = $3,
			default_monthly_limit_usd = $4, updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`, teamID, daily, weekly, monthly)
	return RequireTeamAffected(result, err)
}

// ListMembers returns current memberships joined to undeleted accounts, with
// owner first then ascending join time. It adds no team-lifecycle or
// account-active filter, normalizes returned quota windows per row using
// time.Now and returns iteration errors; close failure is discarded.
func (r *TeamRepository) ListMembers(ctx context.Context, teamID int64) ([]team.TeamMembership, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT m.id, m.team_id, m.user_id, u.email, u.username, m.role,
		       m.daily_limit_usd, m.weekly_limit_usd, m.monthly_limit_usd,
		       m.daily_usage_usd, m.weekly_usage_usd, m.monthly_usage_usd,
		       m.daily_window_start, m.weekly_window_start, m.monthly_window_start,
		       m.joined_at, u.last_active_at
		FROM team_memberships m
		JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL
		WHERE m.team_id = $1 AND m.left_at IS NULL
		ORDER BY CASE WHEN m.role = 'owner' THEN 0 ELSE 1 END, m.joined_at ASC`, teamID)
	if err != nil {
		return nil, err
	}
	// Close member result rows on every exit so their connection can be reused;
	// scan/iteration errors remain the returned observation and closing errors
	// are discarded.
	defer func() { _ = rows.Close() }()
	members := make([]team.TeamMembership, 0)
	for rows.Next() {
		var member team.TeamMembership
		if err := ScanTeamMembership(rows, &member); err != nil {
			return nil, err
		}
		r.normalizeMemberWindows(&member, time.Now())
		members = append(members, member)
	}
	return members, rows.Err()
}

// CreateInvitation saves a pending invitation digest/deadline and returns its
// metadata. The caller supplies admitted/normalized review inputs and handles
// mail after this persistence result.
//
// The transaction revokes prior pending invitations for the same team and
// exact mailbox before inserting the new row, satisfying the pending
// team/mailbox uniqueness rule. Insert and commit errors stop successful
// return, with deferred rollback attempted and discarded cleanup errors. The
// returned row is scanned before commit but exposed after success; subsequent
// mail failure belongs to TeamService and does not roll this operation back.
func (r *TeamRepository) CreateInvitation(ctx context.Context, teamID, inviterUserID int64, email, tokenHash string, expiresAt time.Time) (*team.TeamInvitation, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// Attempt rollback after failed revoke/insert/commit work, keeping prior
	// pending review state within this transaction; discard cleanup errors in
	// favor of the operation result.
	defer func() { _ = tx.Rollback() }()
	// Revoke earlier pending reviews for this exact team/mailbox before
	// inserting replacement digest state, keeping the pending uniqueness slot
	// and new row coordinated through commit.
	if _, err = tx.ExecContext(ctx, `UPDATE team_invitations SET status = 'revoked', updated_at = NOW() WHERE team_id = $1 AND email = $2 AND status = 'pending'`, teamID, email); err != nil {
		return nil, err
	}
	invitation := &team.TeamInvitation{}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO team_invitations (team_id, inviter_user_id, email, token_hash, status, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'pending', $5, NOW(), NOW())
		RETURNING id, team_id, inviter_user_id, email, status, expires_at, accepted_at, created_at`,
		teamID, inviterUserID, email, tokenHash, expiresAt,
	).Scan(&invitation.ID, &invitation.TeamID, &invitation.InviterUserID, &invitation.Email, &invitation.Status, &invitation.ExpiresAt, &invitation.AcceptedAt, &invitation.CreatedAt)
	if err != nil {
		return nil, err
	}
	// Commit revoked predecessors and the inserted pending review before
	// returning its scanned metadata; later TeamService mail delivery remains
	// outside this persistence transaction.
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return invitation, nil
}

// ListInvitations attempts to mark expired pending reviews before listing
// every status for the team in descending creation order. The caller
// authorizes owner access, and no team/account active filter participates in
// this listing.
//
// Expiry marking runs separately on the bound database and discards its
// result/error, so a later successful list can still contain pending rows
// whose deadline has passed. Selection/scanning failures return errors,
// successful iteration returns its slice plus rows.Err, and close errors are
// discarded. Returned expiry/status observations therefore do not guarantee
// that the preceding marker was persisted.
func (r *TeamRepository) ListInvitations(ctx context.Context, teamID int64) ([]team.TeamInvitation, error) {
	// Attempt expiry marking before display selection, but ignore its
	// result/error so readable review metadata can still be listed with its
	// stored status after marker failure.
	_, _ = r.db.ExecContext(ctx, `UPDATE team_invitations SET status = 'expired', updated_at = NOW() WHERE team_id = $1 AND status = 'pending' AND expires_at <= NOW()`, teamID)
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, team_id, inviter_user_id, email, status, expires_at, accepted_at, created_at
		FROM team_invitations WHERE team_id = $1 ORDER BY created_at DESC`, teamID)
	if err != nil {
		return nil, err
	}
	// Close invitation result rows after selected metadata is scanned;
	// scan/iteration failures remain returned errors, while cleanup errors are
	// discarded and expiry-marker errors remain independently ignored.
	defer func() { _ = rows.Close() }()
	items := make([]team.TeamInvitation, 0)
	for rows.Next() {
		var item team.TeamInvitation
		if err := rows.Scan(&item.ID, &item.TeamID, &item.InviterUserID, &item.Email, &item.Status, &item.ExpiresAt, &item.AcceptedAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetInvitationByID reads one review within the selected team without
// restricting its status. Missing rows become invitation-invalid and other
// read failures propagate; reissue callers separately evaluate
// pending/expired state before replacing the digest.
func (r *TeamRepository) GetInvitationByID(ctx context.Context, teamID, invitationID int64) (*team.TeamInvitation, error) {
	item := &team.TeamInvitation{}
	err := r.db.QueryRowContext(ctx, `
		SELECT id, team_id, inviter_user_id, email, status, expires_at, accepted_at, created_at
		FROM team_invitations WHERE team_id = $1 AND id = $2`, teamID, invitationID).
		Scan(&item.ID, &item.TeamID, &item.InviterUserID, &item.Email, &item.Status, &item.ExpiresAt, &item.AcceptedAt, &item.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, team.ErrTeamInvitationInvalid
	}
	if err != nil {
		return nil, err
	}
	return item, nil
}

// ReissueInvitation replaces a selected pending or expired review's
// digest/deadline and returns its metadata. The caller supplies rate-admitted
// resend inputs and sends mail afterward; other states or missing rows return
// invitation-invalid.
//
// The transaction locks the selected review, revokes other pending rows for
// its team/exact mailbox, clears acceptance data and sets the replacement
// pending state before commit. Failed selection/write/commit returns through
// attempted deferred rollback. Successful replacement keeps the row
// identity/creation time while displacing the old token, so later delivery
// failure does not retain the prior digest.
func (r *TeamRepository) ReissueInvitation(ctx context.Context, teamID, invitationID int64, tokenHash string, expiresAt time.Time) (*team.TeamInvitation, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// Attempt rollback after failed locked replacement, keeping competing-review
	// revocation and token/acceptance edits within the same transaction; cleanup
	// errors do not replace its reported outcome.
	defer func() { _ = tx.Rollback() }()
	var email, status string
	// Hold the selected review lock while checking reissuable state and
	// replacing digest/acceptance fields, keeping that row stable through
	// competing-review revocation and commit.
	err = tx.QueryRowContext(ctx, `SELECT email, status FROM team_invitations WHERE id = $2 AND team_id = $1 FOR UPDATE`, teamID, invitationID).Scan(&email, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, team.ErrTeamInvitationInvalid
	}
	if err != nil {
		return nil, err
	}
	if status != "pending" && status != "expired" {
		return nil, team.ErrTeamInvitationInvalid
	}
	// Revoke other pending rows for the selected review's exact team/mailbox
	// before its replacement, aligning the pending uniqueness slot with the new
	// digest and cleared acceptance fields.
	if _, err = tx.ExecContext(ctx, `
		UPDATE team_invitations SET status = 'revoked', updated_at = NOW()
		WHERE team_id = $1 AND email = $2 AND id <> $3 AND status = 'pending'`, teamID, email, invitationID); err != nil {
		return nil, err
	}
	item := &team.TeamInvitation{}
	err = tx.QueryRowContext(ctx, `
		UPDATE team_invitations SET token_hash = $3, status = 'pending', expires_at = $4, accepted_by_user_id = NULL, accepted_at = NULL, updated_at = NOW()
		WHERE id = $2 AND team_id = $1 AND status IN ('pending', 'expired')
		RETURNING id, team_id, inviter_user_id, email, status, expires_at, accepted_at, created_at`,
		teamID, invitationID, tokenHash, expiresAt,
	).Scan(&item.ID, &item.TeamID, &item.InviterUserID, &item.Email, &item.Status, &item.ExpiresAt, &item.AcceptedAt, &item.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, team.ErrTeamInvitationInvalid
	}
	if err != nil {
		return nil, err
	}
	// Commit competing-review revocation and replacement before exposing the
	// scanned row, leaving any later mail failure with the new digest saved and
	// old token displaced.
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

// RevokeInvitation marks one matching pending team review revoked. Execution
// error propagates; RowsAffected error is discarded and its companion zero
// count returns invitation-invalid, while other counts report success without
// further token lookup.
func (r *TeamRepository) RevokeInvitation(ctx context.Context, teamID, invitationID int64) error {
	result, err := r.db.ExecContext(ctx, `UPDATE team_invitations SET status = 'revoked', updated_at = NOW() WHERE id = $2 AND team_id = $1 AND status = 'pending'`, teamID, invitationID)
	if err != nil {
		return err
	}
	// Discard affected-count errors but inspect their companion count; zero
	// selects invitation-invalid, leaving that reported missing-state outcome
	// separate from earlier statement execution failure.
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return team.ErrTeamInvitationInvalid
	}
	return nil
}

// PreviewInvitation returns display fields after digest, pending-state,
// deadline and case-insensitive normalized-mailbox checks. Its query requires
// undeleted team/inviter records but does not require active team status or
// current inviter ownership.
//
// A deadline at or before now attempts an expired marker, discarding that
// write's error before returning invitation-expired. Inviter greeting selects
// trimmed nonblank username before mailbox. This read grants no membership
// and holds no decision transaction; resolution later reloads state under
// locks before accepting a current invitation.
func (r *TeamRepository) PreviewInvitation(ctx context.Context, tokenHash, normalizedEmail string, now time.Time) (*team.TeamInvitationPreview, error) {
	var invitationID int64
	var invitationEmail, status string
	preview := &team.TeamInvitationPreview{}
	err := r.db.QueryRowContext(ctx, `
		SELECT ti.id, ti.email, ti.status, ti.expires_at, t.name,
		       COALESCE(NULLIF(BTRIM(inviter.username), ''), inviter.email), inviter.email
		FROM team_invitations ti
		JOIN teams t ON t.id = ti.team_id AND t.deleted_at IS NULL
		JOIN users inviter ON inviter.id = ti.inviter_user_id AND inviter.deleted_at IS NULL
		WHERE ti.token_hash = $1`, tokenHash).
		Scan(
			&invitationID,
			&invitationEmail,
			&status,
			&preview.ExpiresAt,
			&preview.TeamName,
			&preview.InviterName,
			&preview.InviterEmail,
		)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, team.ErrTeamInvitationInvalid
	}
	if err != nil {
		return nil, err
	}
	if status != "pending" {
		return nil, team.ErrTeamInvitationInvalid
	}
	// An elapsed deadline rejects preview even if its best-effort expired marker
	// fails; the reader does not turn that failed write into a usable invitation
	// summary.
	if !preview.ExpiresAt.After(now) {
		_, _ = r.db.ExecContext(ctx, `UPDATE team_invitations SET status = 'expired', updated_at = $2 WHERE id = $1 AND status = 'pending'`, invitationID, now)
		return nil, team.ErrTeamInvitationExpired
	}
	if !strings.EqualFold(invitationEmail, normalizedEmail) {
		return nil, team.ErrTeamInvitationEmail
	}
	return preview, nil
}

// ResolveInvitation confirms a pending recipient-bound review and applies
// decline or ordinary-member admission. Callers supply exact
// accepted/declined resolution, normalized current email and evaluation time;
// other resolution text takes the acceptance branch here, so the service
// validates it first.
//
// User advisory locking precedes invitation and team row locks. Acceptance
// checks active undeleted team, ordinary-member ceiling and current user
// membership, then stores inherited limits, disables earlier-created member
// Keys, accepts this review and revokes other pending reviews for its exact
// mailbox across teams. The count is queried after the team lock; visible
// rows still follow database isolation. Commit precedes context readback,
// whose error can follow admitted membership.
//
// Decline commits and returns nil context. Expiry attempts its marker and
// commit with both errors discarded, then returns expired; it does not
// guarantee that marker reached storage. Other failures attempt deferred
// rollback, with cleanup errors discarded and selected uniqueness errors
// mapped to already-joined.
func (r *TeamRepository) ResolveInvitation(ctx context.Context, tokenHash string, userID int64, normalizedEmail, resolution string, now time.Time) (*team.TeamContext, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// Attempt rollback on rejected or failed confirmation, retaining membership,
	// historical-Key and invitation changes in this transaction;
	// successful/expiry commits leave deferred cleanup errors discarded.
	defer func() { _ = tx.Rollback() }()
	// Serialize this user before invitation/team row locks so competing
	// acceptances follow the same lock order and current-membership admission is
	// checked within that coordinated transaction.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, userID); err != nil {
		return nil, err
	}
	var invitationID, teamID int64
	var email, status string
	var expiresAt time.Time
	err = tx.QueryRowContext(ctx, `SELECT id, team_id, email, status, expires_at FROM team_invitations WHERE token_hash = $1 FOR UPDATE`, tokenHash).
		Scan(&invitationID, &teamID, &email, &status, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, team.ErrTeamInvitationInvalid
	}
	if err != nil {
		return nil, err
	}
	if status != "pending" {
		return nil, team.ErrTeamInvitationInvalid
	}
	// An elapsed review attempts its expired marker and commit but discards both
	// errors before returning expired, so callers cannot infer that expiry
	// status was saved.
	if !expiresAt.After(now) {
		_, _ = tx.ExecContext(ctx, `UPDATE team_invitations SET status = 'expired', updated_at = $2 WHERE id = $1`, invitationID, now)
		_ = tx.Commit()
		return nil, team.ErrTeamInvitationExpired
	}
	if !strings.EqualFold(email, normalizedEmail) {
		return nil, team.ErrTeamInvitationEmail
	}
	// A confirmed decline saves review status and commits without membership
	// admission, returning nil context; accepted-only team/capacity checks
	// remain in the following branch.
	if resolution == "declined" {
		if _, err = tx.ExecContext(ctx, `UPDATE team_invitations SET status = 'declined', updated_at = $2 WHERE id = $1`, invitationID, now); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	var teamStatus string
	var defaultDaily, defaultWeekly, defaultMonthly float64
	var memberLimit, memberCount int
	err = tx.QueryRowContext(ctx, `
		SELECT t.status, t.member_limit, t.default_daily_limit_usd,
		       t.default_weekly_limit_usd, t.default_monthly_limit_usd
		FROM teams t WHERE t.id = $1 AND t.deleted_at IS NULL FOR UPDATE`, teamID).
		Scan(&teamStatus, &memberLimit, &defaultDaily, &defaultWeekly, &defaultMonthly)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, team.ErrTeamNotFound
	}
	if err != nil {
		return nil, err
	}
	if teamStatus != team.TeamStatusActive {
		return nil, team.ErrTeamSuspended
	}
	// Query ordinary-member occupancy after acquiring the team lock rather than
	// retaining a pre-wait count; visibility of preceding commits follows the
	// database's configured transaction isolation.
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM team_memberships WHERE team_id = $1 AND left_at IS NULL AND role = 'member'`, teamID).Scan(&memberCount); err != nil {
		return nil, err
	}
	if memberCount >= memberLimit {
		return nil, team.ErrTeamMemberLimitReached
	}
	var alreadyJoined bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM team_memberships WHERE user_id = $1 AND left_at IS NULL)`, userID).Scan(&alreadyJoined); err != nil {
		return nil, err
	}
	if alreadyJoined {
		return nil, team.ErrTeamAlreadyJoined
	}
	// Store the newly admitted ordinary membership with the defaults read under
	// the team lock, keeping its join instant and historical-Key/review changes
	// in the same transaction.
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO team_memberships (
			team_id, user_id, role, daily_limit_usd, weekly_limit_usd, monthly_limit_usd,
			joined_at, created_at, updated_at
		)
		VALUES ($1, $2, 'member', $3, $4, $5, $6, $6, $6)`,
		teamID, userID, defaultDaily, defaultWeekly, defaultMonthly, now); err != nil {
		return nil, MapTeamConstraintError(err)
	}
	// Disable this member's credentials created before the supplied join instant
	// within the membership transaction; equal timestamps remain eligible under
	// the Key participant's historical predicate.
	if err = r.keys.DisableHistoricalMemberInTx(ctx, tx, teamID, userID, now); err != nil {
		return nil, err
	}
	// Accept this review after member/Key writes, then revoke every other
	// pending invitation for its exact mailbox across teams before committing
	// the complete admission sequence.
	if _, err = tx.ExecContext(ctx, `
		UPDATE team_invitations SET status = 'accepted', accepted_by_user_id = $2, accepted_at = $3, updated_at = $3 WHERE id = $1`, invitationID, userID, now); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE team_invitations SET status = 'revoked', updated_at = $2
		WHERE email = $1 AND status = 'pending' AND id <> $3`, email, now, invitationID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, MapTeamConstraintError(err)
	}
	// Read admitted context after commit on the bound database; a failed read
	// returns an error after membership and review changes may already be
	// persisted.
	return r.GetContextByUserID(ctx, userID)
}

// RemoveMember ends the selected current ordinary membership and disables its
// team credentials with one supplied instant. Owner rows do not match, and a
// companion zero affected count becomes membership-required after discarding
// RowsAffected error.
//
// Membership left/update times are written before the Key participant runs in
// the same transaction, so participant failure returns before commit and
// cleanup attempts rollback. The participant must use that supplied
// transaction and leave completion here. Commit errors remain visible, and
// the service requests optional authentication invalidation after this method
// successfully returns.
func (r *TeamRepository) RemoveMember(ctx context.Context, teamID, userID int64, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Attempt rollback when departure or Key participation fails before commit,
	// keeping the member's left marker and credential disablement under one
	// owned transaction.
	defer func() { _ = tx.Rollback() }()
	// Write the ordinary member's departure before disabling credentials in the
	// same transaction; the participant uses this supplied connection and leaves
	// commit with the membership repository.
	result, err := tx.ExecContext(ctx, `
		UPDATE team_memberships SET left_at = $3, updated_at = $3
		WHERE team_id = $1 AND user_id = $2 AND left_at IS NULL AND role = 'member'`, teamID, userID, now)
	if err != nil {
		return err
	}
	// Discard affected-count errors but inspect their companion count; zero
	// selects membership-required, leaving that reported missing-state outcome
	// separate from earlier statement execution failure.
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return team.ErrTeamMembershipRequired
	}
	if err = r.keys.DisableMemberInTx(ctx, tx, teamID, userID, now); err != nil {
		return err
	}
	// Commit departure and credential disablement together after participant
	// success, making the service's later invalidation depend on this accepted
	// persistence result.
	return tx.Commit()
}

// UpdateMemberLimits replaces the current ordinary member's complete USD
// triple and update timestamp. Callers reject negatives and authorize
// ownership; execution failures propagate, while RowsAffected error is
// discarded and zero companion count becomes membership-required.
func (r *TeamRepository) UpdateMemberLimits(ctx context.Context, teamID, userID int64, daily, weekly, monthly float64) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE team_memberships SET daily_limit_usd = $3, weekly_limit_usd = $4, monthly_limit_usd = $5, updated_at = NOW()
		WHERE team_id = $1 AND user_id = $2 AND left_at IS NULL AND role = 'member'`, teamID, userID, daily, weekly, monthly)
	if err != nil {
		return err
	}
	// Discard affected-count errors but inspect their companion count; zero
	// selects membership-required, leaving that reported missing-state outcome
	// separate from earlier statement execution failure.
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return team.ErrTeamMembershipRequired
	}
	return nil
}

// ResetMemberUsage delegates selected calendar resets with team/member IDs
// and evaluation time. Caller validation selects at least one period and
// establishes ownership; the retained billing participant owns its bound
// database write and missing-membership result.
func (r *TeamRepository) ResetMemberUsage(ctx context.Context, teamID, userID int64, resetDaily, resetWeekly, resetMonthly bool, now time.Time) error {
	return r.usage.ResetMemberUsage(ctx, teamID, userID, resetDaily, resetWeekly, resetMonthly, now)
}

// CreateOwnershipTransfer stores a pending digest/deadline for a target
// current ordinary member. The caller supplies current-owner authorization,
// while this query does not independently confirm originating owner or target
// account active status.
//
// Its transaction attempts cancellation of all prior pending team reviews
// before inserting the new one. Cancellation result/error is discarded, but a
// PostgreSQL statement error can abort the transaction and surface through
// later insertion/commit. Successful return exposes the scanned row after
// commit, while later mail is separate; selection, insert or commit failure
// attempts deferred rollback with cleanup errors discarded.
func (r *TeamRepository) CreateOwnershipTransfer(ctx context.Context, teamID, fromUserID, toUserID int64, tokenHash string, expiresAt time.Time) (*team.TeamOwnershipTransfer, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// Attempt rollback when target selection, replacement insertion or commit
	// fails; old-review cancellation belongs to this transaction, and its
	// cleanup errors are discarded on return.
	defer func() { _ = tx.Rollback() }()
	var targetMember bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM team_memberships WHERE team_id = $1 AND user_id = $2 AND left_at IS NULL AND role = 'member')`, teamID, toUserID).Scan(&targetMember); err != nil {
		return nil, err
	}
	if !targetMember {
		return nil, team.ErrTeamTransferInvalid
	}
	// Attempt to displace pending reviews before inserting the successor review;
	// ignore the cancellation error, while a PostgreSQL aborted transaction can
	// still fail the following insert or commit.
	_, _ = tx.ExecContext(ctx, `UPDATE team_ownership_transfers SET status = 'cancelled', resolved_at = NOW(), updated_at = NOW() WHERE team_id = $1 AND status = 'pending'`, teamID)
	item := &team.TeamOwnershipTransfer{}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO team_ownership_transfers (team_id, from_user_id, to_user_id, token_hash, status, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'pending', $5, NOW(), NOW())
		RETURNING id, team_id, from_user_id, to_user_id, status, expires_at, resolved_at, created_at`,
		teamID, fromUserID, toUserID, tokenHash, expiresAt,
	).Scan(&item.ID, &item.TeamID, &item.FromUserID, &item.ToUserID, &item.Status, &item.ExpiresAt, &item.ResolvedAt, &item.CreatedAt)
	if err != nil {
		return nil, err
	}
	// Commit pending-review displacement and the new digest before returning
	// metadata, leaving optional mail handling and its failure consequence with
	// TeamService after persistence.
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

// ResolveOwnershipTransfer resolves a pending review for its exact target
// actor under a serializable transaction. Missing/nonpending/wrong-target
// rows reject, and callers supply exact accepted/declined text because other
// text follows acceptance here.
//
// Decline saves its state, commits and reads the actor context. Acceptance
// demotes owner before promoting the ordinary target, records accepted review
// state and commits before actor-context readback. Both paths can return a
// read error after a saved decision; serialization failures are returned
// without retry. Expiry attempts marker and commit with both errors discarded
// before returning transfer-expired, and other exits attempt deferred
// rollback.
func (r *TeamRepository) ResolveOwnershipTransfer(ctx context.Context, tokenHash string, actorUserID int64, resolution string, now time.Time) (*team.TeamContext, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	// Attempt rollback on failed target review or owner swap, retaining
	// role/review changes within the serializable transaction; discard cleanup
	// errors after any successful or attempted expiry commit.
	defer func() { _ = tx.Rollback() }()
	var id, teamID, fromUserID, toUserID int64
	var status string
	var expiresAt time.Time
	err = tx.QueryRowContext(ctx, `SELECT id, team_id, from_user_id, to_user_id, status, expires_at FROM team_ownership_transfers WHERE token_hash = $1 FOR UPDATE`, tokenHash).
		Scan(&id, &teamID, &fromUserID, &toUserID, &status, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, team.ErrTeamTransferInvalid
	}
	if err != nil {
		return nil, err
	}
	if status != "pending" || actorUserID != toUserID {
		return nil, team.ErrTeamTransferInvalid
	}
	// An elapsed transfer attempts marking and commit with errors discarded,
	// then reports expired; callers still have no assurance that the attempted
	// status marker reached storage.
	if !expiresAt.After(now) {
		_, _ = tx.ExecContext(ctx, `UPDATE team_ownership_transfers SET status = 'expired', resolved_at = $2, updated_at = $2 WHERE id = $1`, id, now)
		_ = tx.Commit()
		return nil, team.ErrTeamTransferExpired
	}
	// Save a target-confirmed decline before committing and reading context,
	// leaving owner roles unchanged but allowing a readback error after review
	// status is already saved.
	if resolution == "declined" {
		if _, err = tx.ExecContext(ctx, `UPDATE team_ownership_transfers SET status = 'declined', resolved_at = $2, updated_at = $2 WHERE id = $1`, id, now); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return r.GetContextByUserID(ctx, actorUserID)
	}
	// Swap owner roles before recording accepted review state in the same
	// transaction, then commit before readback so each saved decision
	// corresponds to the selected role transition.
	if err = transferTeamOwnership(ctx, tx, teamID, fromUserID, toUserID, now); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE team_ownership_transfers SET status = 'accepted', resolved_at = $2, updated_at = $2 WHERE id = $1`, id, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return r.GetContextByUserID(ctx, actorUserID)
}

// CancelOwnershipTransfer marks pending team reviews from the selected
// originating actor cancelled and records now. Execution failure propagates;
// RowsAffected error is discarded and companion zero returns transfer-invalid
// before service acknowledgement.
func (r *TeamRepository) CancelOwnershipTransfer(ctx context.Context, teamID, actorUserID int64, now time.Time) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE team_ownership_transfers SET status = 'cancelled', resolved_at = $3, updated_at = $3
		WHERE team_id = $1 AND from_user_id = $2 AND status = 'pending'`, teamID, actorUserID, now)
	if err != nil {
		return err
	}
	// Discard affected-count errors but inspect their companion count; zero
	// selects transfer-invalid, leaving that reported missing-state outcome
	// separate from earlier statement execution failure.
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return team.ErrTeamTransferInvalid
	}
	return nil
}

// Dissolve ends an undeleted tenant and its current
// memberships/reviews/credentials using one supplied lifecycle instant. The
// caller authorizes owner/administrator action; a missing team becomes
// team-not-found after the initial conditional write.
//
// The transaction suspends and soft-deletes the team, sets current
// memberships left, revokes pending invitations and cancels pending ownership
// reviews before asking the Key participant to disable team credentials. That
// participant uses this transaction and leaves completion to the repository.
// Errors return before successful commit with deferred rollback attempted;
// service cache publication follows only the accepted result.
func (r *TeamRepository) Dissolve(ctx context.Context, teamID int64, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Attempt rollback if any teardown phase or Key participant fails before
	// commit, keeping tenant/member/review/credential lifecycle changes within
	// this owned transaction.
	defer func() { _ = tx.Rollback() }()
	// End the tenant first, then memberships and pending reviews before Key
	// participation; one transaction retains this lifecycle sequence until
	// credential disablement also succeeds. The initial write returns execution
	// errors first; its affected-count error is discarded and companion zero
	// selects team-not-found before later teardown phases.
	if result, execErr := tx.ExecContext(ctx, `UPDATE teams SET status = 'suspended', deleted_at = $2, updated_at = $2 WHERE id = $1 AND deleted_at IS NULL`, teamID, now); execErr != nil {
		return execErr
	} else if affected, _ := result.RowsAffected(); affected == 0 {
		return team.ErrTeamNotFound
	}
	if _, err = tx.ExecContext(ctx, `UPDATE team_memberships SET left_at = $2, updated_at = $2 WHERE team_id = $1 AND left_at IS NULL`, teamID, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE team_invitations SET status = 'revoked', updated_at = $2 WHERE team_id = $1 AND status = 'pending'`, teamID, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE team_ownership_transfers SET status = 'cancelled', resolved_at = $2, updated_at = $2 WHERE team_id = $1 AND status = 'pending'`, teamID, now); err != nil {
		return err
	}
	if err = r.keys.DisableTeamInTx(ctx, tx, teamID, now); err != nil {
		return err
	}
	// Commit tenant/member/review teardown with credential disablement after
	// every participant accepts, leaving optional service cache publication
	// outside this database transaction.
	return tx.Commit()
}

// SetStatus replaces an undeleted team status and update timestamp in one
// statement. Caller validation/authorization supplies supported text;
// execution and companion zero affected outcomes use RequireTeamAffected
// before the service requests optional invalidation.
func (r *TeamRepository) SetStatus(ctx context.Context, teamID int64, status string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE teams SET status = $2, updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL`, teamID, status)
	return RequireTeamAffected(result, err)
}

// SetMemberLimit replaces an undeleted team's ordinary-member ceiling and
// update timestamp. Caller validation/authorization precedes storage
// constraints; zero prevents ordinary admission and execution/zero affected
// outcomes use RequireTeamAffected.
func (r *TeamRepository) SetMemberLimit(ctx context.Context, teamID int64, limit int) error {
	result, err := r.db.ExecContext(ctx, `UPDATE teams SET member_limit = $2, updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL`, teamID, limit)
	return RequireTeamAffected(result, err)
}

// UpdateAdmin writes each nonnil administrator field with update time in one
// statement. Nil fields omit their columns, but a direct entirely empty call
// still advances updated_at; the service avoids that write and reads context
// for an empty update.
func (r *TeamRepository) UpdateAdmin(ctx context.Context, teamID int64, update team.TeamAdminUpdate) error {
	setClauses := []string{"updated_at = NOW()"}
	args := []any{teamID}
	if update.Name != nil {
		args = append(args, *update.Name)
		setClauses = append(setClauses, fmt.Sprintf("name = $%d", len(args)))
	}
	if update.Status != nil {
		args = append(args, *update.Status)
		setClauses = append(setClauses, fmt.Sprintf("status = $%d", len(args)))
	}
	if update.MemberLimit != nil {
		args = append(args, *update.MemberLimit)
		setClauses = append(setClauses, fmt.Sprintf("member_limit = $%d", len(args)))
	}
	result, err := r.db.ExecContext(ctx, `UPDATE teams SET `+strings.Join(setClauses, ", ")+` WHERE id = $1 AND deleted_at IS NULL`, args...)
	return RequireTeamAffected(result, err)
}

// ForceTransfer swaps the current owner to another current ordinary member in
// a serializable transaction. It locks the current owner, rejects equal IDs
// and requires an undeleted target account; target active status is not
// separately checked.
//
// The ordered swap enforces one matched owner demotion and one matched target
// promotion, clearing both member-limit triples while retaining
// usage/window/join records. Pending review cancellation is attempted
// afterward with its error discarded, though an aborted transaction can still
// fail commit. Commit precedes team-context readback, so read errors can
// follow saved ownership; serialization failure returns without retry and
// other exits attempt deferred rollback.
func (r *TeamRepository) ForceTransfer(ctx context.Context, teamID, toUserID int64, now time.Time) (*team.TeamContext, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	// Attempt rollback when target validation, owner swap or commit fails,
	// leaving changed owner roles and attempted review cancellation inside this
	// serializable transaction.
	defer func() { _ = tx.Rollback() }()
	var fromUserID int64
	// Lock the current owner before validating the distinct undeleted target and
	// swapping roles, retaining that owner observation through the serializable
	// transaction without a retry loop.
	if err = tx.QueryRowContext(ctx, `SELECT user_id FROM team_memberships WHERE team_id = $1 AND left_at IS NULL AND role = 'owner' FOR UPDATE`, teamID).Scan(&fromUserID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, team.ErrTeamNotFound
		}
		return nil, err
	}
	if fromUserID == toUserID {
		return nil, team.ErrTeamTransferInvalid
	}
	var targetAvailable bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id = $1 AND deleted_at IS NULL)`, toUserID).Scan(&targetAvailable); err != nil {
		return nil, err
	}
	if !targetAvailable {
		return nil, team.ErrTeamTransferInvalid
	}
	if err = transferTeamOwnership(ctx, tx, teamID, fromUserID, toUserID, now); err != nil {
		return nil, err
	}
	// Attempt cancellation of pending ownership reviews after the forced role
	// swap, discarding that statement error while leaving any resulting
	// transaction abort visible at commit.
	_, _ = tx.ExecContext(ctx, `UPDATE team_ownership_transfers SET status = 'cancelled', resolved_at = $2, updated_at = $2 WHERE team_id = $1 AND status = 'pending'`, teamID, now)
	// Commit the owner swap and attempted review cancellation before
	// team-context readback; a later read error can prevent service publication
	// despite saved ownership changes.
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return r.GetContextByTeamID(ctx, teamID)
}

// transferTeamOwnership demotes a matched current owner before promoting a
// distinct current ordinary member within the caller-owned transaction. Each
// statement must report exactly one affected row; RowsAffected errors are
// discarded and any other companion count returns transfer-invalid.
//
// Demotion first releases the partial unique-owner slot before promotion,
// keeping statement-level uniqueness from observing two current owners. Both
// limit triples become zero while usage, window starts and join times remain
// with their memberships. A failed promotion can follow a successful demotion
// within tx, so callers roll back failure and commit success themselves.
// ResolveOwnershipTransfer and ForceTransfer attempt deferred rollback and
// own completion.
func transferTeamOwnership(ctx context.Context, tx *sql.Tx, teamID, fromUserID, toUserID int64, now time.Time) error {
	// Demote the old owner before promoting the target so the partial unique
	// current-owner index releases its slot between statements; zero both limit
	// triples while retaining recorded usage/windows.
	demoted, err := tx.ExecContext(ctx, `
		UPDATE team_memberships SET role = 'member', daily_limit_usd = 0, weekly_limit_usd = 0, monthly_limit_usd = 0, updated_at = $4
		WHERE team_id = $1 AND user_id = $2 AND user_id <> $3 AND left_at IS NULL AND role = 'owner'`, teamID, fromUserID, toUserID, now)
	if err != nil {
		return err
	}
	// Require exactly one old-owner row before promotion; an affected-count
	// error is discarded but any companion count other than one rejects this
	// swap before completion.
	if affected, _ := demoted.RowsAffected(); affected != 1 {
		return team.ErrTeamTransferInvalid
	}
	promoted, err := tx.ExecContext(ctx, `
		UPDATE team_memberships SET role = 'owner', daily_limit_usd = 0, weekly_limit_usd = 0, monthly_limit_usd = 0, updated_at = $4
		WHERE team_id = $1 AND user_id = $3 AND user_id <> $2 AND left_at IS NULL AND role = 'member'`, teamID, fromUserID, toUserID, now)
	if err != nil {
		return err
	}
	// Require exactly one current ordinary target after demotion; mismatched
	// affected count returns transfer-invalid and the owning transaction caller
	// attempts rollback of the earlier role write.
	if affected, _ := promoted.RowsAffected(); affected != 1 {
		return team.ErrTeamTransferInvalid
	}
	return nil
}

// ListAdmin returns undeleted teams with current owner identity in descending
// creation order. Ordinary-member counts use current member rows, and the
// joined owner account has no deleted/active filter here; scan/iteration
// errors propagate while close failure is discarded.
func (r *TeamRepository) ListAdmin(ctx context.Context) ([]team.TeamAdminListItem, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT t.id, t.name, t.status, t.member_limit,
		       t.default_daily_limit_usd, t.default_weekly_limit_usd, t.default_monthly_limit_usd,
		       t.created_at, t.updated_at,
		       (SELECT COUNT(*) FROM team_memberships cm WHERE cm.team_id = t.id AND cm.left_at IS NULL AND cm.role = 'member'),
		       owner.user_id, u.email
		FROM teams t
		JOIN team_memberships owner ON owner.team_id = t.id AND owner.left_at IS NULL AND owner.role = 'owner'
		JOIN users u ON u.id = owner.user_id
		WHERE t.deleted_at IS NULL ORDER BY t.created_at DESC`)
	if err != nil {
		return nil, err
	}
	// Close administrator list rows on every return path to release their result
	// resources; scan/iteration errors remain visible and closing errors are
	// discarded.
	defer func() { _ = rows.Close() }()
	items := make([]team.TeamAdminListItem, 0)
	for rows.Next() {
		var item team.TeamAdminListItem
		if err := rows.Scan(
			&item.ID, &item.Name, &item.Status, &item.MemberLimit,
			&item.DefaultDailyLimitUSD, &item.DefaultWeeklyLimitUSD, &item.DefaultMonthlyLimitUSD,
			&item.CreatedAt, &item.UpdatedAt, &item.MemberCount, &item.OwnerUserID, &item.OwnerEmail,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListTeamKeyStrings delegates full credential enumeration for a
// caller-authorized team's invalidation attempt. The retained Key adapter
// owns selection and read errors; TeamService suppresses those listing errors
// after its selected mutation.
func (r *TeamRepository) ListTeamKeyStrings(ctx context.Context, teamID int64) ([]string, error) {
	return r.keys.ListTeamKeyStrings(ctx, teamID)
}

// GetUsageSummary delegates authorized totals to configured shared reports or
// raw-query helpers when absent. Report errors return directly without a raw
// fallback; callers supply normalized range/member scope and raw queries use
// this repository's calendar.
func (r *TeamRepository) GetUsageSummary(ctx context.Context, teamID int64, query team.TeamUsageQuery) (*team.TeamUsageSummary, error) {
	// Choose configured shared reporting before raw helpers, returning its
	// result/error directly; raw queries apply only when that reader is absent,
	// not when its query fails.
	if r.reports != nil {
		return r.reports.GetTeamUsageSummary(ctx, teamID, query)
	}
	return usagequery.GetUsageSummary(ctx, r.db, teamID, query, r.dateCalendar())
}

// ListMemberUsageSeries delegates authorized actor trends to shared reports
// or raw-query helpers when absent. A shared-reader error remains visible
// without raw retry; caller scope precedes this method and returned report
// data stays with its reader.
func (r *TeamRepository) ListMemberUsageSeries(ctx context.Context, teamID int64, query team.TeamUsageQuery) ([]team.TeamMemberUsageSeries, error) {
	// Choose configured shared reporting before raw helpers, returning its
	// result/error directly; raw queries apply only when that reader is absent,
	// not when its query fails.
	if r.reports != nil {
		return r.reports.ListTeamMemberUsageSeries(ctx, teamID, query)
	}
	return usagequery.ListMemberUsageSeries(ctx, r.db, teamID, query, r.dateCalendar())
}

// ListUsageLogs delegates raw scoped records and total to the usage query
// helper. The caller authorizes team/member filters and supplies normalized
// pagination; this method does not use the optional shared summary/trend
// reader.
func (r *TeamRepository) ListUsageLogs(ctx context.Context, teamID int64, query team.TeamUsageQuery) ([]team.TeamUsageLogItem, int64, error) {
	return usagequery.ListUsageLogs(ctx, r.db, teamID, query)
}

// ListTeamKeys delegates team credential metadata and optional creator
// filtering to the retained Key adapter. Caller authorization comes first;
// returned plaintext is consumed by TeamService masking and its result
// storage is not copied here.
func (r *TeamRepository) ListTeamKeys(ctx context.Context, teamID int64, actorUserID *int64) ([]team.TeamAPIKeyItem, error) {
	return r.keys.ListTeamKeys(ctx, teamID, actorUserID)
}

// DisableTeamKey delegates owner lock/disabled status mutation and returns
// the adapter credential/result. Callers establish authorization before
// optional positive creator filtering; TeamService requests invalidation only
// after successful returned storage outcome.
func (r *TeamRepository) DisableTeamKey(ctx context.Context, teamID, keyID int64, actorUserID *int64) (string, error) {
	return r.keys.DisableTeamKey(ctx, teamID, keyID, actorUserID)
}

// EnableTeamKey delegates clearing owner lock and activating the selected
// team credential. Caller authorization determines who may clear that lock;
// the returned full Key supplies later service invalidation after adapter
// success.
func (r *TeamRepository) EnableTeamKey(ctx context.Context, teamID, keyID int64, actorUserID *int64) (string, error) {
	return r.keys.EnableTeamKey(ctx, teamID, keyID, actorUserID)
}

// DeleteTeamKey delegates selected team credential deletion and returns its
// prior full Key after adapter success. The retained adapter owns its
// transaction, while caller authorization precedes selection and service
// invalidation follows successful return.
func (r *TeamRepository) DeleteTeamKey(ctx context.Context, teamID, keyID int64, actorUserID *int64) (string, error) {
	return r.keys.DeleteTeamKey(ctx, teamID, keyID, actorUserID)
}

// normalizeMemberQuotaWindows updates only the supplied view's usage amounts
// and calendar start pointers through billing normalization. Nil member is
// ignored, limits remain unchanged and current/future pointers remain shared;
// missing/older starts select fresh current starts without a database write.
func normalizeMemberQuotaWindows(member *team.TeamMembership, now time.Time, calendar timezone.Calendar) {
	if member == nil {
		return
	}
	projected := billing.NormalizeMemberQuotaWindows(billing.MemberQuotaSnapshot{DailyUsageUSD: member.DailyUsageUSD, WeeklyUsageUSD: member.WeeklyUsageUSD, MonthlyUsageUSD: member.MonthlyUsageUSD, DailyWindowStart: member.DailyWindowStart, WeeklyWindowStart: member.WeeklyWindowStart, MonthlyWindowStart: member.MonthlyWindowStart}, now, calendar)
	member.DailyUsageUSD = projected.DailyUsageUSD
	member.WeeklyUsageUSD = projected.WeeklyUsageUSD
	member.MonthlyUsageUSD = projected.MonthlyUsageUSD
	member.DailyWindowStart = projected.DailyWindowStart
	member.WeeklyWindowStart = projected.WeeklyWindowStart
	member.MonthlyWindowStart = projected.MonthlyWindowStart
}

// normalizeMemberWindows delegates view-only quota normalization using the
// repository calendar. Nil member remains a skip; the selected calendar owns
// timezone rules while the supplied instant determines which observed usage
// windows are stale.
func (r *TeamRepository) normalizeMemberWindows(member *team.TeamMembership, now time.Time) {
	normalizeMemberQuotaWindows(member, now, r.dateCalendar())
}

// dateCalendar returns the supplied calendar value or a new time.Local
// calendar when its pointer is absent. Location/clock references in a
// configured value remain shared; current member views independently pass
// time.Now rather than invoking that clock.
func (r *TeamRepository) dateCalendar() timezone.Calendar {
	// Use the configured calendar value when supplied, retaining its shared
	// location/clock references; otherwise derive local date rules from the
	// current process time.Local.
	if r.calendar != nil {
		return *r.calendar
	}
	return timezone.NewCalendar(time.Local)
}

// ScanTeamMembership decodes the repository's fixed membership-column order
// into a required nonnil record. Scan failure can leave partial fields and
// returns directly; no active-status validation or calendar normalization
// occurs inside this scanner helper.
func ScanTeamMembership(row TeamRowScanner, member *team.TeamMembership) error {
	return row.Scan(
		&member.ID, &member.TeamID, &member.UserID, &member.Email, &member.Username, &member.Role,
		&member.DailyLimitUSD, &member.WeeklyLimitUSD, &member.MonthlyLimitUSD,
		&member.DailyUsageUSD, &member.WeeklyUsageUSD, &member.MonthlyUsageUSD,
		&member.DailyWindowStart, &member.WeeklyWindowStart, &member.MonthlyWindowStart,
		&member.JoinedAt, &member.LastActiveAt,
	)
}

// RequireTeamAffected returns execution errors first, otherwise selects
// team-not-found for a zero companion affected count. A result is required on
// success; RowsAffected errors are discarded and a nonzero count reports
// success even if that count operation also errored.
func RequireTeamAffected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	// Discard affected-count errors but inspect their companion count; zero
	// selects team-not-found, leaving that reported missing-state outcome
	// separate from earlier statement execution failure.
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return team.ErrTeamNotFound
	}
	return nil
}

// MapTeamConstraintError maps any wrapped pq uniqueness code 23505 to
// already-joined, regardless of the violated constraint name. Other failures
// are wrapped as team-data constraints; callers pass a nonnil error, since
// nil is also wrapped into a nonnil formatting error.
func MapTeamConstraintError(err error) error {
	var pqErr *pq.Error
	// Map any wrapped PostgreSQL uniqueness failure to membership conflict for
	// callers, even when another team-related unique constraint caused it; other
	// errors retain wrapping diagnostics.
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return team.ErrTeamAlreadyJoined
	}
	return fmt.Errorf("team data constraint failed: %w", err)
}
