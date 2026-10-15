package identity

import (
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/identity/authconfig"
)

// admin_settings_read.go builds privileged authentication settings views from
// stored values and startup defaults.
// Administrator settings can display a secret and its presence flag together,
// so this view belongs to privileged settings readback. Raw CAPTCHA presence
// differs from provider credential fallback, and a displayed enabled flag
// does not establish that effective provider validation will accept every
// required endpoint or credential.
//
// ReadAdminSettings combines supplied flat values with retained startup
// defaults and provider helpers. Missing, empty and explicit zero have
// different consequences for concurrency, OIDC skew and userinfo paths, while
// password reset is displayed only with mailbox verification enabled. The
// conversion performs no persistence or provider discovery; callers retain
// the current settings observation and use the dedicated effective readers
// before starting authentication.

// AdminReadSettings carries privileged authentication values and observed
// credential-presence flags for administration.
type AdminReadSettings struct {
	// AdminSettings contains the privileged effective values returned by this
	// conversion, including secret text. Public authentication views use their
	// separately selected fields.
	AdminSettings
	// AliyunCaptchaAccessKeySecretConfigured,
	// TencentCaptchaAppSecretKeyConfigured,
	// TencentCaptchaCloudSecretIDConfigured,
	// TencentCaptchaCloudSecretKeyConfigured and TurnstileSecretKeyConfigured
	// test raw nonempty stored text. DingTalkConnectClientSecretConfigured,
	// GitHubOAuthClientSecretConfigured, GoogleOAuthClientSecretConfigured,
	// LinuxDoConnectClientSecretConfigured, OIDCConnectClientSecretConfigured,
	// WeChatConnectAppSecretConfigured, WeChatConnectMPAppSecretConfigured,
	// WeChatConnectMobileAppSecretConfigured and
	// WeChatConnectOpenAppSecretConfigured test their effective secrets after
	// trimmed/startup fallback. These presence observations do not establish
	// complete provider configuration readiness.
	AliyunCaptchaAccessKeySecretConfigured bool // raw Aliyun secret presence
	DingTalkConnectClientSecretConfigured  bool // effective DingTalk secret presence
	GitHubOAuthClientSecretConfigured      bool // effective GitHub secret presence
	GoogleOAuthClientSecretConfigured      bool // effective Google secret presence
	LinuxDoConnectClientSecretConfigured   bool // effective LinuxDo secret presence
	OIDCConnectClientSecretConfigured      bool // effective OIDC secret presence
	TencentCaptchaAppSecretKeyConfigured   bool // raw Tencent application-secret presence
	TencentCaptchaCloudSecretIDConfigured  bool // raw Tencent cloud-identifier presence
	TencentCaptchaCloudSecretKeyConfigured bool // raw Tencent cloud-secret presence
	TurnstileSecretKeyConfigured           bool // raw Cloudflare secret presence
	WeChatConnectAppSecretConfigured       bool // effective legacy WeChat secret presence
	WeChatConnectMPAppSecretConfigured     bool // effective official-account secret presence
	WeChatConnectMobileAppSecretConfigured bool // effective mobile WeChat secret presence
	WeChatConnectOpenAppSecretConfigured   bool // effective open-platform secret presence
}

