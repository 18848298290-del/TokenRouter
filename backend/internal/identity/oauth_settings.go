package identity

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/identity/authconfig"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// OAuth configuration distinguishes an absent setting from an explicit false
// or an empty string that cannot replace a startup value. WeChat credential
// pairs resolve their identifier and secret independently through
// legacy/channel candidates, while its capability merge can restore startup
// flags for a specific absent-global/all-false saved state.
//
// OIDC compatibility readers enable omitted security switches, but
// administrator write defaults select saved or explicit startup values and
// otherwise false. Required missing endpoints can invoke provider discovery
// without replacing already configured ones; missing userinfo alone does not
// trigger it. Action readers validate effective settings before the provider
// client uses them, whereas public hints and administrator readback
// intentionally select smaller observations. Startup value snapshots clone
// DingTalk's mutable string slice, retaining store and resolver dependencies
// for later reads.

// These persistence keys select current provider credentials, callbacks and
// security options, while built-in endpoint/scope constants supply the known
// GitHub, Google and WeChat base configuration.
const (
	SettingKeyDingTalkConnectBypassRegistration     = "dingtalk_connect_bypass_registration"
	SettingKeyDingTalkConnectClientID               = "dingtalk_connect_client_id"
	SettingKeyDingTalkConnectClientSecret           = "dingtalk_connect_client_secret"
	SettingKeyDingTalkConnectCorpRestrictionPolicy  = "dingtalk_connect_corp_restriction_policy"
	SettingKeyDingTalkConnectInternalCorpID         = "dingtalk_connect_internal_corp_id"
	SettingKeyDingTalkConnectRedirectURL            = "dingtalk_connect_redirect_url"
	SettingKeyDingTalkConnectSyncCorpEmail          = "dingtalk_connect_sync_corp_email"
	SettingKeyDingTalkConnectSyncCorpEmailAttrKey   = "dingtalk_connect_sync_corp_email_attr_key"
	SettingKeyDingTalkConnectSyncDept               = "dingtalk_connect_sync_dept"
	SettingKeyDingTalkConnectSyncDeptAttrKey        = "dingtalk_connect_sync_dept_attr_key"
	SettingKeyDingTalkConnectSyncDisplayName        = "dingtalk_connect_sync_display_name"
	SettingKeyDingTalkConnectSyncDisplayNameAttrKey = "dingtalk_connect_sync_display_name_attr_key"
	SettingKeyGitHubOAuthClientID                   = "github_oauth_client_id"
	SettingKeyGitHubOAuthClientSecret               = "github_oauth_client_secret"
	SettingKeyGitHubOAuthEnabled                    = "github_oauth_enabled"
	SettingKeyGitHubOAuthFrontendRedirectURL        = "github_oauth_frontend_redirect_url"
	SettingKeyGitHubOAuthRedirectURL                = "github_oauth_redirect_url"
	SettingKeyGoogleOAuthClientID                   = "google_oauth_client_id"
	SettingKeyGoogleOAuthClientSecret               = "google_oauth_client_secret"
	SettingKeyGoogleOAuthEnabled                    = "google_oauth_enabled"
	SettingKeyGoogleOAuthFrontendRedirectURL        = "google_oauth_frontend_redirect_url"
	SettingKeyGoogleOAuthRedirectURL                = "google_oauth_redirect_url"
	SettingKeyGoogleOneTapEnabled                   = "google_one_tap_enabled"
	SettingKeyLinuxDoConnectClientID                = "linuxdo_connect_client_id"
	SettingKeyLinuxDoConnectClientSecret            = "linuxdo_connect_client_secret"
	SettingKeyLinuxDoConnectRedirectURL             = "linuxdo_connect_redirect_url"
	SettingKeyOIDCConnectAllowedSigningAlgs         = "oidc_connect_allowed_signing_algs"
	SettingKeyOIDCConnectAuthorizeURL               = "oidc_connect_authorize_url"
	SettingKeyOIDCConnectClientID                   = "oidc_connect_client_id"
	SettingKeyOIDCConnectClientSecret               = "oidc_connect_client_secret"
	SettingKeyOIDCConnectClockSkewSeconds           = "oidc_connect_clock_skew_seconds"
	SettingKeyOIDCConnectDiscoveryURL               = "oidc_connect_discovery_url"
	SettingKeyOIDCConnectFrontendRedirectURL        = "oidc_connect_frontend_redirect_url"
	SettingKeyOIDCConnectIssuerURL                  = "oidc_connect_issuer_url"
	SettingKeyOIDCConnectJWKSURL                    = "oidc_connect_jwks_url"
	SettingKeyOIDCConnectProviderName               = "oidc_connect_provider_name"
	SettingKeyOIDCConnectRedirectURL                = "oidc_connect_redirect_url"
	SettingKeyOIDCConnectRequireEmailVerified       = "oidc_connect_require_email_verified"
	SettingKeyOIDCConnectScopes                     = "oidc_connect_scopes"
	SettingKeyOIDCConnectTokenAuthMethod            = "oidc_connect_token_auth_method"
	SettingKeyOIDCConnectTokenURL                   = "oidc_connect_token_url"
	SettingKeyOIDCConnectUsePKCE                    = "oidc_connect_use_pkce"
	SettingKeyOIDCConnectUserInfoEmailPath          = "oidc_connect_userinfo_email_path"
	SettingKeyOIDCConnectUserInfoIDPath             = "oidc_connect_userinfo_id_path"
	SettingKeyOIDCConnectUserInfoURL                = "oidc_connect_userinfo_url"
	SettingKeyOIDCConnectUserInfoUsernamePath       = "oidc_connect_userinfo_username_path"
	SettingKeyOIDCConnectValidateIDToken            = "oidc_connect_validate_id_token"
	SettingKeyWeChatConnectAppID                    = "wechat_connect_app_id"
	SettingKeyWeChatConnectAppSecret                = "wechat_connect_app_secret"
	SettingKeyWeChatConnectFrontendRedirectURL      = "wechat_connect_frontend_redirect_url"
	SettingKeyWeChatConnectMPAppID                  = "wechat_connect_mp_app_id"
	SettingKeyWeChatConnectMPAppSecret              = "wechat_connect_mp_app_secret"
	SettingKeyWeChatConnectMobileAppID              = "wechat_connect_mobile_app_id"
	SettingKeyWeChatConnectMobileAppSecret          = "wechat_connect_mobile_app_secret"
	SettingKeyWeChatConnectOpenAppID                = "wechat_connect_open_app_id"
	SettingKeyWeChatConnectOpenAppSecret            = "wechat_connect_open_app_secret"
	SettingKeyWeChatConnectRedirectURL              = "wechat_connect_redirect_url"
	SettingKeyWeChatConnectScopes                   = "wechat_connect_scopes"
	OAuthDefaultGitHubOAuthAuthorize                = "https://github.com/login/oauth/authorize"
	OAuthDefaultGitHubOAuthEmails                   = "https://api.github.com/user/emails"
	OAuthDefaultGitHubOAuthFrontend                 = "/auth/oauth/callback"
	OAuthDefaultGitHubOAuthScopes                   = "read:user user:email"
	OAuthDefaultGitHubOAuthToken                    = "https://github.com/login/oauth/access_token"
	OAuthDefaultGitHubOAuthUserInfo                 = "https://api.github.com/user"
	OAuthDefaultGoogleOAuthAuthorize                = "https://accounts.google.com/o/oauth2/v2/auth"
	OAuthDefaultGoogleOAuthFrontend                 = "/auth/oauth/callback"
	OAuthDefaultGoogleOAuthScopes                   = "openid email profile"
	OAuthDefaultGoogleOAuthToken                    = "https://oauth2.googleapis.com/token"
	OAuthDefaultGoogleOAuthUserInfo                 = "https://openidconnect.googleapis.com/v1/userinfo"
	OAuthDefaultWeChatConnectFrontend               = "/auth/wechat/callback"
	OAuthDefaultWeChatConnectScopes                 = "snsapi_login"
)

// OAuthSettingsDefaults carries the startup provider values snapshotted for
// later runtime merging.
type OAuthSettingsDefaults struct {
	LinuxDo authconfig.LinuxDoConnectConfig // startup LinuxDo authorization defaults
	// DingTalk supplies startup organization and sync defaults. NewOAuthSettings
	// clones its AttributeSyncFields string slice so later caller edits do not
	// change the retained snapshot.
	DingTalk    authconfig.DingTalkConnectConfig
	OIDC        authconfig.OIDCConnectConfig        // startup OIDC security/endpoint defaults
	WeChat      authconfig.WeChatConnectConfig      // startup WeChat channel defaults
	GitHubOAuth authconfig.EmailOAuthProviderConfig // startup GitHub email-provider defaults
	GoogleOAuth authconfig.EmailOAuthProviderConfig // startup Google email-provider defaults
}

// OAuthSettingsStore supplies flat provider settings for effective
// configuration reads.
type OAuthSettingsStore interface {
	// GetMultiple reads selected provider keys into a flat string map. Missing
	// keys remain absent for their presence/default rules; batch failures remain
	// errors before action configuration can be accepted.
	GetMultiple(context.Context, []string) (map[string]string, error)
}

