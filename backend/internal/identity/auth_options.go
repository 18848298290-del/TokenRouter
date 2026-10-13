package identity

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// Authentication actions read runtime policy through AuthSettings while
// startup options supply JWT and release-mode defaults. Provider grants
// retain separate signup and first-binding switches, and application wiring
// adds optional storage capabilities for domain quota and mailbox-alias
// ownership checks.
//
// AuthService retains the shared dependency pointer but builds SessionService
// from selected dependencies at construction. Callers keep that assembly
// stable while requests run; changing a field can affect direct
// authentication reads without rebuilding the embedded session service.
// Account creation, challenge proof and provider identity writes use their
// own services, so each operation relies on the dependencies assembled for
// its selected path.

// These billing aliases identify invitation codes and their unused/used
// states for authentication checks and later conditional invitation
// redemption.
const (
	RedeemTypeInvitation = billing.RedeemTypeInvitation // invitation redemption code kind
	StatusUnused         = billing.StatusUnused         // unclaimed redemption code state
	StatusUsed           = billing.StatusUsed           // claimed redemption code state
)

// ErrRedeemCodeNotFound exposes missing invitation lookup as the billing
// error recognized by authentication registration and completion callers.
var ErrRedeemCodeNotFound = billing.ErrRedeemCodeNotFound

// ProviderDefaultGrantSettings carries one provider's amounts, plan
// selections and independent action switches.
type ProviderDefaultGrantSettings struct {
	// Balance supplies a USD provider grant amount. Enabled grant merging
	// accepts nonnegative values, including zero, before falling back to global
	// balance.
	Balance float64
	// Concurrency supplies the provider concurrent-request default. Enabled
	// grant merging accepts positive values and retains the global limit when
	// this value is nonpositive.
	Concurrency int
	// Subscriptions supplies provider signup plan choices. An empty list retains
	// global selections during grant merging; selected plan values are copied
	// for the resulting grant.
	Subscriptions []DefaultSubscriptionSetting
	// GrantOnSignup and GrantOnFirstBind admit different grant actions for the
	// same provider. ResolveAuthSourceGrantSettings chooses one switch from
	// firstBind before provider values can override global defaults.
	GrantOnSignup    bool // provider signup grant switch
	GrantOnFirstBind bool // provider first-binding grant switch
}

// AuthSourceDefaultSettings contains provider grant choices and third-party
// email policy.
type AuthSourceDefaultSettings struct {
	Email                        ProviderDefaultGrantSettings // email authentication grant settings
	LinuxDo                      ProviderDefaultGrantSettings // LinuxDo authentication grant settings
	OIDC                         ProviderDefaultGrantSettings // OIDC authentication grant settings
	WeChat                       ProviderDefaultGrantSettings // WeChat authentication grant settings
	GitHub                       ProviderDefaultGrantSettings // GitHub authentication grant settings
	Google                       ProviderDefaultGrantSettings // Google authentication grant settings
	DingTalk                     ProviderDefaultGrantSettings // DingTalk authentication grant settings
	ForceEmailOnThirdPartySignup bool                         // third-party local-email completion policy
}

// DefaultSubscriptionSetting is billing's plan selection value used by
// authentication grant preparation.
type DefaultSubscriptionSetting = billing.DefaultSubscriptionSetting

// AuthOptions supplies startup authentication fallbacks and
// token/release-mode configuration.
type AuthOptions struct {
	// Default supplies startup global grant fallbacks when an action lacks
	// usable runtime settings. These values are snapshotted for creation rather
	// than applied to existing accounts.
	Default struct {
		UserBalance     float64 // startup signup balance in USD
		UserConcurrency int     // startup concurrent-request signup limit
	}
	// JWT configures the embedded SessionService at construction. AuthService
	// retains AuthOptions separately, so changing startup options later does not
	// rebuild that existing service.
	JWT SessionOptions
	// Server and Turnstile supply deployment-mode and required-proof
	// observations. Authentication applies the startup requirement in release
	// mode before accepting an anonymous challenge action.
	// Server selects deployment mode for release-time challenge admission
	// checks. The current mode is retained with the startup authentication
	// options.
	Server struct {
		Mode string // deployment mode label
	}
	// Turnstile supplies the startup proof requirement. Its Required flag
	// participates in release-mode admission even when another challenge
	// provider is configured.
	Turnstile struct {
		Required bool // release-mode challenge requirement
	}
}

