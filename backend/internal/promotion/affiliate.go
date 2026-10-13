package promotion

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// An invitation binds one account to another account by user ID, so later
// invitation-code edits keep that relationship intact. Rebate accrual applies
// the inviter's custom percentage before the global percentage and checks the
// invitee profile's eligible lifetime before calculating a rounded grant.
//
// The inviter lock covers the cumulative rebate read and the next accrual,
// allowing concurrent purchases to share the same lifetime-cap allowance.
// Source order IDs identify the originating purchase, while payment's audit
// transaction supplies order replay protection. Detail reads attempt
// persistent thaw before loading quota, and transfer delegates the quota,
// balance, and ledger transaction to storage before requesting cache
// invalidation.

// Affiliate display size and invitation-code length bounds govern the user
// detail response and accepted normalized code text.
const (
	affiliateInviteesLimit = 100 // maximum user-page invitee rows
	// AffiliateCodeMinLength and AffiliateCodeMaxLength bound both generated and custom codes.
	AffiliateCodeMinLength = 4  // minimum accepted code bytes
	AffiliateCodeMaxLength = 32 // maximum accepted code bytes
)

// Affiliate errors distinguish missing profiles, invalid or claimed codes,
// competing inviter binding, and empty transferable quota.
var (
	ErrAffiliateProfileNotFound = infraerrors.NotFound("AFFILIATE_PROFILE_NOT_FOUND", "affiliate profile not found")
	ErrAffiliateCodeInvalid     = infraerrors.BadRequest("AFFILIATE_CODE_INVALID", "invalid affiliate code")
	ErrAffiliateCodeTaken       = infraerrors.Conflict("AFFILIATE_CODE_TAKEN", "affiliate code already in use")
	ErrAffiliateAlreadyBound    = infraerrors.Conflict("AFFILIATE_ALREADY_BOUND", "affiliate inviter already bound")
	ErrAffiliateQuotaEmpty      = infraerrors.BadRequest("AFFILIATE_QUOTA_EMPTY", "no affiliate quota available to transfer")

	// affiliateCodeValidChar holds the shared accepted-byte table used by
	// normalized invitation-code validation across service calls.
	affiliateCodeValidChar = buildAffiliateCodeValidChar()
)

// AffiliateSummary is the saved invitation profile and its current quota
// totals.
type AffiliateSummary struct {
	// profile owner account identifier
	UserID int64 `json:"user_id"`
	// current invitation code text
	AffCode string `json:"aff_code"`
	// administrator assigned code flag
	AffCodeCustom bool `json:"aff_code_custom"`
	// AffRebateRatePercent overrides the global percentage when nonnil and
	// finite. resolveRebateRatePercent treats a nonfinite value as a request for
	// global policy.
	// optional inviter percentage override
	AffRebateRatePercent *float64 `json:"aff_rebate_rate_percent,omitempty"`
	// InviterID identifies the user earning this account's rebates. A nil stored
	// relationship can be claimed, including when deletion has cleared an
	// earlier inviter; changing AffCode leaves the user ID relationship intact.
	// optional bound inviter identifier
	InviterID *int64 `json:"inviter_id,omitempty"`
	// accumulated invited account count
	AffCount int `json:"aff_count"`
	// AffQuota and AffFrozenQuota split current transferable and waiting funds.
	// AffHistoryQuota accumulates grants across both states and remains
	// available for statistics after quota transfers.
	// available reasoning point quota
	AffQuota float64 `json:"aff_quota"`
	// frozen reasoning point quota
	AffFrozenQuota float64 `json:"aff_frozen_quota"`
	// cumulative reasoning point grants
	AffHistoryQuota float64 `json:"aff_history_quota"`
	// CreatedAt is affiliate profile creation time. AccrueInviteRebateForOrder
	// measures the configured rebate duration from this timestamp, rather than
	// the date an inviter is bound.
	// affiliate profile creation timestamp
	CreatedAt time.Time `json:"created_at"`
	// latest profile mutation timestamp
	UpdatedAt time.Time `json:"updated_at"`
}

// AffiliateInvitee carries one referred account and its cumulative rebate
// contribution.
type AffiliateInvitee struct {
	UserID int64 `json:"user_id"` // referred account identifier
	// Email contains the storage email on repository results and a masked value
	// on user-page results. listInvitees changes this field in the returned
	// slice.
	Email    string `json:"email"`    // referred account email text
	Username string `json:"username"` // referred account display name
	// CreatedAt points to the invitee profile creation time returned by storage.
	// The user detail response retains that timestamp pointer.
	// affiliate profile creation timestamp
	CreatedAt *time.Time `json:"created_at,omitempty"`
	// cumulative pair reasoning points
	TotalRebate float64 `json:"total_rebate"`
}

// AffiliateDetail is the user-page profile, effective inviter rate, and
// masked invitee list.
type AffiliateDetail struct {
	UserID  int64  `json:"user_id"`  // profile owner account identifier
	AffCode string `json:"aff_code"` // current invitation code text
	// InviterID retains the selected profile's optional inviter pointer. Nil
	// omits the relationship from JSON, while later rebate attribution uses the
	// referenced account ID.
	// optional bound inviter identifier
	InviterID *int64 `json:"inviter_id,omitempty"`
	// accumulated invited account count
	AffCount int `json:"aff_count"`
	// AffQuota and AffFrozenQuota report the stored current split after the
	// detail read's thaw attempt. AffHistoryQuota reports lifetime grants, so it
	// is not the transferable amount.
	// available reasoning point quota
	AffQuota float64 `json:"aff_quota"`
	// frozen reasoning point quota
	AffFrozenQuota float64 `json:"aff_frozen_quota"`
	// cumulative reasoning point grants
	AffHistoryQuota float64 `json:"aff_history_quota"`
	// EffectiveRebateRatePercent is the viewer's percentage as an inviter. A
	// usable profile override precedes the live global setting for this
	// displayed value.
	// effective inviter rebate percentage
	EffectiveRebateRatePercent float64 `json:"effective_rebate_rate_percent"`
	// Invitees contains up to the service's display limit of recent referred
	// accounts. The slice comes from storage with its email values masked in
	// place.
	// masked recent invitee records
	Invitees []AffiliateInvitee `json:"invitees"`
}