// OAuthSettings retains startup snapshots and current store/discovery readers
// for OAuth configuration selection.
type OAuthSettings struct {
	settingRepo OAuthSettingsStore // current provider setting reader
	// defaults owns copied startup provider values when supplied. Its DingTalk
	// string slice is independently cloned, while nil snapshot is rejected by
	// the action readers that require startup configuration.
	defaults *OAuthSettingsDefaults
	// resolveMetadata is required when OIDC lacks an authorization/token
	// endpoint or required JWKS address. The callback receives request context
	// and returns nonnil usable endpoint metadata on success.
	resolveMetadata func(context.Context, string) (*OAuthProviderMetadata, error)
}

// OAuthProviderMetadata carries endpoint addresses returned by the configured
// OIDC discovery resolver.
type OAuthProviderMetadata struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"` // provider browser authorization endpoint
	TokenEndpoint         string `json:"token_endpoint"`         // provider code exchange endpoint
	UserInfoEndpoint      string `json:"userinfo_endpoint"`      // provider profile lookup endpoint
	JWKSURI               string `json:"jwks_uri"`               // provider signing-key metadata endpoint
}

// WeChatConnectOAuthConfig carries effective global/channel flags,
// credentials and callback observations.
type WeChatConnectOAuthConfig struct {
	// Enabled is the effective global switch. Capability flags may still be true
	// when it is false, so parser/public-readiness operations apply this global
	// gate separately.
	Enabled bool
	// LegacyAppID and LegacyAppSecret are independently resolved credential
	// fallbacks for OpenAppID/OpenAppSecret, MPAppID/MPAppSecret and
	// MobileAppID/MobileAppSecret. Channel accessors select their own nonblank
	// value before the matching legacy text.
	LegacyAppID     string // effective legacy application identifier
	LegacyAppSecret string // effective legacy application secret
	OpenAppID       string // effective open-platform application identifier
	OpenAppSecret   string // effective open-platform application secret
	MPAppID         string // effective official-account application identifier
	MPAppSecret     string // effective official-account application secret
	MobileAppID     string // effective mobile application identifier
	MobileAppSecret string // effective mobile application secret
	// OpenEnabled, MPEnabled and MobileEnabled describe separate configured
	// channels. SupportsMode reads their flag, while readiness additionally
	// needs effective credentials and the global Enabled switch.
	OpenEnabled   bool // configured open-platform capability switch
	MPEnabled     bool // configured official-account capability switch
	MobileEnabled bool // configured mobile capability switch
	// Mode and Scopes carry the selected normalized channel and its scope
	// representation. ScopeForMode reinterprets scope for the requested channel,
	// including empty mobile and fixed open-platform scope.
	Mode                string // normalized selected channel mode
	Scopes              string // selected channel authorization scope
	RedirectURL         string // selected backend callback address
	FrontendRedirectURL string // selected frontend return destination
}

// NewOAuthSettings retains the store/resolver and copies optional startup
// values into an owned snapshot. DingTalk's string slice is cloned so later
// caller slice edits do not change defaults; methods requiring discovery also
// need a resolver returning usable metadata.
func NewOAuthSettings(repo OAuthSettingsStore, defaults *OAuthSettingsDefaults, resolver func(context.Context, string) (*OAuthProviderMetadata, error)) *OAuthSettings {
	var snapshot *OAuthSettingsDefaults
	if defaults != nil {
		value := *defaults
		// Clone the startup string slice while copying value-only provider fields,
		// preventing later caller slice edits from changing the service's retained
		// DingTalk defaults.
		value.DingTalk.AttributeSyncFields = slices.Clone(value.DingTalk.AttributeSyncFields)
		snapshot = &value
	}
	return &OAuthSettings{settingRepo: repo, defaults: snapshot, resolveMetadata: resolver}
}

// SettingsCoerceDingTalkCorpPolicyForWrite delegates the deprecated-policy
// conversion used during administration. Exact whitelist becomes none and
// emits the shared warning; other input passes through as supplied, with
// validation owned by the enclosing settings workflow.
func SettingsCoerceDingTalkCorpPolicyForWrite(policy string) string {
	return SettingsCoerceDeprecatedDingTalkCorpPolicy(policy)
}

// SettingsCoerceDeprecatedDingTalkCorpPolicy maps exact whitelist to none
// with a diagnostic warning. It performs no trimming or case folding and
// leaves other text unchanged, so callers normalize or validate surrounding
// policy input themselves.
func SettingsCoerceDeprecatedDingTalkCorpPolicy(policy string) string {
	// The deprecated whitelist label has no supported organization policy;
	// expose none with a warning before callers validate or apply current
	// internal-only behavior.
	if policy == "whitelist" {
		slog.Warn("dingtalk: corp_restriction_policy=whitelist is deprecated and unsupported, coercing to none",
			"hint", "re-save DingTalk settings in admin UI to clear this warning")
		return "none"
	}
	return policy
}

// SettingsDefaultWeChatConnectScopeForMode selects scope text for a
// normalized channel. Official-account uses snsapi_userinfo, mobile uses
// empty text and other normalized modes use the open-platform login constant;
// this choice does not check capability enablement.
func SettingsDefaultWeChatConnectScopeForMode(mode string) string {
	// Scope defaults follow the normalized channel used by authorization URLs,
	// so official-account scope cannot become the default for mobile or
	// open-platform ceremonies.
	switch NormalizeWeChatConnectModeSetting(mode) {
	case "mp":
		return "snsapi_userinfo"
	case "mobile":
		return ""
	}
	return OAuthDefaultWeChatConnectScopes
}

// SettingsNormalizeWeChatConnectScopeSetting chooses compatible scope text
// for the normalized mode. Official-account accepts trimmed exact snsapi_base
// or snsapi_userinfo and defaults otherwise; mobile ignores supplied scope
// and returns empty, while open-platform always selects its login constant.
//
// The supplied scope therefore cannot carry an incompatible channel setting
// through this conversion. Callers keep mode aligned with the channel they
// actually authorize, and use the separate capability/credential checks
// before starting that flow. Scope selection alone performs no provider
// request or browser admission and can be used for disabled configuration
// readback.
func SettingsNormalizeWeChatConnectScopeSetting(raw, mode string) string {
	// Interpret scope under the normalized channel, replacing incompatible text
	// with that channel's accepted/default representation so mobile or
	// open-platform cannot retain official-account-only scope.
	switch NormalizeWeChatConnectModeSetting(mode) {
	case "mp":
		switch strings.TrimSpace(raw) {
		case "snsapi_base":
			return "snsapi_base"
		case "snsapi_userinfo":
			return "snsapi_userinfo"
		default:
			return SettingsDefaultWeChatConnectScopeForMode(mode)
		}
	case "mobile":
		return ""
	default:
		return OAuthDefaultWeChatConnectScopes
	}
}

// SettingsNormalizeWeChatConnectStoredMode retains a normalized requested
// channel when its capability is enabled. Otherwise it selects the first
// enabled channel in open, official-account, mobile order; when none is
// enabled the normalized requested mode remains.
//
// The input flags are configuration observations rather than credential
// readiness. Callers separately apply the global enablement and validate
// credentials for the eventual action. This selection lets stored/readback
// mode follow usable configured capabilities while keeping an explicit
// normalized mode available even when every capability is disabled.
func SettingsNormalizeWeChatConnectStoredMode(openEnabled, mpEnabled, mobileEnabled bool, mode string) string {
	mode = NormalizeWeChatConnectModeSetting(mode)
	switch mode {
	case "open":
		if openEnabled {
			return "open"
		}
	case "mp":
		if mpEnabled {
			return "mp"
		}
	case "mobile":
		if mobileEnabled {
			return "mobile"
		}
	}
	// The requested channel was not enabled; select the ordered enabled
	// capability while leaving normalized mode available when the configured set
	// has no enabled channel.
	switch {
	case openEnabled:
		return "open"
	case mpEnabled:
		return "mp"
	case mobileEnabled:
		return "mobile"
	default:
		return mode
	}
}

// SettingsMergeWeChatConnectCapabilitySettings merges channel switches with
// startup capability observations. Any nonblank saved channel value chooses
// the saved set using trimmed exact true, but an absent global-enabled key
// plus enabled=true and an all-false saved set restores nonempty startup
// capabilities.
//
// Without configured channel switches, disabled input returns all false,
// existing startup capabilities are retained, and remaining cases use legacy
// mode-based parsing. A configured saved set can retain true channel flags
// even when enabled is false; callers apply the global gate separately. The
// special all-false restoration depends on key presence, so absent global
// text differs from an explicitly stored global key.
func SettingsMergeWeChatConnectCapabilitySettings(settings map[string]string, base authconfig.WeChatConnectConfig, enabled bool, mode string) (bool, bool, bool) {
	// Absent usable mode text takes startup mode before channel normalization,
	// aligning the legacy capability parser with the same selected mode used for
	// readback.
	mode = NormalizeWeChatConnectModeSetting(oauthSettingsFirstNonEmpty(mode, base.Mode))
	rawOpen, hasOpen := settings[SettingKeyWeChatConnectOpenEnabled]
	rawMP, hasMP := settings[SettingKeyWeChatConnectMPEnabled]
	rawMobile, hasMobile := settings[SettingKeyWeChatConnectMobileEnabled]
	openConfigured := hasOpen && strings.TrimSpace(rawOpen) != ""
	mpConfigured := hasMP && strings.TrimSpace(rawMP) != ""
	mobileConfigured := hasMobile && strings.TrimSpace(rawMobile) != ""

	// Nonblank channel settings choose a saved capability set; the following
	// absent-global/all-false exception can still restore startup flags,
	// preserving this presence-dependent compatibility rule.
	if openConfigured || mpConfigured || mobileConfigured {
		openEnabled := strings.TrimSpace(rawOpen) == "true"
		mpEnabled := strings.TrimSpace(rawMP) == "true"
		mobileEnabled := strings.TrimSpace(rawMobile) == "true"
		_, enabledConfigured := settings[SettingKeyWeChatConnectEnabled]
		if !enabledConfigured &&
			enabled &&
			!openEnabled &&
			!mpEnabled &&
			!mobileEnabled &&
			(base.OpenEnabled || base.MPEnabled || base.MobileEnabled) {
			return base.OpenEnabled, base.MPEnabled, base.MobileEnabled
		}
		return openEnabled, mpEnabled, mobileEnabled
	}
	// With no saved channel set, global disablement wins before startup
	// capabilities or legacy mode parsing; configured saved sets above use their
	// separate presence rule.
	if !enabled {
		return false, false, false
	}
	if base.OpenEnabled || base.MPEnabled || base.MobileEnabled {
		return base.OpenEnabled, base.MPEnabled, base.MobileEnabled
	}
	return ParseWeChatConnectCapabilitySettings(settings, enabled, mode)
}