// DingTalkRegistrationPolicy contains the runtime conditions used to permit
// provider registration bypass.
type DingTalkRegistrationPolicy struct {
	// Enabled and BypassRegistration both participate in DingTalk bypass
	// admission. The caller also requires the internal corporation policy before
	// bypassing the ordinary registration switch.
	// Enabled: DingTalk provider policy switch. BypassRegistration: internal
	// provider registration exception.
	Enabled, BypassRegistration bool
	CorpRestrictionPolicy       string // configured corporation restriction mode
}
type (
	// RedeemCode is billing's stored redemption value used for signup invitation
	// checks.
	RedeemCode = billing.RedeemCode
	// RedeemCodeRepository is billing's invitation lookup and conditional
	// redemption persistence interface.
	RedeemCodeRepository = billing.RedeemCodeRepository
	// AssignSubscriptionInput is billing's signup plan assignment request
	// carrying its target account.
	AssignSubscriptionInput = billing.AssignSubscriptionInput
	// UserSubscription is billing's stored user entitlement returned after plan
	// assignment.
	UserSubscription = billing.UserSubscription
)

// AuthIdentity exposes stored provider identity observations with shared
// optional values and metadata.
type AuthIdentity struct {
	ID              int64     `json:"id,omitempty"`               // stored identity record identifier
	CreatedAt       time.Time `json:"created_at,omitempty"`       // identity record creation time
	UpdatedAt       time.Time `json:"updated_at,omitempty"`       // identity record update time
	UserID          int64     `json:"user_id,omitempty"`          // identity owner account identifier
	ProviderType    string    `json:"provider_type,omitempty"`    // stored authentication provider type
	ProviderKey     string    `json:"provider_key,omitempty"`     // stored provider identity namespace
	ProviderSubject string    `json:"provider_subject,omitempty"` // stable provider subject value
	// VerifiedAt and Issuer retain the pointers received from the PostgreSQL
	// entity conversion. Callers keep those pointed-to observations stable while
	// the identity participates in authentication decisions.
	VerifiedAt *time.Time `json:"verified_at,omitempty"` // optional provider proof timestamp
	Issuer     *string    `json:"issuer,omitempty"`      // optional identity issuer text
	// Metadata carries provider profile/source observations. PostgreSQL
	// AuthIdentityFromEntity retains this map and its nested values directly, so
	// callers keep them stable through authentication decisions.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// AuthSettings reads current authentication policy and grant defaults for
// each action.
type AuthSettings interface {
	// GetCaptchaProviderConfig reads the current challenge-provider settings as
	// one configuration. Failures remain errors for authentication admission,
	// and enabled-provider conflicts are interpreted by the calling CAPTCHA
	// operation before any external proof verification.
	GetCaptchaProviderConfig(ctx context.Context) (CaptchaProviderConfig, error)
	// GetDefaultBalance reads the global signup USD amount with its configured
	// fallback. Authentication snapshots this value when building an
	// account-creation grant, then may replace it with enabled provider
	// defaults.
	GetDefaultBalance(ctx context.Context) float64
	// GetDefaultConcurrency reads the global signup concurrent-request limit
	// with its configured fallback. Account creation snapshots that value before
	// provider-grant selection; changing the setting does not update previously
	// created accounts.
	GetDefaultConcurrency(ctx context.Context) int
	// GetDefaultSubscriptions reads parsed global signup plan selections for
	// later assignment. Missing or unusable values supply no plans;
	// authentication can retain these selections when an enabled provider
	// supplies an empty plan list.
	GetDefaultSubscriptions(ctx context.Context) []DefaultSubscriptionSetting
	// GetDefaultUserAPIKeyLimit reads the count limit snapshotted onto newly
	// created accounts. RuntimeSettings selects DefaultUserAPIKeyLimit for
	// unusable text, and changing this default leaves existing account limits unchanged.
	GetDefaultUserAPIKeyLimit(ctx context.Context) int
	// GetDefaultUserRPMLimit reads the request-rate limit snapshotted onto
	// applicable new accounts. Zero selects the stored unlimited/default
	// representation; existing accounts retain their earlier limit when the
	// global setting changes.
	GetDefaultUserRPMLimit(ctx context.Context) int
	// GetDingTalkConnectOAuthConfig reads the DingTalk registration-bypass
	// policy. Authentication uses enabled/internal/bypass conditions together
	// when deciding registration eligibility; read failures remain errors for
	// the caller rather than verified organization membership.
	GetDingTalkConnectOAuthConfig(ctx context.Context) (DingTalkRegistrationPolicy, error)
	// GetRegistrationEmailSuffixWhitelist reads mailbox suffix policy for the
	// current action. Empty results permit unrestricted suffixes in the
	// applicable authentication path; registration-domain quota and
	// binding/verified-provider email policy use their separate checks.
	GetRegistrationEmailSuffixWhitelist(ctx context.Context) []string
	// GetSiteName returns the current branding text supplied to verification
	// email operations.
	GetSiteName(ctx context.Context) string
	// IsEmailVerifyEnabled reports whether ordinary registration requires a
	// local email challenge.
	IsEmailVerifyEnabled(ctx context.Context) bool
	// IsInvitationCodeEnabled reports whether registration requires an unused
	// invitation before account creation.
	IsInvitationCodeEnabled(ctx context.Context) bool
	// IsPasswordResetEnabled reports whether public mailbox-based password reset
	// is currently admitted.
	IsPasswordResetEnabled(ctx context.Context) bool
	// IsPromoCodeEnabled reports whether applicable signup paths attempt
	// promotion-code grant application.
	IsPromoCodeEnabled(ctx context.Context) bool
	// IsRegistrationEmailDomainQuotaEnabled reports whether registration may use
	// a nonwhitelisted domain's one-account capacity.
	IsRegistrationEmailDomainQuotaEnabled(ctx context.Context) bool
	// IsRegistrationEmailNormalizationEnabled reports whether registration
	// additionally checks normalized mailbox occupancy.
	IsRegistrationEmailNormalizationEnabled(ctx context.Context) bool
	// IsRegistrationEnabled reports whether ordinary account creation is
	// currently permitted by policy.
	IsRegistrationEnabled(ctx context.Context) bool
	// IsSessionBindingEnabled reports whether token operations use the
	// configured client binding checks.
	IsSessionBindingEnabled(ctx context.Context) bool
	// IsUserEmailChangeEnabled reports whether replacing an already assigned
	// account mailbox is permitted.
	IsUserEmailChangeEnabled(ctx context.Context) bool
	// ResolveAuthSourceGrantSettings reads the provider action switch and
	// combines usable provider values with global grants. firstBind selects the
	// independent first-binding switch; disabled/unknown sources retain globals,
	// while read errors return that fallback with enabled=false.
	ResolveAuthSourceGrantSettings(ctx context.Context, signupSource string, firstBind bool) (ProviderDefaultGrantSettings, bool, error)
}

// AuthEmail checks mailbox challenges and prepares authentication
// verification/reset messages.
type AuthEmail interface {
	// ConsumePasswordResetToken verifies the mailbox reset credential and
	// attempts separate deletion. EmailChallenges diagnoses deletion failure and
	// still returns success; password persistence follows, so callers do not
	// infer atomic one-time consumption from a nil result.
	ConsumePasswordResetToken(ctx context.Context, email, token string) error
	// SendPasswordResetEmail prepares and sends a mailbox reset message using
	// site/link text and optional locale. The caller handles returned
	// preparation/delivery errors; queued callers perform this work later in the
	// email worker.
	SendPasswordResetEmail(ctx context.Context, email, siteName, resetURL string, locale ...string) error
	// SendVerifyCode prepares and sends a mailbox verification challenge with
	// site text and optional locale. The identity queue can invoke it
	// asynchronously after admission, so an enqueue success is distinct from
	// successful delivery.
	SendVerifyCode(ctx context.Context, email, siteName string, locale ...string) error
	// VerifyCode checks the submitted mailbox challenge for authentication or
	// binding. The calling flow selects when proof is required and propagates
	// rejection before applicable credential/account writes; proof storage owns
	// its attempt and consumption behavior.
	VerifyCode(ctx context.Context, email, code string) error
}

// AuthEmailQueue admits requests for later identity email worker delivery.
type AuthEmailQueue interface {
	// EnqueuePasswordReset admits a mailbox reset request with site/link text
	// and optional locale. A successful enqueue schedules later worker delivery;
	// queue admission errors remain errors for the authentication caller.
	EnqueuePasswordReset(email, siteName, resetURL string, locale ...string) error
	// EnqueueVerifyCode admits a mailbox challenge request with site text and
	// optional locale. The worker generates and delivers its code later, so
	// authentication handles enqueue failure separately from later mail
	// failures.
	EnqueueVerifyCode(email, siteName string, locale ...string) error
}

// AuthPromo applies signup promotion grants after an account has been
// created.
type AuthPromo interface {
	// ApplyPromoCode requests promotion grants for an existing account and code.
	// Applicable signup callers invoke it after account creation and diagnose
	// failure while continuing login, so an error does not establish account
	// rollback.
	ApplyPromoCode(context.Context, int64, string) error
}

// AuthAffiliate supplies signup affiliate creation and inviter attribution
// operations.
type AuthAffiliate interface {
	// EnsureUserAffiliate establishes the account's affiliate record when that
	// capability is configured. Signup invokes it after creating the account and
	// treats failures as diagnostic, leaving account creation completed.
	EnsureUserAffiliate(context.Context, int64) error
	// BindInviterByCode requests inviter attribution for the supplied account
	// and signup code. Signup invokes it after account creation and records
	// failures diagnostically; its returned error does not remove the created
	// account.
	BindInviterByCode(context.Context, int64, string) error
}

// AuthDependencies retains the account stores, challenge services and policy
// readers assembled by app.
type AuthDependencies struct {
	Users UserRepository // current account persistence service
	// Redeem supplies invitation lookup and conditional use. Registration paths
	// require it when the runtime invitation switch requests a code before
	// account creation.
	Redeem        RedeemCodeRepository
	RefreshTokens RefreshTokenCache // refresh credential/session-family cache
	// Options supplies startup fallback values when nonnil. NewAuthService
	// copies its JWT values into SessionService, while other authentication
	// reads retain this options pointer.
	Options *AuthOptions
	// Settings supplies current authentication policy. Nil settings disables
	// ordinary registration, while operations reading it directly require the
	// caller to assemble this dependency.
	Settings AuthSettings
	// Email verifies mailbox challenges and performs direct delivery.
	// Required-proof/reset paths reject missing service, while password-only
	// login uses its separate credential check.
	Email AuthEmail
	// Turnstile verifies the selected Cloudflare proof when configured. An
	// enabled provider without its verifier is unavailable to the corresponding
	// CAPTCHA admission path.
	Turnstile *TurnstileService
	// Tencent verifies Tencent ticket/random-string proofs when selected. An
	// enabled provider without this verifier is unavailable to the corresponding
	// CAPTCHA admission path.
	Tencent *TencentCaptchaService
	// Aliyun verifies captchaVerifyParam proof when selected. An enabled
	// provider without this verifier is unavailable to the corresponding CAPTCHA
	// admission path.
	Aliyun *AliyunCaptchaService
	// EmailQueue admits asynchronous challenge/reset delivery. Missing queue
	// returns a configuration error from SendVerifyCodeAsync and
	// ErrServiceUnavailable from RequestPasswordResetAsync before any enqueue
	// attempt.
	EmailQueue AuthEmailQueue
	// Promo supplies optional signup promotion grants after account creation. A
	// missing service skips application, and applicable callers diagnose grant
	// failure while continuing login.
	Promo AuthPromo
	// Affiliate supplies optional signup affiliate creation and inviter
	// attribution. Missing service skips those effects; applicable account
	// creation callers diagnose failures after persistence.
	Affiliate AuthAffiliate
	// DefaultSubscriptions supplies optional signup plan assignment. Missing
	// service skips assignment; authentication diagnoses assignment failures
	// after account creation rather than deleting the account.
	DefaultSubscriptions DefaultSubscriptionAssigner
	// Invalidator retains the authentication-cache callback assembled by app.
	// Current AuthService and PostgreSQL AuthState operations do not read this
	// field, so assignment alone establishes no cache publication.
	Invalidator UserAuthInvalidator
	// BalanceCache retains the funding-cache callback assembled by app. Current
	// AuthService and PostgreSQL AuthState operations do not read it, so
	// assignment alone establishes no grant cleanup.
	BalanceCache UserBalanceCache
	Observer     Observer // authentication diagnostic event callbacks
	// DomainRegistration supplies guarded registration under
	// nonwhitelisted-domain quota. Missing capability rejects database-backed
	// quota checks, while database-free fixtures can omit that check.
	DomainRegistration RegistrationEmailDomainRepository
	// NormalizedEmailConflict supplies owner-excluding normalized-address checks
	// when selected. Without it, a normalized match to the current mailbox
	// cannot establish another owner through boolean lookup.
	NormalizedEmailConflict AuthNormalizedEmailBindingConflictChecker
	// EmailAliasGuard performs destination alias checks and credential
	// replacement in PostgreSQL AuthState's bound-email transaction. When
	// absent, that adapter uses direct transactional credential writes with
	// applicable normalized-email checks.
	EmailAliasGuard AuthEmailIdentityAliasGuardRepository
	// AliasLookup supplies boolean alias occupancy when owner-aware lookup is
	// absent. Without either alias capability this preflight is skipped; a
	// positive boolean result is rejected.
	AliasLookup EmailAliasLookupRepository
	// AliasOwner identifies alias occupancy before boolean-only lookup. It
	// permits the current account's own alias and rejects another owner; absence
	// selects AliasLookup when supplied.
	AliasOwner EmailAliasOwnerLookupRepository
}

// AuthService implements registration, login, and binding rules; Storage executes database operations.
// @project-doc docs/domains/identity_and_tenancy.md#authentication_boundaries
type AuthService struct {
	// AuthDependencies retains the shared assembled dependency pointer. Callers
	// leave its fields stable during requests so direct authentication
	// operations use consistent services.
	*AuthDependencies
	// SessionService is constructed from the selected JWT/account/refresh
	// dependencies. Replacing AuthDependencies fields later does not reconstruct
	// this embedded service.
	*SessionService
	Storage AuthStorage // provider identity persistence operations
}

// EmailAliasLookupRepository supplies optional boolean mailbox-alias
// occupancy checks for email binding.
type EmailAliasLookupRepository interface {
	// ExistsByEmailAlias reports whether the supplied mailbox alias is occupied.
	// Boolean-only capability cannot identify the owner, so binding uses
	// owner-aware lookup when available to permit another alias belonging to the
	// same account.
	ExistsByEmailAlias(ctx context.Context, email string) (bool, error)
}

// EmailAliasOwnerLookupRepository distinguishes a matching mailbox-alias
// owner during verified email binding.
type EmailAliasOwnerLookupRepository interface {
	// EmailAliasOwnerID reports a matching alias owner and whether occupancy was
	// observed. Binding passes currentUserID and permits that owner's own alias;
	// storage errors remain errors and write-time alias guards recheck
	// occupancy.
	EmailAliasOwnerID(ctx context.Context, email string, currentUserID int64) (int64, bool, error)
}

// NewAuthService retains a required dependency pointer and creates its
// embedded session service from JWT, account, refresh, settings and observer
// values. Nil Options selects zero SessionOptions; callers assemble the
// required path dependencies and keep the shared wiring stable during
// requests.
func NewAuthService(deps *AuthDependencies, storage AuthStorage) *AuthService {
	options := SessionOptions{}
	if deps.Options != nil {
		options = deps.Options.JWT
	}
	return &AuthService{AuthDependencies: deps, SessionService: NewSessionService(options, deps.Users, deps.RefreshTokens, deps.Settings, deps.Observer.Log), Storage: storage}
}

// HasDatabase reports whether Storage exposes a configured database. The
// receiver is required, while nil Storage returns false; this check does not
// establish other authentication or session dependencies before their
// operations.
func (s *AuthService) HasDatabase() bool { return s.Storage != nil && s.Storage.HasDatabase() }
