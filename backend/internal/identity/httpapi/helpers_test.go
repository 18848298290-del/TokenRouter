package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// OAuth start tests need an enabled CAPTCHA configuration while provider setup
// remains deliberately absent. These fixtures expose runtime CAPTCHA reads,
// record Tencent ticket verification, and create the provider start handlers
// needed to check admission order across login providers.

// oauthCaptchaSettingRepo supplies the string settings used by OAuth
// action-CAPTCHA tests.
type oauthCaptchaSettingRepo struct {
	values map[string]string // runtime setting fixture values
}

// oauthCaptchaVerifier records the last Tencent proof and returns a successful
// response.
type oauthCaptchaVerifier struct {
	calls int                          // ticket verification call count
	proof identity.TencentCaptchaProof // last submitted Tencent proof
}

// captchaRuntimeFixture delegates CAPTCHA configuration while leaving other
// AuthSettings methods unavailable.
type captchaRuntimeFixture struct {
	// AuthSettings is nil in this fixture. Tests must stay on CAPTCHA
	// configuration paths; invoking another promoted method panics and exposes an
	// unexpected dependency read.
	identity.AuthSettings
	runtime *identity.RuntimeSettings // CAPTCHA runtime settings reader
}

// Get returns the missing-setting error for direct fixture record reads.
func (r *oauthCaptchaSettingRepo) Get(context.Context, string) (*settingscore.Setting, error) {
	return nil, settingscore.ErrSettingNotFound
}

// GetValue returns a configured CAPTCHA setting from the fixture map. Missing
// keys return the repository sentinel used by RuntimeSettings to select its
// unconfigured behavior for those values.
func (r *oauthCaptchaSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", settingscore.ErrSettingNotFound
	}
	return value, nil
}

// Set acknowledges fixture writes while leaving the configured map unchanged.
func (r *oauthCaptchaSettingRepo) Set(context.Context, string, string) error { return nil }

// GetMultiple returns a new map containing configured requested keys from this
// fixture. Unconfigured keys are omitted, allowing runtime readers to apply
// their missing-setting behavior.
func (r *oauthCaptchaSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

// SetMultiple acknowledges bulk fixture writes while leaving configured
// settings unchanged.
func (r *oauthCaptchaSettingRepo) SetMultiple(context.Context, map[string]string) error {
	return nil
}

// GetAll returns the fixture map directly for settings enumeration. Callers
// observe shared map storage, so tests retaining this result keep the
// configured values unchanged during their reads.
func (r *oauthCaptchaSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return r.values, nil
}

// Delete acknowledges fixture deletion while retaining the configured map
// entry.
func (r *oauthCaptchaSettingRepo) Delete(context.Context, string) error { return nil }

// VerifyTicket records the submitted proof and increments its fixture call
// count. Every invocation returns Tencent success, so tests can isolate
// handler admission and field transport from provider rejection.
func (v *oauthCaptchaVerifier) VerifyTicket(_ context.Context, _ identity.TencentCaptchaCredentials, proof identity.TencentCaptchaProof, _ string) (*identity.TencentCaptchaVerifyResponse, error) {
	v.calls++
	v.proof = proof
	return &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}, nil
}

// newOAuthCaptchaTestHandler constructs shared login handlers with optional
// Tencent CAPTCHA enabled. Provider dependencies remain nil so rejected starts
// reveal whether action-CAPTCHA admission occurred before provider
// configuration or cookie writes.
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
	// Rejected GET starts must stop before reading provider configuration. Nil
	// provider dependencies turn a premature configuration read into a test
	// failure instead of silently continuing.
	return &AuthenticationHandler{
		Session: session, Pending: pending,
		Email:    NewEmailOAuthHandler(pending, nil, nil),
		LinuxDo:  NewLinuxDoHandler(pending, nil, nil, nil),
		OIDC:     NewOIDCHandler(pending, nil, nil, nil),
		WeChat:   NewWeChatHandler(pending, nil, nil, WeChatHTTPOptions{}),
		DingTalk: NewDingTalkHandler(pending, nil, nil, DingTalkHTTPOptions{}),
	}, verifier
}

// oauthStartHandlers returns dispatch callbacks for the six supported OAuth
// start providers.
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

// GetCaptchaProviderConfig delegates fixture CAPTCHA configuration reads to
// its runtime settings instance.
func (s captchaRuntimeFixture) GetCaptchaProviderConfig(ctx context.Context) (identity.CaptchaProviderConfig, error) {
	return s.runtime.GetCaptchaProviderConfig(ctx)
}
