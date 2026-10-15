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
	ErrTeamSelfServiceDisabled = infraerrors.Forbidden("TEAM_SELF_SERVICE_DISABLED", "暂不允许用户自助创建团队")
	// ErrTeamNotFound reports a missing undeleted team or a zero-row team write.
	ErrTeamNotFound = infraerrors.NotFound("TEAM_NOT_FOUND", "团队不存在")
	// ErrTeamMembershipRequired shares billing's sentinel for a missing required
	// team membership.
	ErrTeamMembershipRequired = billing.ErrTeamMembershipRequired
	// ErrTeamOwnerRequired rejects actions requiring the acting membership to
	// own the team.
	ErrTeamOwnerRequired = infraerrors.Forbidden("TEAM_OWNER_REQUIRED", "仅团队所有者可执行此操作")
	// ErrTeamAlreadyJoined reports existing current membership or a mapped team
	// uniqueness conflict.
	ErrTeamAlreadyJoined = infraerrors.Conflict("TEAM_ALREADY_JOINED", "用户已经属于一个团队")
	// ErrTeamMemberLimitReached rejects admission when ordinary membership count
	// reaches the team ceiling.
	ErrTeamMemberLimitReached = infraerrors.Conflict("TEAM_MEMBER_LIMIT_REACHED", "团队成员数量已达上限")
	// ErrTeamInvitationInvalid rejects unavailable invitation records, states or
	// unsupported resolution text.
	ErrTeamInvitationInvalid = infraerrors.BadRequest("TEAM_INVITATION_INVALID", "团队邀请无效")
	// ErrTeamInvitationExpired reports an invitation deadline at or before the
	// evaluation time.
	ErrTeamInvitationExpired = infraerrors.BadRequest("TEAM_INVITATION_EXPIRED", "团队邀请已过期")
	// ErrTeamInvitationEmail rejects confirmation by an account whose mailbox
	// differs from the invitation.
	ErrTeamInvitationEmail = infraerrors.Forbidden("TEAM_INVITATION_EMAIL_MISMATCH", "当前账号邮箱与受邀邮箱不一致")
	// ErrTeamInvitationRateLimited reports recipient/team invitation throttling
	// with retry seconds supplied separately.
	ErrTeamInvitationRateLimited = infraerrors.TooManyRequests("TEAM_INVITATION_RATE_LIMITED", "团队邀请发送过于频繁")
	// ErrTeamInvitationUnavailable rejects invitation admission when its rate
	// limiter is absent or fails.
	ErrTeamInvitationUnavailable = infraerrors.ServiceUnavailable("TEAM_INVITATION_UNAVAILABLE", "团队邀请服务暂时不可用")
	// ErrTeamFrontendURLUnavailable reports an unavailable base URL or blank
	// configured invitation review link.
	ErrTeamFrontendURLUnavailable = infraerrors.ServiceUnavailable("TEAM_FRONTEND_URL_UNAVAILABLE", "未配置前端地址，无法生成团队邮件链接")
	// ErrTeamOwnerCannotLeave requires ownership transfer or team dissolution
	// before an owner leaves.
	ErrTeamOwnerCannotLeave = infraerrors.Conflict("TEAM_OWNER_CANNOT_LEAVE", "团队所有者必须先转让所有权或解散团队")
	// ErrTeamOwnerTransferRequired reports the owner deletion restriction
	// enforced by account persistence.
	ErrTeamOwnerTransferRequired = infraerrors.Conflict("TEAM_OWNER_TRANSFER_REQUIRED", "删除团队所有者前必须先转让所有权或解散团队")
	// ErrTeamTransferInvalid rejects invalid ownership participants, state or
	// unsupported resolution text.
	ErrTeamTransferInvalid = infraerrors.BadRequest("TEAM_TRANSFER_INVALID", "所有权转让无效")
	// ErrTeamTransferExpired reports an ownership transfer deadline at or before
	// evaluation time.
	ErrTeamTransferExpired = infraerrors.BadRequest("TEAM_TRANSFER_EXPIRED", "所有权转让已过期")
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