// EffectiveWeChatConnectOAuthConfig combines supplied flat values with
// optional startup WeChat defaults. Global enablement uses a present trimmed
// true value; legacy identifier/secret each chooses saved, startup legacy and
// startup channel candidates, then channel credentials select their own
// saved/startup value before legacy fallback.
//
// Capability merging and mode selection are separate from scope
// normalization. The returned strings are trimmed and frontend return text
// can use its built-in route, but this conversion does not establish
// credential/URL readiness. A nil receiver uses an empty startup base,
// allowing callers to prepare a readback view before
// ParseWeChatConnectOAuthConfig performs browser-flow admission.
func (s *OAuthSettings) EffectiveWeChatConnectOAuthConfig(settings map[string]string) WeChatConnectOAuthConfig {
	base := authconfig.WeChatConnectConfig{}
	if s != nil && s.defaults != nil {
		base = s.defaults.WeChat
	}

	enabled := base.Enabled
	if raw, ok := settings[SettingKeyWeChatConnectEnabled]; ok {
		enabled = strings.TrimSpace(raw) == "true"
	}

	// Resolve identifier and secret independently through saved legacy, startup
	// legacy and startup channel candidates before using each resolved value as
	// the corresponding channel fallback.
	legacyAppID := strings.TrimSpace(oauthSettingsFirstNonEmpty(
		settings[SettingKeyWeChatConnectAppID],
		base.AppID,
		base.OpenAppID,
		base.MPAppID,
		base.MobileAppID,
	))
	legacyAppSecret := strings.TrimSpace(oauthSettingsFirstNonEmpty(
		settings[SettingKeyWeChatConnectAppSecret],
		base.AppSecret,
		base.OpenAppSecret,
		base.MPAppSecret,
		base.MobileAppSecret,
	))
	openAppID := strings.TrimSpace(oauthSettingsFirstNonEmpty(settings[SettingKeyWeChatConnectOpenAppID], base.OpenAppID, legacyAppID))
	openAppSecret := strings.TrimSpace(oauthSettingsFirstNonEmpty(settings[SettingKeyWeChatConnectOpenAppSecret], base.OpenAppSecret, legacyAppSecret))
	mpAppID := strings.TrimSpace(oauthSettingsFirstNonEmpty(settings[SettingKeyWeChatConnectMPAppID], base.MPAppID, legacyAppID))
	mpAppSecret := strings.TrimSpace(oauthSettingsFirstNonEmpty(settings[SettingKeyWeChatConnectMPAppSecret], base.MPAppSecret, legacyAppSecret))
	mobileAppID := strings.TrimSpace(oauthSettingsFirstNonEmpty(settings[SettingKeyWeChatConnectMobileAppID], base.MobileAppID, legacyAppID))
	mobileAppSecret := strings.TrimSpace(oauthSettingsFirstNonEmpty(settings[SettingKeyWeChatConnectMobileAppSecret], base.MobileAppSecret, legacyAppSecret))

	// Mode and scope prefer usable saved text before startup values; callbacks
	// follow that order, with only the frontend destination receiving a built-in
	// route candidate.
	modeRaw := oauthSettingsFirstNonEmpty(settings[SettingKeyWeChatConnectMode], base.Mode)
	openEnabled, mpEnabled, mobileEnabled := SettingsMergeWeChatConnectCapabilitySettings(settings, base, enabled, modeRaw)
	mode := SettingsNormalizeWeChatConnectStoredMode(openEnabled, mpEnabled, mobileEnabled, modeRaw)

	return WeChatConnectOAuthConfig{
		Enabled:             enabled,
		LegacyAppID:         legacyAppID,
		LegacyAppSecret:     legacyAppSecret,
		OpenAppID:           openAppID,
		OpenAppSecret:       openAppSecret,
		MPAppID:             mpAppID,
		MPAppSecret:         mpAppSecret,
		MobileAppID:         mobileAppID,
		MobileAppSecret:     mobileAppSecret,
		OpenEnabled:         openEnabled,
		MPEnabled:           mpEnabled,
		MobileEnabled:       mobileEnabled,
		Mode:                mode,
		Scopes:              SettingsNormalizeWeChatConnectScopeSetting(oauthSettingsFirstNonEmpty(settings[SettingKeyWeChatConnectScopes], base.Scopes), mode),
		RedirectURL:         strings.TrimSpace(oauthSettingsFirstNonEmpty(settings[SettingKeyWeChatConnectRedirectURL], base.RedirectURL)),
		FrontendRedirectURL: strings.TrimSpace(oauthSettingsFirstNonEmpty(settings[SettingKeyWeChatConnectFrontendRedirectURL], base.FrontendRedirectURL, OAuthDefaultWeChatConnectFrontend)),
	}
}

// SettingsDefaultWeChatConnectScopesForMode delegates the normalized
// channel's default scope choice. Official-account, mobile and open-platform
// retain their separate text rules; callers check capability and credentials
// independently before using that scope in authorization.
func SettingsDefaultWeChatConnectScopesForMode(mode string) string {
	return SettingsDefaultWeChatConnectScopeForMode(mode)
}

// ParseWeChatConnectOAuthConfig validates an effective browser WeChat
// configuration. Global disablement or absence of both browser capability
// flags returns OAuth-disabled, so a mobile-only configuration cannot enable
// this browser flow.
//
// Every enabled channel, including mobile alongside a browser channel, needs
// its effective identifier and secret. Nonblank backend callback text is
// checked as absolute HTTP(S), and frontend return text receives its separate
// validation. The result retains configured flags and selected mode; callers
// still select a supported actual channel and establish provider/browser
// state before authorization.
func (s *OAuthSettings) ParseWeChatConnectOAuthConfig(settings map[string]string) (WeChatConnectOAuthConfig, error) {
	cfg := s.EffectiveWeChatConnectOAuthConfig(settings)

	if !cfg.Enabled || (!cfg.OpenEnabled && !cfg.MPEnabled) {
		return WeChatConnectOAuthConfig{}, apperror.NotFound("OAUTH_DISABLED", "wechat oauth is disabled")
	}
	if cfg.OpenEnabled {
		if cfg.AppIDForMode("open") == "" {
			return WeChatConnectOAuthConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "wechat oauth pc app id not configured")
		}
		if cfg.AppSecretForMode("open") == "" {
			return WeChatConnectOAuthConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "wechat oauth pc app secret not configured")
		}
	}
	if cfg.MPEnabled {
		if cfg.AppIDForMode("mp") == "" {
			return WeChatConnectOAuthConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "wechat oauth official account app id not configured")
		}
		if cfg.AppSecretForMode("mp") == "" {
			return WeChatConnectOAuthConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "wechat oauth official account app secret not configured")
		}
	}
	if cfg.MobileEnabled {
		if cfg.AppIDForMode("mobile") == "" {
			return WeChatConnectOAuthConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "wechat oauth mobile app id not configured")
		}
		if cfg.AppSecretForMode("mobile") == "" {
			return WeChatConnectOAuthConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "wechat oauth mobile app secret not configured")
		}
	}
	if v := strings.TrimSpace(cfg.RedirectURL); v != "" {
		if err := authconfig.ValidateAbsoluteHTTPURL(v); err != nil {
			return WeChatConnectOAuthConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "wechat oauth redirect url invalid")
		}
	}
	if err := authconfig.ValidateFrontendRedirectURL(cfg.FrontendRedirectURL); err != nil {
		return WeChatConnectOAuthConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "wechat oauth frontend redirect url invalid")
	}
	return cfg, nil
}

// WeChatOAuthCapabilitiesFromSettings returns browser-ready, open-ready,
// official-account-ready and mobile-ready flags in that order. Global
// disablement returns four false values; each remaining channel needs its
// flag and nonblank effective credentials, while URL validation belongs to the
// fuller reader. Provider connectivity is determined by later provider requests.
func (s *OAuthSettings) WeChatOAuthCapabilitiesFromSettings(settings map[string]string) (bool, bool, bool, bool) {
	cfg := s.EffectiveWeChatConnectOAuthConfig(settings)
	if !cfg.Enabled {
		return false, false, false, false
	}

	openReady := cfg.OpenEnabled && cfg.AppIDForMode("open") != "" && cfg.AppSecretForMode("open") != ""
	mpReady := cfg.MPEnabled && cfg.AppIDForMode("mp") != "" && cfg.AppSecretForMode("mp") != ""
	mobileReady := cfg.MobileEnabled && cfg.AppIDForMode("mobile") != "" && cfg.AppSecretForMode("mobile") != ""

	return openReady || mpReady, openReady, mpReady, mobileReady
}

