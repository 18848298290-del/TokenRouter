package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

type oauthCaptchaSettingRepo struct {
	values map[string]string
}

type oauthCaptchaVerifier struct {
	calls int
	proof identity.TencentCaptchaProof
}

// captchaRuntimeFixture provides CAPTCHA settings; calls to other embedded methods fail the test.
type captchaRuntimeFixture struct {
	identity.AuthSettings
	runtime *identity.RuntimeSettings
}

func (r *oauthCaptchaSettingRepo) Get(context.Context, string) (*settingscore.Setting, error) {
	return nil, settingscore.ErrSettingNotFound
}

func (r *oauthCaptchaSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", settingscore.ErrSettingNotFound
	}
	return value, nil
}

func (r *oauthCaptchaSettingRepo) Set(context.Context, string, string) error { return nil }

func (r *oauthCaptchaSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func (r *oauthCaptchaSettingRepo) SetMultiple(context.Context, map[string]string) error {
	return nil
}

func (r *oauthCaptchaSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return r.values, nil
}

func (r *oauthCaptchaSettingRepo) Delete(context.Context, string) error { return nil }

func (v *oauthCaptchaVerifier) VerifyTicket(_ context.Context, _ identity.TencentCaptchaCredentials, proof identity.TencentCaptchaProof, _ string) (*identity.TencentCaptchaVerifyResponse, error) {
	v.calls++
	v.proof = proof
	return &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}, nil
}

func newOAuthCaptchaTestHandler(enabled bool) (*AuthenticationHandler, *oauthCaptchaVerifier) {
	values := map[string]string{}
	if enabled {
		values = map[string]string{
			identity.SettingKeyTencentCaptchaEnabled:        "true",
			identity.SettingKeyTencentCaptchaAppID:          "123456789",
			identity.SettingKeyTencentCaptchaAppSecretKey:   "app-secret",
			identity.SettingKeyTencentCaptchaCloudSecretID:  "cloud-secret-id",
			identity.SettingKeyTencentCaptchaCloudSecretKey: "cloud-secret-key",
		}
	}
	runtime := identity.NewRuntimeSettings(&oauthCaptchaSettingRepo{values: values}, settingscore.ErrSettingNotFound)
	settings := captchaRuntimeFixture{runtime: runtime}
	verifier := &oauthCaptchaVerifier{}
	auth := identity.NewAuthService(&identity.AuthDependencies{Settings: settings, Tencent: identity.NewTencentCaptchaService(runtime, verifier)}, nil)
	session := NewSessionHandler(auth, nil, nil, nil, nil, nil, SessionHTTPOptions{})
	pending := NewPendingHandler(session, nil, PendingHTTPOptions{})
	// Nil provider dependencies verify that GET CAPTCHA checks run before configuration reads or cookie writes.
	return &AuthenticationHandler{
		Session: session, Pending: pending,
		Email:    NewEmailOAuthHandler(pending, nil, nil),
		LinuxDo:  NewLinuxDoHandler(pending, nil, nil, nil),
		OIDC:     NewOIDCHandler(pending, nil, nil, nil),
		WeChat:   NewWeChatHandler(pending, nil, nil, WeChatHTTPOptions{}),
		DingTalk: NewDingTalkHandler(pending, nil, nil, DingTalkHTTPOptions{}),
	}, verifier
}

func oauthStartHandlers() map[string]func(*AuthenticationHandler, *gin.Context) {
	return map[string]func(*AuthenticationHandler, *gin.Context){
		"github":   func(h *AuthenticationHandler, c *gin.Context) { h.GitHubOAuthStart(c) },
		"google":   func(h *AuthenticationHandler, c *gin.Context) { h.GoogleOAuthStart(c) },
		"linuxdo":  func(h *AuthenticationHandler, c *gin.Context) { h.LinuxDoOAuthStart(c) },
		"dingtalk": func(h *AuthenticationHandler, c *gin.Context) { h.DingTalkOAuthStart(c) },
		"wechat":   func(h *AuthenticationHandler, c *gin.Context) { h.WeChatOAuthStart(c) },
		"oidc":     func(h *AuthenticationHandler, c *gin.Context) { h.OIDCOAuthStart(c) },
	}
}

func (s captchaRuntimeFixture) GetCaptchaProviderConfig(ctx context.Context) (identity.CaptchaProviderConfig, error) {
	return s.runtime.GetCaptchaProviderConfig(ctx)
}