// AffiliateRepository persists invitation profiles, rebate quota, and their
// audit records.
//
// Affiliate mutations reuse a transaction carried by context when present.
// The outer owner completes that transaction; callers keep related locked
// reads and writes on the supplied transaction context.
type AffiliateRepository interface {
	// WithLockedInviter runs fn with the inviter's profile locked in a
	// transaction.
	//
	// The callback uses the supplied context for cumulative reads and accruals.
	// An outer transaction owner keeps the lock until completion.
	WithLockedInviter(context.Context, int64, func(context.Context) error) error
	// EnsureUserAffiliate returns the existing profile or attempts creation with
	// a generated code checked for uniqueness.
	//
	// A context transaction is reused when present; missing user identities or
	// persistence failures return errors.
	EnsureUserAffiliate(ctx context.Context, userID int64) (*AffiliateSummary, error)
	// GetAffiliateByCode returns the profile owning the trimmed uppercase
	// invitation code.
	//
	// An absent code returns ErrAffiliateProfileNotFound; lookup can use the
	// transaction carried by the supplied context.
	GetAffiliateByCode(ctx context.Context, code string) (*AffiliateSummary, error)
	// BindInviter claims the user profile's unset inviter and increments the
	// inviter's count together.
	//
	// It returns false when another binding already occupies that field. Both
	// profiles are ensured before the conditional write, and a successful claim
	// precedes the count increment in one transaction. The caller checks
	// self-invitation and code validity before calling; repository transaction
	// participation leaves completion with any outer owner.
	BindInviter(ctx context.Context, userID, inviterID int64) (bool, error)
	// AccrueQuota adds a positive rebate to the inviter's quota and writes its
	// source ledger entry together.
	//
	// Positive freezeHours selects frozen quota; other values select available
	// quota, while lifetime history grows in either case. Missing profiles or
	// nonpositive amounts return false. The caller coordinates cumulative-cap
	// reads and order replays; sourceOrderID supplies an audit association
	// rather than a unique accrual claim. Callers provide finite quota amounts.
	AccrueQuota(ctx context.Context, inviterID, inviteeUserID int64, amount float64, freezeHours int, sourceOrderID *int64) (bool, error)
	// GetAccruedRebateFromInvitee returns the sum of accrual ledger amounts for
	// one inviter and invitee pair.
	//
	// Use the locked transaction context when that total controls a subsequent
	// lifetime-cap grant.
	GetAccruedRebateFromInvitee(ctx context.Context, inviterID, inviteeUserID int64) (float64, error)
	// ThawFrozenQuota persistently moves matured ledger amounts from frozen
	// quota into available quota.
	//
	// It clears each selected deadline before updating the profile totals in the
	// same transaction. Repeated calls therefore find only deadlines still
	// present and due. The return value is the amount moved during this
	// invocation; history remains unchanged, and an outer transaction owner
	// retains responsibility for completing the joined writes.
	ThawFrozenQuota(ctx context.Context, userID int64) (float64, error)
	// TransferQuotaToBalance transfers the user's entire positive available
	// quota and returns the amount and observed balance.
	//
	// It thaws due quota before claiming the available profile funds, credits
	// billing through the same transaction, and records the resulting snapshots
	// in the transfer ledger. Empty quota returns ErrAffiliateQuotaEmpty. Joined
	// transactions leave commit and rollback to the outer owner, while an owned
	// transaction completes before returning success.
	TransferQuotaToBalance(ctx context.Context, userID int64) (float64, float64, error)
	// ListInvitees returns recent referred accounts and their cumulative
	// contributions for one inviter.
	//
	// Nonpositive limits select one hundred rows. Repository email values are
	// unmasked; user-page service callers apply masking afterward.
	ListInvitees(ctx context.Context, inviterID int64, limit int) ([]AffiliateInvitee, error)

	// UpdateUserAffCode assigns normalized custom code text and its
	// administrator marker after ensuring a profile exists.
	//
	// Profile creation and code replacement participate in one transaction, so a
	// failed final code claim can be rolled back with a newly created profile. A
	// uniqueness conflict returns ErrAffiliateCodeTaken. Callers validate
	// accepted code characters and length beforehand, and an outer transaction
	// owner controls completion when supplied.
	UpdateUserAffCode(ctx context.Context, userID int64, newCode string) error
	// ResetUserAffCode attempts a generated replacement and clears the profile's
	// custom-code marker.
	//
	// It ensures the profile before the replacement write in one transaction and
	// bounds retry attempts on recognized code collisions. The same transaction
	// is reused, so a collision does not create savepoint recovery. Existing
	// relationships keep the account ID as inviter identity, and callers with an
	// outer transaction own completion of its joined writes.
	ResetUserAffCode(ctx context.Context, userID int64) (string, error)
	// SetUserRebateRate stores the supplied inviter percentage after ensuring
	// the profile exists.
	//
	// Nil clears the override while a supplied zero remains a custom rate that
	// disables grants. Profile creation and the override write participate in
	// one transaction; an outer owner controls completion when supplied. Callers
	// validate finite bounds beforehand, since persistence encodes the accepted
	// value rather than repeating that validation. Nonpositive user IDs are
	// rejected.
	SetUserRebateRate(ctx context.Context, userID int64, ratePercent *float64) error
	// BatchSetUserRebateRate stores one supplied rate for selected users in a
	// transaction.
	//
	// Nil clears overrides and an empty list returns immediately. Positive
	// target profiles are ensured before the common rate update, so creation and
	// replacement can be rolled back together on an owned transaction failure.
	// Callers validate the percentage and normalize target identities; a
	// supplied outer transaction retains commit and rollback ownership.
	BatchSetUserRebateRate(ctx context.Context, userIDs []int64, ratePercent *float64) error
	// ListUsersWithCustomSettings returns accounts selected by a custom code or
	// saved rate and their matching total.
	//
	// A nil returned percentage identifies a code-only account with no rate
	// override; a nonnil zero retains an explicitly disabled custom rate.
	// Pagination is normalized by storage, and results carry the saved override
	// for administrator editing. Counting and paging can observe different
	// committed changes because they use separate queries on the selected
	// client. The total describes a page query's observation for the response.
	ListUsersWithCustomSettings(ctx context.Context, filter AffiliateAdminFilter) ([]AffiliateAdminEntry, int64, error)
	// ListAffiliateInviteRecords returns paged inviter relationships and their
	// accumulated ledger grants.
	//
	// Callers supply normalized pagination; search and inclusive date bounds
	// apply to the same matching rows used for counting.
	ListAffiliateInviteRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateInviteRecord, int64, error)
	// ListAffiliateRebateRecords returns paged accrual entries that still join
	// to a source order and both accounts, with a matching total.
	//
	// The ledger supplies the grant amount, while the order supplies its list
	// price and charged money. Subscription list currency prefers the saved
	// plan snapshot, current plan, then USD; charged currency uses the provider
	// snapshot and defaults to CNY. Callers normalize pagination. Count and page
	// are separate queries, so their observations can reflect intervening
	// committed changes.
	ListAffiliateRebateRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateRebateRecord, int64, error)
	// ListAffiliateTransferRecords returns paged transfer receipts, their saved
	// fund observations, and a matching total.
	//
	// Each nullable amount retains absence separately from a stored zero.
	// SnapshotAvailable requires balance and all three quota observations, so
	// partial older receipts remain distinguishable from complete snapshots.
	// Current account amounts cannot establish a missing historical observation.
	// Callers supply normalized filters; count and page use separate queries
	// whose results can reflect intervening committed changes.
	ListAffiliateTransferRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateTransferRecord, int64, error)
	// GetAffiliateUserOverview returns one existing profile's counts, saved
	// custom-rate marker, and computed available-quota observation.
	//
	// The marker keeps an absent override distinct from an explicit saved zero,
	// letting the administrator service select global policy only for absence.
	// Due frozen ledger amounts augment the displayed quota, and this query
	// leaves persistent deadlines untouched. Callers use the transfer operation
	// for a funds change rather than treating this read as a transfer receipt.
	GetAffiliateUserOverview(ctx context.Context, userID int64) (*AffiliateUserOverview, error)
}

