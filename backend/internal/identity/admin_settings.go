package identity

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Administrator authentication saves use different empty-secret rules by
// provider. Exact empty raw CAPTCHA/LinuxDo/DingTalk/OIDC credentials omit
// writes, while GitHub/Google/WeChat credentials are trimmed first and
// missing WeChat channel values can inherit legacy credentials.
//
// PrepareAdminSettings modifies the supplied model while normalizing
// whitelist and selected provider values, then builds a flat persistence map.
// The API Key allowance check occurs at the end, so rejection can leave that
// input normalized although no prepared map is returned. The settings
// participant later filters prepared keys by JSON field presence; model
// preparation itself is not a partial-patch decoder or a persistence
// transaction.

// These identity-owned keys select administrator CAPTCHA browser settings,
// signup concurrency and the labels attached to provider-synchronized
// attribute destinations.
const (
	SettingKeyAliyunCaptchaPrefix                    = "aliyun_captcha_prefix"
	SettingKeyDefaultConcurrency                     = "default_concurrency"
	SettingKeyDingTalkConnectSyncCorpEmailAttrName   = "dingtalk_connect_sync_corp_email_attr_name"
	SettingKeyDingTalkConnectSyncDeptAttrName        = "dingtalk_connect_sync_dept_attr_name"
	SettingKeyDingTalkConnectSyncDisplayNameAttrName = "dingtalk_connect_sync_display_name_attr_name"
	SettingKeyTurnstileSiteKey                       = "turnstile_site_key"
)