// SettingsMergeEmailOAuthBaseConfig overlays startup GitHub/Google text onto
// built-in endpoint/scope defaults. Enabled is replaced even when false; each
// nonblank trimmed string replaces its base value, while blank input retains
// the prepared base.
//
// Identifier, secret and callbacks use that same selection alongside endpoint
// and scope text. These scalar configuration fields copy by value, and this
// merge validates no address or credentials. Callers use the resulting base
// for runtime overrides, where an explicit saved enablement key and nonblank
// saved credential/callback text can change the final action configuration.
func SettingsMergeEmailOAuthBaseConfig(base, override authconfig.EmailOAuthProviderConfig) authconfig.EmailOAuthProviderConfig {
	// Startup enablement replaces its base boolean directly, while only nonblank
	// trimmed text replaces endpoints/credentials; omission cannot clear the
	// built-in string candidates here.
	base.Enabled = override.Enabled
	if strings.TrimSpace(override.ClientID) != "" {
		base.ClientID = strings.TrimSpace(override.ClientID)
	}
	if strings.TrimSpace(override.ClientSecret) != "" {
		base.ClientSecret = strings.TrimSpace(override.ClientSecret)
	}
	if strings.TrimSpace(override.AuthorizeURL) != "" {
		base.AuthorizeURL = strings.TrimSpace(override.AuthorizeURL)
	}
	if strings.TrimSpace(override.TokenURL) != "" {
		base.TokenURL = strings.TrimSpace(override.TokenURL)
	}
	if strings.TrimSpace(override.UserInfoURL) != "" {
		base.UserInfoURL = strings.TrimSpace(override.UserInfoURL)
	}
	if strings.TrimSpace(override.EmailsURL) != "" {
		base.EmailsURL = strings.TrimSpace(override.EmailsURL)
	}
	if strings.TrimSpace(override.Scopes) != "" {
		base.Scopes = strings.TrimSpace(override.Scopes)
	}
	if strings.TrimSpace(override.RedirectURL) != "" {
		base.RedirectURL = strings.TrimSpace(override.RedirectURL)
	}
	if strings.TrimSpace(override.FrontendRedirectURL) != "" {
		base.FrontendRedirectURL = strings.TrimSpace(override.FrontendRedirectURL)
	}
	return base
}

// EmailOAuthPublicEnabled reports enabled plus nonblank effective client ID,
// secret and backend callback for the selected provider. This public hint
// performs no complete endpoint/frontend validation, so start handlers use
// GetEmailOAuthProviderConfig for full configuration admission.
func (s *OAuthSettings) EmailOAuthPublicEnabled(settings map[string]string, provider string) bool {
	cfg := s.EffectiveEmailOAuthConfig(settings, provider)
	return cfg.Enabled &&
		strings.TrimSpace(cfg.ClientID) != "" &&
		strings.TrimSpace(cfg.ClientSecret) != "" &&
		strings.TrimSpace(cfg.RedirectURL) != ""
}

// EffectiveEmailOAuthConfig applies recognized provider keys over
// built-in/startup values. A present enablement key requires exact true text,
// while saved identifier, secret and callback strings override only when
// nonblank after trimming.
//
// Blank saved strings therefore retain the prepared base and cannot clear it
// through this conversion. Unknown providers have no key mapping and return
// their empty base. The helper performs no store query or address validation;
// callers retain this distinction between an effective display hint and a
// configuration accepted by the provider-specific action reader.
func (s *OAuthSettings) EffectiveEmailOAuthConfig(settings map[string]string, provider string) authconfig.EmailOAuthProviderConfig {
	base := s.BaseEmailOAuthConfig(provider)
	enabledKey, clientIDKey, clientSecretKey, redirectURLKey, frontendRedirectURLKey := SettingsEmailOAuthSettingKeys(provider)
	if enabledKey == "" {
		return base
	}
	// Saved enablement uses key presence even for empty text;
	// credential/callback text overrides only when nonblank, retaining its
	// prepared base otherwise.
	if raw, ok := settings[enabledKey]; ok {
		base.Enabled = raw == "true"
	}
	if v, ok := settings[clientIDKey]; ok && strings.TrimSpace(v) != "" {
		base.ClientID = strings.TrimSpace(v)
	}
	if v, ok := settings[clientSecretKey]; ok && strings.TrimSpace(v) != "" {
		base.ClientSecret = strings.TrimSpace(v)
	}
	if v, ok := settings[redirectURLKey]; ok && strings.TrimSpace(v) != "" {
		base.RedirectURL = strings.TrimSpace(v)
	}
	if v, ok := settings[frontendRedirectURLKey]; ok && strings.TrimSpace(v) != "" {
		base.FrontendRedirectURL = strings.TrimSpace(v)
	}
	return base
}

// SettingsOidcUsePKCECompatibilityDefault returns startup PKCE when its
// presence marker is set. Otherwise omission enables the compatibility read
// default even when the bare startup bool is false, preserving the difference
// between omitted and explicitly false configuration.
func SettingsOidcUsePKCECompatibilityDefault(base authconfig.OIDCConnectConfig) bool {
	// The startup presence marker distinguishes intentional false from omission,
	// allowing disabled PKCE to survive the compatibility default used by action
	// configuration reads.
	if base.UsePKCEExplicit {
		return base.UsePKCE
	}
	return true
}

// SettingsOidcValidateIDTokenCompatibilityDefault returns startup validation
// when its presence marker is set. Otherwise the omitted startup switch
// selects true for compatibility reads; callers keep this separate from
// administrator write-default selection.
func SettingsOidcValidateIDTokenCompatibilityDefault(base authconfig.OIDCConnectConfig) bool {
	// An omitted startup switch enables compatibility validation when no saved
	// key replaces it; its presence marker keeps deliberate startup false
	// available to action readers.
	if base.ValidateIDTokenExplicit {
		return base.ValidateIDToken
	}
	return true
}

// SettingsOidcCompatibilityWriteDefault selects saved text, explicit startup
// value, then false in that order. Configured text is trimmed before exact
// true comparison; base itself is unused because callers supply its
// presence/value observations separately.
//
// This write-default rule intentionally differs from compatibility readers
// that enable an omitted security switch. A present empty saved key selects
// false and does not fall through to startup state. Callers use it when
// preparing administrator writes, while action readers keep their own raw
// saved-text and startup-presence interpretation.
func SettingsOidcCompatibilityWriteDefault(base authconfig.OIDCConnectConfig, configured bool, raw string, explicit bool, explicitValue bool) bool {
	// Write preparation gives saved key presence priority over startup
	// explicitness; its final false default stays separate from the
	// enabled-on-omission compatibility read rule.
	if configured {
		return strings.TrimSpace(raw) == "true"
	}
	if explicit {
		return explicitValue
	}
	return false
}

// OIDCSecurityWriteDefaults reads PKCE and ID-token validation keys together
// and returns their administrator write defaults. Store/receiver are required
// and read failure returns a wrapped error; absent startup snapshot supplies
// zero explicit markers, selecting false for an omitted saved switch.
func (s *OAuthSettings) OIDCSecurityWriteDefaults(ctx context.Context) (bool, bool, error) {
	rawSettings, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyOIDCConnectUsePKCE,
		SettingKeyOIDCConnectValidateIDToken,
	})
	if err != nil {
		return false, false, fmt.Errorf("get oidc security write defaults: %w", err)
	}

	base := authconfig.OIDCConnectConfig{}
	if s != nil && s.defaults != nil {
		base = s.defaults.OIDC
	}

	rawUsePKCE, hasUsePKCE := rawSettings[SettingKeyOIDCConnectUsePKCE]
	rawValidateIDToken, hasValidateIDToken := rawSettings[SettingKeyOIDCConnectValidateIDToken]

	return SettingsOidcCompatibilityWriteDefault(base, hasUsePKCE, rawUsePKCE, base.UsePKCEExplicit, base.UsePKCE),
		SettingsOidcCompatibilityWriteDefault(base, hasValidateIDToken, rawValidateIDToken, base.ValidateIDTokenExplicit, base.ValidateIDToken),
		nil
}

