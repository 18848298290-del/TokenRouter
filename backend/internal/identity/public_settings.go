package identity

import "strings"

// PublicAuthSettings contains login switches and authentication parameters safe for browsers.
type PublicAuthSettings struct {
	LinuxDo, DingTalk, OIDC, WeChat, WeChatOpen, WeChatMP, WeChatMobile, GitHub, Google, GoogleOneTap bool
	OIDCName, GoogleClientID, TencentRegion, AliyunRegion                                             string
	RegistrationSuffixes                                                                              []string
}

// PublicSettingsFromValues derives public settings from a supplied batch of values without I/O.
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