// AdminSettings carries administrator authentication input and effective
// readback values for provider/policy configuration.
type AdminSettings struct {
	// Aliyun verification access identifier
	AliyunCaptchaAccessKeyID string `json:"aliyun_captcha_access_key_id"`
	// AliyunCaptchaAccessKeySecret is saved when its raw text is nonempty. Exact
	// empty input omits this key, retaining the stored verification credential.
	AliyunCaptchaAccessKeySecret string `json:"aliyun_captcha_access_key_secret"`
	// Aliyun challenge selection switch
	AliyunCaptchaEnabled bool `json:"aliyun_captcha_enabled"`
	// Aliyun browser resource prefix
	AliyunCaptchaPrefix string `json:"aliyun_captcha_prefix"`
	AliyunCaptchaRegion string `json:"aliyun_captcha_region"` // Aliyun challenge region label
	// Aliyun verification scene identifier
	AliyunCaptchaSceneID string `json:"aliyun_captcha_scene_id"`
	// DefaultConcurrency is serialized as supplied by administrator preparation.
	// ReadAdminSettings accepts parsed zero/negative text, while the signup
	// runtime reader selects fallback after nonpositive values.
	DefaultConcurrency int `json:"default_concurrency"`
	// DefaultUserAPIKeyLimit selects the signup Key allowance. Preparation
	// rejects values outside the supported database range; accepted zero retains
	// the unlimited representation.
	DefaultUserAPIKeyLimit int `json:"default_user_api_key_limit"`
	// DefaultUserRPMLimit is serialized as supplied. Runtime/admin readers
	// accept nonnegative text and resolve malformed or negative text to the zero
	// unlimited value.
	DefaultUserRPMLimit int `json:"default_user_rpm_limit"`
	// DingTalkConnectBypassRegistration requests the closed-registration
	// exception. Effective/admin readers clear it outside internal_only policy
	// before the authentication gate considers the provider.
	DingTalkConnectBypassRegistration bool `json:"dingtalk_connect_bypass_registration"`
	// DingTalk authorization client identifier
	DingTalkConnectClientID string `json:"dingtalk_connect_client_id"`
	// DingTalkConnectClientSecret omits its write when exactly empty.
	// Preparation retains other raw text, while effective/admin reads can use
	// trimmed startup credentials when stored text is blank.
	DingTalkConnectClientSecret string `json:"dingtalk_connect_client_secret"`
	// DingTalk organization eligibility policy
	DingTalkConnectCorpRestrictionPolicy string `json:"dingtalk_connect_corp_restriction_policy"`
	// DingTalk login selection switch
	DingTalkConnectEnabled bool `json:"dingtalk_connect_enabled"`
	// configured DingTalk organization identifier
	DingTalkConnectInternalCorpID string `json:"dingtalk_connect_internal_corp_id"`
	// DingTalk backend callback address
	DingTalkConnectRedirectURL string `json:"dingtalk_connect_redirect_url"`
	// DingTalkConnectSyncCorpEmail requests enterprise-email attribute
	// synchronization. Effective/admin settings clear this switch outside
	// internal_only policy, and the sync service checks that policy again.
	DingTalkConnectSyncCorpEmail bool `json:"dingtalk_connect_sync_corp_email"`
	// DingTalkConnectSyncCorpEmailAttrKey and
	// DingTalkConnectSyncCorpEmailAttrName name the existing synchronization
	// destination and its administrator label. Readback selects configured,
	// startup and built-in values for that pair.
	// enterprise email destination attribute key
	DingTalkConnectSyncCorpEmailAttrKey string `json:"dingtalk_connect_sync_corp_email_attr_key"`
	// enterprise email attribute display label
	DingTalkConnectSyncCorpEmailAttrName string `json:"dingtalk_connect_sync_corp_email_attr_name"`
	// DingTalkConnectSyncDept requests department-path attribute
	// synchronization. Effective/admin settings clear it outside internal_only
	// policy before the sync service selects department data.
	DingTalkConnectSyncDept bool `json:"dingtalk_connect_sync_dept"`
	// DingTalkConnectSyncDeptAttrKey and DingTalkConnectSyncDeptAttrName supply
	// the department destination key and display label. Readback uses their
	// separate configured/startup/built-in candidates while synchronization
	// addresses the key.
	// department path destination attribute key
	DingTalkConnectSyncDeptAttrKey string `json:"dingtalk_connect_sync_dept_attr_key"`
	// department path attribute display label
	DingTalkConnectSyncDeptAttrName string `json:"dingtalk_connect_sync_dept_attr_name"`
	// DingTalkConnectSyncDisplayName requests provider-name attribute
	// synchronization. Effective/admin settings clear it outside internal_only
	// policy, keeping the configured attribute destination separate from account
	// username adoption.
	DingTalkConnectSyncDisplayName bool `json:"dingtalk_connect_sync_display_name"`
	// DingTalkConnectSyncDisplayNameAttrKey and
	// DingTalkConnectSyncDisplayNameAttrName supply the provider-name
	// destination and its administrator label. Effective synchronization writes
	// by the key, while settings preparation retains the selected label.
	// provider name destination attribute key
	DingTalkConnectSyncDisplayNameAttrKey string `json:"dingtalk_connect_sync_display_name_attr_key"`
	// provider name attribute display label
	DingTalkConnectSyncDisplayNameAttrName string `json:"dingtalk_connect_sync_display_name_attr_name"`
	// ordinary registration mailbox-proof switch
	EmailVerifyEnabled bool `json:"email_verify_enabled"`
	// GitHub authorization client identifier
	GitHubOAuthClientID string `json:"github_oauth_client_id"`
	// GitHubOAuthClientSecret is trimmed before preparation decides whether to
	// save it. Blank trimmed text omits its key, retaining the stored credential
	// for later effective reads.
	GitHubOAuthClientSecret string `json:"github_oauth_client_secret"`
	GitHubOAuthEnabled      bool   `json:"github_oauth_enabled"` // GitHub login selection switch
	// GitHubOAuthFrontendRedirectURL is trimmed for persistence. Blank text
	// selects OAuthDefaultGitHubOAuthFrontend, while the effective configuration
	// reader validates its later browser destination.
	GitHubOAuthFrontendRedirectURL string `json:"github_oauth_frontend_redirect_url"`
	// GitHub backend callback address
	GitHubOAuthRedirectURL string `json:"github_oauth_redirect_url"`
	// Google authorization client identifier
	GoogleOAuthClientID string `json:"google_oauth_client_id"`
	// GoogleOAuthClientSecret is trimmed before its nonblank write. Blank
	// trimmed input omits the key, preserving stored credentials rather than
	// requesting their deletion.
	GoogleOAuthClientSecret string `json:"google_oauth_client_secret"`
	GoogleOAuthEnabled      bool   `json:"google_oauth_enabled"` // Google login selection switch
	// GoogleOAuthFrontendRedirectURL is trimmed for persistence. Blank input
	// selects OAuthDefaultGoogleOAuthFrontend, and effective configuration
	// validation checks its browser destination separately.
	GoogleOAuthFrontendRedirectURL string `json:"google_oauth_frontend_redirect_url"`
	// Google backend callback address
	GoogleOAuthRedirectURL string `json:"google_oauth_redirect_url"`
	GoogleOneTapEnabled    bool   `json:"google_one_tap_enabled"` // Google ID-token login switch
	// LinuxDo authorization client identifier
	LinuxDoConnectClientID string `json:"linuxdo_connect_client_id"`
	// LinuxDoConnectClientSecret skips its write only for exact empty input.
	// Preparation retains other raw text; effective/admin readers fall back to
	// startup secret after trimmed blank storage.
	LinuxDoConnectClientSecret string `json:"linuxdo_connect_client_secret"`
	// LinuxDo login selection switch
	LinuxDoConnectEnabled bool `json:"linuxdo_connect_enabled"`
	// LinuxDo backend callback address
	LinuxDoConnectRedirectURL string `json:"linuxdo_connect_redirect_url"`
	// accepted ID-token signature algorithms
	OIDCConnectAllowedSigningAlgs string `json:"oidc_connect_allowed_signing_algs"`
	// OIDC browser authorization endpoint
	OIDCConnectAuthorizeURL string `json:"oidc_connect_authorize_url"`
	// OIDC authorization client identifier
	OIDCConnectClientID string `json:"oidc_connect_client_id"`
	// OIDCConnectClientSecret omits its write for exact empty input.
	// Effective/admin readers can use startup credentials after trimmed blank
	// storage; preparation does not trim this field itself.
	OIDCConnectClientSecret string `json:"oidc_connect_client_secret"`
	// OIDCConnectClockSkewSeconds supplies validation seconds. Administrator
	// reads retain parsed stored zero, but absent/unusable storage with startup
	// zero displays 120; effective OAuth validation keeps its own
	// zero-through-600 rule.
	OIDCConnectClockSkewSeconds int `json:"oidc_connect_clock_skew_seconds"`
	// OIDC provider metadata address
	OIDCConnectDiscoveryURL string `json:"oidc_connect_discovery_url"`
	OIDCConnectEnabled      bool   `json:"oidc_connect_enabled"` // OIDC login selection switch
	// OIDC frontend return destination
	OIDCConnectFrontendRedirectURL string `json:"oidc_connect_frontend_redirect_url"`
	// expected provider issuer address
	OIDCConnectIssuerURL string `json:"oidc_connect_issuer_url"`
	// ID-token signing-key metadata address
	OIDCConnectJWKSURL      string `json:"oidc_connect_jwks_url"`
	OIDCConnectProviderName string `json:"oidc_connect_provider_name"` // OIDC login display name
	// OIDC backend callback address
	OIDCConnectRedirectURL string `json:"oidc_connect_redirect_url"`
	// new-identity verified-mailbox requirement
	OIDCConnectRequireEmailVerified bool `json:"oidc_connect_require_email_verified"`
	// requested OIDC authorization scopes
	OIDCConnectScopes string `json:"oidc_connect_scopes"`
	// token endpoint credential method
	OIDCConnectTokenAuthMethod string `json:"oidc_connect_token_auth_method"`
	OIDCConnectTokenURL        string `json:"oidc_connect_token_url"` // OIDC code exchange endpoint
	// OIDCConnectUsePKCE carries the selected authorization-proof switch.
	// ReadAdminSettings uses compatibility defaults when storage omits this key,
	// while submitted writes persist the boolean selected by their
	// presence-aware workflow.
	OIDCConnectUsePKCE bool `json:"oidc_connect_use_pkce"`
	// OIDCConnectUserInfoEmailPath, OIDCConnectUserInfoIDPath and
	// OIDCConnectUserInfoUsernamePath select different userinfo values.
	// ReadAdminSettings permits a present empty path to clear startup text,
	// unlike nonblank-only endpoint overrides.
	// userinfo mailbox extraction path
	OIDCConnectUserInfoEmailPath string `json:"oidc_connect_userinfo_email_path"`
	// userinfo stable subject extraction path
	OIDCConnectUserInfoIDPath string `json:"oidc_connect_userinfo_id_path"`
	// OIDC provider profile endpoint
	OIDCConnectUserInfoURL string `json:"oidc_connect_userinfo_url"`
	// userinfo account name extraction path
	OIDCConnectUserInfoUsernamePath string `json:"oidc_connect_userinfo_username_path"`
	// OIDCConnectValidateIDToken carries the selected token-verification switch.
	// Effective/admin readers use startup presence flags for compatibility
	// defaults when its store key is missing; present saved text selects its own
	// boolean.
	OIDCConnectValidateIDToken bool `json:"oidc_connect_validate_id_token"`
	// PasswordResetEnabled requests mailbox password recovery. Administrator
	// readback and AuthService admission additionally require
	// EmailVerifyEnabled, while preparation stores the submitted switch
	// independently.
	PasswordResetEnabled bool `json:"password_reset_enabled"`
	// nonwhitelisted domain registration-capacity switch
	RegistrationEmailDomainQuotaEnabled bool `json:"registration_email_domain_quota_enabled"`
	// additional normalized-mailbox occupancy switch
	RegistrationEmailNormalization bool `json:"registration_email_normalization"`
	// RegistrationEmailSuffixWhitelist supplies allowed mailbox suffixes.
	// Preparation normalizes a fresh list and encodes absent selections as an
	// empty JSON array; empty policy permits unrestricted suffixes.
	RegistrationEmailSuffixWhitelist []string `json:"registration_email_suffix_whitelist"`
	// ordinary account creation switch
	RegistrationEnabled bool `json:"registration_enabled"`
	// client-bound token validation switch
	SessionBindingEnabled bool `json:"session_binding_enabled"`
	// administrator sensitive-operation proof switch
	StepUpEnabled bool `json:"step_up_enabled"`
	// Tencent verification application identifier
	TencentCaptchaAppID string `json:"tencent_captcha_app_id"`
	// TencentCaptchaAppSecretKey is written for nonempty raw text. Exact empty
	// input omits its key, keeping the existing application challenge
	// credential.
	TencentCaptchaAppSecretKey string `json:"tencent_captcha_app_secret_key"`
	// TencentCaptchaCloudSecretID supplies the cloud credential identifier.
	// Exact empty input omits the write alongside the cloud-secret preservation
	// rule used by administrator preparation.
	TencentCaptchaCloudSecretID string `json:"tencent_captcha_cloud_secret_id"`
	// TencentCaptchaCloudSecretKey is saved for nonempty raw input. Exact empty
	// text omits the key, retaining current cloud verification credentials.
	TencentCaptchaCloudSecretKey string `json:"tencent_captcha_cloud_secret_key"`
	// Tencent challenge selection switch
	TencentCaptchaEnabled bool `json:"tencent_captcha_enabled"`
	// Tencent challenge site region
	TencentCaptchaRegion string `json:"tencent_captcha_region"`
	TotpEnabled          bool   `json:"totp_enabled"` // TOTP setup feature switch
	// Cloudflare challenge selection switch
	TurnstileEnabled bool `json:"turnstile_enabled"`
	// TurnstileSecretKey is saved only when raw text is nonempty. Exact empty
	// input omits the key; administrator readback can still report raw
	// credential presence.
	TurnstileSecretKey string `json:"turnstile_secret_key"`
	// Cloudflare browser challenge identifier
	TurnstileSiteKey string `json:"turnstile_site_key"`
	// assigned mailbox replacement policy switch
	UserEmailChangeEnabled bool `json:"user_email_change_enabled"`
	// WeChatConnectAppID and WeChatConnectAppSecret provide legacy credentials
	// used by missing channel fields. Preparation trims them first before
	// selecting open-platform, official-account and mobile effective credential
	// pairs.
	// legacy WeChat application identifier
	WeChatConnectAppID string `json:"wechat_connect_app_id"`
	// WeChatConnectAppSecret is trimmed before credential fallback and writing.
	// A remaining blank legacy secret omits its key; supplied text can also fill
	// missing channel secret values.
	WeChatConnectAppSecret string `json:"wechat_connect_app_secret"`
	// WeChat login selection switch
	WeChatConnectEnabled bool `json:"wechat_connect_enabled"`
	// WeChatConnectFrontendRedirectURL is trimmed before persistence. Blank
	// input selects OAuthDefaultWeChatConnectFrontend, with destination
	// validation handled by the effective configuration reader.
	WeChatConnectFrontendRedirectURL string `json:"wechat_connect_frontend_redirect_url"`
	// WeChatConnectMPAppID and WeChatConnectMPAppSecret describe one
	// official-account application. Preparation selects each nonblank channel
	// value before its corresponding legacy credential, keeping identifier and
	// secret fallback separate.
	// official-account WeChat application identifier
	WeChatConnectMPAppID string `json:"wechat_connect_mp_app_id"`
	// WeChatConnectMPAppSecret uses nonblank channel input or the legacy secret
	// during preparation. Only the remaining blank effective value omits its
	// write, so empty channel input can still produce a saved fallback.
	WeChatConnectMPAppSecret string `json:"wechat_connect_mp_app_secret"`
	// official-account WeChat capability switch
	WeChatConnectMPEnabled bool `json:"wechat_connect_mp_enabled"`
	// WeChatConnectMobileAppID and WeChatConnectMobileAppSecret describe one
	// mobile application. Preparation trims and fills each missing channel value
	// from its matching legacy credential before producing store updates.
	// mobile WeChat application identifier
	WeChatConnectMobileAppID string `json:"wechat_connect_mobile_app_id"`
	// WeChatConnectMobileAppSecret falls back to the legacy secret after
	// trimming. A nonblank effective value is saved even when the original
	// channel field was blank.
	WeChatConnectMobileAppSecret string `json:"wechat_connect_mobile_app_secret"`
	// mobile WeChat capability switch
	WeChatConnectMobileEnabled bool `json:"wechat_connect_mobile_enabled"`
	// WeChatConnectMode is normalized against the three capability switches. An
	// unsupported selection uses the first enabled open/MP/mobile capability.
	// When no capability is enabled, the normalized selection remains.
	WeChatConnectMode string `json:"wechat_connect_mode"`
	// WeChatConnectOpenAppID and WeChatConnectOpenAppSecret describe one
	// open-platform application. Preparation prefers their nonblank channel
	// inputs and then uses their separate legacy identifier/secret defaults.
	// open-platform WeChat application identifier
	WeChatConnectOpenAppID string `json:"wechat_connect_open_app_id"`
	// WeChatConnectOpenAppSecret takes nonblank channel input before the legacy
	// secret. Preparation trims the effective result and omits the key only when
	// that result remains blank.
	WeChatConnectOpenAppSecret string `json:"wechat_connect_open_app_secret"`
	// open-platform WeChat capability switch
	WeChatConnectOpenEnabled bool `json:"wechat_connect_open_enabled"`
	// WeChat backend callback address
	WeChatConnectRedirectURL string `json:"wechat_connect_redirect_url"`
	// WeChatConnectScopes is normalized for the selected mode before writing.
	// Official-account scope admits its known values, mobile uses empty text and
	// open-platform uses the configured login scope constant.
	WeChatConnectScopes string `json:"wechat_connect_scopes"`
}

