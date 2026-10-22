package team

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

// team.go coordinates team permissions, membership management, invitations,
// and ownership transfers.
// Team management distinguishes the acting membership from its owner and
// payer, so ordinary members cannot expand report scope or manage another
// creator's credentials. Owner actions can require active teams or
// deliberately allow suspended context for resume, departure and teardown.
//
// Invitation admission is recorded before token/link/storage preparation, and
// mail delivery follows saved review state. URL or mail errors retain the work
// completed before their respective stages.
// Lifecycle repositories own their related transactions and can return
// post-commit readback errors; optional authentication invalidation is
// requested only after successful selected results and Key-list failure is
// suppressed. Member ceilings count ordinary memberships, while USD limits
// apply calendar spending periods with zero unlimited.

// These stored team states and membership roles select active-team admission,
// suspended-team management and owner versus ordinary-member authorization
// throughout the service and repositories.
const (
	// TeamStatusActive permits actions requiring an active team context.
	TeamStatusActive = "active"
	// TeamStatusSuspended blocks active-team actions while selected
	// owner/departure operations remain available.
	TeamStatusSuspended = "suspended"
	// TeamRoleOwner identifies the team manager and payer in a current
	// membership.
	TeamRoleOwner = "owner"
	// TeamRoleMember identifies ordinary acting membership subject to member
	// spending limits.
	TeamRoleMember = "member"
)

// Team carries tenant metadata, ordinary-member admission ceiling and
// defaults for later membership joins.
type Team struct {
	ID     int64  `json:"id"`     // stored tenant identity value
	Name   string `json:"name"`   // validated tenant display name
	Status string `json:"status"` // active or suspended state
	// MemberLimit caps current ordinary memberships, excluding the owner.
	// Invitation acceptance rejects count at or above this value, so zero admits
	// no ordinary members.
	MemberLimit int `json:"member_limit"`
	// DefaultDailyLimitUSD, DefaultWeeklyLimitUSD and DefaultMonthlyLimitUSD
	// become the matching member limits when an invitation is accepted. Zero
	// leaves that billing period unlimited; updating defaults does not rewrite
	// existing memberships.
	DefaultDailyLimitUSD   float64 `json:"default_daily_limit_usd"`   // USD daily join default
	DefaultWeeklyLimitUSD  float64 `json:"default_weekly_limit_usd"`  // USD weekly join default
	DefaultMonthlyLimitUSD float64 `json:"default_monthly_limit_usd"` // USD monthly join default
	// MemberCount counts current ordinary memberships in PostgreSQL context/list
	// reads. It excludes the owner and follows left_at presence rather than
	// account active status.
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"` // stored tenant creation time
	UpdatedAt   time.Time `json:"updated_at"` // stored tenant update time
	// DeletedAt is the optional soft-deletion timestamp. Ordinary repository
	// reads select undeleted teams and JSON serialization omits this storage
	// lifecycle field.
	DeletedAt *time.Time `json:"-"`
}

// TeamMembership carries one acting account's team role, calendar spending
// limits and observed usage.
type TeamMembership struct {
	ID       int64  `json:"id"`       // stored membership identity value
	TeamID   int64  `json:"team_id"`  // associated tenant identity value
	UserID   int64  `json:"user_id"`  // associated acting account identity
	Email    string `json:"email"`    // current member account mailbox
	Username string `json:"username"` // current member account name
	Role     string `json:"role"`     // owner or ordinary member
	// DailyLimitUSD, WeeklyLimitUSD and MonthlyLimitUSD cap DailyUsageUSD,
	// WeeklyUsageUSD and MonthlyUsageUSD respectively for ordinary-member
	// admission. Positive limits reject usage at or above the ceiling;
	// nonpositive values leave the period unlimited, and owner admission
	// bypasses this member-quota check.
	DailyLimitUSD   float64 `json:"daily_limit_usd"`   // USD daily member ceiling
	WeeklyLimitUSD  float64 `json:"weekly_limit_usd"`  // USD weekly member ceiling
	MonthlyLimitUSD float64 `json:"monthly_limit_usd"` // USD monthly member ceiling
	// DailyUsageUSD, WeeklyUsageUSD and MonthlyUsageUSD belong to
	// DailyWindowStart, WeeklyWindowStart and MonthlyWindowStart respectively.
	// Repository views normalize missing/older starts in the configured calendar
	// and clear only corresponding returned usage; that read normalization does
	// not itself persist a reset. Current or future start pointers remain
	// retained through normalization, so consumers copy a retained time before
	// independent mutation.
	DailyUsageUSD      float64    `json:"daily_usage_usd"`      // USD observed daily spending
	WeeklyUsageUSD     float64    `json:"weekly_usage_usd"`     // USD observed weekly spending
	MonthlyUsageUSD    float64    `json:"monthly_usage_usd"`    // USD observed monthly spending
	DailyWindowStart   *time.Time `json:"daily_window_start"`   // daily calendar origin pointer
	WeeklyWindowStart  *time.Time `json:"weekly_window_start"`  // weekly calendar origin pointer
	MonthlyWindowStart *time.Time `json:"monthly_window_start"` // monthly calendar origin pointer
	JoinedAt           time.Time  `json:"joined_at"`            // current membership join time
	// LastActiveAt retains the account's optional last-active timestamp.
	// PostgreSQL membership views read users.last_active_at rather than tracking
	// a separate team-local activity time.
	LastActiveAt *time.Time `json:"last_active_at"`
}

// TeamContext retains tenant, acting membership and owner observations used
// by team permissions and views.
type TeamContext struct {
	// Team, Membership and Owner are retained pointers supplied by the
	// repository. Service membership admission requires Team and Membership,
	// while owner role is checked on Membership; Owner supplies the separate
	// team-owner view and payer identity.
	Team       *Team           `json:"team"`       // retained selected tenant metadata
	Membership *TeamMembership `json:"membership"` // retained acting membership observation
	Owner      *TeamMembership `json:"owner"`      // retained payer owner observation
}

// TeamInvitation carries recipient-bound review status and deadlines while
// the stored token digest stays private.
type TeamInvitation struct {
	ID            int64 `json:"id"`              // stored invitation identity value
	TeamID        int64 `json:"team_id"`         // invited tenant identity value
	InviterUserID int64 `json:"inviter_user_id"` // originating owner account identity
	// Email is the normalized invitation mailbox stored by the service.
	// Preview/resolution compare the normalized current account address
	// case-insensitively to this recipient identity.
	Email  string `json:"email"`
	Status string `json:"status"` // pending accepted declined revoked expired
	// ExpiresAt is the service-supplied invitation deadline. Preview/resolution
	// require it to be after evaluation time, so equality already counts as
	// expired.
	ExpiresAt time.Time `json:"expires_at"`
	// AcceptedAt retains the optional successful acceptance timestamp. Reissue
	// clears acceptance fields while replacing pending token state; other
	// unresolved invitations retain nil.
	AcceptedAt *time.Time `json:"accepted_at"`
	CreatedAt  time.Time  `json:"created_at"` // stored invitation creation time
}

// TeamInvitationPreview carries team/inviter display fields for a
// mailbox-matched pending review.
type TeamInvitationPreview struct {
	TeamName string `json:"team_name"` // current invited tenant name
	// InviterName selects trimmed nonblank username in PostgreSQL preview,
	// otherwise the inviter mailbox. It is display text rather than proof of
	// current ownership.
	InviterName  string    `json:"inviter_name"`
	InviterEmail string    `json:"inviter_email"` // current inviter account mailbox
	ExpiresAt    time.Time `json:"expires_at"`    // invitation review expiry instant
}

// TeamInvitationLimiter records shared invitation admission and reports
// denial delay or unavailable checks.
type TeamInvitationLimiter interface {
	// CheckAndRecord records permitted invitation admission and returns a retry
	// duration when denied. The service requires a usable limiter and rejects
	// errors; accepted checks precede token/link/storage work and are not
	// refunded by later failure.
	CheckAndRecord(ctx context.Context, teamID int64, email string) (allowed bool, retryAfter time.Duration, err error)
}

// TeamOwnershipTransfer carries source/target ownership participants and the
// review's deadline and resolution state.
type TeamOwnershipTransfer struct {
	ID         int64  `json:"id"`           // stored transfer identity value
	TeamID     int64  `json:"team_id"`      // associated tenant identity value
	FromUserID int64  `json:"from_user_id"` // originating owner account identity
	ToUserID   int64  `json:"to_user_id"`   // intended successor account identity
	Status     string `json:"status"`       // pending accepted declined cancelled expired
	// ExpiresAt is the transfer review deadline supplied by the service.
	// Resolution requires it to be after evaluation time; equality returns
	// transfer-expired.
	ExpiresAt time.Time `json:"expires_at"`
	// ResolvedAt retains an optional acceptance, decline, cancellation or expiry
	// timestamp. Pending transfers normally have no resolved observation until a
	// repository state transition records it.
	ResolvedAt *time.Time `json:"resolved_at"`
	CreatedAt  time.Time  `json:"created_at"` // stored transfer creation time
}

// TeamAdminListItem combines team metadata with owner identity and mailbox
// for administrator listings.
type TeamAdminListItem struct {
	Team               // embedded stored tenant metadata
	OwnerUserID int64  `json:"owner_user_id"` // current owner account identity
	OwnerEmail  string `json:"owner_email"`   // current owner account mailbox
}

// TeamAdminUpdate retains optional administrator replacements for team name,
// status and member ceiling.
type TeamAdminUpdate struct {
	// Name supplies a replacement when nonnil. AdminUpdate validates and trims
	// it into a fresh local string; nil leaves the stored name unchanged.
	Name *string
	// Status supplies a replacement when nonnil. Exact active/suspended text is
	// admitted, and selecting it triggers service invalidation after repository
	// success even for an equal value.
	Status *string
	// MemberLimit supplies a replacement when nonnil. Negatives reject, zero
	// prevents ordinary-member admission and nil leaves the stored ceiling
	// unchanged.
	MemberLimit *int
}

