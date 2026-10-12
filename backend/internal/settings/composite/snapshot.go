package composite

import (
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	"github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"
	"github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/identity/contact"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// Snapshot combines module settings for admin reads and partial update merging.
type Snapshot struct {
	ResponsesWS          json.RawMessage
	ResponsesWSEffective ws.Parameters
	// StoredValues holds the database values from this read for revision checks and partial updates.
	StoredValues map[string]string `json:"-"`

	RegistrationEnabled                 bool
	EmailVerifyEnabled                  bool
	RegistrationEmailSuffixWhitelist    []string
	RegistrationEmailNormalization      bool
	RegistrationEmailDomainQuotaEnabled bool // per-registrable-domain quotas outside the allowlist; disabled by default
	UserEmailChangeEnabled              bool // allow users with an email identity to replace their primary email; disabled by default
	PromoCodeEnabled                    bool
	PasswordResetEnabled                bool
	FrontendURL                         string
	InvitationCodeEnabled               bool
	TotpEnabled                         bool // TOTP two-factor authentication
	SessionBindingEnabled               bool // bind sessions to IP and User-Agent; changes invalidate the session
	StepUpEnabled                       bool // step-up 2FA for sensitive operations
	AuditLogRetentionDays               int  // retention in days; <=0 keeps audit logs indefinitely
	LoginAgreementEnabled               bool
	LoginAgreementMode                  string
	LoginAgreementUpdatedAt             string
	LoginAgreementDocuments             []site.LoginAgreementDocument

	SMTPHost               string
	SMTPPort               int
	SMTPUsername           string
	SMTPPassword           string
	SMTPPasswordConfigured bool
	SMTPFrom               string
	SMTPFromName           string
	SMTPUseTLS             bool

	TurnstileEnabled                       bool
	TurnstileSiteKey                       string
	TurnstileSecretKey                     string
	TurnstileSecretKeyConfigured           bool
	TencentCaptchaEnabled                  bool
	TencentCaptchaAppID                    string
	TencentCaptchaAppSecretKey             string
	TencentCaptchaAppSecretKeyConfigured   bool
	TencentCaptchaCloudSecretID            string
	TencentCaptchaCloudSecretIDConfigured  bool
	TencentCaptchaCloudSecretKey           string
	TencentCaptchaCloudSecretKeyConfigured bool
	TencentCaptchaRegion                   string
	AliyunCaptchaEnabled                   bool
	AliyunCaptchaAccessKeyID               string
	AliyunCaptchaAccessKeySecret           string
	AliyunCaptchaAccessKeySecretConfigured bool
	AliyunCaptchaSceneID                   string
	AliyunCaptchaPrefix                    string
	AliyunCaptchaRegion                    string
	APIKeyACLTrustForwardedIP              bool
	ForwardedClientIPHeaders               []string

	// LinuxDo Connect OAuth login.
	LinuxDoConnectEnabled                bool
	LinuxDoConnectClientID               string
	LinuxDoConnectClientSecret           string
	LinuxDoConnectClientSecretConfigured bool
	LinuxDoConnectRedirectURL            string

	// DingTalk Connect OAuth login.
	DingTalkConnectEnabled                 bool
	DingTalkConnectClientID                string
	DingTalkConnectClientSecret            string
	DingTalkConnectClientSecretConfigured  bool
	DingTalkConnectRedirectURL             string
	DingTalkConnectCorpRestrictionPolicy   string
	DingTalkConnectInternalCorpID          string
	DingTalkConnectBypassRegistration      bool
	DingTalkConnectSyncCorpEmail           bool
	DingTalkConnectSyncDisplayName         bool
	DingTalkConnectSyncDept                bool
	DingTalkConnectSyncCorpEmailAttrKey    string
	DingTalkConnectSyncDisplayNameAttrKey  string
	DingTalkConnectSyncDeptAttrKey         string
	DingTalkConnectSyncCorpEmailAttrName   string
	DingTalkConnectSyncDisplayNameAttrName string
	DingTalkConnectSyncDeptAttrName        string

	// WeChat Connect OAuth login.
	WeChatConnectEnabled                   bool
	WeChatConnectAppID                     string
	WeChatConnectAppSecret                 string
	WeChatConnectAppSecretConfigured       bool
	WeChatConnectOpenAppID                 string
	WeChatConnectOpenAppSecret             string
	WeChatConnectOpenAppSecretConfigured   bool
	WeChatConnectMPAppID                   string
	WeChatConnectMPAppSecret               string
	WeChatConnectMPAppSecretConfigured     bool
	WeChatConnectMobileAppID               string
	WeChatConnectMobileAppSecret           string
	WeChatConnectMobileAppSecretConfigured bool
	WeChatConnectOpenEnabled               bool
	WeChatConnectMPEnabled                 bool
	WeChatConnectMobileEnabled             bool
	WeChatConnectMode                      string
	WeChatConnectScopes                    string
	WeChatConnectRedirectURL               string
	WeChatConnectFrontendRedirectURL       string

	// Generic OIDC login.
	OIDCConnectEnabled                bool
	OIDCConnectProviderName           string
	OIDCConnectClientID               string
	OIDCConnectClientSecret           string
	OIDCConnectClientSecretConfigured bool
	OIDCConnectIssuerURL              string
	OIDCConnectDiscoveryURL           string
	OIDCConnectAuthorizeURL           string
	OIDCConnectTokenURL               string
	OIDCConnectUserInfoURL            string
	OIDCConnectJWKSURL                string
	OIDCConnectScopes                 string
	OIDCConnectRedirectURL            string
	OIDCConnectFrontendRedirectURL    string
	OIDCConnectTokenAuthMethod        string
	OIDCConnectUsePKCE                bool
	OIDCConnectValidateIDToken        bool
	OIDCConnectAllowedSigningAlgs     string
	OIDCConnectClockSkewSeconds       int
	OIDCConnectRequireEmailVerified   bool
	OIDCConnectUserInfoEmailPath      string
	OIDCConnectUserInfoIDPath         string
	OIDCConnectUserInfoUsernamePath   string

	// GitHub and Google email login.
	GitHubOAuthEnabled                bool
	GitHubOAuthClientID               string
	GitHubOAuthClientSecret           string
	GitHubOAuthClientSecretConfigured bool
	GitHubOAuthRedirectURL            string
	GitHubOAuthFrontendRedirectURL    string
	GoogleOAuthEnabled                bool
	GoogleOneTapEnabled               bool
	GoogleOAuthClientID               string
	GoogleOAuthClientSecret           string
	GoogleOAuthClientSecretConfigured bool
	GoogleOAuthRedirectURL            string
	GoogleOAuthFrontendRedirectURL    string

	LocalizedSettings           settings.LocalizedTexts `json:"localized_settings"`
	SiteTexts                   site.LocalizedTexts     `json:"site_texts"`
	DefaultLocale               string                  `json:"default_locale"`
	SiteName                    string
	SiteLogo                    string
	SiteSubtitle                string
	APIBaseURL                  string
	ContactInfo                 string
	DocURL                      string
	HomeContent                 string
	HideCcsImportButton         bool
	PurchaseSubscriptionEnabled bool
	PurchaseSubscriptionURL     string
	TableDefaultPageSize        int
	TablePageSizeOptions        []int
	UsageRankingLimit           int
	UsageRankingEnabled         bool
	UsageRankingSortBy          string
	UsageRankingShowTotalTokens bool
	UsageRankingShowRequests    bool
	UsageRankingShowActualCost  bool
	CustomMenuItems             string // JSON array of custom menu items
	CustomEndpoints             string // JSON array of custom endpoints
	FooterLinks                 string // JSON array of footer link groups
	FooterText                  string // Extra footer text (ICP number etc.)
	HomeFeaturedModels          string // JSON array of model IDs featured on the home page
	// CreativeModelSettings lists the globally allowed Creative Studio group, model, and capability combinations.
	CreativeModelSettings []creative.CreativeModelSetting
	// CreativeWorkerCount is the task worker count, resolved to the default when missing.
	CreativeWorkerCount int

	DefaultConcurrency int
	DefaultBalance     float64
	// TeamEnabled controls access to team pages.
	TeamEnabled bool
	// CreativeEnabled controls Creative Studio page and API access; process configuration creative.enabled is
	// also required.
	CreativeEnabled bool
	// RiskControlEnabled controls the risk center and gateway content auditing.
	RiskControlEnabled                   bool
	CyberSessionBlockEnabled             bool
	CyberSessionBlockTTLSeconds          int
	AffiliateEnabled                     bool
	AffiliateRebateRate                  float64
	AffiliateRebateFreezeHours           int
	AffiliateRebateDurationDays          int
	AffiliateRebatePerInviteeCap         float64
	AdminRechargeRebateEnabled           bool
	DefaultUserRPMLimit                  int
	DefaultUserAPIKeyLimit               int
	DefaultSubscriptions                 []billing.DefaultSubscriptionSetting
	BalanceUnitName                      string
	BalanceUnitSymbol                    string
	BalanceIconSVG                       string
	ReasoningPointRMBUnitPrice           float64
	USDExchangeRate                      float64
	MarketplaceAvailabilityWindowDays    int
	MarketplaceAvailabilityBucketMinutes int

	// Model fallback configuration
	EnableModelFallback      bool   `json:"enable_model_fallback"`
	FallbackModelAnthropic   string `json:"fallback_model_anthropic"`
	FallbackModelOpenAI      string `json:"fallback_model_openai"`
	FallbackModelGemini      string `json:"fallback_model_gemini"`
	FallbackModelAntigravity string `json:"fallback_model_antigravity"`

	// Identity patch configuration (Claude -> Gemini)
	EnableIdentityPatch bool   `json:"enable_identity_patch"`
	IdentityPatchPrompt string `json:"identity_patch_prompt"`

	// Grok defaults used when the provider has no model mapping.
	GrokDefaultTextModel   string `json:"grok_default_text_model"`
	GrokDefaultBaseURLMode string `json:"grok_default_base_url_mode"`

	// Operations monitoring.
	OpsMonitoringEnabled         bool
	OpsRealtimeMonitoringEnabled bool
	OpsMetricsIntervalSeconds    int

	// Claude Code version check
	MinClaudeCodeVersion string
	MaxClaudeCodeVersion string

	// Backend mode disables registration and self-service and restricts login to administrators.
	BackendModeEnabled bool

	// Gateway forwarding behavior
	OpenAITTFTMode                         string // Responses first_token_ms measurement mode; defaults to semantic
	EnableFingerprintUnification           bool   // unify OAuth provider fingerprint headers; defaults to true
	EnableMetadataPassthrough              bool   // forward client metadata; defaults to false
	EnableCCHSigning                       bool   // deprecated no-op; the gateway does not inject or sign cch
	EnableClaudeOAuthSystemPromptInjection bool   // inject Claude Code system blocks on the Claude OAuth mimic path; defaults to true
	ClaudeOAuthSystemPrompt                string // Claude OAuth mimic extension prompt; empty uses the built-in default
	ClaudeOAuthSystemPromptBlocks          string // Claude OAuth mimic system blocks JSON; empty uses built-in defaults
	EnableAnthropicCacheTTL1hInjection     bool   // inject 1h cache_control TTL for Anthropic OAuth/SetupToken; defaults to false
	EnableClientDatelineNormalization      bool   // normalize client datelines for Anthropic OAuth/SetupToken; defaults to true
	RewriteMessageCacheControl             bool   // rewrite messages[*].content[*].cache_control; defaults to false
	AntigravityUserAgentVersion            string // upstream Antigravity User-Agent version; empty uses configuration defaults
	OpenAICodexUserAgent                   string // full upstream Codex User-Agent; empty uses the built-in TUI default
	OpenAIAllowClaudeCodeCodexPlugin       bool   // allow the Claude Code Codex plugin globally; defaults to false
	UserPromptReplacementConfig            *promptpolicy.UserPromptReplacementConfig

	// Web Search Emulation
	WebSearchEmulationEnabled bool

	// Payment visible method routing
	PaymentVisibleMethodAlipaySource  string
	PaymentVisibleMethodWxpaySource   string
	PaymentVisibleMethodAlipayEnabled bool
	PaymentVisibleMethodWxpayEnabled  bool

	// Shared advanced scheduler parameters; each group's scheduler_type selects whether to use them.
	AdvancedSchedulerStickyWeightedEnabled       bool
	AdvancedSchedulerSubscriptionPriorityEnabled bool
	AdvancedSchedulerEWMAErrorRateAlpha          string
	AdvancedSchedulerEWMATTFTAlpha               string
	AdvancedSchedulerStickyEscapeEnabled         bool
	// AdvancedSchedulerStickyEscapeEnabledSet records whether the database overrides the inherited switch.
	AdvancedSchedulerStickyEscapeEnabledSet          bool
	AdvancedSchedulerStickyEscapeTTFTMs              string
	AdvancedSchedulerStickyEscapeErrorRate           string
	AdvancedSchedulerLBTopK                          string
	AdvancedSchedulerWeightPriority                  string
	AdvancedSchedulerWeightLoad                      string
	AdvancedSchedulerWeightQueue                     string
	AdvancedSchedulerWeightErrorRate                 string
	AdvancedSchedulerWeightTTFT                      string
	AdvancedSchedulerWeightReset                     string
	AdvancedSchedulerWeightQuotaHeadroom             string
	AdvancedSchedulerWeightPreviousResponse          string
	AdvancedSchedulerWeightSessionSticky             string
	AdvancedSchedulerEffectiveLBTopK                 string
	AdvancedSchedulerEffectiveWeightPriority         string
	AdvancedSchedulerEffectiveWeightLoad             string
	AdvancedSchedulerEffectiveWeightQueue            string
	AdvancedSchedulerEffectiveWeightErrorRate        string
	AdvancedSchedulerEffectiveWeightTTFT             string
	AdvancedSchedulerEffectiveWeightReset            string
	AdvancedSchedulerEffectiveWeightQuotaHeadroom    string
	AdvancedSchedulerEffectiveWeightPreviousResponse string
	AdvancedSchedulerEffectiveWeightSessionSticky    string
	AdvancedSchedulerEffectiveEWMAErrorRateAlpha     string
	AdvancedSchedulerEffectiveEWMATTFTAlpha          string
	AdvancedSchedulerEffectiveStickyEscapeEnabled    bool
	AdvancedSchedulerEffectiveStickyEscapeTTFTMs     string
	AdvancedSchedulerEffectiveStickyEscapeErrorRate  string
	// OpenAIQuotaAutoPauseSettings contains global OpenAI quota thresholds stored in ops_advanced_settings.
	OpenAIQuotaAutoPauseSettings provider.QuotaAutoPauseSettings
	// OpenAIQuotaAutoPauseSettingsSet records request presence so omitted values do not overwrite saved thresholds.
	OpenAIQuotaAutoPauseSettingsSet bool

	// Low-balance notifications.
	BalanceLowNotifyEnabled     bool
	BalanceLowNotifyThreshold   float64
	BalanceLowNotifyRechargeURL string

	// Subscription expiry notifications.
	SubscriptionExpiryNotifyEnabled bool

	// Provider quota notifications.
	ProviderQuotaNotifyEnabled bool
	ProviderQuotaNotifyEmails  []contact.Entry

	// Global provider auto-pause thresholds by platform; 100 disables pausing.
	ProviderSchedulingThresholds map[string]int `json:"provider_scheduling_thresholds"`

	// Allow users to view their own failed requests on the usage page.
	AllowUserViewErrorRequests bool
}