// AffiliateAdminFilter selects the custom-settings user list and requested
// page.
type AffiliateAdminFilter struct {
	Search   string // email or username search
	Page     int    // requested one-based page number
	PageSize int    // requested rows per page
}

// AffiliateAdminEntry identifies an account with a custom invitation code or
// rate.
type AffiliateAdminEntry struct {
	// profile owner account identifier
	UserID   int64  `json:"user_id"`
	Email    string `json:"email"`    // account email address
	Username string `json:"username"` // account display name
	// current invitation code text
	AffCode string `json:"aff_code"`
	// administrator assigned code flag
	AffCodeCustom bool `json:"aff_code_custom"`
	// AffRebateRatePercent exposes the saved custom percentage. Nil means the
	// account is selected for its custom code and has no rate override.
	// optional inviter percentage override
	AffRebateRatePercent *float64 `json:"aff_rebate_rate_percent,omitempty"`
	// accumulated invited account count
	AffCount int `json:"aff_count"`
}

// AffiliateRecordFilter carries record search, inclusive time bounds,
// pagination, and sorting.
type AffiliateRecordFilter struct {
	Search   string     // record-specific text search
	Page     int        // requested one-based page number
	PageSize int        // requested rows per page
	StartAt  *time.Time // inclusive earliest record time
	EndAt    *time.Time // inclusive latest record time
	// SortBy names a key accepted by the selected record query. Unsupported keys
	// choose that query's timestamp sort, keeping user text out of SQL column
	// expressions.
	SortBy   string // requested allowlisted sort key
	SortDesc bool   // descending record order flag
}

// AffiliateInviteRecord describes an inviter relationship and its accumulated
// grant amount.
type AffiliateInviteRecord struct {
	// inviting account identifier
	InviterID int64 `json:"inviter_id"`
	// inviting account email
	InviterEmail string `json:"inviter_email"`
	// inviting account display name
	InviterUsername string `json:"inviter_username"`
	// referred account identifier
	InviteeID int64 `json:"invitee_id"`
	// referred account email
	InviteeEmail string `json:"invitee_email"`
	// referred account display name
	InviteeUsername string `json:"invitee_username"`
	// inviter current invitation code
	AffCode string `json:"aff_code"`
	// cumulative pair reasoning points
	TotalRebate float64 `json:"total_rebate"`
	// invitee profile creation timestamp
	CreatedAt time.Time `json:"created_at"`
}