// TeamUsageQuery carries a report range, retained identity filters and
// requested pagination values.
type TeamUsageQuery struct {
	// From and To select an inclusive-start/exclusive-end range for team
	// reports. NormalizeTeamUsageQueryAt fills zero dates before swapping
	// reversed endpoints; HTTP date-only end inputs advance one
	// injected-calendar day before that normalization.
	From time.Time // inclusive record start instant
	To   time.Time // exclusive record end instant
	// ActorUserID is a retained optional acting-member filter. PostgreSQL query
	// builders use it only when positive; ordinary-member service calls replace
	// it with their own user ID.
	ActorUserID *int64
	// APIKeyID is a retained optional credential-record filter. PostgreSQL query
	// builders apply it when positive; range normalization leaves the pointed
	// input unchanged.
	APIKeyID *int64
	// Limit selects page size after normalization. Values outside one through
	// one hundred select twenty before logs are read or their paging metadata is
	// returned.
	Limit int
	// Offset selects skipped matching records after normalization. Negative
	// values select zero, and positive offsets have no upper cap in this
	// service.
	Offset int
}

// TeamUsageDaily carries cost and request totals grouped by one reporting
// calendar date.
type TeamUsageDaily struct {
	Date         string  `json:"date"`          // YYYY-MM-DD
	ActualCost   float64 `json:"actual_cost"`   // USD
	RequestCount int64   `json:"request_count"` // observed scoped request count
}

// TeamUsageSummary aggregates scoped team cost, request/token counts and
// retained daily rows.
type TeamUsageSummary struct {
	ActualCost   float64 `json:"actual_cost"`   // USD
	RequestCount int64   `json:"request_count"` // observed scoped request count
	InputTokens  int64   `json:"input_tokens"`  // observed input token count
	OutputTokens int64   `json:"output_tokens"` // observed output token count
	// Daily retains the report reader's calendar-date rows and backing storage.
	// Service wrappers return them without copying, so callers coordinate any
	// independent mutation.
	Daily []TeamUsageDaily `json:"daily"`
}

// TeamMemberUsageSeries identifies a represented current or departed actor
// and its scoped usage totals.
type TeamMemberUsageSeries struct {
	ActorUserID int64 `json:"actor_user_id"` // represented acting account identity
	// DisplayName is selected by the report reader from username, email or User
	// #ID. It describes the actor and does not establish membership or account
	// activation.
	DisplayName string `json:"display_name"`
	// Status records current membership presence as active or left in PostgreSQL
	// report output. It is separate from the user's account status and team
	// activation.
	Status string `json:"status"`
	// Summary carries that actor's selected cost/counts and retained Daily
	// slice. Current zero-usage members and departed members with scoped history
	// follow the report reader's actor selection.
	Summary TeamUsageSummary `json:"summary"`
}

// TeamUsageLogItem carries one scoped record's actor, credential identity,
// correlation and charged usage.
type TeamUsageLogItem struct {
	ID          int64  `json:"id"`            // stored usage record identity
	ActorUserID int64  `json:"actor_user_id"` // recorded request actor identity
	ActorEmail  string `json:"actor_email"`   // current actor account mailbox
	APIKeyID    int64  `json:"api_key_id"`    // recorded credential record identity
	APIKeyName  string `json:"api_key_name"`  // current credential display name
	RequestID   string `json:"request_id"`    // platform request correlation identifier
	// Model selects nonempty requested_model before the recorded actual model in
	// raw PostgreSQL usage logs. It describes display selection rather than an
	// execution routing decision.
	Model        string    `json:"model"`
	ActualCost   float64   `json:"actual_cost"`   // USD
	InputTokens  int       `json:"input_tokens"`  // recorded input token count
	OutputTokens int       `json:"output_tokens"` // recorded output token count
	CreatedAt    time.Time `json:"created_at"`    // recorded request creation time
}

// TeamUsagePage retains scoped usage records and their total with normalized
// paging inputs.
type TeamUsagePage struct {
	// Items retains the repository's scoped record slice and backing storage.
	// Service paging wraps it directly, so callers coordinate mutations of
	// returned records.
	Items  []TeamUsageLogItem `json:"items"`
	Total  int64              `json:"total"`  // total matching record count
	Limit  int                `json:"limit"`  // normalized requested page size
	Offset int                `json:"offset"` // normalized skipped record count
}

// TeamAPIKeyItem carries creator/credential display metadata for team
// management and credential masking.
type TeamAPIKeyItem struct {
	ID        int64  `json:"id"`         // stored credential record identity
	UserID    int64  `json:"user_id"`    // credential creator account identity
	UserEmail string `json:"user_email"` // current creator account mailbox
	Name      string `json:"name"`       // stored credential display name
	// MaskedKey is populated from Key by ListTeamKeys before plaintext is
	// cleared. Repository items can share backing storage with other callers, so
	// this display conversion mutates the returned items.
	MaskedKey string `json:"masked_key"`
	Status    string `json:"status"` // stored credential admission status
	// OwnerDisabled is the owner's separate persisted lock. Owner disablement
	// sets it with disabled status, and owner reactivation clears it with active
	// status.
	OwnerDisabled bool `json:"team_owner_disabled"`
	// GroupID retains the optional primary group identity from stored Key
	// metadata. This listing does not hydrate full composite group bindings; nil
	// represents no selected primary group.
	GroupID *int64 `json:"group_id"`
	// GroupName is the primary group display name selected under request locale
	// by PostgreSQL listing. Missing related groups produce empty display text
	// through its left join.
	GroupName string `json:"group_name"`
	// LastUsedAt retains the Key's optional last-use timestamp from storage. Nil
	// represents absent observation; callers copy a retained time before
	// independently mutating it.
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"` // stored credential creation time
	// Key holds the full stored credential for service masking or invalidation
	// inputs. JSON omits it, and ListTeamKeys also clears it after deriving
	// MaskedKey.
	Key string `json:"-"`
}

