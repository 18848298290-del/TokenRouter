package identity

import "strings"

// Browser authentication choices are derived from supplied setting values and
// startup defaults. LinuxDo, DingTalk and OIDC expose their selected switches;
// GitHub and Google also require client credentials and a backend callback to
// advertise email OAuth. These public decisions have fewer validation checks
// than the configuration reads used to start an actual login.
//
// WeChat advertises browser login when open-platform or official-account
// credentials are ready, while its mobile capability is reported separately.
// Google One Tap additionally needs its own switch and advertised Google OAuth;
// its client ID is emitted only in that case. OIDC labels, CAPTCHA regions and
// registration suffixes are normalized independently for the same browser
// settings result.

// PublicAuthSettings holds advertised login choices and browser-safe
// authentication parameters.
type PublicAuthSettings struct {
	// LinuxDo, DingTalk and OIDC use supplied switches or startup defaults for
	// missing keys. WeChatOpen, WeChatMP and WeChatMobile require their enabled
	// capability and nonblank application credentials. WeChat is true when the
	// open-platform or official-account check succeeds; ready mobile credentials
	// alone leave the browser choice false. GitHub and Google require enabled
	// configuration with client credentials and a backend callback, while
	// GoogleOneTap also needs its separate stored switch. Login-start reads apply
	// the remaining configuration checks for each advertised choice. LinuxDo:
	// advertised LinuxDo login. DingTalk: advertised DingTalk login. OIDC:
	// advertised OIDC login. WeChat: advertised WeChat browser login. WeChatOpen:
	// ready open-platform login. WeChatMP: ready official-account login.
	// WeChatMobile: ready mobile login. GitHub: advertised GitHub email login.
	// Google: advertised Google email login. GoogleOneTap: advertised One Tap
	// login.
	LinuxDo, DingTalk, OIDC, WeChat, WeChatOpen, WeChatMP, WeChatMobile, GitHub, Google, GoogleOneTap bool
	// OIDCName uses the trimmed supplied label, then a nonblank startup label,
	// then OIDC for the browser's provider display. GoogleClientID is emitted when
	// GoogleOneTap is true; all other cases return an empty client ID so the
	// browser receives no audience for that option. TencentRegion and AliyunRegion
	// are derived from the supplied map using their own exact accepted site codes.
	// Missing or unrecognized values select cn for both regional parameters.
	// OIDCName: displayed provider name. GoogleClientID: public One Tap audience.
	// TencentRegion: selected Tencent site. AliyunRegion: selected Aliyun region.
	OIDCName, GoogleClientID, TencentRegion, AliyunRegion string
	// RegistrationSuffixes contains tolerant normalized whitelist entries.
	// Unusable or missing saved input yields an empty list, which suffix checks
	// interpret as an unrestricted registration domain policy.
	RegistrationSuffixes []string
}

// PublicSettingsFromValues returns advertised authentication choices from one
// supplied settings map and the retained startup defaults. It performs no store
// or discovery request, and callers provide a constructed OAuthSettings
// instance.
//
// Missing LinuxDo, DingTalk and OIDC switches use startup defaults; supplied
// values enable them only for exact true text. GitHub and Google also require
// credential and callback presence. WeChat browser readiness excludes a solely
// mobile configuration, while One Tap requires its separate switch and Google
// readiness before the public client ID is emitted. Each provider's login-start
// read still owns its complete configuration validation.
func (s *OAuthSettings) PublicSettingsFromValues(settings map[string]string) PublicAuthSettings {
	linuxDoEnabled := false
	if raw, ok := settings[SettingKeyLinuxDoConnectEnabled]; ok {
		linuxDoEnabled = raw == "true"
	} else {
		linuxDoEnabled = s.defaults != nil && s.defaults.LinuxDo.Enabled
	}
	dingTalkEnabled := false
	if raw, ok := settings[SettingKeyDingTalkConnectEnabled]; ok {
		dingTalkEnabled = raw == "true"
	} else {
		dingTalkEnabled = s.defaults != nil && s.defaults.DingTalk.Enabled
	}
	oidcEnabled := false
	if raw, ok := settings[SettingKeyOIDCConnectEnabled]; ok {
		oidcEnabled = raw == "true"
	} else {
		oidcEnabled = s.defaults != nil && s.defaults.OIDC.Enabled
	}
	oidcProviderName := strings.TrimSpace(settings[SettingKeyOIDCConnectProviderName])
	if oidcProviderName == "" && s.defaults != nil {
		oidcProviderName = strings.TrimSpace(s.defaults.OIDC.ProviderName)
	}
	if oidcProviderName == "" {
		oidcProviderName = "OIDC"
	}
	weChatEnabled, weChatOpenEnabled, weChatMPEnabled, weChatMobileEnabled := s.WeChatOAuthCapabilitiesFromSettings(settings)
	gitHubOAuthEnabled := s.EmailOAuthPublicEnabled(settings, "github")
	googleOAuthEnabled := s.EmailOAuthPublicEnabled(settings, "google")
	googleOAuthConfig := s.EffectiveEmailOAuthConfig(settings, "google")
	googleOneTapEnabled := settings[SettingKeyGoogleOneTapEnabled] == "true" && googleOAuthEnabled
	googleOAuthClientID := ""
	if googleOneTapEnabled {
		googleOAuthClientID = strings.TrimSpace(googleOAuthConfig.ClientID)
	}

	return PublicAuthSettings{
		LinuxDo: linuxDoEnabled, DingTalk: dingTalkEnabled, OIDC: oidcEnabled, OIDCName: oidcProviderName,
		WeChat: weChatEnabled, WeChatOpen: weChatOpenEnabled, WeChatMP: weChatMPEnabled, WeChatMobile: weChatMobileEnabled,
		GitHub: gitHubOAuthEnabled, Google: googleOAuthEnabled, GoogleOneTap: googleOneTapEnabled, GoogleClientID: googleOAuthClientID,
		RegistrationSuffixes: ParseRegistrationEmailSuffixWhitelist(settings[SettingKeyRegistrationEmailSuffixWhitelist]), TencentRegion: NormalizeTencentCaptchaRegion(settings[SettingKeyTencentCaptchaRegion]), AliyunRegion: NormalizeAliyunCaptchaRegion(settings[SettingKeyAliyunCaptchaRegion]),
	}
}