// AffiliateRebateRecord combines an accrual ledger amount with its source
// payment order.
type AffiliateRebateRecord struct {
	// source payment order identifier
	OrderID int64 `json:"order_id"`
	// merchant payment order number
	OutTradeNo string `json:"out_trade_no"`
	// credited inviter account identifier
	InviterID int64 `json:"inviter_id"`
	// credited inviter account email
	InviterEmail string `json:"inviter_email"`
	// credited inviter display name
	InviterUsername string `json:"inviter_username"`
	// purchasing invitee account identifier
	InviteeID int64 `json:"invitee_id"`
	// purchasing invitee account email
	InviteeEmail string `json:"invitee_email"`
	// purchasing invitee display name
	InviteeUsername string `json:"invitee_username"`
	// order entitlement or price amount
	OrderAmount float64 `json:"order_amount"`
	// provider charged order amount
	PayAmount float64 `json:"pay_amount"`
	// balance or subscription order
	OrderType string `json:"order_type"`
	// OrderCurrency is set for subscription orders from plan snapshot, current
	// plan, then USD. Balance orders use an empty list-price currency.
	// subscription list-price currency
	OrderCurrency string `json:"order_currency"`
	// PayCurrency uses the source order's provider snapshot currency and
	// defaults to CNY when absent. It describes charged money separately from
	// granted quota.
	// payment provider charged currency
	PayCurrency string `json:"pay_currency"`
	// ledger granted reasoning points
	RebateAmount float64 `json:"rebate_amount"`
	// saved payment method identifier
	PaymentType string `json:"payment_type"`
	// source order current status
	OrderStatus string `json:"order_status"`
	// accrual ledger creation timestamp
	CreatedAt time.Time `json:"created_at"`
}

// AffiliateTransferRecord carries a transfer receipt and its optional stored
// fund snapshots.
type AffiliateTransferRecord struct {
	LedgerID  int64  `json:"ledger_id"`  // transfer ledger row identifier
	UserID    int64  `json:"user_id"`    // credited account identifier
	UserEmail string `json:"user_email"` // credited account email
	Username  string `json:"username"`   // credited account display name
	// transferred reasoning point quota
	Amount float64 `json:"amount"`
	// BalanceAfter, AvailableQuotaAfter, FrozenQuotaAfter, and HistoryQuotaAfter
	// retain nullable ledger snapshots. SnapshotAvailable is true only when all
	// four exist; older receipts can expose partial snapshots.
	// stored post-transfer balance
	BalanceAfter *float64 `json:"balance_after,omitempty"`
	// stored post-transfer available quota
	AvailableQuotaAfter *float64 `json:"available_quota_after,omitempty"`
	// stored post-transfer frozen quota
	FrozenQuotaAfter *float64 `json:"frozen_quota_after,omitempty"`
	// stored post-transfer lifetime quota
	HistoryQuotaAfter *float64 `json:"history_quota_after,omitempty"`
	// all stored snapshots present
	SnapshotAvailable bool `json:"snapshot_available"`
	// legacy internal balance placeholder
	CurrentBalance float64 `json:"-"`
	// legacy internal available placeholder
	RemainingQuota float64 `json:"-"`
	// legacy internal frozen placeholder
	FrozenQuota float64 `json:"-"`
	// legacy internal history placeholder
	HistoryQuota float64 `json:"-"`
	// transfer ledger creation timestamp
	CreatedAt time.Time `json:"created_at"`
}

// AffiliateUserOverview combines invitation counts, effective rate, and
// available rebate observations.
type AffiliateUserOverview struct {
	UserID   int64  `json:"user_id"`  // profile owner account identifier
	Email    string `json:"email"`    // account email address
	Username string `json:"username"` // account display name
	AffCode  string `json:"aff_code"` // current invitation code text
	// RebateRatePercent and RebateRateCustom distinguish a saved override from
	// global policy. Storage supplies the custom percentage only when present;
	// AdminGetUserOverview substitutes the live global value when the marker is
	// false.
	// selected inviter rebate percentage
	RebateRatePercent float64 `json:"rebate_rate_percent"`
	// saved override presence flag
	RebateRateCustom bool `json:"-"`
	// accumulated invited account count
	InvitedCount int `json:"invited_count"`
	// distinct ledger contributing invitees
	RebatedInviteeCount int `json:"rebated_invitee_count"`
	// AvailableQuota includes stored available funds and currently matured
	// frozen ledger amounts. GetAffiliateUserOverview reports that observation
	// without updating persistent frozen deadlines.
	// available reasoning point observation
	AvailableQuota float64 `json:"available_quota"`
	// cumulative reasoning point grants
	HistoryQuota float64 `json:"history_quota"`
}

// AffiliateService applies invitation and rebate rules through persistence
// and live settings.
type AffiliateService struct {
	// repo, settingService, authCacheInvalidator, and billingCacheService are
	// retained dependencies shared across calls. runtime supplies callback
	// state; their owners coordinate lifetime and concurrent use.
	//
	// repo supplies profile and ledger operations. An absent dependency makes
	// guarded operations unavailable, while optional rebate accrual treats
	// missing persistence as a skipped grant.
	// invitation and rebate persistence
	repo AffiliateRepository
	// settingService supplies current policy. Its absence disables IsEnabled and
	// selects the default percentage when detail or rate resolution needs global
	// policy.
	settingService SettingsReader // live rebate policy reader
	// authCacheInvalidator is optional. An absent interface dependency skips
	// authentication eviction after transfers while a configured balance
	// invalidator can still run.
	// API Key authentication invalidator
	authCacheInvalidator AuthCacheInvalidator
	// billingCacheService is optional. An absent interface dependency skips
	// balance eviction, and returned errors from a present dependency reach
	// runtime.Warn when supplied.
	// cached balance invalidation dependency
	billingCacheService BalanceCache
	runtime             Runtime // clock and warning callbacks
}