// TeamRepository stores team lifecycle state and reports for scopes
// authorized by the service. Persistent membership/ownership operations
// coordinate related changes in their own transaction; callers handle errors
// that can follow committed write readback.
type TeamRepository interface {
	// Create stores a team and current owner membership and returns its context.
	// The caller supplies normalized name and member ceiling after
	// feature/permission admission.
	//
	// Persistent implementations coordinate creation with current-membership
	// uniqueness before publishing both records together. PostgreSQL serializes
	// the owner identity and commits before loading context, so a readback error
	// can follow a created team. A successful result supplies team/acting/owner
	// observations for subsequent service and browser use.
	Create(ctx context.Context, name string, ownerUserID int64, memberLimit int) (*TeamContext, error)
	// GetContextByUserID reads current team and membership for the selected
	// account. The service applies active/owner requirements separately;
	// repository errors and incomplete context results remain subject to those
	// service checks.
	GetContextByUserID(ctx context.Context, userID int64) (*TeamContext, error)
	// GetContextByTeamID reads an undeleted team and its owner context for
	// administrator or write readback. Authorization belongs to the caller, and
	// a read failure can follow its preceding successful mutation.
	GetContextByTeamID(ctx context.Context, teamID int64) (*TeamContext, error)
	// UpdateName saves a normalized name for an undeleted team. The caller
	// establishes owner/administrator permission first; storage reports missing
	// rows or write failure before the service requests context readback.
	UpdateName(ctx context.Context, teamID int64, name string) error
	// SetDefaultMemberLimits replaces the team's complete future-join USD
	// triple. The caller rejects negative inputs and authorizes ownership;
	// existing memberships keep their own limits and storage returns
	// missing/write outcomes.
	SetDefaultMemberLimits(ctx context.Context, teamID int64, daily, weekly, monthly float64) error
	// ListMembers lists current memberships for a caller-authorized team. The
	// service applies owner/self display filtering afterward; returned records
	// can retain shared time pointers and the implementation owns window-view
	// normalization.
	ListMembers(ctx context.Context, teamID int64) ([]TeamMembership, error)
	// CreateInvitation stores a recipient-bound pending token digest and returns
	// invitation metadata. Callers finish rate admission and required link
	// preparation before persistence, then handle mail separately.
	//
	// The persistent operation revokes older pending rows for the same
	// team/mailbox and inserts the replacement in one transaction, keeping its
	// pending uniqueness consistent. The supplied expiry dates that digest;
	// plaintext stays with the caller's review link. Successful return precedes
	// delivery, whose later failure leaves this saved invitation state
	// available.
	CreateInvitation(ctx context.Context, teamID, inviterUserID int64, email, tokenHash string, expiresAt time.Time) (*TeamInvitation, error)
	// ListInvitations lists team review metadata for an authorized owner.
	// Persistent readers can attempt expiry marking before reading all statuses;
	// callers handle read errors and do not infer marker success from a returned
	// list.
	ListInvitations(ctx context.Context, teamID int64) ([]TeamInvitation, error)
	// GetInvitationByID selects an invitation within the caller's authorized
	// team. It returns stored recipient/state metadata for reissue preparation;
	// missing identity or unreadable rows remain errors before rate admission
	// continues.
	GetInvitationByID(ctx context.Context, teamID, invitationID int64) (*TeamInvitation, error)
	// ReissueInvitation replaces a selected pending or expired invitation's
	// digest/deadline and returns metadata. The caller supplies admitted resend
	// state and delivers mail after persistence.
	//
	// Persistent implementations lock the selected review, revoke competing
	// pending rows for its team/mailbox, clear acceptance data and publish
	// pending replacement state together. This keeps the pending uniqueness and
	// replacement digest aligned. Later delivery failure does not restore the
	// prior digest; callers handle that saved replacement even when their
	// service operation returns a mail error.
	ReissueInvitation(ctx context.Context, teamID, invitationID int64, tokenHash string, expiresAt time.Time) (*TeamInvitation, error)
	// RevokeInvitation changes a matching pending invitation to revoked within
	// the authorized team. The caller owns permission; missing/nonpending
	// matches return the invitation-invalid outcome instead of a successful
	// revocation.
	RevokeInvitation(ctx context.Context, teamID, invitationID int64) error
	// PreviewInvitation checks digest, pending status, supplied normalized
	// mailbox and evaluation time before returning display fields. It grants no
	// membership or team-management permission.
	//
	// The PostgreSQL reader joins undeleted team and inviter records but does
	// not require active team status or establish that the inviter still owns
	// it. Expired reviews attempt a best-effort marker and return expired
	// regardless of that write outcome. Confirmation later reloads review/team
	// state under the resolution transaction before membership can be admitted.
	PreviewInvitation(ctx context.Context, tokenHash, normalizedEmail string, now time.Time) (*TeamInvitationPreview, error)
	// ResolveInvitation confirms a recipient-bound review and either declines it
	// or admits ordinary membership. The caller supplies exact resolution,
	// normalized current mailbox and one evaluation instant.
	//
	// Persistent acceptance coordinates user/team membership uniqueness, team
	// status/capacity, inherited limits, historical member-Key disablement and
	// invitation state in its transaction. Decline can return nil context
	// successfully; accepted context is read after commit in PostgreSQL, so
	// readback failure does not prove rollback. Expired review marking there is
	// attempted best effort and its failure can remain hidden behind the expired
	// outcome.
	ResolveInvitation(ctx context.Context, tokenHash string, userID int64, normalizedEmail, resolution string, now time.Time) (*TeamContext, error)
	// RemoveMember ends a current ordinary membership and disables its team
	// credentials with the supplied lifecycle time. The caller authorizes the
	// owner or acting-member departure before delegation.
	//
	// Persistent implementations keep departure and credential disablement in
	// one transaction so a failed Key participant prevents a completed
	// membership removal. Owners do not match this ordinary-member write. The
	// service requests authentication invalidation only after successful return;
	// the participant leaves transaction completion to the repository rather
	// than independently committing its Key changes.
	RemoveMember(ctx context.Context, teamID, userID int64, now time.Time) error
	// UpdateMemberLimits replaces the current ordinary member's USD triple for
	// an authorized owner. Caller validation rejects negatives; zero leaves
	// periods unlimited, and missing membership/write failures stop subsequent
	// service invalidation.
	UpdateMemberLimits(ctx context.Context, teamID, userID int64, daily, weekly, monthly float64) error
	// ResetMemberUsage resets selected member calendar totals using the supplied
	// time. The caller selects at least one period and authorizes ownership; the
	// billing participant determines current-membership lookup and persistence
	// before service invalidation.
	ResetMemberUsage(ctx context.Context, teamID, userID int64, resetDaily, resetWeekly, resetMonthly bool, now time.Time) error
	// CreateOwnershipTransfer stores a target-member review with digest and
	// expiry after caller ownership admission. The target must be a current
	// ordinary member, while the caller keeps plaintext for optional mail.
	//
	// The persistent operation attempts cancellation of older pending reviews
	// before inserting the new one in its transaction, subject to pending-review
	// uniqueness. PostgreSQL discards the cancellation error but later
	// insert/commit can still fail if that transaction was aborted. Successful
	// persistence precedes optional mail delivery, whose error cannot restore
	// the displaced review state.
	CreateOwnershipTransfer(ctx context.Context, teamID, fromUserID, toUserID int64, tokenHash string, expiresAt time.Time) (*TeamOwnershipTransfer, error)
	// ResolveOwnershipTransfer confirms the intended target, review state and
	// deadline, then applies accepted or declined resolution. The caller
	// supplies exact resolution and evaluation time after the feature gate.
	//
	// Persistent acceptance demotes the current owner before promoting the
	// ordinary target in one transaction, preserving the partial unique-owner
	// index and zeroing both member-limit triples. Usage/window histories stay
	// with their memberships. PostgreSQL returns a context read after commit and
	// does not retry serialization failures; callers account for readback errors
	// that can follow a saved decision.
	ResolveOwnershipTransfer(ctx context.Context, tokenHash string, actorUserID int64, resolution string, now time.Time) (*TeamContext, error)
	// CancelOwnershipTransfer cancels pending reviews matching the team and
	// originating actor. The caller establishes current ownership; storage dates
	// resolution with now and rejects when no pending matching review can be
	// changed.
	CancelOwnershipTransfer(ctx context.Context, teamID, actorUserID int64, now time.Time) error
	// Dissolve ends an authorized team using one lifecycle instant. The caller
	// establishes owner or administrator permission and requests credential
	// invalidation after repository success.
	//
	// Persistent implementations suspend/soft-delete the tenant, end current
	// memberships, revoke pending invitations, cancel pending transfers and
	// disable team Keys together in their transaction. A participant failure
	// prevents successful teardown. The Key participant uses the supplied
	// transaction and leaves completion to the repository, keeping lifecycle
	// records and disabled credentials coordinated before service publication.
	Dissolve(ctx context.Context, teamID int64, now time.Time) error
	// SetStatus replaces an undeleted team's status after caller
	// validation/authorization. The service admits exact active/suspended values
	// and requests invalidation afterward; storage returns missing/write errors
	// before that publication attempt.
	SetStatus(ctx context.Context, teamID int64, status string) error
	// SetMemberLimit replaces an undeleted team's ordinary-member ceiling after
	// caller validation. Zero admits no ordinary members and negative values
	// violate storage constraints; authorization is established outside this
	// storage interface.
	SetMemberLimit(ctx context.Context, teamID int64, limit int) error
	// UpdateAdmin writes selected name/status/member-ceiling fields in one
	// administrator operation. Nil pointers omit columns; the service completes
	// validation before this method and reads back context after storage
	// success.
	UpdateAdmin(ctx context.Context, teamID int64, update TeamAdminUpdate) error
	// ForceTransfer swaps the current owner to an administrator-selected
	// ordinary member using the supplied time. Callers authorize platform
	// administration before bypassing current-member service checks.
	//
	// Persistent implementations validate an undeleted target and coordinate
	// demote-before-promote updates, with both memberships' limits zeroed and
	// their usage/windows retained. PostgreSQL uses a serializable transaction
	// and then reads team context after commit, without retrying serialization
	// failure. A later read error can prevent service invalidation even though
	// the ownership swap is saved.
	ForceTransfer(ctx context.Context, teamID, toUserID int64, now time.Time) (*TeamContext, error)
	// ListAdmin returns team and current-owner metadata for administrator
	// enumeration. The caller establishes administrator authorization; the
	// repository selects stored statuses and its undeleted-team filter before
	// returning list/read failures.
	ListAdmin(ctx context.Context) ([]TeamAdminListItem, error)
	// ListTeamKeyStrings returns stored credentials used by optional
	// authentication invalidation. Caller authorization precedes mutation; the
	// service suppresses listing errors and receives no delivery acknowledgement
	// from the later cache capability.
	ListTeamKeyStrings(ctx context.Context, teamID int64) ([]string, error)
	// GetUsageSummary reads totals for the authorized team/range and selected
	// filters. The service normalizes dates and ordinary-member scope first;
	// report adapters choose raw/preaggregated sources under their own query
	// rules.
	GetUsageSummary(ctx context.Context, teamID int64, query TeamUsageQuery) (*TeamUsageSummary, error)
	// ListMemberUsageSeries reads scoped actor trends, including current
	// zero-usage members and departed actors with represented history. The
	// caller establishes scope; returned Summary.Daily storage is retained for
	// its consumers.
	ListMemberUsageSeries(ctx context.Context, teamID int64, query TeamUsageQuery) ([]TeamMemberUsageSeries, error)
	// ListUsageLogs returns raw scoped records and a matching total for
	// normalized pagination. The caller authorizes team/member filters first;
	// the reader uses inclusive From and exclusive To for record timestamps.
	ListUsageLogs(ctx context.Context, teamID int64, query TeamUsageQuery) ([]TeamUsageLogItem, int64, error)
	// ListTeamKeys returns nondeleted credential metadata for an authorized team
	// and optional positive creator filter. It includes plaintext Key for
	// service masking, and returned storage can be modified in place by that
	// service.
	ListTeamKeys(ctx context.Context, teamID int64, actorUserID *int64) ([]TeamAPIKeyItem, error)
	// DisableTeamKey sets disabled status with the owner lock and returns its
	// credential after a matching write. A positive optional creator filter
	// narrows selection; callers authorize ownership and request invalidation
	// after success.
	DisableTeamKey(ctx context.Context, teamID, keyID int64, actorUserID *int64) (string, error)
	// EnableTeamKey sets active status and clears the owner lock, returning its
	// credential after a matching write. Caller authorization determines who can
	// clear that lock; successful service callers request authentication
	// invalidation.
	EnableTeamKey(ctx context.Context, teamID, keyID int64, actorUserID *int64) (string, error)
	// DeleteTeamKey deletes a selected credential and returns its prior
	// plaintext for invalidation. Caller authorization establishes team and
	// optional creator eligibility before storage selection.
	//
	// Persistent implementations lock the matching row before replacing its
	// credential with a tombstone and recording soft deletion in the same
	// transaction. They commit before returning the prior credential, keeping
	// the invalidation target tied to the selected deleted record. Selection,
	// write or commit errors return before successful completion, with rollback
	// attempted by storage cleanup.
	DeleteTeamKey(ctx context.Context, teamID, keyID int64, actorUserID *int64) (string, error)
}