// ReadAdminSettings builds an administrator view from a supplied flat
// settings observation and retained startup defaults. The receiver is
// required and defaultConcurrency must be callable when its stored text
// cannot parse; parsed zero/negative concurrency remains visible here even
// though signup reading has its own positive-value rule.
//
// The view contains credential text and separate presence flags. CAPTCHA
// presence checks raw nonempty strings, while provider secret fields use
// effective trimmed/startup fallback; flags do not establish complete
// provider readiness. OIDC keeps a parsed stored skew zero, but
// absent/unusable storage with startup zero displays 120. Present userinfo
// path keys can clear defaults, and LinuxDo/DingTalk startup string fallback
// can retain its raw representation. No store query, provider discovery or
// persistence occurs in this conversion.
func (s *OAuthSettings) ReadAdminSettings(settings map[string]string, defaultConcurrency func() int) *AdminReadSettings {
	emailVerifyEnabled := settings[SettingKeyEmailVerifyEnabled] == "true"
	result := &AdminReadSettings{}
	result.RegistrationEnabled = settings[SettingKeyRegistrationEnabled] == "true"
	result.EmailVerifyEnabled = emailVerifyEnabled
	result.RegistrationEmailSuffixWhitelist = ParseRegistrationEmailSuffixWhitelist(settings[SettingKeyRegistrationEmailSuffixWhitelist])
	result.RegistrationEmailNormalization = settings[SettingKeyRegistrationEmailNormalization] == "true"
	result.RegistrationEmailDomainQuotaEnabled = settings[SettingKeyRegistrationEmailDomainQuotaEnabled] == "true"
	result.UserEmailChangeEnabled = settings[SettingKeyUserEmailChangeEnabled] == "true"
	result.PasswordResetEnabled = emailVerifyEnabled && settings[SettingKeyPasswordResetEnabled] == "true"
	result.TotpEnabled = settings[SettingKeyTotpEnabled] == "true"
	result.SessionBindingEnabled = settings[SettingKeySessionBindingEnabled] == "true"
	result.StepUpEnabled = settings[SettingKeyStepUpEnabled] == "true"
	result.TurnstileEnabled = settings[SettingKeyTurnstileEnabled] == "true"
	result.TurnstileSiteKey = settings[SettingKeyTurnstileSiteKey]
	result.TurnstileSecretKeyConfigured = settings[SettingKeyTurnstileSecretKey] != ""
	result.TencentCaptchaEnabled = settings[SettingKeyTencentCaptchaEnabled] == "true"
	result.TencentCaptchaAppID = settings[SettingKeyTencentCaptchaAppID]
	result.TencentCaptchaAppSecretKeyConfigured = settings[SettingKeyTencentCaptchaAppSecretKey] != ""
	result.TencentCaptchaCloudSecretIDConfigured = settings[SettingKeyTencentCaptchaCloudSecretID] != ""
	result.TencentCaptchaCloudSecretKeyConfigured = settings[SettingKeyTencentCaptchaCloudSecretKey] != ""
	result.TencentCaptchaRegion = NormalizeTencentCaptchaRegion(settings[SettingKeyTencentCaptchaRegion])
	result.AliyunCaptchaEnabled = settings[SettingKeyAliyunCaptchaEnabled] == "true"
	result.AliyunCaptchaAccessKeyID = settings[SettingKeyAliyunCaptchaAccessKeyID]
	result.AliyunCaptchaAccessKeySecretConfigured = settings[SettingKeyAliyunCaptchaAccessKeySecret] != ""
	result.AliyunCaptchaSceneID = settings[SettingKeyAliyunCaptchaSceneID]
	result.AliyunCaptchaPrefix = settings[SettingKeyAliyunCaptchaPrefix]
	result.AliyunCaptchaRegion = NormalizeAliyunCaptchaRegion(settings[SettingKeyAliyunCaptchaRegion])
	result.DefaultUserAPIKeyLimit = DefaultUserAPIKeyLimit
	// Unparseable concurrency selects the supplied default callback; a
	// successful zero or negative parse remains an administrator observation
	// rather than being replaced by signup admission defaults.
	if concurrency, err := strconv.Atoi(settings[SettingKeyDefaultConcurrency]); err == nil {
		result.DefaultConcurrency = concurrency
	} else {
		result.DefaultConcurrency = defaultConcurrency()
	}
	// Unusable or negative RPM text leaves the initialized unlimited value; this
	// administrator view does not replace it with the separate concurrency
	// callback default.
	if rpm, err := strconv.Atoi(settings[SettingKeyDefaultUserRPMLimit]); err == nil && rpm >= 0 {
		result.DefaultUserRPMLimit = rpm
	}
	// Missing, malformed or out-of-range Key allowance retains the built-in
	// default; a valid zero remains present and preserves the configured
	// unlimited representation.
	if limit, err := strconv.Atoi(settings[SettingKeyDefaultUserAPIKeyLimit]); err == nil && IsValidUserAPIKeyLimit(limit) {
		result.DefaultUserAPIKeyLimit = limit
	}
	result.TurnstileSecretKey = settings[SettingKeyTurnstileSecretKey]
	result.TencentCaptchaAppSecretKey = settings[SettingKeyTencentCaptchaAppSecretKey]
	result.TencentCaptchaCloudSecretID = settings[SettingKeyTencentCaptchaCloudSecretID]
	result.TencentCaptchaCloudSecretKey = settings[SettingKeyTencentCaptchaCloudSecretKey]
	result.AliyunCaptchaAccessKeySecret = settings[SettingKeyAliyunCaptchaAccessKeySecret]
	linuxDoBase := authconfig.LinuxDoConnectConfig{}
	if s.defaults != nil {
		linuxDoBase = s.defaults.LinuxDo
	}
	if raw, ok := settings[SettingKeyLinuxDoConnectEnabled]; ok {
		result.LinuxDoConnectEnabled = raw == "true"
	} else {
		result.LinuxDoConnectEnabled = linuxDoBase.Enabled
	}
	if v, ok := settings[SettingKeyLinuxDoConnectClientID]; ok && strings.TrimSpace(v) != "" {
		result.LinuxDoConnectClientID = strings.TrimSpace(v)
	} else {
		result.LinuxDoConnectClientID = linuxDoBase.ClientID
	}
	if v, ok := settings[SettingKeyLinuxDoConnectRedirectURL]; ok && strings.TrimSpace(v) != "" {
		result.LinuxDoConnectRedirectURL = strings.TrimSpace(v)
	} else {
		result.LinuxDoConnectRedirectURL = linuxDoBase.RedirectURL
	}
	result.LinuxDoConnectClientSecret = strings.TrimSpace(settings[SettingKeyLinuxDoConnectClientSecret])
	// Blank stored LinuxDo secret uses trimmed startup text for privileged
	// readback and its presence flag, while the identifier/callback fallback
	// keeps its own raw startup representation.
	if result.LinuxDoConnectClientSecret == "" {
		result.LinuxDoConnectClientSecret = strings.TrimSpace(linuxDoBase.ClientSecret)
	}
	result.LinuxDoConnectClientSecretConfigured = result.LinuxDoConnectClientSecret != ""
	dingTalkBase := authconfig.DingTalkConnectConfig{}
	if s.defaults != nil {
		dingTalkBase = s.defaults.DingTalk
	}
	if raw, ok := settings[SettingKeyDingTalkConnectEnabled]; ok {
		result.DingTalkConnectEnabled = raw == "true"
	} else {
		result.DingTalkConnectEnabled = dingTalkBase.Enabled
	}
	if v, ok := settings[SettingKeyDingTalkConnectClientID]; ok && strings.TrimSpace(v) != "" {
		result.DingTalkConnectClientID = strings.TrimSpace(v)
	} else {
		result.DingTalkConnectClientID = dingTalkBase.ClientID
	}
	if v, ok := settings[SettingKeyDingTalkConnectRedirectURL]; ok && strings.TrimSpace(v) != "" {
		result.DingTalkConnectRedirectURL = strings.TrimSpace(v)
	} else {
		result.DingTalkConnectRedirectURL = dingTalkBase.RedirectURL
	}
	result.DingTalkConnectClientSecret = strings.TrimSpace(settings[SettingKeyDingTalkConnectClientSecret])
	// Blank stored DingTalk secret uses trimmed startup text before computing
	// its presence flag, keeping effective readback independent of the raw
	// CAPTCHA presence checks.
	if result.DingTalkConnectClientSecret == "" {
		result.DingTalkConnectClientSecret = strings.TrimSpace(dingTalkBase.ClientSecret)
	}
	result.DingTalkConnectClientSecretConfigured = result.DingTalkConnectClientSecret != ""
	if v, ok := settings[SettingKeyDingTalkConnectCorpRestrictionPolicy]; ok && strings.TrimSpace(v) != "" {
		result.DingTalkConnectCorpRestrictionPolicy = strings.TrimSpace(v)
	} else {
		result.DingTalkConnectCorpRestrictionPolicy = dingTalkBase.CorpRestrictionPolicy
	}
	// A deprecated exact whitelist policy is displayed as none with a diagnostic
	// warning; later bypass/synchronization selection therefore uses the current
	// accepted organization-policy label.
	result.DingTalkConnectCorpRestrictionPolicy = SettingsCoerceDeprecatedDingTalkCorpPolicy(result.DingTalkConnectCorpRestrictionPolicy)
	if v, ok := settings[SettingKeyDingTalkConnectInternalCorpID]; ok && strings.TrimSpace(v) != "" {
		result.DingTalkConnectInternalCorpID = strings.TrimSpace(v)
	} else {
		result.DingTalkConnectInternalCorpID = dingTalkBase.InternalCorpID
	}
	if v, ok := settings[SettingKeyDingTalkConnectBypassRegistration]; ok && strings.TrimSpace(v) != "" {
		result.DingTalkConnectBypassRegistration = strings.EqualFold(strings.TrimSpace(v), "true")
	} else {
		result.DingTalkConnectBypassRegistration = dingTalkBase.BypassRegistration
	}
	// Organization policy governs the displayed bypass flag; clear it outside
	// internal_only so readback does not advertise an exception that the
	// authentication predicate cannot use.
	if result.DingTalkConnectCorpRestrictionPolicy != "internal_only" {
		result.DingTalkConnectBypassRegistration = false
	}
	if v, ok := settings[SettingKeyDingTalkConnectSyncCorpEmail]; ok && strings.TrimSpace(v) != "" {
		result.DingTalkConnectSyncCorpEmail = strings.EqualFold(strings.TrimSpace(v), "true")
	} else {
		result.DingTalkConnectSyncCorpEmail = dingTalkBase.SyncCorpEmail
	}
	if v, ok := settings[SettingKeyDingTalkConnectSyncDisplayName]; ok && strings.TrimSpace(v) != "" {
		result.DingTalkConnectSyncDisplayName = strings.EqualFold(strings.TrimSpace(v), "true")
	} else {
		result.DingTalkConnectSyncDisplayName = dingTalkBase.SyncDisplayName
	}
	if v, ok := settings[SettingKeyDingTalkConnectSyncDept]; ok && strings.TrimSpace(v) != "" {
		result.DingTalkConnectSyncDept = strings.EqualFold(strings.TrimSpace(v), "true")
	} else {
		result.DingTalkConnectSyncDept = dingTalkBase.SyncDept
	}
	// Disable enterprise-email, display-name and department synchronization
	// outside internal_only policy while retaining their destination key/label
	// values for administrator display and later configuration.
	if result.DingTalkConnectCorpRestrictionPolicy != "internal_only" {
		result.DingTalkConnectSyncCorpEmail = false
		result.DingTalkConnectSyncDisplayName = false
		result.DingTalkConnectSyncDept = false
	}
	// Choose each synchronization destination key and label from readable
	// stored, startup and built-in candidates; absent/blank text cannot clear
	// these administrator destination defaults.
	result.DingTalkConnectSyncCorpEmailAttrKey = oauthSettingsFirstNonEmpty(
		settings[SettingKeyDingTalkConnectSyncCorpEmailAttrKey],
		dingTalkBase.SyncCorpEmailAttrKey,
		"dingtalk_email",
	)
	result.DingTalkConnectSyncDisplayNameAttrKey = oauthSettingsFirstNonEmpty(
		settings[SettingKeyDingTalkConnectSyncDisplayNameAttrKey],
		dingTalkBase.SyncDisplayNameAttrKey,
		"dingtalk_name",
	)
	result.DingTalkConnectSyncDeptAttrKey = oauthSettingsFirstNonEmpty(
		settings[SettingKeyDingTalkConnectSyncDeptAttrKey],
		dingTalkBase.SyncDeptAttrKey,
		"dingtalk_department",
	)
	result.DingTalkConnectSyncCorpEmailAttrName = oauthSettingsFirstNonEmpty(
		settings[SettingKeyDingTalkConnectSyncCorpEmailAttrName],
		dingTalkBase.SyncCorpEmailAttrName,
		"钉钉企业邮箱",
	)
	result.DingTalkConnectSyncDisplayNameAttrName = oauthSettingsFirstNonEmpty(
		settings[SettingKeyDingTalkConnectSyncDisplayNameAttrName],
		dingTalkBase.SyncDisplayNameAttrName,
		"钉钉姓名",
	)
	result.DingTalkConnectSyncDeptAttrName = oauthSettingsFirstNonEmpty(
		settings[SettingKeyDingTalkConnectSyncDeptAttrName],
		dingTalkBase.SyncDeptAttrName,
		"钉钉部门",
	)
	oidcBase := authconfig.OIDCConnectConfig{}
	if s.defaults != nil {
		oidcBase = s.defaults.OIDC
	}
	if raw, ok := settings[SettingKeyOIDCConnectEnabled]; ok {
		result.OIDCConnectEnabled = raw == "true"
	} else {
		result.OIDCConnectEnabled = oidcBase.Enabled
	}
	result.OIDCConnectProviderName = oauthSettingsFirstNonEmpty(settings[SettingKeyOIDCConnectProviderName], oidcBase.ProviderName)
	if result.OIDCConnectProviderName == "" {
		result.OIDCConnectProviderName = "OIDC"
	}
	result.OIDCConnectClientID = oauthSettingsFirstNonEmpty(settings[SettingKeyOIDCConnectClientID], oidcBase.ClientID)
	result.OIDCConnectIssuerURL = oauthSettingsFirstNonEmpty(settings[SettingKeyOIDCConnectIssuerURL], oidcBase.IssuerURL)
	result.OIDCConnectDiscoveryURL = oauthSettingsFirstNonEmpty(settings[SettingKeyOIDCConnectDiscoveryURL], oidcBase.DiscoveryURL)
	result.OIDCConnectAuthorizeURL = oauthSettingsFirstNonEmpty(settings[SettingKeyOIDCConnectAuthorizeURL], oidcBase.AuthorizeURL)
	result.OIDCConnectTokenURL = oauthSettingsFirstNonEmpty(settings[SettingKeyOIDCConnectTokenURL], oidcBase.TokenURL)
	result.OIDCConnectUserInfoURL = oauthSettingsFirstNonEmpty(settings[SettingKeyOIDCConnectUserInfoURL], oidcBase.UserInfoURL)
	result.OIDCConnectJWKSURL = oauthSettingsFirstNonEmpty(settings[SettingKeyOIDCConnectJWKSURL], oidcBase.JWKSURL)
	result.OIDCConnectScopes = oauthSettingsFirstNonEmpty(settings[SettingKeyOIDCConnectScopes], oidcBase.Scopes)
	result.OIDCConnectRedirectURL = oauthSettingsFirstNonEmpty(settings[SettingKeyOIDCConnectRedirectURL], oidcBase.RedirectURL)
	result.OIDCConnectFrontendRedirectURL = oauthSettingsFirstNonEmpty(
		settings[SettingKeyOIDCConnectFrontendRedirectURL],
		oidcBase.FrontendRedirectURL,
	)
	if v, ok := settings[SettingKeyOIDCConnectTokenAuthMethod]; ok && strings.TrimSpace(v) != "" {
		result.OIDCConnectTokenAuthMethod = strings.ToLower(strings.TrimSpace(v))
	} else {
		result.OIDCConnectTokenAuthMethod = strings.ToLower(strings.TrimSpace(oidcBase.TokenAuthMethod))
	}
	// Stored presence decides PKCE and ID-token validation switches even for
	// empty text; omission uses startup presence-aware compatibility defaults
	// for the two independent security controls.
	if raw, ok := settings[SettingKeyOIDCConnectUsePKCE]; ok {
		result.OIDCConnectUsePKCE = raw == "true"
	} else {
		result.OIDCConnectUsePKCE = SettingsOidcUsePKCECompatibilityDefault(oidcBase)
	}
	if raw, ok := settings[SettingKeyOIDCConnectValidateIDToken]; ok {
		result.OIDCConnectValidateIDToken = raw == "true"
	} else {
		result.OIDCConnectValidateIDToken = SettingsOidcValidateIDTokenCompatibilityDefault(oidcBase)
	}
	result.OIDCConnectAllowedSigningAlgs = oauthSettingsFirstNonEmpty(
		settings[SettingKeyOIDCConnectAllowedSigningAlgs],
		oidcBase.AllowedSigningAlgs,
	)
	// Remember successful stored skew parsing so explicit zero stays visible;
	// only missing/unusable storage selects startup skew and its zero-to-120
	// administrator display fallback.
	clockSkewSet := false
	if raw, ok := settings[SettingKeyOIDCConnectClockSkewSeconds]; ok && strings.TrimSpace(raw) != "" {
		if parsed, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			result.OIDCConnectClockSkewSeconds = parsed
			clockSkewSet = true
		}
	}
	if !clockSkewSet {
		result.OIDCConnectClockSkewSeconds = oidcBase.ClockSkewSeconds
	}
	if !clockSkewSet && result.OIDCConnectClockSkewSeconds == 0 {
		result.OIDCConnectClockSkewSeconds = 120
	}
	if raw, ok := settings[SettingKeyOIDCConnectRequireEmailVerified]; ok {
		result.OIDCConnectRequireEmailVerified = raw == "true"
	} else {
		result.OIDCConnectRequireEmailVerified = oidcBase.RequireEmailVerified
	}
	// Userinfo path presence allows intentional clearing, so a stored empty path
	// overrides startup text while omitted email/subject/username paths retain
	// their selected startup candidates.
	if v, ok := settings[SettingKeyOIDCConnectUserInfoEmailPath]; ok {
		result.OIDCConnectUserInfoEmailPath = strings.TrimSpace(v)
	} else {
		result.OIDCConnectUserInfoEmailPath = strings.TrimSpace(oidcBase.UserInfoEmailPath)
	}
	if v, ok := settings[SettingKeyOIDCConnectUserInfoIDPath]; ok {
		result.OIDCConnectUserInfoIDPath = strings.TrimSpace(v)
	} else {
		result.OIDCConnectUserInfoIDPath = strings.TrimSpace(oidcBase.UserInfoIDPath)
	}
	if v, ok := settings[SettingKeyOIDCConnectUserInfoUsernamePath]; ok {
		result.OIDCConnectUserInfoUsernamePath = strings.TrimSpace(v)
	} else {
		result.OIDCConnectUserInfoUsernamePath = strings.TrimSpace(oidcBase.UserInfoUsernamePath)
	}
	result.OIDCConnectClientSecret = strings.TrimSpace(settings[SettingKeyOIDCConnectClientSecret])
	// Blank stored OIDC secret uses trimmed startup credentials before the
	// presence flag, keeping administrator readback separate from
	// endpoint/discovery and token-validation readiness checks.
	if result.OIDCConnectClientSecret == "" {
		result.OIDCConnectClientSecret = strings.TrimSpace(oidcBase.ClientSecret)
	}
	result.OIDCConnectClientSecretConfigured = result.OIDCConnectClientSecret != ""
	gitHubEffective := s.EffectiveEmailOAuthConfig(settings, "github")
	result.GitHubOAuthEnabled = gitHubEffective.Enabled
	result.GitHubOAuthClientID = strings.TrimSpace(gitHubEffective.ClientID)
	result.GitHubOAuthClientSecret = strings.TrimSpace(gitHubEffective.ClientSecret)
	result.GitHubOAuthClientSecretConfigured = result.GitHubOAuthClientSecret != ""
	result.GitHubOAuthRedirectURL = strings.TrimSpace(gitHubEffective.RedirectURL)
	result.GitHubOAuthFrontendRedirectURL = strings.TrimSpace(gitHubEffective.FrontendRedirectURL)
	googleEffective := s.EffectiveEmailOAuthConfig(settings, "google")
	result.GoogleOAuthEnabled = googleEffective.Enabled
	result.GoogleOneTapEnabled = settings[SettingKeyGoogleOneTapEnabled] == "true"
	result.GoogleOAuthClientID = strings.TrimSpace(googleEffective.ClientID)
	result.GoogleOAuthClientSecret = strings.TrimSpace(googleEffective.ClientSecret)
	result.GoogleOAuthClientSecretConfigured = result.GoogleOAuthClientSecret != ""
	result.GoogleOAuthRedirectURL = strings.TrimSpace(googleEffective.RedirectURL)
	result.GoogleOAuthFrontendRedirectURL = strings.TrimSpace(googleEffective.FrontendRedirectURL)
	weChatEffective := s.EffectiveWeChatConnectOAuthConfig(settings)
	result.WeChatConnectEnabled = weChatEffective.Enabled
	result.WeChatConnectAppID = weChatEffective.LegacyAppID
	result.WeChatConnectAppSecret = weChatEffective.LegacyAppSecret
	result.WeChatConnectAppSecretConfigured = weChatEffective.LegacyAppSecret != ""
	result.WeChatConnectOpenAppID = weChatEffective.OpenAppID
	result.WeChatConnectOpenAppSecret = weChatEffective.OpenAppSecret
	result.WeChatConnectOpenAppSecretConfigured = weChatEffective.OpenAppSecret != ""
	result.WeChatConnectMPAppID = weChatEffective.MPAppID
	result.WeChatConnectMPAppSecret = weChatEffective.MPAppSecret
	result.WeChatConnectMPAppSecretConfigured = weChatEffective.MPAppSecret != ""
	result.WeChatConnectMobileAppID = weChatEffective.MobileAppID
	result.WeChatConnectMobileAppSecret = weChatEffective.MobileAppSecret
	result.WeChatConnectMobileAppSecretConfigured = weChatEffective.MobileAppSecret != ""
	result.WeChatConnectOpenEnabled = weChatEffective.OpenEnabled
	result.WeChatConnectMPEnabled = weChatEffective.MPEnabled
	result.WeChatConnectMobileEnabled = weChatEffective.MobileEnabled
	result.WeChatConnectMode = weChatEffective.Mode
	result.WeChatConnectScopes = weChatEffective.Scopes
	result.WeChatConnectRedirectURL = weChatEffective.RedirectURL
	result.WeChatConnectFrontendRedirectURL = weChatEffective.FrontendRedirectURL
	return result
}