// buildAffiliateCodeValidChar returns the accepted ASCII-byte table for
// uppercase invitation-code validation.
func buildAffiliateCodeValidChar() [256]bool {
	var tbl [256]bool
	for c := byte('A'); c <= 'Z'; c++ {
		tbl[c] = true
	}
	for c := byte('0'); c <= '9'; c++ {
		tbl[c] = true
	}
	tbl['_'] = true
	tbl['-'] = true
	return tbl
}

// isValidAffiliateCodeFormat reports whether code meets invitation-code byte
// length and character rules.
//
// Callers trim and uppercase input first; validation itself accepts only the
// supplied normalized representation.
func isValidAffiliateCodeFormat(code string) bool {
	// The shared byte bounds govern both administrator code edits and signup
	// inviter binding.
	if len(code) < AffiliateCodeMinLength || len(code) > AffiliateCodeMaxLength {
		return false
	}
	for i := range len(code) {
		if !affiliateCodeValidChar[code[i]] {
			return false
		}
	}
	return true
}

// NewAffiliateService retains repository, policy, invalidators, and runtime
// callbacks for invitation and rebate operations.
//
// An absent clock selects time.Now. Dependencies and mutable callback state
// remain shared with their owners.
func NewAffiliateService(repo AffiliateRepository, settingService SettingsReader, authCacheInvalidator AuthCacheInvalidator, billingCacheService BalanceCache, runtime Runtime) *AffiliateService {
	if runtime.Now == nil {
		runtime.Now = time.Now
	}
	return &AffiliateService{
		repo:                 repo,
		settingService:       settingService,
		authCacheInvalidator: authCacheInvalidator,
		billingCacheService:  billingCacheService,
		runtime:              runtime,
	}
}

// IsEnabled reports whether live settings enable invitation binding and
// rebate accrual.
//
// A nil service or missing settings reader returns the disabled default,
// letting optional promotion callers skip work.
func (s *AffiliateService) IsEnabled(ctx context.Context) bool {
	if s == nil || s.settingService == nil {
		return AffiliateEnabledDefault
	}
	return s.settingService.IsAffiliateEnabled(ctx)
}

// EnsureUserAffiliate returns or creates the requested user's invitation
// profile through storage.
//
// Nonpositive IDs return a bad request, and a missing service or repository
// returns service unavailable.
func (s *AffiliateService) EnsureUserAffiliate(ctx context.Context, userID int64) (*AffiliateSummary, error) {
	if userID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_USER", "invalid user")
	}
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	return s.repo.EnsureUserAffiliate(ctx, userID)
}