// TeamService applies acting-role rules before token, repository, mail and
// optional authentication-invalidation operations.
type TeamService struct {
	// repo is retained for lifecycle state and reports. User operations apply
	// service role checks, while administrator callers supply route
	// authorization; persistent implementations own transactions/readback.
	repo TeamRepository
	// userRepo supplies current mailbox/target hints during review operations.
	// Successful reads used by preview, invitation resolution or transfer
	// preparation require a nonnil account snapshot.
	userRepo UserReader
	// emailService optionally receives saved invitation/transfer review
	// messages. Nil skips delivery and review-link preparation; later mail
	// errors can follow a successful repository write.
	emailService Notifier
	// apiKeyCache optionally receives authentication invalidation after
	// successful selected mutations. Its methods supply no acknowledgement;
	// missing cache or failed Key listing can skip publication.
	apiKeyCache KeyInvalidator
	// inviteLimiter records invitation/resend admission before
	// token/link/storage preparation. Nil or error rejects admission, and
	// downstream failure does not refund a recorded permit.
	inviteLimiter TeamInvitationLimiter
	// settingService supplies frontend base text when present. Its empty result
	// causes link-unavailable even when options contain a URL; options apply
	// only when this reader is absent.
	settingService Settings
	// options retains process feature/self-service switches, creation default,
	// link base and clock by pointer. Callers configure it before concurrent
	// service use and keep it stable.
	options *Options
}

// Options supplies retained process team switches, creation defaults, link
// base and service clock.
type Options struct {
	// Now supplies service instants when nonnil and otherwise uses time.Now.
	// Callers make injected clocks safe for concurrent use; their returned
	// location passes through unchanged.
	Now func() time.Time

	// Enabled and SelfServiceEnabled are retained process switches. Feature
	// checks use Enabled, while Create also requires SelfServiceEnabled;
	// AdminCreate bypasses only self-service, and nil Options permit both
	// checks.
	// Enabled: process team feature switch. SelfServiceEnabled: user team
	// creation switch.
	Enabled, SelfServiceEnabled bool
	// DefaultMemberLimit supplies the creation ceiling when nonnegative.
	// Negative values use the built-in ten, while zero admits no ordinary
	// memberships through invitation acceptance.
	DefaultMemberLimit int
	// FrontendURL supplies review-link base text when no settings reader is
	// present. FrontendLink trims surrounding whitespace/trailing slashes and
	// rejects an empty selected base.
	FrontendURL string
}

// UserSnapshot carries account identity and mailbox/name hints used by
// invitation and transfer preparation.
type UserSnapshot struct {
	ID int64 // looked-up account identity value
	// Email and Username supply separate account hints from UserReader. Review
	// confirmation normalizes Email for recipient identity; invitation greetings
	// prefer nonblank trimmed Username while retaining sender-derived text when
	// unavailable.
	// Email: current account mailbox text. Username: current account display
	// name.
	Email, Username string
}

// UserReader supplies current account mailbox and display values for team
// review operations.
type UserReader interface {
	// GetByID supplies current account ID, mailbox and username for review
	// preparation. Preview/resolution and successful transfer-target reads need
	// a nonnil snapshot; repository failure remains visible except the
	// documented transfer notification lookup skip.
	GetByID(context.Context, int64) (*UserSnapshot, error)
	// GetByEmail supplies optional recipient display/account hints for an
	// invitation mailbox. Notification lookup failure or absent account is
	// ignored in favor of sender-derived greeting and zero recipient identity;
	// storage owns matching rules.
	GetByEmail(context.Context, string) (*UserSnapshot, error)
}
type (
	// Settings supplies selected frontend base text for team review links.
	Settings interface {
		// GetFrontendURL returns the selected frontend base for review links.
		// TeamService gives a present reader precedence even for an empty result;
		// readers own their stored/startup selection and caller context handling.
		GetFrontendURL(context.Context) string
	}
	// KeyInvalidator accepts authentication invalidation requests for selected
	// full team credentials.
	KeyInvalidator interface {
		// InvalidateAuthCacheByKey requests invalidation for a full team credential
		// under caller context. It supplies no acknowledgement; TeamService invokes
		// it after successful selected mutations and handles no publication retry
		// here.
		InvalidateAuthCacheByKey(context.Context, string)
	}
	// Notifier delivers persisted team invitation and ownership-review links to
	// their recipients.
	Notifier interface {
		// SendInvitation delivers the normalized recipient mailbox, team name,
		// prepared link and invitation expiry. Repository persistence precedes this
		// call, and delivery failure leaves that saved review state available to
		// its caller.
		SendInvitation(context.Context, string, string, string, time.Time) error
		// SendOwnershipTransfer delivers the target mailbox, team name and prepared
		// ownership-review link. The service saves the review before this call and
		// reports delivery failure without restoring its earlier review state.
		SendOwnershipTransfer(context.Context, string, string, string) error
	}
)

// NewTeamService retains repositories, optional
// notification/invalidation/settings capabilities, invitation limiter and
// options. Callers keep these dependencies alive and configure shared options
// before handling requests; nil options permit feature/self-service checks
// with the built-in member ceiling.
func NewTeamService(repo TeamRepository, users UserReader, notifier Notifier, keys KeyInvalidator, limiter TeamInvitationLimiter, settings Settings, options *Options) *TeamService {
	return &TeamService{repo: repo, userRepo: users, emailService: notifier, apiKeyCache: keys, inviteLimiter: limiter, settingService: settings, options: options}
}

// ensureEnabled rejects team operations when retained options disable the
// feature. Nil options permit the call; this action gate reads startup
// options rather than the stored public team switch.
func (s *TeamService) ensureEnabled() error {
	if s.options != nil && !s.options.Enabled {
		return ErrTeamFeatureDisabled
	}
	return nil
}

// Create validates feature/self-service enablement and trimmed team name
// before delegating owner/team creation. Nil options or a negative default
// ceiling selects ten; a nonnegative configured ceiling, including zero, is
// passed to storage for ordinary-member admission.
func (s *TeamService) Create(ctx context.Context, userID int64, name string) (*TeamContext, error) {
	if err := s.ensureEnabled(); err != nil {
		return nil, err
	}
	if s.options != nil && !s.options.SelfServiceEnabled {
		return nil, ErrTeamSelfServiceDisabled
	}
	name, err := NormalizeTeamName(name)
	if err != nil {
		return nil, err
	}
	limit := 10
	if s.options != nil && s.options.DefaultMemberLimit >= 0 {
		limit = s.options.DefaultMemberLimit
	}
	return s.repo.Create(ctx, name, userID, limit)
}

// AdminCreate validates feature availability and team name for an authorized
// administrator. Self-service disablement does not block it; an optional
// ceiling replaces startup/built-in selection, negatives reject and zero
// admits no ordinary members through repository resolution.
func (s *TeamService) AdminCreate(ctx context.Context, ownerUserID int64, name string, memberLimit *int) (*TeamContext, error) {
	if err := s.ensureEnabled(); err != nil {
		return nil, err
	}
	name, err := NormalizeTeamName(name)
	if err != nil {
		return nil, err
	}
	limit := 10
	if s.options != nil && s.options.DefaultMemberLimit >= 0 {
		limit = s.options.DefaultMemberLimit
	}
	if memberLimit != nil {
		limit = *memberLimit
	}
	if limit < 0 {
		return nil, infraerrors.BadRequest("TEAM_MEMBER_LIMIT_INVALID", "Team member limit must not be negative")
	}
	return s.repo.Create(ctx, name, ownerUserID, limit)
}

// GetCurrent delegates current team-context lookup after the process feature
// gate. The repository determines membership/missing-row behavior; this
// reader performs no active-team or owner requirement, allowing callers to
// display suspended context.
func (s *TeamService) GetCurrent(ctx context.Context, userID int64) (*TeamContext, error) {
	if err := s.ensureEnabled(); err != nil {
		return nil, err
	}
	return s.repo.GetContextByUserID(ctx, userID)
}

// UpdateName validates active ownership and trimmed name text before a
// repository rename. It returns team readback afterward, so a later read
// failure can follow a successful name write; administrator correction uses
// its separate method.
func (s *TeamService) UpdateName(ctx context.Context, userID int64, name string) (*TeamContext, error) {
	teamCtx, err := s.requireOwner(ctx, userID)
	if err != nil {
		return nil, err
	}
	name, err = NormalizeTeamName(name)
	if err != nil {
		return nil, err
	}
	// Save the validated name before requesting context readback; a failed later
	// read can leave the renamed team stored even though this call returns an
	// error.
	if err := s.repo.UpdateName(ctx, teamCtx.Team.ID, name); err != nil {
		return nil, err
	}
	return s.repo.GetContextByTeamID(ctx, teamCtx.Team.ID)
}

// UpdateDefaultMemberLimits saves the daily/weekly/monthly USD defaults
// inherited by later joins after active-owner admission. Negative amounts
// reject before writing; zero selects unlimited periods, and existing
// memberships keep their separate limits. Readback follows the default write.
func (s *TeamService) UpdateDefaultMemberLimits(ctx context.Context, userID int64, daily, weekly, monthly float64) (*TeamContext, error) {
	teamCtx, err := s.requireOwner(ctx, userID)
	if err != nil {
		return nil, err
	}
	if daily < 0 || weekly < 0 || monthly < 0 {
		return nil, infraerrors.BadRequest("TEAM_DEFAULT_MEMBER_LIMIT_INVALID", "Default member quota must not be negative")
	}
	// Save future-join defaults before readback, leaving existing member
	// ceilings unchanged; context read failure can follow a completed default
	// replacement.
	if err := s.repo.SetDefaultMemberLimits(ctx, teamCtx.Team.ID, daily, weekly, monthly); err != nil {
		return nil, err
	}
	return s.repo.GetContextByTeamID(ctx, teamCtx.Team.ID)
}