// GetEmailOAuthProviderConfig loads and validates GitHub or Google effective
// action settings. It requires receiver/startup snapshot/store, normalizes
// the provider label and rejects other labels; store failure remains wrapped
// and a disabled effective provider returns OAuth-disabled.
//
// Required credentials/endpoints/backend and frontend callbacks are checked
// before return, with GitHub additionally requiring its emails endpoint.
// HTTP(S) validation is separate from frontend destination validation. Public
// enabled hints perform fewer checks, so callers use this result before
// actual provider authorization rather than treating credential presence
// alone as readiness.
func (s *OAuthSettings) GetEmailOAuthProviderConfig(ctx context.Context, provider string) (authconfig.EmailOAuthProviderConfig, error) {
	if s == nil || s.defaults == nil {
		return authconfig.EmailOAuthProviderConfig{}, apperror.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider != "github" && provider != "google" {
		return authconfig.EmailOAuthProviderConfig{}, apperror.NotFound("OAUTH_PROVIDER_NOT_FOUND", "oauth provider not found")
	}

	enabledKey, clientIDKey, clientSecretKey, redirectURLKey, frontendRedirectURLKey := SettingsEmailOAuthSettingKeys(provider)
	keys := []string{enabledKey, clientIDKey, clientSecretKey, redirectURLKey, frontendRedirectURLKey}
	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return authconfig.EmailOAuthProviderConfig{}, fmt.Errorf("get email oauth settings: %w", err)
	}

	effective := s.EffectiveEmailOAuthConfig(settings, provider)
	if !effective.Enabled {
		return authconfig.EmailOAuthProviderConfig{}, apperror.NotFound("OAUTH_DISABLED", "oauth login is disabled")
	}
	if strings.TrimSpace(effective.ClientID) == "" {
		return authconfig.EmailOAuthProviderConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth client id not configured")
	}
	if strings.TrimSpace(effective.ClientSecret) == "" {
		return authconfig.EmailOAuthProviderConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth client secret not configured")
	}
	if strings.TrimSpace(effective.AuthorizeURL) == "" {
		return authconfig.EmailOAuthProviderConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth authorize url not configured")
	}
	if strings.TrimSpace(effective.TokenURL) == "" {
		return authconfig.EmailOAuthProviderConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth token url not configured")
	}
	if strings.TrimSpace(effective.UserInfoURL) == "" {
		return authconfig.EmailOAuthProviderConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth userinfo url not configured")
	}
	if provider == "github" && strings.TrimSpace(effective.EmailsURL) == "" {
		return authconfig.EmailOAuthProviderConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth emails url not configured")
	}
	if strings.TrimSpace(effective.RedirectURL) == "" {
		return authconfig.EmailOAuthProviderConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth redirect url not configured")
	}
	if strings.TrimSpace(effective.FrontendRedirectURL) == "" {
		return authconfig.EmailOAuthProviderConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth frontend redirect url not configured")
	}
	if err := authconfig.ValidateAbsoluteHTTPURL(effective.AuthorizeURL); err != nil {
		return authconfig.EmailOAuthProviderConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth authorize url invalid")
	}
	if err := authconfig.ValidateAbsoluteHTTPURL(effective.TokenURL); err != nil {
		return authconfig.EmailOAuthProviderConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth token url invalid")
	}
	if err := authconfig.ValidateAbsoluteHTTPURL(effective.UserInfoURL); err != nil {
		return authconfig.EmailOAuthProviderConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth userinfo url invalid")
	}
	if strings.TrimSpace(effective.EmailsURL) != "" {
		if err := authconfig.ValidateAbsoluteHTTPURL(effective.EmailsURL); err != nil {
			return authconfig.EmailOAuthProviderConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth emails url invalid")
		}
	}
	if err := authconfig.ValidateAbsoluteHTTPURL(effective.RedirectURL); err != nil {
		return authconfig.EmailOAuthProviderConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth redirect url invalid")
	}
	if err := authconfig.ValidateFrontendRedirectURL(effective.FrontendRedirectURL); err != nil {
		return authconfig.EmailOAuthProviderConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth frontend redirect url invalid")
	}
	return effective, nil
}

// GetGoogleOneTapConfig reads the exact saved One Tap switch before
// delegating Google OAuth configuration validation. Missing receiver/store is
// unavailable, absent or nontrue text is disabled, and startup OAuth settings
// are still required by the delegated reader.
func (s *OAuthSettings) GetGoogleOneTapConfig(ctx context.Context) (authconfig.EmailOAuthProviderConfig, error) {
	if s == nil || s.settingRepo == nil {
		return authconfig.EmailOAuthProviderConfig{}, apperror.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
	}
	settings, err := s.settingRepo.GetMultiple(ctx, []string{SettingKeyGoogleOneTapEnabled})
	if err != nil {
		return authconfig.EmailOAuthProviderConfig{}, fmt.Errorf("get google one tap setting: %w", err)
	}
	if settings[SettingKeyGoogleOneTapEnabled] != "true" {
		return authconfig.EmailOAuthProviderConfig{}, apperror.NotFound("OAUTH_DISABLED", "google one tap is disabled")
	}
	return s.GetEmailOAuthProviderConfig(ctx, "google")
}

// GetLinuxDoConnectOAuthConfig loads four runtime overrides over a required
// startup LinuxDo base. A present enabled key uses exact true, while nonblank
// trimmed identifier, secret and backend callback replace their startup text;
// other endpoint/profile/proof values remain startup selections.
//
// Disabled providers return OAuth-disabled. Required endpoints/callbacks and
// frontend destination are validated, while token authentication method
// decides whether a secret is required: none permits absence, blank or secret
// methods require it, and unsupported methods reject configuration. The
// returned value describes locally validated options; transport/provider
// proof follows later in the client flow.
func (s *OAuthSettings) GetLinuxDoConnectOAuthConfig(ctx context.Context) (authconfig.LinuxDoConnectConfig, error) {
	if s == nil || s.defaults == nil {
		return authconfig.LinuxDoConnectConfig{}, apperror.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
	}

	effective := s.defaults.LinuxDo

	keys := []string{
		SettingKeyLinuxDoConnectEnabled,
		SettingKeyLinuxDoConnectClientID,
		SettingKeyLinuxDoConnectClientSecret,
		SettingKeyLinuxDoConnectRedirectURL,
	}
	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return authconfig.LinuxDoConnectConfig{}, fmt.Errorf("get linuxdo connect settings: %w", err)
	}

	// Saved credential and callback text must be usable to replace startup
	// values; the global switch instead follows key presence, including an empty
	// stored value.
	if raw, ok := settings[SettingKeyLinuxDoConnectEnabled]; ok {
		effective.Enabled = raw == "true"
	}
	if v, ok := settings[SettingKeyLinuxDoConnectClientID]; ok && strings.TrimSpace(v) != "" {
		effective.ClientID = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyLinuxDoConnectClientSecret]; ok && strings.TrimSpace(v) != "" {
		effective.ClientSecret = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyLinuxDoConnectRedirectURL]; ok && strings.TrimSpace(v) != "" {
		effective.RedirectURL = strings.TrimSpace(v)
	}
	if !effective.Enabled {
		return authconfig.LinuxDoConnectConfig{}, apperror.NotFound("OAUTH_DISABLED", "oauth login is disabled")
	}

	if strings.TrimSpace(effective.ClientID) == "" {
		return authconfig.LinuxDoConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth client id not configured")
	}
	if strings.TrimSpace(effective.AuthorizeURL) == "" {
		return authconfig.LinuxDoConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth authorize url not configured")
	}
	if strings.TrimSpace(effective.TokenURL) == "" {
		return authconfig.LinuxDoConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth token url not configured")
	}
	if strings.TrimSpace(effective.UserInfoURL) == "" {
		return authconfig.LinuxDoConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth userinfo url not configured")
	}
	if strings.TrimSpace(effective.RedirectURL) == "" {
		return authconfig.LinuxDoConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth redirect url not configured")
	}
	if strings.TrimSpace(effective.FrontendRedirectURL) == "" {
		return authconfig.LinuxDoConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth frontend redirect url not configured")
	}

	if err := authconfig.ValidateAbsoluteHTTPURL(effective.AuthorizeURL); err != nil {
		return authconfig.LinuxDoConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth authorize url invalid")
	}
	if err := authconfig.ValidateAbsoluteHTTPURL(effective.TokenURL); err != nil {
		return authconfig.LinuxDoConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth token url invalid")
	}
	if err := authconfig.ValidateAbsoluteHTTPURL(effective.UserInfoURL); err != nil {
		return authconfig.LinuxDoConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth userinfo url invalid")
	}
	if err := authconfig.ValidateAbsoluteHTTPURL(effective.RedirectURL); err != nil {
		return authconfig.LinuxDoConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth redirect url invalid")
	}
	if err := authconfig.ValidateFrontendRedirectURL(effective.FrontendRedirectURL); err != nil {
		return authconfig.LinuxDoConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth frontend redirect url invalid")
	}

	// Blank token-authentication method joins the secret-bearing methods for
	// configuration admission; the none method permits public-client
	// credentials, while transport follows the accepted provider options.
	method := strings.ToLower(strings.TrimSpace(effective.TokenAuthMethod))
	switch method {
	case "", "client_secret_post", "client_secret_basic":
		if strings.TrimSpace(effective.ClientSecret) == "" {
			return authconfig.LinuxDoConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth client secret not configured")
		}
	case "none":
	default:
		return authconfig.LinuxDoConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth token_auth_method invalid")
	}

	return effective, nil
}

