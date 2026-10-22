package team

import (
	"github.com/TokenFlux/TokenRouter/internal/billing"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// errors.go defines team rejection errors and shares membership and
// spending-limit identities with billing.
// Team rejection reasons distinguish process availability, acting membership,
// ownership and invitation/transfer state. Spending-limit and membership
// errors share billing's sentinel values, allowing gateway quota checks and
// team use cases to report the same domain identity.

// These application errors carry stable team reasons/statuses for service and
// HTTP callers, including shared billing identities for membership and
// exhausted member allowances.
var (
	// ErrTeamFeatureDisabled rejects team operations when the process team
	// switch is disabled.
	ErrTeamFeatureDisabled = infraerrors.Forbidden("TEAM_FEATURE_DISABLED", "The team feature is disabled")
	// ErrTeamSelfServiceDisabled rejects user creation while administrator team
	// creation remains separately authorized.
	ErrTeamSelfServiceDisabled = infraerrors.Forbidden("TEAM_SELF_SERVICE_DISABLED", "Team self-service creation is disabled")
	// ErrTeamNotFound reports a missing undeleted team or a zero-row team write.
	ErrTeamNotFound = infraerrors.NotFound("TEAM_NOT_FOUND", "Team not found")
	// ErrTeamMembershipRequired shares billing's sentinel for a missing required
	// team membership.
	ErrTeamMembershipRequired = billing.ErrTeamMembershipRequired
	// ErrTeamOwnerRequired rejects actions requiring the acting membership to
	// own the team.
	ErrTeamOwnerRequired = infraerrors.Forbidden("TEAM_OWNER_REQUIRED", "Only the team owner can perform this action")
	// ErrTeamAlreadyJoined reports existing current membership or a mapped team
	// uniqueness conflict.
	ErrTeamAlreadyJoined = infraerrors.Conflict("TEAM_ALREADY_JOINED", "User already belongs to a team")
	// ErrTeamMemberLimitReached rejects admission when ordinary membership count
	// reaches the team ceiling.
	ErrTeamMemberLimitReached = infraerrors.Conflict("TEAM_MEMBER_LIMIT_REACHED", "Team member limit reached")
	// ErrTeamInvitationInvalid rejects unavailable invitation records, states or
	// unsupported resolution text.
	ErrTeamInvitationInvalid = infraerrors.BadRequest("TEAM_INVITATION_INVALID", "Invalid team invitation")
	// ErrTeamInvitationExpired reports an invitation deadline at or before the
	// evaluation time.
	ErrTeamInvitationExpired = infraerrors.BadRequest("TEAM_INVITATION_EXPIRED", "Team invitation has expired")
	// ErrTeamInvitationEmail rejects confirmation by an account whose mailbox
	// differs from the invitation.
	ErrTeamInvitationEmail = infraerrors.Forbidden("TEAM_INVITATION_EMAIL_MISMATCH", "Account email does not match the invited email")
	// ErrTeamInvitationRateLimited reports recipient/team invitation throttling
	// with retry seconds supplied separately.
	ErrTeamInvitationRateLimited = infraerrors.TooManyRequests("TEAM_INVITATION_RATE_LIMITED", "Team invitations are being sent too frequently")
	// ErrTeamInvitationUnavailable rejects invitation admission when its rate
	// limiter is absent or fails.
	ErrTeamInvitationUnavailable = infraerrors.ServiceUnavailable("TEAM_INVITATION_UNAVAILABLE", "Team invitation service is temporarily unavailable")
	// ErrTeamFrontendURLUnavailable reports an unavailable base URL or blank
	// configured invitation review link.
	ErrTeamFrontendURLUnavailable = infraerrors.ServiceUnavailable("TEAM_FRONTEND_URL_UNAVAILABLE", "Frontend URL is not configured; cannot generate team email links")
	// ErrTeamOwnerCannotLeave requires ownership transfer or team dissolution
	// before an owner leaves.
	ErrTeamOwnerCannotLeave = infraerrors.Conflict("TEAM_OWNER_CANNOT_LEAVE", "Team owners must transfer ownership or dissolve the team before leaving")
	// ErrTeamOwnerTransferRequired reports the owner deletion restriction
	// enforced by account persistence.
	ErrTeamOwnerTransferRequired = infraerrors.Conflict("TEAM_OWNER_TRANSFER_REQUIRED", "Transfer ownership or dissolve the team before deleting its owner")
	// ErrTeamTransferInvalid rejects invalid ownership participants, state or
	// unsupported resolution text.
	ErrTeamTransferInvalid = infraerrors.BadRequest("TEAM_TRANSFER_INVALID", "Invalid ownership transfer")
	// ErrTeamTransferExpired reports an ownership transfer deadline at or before
	// evaluation time.
	ErrTeamTransferExpired = infraerrors.BadRequest("TEAM_TRANSFER_EXPIRED", "Ownership transfer has expired")
	// ErrTeamSuspended rejects operations that require an active team membership
	// context.
	ErrTeamSuspended = infraerrors.Forbidden("TEAM_SUSPENDED", "The team is suspended")
	// ErrTeamMemberDailyExceeded shares billing's sentinel for an exhausted
	// positive daily member allowance.
	ErrTeamMemberDailyExceeded = billing.ErrTeamMemberDailyExceeded
	// ErrTeamMemberWeeklyExceeded shares billing's sentinel for an exhausted
	// positive weekly member allowance.
	ErrTeamMemberWeeklyExceeded = billing.ErrTeamMemberWeeklyExceeded
	// ErrTeamMemberMonthlyExceeded shares billing's sentinel for an exhausted
	// positive monthly member allowance.
	ErrTeamMemberMonthlyExceeded = billing.ErrTeamMemberMonthlyExceeded
)
