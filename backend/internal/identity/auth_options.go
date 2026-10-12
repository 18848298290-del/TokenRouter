package identity

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

const (
	RedeemTypeInvitation = billing.RedeemTypeInvitation
	StatusUnused         = billing.StatusUnused
	StatusUsed           = billing.StatusUsed
)

var ErrRedeemCodeNotFound = billing.ErrRedeemCodeNotFound

type ProviderDefaultGrantSettings struct {
	Balance          float64
	Concurrency      int
	Subscriptions    []DefaultSubscriptionSetting
	GrantOnSignup    bool
	GrantOnFirstBind bool
}

type AuthSourceDefaultSettings struct {
	Email                        ProviderDefaultGrantSettings
	LinuxDo                      ProviderDefaultGrantSettings
	OIDC                         ProviderDefaultGrantSettings
	WeChat                       ProviderDefaultGrantSettings
	GitHub                       ProviderDefaultGrantSettings
	Google                       ProviderDefaultGrantSettings
	DingTalk                     ProviderDefaultGrantSettings
	ForceEmailOnThirdPartySignup bool
}

type DefaultSubscriptionSetting = billing.DefaultSubscriptionSetting

// AuthOptions contains startup authentication options.
type AuthOptions struct {
	Default struct {
		UserBalance     float64
		UserConcurrency int
	}
	JWT       SessionOptions
	Server    struct{ Mode string }
	Turnstile struct{ Required bool }
}
type DingTalkRegistrationPolicy struct {
	Enabled, BypassRegistration bool
	CorpRestrictionPolicy       string
}
type (
	RedeemCode              = billing.RedeemCode
	RedeemCodeRepository    = billing.RedeemCodeRepository
	AssignSubscriptionInput = billing.AssignSubscriptionInput
	UserSubscription        = billing.UserSubscription
)

// AuthIdentity is a stored provider identity exposed as a read-only value.
type AuthIdentity struct {
	ID              int64          `json:"id,omitempty"`
	CreatedAt       time.Time      `json:"created_at,omitempty"`
	UpdatedAt       time.Time      `json:"updated_at,omitempty"`
	UserID          int64          `json:"user_id,omitempty"`
	ProviderType    string         `json:"provider_type,omitempty"`
	ProviderKey     string         `json:"provider_key,omitempty"`
	ProviderSubject string         `json:"provider_subject,omitempty"`
	VerifiedAt      *time.Time     `json:"verified_at,omitempty"`
	Issuer          *string        `json:"issuer,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

// AuthSettings reads current policy for each action. Callers must not cache a startup snapshot.
type AuthSettings interface {
	GetCaptchaProviderConfig(ctx context.Context) (CaptchaProviderConfig, error)
	GetDefaultBalance(ctx context.Context) float64
	GetDefaultConcurrency(ctx context.Context) int
	GetDefaultSubscriptions(ctx context.Context) []DefaultSubscriptionSetting
	GetDefaultUserAPIKeyLimit(ctx context.Context) int
	GetDefaultUserRPMLimit(ctx context.Context) int
	GetDingTalkConnectOAuthConfig(ctx context.Context) (DingTalkRegistrationPolicy, error)
	GetRegistrationEmailSuffixWhitelist(ctx context.Context) []string
	GetSiteName(ctx context.Context) string
	IsEmailVerifyEnabled(ctx context.Context) bool
	IsInvitationCodeEnabled(ctx context.Context) bool
	IsPasswordResetEnabled(ctx context.Context) bool
	IsPromoCodeEnabled(ctx context.Context) bool
	IsRegistrationEmailDomainQuotaEnabled(ctx context.Context) bool
	IsRegistrationEmailNormalizationEnabled(ctx context.Context) bool
	IsRegistrationEnabled(ctx context.Context) bool
	IsSessionBindingEnabled(ctx context.Context) bool
	IsUserEmailChangeEnabled(ctx context.Context) bool
	ResolveAuthSourceGrantSettings(ctx context.Context, signupSource string, firstBind bool) (ProviderDefaultGrantSettings, bool, error)
}

type AuthEmail interface {
	ConsumePasswordResetToken(ctx context.Context, email, token string) error
	SendPasswordResetEmail(ctx context.Context, email, siteName, resetURL string, locale ...string) error
	SendVerifyCode(ctx context.Context, email, siteName string, locale ...string) error
	VerifyCode(ctx context.Context, email, code string) error
}

type AuthEmailQueue interface {
	EnqueuePasswordReset(email, siteName, resetURL string, locale ...string) error
	EnqueueVerifyCode(email, siteName string, locale ...string) error
}

type AuthPromo interface {
	ApplyPromoCode(context.Context, int64, string) error
}
type AuthAffiliate interface {
	EnsureUserAffiliate(context.Context, int64) error
	BindInviterByCode(context.Context, int64, string) error
}

// AuthDependencies contains the shared stores, caches, and services wired by app.
type AuthDependencies struct {
	Users                   UserRepository
	Redeem                  RedeemCodeRepository
	RefreshTokens           RefreshTokenCache
	Options                 *AuthOptions
	Settings                AuthSettings
	Email                   AuthEmail
	Turnstile               *TurnstileService
	Tencent                 *TencentCaptchaService
	Aliyun                  *AliyunCaptchaService
	EmailQueue              AuthEmailQueue
	Promo                   AuthPromo
	Affiliate               AuthAffiliate
	DefaultSubscriptions    DefaultSubscriptionAssigner
	Invalidator             UserAuthInvalidator
	BalanceCache            UserBalanceCache
	Observer                Observer
	DomainRegistration      RegistrationEmailDomainRepository
	NormalizedEmailConflict AuthNormalizedEmailBindingConflictChecker
	EmailAliasGuard         AuthEmailIdentityAliasGuardRepository
	AliasLookup             EmailAliasLookupRepository
	AliasOwner              EmailAliasOwnerLookupRepository
}

// AuthService implements registration, login, and binding rules; Storage executes database operations.
// @project-doc docs/domains/identity_and_tenancy.md#authentication_boundaries
type AuthService struct {
	*AuthDependencies
	*SessionService
	Storage AuthStorage
}

// EmailAliasLookupRepository optionally checks mailbox-alias occupancy.
// Repositories used by other services may omit this capability.
type EmailAliasLookupRepository interface {
	ExistsByEmailAlias(ctx context.Context, email string) (bool, error)
}

// EmailAliasOwnerLookupRepository distinguishes aliases owned by the current user from other users.
// Owner-aware checks permit users to select another alias of their own mailbox.
type EmailAliasOwnerLookupRepository interface {
	EmailAliasOwnerID(ctx context.Context, email string, currentUserID int64) (int64, bool, error)
}

func NewAuthService(deps *AuthDependencies, storage AuthStorage) *AuthService {
	options := SessionOptions{}
	if deps.Options != nil {
		options = deps.Options.JWT
	}
	return &AuthService{AuthDependencies: deps, SessionService: NewSessionService(options, deps.Users, deps.RefreshTokens, deps.Settings, deps.Observer.Log), Storage: storage}
}
func (s *AuthService) HasDatabase() bool { return s.Storage != nil && s.Storage.HasDatabase() }