// GetDingTalkConnectOAuthConfig combines runtime DingTalk fields with a
// required startup base and independently cloned attribute-selection slice. Stored
// enablement uses exact true, selected nonblank strings override startup
// text, and bypass/sync booleans accept trimmed case-insensitive true when
// present and nonblank.
//
// Deprecated exact whitelist policy becomes none. Bypass and
// enterprise-email/display-name/department sync are cleared outside
// internal_only policy, while destination keys still receive their
// startup/built-in fallbacks. Required endpoint/credential validation
// precedes provider configuration validation, and internal_only selects
// effective internal AppType. Callers separately establish organization
// membership and upstream identity proof before using these options for
// account admission.
func (s *OAuthSettings) GetDingTalkConnectOAuthConfig(ctx context.Context) (authconfig.DingTalkConnectConfig, error) {
	if s == nil || s.defaults == nil {
		return authconfig.DingTalkConnectConfig{}, apperror.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
	}

	effective := s.defaults.DingTalk
	effective.AttributeSyncFields = slices.Clone(effective.AttributeSyncFields)

	keys := []string{
		SettingKeyDingTalkConnectEnabled,
		SettingKeyDingTalkConnectClientID,
		SettingKeyDingTalkConnectClientSecret,
		SettingKeyDingTalkConnectRedirectURL,
		SettingKeyDingTalkConnectCorpRestrictionPolicy,
		SettingKeyDingTalkConnectInternalCorpID,
		SettingKeyDingTalkConnectBypassRegistration,
		SettingKeyDingTalkConnectSyncCorpEmail,
		SettingKeyDingTalkConnectSyncDisplayName,
		SettingKeyDingTalkConnectSyncDept,
		SettingKeyDingTalkConnectSyncCorpEmailAttrKey,
		SettingKeyDingTalkConnectSyncDisplayNameAttrKey,
		SettingKeyDingTalkConnectSyncDeptAttrKey,
	}
	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return authconfig.DingTalkConnectConfig{}, fmt.Errorf("get dingtalk connect settings: %w", err)
	}

	// Nonblank saved provider text replaces startup selections before
	// organization-policy coercion; an empty secret retains its startup
	// credential rather than disabling the configured client.
	if raw, ok := settings[SettingKeyDingTalkConnectEnabled]; ok {
		effective.Enabled = raw == "true"
	}
	if v, ok := settings[SettingKeyDingTalkConnectClientID]; ok && strings.TrimSpace(v) != "" {
		effective.ClientID = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyDingTalkConnectClientSecret]; ok && strings.TrimSpace(v) != "" {
		effective.ClientSecret = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyDingTalkConnectRedirectURL]; ok && strings.TrimSpace(v) != "" {
		effective.RedirectURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyDingTalkConnectCorpRestrictionPolicy]; ok && strings.TrimSpace(v) != "" {
		effective.CorpRestrictionPolicy = strings.TrimSpace(v)
	}
	effective.CorpRestrictionPolicy = SettingsCoerceDeprecatedDingTalkCorpPolicy(effective.CorpRestrictionPolicy)
	if v, ok := settings[SettingKeyDingTalkConnectInternalCorpID]; ok && strings.TrimSpace(v) != "" {
		effective.InternalCorpID = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyDingTalkConnectBypassRegistration]; ok && strings.TrimSpace(v) != "" {
		effective.BypassRegistration = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	// Closed-registration bypass needs internal_only policy in the
	// authentication predicate; clear the effective flag elsewhere while
	// retaining its stored input for administration.
	if effective.CorpRestrictionPolicy != "internal_only" {
		effective.BypassRegistration = false
	}

	if v, ok := settings[SettingKeyDingTalkConnectSyncCorpEmail]; ok && strings.TrimSpace(v) != "" {
		effective.SyncCorpEmail = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	if v, ok := settings[SettingKeyDingTalkConnectSyncDisplayName]; ok && strings.TrimSpace(v) != "" {
		effective.SyncDisplayName = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	if v, ok := settings[SettingKeyDingTalkConnectSyncDept]; ok && strings.TrimSpace(v) != "" {
		effective.SyncDept = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	// Enterprise-mail, name and department synchronization use internal-only
	// eligibility in the sync service; clear their effective flags for other
	// policy while destination labels remain configured.
	if effective.CorpRestrictionPolicy != "internal_only" {
		effective.SyncCorpEmail = false
		effective.SyncDisplayName = false
		effective.SyncDept = false
	}

	// Resolve synchronization destinations from usable stored text before
	// startup/built-in candidates, keeping display keys available even when
	// organization policy disabled the corresponding sync flags.
	if v := strings.TrimSpace(settings[SettingKeyDingTalkConnectSyncCorpEmailAttrKey]); v != "" {
		effective.SyncCorpEmailAttrKey = v
	}
	if effective.SyncCorpEmailAttrKey == "" {
		effective.SyncCorpEmailAttrKey = "dingtalk_email"
	}
	if v := strings.TrimSpace(settings[SettingKeyDingTalkConnectSyncDisplayNameAttrKey]); v != "" {
		effective.SyncDisplayNameAttrKey = v
	}
	if effective.SyncDisplayNameAttrKey == "" {
		effective.SyncDisplayNameAttrKey = "dingtalk_name"
	}
	if v := strings.TrimSpace(settings[SettingKeyDingTalkConnectSyncDeptAttrKey]); v != "" {
		effective.SyncDeptAttrKey = v
	}
	if effective.SyncDeptAttrKey == "" {
		effective.SyncDeptAttrKey = "dingtalk_department"
	}

	if !effective.Enabled {
		return authconfig.DingTalkConnectConfig{}, apperror.NotFound("OAUTH_DISABLED", "dingtalk oauth login is disabled")
	}

	if strings.TrimSpace(effective.ClientID) == "" {
		return authconfig.DingTalkConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth client id not configured")
	}
	if strings.TrimSpace(effective.AuthorizeURL) == "" {
		return authconfig.DingTalkConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth authorize url not configured")
	}
	if strings.TrimSpace(effective.TokenURL) == "" {
		return authconfig.DingTalkConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth token url not configured")
	}
	if strings.TrimSpace(effective.UserInfoURL) == "" {
		return authconfig.DingTalkConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth userinfo url not configured")
	}
	if strings.TrimSpace(effective.RedirectURL) == "" {
		return authconfig.DingTalkConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth redirect url not configured")
	}
	if strings.TrimSpace(effective.FrontendRedirectURL) == "" {
		return authconfig.DingTalkConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth frontend redirect url not configured")
	}

	if err := authconfig.ValidateAbsoluteHTTPURL(effective.AuthorizeURL); err != nil {
		return authconfig.DingTalkConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth authorize url invalid")
	}
	if err := authconfig.ValidateAbsoluteHTTPURL(effective.TokenURL); err != nil {
		return authconfig.DingTalkConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth token url invalid")
	}
	if err := authconfig.ValidateAbsoluteHTTPURL(effective.UserInfoURL); err != nil {
		return authconfig.DingTalkConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth userinfo url invalid")
	}
	if err := authconfig.ValidateAbsoluteHTTPURL(effective.RedirectURL); err != nil {
		return authconfig.DingTalkConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth redirect url invalid")
	}
	if err := authconfig.ValidateFrontendRedirectURL(effective.FrontendRedirectURL); err != nil {
		return authconfig.DingTalkConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth frontend redirect url invalid")
	}
	if strings.TrimSpace(effective.ClientSecret) == "" {
		return authconfig.DingTalkConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "dingtalk oauth client secret not configured")
	}

	// The selected internal_only policy requires effective internal AppType for
	// configuration validation; select it before the final validator checks
	// enabled application-kind and policy requirements.
	if effective.CorpRestrictionPolicy == "internal_only" {
		effective.AppType = "internal"
	}

	if err := authconfig.ValidateDingTalkConfig(effective); err != nil {
		return authconfig.DingTalkConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", err.Error())
	}

	return effective, nil
}

// GetWeChatConnectOAuthConfig reads the WeChat credential/capability/callback
// keys in one batch and delegates browser configuration validation.
// Receiver/store are required, read failure remains wrapped, and the
// delegated parser decides disabled capabilities, effective credential
// completeness and URL validity.
func (s *OAuthSettings) GetWeChatConnectOAuthConfig(ctx context.Context) (WeChatConnectOAuthConfig, error) {
	keys := []string{
		SettingKeyWeChatConnectEnabled,
		SettingKeyWeChatConnectAppID,
		SettingKeyWeChatConnectAppSecret,
		SettingKeyWeChatConnectOpenAppID,
		SettingKeyWeChatConnectOpenAppSecret,
		SettingKeyWeChatConnectMPAppID,
		SettingKeyWeChatConnectMPAppSecret,
		SettingKeyWeChatConnectMobileAppID,
		SettingKeyWeChatConnectMobileAppSecret,
		SettingKeyWeChatConnectOpenEnabled,
		SettingKeyWeChatConnectMPEnabled,
		SettingKeyWeChatConnectMobileEnabled,
		SettingKeyWeChatConnectMode,
		SettingKeyWeChatConnectScopes,
		SettingKeyWeChatConnectRedirectURL,
		SettingKeyWeChatConnectFrontendRedirectURL,
	}
	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return WeChatConnectOAuthConfig{}, fmt.Errorf("get wechat connect settings: %w", err)
	}
	return s.ParseWeChatConnectOAuthConfig(settings)
}

// GetOIDCConnectOAuthConfig resolves and validates OIDC action settings from
// required startup/store dependencies. Nonblank strings override selected
// defaults, present security booleans use exact raw true, userinfo path
// presence permits clearing, and usable stored skew replaces the startup
// number without the administrator display fallback.
//
// Missing authorization/token endpoints or required JWKS selects provider
// discovery, whose resolver must return usable metadata. Only missing
// endpoints are filled, including userinfo if discovery runs for another
// required field; missing userinfo alone does not trigger discovery. Openid
// scope, zero-through-600 skew, callbacks/endpoints and validation-dependent
// signing keys/algorithms are checked before return. Token authentication
// mode determines secret necessity, and provider proof verification remains
// in the downstream client.
func (s *OAuthSettings) GetOIDCConnectOAuthConfig(ctx context.Context) (authconfig.OIDCConnectConfig, error) {
	if s == nil || s.defaults == nil {
		return authconfig.OIDCConnectConfig{}, apperror.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
	}

	effective := s.defaults.OIDC

	keys := []string{
		SettingKeyOIDCConnectEnabled,
		SettingKeyOIDCConnectProviderName,
		SettingKeyOIDCConnectClientID,
		SettingKeyOIDCConnectClientSecret,
		SettingKeyOIDCConnectIssuerURL,
		SettingKeyOIDCConnectDiscoveryURL,
		SettingKeyOIDCConnectAuthorizeURL,
		SettingKeyOIDCConnectTokenURL,
		SettingKeyOIDCConnectUserInfoURL,
		SettingKeyOIDCConnectJWKSURL,
		SettingKeyOIDCConnectScopes,
		SettingKeyOIDCConnectRedirectURL,
		SettingKeyOIDCConnectFrontendRedirectURL,
		SettingKeyOIDCConnectTokenAuthMethod,
		SettingKeyOIDCConnectUsePKCE,
		SettingKeyOIDCConnectValidateIDToken,
		SettingKeyOIDCConnectAllowedSigningAlgs,
		SettingKeyOIDCConnectClockSkewSeconds,
		SettingKeyOIDCConnectRequireEmailVerified,
		SettingKeyOIDCConnectUserInfoEmailPath,
		SettingKeyOIDCConnectUserInfoIDPath,
		SettingKeyOIDCConnectUserInfoUsernamePath,
	}
	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return authconfig.OIDCConnectConfig{}, fmt.Errorf("get oidc connect settings: %w", err)
	}

	// Keep startup endpoints and credential text when saved overrides are blank,
	// allowing explicitly configured endpoints to remain available before
	// missing-field discovery is considered.
	if raw, ok := settings[SettingKeyOIDCConnectEnabled]; ok {
		effective.Enabled = raw == "true"
	}
	if v, ok := settings[SettingKeyOIDCConnectProviderName]; ok && strings.TrimSpace(v) != "" {
		effective.ProviderName = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectClientID]; ok && strings.TrimSpace(v) != "" {
		effective.ClientID = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectClientSecret]; ok && strings.TrimSpace(v) != "" {
		effective.ClientSecret = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectIssuerURL]; ok && strings.TrimSpace(v) != "" {
		effective.IssuerURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectDiscoveryURL]; ok && strings.TrimSpace(v) != "" {
		effective.DiscoveryURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectAuthorizeURL]; ok && strings.TrimSpace(v) != "" {
		effective.AuthorizeURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectTokenURL]; ok && strings.TrimSpace(v) != "" {
		effective.TokenURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectUserInfoURL]; ok && strings.TrimSpace(v) != "" {
		effective.UserInfoURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectJWKSURL]; ok && strings.TrimSpace(v) != "" {
		effective.JWKSURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectScopes]; ok && strings.TrimSpace(v) != "" {
		effective.Scopes = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectRedirectURL]; ok && strings.TrimSpace(v) != "" {
		effective.RedirectURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectFrontendRedirectURL]; ok && strings.TrimSpace(v) != "" {
		effective.FrontendRedirectURL = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectTokenAuthMethod]; ok && strings.TrimSpace(v) != "" {
		effective.TokenAuthMethod = strings.ToLower(strings.TrimSpace(v))
	}
	// Saved PKCE and validation key presence selects exact raw true/false
	// interpretation; only omitted keys use startup presence-aware compatibility
	// defaults for these independent security choices.
	if raw, ok := settings[SettingKeyOIDCConnectUsePKCE]; ok {
		effective.UsePKCE = raw == "true"
	} else {
		effective.UsePKCE = SettingsOidcUsePKCECompatibilityDefault(effective)
	}
	if raw, ok := settings[SettingKeyOIDCConnectValidateIDToken]; ok {
		effective.ValidateIDToken = raw == "true"
	} else {
		effective.ValidateIDToken = SettingsOidcValidateIDTokenCompatibilityDefault(effective)
	}
	if v, ok := settings[SettingKeyOIDCConnectAllowedSigningAlgs]; ok && strings.TrimSpace(v) != "" {
		effective.AllowedSigningAlgs = strings.TrimSpace(v)
	}
	// Unparseable saved skew retains the startup allowance silently; range
	// validation below then checks that retained or parsed value, including its
	// valid zero case.
	if raw, ok := settings[SettingKeyOIDCConnectClockSkewSeconds]; ok && strings.TrimSpace(raw) != "" {
		if parsed, parseErr := strconv.Atoi(strings.TrimSpace(raw)); parseErr == nil {
			effective.ClockSkewSeconds = parsed
		}
	}
	if raw, ok := settings[SettingKeyOIDCConnectRequireEmailVerified]; ok {
		effective.RequireEmailVerified = raw == "true"
	}
	// A present userinfo path can intentionally clear startup text, unlike
	// nonblank-only endpoint overrides; apply that presence rule independently
	// to email, subject and username extraction.
	if v, ok := settings[SettingKeyOIDCConnectUserInfoEmailPath]; ok {
		effective.UserInfoEmailPath = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectUserInfoIDPath]; ok {
		effective.UserInfoIDPath = strings.TrimSpace(v)
	}
	if v, ok := settings[SettingKeyOIDCConnectUserInfoUsernamePath]; ok {
		effective.UserInfoUsernamePath = strings.TrimSpace(v)
	}

	if !effective.Enabled {
		return authconfig.OIDCConnectConfig{}, apperror.NotFound("OAUTH_DISABLED", "oauth login is disabled")
	}
	if strings.TrimSpace(effective.ProviderName) == "" {
		effective.ProviderName = "OIDC"
	}
	if strings.TrimSpace(effective.ClientID) == "" {
		return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth client id not configured")
	}
	if strings.TrimSpace(effective.IssuerURL) == "" {
		return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth issuer url not configured")
	}
	if strings.TrimSpace(effective.RedirectURL) == "" {
		return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth redirect url not configured")
	}
	if strings.TrimSpace(effective.FrontendRedirectURL) == "" {
		return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth frontend redirect url not configured")
	}
	if !SettingsScopesContainOpenID(effective.Scopes) {
		return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth scopes must contain openid")
	}
	// Bound token-validation leeway while retaining zero additional skew allowance.
	if effective.ClockSkewSeconds < 0 || effective.ClockSkewSeconds > 600 {
		return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth clock skew must be between 0 and 600")
	}

	if err := authconfig.ValidateAbsoluteHTTPURL(effective.IssuerURL); err != nil {
		return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth issuer url invalid")
	}

	discoveryURL := strings.TrimSpace(effective.DiscoveryURL)
	// Absent discovery text derives the well-known path from the validated
	// issuer, giving required missing endpoints a resolver address without
	// replacing already configured endpoint text.
	if discoveryURL == "" {
		discoveryURL = SettingsOidcDefaultDiscoveryURL(effective.IssuerURL)
		effective.DiscoveryURL = discoveryURL
	}
	if discoveryURL != "" {
		if err := authconfig.ValidateAbsoluteHTTPURL(discoveryURL); err != nil {
			return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth discovery url invalid")
		}
	}

	// Discover only missing authorization/token or validation-required signing
	// keys; if another required field triggers discovery, userinfo can also
	// fill, but its absence alone does not fetch metadata.
	needsDiscovery := strings.TrimSpace(effective.AuthorizeURL) == "" ||
		strings.TrimSpace(effective.TokenURL) == "" ||
		(effective.ValidateIDToken && strings.TrimSpace(effective.JWKSURL) == "")
	if needsDiscovery && discoveryURL != "" {
		metadata, resolveErr := s.resolveMetadata(ctx, discoveryURL)
		if resolveErr != nil {
			return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth discovery resolve failed").WithCause(resolveErr)
		}
		if strings.TrimSpace(effective.AuthorizeURL) == "" {
			effective.AuthorizeURL = strings.TrimSpace(metadata.AuthorizationEndpoint)
		}
		if strings.TrimSpace(effective.TokenURL) == "" {
			effective.TokenURL = strings.TrimSpace(metadata.TokenEndpoint)
		}
		if strings.TrimSpace(effective.UserInfoURL) == "" {
			effective.UserInfoURL = strings.TrimSpace(metadata.UserInfoEndpoint)
		}
		if strings.TrimSpace(effective.JWKSURL) == "" {
			effective.JWKSURL = strings.TrimSpace(metadata.JWKSURI)
		}
	}

	if strings.TrimSpace(effective.AuthorizeURL) == "" {
		return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth authorize url not configured")
	}
	if strings.TrimSpace(effective.TokenURL) == "" {
		return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth token url not configured")
	}
	if err := authconfig.ValidateAbsoluteHTTPURL(effective.AuthorizeURL); err != nil {
		return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth authorize url invalid")
	}
	if err := authconfig.ValidateAbsoluteHTTPURL(effective.TokenURL); err != nil {
		return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth token url invalid")
	}
	if v := strings.TrimSpace(effective.UserInfoURL); v != "" {
		if err := authconfig.ValidateAbsoluteHTTPURL(v); err != nil {
			return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth userinfo url invalid")
		}
	}
	if effective.ValidateIDToken {
		if strings.TrimSpace(effective.JWKSURL) == "" {
			return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth jwks url not configured")
		}
		if strings.TrimSpace(effective.AllowedSigningAlgs) == "" {
			return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth signing algs not configured")
		}
	}
	if v := strings.TrimSpace(effective.JWKSURL); v != "" {
		if err := authconfig.ValidateAbsoluteHTTPURL(v); err != nil {
			return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth jwks url invalid")
		}
	}
	if err := authconfig.ValidateAbsoluteHTTPURL(effective.RedirectURL); err != nil {
		return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth redirect url invalid")
	}
	if err := authconfig.ValidateFrontendRedirectURL(effective.FrontendRedirectURL); err != nil {
		return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth frontend redirect url invalid")
	}

	// Token-authentication admission requires a secret for blank or
	// secret-bearing method labels; none permits its absence, while unsupported
	// labels cannot reach the provider client.
	method := strings.ToLower(strings.TrimSpace(effective.TokenAuthMethod))
	switch method {
	case "", "client_secret_post", "client_secret_basic":
		if strings.TrimSpace(effective.ClientSecret) == "" {
			return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth client secret not configured")
		}
	case "none":
	default:
		return authconfig.OIDCConnectConfig{}, apperror.InternalServer("OAUTH_CONFIG_INVALID", "oauth token_auth_method invalid")
	}

	return effective, nil
}