// GetAffiliateDetail returns stored quota, effective inviter percentage, and
// recent invitees with masked emails.
//
// It attempts persistent thaw before reading the profile and continues after
// a thaw error; later profile or invitee query errors are returned.
func (s *AffiliateService) GetAffiliateDetail(ctx context.Context, userID int64) (*AffiliateDetail, error) {
	// Try to persist matured quota before the profile read. A thaw failure is
	// discarded, so detail still exposes the subsequent stored observation.
	if s != nil && s.repo != nil {
		_, _ = s.repo.ThawFrozenQuota(ctx, userID)
	}

	summary, err := s.EnsureUserAffiliate(ctx, userID)
	if err != nil {
		return nil, err
	}
	invitees, err := s.listInvitees(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &AffiliateDetail{
		UserID:                     summary.UserID,
		AffCode:                    summary.AffCode,
		InviterID:                  summary.InviterID,
		AffCount:                   summary.AffCount,
		AffQuota:                   summary.AffQuota,
		AffFrozenQuota:             summary.AffFrozenQuota,
		AffHistoryQuota:            summary.AffHistoryQuota,
		EffectiveRebateRatePercent: s.resolveRebateRatePercent(ctx, summary),
		Invitees:                   invitees,
	}, nil
}

// BindInviterByCode normalizes the optional code and requests an inviter for
// an unset relationship.
//
// Empty input or disabled affiliates returns success; an existing inviter is
// retained. Invalid, absent, or self-owned codes are rejected, and a lost
// concurrent claim returns ErrAffiliateAlreadyBound.
func (s *AffiliateService) BindInviterByCode(ctx context.Context, userID int64, rawCode string) error {
	code := strings.ToUpper(strings.TrimSpace(rawCode))
	if code == "" {
		return nil
	}
	if s == nil || s.repo == nil {
		return infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	// When affiliates are disabled, ignore the registration code so registration can proceed.
	if !s.IsEnabled(ctx) {
		return nil
	}
	if !isValidAffiliateCodeFormat(code) {
		return ErrAffiliateCodeInvalid
	}

	selfSummary, err := s.repo.EnsureUserAffiliate(ctx, userID)
	if err != nil {
		return err
	}
	if selfSummary.InviterID != nil {
		return nil
	}

	inviterSummary, err := s.repo.GetAffiliateByCode(ctx, code)
	if err != nil {
		if errors.Is(err, ErrAffiliateProfileNotFound) {
			// A missing profile for the submitted code becomes the public invalid-code
			// error, keeping inviter lookup failures distinct from infrastructure
			// errors.
			return ErrAffiliateCodeInvalid
		}
		return err
	}
	if inviterSummary == nil || inviterSummary.UserID <= 0 || inviterSummary.UserID == userID {
		return ErrAffiliateCodeInvalid
	}

	bound, err := s.repo.BindInviter(ctx, userID, inviterSummary.UserID)
	if err != nil {
		return err
	}
	if !bound {
		return ErrAffiliateAlreadyBound
	}
	return nil
}

// AccrueInviteRebate requests a rebate from the invitee's purchased reasoning
// points without a source-order association.
//
// It delegates eligibility, rounding, cap locking, and persistence to
// AccrueInviteRebateForOrder; callers coordinate repeated grants.
func (s *AffiliateService) AccrueInviteRebate(ctx context.Context, inviteeUserID int64, purchasedPoints float64) (float64, error) {
	return s.AccrueInviteRebateForOrder(ctx, inviteeUserID, purchasedPoints, nil)
}

// AccrueInviteRebateForOrder computes a purchase grant and returns the amount
// reported as applied, with an optional source-order association.
//
// Missing dependencies, invalid inputs, disabled policy, absent inviter,
// expired profile lifetime, and exhausted allowance can return zero
// successfully. The inviter's usable custom rate precedes the global rate;
// the calculated grant and remaining allowance are rounded to eight places.
//
// The inviter lock encloses the pair's prior accrual total and the new quota
// write, so competing purchases observe serialized allowance consumption.
// Source order association supplies audit context; payment's separate audit
// transaction coordinates replay protection, and other callers coordinate
// duplicate requests themselves. An outer transaction keeps the returned
// amount provisional until its owner commits; callers discard that amount
// when an error is returned.
// @project-doc docs/domains/promotions_and_affiliates.md#affiliate_rebate_lifecycle
func (s *AffiliateService) AccrueInviteRebateForOrder(ctx context.Context, inviteeUserID int64, purchasedPoints float64, sourceOrderID *int64) (float64, error) {
	// Optional rebate callers treat unavailable dependencies as a skipped grant,
	// allowing the surrounding entitlement operation to continue successfully.
	if s == nil || s.repo == nil {
		return 0, nil
	}
	if inviteeUserID <= 0 || purchasedPoints <= 0 || math.IsNaN(purchasedPoints) || math.IsInf(purchasedPoints, 0) {
		return 0, nil
	}
	// Disabled affiliates produce no rebate for new recharges.
	if !s.IsEnabled(ctx) {
		return 0, nil
	}

	inviteeSummary, err := s.repo.EnsureUserAffiliate(ctx, inviteeUserID)
	if err != nil {
		return 0, err
	}
	if inviteeSummary.InviterID == nil || *inviteeSummary.InviterID <= 0 {
		return 0, nil
	}

	// Load the earning inviter's profile so its override determines this
	// purchase's rate; the invitee's own override applies when they earn from
	// referrals.
	inviterSummary, err := s.repo.EnsureUserAffiliate(ctx, *inviteeSummary.InviterID)
	if err != nil {
		return 0, err
	}
	// Eligibility uses invitee profile creation plus calendar days, so changing
	// the inviter or invitation code cannot restart this lifetime.
	if s.settingService != nil {
		if durationDays := s.settingService.GetAffiliateRebateDurationDays(ctx); durationDays > 0 {
			if s.runtime.Now().After(inviteeSummary.CreatedAt.AddDate(0, 0, durationDays)) {
				return 0, nil
			}
		}
	}

	rebateRatePercent := s.resolveRebateRatePercent(ctx, inviterSummary)
	// Convert percentage to a fraction and round the grant to the eight-place
	// quota representation before comparing remaining allowance.
	rebate := roundTo(purchasedPoints*(rebateRatePercent/100), 8)
	if rebate <= 0 {
		return 0, nil
	}

	var credited float64
	// Hold the inviter lock across cumulative pair lookup and AccrueQuota so
	// concurrent purchases cannot both use an earlier allowance observation.
	err = s.repo.WithLockedInviter(ctx, *inviteeSummary.InviterID, func(ctx context.Context) error {
		if s.settingService != nil {
			if perInviteeCap := s.settingService.GetAffiliateRebatePerInviteeCap(ctx); perInviteeCap > 0 {
				existing, err := s.repo.GetAccruedRebateFromInvitee(ctx, *inviteeSummary.InviterID, inviteeUserID)
				if err != nil {
					return err
				}
				if existing >= perInviteeCap {
					return nil
				}
				// Restrict this grant to the remaining cumulative allowance, rounding
				// that remainder with the same precision used for quota amounts.
				if remaining := perInviteeCap - existing; rebate > remaining {
					rebate = roundTo(remaining, 8)
				}
			}
		}

		var freezeHours int
		if s.settingService != nil {
			freezeHours = s.settingService.GetAffiliateRebateFreezeHours(ctx)
		}

		applied, err := s.repo.AccrueQuota(ctx, *inviteeSummary.InviterID, inviteeUserID, rebate, freezeHours, sourceOrderID)
		if err != nil {
			return err
		}
		if !applied {
			return nil
		}
		credited = rebate
		return nil
	})
	return credited, err
}

// resolveRebateRatePercent returns the inviter's bounded custom percentage
// when its pointer holds a finite value.
//
// Missing or nonfinite overrides select the global policy, while a custom
// zero disables that inviter's grant.
func (s *AffiliateService) resolveRebateRatePercent(ctx context.Context, inviter *AffiliateSummary) float64 {
	if inviter != nil && inviter.AffRebateRatePercent != nil {
		v := *inviter.AffRebateRatePercent
		if math.IsNaN(v) || math.IsInf(v, 0) {
			// A nonfinite saved override falls back to global policy so invalid custom
			// arithmetic does not determine the purchase's grant.
			return s.globalRebateRatePercent(ctx)
		}
		return ClampRebateRate(v)
	}
	// With no supplied custom percentage, use the live global setting as the
	// inviter's applicable purchase rebate rate.
	return s.globalRebateRatePercent(ctx)
}

// globalRebateRatePercent returns the live global rebate percentage through
// the settings reader.
//
// A nil service or missing reader selects the default percentage; configured
// readers provide the validated value.
func (s *AffiliateService) globalRebateRatePercent(ctx context.Context) float64 {
	if s == nil || s.settingService == nil {
		// Optional policy readers can be absent in callers and fixtures; supply the
		// default percentage for their rate decision.
		return AffiliateRebateRateDefault
	}
	return s.settingService.GetAffiliateRebateRatePercent(ctx)
}

// TransferAffiliateQuota returns the transferred quota and resulting balance
// from the repository.
//
// Positive transfers request authentication and balance cache invalidation
// after repository success. With an outer transaction, callers coordinate its
// later completion.
func (s *AffiliateService) TransferAffiliateQuota(ctx context.Context, userID int64) (float64, float64, error) {
	if s == nil || s.repo == nil {
		return 0, 0, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}

	transferred, balance, err := s.repo.TransferQuotaToBalance(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	if transferred > 0 {
		s.invalidateAffiliateCaches(ctx, userID)
	}
	return transferred, balance, nil
}

// listInvitees returns at most the user-page limit of referred accounts and
// masks each returned email.
//
// The repository slice is updated in place, so implementations provide
// records the service may modify.
func (s *AffiliateService) listInvitees(ctx context.Context, inviterID int64) ([]AffiliateInvitee, error) {
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	invitees, err := s.repo.ListInvitees(ctx, inviterID, affiliateInviteesLimit)
	if err != nil {
		return nil, err
	}
	for i := range invitees {
		invitees[i].Email = maskEmail(invitees[i].Email)
	}
	return invitees, nil
}

// roundTo rounds v to scale decimal places using math.Round's
// half-away-from-zero rule.
//
// Rebate callers use finite purchased points and an eight-place scale;
// callers control overflow of the scaling calculation.
func roundTo(v float64, scale int) float64 {
	factor := math.Pow10(scale)
	// Rebate callers request eight places to match persistent quota precision.
	return math.Round(v*factor) / factor
}

// maskEmail masks trimmed address text while retaining the first local and
// domain runes.
//
// A usable final domain dot preserves its suffix. Empty text stays empty;
// missing or empty address parts return three asterisks.
func maskEmail(email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return ""
	}
	at := strings.Index(email, "@")
	if at <= 0 || at >= len(email)-1 {
		return "***"
	}

	local := email[:at]
	domain := email[at+1:]
	dot := strings.LastIndex(domain, ".")

	maskedLocal := maskSegment(local)
	if dot <= 0 || dot >= len(domain)-1 {
		return maskedLocal + "@" + maskSegment(domain)
	}

	domainName := domain[:dot]
	tld := domain[dot:]
	return maskedLocal + "@" + maskSegment(domainName) + tld
}

// maskSegment returns the first rune followed by three asterisks for nonempty
// text.
//
// Empty text returns three asterisks, so callers receive a mask even when a
// segment has no rune.
func maskSegment(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return "***"
	}
	if len(r) == 1 {
		return string(r[0]) + "***"
	}
	return string(r[0]) + "***"
}

// invalidateAffiliateCaches requests authentication and balance eviction with
// the caller's context.
//
// Missing invalidators are skipped; returned balance-cache errors reach the
// optional warning callback and do not alter the returned transfer result.
func (s *AffiliateService) invalidateAffiliateCaches(ctx context.Context, userID int64) {
	// Authentication entries include funding observations, so request their
	// eviction together with the balance cache after a positive repository
	// transfer.
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if s.billingCacheService != nil {
		// A failed balance eviction reaches Warn when configured. With no
		// warning callback, its error is discarded after repository success
		// and the transfer result remains unchanged.
		if err := s.billingCacheService.InvalidateUserBalance(ctx, userID); err != nil {
			if s.runtime.Warn != nil {
				s.runtime.Warn(userID, err)
			}
		}
	}
}

// Admin operations on custom affiliate settings.

// validateExclusiveRate accepts nil or a finite percentage between zero and
// one hundred.
//
// Nil requests removal of a saved override, while rejected numbers return a
// business validation error.
func validateExclusiveRate(ratePercent *float64) error {
	if ratePercent == nil {
		return nil
	}
	v := *ratePercent
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return infraerrors.BadRequest("INVALID_RATE", "invalid rebate rate")
	}
	// Administrator edits reject out-of-range overrides, while runtime policy
	// parsing clamps percentages to the supported endpoints.
	if v < AffiliateRebateRateMin || v > AffiliateRebateRateMax {
		return infraerrors.BadRequest("INVALID_RATE", "rebate rate out of range")
	}
	return nil
}

