package identity

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity/authconfig"
)

// TestReadAdminSettingsStringFallbacks combines absent, empty, padded and
// overriding stored strings with startup defaults. It checks trimmed OIDC
// scope and DingTalk attribute-key selection alongside the
// default-concurrency callback result.
func TestReadAdminSettingsStringFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name         string            // settings fallback scenario label
		settings     map[string]string // submitted flat setting values
		fallback     string            // startup string fallback text
		wantOIDC     string            // expected OIDC scope value
		wantDingTalk string            // expected DingTalk destination key
	}{
		{name: "missing", fallback: " configured ", wantOIDC: "configured", wantDingTalk: "configured"},
		{name: "empty", settings: map[string]string{SettingKeyOIDCConnectScopes: "", SettingKeyDingTalkConnectSyncCorpEmailAttrKey: ""}, fallback: " configured ", wantOIDC: "configured", wantDingTalk: "configured"},
		{name: "whitespace", settings: map[string]string{SettingKeyOIDCConnectScopes: " \t", SettingKeyDingTalkConnectSyncCorpEmailAttrKey: " \n"}, fallback: " configured ", wantOIDC: "configured", wantDingTalk: "configured"},
		{name: "override", settings: map[string]string{SettingKeyOIDCConnectScopes: " custom ", SettingKeyDingTalkConnectSyncCorpEmailAttrKey: " custom "}, fallback: " configured ", wantOIDC: "custom", wantDingTalk: "custom"},
		{name: "builtin", fallback: " \t", wantOIDC: "", wantDingTalk: "dingtalk_email"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := NewOAuthSettings(nil, &OAuthSettingsDefaults{
				OIDC:     authconfig.OIDCConnectConfig{Scopes: tc.fallback},
				DingTalk: authconfig.DingTalkConnectConfig{SyncCorpEmailAttrKey: tc.fallback},
			}, nil)
			result := source.ReadAdminSettings(tc.settings, func() int { return 7 })
			require.Equal(t, tc.wantOIDC, result.OIDCConnectScopes)
			require.Equal(t, tc.wantDingTalk, result.DingTalkConnectSyncCorpEmailAttrKey)
			require.Equal(t, 7, result.DefaultConcurrency)
		})
	}
}

// TestReadAdminSettingsExplicitEmptyValues supplies clearable OIDC paths and
// explicit saved zero/empty values. It checks path clearing, disabled
// presence, zero skew/concurrency and unchanged padded LinuxDo startup
// identifier text.
func TestReadAdminSettingsExplicitEmptyValues(t *testing.T) {
	source := NewOAuthSettings(nil, &OAuthSettingsDefaults{
		LinuxDo: authconfig.LinuxDoConnectConfig{ClientID: " padded "},
		OIDC:    authconfig.OIDCConnectConfig{UserInfoEmailPath: "email", Enabled: true, ClockSkewSeconds: 90},
	}, nil)
	result := source.ReadAdminSettings(map[string]string{
		SettingKeyOIDCConnectUserInfoEmailPath: " ",
		SettingKeyOIDCConnectEnabled:           "",
		SettingKeyOIDCConnectClockSkewSeconds:  "0",
		SettingKeyDefaultConcurrency:           "0",
	}, func() int {
		t.Fatal("合法并发设置不应读取默认值")
		return 1
	})
	require.Empty(t, result.OIDCConnectUserInfoEmailPath)
	require.False(t, result.OIDCConnectEnabled)
	require.Zero(t, result.OIDCConnectClockSkewSeconds)
	require.Zero(t, result.DefaultConcurrency)
	require.Equal(t, " padded ", result.LinuxDoConnectClientID)
}
