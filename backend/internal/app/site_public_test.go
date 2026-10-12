package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/site"
	sitehttp "github.com/TokenFlux/TokenRouter/internal/site/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/team"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

type settingHandlerPublicRepoStub struct {
	values map[string]string
}

type settingPublicRepoStub struct {
	values map[string]string
	err    error
}

func (s *settingHandlerPublicRepoStub) Get(ctx context.Context, key string) (*settingscore.Setting, error) {
	panic("unexpected Get call")
}

func (s *settingHandlerPublicRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	panic("unexpected GetValue call")
}

func (s *settingHandlerPublicRepoStub) Set(ctx context.Context, key, value string) error {
	panic("unexpected Set call")
}

func (s *settingHandlerPublicRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (s *settingHandlerPublicRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (s *settingHandlerPublicRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *settingHandlerPublicRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}

func TestSettingHandler_GetPublicSettings_ExposesForceEmailOnThirdPartySignup(t *testing.T) {
	repo := &settingHandlerPublicRepoStub{
		values: map[string]string{
			identity.SettingKeyForceEmailOnThirdPartySignup: "true",
		},
	}
	h := sitehttp.NewPublicHandler(newSitePublicSettingsFixture(repo, &config.Config{}), "test-version")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/settings/public", nil)

	h.GetPublicSettings(c)

	require.Equal(t, http.StatusOK, recorder.Code)

	var resp struct {
		Code int `json:"code"`
		Data struct {
			ForceEmailOnThirdPartySignup bool `json:"force_email_on_third_party_signup"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.True(t, resp.Data.ForceEmailOnThirdPartySignup)
}

func TestSettingHandler_GetPublicSettings_ExposesAffiliateEnabled(t *testing.T) {
	repo := &settingHandlerPublicRepoStub{
		values: map[string]string{
			promotion.SettingKeyAffiliateEnabled: "true",
		},
	}
	h := sitehttp.NewPublicHandler(newSitePublicSettingsFixture(repo, &config.Config{}), "test-version")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/settings/public", nil)

	h.GetPublicSettings(c)

	require.Equal(t, http.StatusOK, recorder.Code)

	var resp struct {
		Code int `json:"code"`
		Data struct {
			AffiliateEnabled bool `json:"affiliate_enabled"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.True(t, resp.Data.AffiliateEnabled)
}

func TestSettingHandler_GetPublicSettings_ExposesTencentCaptchaConfiguration(t *testing.T) {
	repo := &settingHandlerPublicRepoStub{
		values: map[string]string{
			identity.SettingKeyTencentCaptchaEnabled: "true",
			identity.SettingKeyTencentCaptchaAppID:   "123456789",
			identity.SettingKeyTencentCaptchaRegion:  identity.TencentCaptchaRegionINTL,
		},
	}
	h := sitehttp.NewPublicHandler(newSitePublicSettingsFixture(repo, &config.Config{}), "test-version")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/settings/public", nil)

	h.GetPublicSettings(c)

	require.Equal(t, http.StatusOK, recorder.Code)

	var resp struct {
		Code int `json:"code"`
		Data struct {
			TencentCaptchaEnabled bool   `json:"tencent_captcha_enabled"`
			TencentCaptchaAppID   string `json:"tencent_captcha_app_id"`
			TencentCaptchaRegion  string `json:"tencent_captcha_region"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.True(t, resp.Data.TencentCaptchaEnabled)
	require.Equal(t, "123456789", resp.Data.TencentCaptchaAppID)
	require.Equal(t, identity.TencentCaptchaRegionINTL, resp.Data.TencentCaptchaRegion)
}

func TestSettingHandler_GetPublicSettings_ExposesWeChatOAuthModeCapabilities(t *testing.T) {
	h := sitehttp.NewPublicHandler(newSitePublicSettingsFixture(&settingHandlerPublicRepoStub{
		values: map[string]string{
			identity.SettingKeyWeChatConnectEnabled:             "true",
			identity.SettingKeyWeChatConnectAppID:               "wx-mp-app",
			identity.SettingKeyWeChatConnectAppSecret:           "wx-mp-secret",
			identity.SettingKeyWeChatConnectMode:                "mp",
			identity.SettingKeyWeChatConnectScopes:              "snsapi_base",
			identity.SettingKeyWeChatConnectOpenEnabled:         "true",
			identity.SettingKeyWeChatConnectMPEnabled:           "true",
			identity.SettingKeyWeChatConnectRedirectURL:         "https://api.example.com/api/v1/auth/oauth/wechat/callback",
			identity.SettingKeyWeChatConnectFrontendRedirectURL: "/auth/wechat/callback",
		},
	}, &config.Config{}), "test-version")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/settings/public", nil)

	h.GetPublicSettings(c)

	require.Equal(t, http.StatusOK, recorder.Code)

	var resp struct {
		Code int `json:"code"`
		Data struct {
			WeChatOAuthEnabled     bool `json:"wechat_oauth_enabled"`
			WeChatOAuthOpenEnabled bool `json:"wechat_oauth_open_enabled"`
			WeChatOAuthMPEnabled   bool `json:"wechat_oauth_mp_enabled"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.True(t, resp.Data.WeChatOAuthEnabled)
	require.True(t, resp.Data.WeChatOAuthOpenEnabled)
	require.True(t, resp.Data.WeChatOAuthMPEnabled)
}

func (s *settingPublicRepoStub) Get(ctx context.Context, key string) (*settingscore.Setting, error) {
	panic("unexpected Get call")
}

func (s *settingPublicRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	panic("unexpected GetValue call")
}

func (s *settingPublicRepoStub) Set(ctx context.Context, key, value string) error {
	panic("unexpected Set call")
}

func (s *settingPublicRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (s *settingPublicRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (s *settingPublicRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *settingPublicRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}

func TestSettingService_GetPublicSettings_ExposesRegistrationEmailSuffixWhitelist(t *testing.T) {
	repo := &settingPublicRepoStub{
		values: map[string]string{
			identity.SettingKeyRegistrationEnabled:                 "true",
			identity.SettingKeyEmailVerifyEnabled:                  "true",
			identity.SettingKeyRegistrationEmailSuffixWhitelist:    `["@EXAMPLE.com"," @foo.bar ","*.EDU.CN","@invalid_domain",""]`,
			identity.SettingKeyRegistrationEmailDomainQuotaEnabled: "true",
		},
	}
	svc := newSitePublicSettingsFixture(repo, &config.Config{})

	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"@example.com", "@foo.bar", "*.edu.cn"}, settings.RegistrationEmailSuffixWhitelist)
	require.True(t, settings.RegistrationEmailDomainQuotaEnabled)
}

func TestSettingService_GetPublicSettings_ExposesTablePreferences(t *testing.T) {
	repo := &settingPublicRepoStub{
		values: map[string]string{
			site.SettingKeyTableDefaultPageSize: "50",
			site.SettingKeyTablePageSizeOptions: "[20,50,100]",
		},
	}
	svc := newSitePublicSettingsFixture(repo, &config.Config{})

	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 50, settings.TableDefaultPageSize)
	require.Equal(t, []int{20, 50, 100}, settings.TablePageSizeOptions)
}

func TestSettingService_GetPublicSettings_ExposesForceEmailOnThirdPartySignup(t *testing.T) {
	repo := &settingPublicRepoStub{
		values: map[string]string{
			identity.SettingKeyForceEmailOnThirdPartySignup: "true",
		},
	}
	svc := newSitePublicSettingsFixture(repo, &config.Config{})

	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.ForceEmailOnThirdPartySignup)
}

// TestSettingService_GetPublicSettings_ExposesAffiliateEnabled checks the affiliate flag used by navigation and route guards.
func TestSettingService_GetPublicSettings_ExposesAffiliateEnabled(t *testing.T) {
	repo := &settingPublicRepoStub{
		values: map[string]string{
			promotion.SettingKeyAffiliateEnabled: "true",
		},
	}
	svc := newSitePublicSettingsFixture(repo, &config.Config{})

	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.AffiliateEnabled)
}

// TestSettingService_GetPublicSettings_ExposesPageFeatureFlags checks public page switches used by navigation and route guards.
func TestSettingService_GetPublicSettings_ExposesPageFeatureFlags(t *testing.T) {
	repo := &settingPublicRepoStub{
		values: map[string]string{
			team.SettingKeyTeamEnabled:         "false",
			creative.SettingKeyCreativeEnabled: "false",
		},
	}
	svc := newSitePublicSettingsFixture(repo, &config.Config{Team: config.TeamConfig{Enabled: true}})

	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.TeamEnabled)
	require.False(t, settings.CreativeEnabled)
}

// TestSettingService_GetPublicSettings_CreativeEnabledDefaultsTrue checks the enabled default when the setting is absent.
func TestSettingService_GetPublicSettings_CreativeEnabledDefaultsTrue(t *testing.T) {
	repo := &settingPublicRepoStub{values: map[string]string{}}
	svc := newSitePublicSettingsFixture(repo, &config.Config{})

	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.CreativeEnabled)

	// HTML injection and the public settings API both default creative to enabled.
	payload, err := svc.GetPublicSettingsForInjection(context.Background())
	require.NoError(t, err)
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"creative_enabled":true`)
}

func TestSettingService_GetPublicSettings_ExposesAllowUserViewErrorRequests(t *testing.T) {
	repo := &settingPublicRepoStub{
		values: map[string]string{
			usage.SettingKeyAllowUserViewErrorRequests: "true",
		},
	}
	svc := newSitePublicSettingsFixture(repo, &config.Config{})

	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.AllowUserViewErrorRequests)
}

// TestSettingService_GetPublicSettingsForInjection_ExposesPublicFeatureFlags checks feature flags in the initial page configuration.
func TestSettingService_GetPublicSettingsForInjection_ExposesPublicFeatureFlags(t *testing.T) {
	repo := &settingPublicRepoStub{
		values: map[string]string{
			promotion.SettingKeyAffiliateEnabled:                   "true",
			identity.SettingKeyForceEmailOnThirdPartySignup:        "true",
			identity.SettingKeyRegistrationEmailDomainQuotaEnabled: "true",
			identity.SettingKeyUserEmailChangeEnabled:              "true",
			usage.SettingKeyAllowUserViewErrorRequests:             "true",
			team.SettingKeyTeamEnabled:                             "true",
			creative.SettingKeyCreativeEnabled:                     "false",
		},
	}
	svc := newSitePublicSettingsFixture(repo, &config.Config{
		Team:     config.TeamConfig{Enabled: true},
		WebAuthn: config.WebAuthnConfig{Enabled: true},
	})

	payload, err := svc.GetPublicSettingsForInjection(context.Background())
	require.NoError(t, err)

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	var settings struct {
		AffiliateEnabled                    bool `json:"affiliate_enabled"`
		ForceEmailOnThirdPartySignup        bool `json:"force_email_on_third_party_signup"`
		AllowUserViewErrorRequests          bool `json:"allow_user_view_error_requests"`
		TeamEnabled                         bool `json:"team_enabled"`
		CreativeEnabled                     bool `json:"creative_enabled"`
		PasskeyEnabled                      bool `json:"passkey_enabled"`
		RegistrationEmailDomainQuotaEnabled bool `json:"registration_email_domain_quota_enabled"`
		UserEmailChangeEnabled              bool `json:"user_email_change_enabled"`
	}
	require.NoError(t, json.Unmarshal(encoded, &settings))
	require.True(t, settings.AffiliateEnabled)
	require.True(t, settings.ForceEmailOnThirdPartySignup)
	require.True(t, settings.AllowUserViewErrorRequests)
	require.True(t, settings.TeamEnabled)
	require.False(t, settings.CreativeEnabled)
	require.True(t, settings.PasskeyEnabled)
	require.True(t, settings.RegistrationEmailDomainQuotaEnabled)
	require.True(t, settings.UserEmailChangeEnabled)
}

func TestSettingService_GetPublicSettings_ExposesWeChatOAuthModeCapabilities(t *testing.T) {
	svc := newSitePublicSettingsFixture(&settingPublicRepoStub{
		values: map[string]string{
			identity.SettingKeyWeChatConnectEnabled:             "true",
			identity.SettingKeyWeChatConnectAppID:               "wx-mp-app",
			identity.SettingKeyWeChatConnectAppSecret:           "wx-mp-secret",
			identity.SettingKeyWeChatConnectMode:                "mp",
			identity.SettingKeyWeChatConnectScopes:              "snsapi_base",
			identity.SettingKeyWeChatConnectOpenEnabled:         "true",
			identity.SettingKeyWeChatConnectMPEnabled:           "true",
			identity.SettingKeyWeChatConnectRedirectURL:         "https://api.example.com/api/v1/auth/oauth/wechat/callback",
			identity.SettingKeyWeChatConnectFrontendRedirectURL: "/auth/wechat/callback",
		},
	}, &config.Config{})

	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.WeChatOAuthEnabled)
	require.True(t, settings.WeChatOAuthOpenEnabled)
	require.True(t, settings.WeChatOAuthMPEnabled)
}

func TestSettingService_GetPublicSettings_DoesNotExposeMobileOnlyWeChatAsWebOAuthAvailable(t *testing.T) {
	svc := newSitePublicSettingsFixture(&settingPublicRepoStub{
		values: map[string]string{
			identity.SettingKeyWeChatConnectEnabled:             "true",
			identity.SettingKeyWeChatConnectMobileEnabled:       "true",
			identity.SettingKeyWeChatConnectMode:                "mobile",
			identity.SettingKeyWeChatConnectMobileAppID:         "wx-mobile-app",
			identity.SettingKeyWeChatConnectMobileAppSecret:     "wx-mobile-secret",
			identity.SettingKeyWeChatConnectFrontendRedirectURL: "/auth/wechat/callback",
		},
	}, &config.Config{})

	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.WeChatOAuthEnabled)
	require.False(t, settings.WeChatOAuthOpenEnabled)
	require.False(t, settings.WeChatOAuthMPEnabled)
	require.True(t, settings.WeChatOAuthMobileEnabled)
}

func TestSettingService_GetPublicSettings_FallsBackToConfigForWeChatOAuthCapabilities(t *testing.T) {
	svc := newSitePublicSettingsFixture(&settingPublicRepoStub{values: map[string]string{}}, &config.Config{
		WeChat: config.WeChatConnectConfig{
			Enabled:             true,
			OpenEnabled:         true,
			OpenAppID:           "wx-open-config",
			OpenAppSecret:       "wx-open-secret",
			FrontendRedirectURL: "/auth/wechat/config-callback",
		},
	})

	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.WeChatOAuthEnabled)
	require.True(t, settings.WeChatOAuthOpenEnabled)
	require.False(t, settings.WeChatOAuthMPEnabled)
	require.False(t, settings.WeChatOAuthMobileEnabled)
}

func TestSettingService_GetPublicSettings_ExposesEffectiveGoogleOneTapConfig(t *testing.T) {
	svc := newSitePublicSettingsFixture(&settingPublicRepoStub{values: map[string]string{
		identity.SettingKeyGoogleOneTapEnabled:            "true",
		identity.SettingKeyGoogleOAuthEnabled:             "true",
		identity.SettingKeyGoogleOAuthClientID:            "google-web-client",
		identity.SettingKeyGoogleOAuthClientSecret:        "google-client-secret",
		identity.SettingKeyGoogleOAuthRedirectURL:         "https://app.example/api/v1/auth/oauth/google/callback",
		identity.SettingKeyGoogleOAuthFrontendRedirectURL: "/auth/oauth/callback",
	}}, &config.Config{})

	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.GoogleOAuthEnabled)
	require.True(t, settings.GoogleOneTapEnabled)
	require.Equal(t, "google-web-client", settings.GoogleOAuthClientID)

	raw, err := json.Marshal(settings)
	require.NoError(t, err)
	require.Contains(t, string(raw), "google-web-client")
	require.NotContains(t, string(raw), "google-client-secret")
}

func TestSettingService_GetPublicSettings_DisablesGoogleOneTapWhenOAuthIsIncomplete(t *testing.T) {
	svc := newSitePublicSettingsFixture(&settingPublicRepoStub{values: map[string]string{
		identity.SettingKeyGoogleOneTapEnabled:    "true",
		identity.SettingKeyGoogleOAuthEnabled:     "true",
		identity.SettingKeyGoogleOAuthClientID:    "google-web-client",
		identity.SettingKeyGoogleOAuthRedirectURL: "https://app.example/api/v1/auth/oauth/google/callback",
	}}, &config.Config{})

	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.GoogleOAuthEnabled)
	require.False(t, settings.GoogleOneTapEnabled)
	require.Empty(t, settings.GoogleOAuthClientID)
}

func TestSettingService_GetGoogleOneTapConfigRequiresIndependentSwitch(t *testing.T) {
	values := map[string]string{
		identity.SettingKeyGoogleOneTapEnabled:            "false",
		identity.SettingKeyGoogleOAuthEnabled:             "true",
		identity.SettingKeyGoogleOAuthClientID:            "google-web-client",
		identity.SettingKeyGoogleOAuthClientSecret:        "google-client-secret",
		identity.SettingKeyGoogleOAuthRedirectURL:         "https://app.example/api/v1/auth/oauth/google/callback",
		identity.SettingKeyGoogleOAuthFrontendRedirectURL: "/auth/oauth/callback",
	}
	svc := newOAuthSettingsFixture(&settingPublicRepoStub{values: values}, &config.Config{})

	_, err := svc.GetGoogleOneTapConfig(context.Background())
	require.Error(t, err)

	values[identity.SettingKeyGoogleOneTapEnabled] = "true"
	settings, err := svc.GetGoogleOneTapConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, "google-web-client", settings.ClientID)
	require.Equal(t, "google-client-secret", settings.ClientSecret)
}

// newSitePublicSettingsFixture shares a Store between OAuth interpretation and public settings.
func newSitePublicSettingsFixture(repo settingscore.Repository, cfg *config.Config) *site.PublicService {
	store := settingscore.New(repo)
	return provideSitePublic(store, provideOAuthSettings(store, cfg), cfg, timezone.NewCalendar(time.Local))
}