// SetStatus accepts exact active or suspended text and admits an owner even
// while the team is suspended. Repository success triggers optional Key
// invalidation, then current-context readback; invalidation listing can fail
// silently and readback can fail after the status write.
func (s *TeamService) SetStatus(ctx context.Context, userID int64, status string) (*TeamContext, error) {
	if status != TeamStatusActive && status != TeamStatusSuspended {
		return nil, infraerrors.BadRequest("TEAM_STATUS_INVALID", "Invalid team status")
	}
	teamCtx, err := s.requireOwnerIncludingSuspended(ctx, userID)
	if err != nil {
		return nil, err
	}
	// Write status before requesting Key invalidation and current context;
	// invalidation list errors stay suppressed and a read failure can follow the
	// saved status.
	if err := s.repo.SetStatus(ctx, teamCtx.Team.ID, status); err != nil {
		return nil, err
	}
	s.invalidateTeamKeys(ctx, teamCtx.Team.ID)
	return s.repo.GetContextByUserID(ctx, userID)
}

// ListMembers returns memberships after active-team admission. Owners receive
// the repository slice; ordinary members receive a newly allocated outer
// subset containing their own and owner records, with any nested time
// pointers retained from those source records.
func (s *TeamService) ListMembers(ctx context.Context, userID int64) ([]TeamMembership, error) {
	teamCtx, err := s.requireMembership(ctx, userID)
	if err != nil {
		return nil, err
	}
	members, err := s.repo.ListMembers(ctx, teamCtx.Team.ID)
	if err != nil {
		return nil, err
	}
	if teamCtx.Membership.Role == TeamRoleOwner {
		return members, nil
	}
	filtered := make([]TeamMembership, 0, 2)
	for _, member := range members {
		if member.UserID == userID || member.Role == TeamRoleOwner {
			filtered = append(filtered, member)
		}
	}
	return filtered, nil
}

// GetUsageSummary normalizes a requested range and delegates totals for an
// active membership. Owners retain the requested actor filter, while ordinary
// members replace it with their own ID; Key filtering remains in the supplied
// query.
func (s *TeamService) GetUsageSummary(ctx context.Context, userID int64, query TeamUsageQuery) (*TeamUsageSummary, error) {
	teamID, query, err := s.prepareUsageReport(ctx, userID, query)
	if err != nil {
		return nil, err
	}
	return s.repo.GetUsageSummary(ctx, teamID, query)
}

// ListMemberUsageSeries returns normalized member trends for an active
// membership. Owner actor filters remain selected, while ordinary members are
// fixed to themselves; repository reporting determines represented
// current/departed members within that authorized scope.
func (s *TeamService) ListMemberUsageSeries(ctx context.Context, userID int64, query TeamUsageQuery) ([]TeamMemberUsageSeries, error) {
	teamID, query, err := s.prepareUsageReport(ctx, userID, query)
	if err != nil {
		return nil, err
	}
	return s.repo.ListMemberUsageSeries(ctx, teamID, query)
}

// ListUsageLogs returns a page of normalized team usage records after
// active-membership admission. Ordinary members are fixed to their own actor
// ID; the result retains repository item storage and reports the normalized
// limit/offset alongside its total.
func (s *TeamService) ListUsageLogs(ctx context.Context, userID int64, query TeamUsageQuery) (*TeamUsagePage, error) {
	teamID, query, err := s.prepareUsageReport(ctx, userID, query)
	if err != nil {
		return nil, err
	}
	items, total, err := s.repo.ListUsageLogs(ctx, teamID, query)
	if err != nil {
		return nil, err
	}
	return &TeamUsagePage{Items: items, Total: total, Limit: query.Limit, Offset: query.Offset}, nil
}

// prepareUsageReport authorizes an active membership and returns its team ID
// with normalized report filters. Membership failures precede clock sampling;
// admitted calls sample once. Key filters pass through, owners keep their actor
// filter, and other roles select the acting user.
func (s *TeamService) prepareUsageReport(ctx context.Context, userID int64, query TeamUsageQuery) (int64, TeamUsageQuery, error) {
	teamCtx, err := s.requireMembership(ctx, userID)
	if err != nil {
		return 0, TeamUsageQuery{}, err
	}
	query = NormalizeTeamUsageQueryAt(query, s.now())
	if teamCtx.Membership.Role != TeamRoleOwner {
		query.ActorUserID = &userID
	}
	return teamCtx.Team.ID, query, nil
}

// ListTeamKeys returns masked team credentials for a membership, including a
// suspended team. Owner lookup uses no creator filter, while other roles
// select the acting account before repository listing.
//
// Each returned item is changed in place to set MaskedKey before clearing
// Key, so a repository sharing item backing storage observes those changes
// too. JSON already omits Key, but clearing also removes plaintext from the
// direct returned values. Callers retain the display records and coordinate
// any mutations of their shared pointer fields after this conversion.
func (s *TeamService) ListTeamKeys(ctx context.Context, userID int64) ([]TeamAPIKeyItem, error) {
	teamCtx, err := s.requireMembershipIncludingSuspended(ctx, userID)
	if err != nil {
		return nil, err
	}
	actorFilter := TeamKeyActorFilter(teamCtx, userID)
	items, err := s.repo.ListTeamKeys(ctx, teamCtx.Team.ID, actorFilter)
	if err != nil {
		return nil, err
	}
	// Mask then clear each plaintext credential in the returned slice before
	// JSON exposure, including repositories that retain shared item storage with
	// this service caller.
	for i := range items {
		items[i].MaskedKey = logredact.MaskCredential(items[i].Key)
		items[i].Key = ""
	}
	return items, nil
}

// DisableTeamKey admits the owner even while suspended and delegates Key
// disablement across the team. Successful storage returns its credential for
// optional invalidation; failure stops invalidation and the service returns
// no publication acknowledgement.
func (s *TeamService) DisableTeamKey(ctx context.Context, userID, keyID int64) error {
	teamCtx, err := s.requireOwnerIncludingSuspended(ctx, userID)
	if err != nil {
		return err
	}
	// Keep credential mutation before invalidation, using the returned full Key
	// to target authentication storage only after the repository accepts owner
	// disablement.
	key, err := s.repo.DisableTeamKey(ctx, teamCtx.Team.ID, keyID, nil)
	if err != nil {
		return err
	}
	s.invalidateKey(ctx, key)
	return nil
}

// EnableTeamKey admits the owner even while suspended and delegates clearing
// the Key's owner lock/disabled status. The returned credential supplies
// optional invalidation after storage success, leaving cache propagation to
// the configured capability.
func (s *TeamService) EnableTeamKey(ctx context.Context, userID, keyID int64) error {
	teamCtx, err := s.requireOwnerIncludingSuspended(ctx, userID)
	if err != nil {
		return err
	}
	// Clear persisted owner disablement before invalidation, using the returned
	// full credential so later authentication can reload the accepted
	// owner-controlled state.
	key, err := s.repo.EnableTeamKey(ctx, teamCtx.Team.ID, keyID, nil)
	if err != nil {
		return err
	}
	s.invalidateKey(ctx, key)
	return nil
}

// DeleteTeamKey admits the owner even while suspended and delegates
// credential deletion. Storage returns the prior credential after success for
// optional invalidation; repository error stops that request and this service
// performs no deletion compensation.
func (s *TeamService) DeleteTeamKey(ctx context.Context, userID, keyID int64) error {
	teamCtx, err := s.requireOwnerIncludingSuspended(ctx, userID)
	if err != nil {
		return err
	}
	// Complete credential deletion before invalidation, retaining the
	// repository's prior full Key as the cache target rather than a display mask
	// or tombstone.
	key, err := s.repo.DeleteTeamKey(ctx, teamCtx.Team.ID, keyID, nil)
	if err != nil {
		return err
	}
	s.invalidateKey(ctx, key)
	return nil
}

// TeamKeyActorFilter returns nil for an owner membership and a fresh pointer
// to userID otherwise, including absent context/membership. Callers authorize
// a usable acting account first; this helper does not validate positive
// identity or team availability.
func TeamKeyActorFilter(teamCtx *TeamContext, userID int64) *int64 {
	if teamCtx != nil && teamCtx.Membership != nil && teamCtx.Membership.Role == TeamRoleOwner {
		return nil
	}
	return &userID
}