// SettingsScopesContainOpenID reports whether lowercased whitespace-separated
// scope text contains openid. It accepts mixed-case scope names but performs
// no provider validation or deduplication; OIDC configuration admission uses
// this predicate before discovery and authorization.
func SettingsScopesContainOpenID(scopes string) bool {
	// This reader accepts mixed-case openid spelling for local configuration
	// admission; the returned predicate does not rewrite scope text sent to the
	// provider.
	return slices.Contains(strings.Fields(strings.ToLower(strings.TrimSpace(scopes))), "openid")
}

// SettingsOidcDefaultDiscoveryURL appends the well-known metadata path to
// trimmed issuer text after removing trailing slashes. Blank issuer returns
// empty text; this string operation performs no URL parsing or
// canonicalization, so its caller validates the constructed address.
func SettingsOidcDefaultDiscoveryURL(issuerURL string) string {
	issuerURL = strings.TrimSpace(issuerURL)
	if issuerURL == "" {
		return ""
	}
	// Construct the well-known metadata suffix after trailing slash removal,
	// leaving URL validity to the caller because this operation keeps other
	// issuer text unchanged.
	return strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration"
}

// BaseEmailOAuthConfig prepares GitHub/Google built-in endpoints, scope and
// frontend route, then overlays optional startup values. Provider labels are
// trimmed/lowercased and unknown providers return empty configuration; scalar
// values copy by value and remain unvalidated until the action reader runs.
func (s *OAuthSettings) BaseEmailOAuthConfig(provider string) authconfig.EmailOAuthProviderConfig {
	// Recognized labels start with provider endpoint/scope candidates, then
	// usable startup overrides; unknown labels stay empty so action readers can
	// reject unsupported provider selection.
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "github":
		base := authconfig.EmailOAuthProviderConfig{
			AuthorizeURL:        OAuthDefaultGitHubOAuthAuthorize,
			TokenURL:            OAuthDefaultGitHubOAuthToken,
			UserInfoURL:         OAuthDefaultGitHubOAuthUserInfo,
			EmailsURL:           OAuthDefaultGitHubOAuthEmails,
			Scopes:              OAuthDefaultGitHubOAuthScopes,
			FrontendRedirectURL: OAuthDefaultGitHubOAuthFrontend,
		}
		if s != nil && s.defaults != nil {
			return SettingsMergeEmailOAuthBaseConfig(base, s.defaults.GitHubOAuth)
		}
		return base
	case "google":
		base := authconfig.EmailOAuthProviderConfig{
			AuthorizeURL:        OAuthDefaultGoogleOAuthAuthorize,
			TokenURL:            OAuthDefaultGoogleOAuthToken,
			UserInfoURL:         OAuthDefaultGoogleOAuthUserInfo,
			Scopes:              OAuthDefaultGoogleOAuthScopes,
			FrontendRedirectURL: OAuthDefaultGoogleOAuthFrontend,
		}
		if s != nil && s.defaults != nil {
			return SettingsMergeEmailOAuthBaseConfig(base, s.defaults.GoogleOAuth)
		}
		return base
	default:
		return authconfig.EmailOAuthProviderConfig{}
	}
}