// PrepareAdminSettings normalizes a required mutable administrator model and
// returns flat values for later persistence. Whitelist validation occurs
// first; selected WeChat/GitHub/Google values are trimmed/defaulted, and
// ordinary nonsecret values are emitted even when empty or zero.
//
// Secrets use their provider-specific omission rules. WeChat channel
// credentials can be filled from legacy values before secret omission, so an
// initially blank channel secret does not necessarily omit a write. The API
// Key allowance is validated after input mutation and map assembly; its
// rejection returns no map but leaves those normalized input changes. Callers
// use the settings participant's presence filter for selected-field updates
// and the coordinator for actual persistence.
func PrepareAdminSettings(settings *AdminSettings) (map[string]string, error) {
	updates := make(map[string]string)
	normalizedWhitelist, err := NormalizeRegistrationEmailSuffixWhitelist(settings.RegistrationEmailSuffixWhitelist)
	if err != nil {
		return nil, apperror.BadRequest("INVALID_REGISTRATION_EMAIL_SUFFIX_WHITELIST", err.Error())
	}
	// Normalize absent whitelist selections to the stored empty-array
	// representation, so later policy reads recognize an unrestricted list
	// instead of a null value.
	if normalizedWhitelist == nil {
		normalizedWhitelist = []string{}
	}
	settings.RegistrationEmailSuffixWhitelist = normalizedWhitelist
	settings.WeChatConnectAppID = strings.TrimSpace(settings.WeChatConnectAppID)
	settings.WeChatConnectAppSecret = strings.TrimSpace(settings.WeChatConnectAppSecret)
	// Resolve each channel identifier/secret from nonblank channel input before
	// its matching legacy value; later omission tests therefore see effective
	// credentials, not only the submitted channel text.
	settings.WeChatConnectOpenAppID = strings.TrimSpace(oauthSettingsFirstNonEmpty(settings.WeChatConnectOpenAppID, settings.WeChatConnectAppID))
	settings.WeChatConnectOpenAppSecret = strings.TrimSpace(oauthSettingsFirstNonEmpty(settings.WeChatConnectOpenAppSecret, settings.WeChatConnectAppSecret))
	settings.WeChatConnectMPAppID = strings.TrimSpace(oauthSettingsFirstNonEmpty(settings.WeChatConnectMPAppID, settings.WeChatConnectAppID))
	settings.WeChatConnectMPAppSecret = strings.TrimSpace(oauthSettingsFirstNonEmpty(settings.WeChatConnectMPAppSecret, settings.WeChatConnectAppSecret))
	settings.WeChatConnectMobileAppID = strings.TrimSpace(oauthSettingsFirstNonEmpty(settings.WeChatConnectMobileAppID, settings.WeChatConnectAppID))
	settings.WeChatConnectMobileAppSecret = strings.TrimSpace(oauthSettingsFirstNonEmpty(settings.WeChatConnectMobileAppSecret, settings.WeChatConnectAppSecret))
	// Select stored mode from current channel capabilities before normalizing
	// scope; scope follows that selected channel so incompatible
	// mobile/open/official-account text is not carried through preparation.
	settings.WeChatConnectMode = SettingsNormalizeWeChatConnectStoredMode(
		settings.WeChatConnectOpenEnabled,
		settings.WeChatConnectMPEnabled,
		settings.WeChatConnectMobileEnabled,
		settings.WeChatConnectMode,
	)
	settings.WeChatConnectScopes = SettingsNormalizeWeChatConnectScopeSetting(settings.WeChatConnectScopes, settings.WeChatConnectMode)
	settings.WeChatConnectRedirectURL = strings.TrimSpace(settings.WeChatConnectRedirectURL)
	settings.WeChatConnectFrontendRedirectURL = strings.TrimSpace(settings.WeChatConnectFrontendRedirectURL)
	if settings.WeChatConnectFrontendRedirectURL == "" {
		settings.WeChatConnectFrontendRedirectURL = OAuthDefaultWeChatConnectFrontend
	}
	settings.GitHubOAuthClientID = strings.TrimSpace(settings.GitHubOAuthClientID)
	settings.GitHubOAuthClientSecret = strings.TrimSpace(settings.GitHubOAuthClientSecret)
	settings.GitHubOAuthRedirectURL = strings.TrimSpace(settings.GitHubOAuthRedirectURL)
	settings.GitHubOAuthFrontendRedirectURL = strings.TrimSpace(settings.GitHubOAuthFrontendRedirectURL)
	if settings.GitHubOAuthFrontendRedirectURL == "" {
		settings.GitHubOAuthFrontendRedirectURL = OAuthDefaultGitHubOAuthFrontend
	}
	settings.GoogleOAuthClientID = strings.TrimSpace(settings.GoogleOAuthClientID)
	settings.GoogleOAuthClientSecret = strings.TrimSpace(settings.GoogleOAuthClientSecret)
	settings.GoogleOAuthRedirectURL = strings.TrimSpace(settings.GoogleOAuthRedirectURL)
	settings.GoogleOAuthFrontendRedirectURL = strings.TrimSpace(settings.GoogleOAuthFrontendRedirectURL)
	if settings.GoogleOAuthFrontendRedirectURL == "" {
		settings.GoogleOAuthFrontendRedirectURL = OAuthDefaultGoogleOAuthFrontend
	}
	updates[SettingKeyRegistrationEnabled] = strconv.FormatBool(settings.RegistrationEnabled)
	updates[SettingKeyEmailVerifyEnabled] = strconv.FormatBool(settings.EmailVerifyEnabled)
	updates[SettingKeyRegistrationEmailNormalization] = strconv.FormatBool(settings.RegistrationEmailNormalization)
	updates[SettingKeyRegistrationEmailDomainQuotaEnabled] = strconv.FormatBool(settings.RegistrationEmailDomainQuotaEnabled)
	updates[SettingKeyUserEmailChangeEnabled] = strconv.FormatBool(settings.UserEmailChangeEnabled)
	registrationEmailSuffixWhitelistJSON, err := json.Marshal(settings.RegistrationEmailSuffixWhitelist)
	if err != nil {
		return nil, fmt.Errorf("marshal registration email suffix whitelist: %w", err)
	}
	updates[SettingKeyRegistrationEmailSuffixWhitelist] = string(registrationEmailSuffixWhitelistJSON)
	updates[SettingKeyPasswordResetEnabled] = strconv.FormatBool(settings.PasswordResetEnabled)
	updates[SettingKeyTotpEnabled] = strconv.FormatBool(settings.TotpEnabled)
	updates[SettingKeySessionBindingEnabled] = strconv.FormatBool(settings.SessionBindingEnabled)
	updates[SettingKeyStepUpEnabled] = strconv.FormatBool(settings.StepUpEnabled)
	updates[SettingKeyTurnstileEnabled] = strconv.FormatBool(settings.TurnstileEnabled)
	updates[SettingKeyTurnstileSiteKey] = settings.TurnstileSiteKey
	// Turnstile, Tencent application/cloud, Aliyun, LinuxDo, DingTalk and OIDC
	// credential writes omit exactly empty raw values to retain saved secrets.
	// GitHub/Google/WeChat use their separately prepared trimmed values;
	// nonsecret fields continue through ordinary encoding.
	if settings.TurnstileSecretKey != "" {
		updates[SettingKeyTurnstileSecretKey] = settings.TurnstileSecretKey
	}
	updates[SettingKeyTencentCaptchaEnabled] = strconv.FormatBool(settings.TencentCaptchaEnabled)
	updates[SettingKeyTencentCaptchaAppID] = settings.TencentCaptchaAppID
	if settings.TencentCaptchaAppSecretKey != "" {
		updates[SettingKeyTencentCaptchaAppSecretKey] = settings.TencentCaptchaAppSecretKey
	}
	if settings.TencentCaptchaCloudSecretID != "" {
		updates[SettingKeyTencentCaptchaCloudSecretID] = settings.TencentCaptchaCloudSecretID
	}
	if settings.TencentCaptchaCloudSecretKey != "" {
		updates[SettingKeyTencentCaptchaCloudSecretKey] = settings.TencentCaptchaCloudSecretKey
	}
	// Use Tencent's shared region selector for persistence; exact intl selects
	// that site and other text takes the same CN interpretation used by runtime
	// verification.
	updates[SettingKeyTencentCaptchaRegion] = NormalizeTencentCaptchaRegion(settings.TencentCaptchaRegion)
	updates[SettingKeyAliyunCaptchaEnabled] = strconv.FormatBool(settings.AliyunCaptchaEnabled)
	updates[SettingKeyAliyunCaptchaAccessKeyID] = settings.AliyunCaptchaAccessKeyID
	if settings.AliyunCaptchaAccessKeySecret != "" {
		updates[SettingKeyAliyunCaptchaAccessKeySecret] = settings.AliyunCaptchaAccessKeySecret
	}
	updates[SettingKeyAliyunCaptchaSceneID] = settings.AliyunCaptchaSceneID
	updates[SettingKeyAliyunCaptchaPrefix] = settings.AliyunCaptchaPrefix
	// Use Aliyun's shared region selector before saving the label; exact sgp
	// chooses that site and other input retains the runtime verifier's CN
	// interpretation.
	updates[SettingKeyAliyunCaptchaRegion] = NormalizeAliyunCaptchaRegion(settings.AliyunCaptchaRegion)
	updates[SettingKeyLinuxDoConnectEnabled] = strconv.FormatBool(settings.LinuxDoConnectEnabled)
	updates[SettingKeyLinuxDoConnectClientID] = settings.LinuxDoConnectClientID
	updates[SettingKeyLinuxDoConnectRedirectURL] = settings.LinuxDoConnectRedirectURL
	if settings.LinuxDoConnectClientSecret != "" {
		updates[SettingKeyLinuxDoConnectClientSecret] = settings.LinuxDoConnectClientSecret
	}
	updates[SettingKeyDingTalkConnectEnabled] = strconv.FormatBool(settings.DingTalkConnectEnabled)
	updates[SettingKeyDingTalkConnectClientID] = settings.DingTalkConnectClientID
	updates[SettingKeyDingTalkConnectRedirectURL] = settings.DingTalkConnectRedirectURL
	if settings.DingTalkConnectClientSecret != "" {
		updates[SettingKeyDingTalkConnectClientSecret] = settings.DingTalkConnectClientSecret
	}
	updates[SettingKeyDingTalkConnectCorpRestrictionPolicy] = settings.DingTalkConnectCorpRestrictionPolicy
	updates[SettingKeyDingTalkConnectInternalCorpID] = settings.DingTalkConnectInternalCorpID
	updates[SettingKeyDingTalkConnectBypassRegistration] = strconv.FormatBool(settings.DingTalkConnectBypassRegistration)
	updates[SettingKeyDingTalkConnectSyncCorpEmail] = strconv.FormatBool(settings.DingTalkConnectSyncCorpEmail)
	updates[SettingKeyDingTalkConnectSyncDisplayName] = strconv.FormatBool(settings.DingTalkConnectSyncDisplayName)
	updates[SettingKeyDingTalkConnectSyncDept] = strconv.FormatBool(settings.DingTalkConnectSyncDept)
	updates[SettingKeyDingTalkConnectSyncCorpEmailAttrKey] = settings.DingTalkConnectSyncCorpEmailAttrKey
	updates[SettingKeyDingTalkConnectSyncDisplayNameAttrKey] = settings.DingTalkConnectSyncDisplayNameAttrKey
	updates[SettingKeyDingTalkConnectSyncDeptAttrKey] = settings.DingTalkConnectSyncDeptAttrKey
	updates[SettingKeyDingTalkConnectSyncCorpEmailAttrName] = settings.DingTalkConnectSyncCorpEmailAttrName
	updates[SettingKeyDingTalkConnectSyncDisplayNameAttrName] = settings.DingTalkConnectSyncDisplayNameAttrName
	updates[SettingKeyDingTalkConnectSyncDeptAttrName] = settings.DingTalkConnectSyncDeptAttrName
	updates[SettingKeyOIDCConnectEnabled] = strconv.FormatBool(settings.OIDCConnectEnabled)
	updates[SettingKeyOIDCConnectProviderName] = settings.OIDCConnectProviderName
	updates[SettingKeyOIDCConnectClientID] = settings.OIDCConnectClientID
	updates[SettingKeyOIDCConnectIssuerURL] = settings.OIDCConnectIssuerURL
	updates[SettingKeyOIDCConnectDiscoveryURL] = settings.OIDCConnectDiscoveryURL
	updates[SettingKeyOIDCConnectAuthorizeURL] = settings.OIDCConnectAuthorizeURL
	updates[SettingKeyOIDCConnectTokenURL] = settings.OIDCConnectTokenURL
	updates[SettingKeyOIDCConnectUserInfoURL] = settings.OIDCConnectUserInfoURL
	updates[SettingKeyOIDCConnectJWKSURL] = settings.OIDCConnectJWKSURL
	updates[SettingKeyOIDCConnectScopes] = settings.OIDCConnectScopes
	updates[SettingKeyOIDCConnectRedirectURL] = settings.OIDCConnectRedirectURL
	updates[SettingKeyOIDCConnectFrontendRedirectURL] = settings.OIDCConnectFrontendRedirectURL
	updates[SettingKeyOIDCConnectTokenAuthMethod] = settings.OIDCConnectTokenAuthMethod
	updates[SettingKeyOIDCConnectUsePKCE] = strconv.FormatBool(settings.OIDCConnectUsePKCE)
	updates[SettingKeyOIDCConnectValidateIDToken] = strconv.FormatBool(settings.OIDCConnectValidateIDToken)
	updates[SettingKeyOIDCConnectAllowedSigningAlgs] = settings.OIDCConnectAllowedSigningAlgs
	updates[SettingKeyOIDCConnectClockSkewSeconds] = strconv.Itoa(settings.OIDCConnectClockSkewSeconds)
	updates[SettingKeyOIDCConnectRequireEmailVerified] = strconv.FormatBool(settings.OIDCConnectRequireEmailVerified)
	updates[SettingKeyOIDCConnectUserInfoEmailPath] = settings.OIDCConnectUserInfoEmailPath
	updates[SettingKeyOIDCConnectUserInfoIDPath] = settings.OIDCConnectUserInfoIDPath
	updates[SettingKeyOIDCConnectUserInfoUsernamePath] = settings.OIDCConnectUserInfoUsernamePath
	if settings.OIDCConnectClientSecret != "" {
		updates[SettingKeyOIDCConnectClientSecret] = settings.OIDCConnectClientSecret
	}
	updates[SettingKeyGitHubOAuthEnabled] = strconv.FormatBool(settings.GitHubOAuthEnabled)
	updates[SettingKeyGitHubOAuthClientID] = settings.GitHubOAuthClientID
	updates[SettingKeyGitHubOAuthRedirectURL] = settings.GitHubOAuthRedirectURL
	updates[SettingKeyGitHubOAuthFrontendRedirectURL] = settings.GitHubOAuthFrontendRedirectURL
	// The earlier trim makes blank GitHub secret input an omitted key, retaining
	// saved credentials while selected nonsecret callback/client fields still
	// receive their prepared values.
	if settings.GitHubOAuthClientSecret != "" {
		updates[SettingKeyGitHubOAuthClientSecret] = settings.GitHubOAuthClientSecret
	}
	updates[SettingKeyGoogleOAuthEnabled] = strconv.FormatBool(settings.GoogleOAuthEnabled)
	updates[SettingKeyGoogleOneTapEnabled] = strconv.FormatBool(settings.GoogleOneTapEnabled)
	updates[SettingKeyGoogleOAuthClientID] = settings.GoogleOAuthClientID
	updates[SettingKeyGoogleOAuthRedirectURL] = settings.GoogleOAuthRedirectURL
	updates[SettingKeyGoogleOAuthFrontendRedirectURL] = settings.GoogleOAuthFrontendRedirectURL
	// Google secret omission follows its earlier trim; a blank effective value
	// keeps existing credential storage while submitted nonsecret fields can
	// still be persisted.
	if settings.GoogleOAuthClientSecret != "" {
		updates[SettingKeyGoogleOAuthClientSecret] = settings.GoogleOAuthClientSecret
	}
	updates[SettingKeyWeChatConnectEnabled] = strconv.FormatBool(settings.WeChatConnectEnabled)
	updates[SettingKeyWeChatConnectAppID] = settings.WeChatConnectAppID
	updates[SettingKeyWeChatConnectOpenAppID] = settings.WeChatConnectOpenAppID
	updates[SettingKeyWeChatConnectMPAppID] = settings.WeChatConnectMPAppID
	updates[SettingKeyWeChatConnectMobileAppID] = settings.WeChatConnectMobileAppID
	updates[SettingKeyWeChatConnectOpenEnabled] = strconv.FormatBool(settings.WeChatConnectOpenEnabled)
	updates[SettingKeyWeChatConnectMPEnabled] = strconv.FormatBool(settings.WeChatConnectMPEnabled)
	updates[SettingKeyWeChatConnectMobileEnabled] = strconv.FormatBool(settings.WeChatConnectMobileEnabled)
	updates[SettingKeyWeChatConnectMode] = settings.WeChatConnectMode
	updates[SettingKeyWeChatConnectScopes] = settings.WeChatConnectScopes
	updates[SettingKeyWeChatConnectRedirectURL] = settings.WeChatConnectRedirectURL
	updates[SettingKeyWeChatConnectFrontendRedirectURL] = settings.WeChatConnectFrontendRedirectURL
	// Write only nonblank effective WeChat secrets after legacy/channel
	// fallback, so a blank submitted channel secret may still save the inherited
	// legacy value.
	if settings.WeChatConnectAppSecret != "" {
		updates[SettingKeyWeChatConnectAppSecret] = settings.WeChatConnectAppSecret
	}
	if settings.WeChatConnectOpenAppSecret != "" {
		updates[SettingKeyWeChatConnectOpenAppSecret] = settings.WeChatConnectOpenAppSecret
	}
	if settings.WeChatConnectMPAppSecret != "" {
		updates[SettingKeyWeChatConnectMPAppSecret] = settings.WeChatConnectMPAppSecret
	}
	if settings.WeChatConnectMobileAppSecret != "" {
		updates[SettingKeyWeChatConnectMobileAppSecret] = settings.WeChatConnectMobileAppSecret
	}
	updates[SettingKeyDefaultConcurrency] = strconv.Itoa(settings.DefaultConcurrency)
	updates[SettingKeyDefaultUserRPMLimit] = strconv.Itoa(settings.DefaultUserRPMLimit)
	if !IsValidUserAPIKeyLimit(settings.DefaultUserAPIKeyLimit) {
		return nil, ErrUserAPIKeyLimitInvalid
	}
	updates[SettingKeyDefaultUserAPIKeyLimit] = strconv.Itoa(settings.DefaultUserAPIKeyLimit)
	return updates, nil
}