// NormalizeTeamUsageQueryAt returns a query with selected dates ordered and
// usable pagination. Zero end uses now, and zero start uses thirty calendar
// days before that selected end; reversed nonzero dates are swapped
// afterward.
//
// Limits outside one through one hundred select twenty, and negative offsets
// select zero while positive offsets have no upper cap. Pointer filters
// remain shared with the input value, and this conversion grants no
// membership scope. Service callers apply their owner/member actor rule after
// normalization so a requested filter cannot expand ordinary-member
// visibility.
func NormalizeTeamUsageQueryAt(query TeamUsageQuery, now time.Time) TeamUsageQuery {
	if query.To.IsZero() {
		query.To = now
	}
	// Derive the omitted start from the selected end in calendar days, then
	// order endpoints before pagination normalization so default ranges follow
	// that chosen report endpoint.
	if query.From.IsZero() {
		query.From = query.To.AddDate(0, 0, -30)
	}
	if query.From.After(query.To) {
		query.From, query.To = query.To, query.From
	}
	// Keep usage-log page sizes within the service-supported bounded interval.
	if query.Limit <= 0 || query.Limit > 100 {
		query.Limit = 20
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	return query
}

// Invite creates an email-bound invitation for an active owner and returns
// its metadata after optional delivery. Mailbox normalization can collapse
// alias text before rate admission, so cooldown/storage/delivery receive that
// normalized identity.
//
// The limiter records admission before random-token generation, review-link
// preparation or repository creation, and later failure does not refund that
// permit. A configured notifier requires the link before saving; an absent
// notifier still permits storing the token hash with a seven-day elapsed
// lifetime. Delivery follows the saved invitation, so mail failure returns an
// error while that invitation remains pending rather than rolling it back.
func (s *TeamService) Invite(ctx context.Context, userID int64, email string) (*TeamInvitation, error) {
	teamCtx, err := s.requireOwner(ctx, userID)
	if err != nil {
		return nil, err
	}
	email, err = NormalizeTeamEmail(email)
	if err != nil {
		return nil, err
	}
	// Record admission before token generation and review-link/storage work, so
	// termination during token filling or later preparation errors leave the accepted
	// recipient/team rate attempt consumed.
	if err := s.CheckInvitationRate(ctx, teamCtx.Team.ID, email); err != nil {
		return nil, err
	}
	token, tokenHash, err := NewTeamToken()
	if err != nil {
		return nil, err
	}
	link := ""
	if s.emailService != nil {
		link, err = s.FrontendLink(ctx, "/team", "invitation", token)
		if err != nil {
			return nil, err
		}
	}
	// Limit pending invitation review to seven elapsed days from creation.
	expiresAt := s.now().Add(7 * 24 * time.Hour)
	// Save the token digest/deadline before optional delivery; a send error
	// leaves the new pending invitation stored, and absence of a notifier still
	// permits persistence.
	invitation, err := s.repo.CreateInvitation(ctx, teamCtx.Team.ID, userID, email, tokenHash, expiresAt)
	if err != nil {
		return nil, err
	}
	if err := s.SendInvitationEmail(ctx, email, teamCtx.Team.Name, link, expiresAt); err != nil {
		return nil, err
	}
	return invitation, nil
}

// ListInvitations delegates the active owner's team invitation listing.
// Repository status/expiry maintenance determines the returned rows; callers
// receive its slice directly and handle read failures before displaying
// invitation metadata.
func (s *TeamService) ListInvitations(ctx context.Context, userID int64) ([]TeamInvitation, error) {
	teamCtx, err := s.requireOwner(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.repo.ListInvitations(ctx, teamCtx.Team.ID)
}

// ReissueInvitation replaces a selected active-owner invitation's token state
// and returns its metadata after optional delivery. The existing record
// supplies its recipient mailbox before rate admission; repository rules
// determine which prior statuses can be reissued.
//
// An admitted resend consumes its permit before token generation and link
// preparation. With a notifier, link preparation precedes replacement; the
// new hash and seven-day elapsed deadline are saved before email delivery. A
// delivery failure leaves that replacement state in storage and the earlier
// credential displaced, so callers cannot infer that a failed return retains
// the old review token.
func (s *TeamService) ReissueInvitation(ctx context.Context, userID, invitationID int64) (*TeamInvitation, error) {
	teamCtx, err := s.requireOwner(ctx, userID)
	if err != nil {
		return nil, err
	}
	existing, err := s.repo.GetInvitationByID(ctx, teamCtx.Team.ID, invitationID)
	if err != nil {
		return nil, err
	}
	// Rate-check the stored recipient before replacing its token, retaining the
	// consumed resend permit even if new-token generation, link preparation or
	// repository replacement later fails.
	if err := s.CheckInvitationRate(ctx, teamCtx.Team.ID, existing.Email); err != nil {
		return nil, err
	}
	token, tokenHash, err := NewTeamToken()
	if err != nil {
		return nil, err
	}
	link := ""
	if s.emailService != nil {
		link, err = s.FrontendLink(ctx, "/team", "invitation", token)
		if err != nil {
			return nil, err
		}
	}
	// Date the replacement review seven elapsed days after the current service instant.
	expiresAt := s.now().Add(7 * 24 * time.Hour)
	// Save the replacement digest/deadline before optional resend; mail failure
	// leaves the earlier token displaced and the new pending state available in
	// storage.
	invitation, err := s.repo.ReissueInvitation(ctx, teamCtx.Team.ID, invitationID, tokenHash, expiresAt)
	if err != nil {
		return nil, err
	}
	if err := s.SendInvitationEmail(ctx, invitation.Email, teamCtx.Team.Name, link, expiresAt); err != nil {
		return nil, err
	}
	return invitation, nil
}

// CheckInvitationRate requests shared admission and returns a rate-limit or
// unavailable error when it cannot proceed. Missing limiter or any limiter
// failure rejects the invitation, with failures logged before unavailable
// selection.
//
// An accepted check is already recorded by the limiter before this service
// prepares token/link/storage work. Denied duration becomes a whole-second
// retry hint with a one-second minimum, stored as decimal metadata. Callers keep
// admission before downstream invitation work; token-generation termination or a
// later storage/mail error leaves that recorded permit consumed.
func (s *TeamService) CheckInvitationRate(ctx context.Context, teamID int64, email string) error {
	if s.inviteLimiter == nil {
		return ErrTeamInvitationUnavailable
	}
	allowed, retryAfter, err := s.inviteLimiter.CheckAndRecord(ctx, teamID, email)
	if err != nil {
		slog.Warn("Team invitation rate limiter is unavailable", "team_id", teamID, "error", err)
		return ErrTeamInvitationUnavailable
	}
	if allowed {
		return nil
	}
	// Round denied delay upward and expose at least one retry second.
	retrySeconds := max(int64((retryAfter+time.Second-1)/time.Second), 1)
	return ErrTeamInvitationRateLimited.WithMetadata(map[string]string{"retry_after": strconv.FormatInt(retrySeconds, 10)})
}

// RevokeInvitation delegates revocation for the authenticated active owner's
// team and selected invitation ID. Storage enforces the pending-state match;
// this service returns its outcome without generating a token, sending mail
// or invalidating Keys.
func (s *TeamService) RevokeInvitation(ctx context.Context, userID, invitationID int64) error {
	teamCtx, err := s.requireOwner(ctx, userID)
	if err != nil {
		return err
	}
	return s.repo.RevokeInvitation(ctx, teamCtx.Team.ID, invitationID)
}

// PreviewInvitation hashes a nonblank supplied token and delegates preview
// with the current account's normalized mailbox and service time. Feature
// availability and account-read failures are checked first; UserReader must
// return a nonnil account on success for mailbox access.
func (s *TeamService) PreviewInvitation(ctx context.Context, userID int64, token string) (*TeamInvitationPreview, error) {
	if err := s.ensureEnabled(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, ErrTeamInvitationInvalid
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.repo.PreviewInvitation(
		ctx,
		HashTeamToken(token),
		identity.NormalizeRegistrationEmailAddress(user.Email),
		s.now(),
	)
}

// ResolveInvitation delegates exact accepted/declined resolution after
// feature admission and current-account lookup. The repository checks hash,
// mailbox and state and owns membership transaction completion; successful
// decline can return nil context, and this method makes no separate
// cache-publication request.
func (s *TeamService) ResolveInvitation(ctx context.Context, userID int64, token, resolution string) (*TeamContext, error) {
	if err := s.ensureEnabled(); err != nil {
		return nil, err
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if resolution != "accepted" && resolution != "declined" {
		return nil, ErrTeamInvitationInvalid
	}
	return s.repo.ResolveInvitation(ctx, HashTeamToken(token), userID, identity.NormalizeRegistrationEmailAddress(user.Email), resolution, s.now())
}

// RemoveMember requires active ownership and rejects removing the acting
// owner from the team. Repository success removes the selected ordinary
// membership before team-wide invalidation is attempted; Key-listing failure
// during that attempt is suppressed.
func (s *TeamService) RemoveMember(ctx context.Context, ownerUserID, memberUserID int64) error {
	teamCtx, err := s.requireOwner(ctx, ownerUserID)
	if err != nil {
		return err
	}
	if ownerUserID == memberUserID {
		return ErrTeamOwnerCannotLeave
	}
	// Complete ordinary membership departure before team-Key invalidation,
	// allowing subsequent authentication reload to observe the repository's
	// member/credential lifecycle result.
	if err := s.repo.RemoveMember(ctx, teamCtx.Team.ID, memberUserID, s.now()); err != nil {
		return err
	}
	s.invalidateTeamKeys(ctx, teamCtx.Team.ID)
	return nil
}

// Leave removes an ordinary acting membership, including while the team is
// suspended. Owners must transfer or dissolve instead; successful repository
// departure precedes team-wide invalidation attempts whose listing errors do
// not change the returned result.
func (s *TeamService) Leave(ctx context.Context, userID int64) error {
	teamCtx, err := s.requireMembershipIncludingSuspended(ctx, userID)
	if err != nil {
		return err
	}
	if teamCtx.Membership.Role == TeamRoleOwner {
		return ErrTeamOwnerCannotLeave
	}
	// End the acting ordinary membership before team-Key invalidation, retaining
	// owner/departure authorization as the earlier admission rule even for
	// suspended teams.
	if err := s.repo.RemoveMember(ctx, teamCtx.Team.ID, userID, s.now()); err != nil {
		return err
	}
	s.invalidateTeamKeys(ctx, teamCtx.Team.ID)
	return nil
}

// UpdateMemberLimits validates active ownership and nonnegative USD amounts
// before replacing an ordinary member's complete quota triple. Zero means
// unlimited per billing admission; successful repository update precedes
// optional team-wide authentication invalidation.
func (s *TeamService) UpdateMemberLimits(ctx context.Context, ownerUserID, memberUserID int64, daily, weekly, monthly float64) error {
	teamCtx, err := s.requireOwner(ctx, ownerUserID)
	if err != nil {
		return err
	}
	if daily < 0 || weekly < 0 || monthly < 0 {
		return infraerrors.BadRequest("TEAM_MEMBER_LIMIT_INVALID", "Member quota must not be negative")
	}
	// Replace the member's complete quota triple before invalidation so future
	// authentication snapshots can reload its accepted ceiling values rather
	// than a failed write.
	if err := s.repo.UpdateMemberLimits(ctx, teamCtx.Team.ID, memberUserID, daily, weekly, monthly); err != nil {
		return err
	}
	s.invalidateTeamKeys(ctx, teamCtx.Team.ID)
	return nil
}

// ResetMemberUsage requires active ownership and at least one selected
// calendar period. It delegates selected resets with service time, then
// attempts team-wide invalidation after success; the repository/billing
// participant owns the window-reset persistence rules.
func (s *TeamService) ResetMemberUsage(ctx context.Context, ownerUserID, memberUserID int64, daily, weekly, monthly bool) error {
	teamCtx, err := s.requireOwner(ctx, ownerUserID)
	if err != nil {
		return err
	}
	if !daily && !weekly && !monthly {
		return infraerrors.BadRequest("TEAM_USAGE_RESET_EMPTY", "Select at least one period to reset")
	}
	// Persist selected billing-period resets before invalidation, keeping future
	// authentication snapshots tied to the participant's completed usage/window
	// update.
	if err := s.repo.ResetMemberUsage(ctx, teamCtx.Team.ID, memberUserID, daily, weekly, monthly, s.now()); err != nil {
		return err
	}
	s.invalidateTeamKeys(ctx, teamCtx.Team.ID)
	return nil
}

// StartOwnershipTransfer creates a reviewable transfer for an active owner
// and a different target ID. It generates a token first; storage later
// requires that target to be an ordinary current member.
//
// With a notifier, a successful target-account read prepares its review link
// before persistence, while lookup failure is ignored and can leave the
// transfer saved without mail. Successful reads must supply a nonnil account.
// The saved hash has a twenty-four-hour elapsed lifetime, and optional
// delivery follows creation; send failure returns an error after saved
// transfer state already exists.
func (s *TeamService) StartOwnershipTransfer(ctx context.Context, ownerUserID, targetUserID int64) (*TeamOwnershipTransfer, error) {
	teamCtx, err := s.requireOwner(ctx, ownerUserID)
	if err != nil {
		return nil, err
	}
	if ownerUserID == targetUserID {
		return nil, ErrTeamTransferInvalid
	}
	token, tokenHash, err := NewTeamToken()
	if err != nil {
		return nil, err
	}
	var targetEmail, link string
	if s.emailService != nil {
		// Optional target lookup determines delivery only: ignore read failure and
		// continue review persistence, while a successful lookup requires a nonnil
		// account before mailbox access.
		target, getErr := s.userRepo.GetByID(ctx, targetUserID)
		if getErr == nil {
			link, err = s.FrontendLink(ctx, "/team", "transfer", token)
			if err != nil {
				return nil, err
			}
			targetEmail = target.Email
		}
	}
	// Limit ownership review to twenty-four elapsed hours after preparation.
	expiresAt := s.now().Add(24 * time.Hour)
	// Save review state before optional target mail, retaining the transfer if
	// delivery fails or target lookup skipped notification after its read error.
	transfer, err := s.repo.CreateOwnershipTransfer(ctx, teamCtx.Team.ID, ownerUserID, targetUserID, tokenHash, expiresAt)
	if err != nil {
		return nil, err
	}
	if targetEmail != "" {
		if sendErr := s.emailService.SendOwnershipTransfer(ctx, targetEmail, teamCtx.Team.Name, link); sendErr != nil {
			return nil, sendErr
		}
	}
	return transfer, nil
}

// ResolveOwnershipTransfer delegates exact accepted/declined review after the
// feature gate. A successful nonnil returned context triggers team-wide
// invalidation; repository readback failure after a committed decision can
// therefore prevent this service publication attempt.
func (s *TeamService) ResolveOwnershipTransfer(ctx context.Context, userID int64, token, resolution string) (*TeamContext, error) {
	if err := s.ensureEnabled(); err != nil {
		return nil, err
	}
	if resolution != "accepted" && resolution != "declined" {
		return nil, ErrTeamTransferInvalid
	}
	// Publish Key invalidation after a successful returned context; a repository
	// context read error after commit can leave the decision saved while
	// skipping this publication.
	teamCtx, err := s.repo.ResolveOwnershipTransfer(ctx, HashTeamToken(token), userID, resolution, s.now())
	if err == nil && teamCtx != nil {
		s.invalidateTeamKeys(ctx, teamCtx.Team.ID)
	}
	return teamCtx, err
}

// CancelOwnershipTransfer delegates pending review cancellation for the
// active owner's team. The repository also matches originating actor/state
// and returns its outcome; this service generates no replacement review token
// or Key invalidation.
func (s *TeamService) CancelOwnershipTransfer(ctx context.Context, ownerUserID int64) error {
	teamCtx, err := s.requireOwner(ctx, ownerUserID)
	if err != nil {
		return err
	}
	return s.repo.CancelOwnershipTransfer(ctx, teamCtx.Team.ID, ownerUserID, s.now())
}

// Dissolve permits an owner of an active or suspended team to request
// teardown. Repository success precedes optional team-Key invalidation; the
// store owns membership/review/credential transaction completion and errors
// remain visible before invalidation.
func (s *TeamService) Dissolve(ctx context.Context, ownerUserID int64) error {
	teamCtx, err := s.requireOwnerIncludingSuspended(ctx, ownerUserID)
	if err != nil {
		return err
	}
	// Complete repository teardown before invalidation, using the team identity
	// to reload the ended tenant/credential lifecycle rather than publishing a
	// failed teardown.
	if err := s.repo.Dissolve(ctx, teamCtx.Team.ID, s.now()); err != nil {
		return err
	}
	s.invalidateTeamKeys(ctx, teamCtx.Team.ID)
	return nil
}

// AdminUpdateName normalizes the requested name before an
// administrator-authorized repository correction. It performs no
// current-membership or feature check; route/caller authorization is
// required, and the repository owns the write result.
func (s *TeamService) AdminUpdateName(ctx context.Context, teamID int64, name string) error {
	name, err := NormalizeTeamName(name)
	if err != nil {
		return err
	}
	return s.repo.UpdateName(ctx, teamID, name)
}

// AdminUpdate validates all selected name/status/member-ceiling inputs before
// a single administrator repository update. Nil pointers omit fields, an
// entirely empty update returns readback, and negative ceilings or
// unsupported status reject before persistence.
//
// A selected name is replaced by newly trimmed text in the local update
// value, while other pointers remain retained. Selected status requests
// team-Key invalidation after storage success, even when its value matches
// the current status, and final readback can still fail after the write. The
// caller supplies administrator authorization; this method applies no
// current-member or feature gate.
func (s *TeamService) AdminUpdate(ctx context.Context, teamID int64, update TeamAdminUpdate) (*TeamContext, error) {
	if update.Name != nil {
		name, err := NormalizeTeamName(*update.Name)
		if err != nil {
			return nil, err
		}
		update.Name = &name
	}
	if update.Status != nil && *update.Status != TeamStatusActive && *update.Status != TeamStatusSuspended {
		return nil, infraerrors.BadRequest("TEAM_STATUS_INVALID", "Invalid team status")
	}
	if update.MemberLimit != nil && *update.MemberLimit < 0 {
		return nil, infraerrors.BadRequest("TEAM_MEMBER_LIMIT_INVALID", "Team member limit must not be negative")
	}
	if update.Name == nil && update.Status == nil && update.MemberLimit == nil {
		return s.repo.GetContextByTeamID(ctx, teamID)
	}
	// Write every validated selected field before optional status invalidation
	// and final readback; readback failure can follow saved administrator state,
	// while invalidation receives no acknowledgement.
	if err := s.repo.UpdateAdmin(ctx, teamID, update); err != nil {
		return nil, err
	}
	if update.Status != nil {
		s.invalidateTeamKeys(ctx, teamID)
	}
	return s.repo.GetContextByTeamID(ctx, teamID)
}

// AdminForceTransfer delegates an authorized administrator ownership swap
// with service time. Repository success triggers team-wide invalidation; a
// committed swap followed by repository readback failure returns an error and
// skips that publication attempt.
func (s *TeamService) AdminForceTransfer(ctx context.Context, teamID, targetUserID int64) (*TeamContext, error) {
	// Request invalidation after a successful swap result; post-commit
	// repository readback failure can skip this service attempt even when owner
	// roles already changed.
	teamCtx, err := s.repo.ForceTransfer(ctx, teamID, targetUserID, s.now())
	if err == nil {
		s.invalidateTeamKeys(ctx, teamID)
	}
	return teamCtx, err
}

// AdminList returns the repository's team enumeration for an
// administrator-authorized caller. It performs no feature or
// current-membership gate itself; route assembly supplies administrator
// permission before this delegation and read errors remain visible.
func (s *TeamService) AdminList(ctx context.Context) ([]TeamAdminListItem, error) {
	return s.repo.ListAdmin(ctx)
}

// AdminGet returns the repository's context for an administrator-authorized
// team lookup. It performs no process-feature or current-membership gate
// itself; route assembly supplies permission and the repository owns
// missing/read outcomes.
func (s *TeamService) AdminGet(ctx context.Context, teamID int64) (*TeamContext, error) {
	return s.repo.GetContextByTeamID(ctx, teamID)
}

// AdminListMembers checks team-context availability before delegating its
// complete membership list. The caller supplies administrator authorization,
// and no acting-member filter or process feature check is applied by this
// method.
func (s *TeamService) AdminListMembers(ctx context.Context, teamID int64) ([]TeamMembership, error) {
	if _, err := s.repo.GetContextByTeamID(ctx, teamID); err != nil {
		return nil, err
	}
	return s.repo.ListMembers(ctx, teamID)
}

// AdminGetUsageSummary checks team availability and normalizes its report
// range before delegation. The caller supplies administrator authorization;
// its selected actor/Key filters remain in the query with no current-member
// scope replacement.
func (s *TeamService) AdminGetUsageSummary(ctx context.Context, teamID int64, query TeamUsageQuery) (*TeamUsageSummary, error) {
	if _, err := s.repo.GetContextByTeamID(ctx, teamID); err != nil {
		return nil, err
	}
	return s.repo.GetUsageSummary(ctx, teamID, NormalizeTeamUsageQueryAt(query, s.now()))
}

// AdminDissolve delegates teardown for an administrator-authorized team ID
// without current membership or feature gating. Repository success precedes
// team-Key invalidation attempts, while storage error stops those attempts
// before return.
func (s *TeamService) AdminDissolve(ctx context.Context, teamID int64) error {
	// Finish administrator teardown before invalidation attempts, leaving
	// lifecycle transaction ownership with the repository and suppressing only
	// the later optional Key-list publication failure.
	if err := s.repo.Dissolve(ctx, teamID, s.now()); err != nil {
		return err
	}
	s.invalidateTeamKeys(ctx, teamID)
	return nil
}

// requireMembership returns a usable acting membership only when its team is
// active. It first applies feature/context checks through the
// suspended-capable reader, then returns team-suspended for every other
// represented status.
func (s *TeamService) requireMembership(ctx context.Context, userID int64) (*TeamContext, error) {
	teamCtx, err := s.requireMembershipIncludingSuspended(ctx, userID)
	if err != nil {
		return nil, err
	}
	if teamCtx.Team.Status != TeamStatusActive {
		return nil, ErrTeamSuspended
	}
	return teamCtx, nil
}

// requireMembershipIncludingSuspended reads a context after the process
// feature gate and requires Team plus Membership. Nil or incomplete success
// results become membership-required; Owner presence and account status are
// not independently checked here.
func (s *TeamService) requireMembershipIncludingSuspended(ctx context.Context, userID int64) (*TeamContext, error) {
	if err := s.ensureEnabled(); err != nil {
		return nil, err
	}
	teamCtx, err := s.repo.GetContextByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if teamCtx == nil || teamCtx.Team == nil || teamCtx.Membership == nil {
		return nil, ErrTeamMembershipRequired
	}
	return teamCtx, nil
}

// requireOwner returns active team context when the acting membership role is
// owner. It delegates feature/membership/status checks first; any other
// represented role returns owner-required without a repository mutation.
func (s *TeamService) requireOwner(ctx context.Context, userID int64) (*TeamContext, error) {
	teamCtx, err := s.requireMembership(ctx, userID)
	if err != nil {
		return nil, err
	}
	if teamCtx.Membership.Role != TeamRoleOwner {
		return nil, ErrTeamOwnerRequired
	}
	return teamCtx, nil
}

// requireOwnerIncludingSuspended permits an owner membership across
// represented team statuses after feature/context checks. Nonowner roles are
// rejected; callers use this for resume, Key management and teardown of
// suspended teams.
func (s *TeamService) requireOwnerIncludingSuspended(ctx context.Context, userID int64) (*TeamContext, error) {
	teamCtx, err := s.requireMembershipIncludingSuspended(ctx, userID)
	if err != nil {
		return nil, err
	}
	if teamCtx.Membership.Role != TeamRoleOwner {
		return nil, ErrTeamOwnerRequired
	}
	return teamCtx, nil
}

// invalidateTeamKeys requests invalidation for listed team credentials when
// cache and repository exist. A Key-list read failure is silently ignored;
// successful listing calls the cache synchronously without an acknowledgement
// result or retry.
func (s *TeamService) invalidateTeamKeys(ctx context.Context, teamID int64) {
	if s.apiKeyCache == nil || s.repo == nil {
		return
	}
	// Read credential targets for the optional publication attempt; listing
	// failure is silently skipped after the selected mutation, keeping its
	// successful repository result available to callers.
	keys, err := s.repo.ListTeamKeyStrings(ctx, teamID)
	if err != nil {
		return
	}
	for _, key := range keys {
		s.apiKeyCache.InvalidateAuthCacheByKey(ctx, key)
	}
}

// invalidateKey requests authentication invalidation when a cache and
// nonblank credential are supplied. Whitespace-only input skips the call,
// while accepted text is passed unchanged with the caller's context and no
// publication acknowledgement.
func (s *TeamService) invalidateKey(ctx context.Context, key string) {
	if s.apiKeyCache == nil || strings.TrimSpace(key) == "" {
		return
	}
	// Pass the unchanged full credential under caller context after nonblank
	// admission, targeting the stored authentication entry rather than the
	// display mask produced by listing.
	s.apiKeyCache.InvalidateAuthCacheByKey(ctx, key)
}

// FrontendLink combines selected base text with path and escaped query
// parameter/token. A present settings reader takes precedence even when it
// returns empty; options supply the base only when that reader is absent.
//
// Selected text is trimmed and stripped of trailing slashes, then an empty
// result returns frontend-URL-unavailable. The helper concatenates path and a
// new question-mark query rather than parsing or validating a URL, so callers
// supply a compatible base/path pair. Settings readers such as
// DisplaySettings can implement their own stored/startup selection before
// this service receives their result.
func (s *TeamService) FrontendLink(ctx context.Context, path, parameter, token string) (string, error) {
	base := ""
	// Select a present settings reader even when its text is empty; its own
	// defaults may supply startup URL, while direct Options selection applies
	// only to an absent reader.
	if s.settingService != nil {
		base = strings.TrimRight(strings.TrimSpace(s.settingService.GetFrontendURL(ctx)), "/")
	} else if s.options != nil {
		base = strings.TrimRight(strings.TrimSpace(s.options.FrontendURL), "/")
	}
	if base == "" {
		return "", ErrTeamFrontendURLUnavailable
	}
	return base + path + "?" + url.QueryEscape(parameter) + "=" + url.QueryEscape(token), nil
}

// NormalizeTeamName returns trimmed nonempty text limited to one hundred
// Unicode runes. Empty or longer names return the team-name application
// error; callers validate before saving a team name.
func NormalizeTeamName(name string) (string, error) {
	name = strings.TrimSpace(name)
	// Match the stored name capacity with Unicode-character validation.
	if name == "" || len([]rune(name)) > 100 {
		return "", infraerrors.BadRequest("TEAM_NAME_INVALID", "Team name must be between 1 and 100 characters")
	}
	return name, nil
}

// NormalizeTeamEmail validates a lowercased trimmed bare mailbox and returns
// its registration-normalized identity. Display-name address syntax is
// rejected when the parsed address differs from that entire input.
//
// Registration normalization removes plus suffixes after a nonempty local
// prefix and trailing domain dots; Gmail-family addresses also fold local
// dots and googlemail.com spelling. Thus invitation storage/delivery can
// receive a different mailbox string from the supplied address. Callers use
// this returned identity consistently for invitation rate/storage, while
// confirmation normalizes the current account mailbox under the same identity
// rules.
func NormalizeTeamEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	parsed, err := mail.ParseAddress(email)
	if err != nil || !strings.EqualFold(parsed.Address, email) {
		return "", infraerrors.BadRequest("TEAM_INVITATION_EMAIL_INVALID", "Enter a valid email address")
	}
	// Convert the validated bare mailbox to the registration alias identity, so
	// invitation rate/storage and current-account confirmation share
	// plus/domain/Gmail folding even when delivery text changes.
	return identity.NormalizeRegistrationEmailAddress(email), nil
}

// NewTeamToken returns lowercase hex from thirty-two random bytes and the
// SHA256 hex digest of that encoded text, in that order, with nil error.
// crypto/rand.Read terminates the process when io.ReadFull reports an error.
// Callers store a returned digest and expose plaintext through the review link.
func NewTeamToken() (string, string, error) {
	// Use a 256-bit random secret before hexadecimal token encoding.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("failed to generate team token: %w", err)
	}
	token := hex.EncodeToString(raw)
	return token, HashTeamToken(token), nil
}

// HashTeamToken returns the SHA256 hex digest of trimmed supplied token text.
// Case stays significant and blank text still has a digest; preview rejects
// blank input separately while resolution relies on repository matching.
func HashTeamToken(token string) string {
	// Trim review token spacing before hashing while retaining case
	// significance, letting emailed/copy-pasted token surroundings converge on
	// the same stored digest without changing its token bytes otherwise.
	hash := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(hash[:])
}

// SendInvitationEmail delegates invitation mail when a notifier is configured
// and the link is nonblank. Missing notifier returns success before link
// validation; configured delivery errors remain visible after caller-owned
// invitation persistence.
func (s *TeamService) SendInvitationEmail(ctx context.Context, email, teamName, link string, expiresAt time.Time) error {
	if s.emailService == nil {
		return nil
	}
	if strings.TrimSpace(link) == "" {
		return ErrTeamFrontendURLUnavailable
	}
	return s.emailService.SendInvitation(ctx, email, teamName, link, expiresAt)
}

// now reads the retained options clock when supplied and otherwise uses
// time.Now. The injected clock must be safe for concurrent callers; its
// returned location and time value pass through unchanged.
func (s *TeamService) now() time.Time {
	// Use an injected clock when configured and preserve its returned value,
	// keeping invitation deadlines and review evaluation tied to the same
	// service-selected source.
	if s.options != nil && s.options.Now != nil {
		return s.options.Now()
	}
	return time.Now()
}