// SettingsEmailOAuthSettingKeys returns enablement, client ID, secret,
// backend callback and frontend callback keys for a recognized provider.
// Labels are trimmed/lowercased; unknown providers return five empty strings
// so effective conversion keeps its prepared base.
func SettingsEmailOAuthSettingKeys(provider string) (enabled, clientID, clientSecret, redirectURL, frontendRedirectURL string) {
	// Normalize the provider label before selecting its stored key names,
	// keeping credential/callback overrides aligned with the base configuration
	// chosen for that provider.
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "github":
		return SettingKeyGitHubOAuthEnabled,
			SettingKeyGitHubOAuthClientID,
			SettingKeyGitHubOAuthClientSecret,
			SettingKeyGitHubOAuthRedirectURL,
			SettingKeyGitHubOAuthFrontendRedirectURL
	case "google":
		return SettingKeyGoogleOAuthEnabled,
			SettingKeyGoogleOAuthClientID,
			SettingKeyGoogleOAuthClientSecret,
			SettingKeyGoogleOAuthRedirectURL,
			SettingKeyGoogleOAuthFrontendRedirectURL
	default:
		return "", "", "", "", ""
	}
}

// SupportsMode reports the stored capability flag for the normalized WeChat
// channel. Unknown/blank normalization selects open-platform; this accessor
// does not check global enablement or credentials, which the readiness/parser
// operations apply separately.
func (cfg WeChatConnectOAuthConfig) SupportsMode(mode string) bool {
	// Channel normalization maps other labels to open-platform for flag lookup;
	// this configured capability observation still needs the parser or readiness
	// helper for admission.
	switch NormalizeWeChatConnectModeSetting(mode) {
	case "mp":
		return cfg.MPEnabled
	case "mobile":
		return cfg.MobileEnabled
	default:
		return cfg.OpenEnabled
	}
}

// ScopeForMode selects scope text for the requested normalized WeChat
// channel. Official-account interprets cfg.Scopes under its known scope
// choices, mobile returns empty and open-platform uses its login default;
// callers keep the actual selected channel aligned with authorization.
func (cfg WeChatConnectOAuthConfig) ScopeForMode(mode string) string {
	// Scope interpretation follows the actual requested channel, so a
	// configuration selected for one channel can provide the compatible scope of
	// another supported ceremony.
	switch NormalizeWeChatConnectModeSetting(mode) {
	case "mp":
		return SettingsNormalizeWeChatConnectScopeSetting(cfg.Scopes, "mp")
	case "mobile":
		return ""
	}
	return SettingsDefaultWeChatConnectScopeForMode("open")
}

// AppIDForMode returns trimmed selected-channel identifier, falling back to
// LegacyAppID when blank. The mode normalizer selects open-platform for other
// inputs; capability/global admission and secret readiness are checked
// separately before that value is used.
func (cfg WeChatConnectOAuthConfig) AppIDForMode(mode string) string {
	// Select the requested channel's identifier before legacy fallback, keeping
	// this lookup aligned with the separately selected secret under the same
	// normalized mode.
	switch NormalizeWeChatConnectModeSetting(mode) {
	case "mp":
		return strings.TrimSpace(oauthSettingsFirstNonEmpty(cfg.MPAppID, cfg.LegacyAppID))
	case "mobile":
		return strings.TrimSpace(oauthSettingsFirstNonEmpty(cfg.MobileAppID, cfg.LegacyAppID))
	}
	return strings.TrimSpace(oauthSettingsFirstNonEmpty(cfg.OpenAppID, cfg.LegacyAppID))
}

// AppSecretForMode returns trimmed selected-channel secret, falling back to
// LegacyAppSecret when blank. Unknown/blank normalized mode selects
// open-platform; callers apply the corresponding capability/identifier/global
// checks rather than treating returned text as proof of readiness.
func (cfg WeChatConnectOAuthConfig) AppSecretForMode(mode string) string {
	// Select the requested channel's secret before its legacy fallback; callers
	// pair it with the same mode's identifier and perform global/capability
	// admission separately.
	switch NormalizeWeChatConnectModeSetting(mode) {
	case "mp":
		return strings.TrimSpace(oauthSettingsFirstNonEmpty(cfg.MPAppSecret, cfg.LegacyAppSecret))
	case "mobile":
		return strings.TrimSpace(oauthSettingsFirstNonEmpty(cfg.MobileAppSecret, cfg.LegacyAppSecret))
	}
	return strings.TrimSpace(oauthSettingsFirstNonEmpty(cfg.OpenAppSecret, cfg.LegacyAppSecret))
}

// oauthSettingsFirstNonEmpty returns the first nonblank trimmed value from
// its candidates. Empty or wholly blank input returns empty text, letting
// provider setting readers choose stored, startup and built-in fallback order
// at their call site.
func oauthSettingsFirstNonEmpty(values ...string) string {
	// Select usable trimmed text in caller-specified priority order, letting
	// blank saved values retain startup or built-in candidates in effective
	// provider setting conversions.
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