// AdminUpdateUserAffCode stores an administrator-supplied code after
// trimming, uppercasing, and validating its format.
//
// Missing storage returns service unavailable; uniqueness and user-presence
// errors come from the repository.
func (s *AffiliateService) AdminUpdateUserAffCode(ctx context.Context, userID int64, rawCode string) error {
	if s == nil || s.repo == nil {
		return infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	code := strings.ToUpper(strings.TrimSpace(rawCode))
	if !isValidAffiliateCodeFormat(code) {
		return ErrAffiliateCodeInvalid
	}
	return s.repo.UpdateUserAffCode(ctx, userID, code)
}

// AdminResetUserAffCode requests a generated replacement invitation code and
// clears its custom marker.
//
// Missing storage returns service unavailable; existing relationships
// continue to identify the inviter by user ID.
func (s *AffiliateService) AdminResetUserAffCode(ctx context.Context, userID int64) (string, error) {
	if s == nil || s.repo == nil {
		return "", infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	return s.repo.ResetUserAffCode(ctx, userID)
}

// AdminSetUserRebateRate validates and stores a user-specific inviter
// percentage through the repository.
//
// Nil clears the override; nonfinite or out-of-range values are rejected
// before storage is invoked.
func (s *AffiliateService) AdminSetUserRebateRate(ctx context.Context, userID int64, ratePercent *float64) error {
	if s == nil || s.repo == nil {
		return infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	if err := validateExclusiveRate(ratePercent); err != nil {
		return err
	}
	return s.repo.SetUserRebateRate(ctx, userID, ratePercent)
}

// AdminBatchSetUserRebateRate validates one percentage and applies it to
// positive submitted user IDs.
//
// Nil clears overrides; nonpositive IDs are removed, duplicates remain, and
// an empty cleaned list returns success.
func (s *AffiliateService) AdminBatchSetUserRebateRate(ctx context.Context, userIDs []int64, ratePercent *float64) error {
	if s == nil || s.repo == nil {
		return infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	if err := validateExclusiveRate(ratePercent); err != nil {
		return err
	}
	cleaned := make([]int64, 0, len(userIDs))
	for _, uid := range userIDs {
		if uid > 0 {
			cleaned = append(cleaned, uid)
		}
	}
	if len(cleaned) == 0 {
		return nil
	}
	return s.repo.BatchSetUserRebateRate(ctx, cleaned, ratePercent)
}

// AdminListCustomUsers returns accounts with a custom code or rebate
// percentage and the matching total.
//
// Missing storage returns service unavailable; repository pagination rules
// apply to the supplied filter.
func (s *AffiliateService) AdminListCustomUsers(ctx context.Context, filter AffiliateAdminFilter) ([]AffiliateAdminEntry, int64, error) {
	if s == nil || s.repo == nil {
		return nil, 0, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	return s.repo.ListUsersWithCustomSettings(ctx, filter)
}

// AdminListInviteRecords returns matching invitation records and their total
// after normalizing search and pagination.
//
// Repository fields supply current inviter codes and invitee profile
// timestamps for these relationships.
func (s *AffiliateService) AdminListInviteRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateInviteRecord, int64, error) {
	if s == nil || s.repo == nil {
		return nil, 0, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	return s.repo.ListAffiliateInviteRecords(ctx, normalizeAffiliateRecordFilter(filter))
}

// AdminListRebateRecords returns matching source-order accrual records and
// their total after filter normalization.
//
// Repository records keep the ledger rebate amount separate from source-order
// prices and charged currencies.
func (s *AffiliateService) AdminListRebateRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateRebateRecord, int64, error) {
	if s == nil || s.repo == nil {
		return nil, 0, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	return s.repo.ListAffiliateRebateRecords(ctx, normalizeAffiliateRecordFilter(filter))
}

// AdminListTransferRecords returns matching quota-transfer receipts and their
// total after filter normalization.
//
// Stored post-transfer snapshots may be absent on older records, as indicated
// by each nullable field.
func (s *AffiliateService) AdminListTransferRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateTransferRecord, int64, error) {
	if s == nil || s.repo == nil {
		return nil, 0, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	return s.repo.ListAffiliateTransferRecords(ctx, normalizeAffiliateRecordFilter(filter))
}

// AdminGetUserOverview returns an existing profile's counts, available quota
// observation, and displayed inviter rate.
//
// It rejects nonpositive IDs before reading storage. RebateRateCustom
// distinguishes a saved zero percentage from the zero produced when SQL has
// no override, so custom zero remains a disabled rate while an absent
// override selects live global policy. The selected percentage is clamped for
// display, and the available-quota observation includes due frozen amounts
// computed by the repository.
func (s *AffiliateService) AdminGetUserOverview(ctx context.Context, userID int64) (*AffiliateUserOverview, error) {
	if userID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_USER", "invalid user")
	}
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	overview, err := s.repo.GetAffiliateUserOverview(ctx, userID)
	if err != nil {
		return nil, err
	}
	if overview != nil {
		if !overview.RebateRateCustom {
			overview.RebateRatePercent = s.globalRebateRatePercent(ctx)
		}
		overview.RebateRatePercent = ClampRebateRate(overview.RebateRatePercent)
	}
	return overview, nil
}

// normalizeAffiliateRecordFilter returns trimmed search and sort text with
// normalized record pagination.
//
// Nonpositive pages select one, nonpositive sizes select twenty, and sizes
// above one hundred are capped.
func normalizeAffiliateRecordFilter(filter AffiliateRecordFilter) AffiliateRecordFilter {
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 20
	}
	if filter.PageSize > 100 {
		// Match the HTTP audit filter's maximum page size.
		filter.PageSize = 100
	}
	filter.Search = strings.TrimSpace(filter.Search)
	filter.SortBy = strings.TrimSpace(filter.SortBy)
	return filter
}
