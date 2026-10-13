package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// aliyunVerifierSpy records credential dispatch and supplies configured verification outcomes.
type aliyunVerifierSpy struct {
	called    int                               // verification call count observed
	lastCred  identity.AliyunCaptchaCredentials // last dispatched credential value
	lastParam string                            // last serialized browser challenge
	// err takes precedence over result; when both are unset, verification returns
	// an accepted challenge. Tests select these outcomes independently of dispatch capture.
	result *identity.AliyunCaptchaVerifyResult // configured challenge result pointer
	err    error                               // configured adapter failure result
}

// VerifyCaptcha records each dispatch before returning its configured outcome.
// A configured error takes precedence over the result pointer; when neither
// is supplied, the fixture returns a newly allocated successful challenge result.
func (s *aliyunVerifierSpy) VerifyCaptcha(_ context.Context, cred identity.AliyunCaptchaCredentials, param string) (*identity.AliyunCaptchaVerifyResult, error) {
	s.called++
	s.lastCred = cred
	s.lastParam = param
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	return &identity.AliyunCaptchaVerifyResult{VerifyResult: true}, nil
}

// aliyunEnabledSettings returns complete enabled Aliyun settings for authentication dispatch tests.
func aliyunEnabledSettings() map[string]string {
	return map[string]string{
		identity.SettingKeyAliyunCaptchaEnabled:         "true",
		identity.SettingKeyAliyunCaptchaAccessKeyID:     "ak-id",
		identity.SettingKeyAliyunCaptchaAccessKeySecret: "ak-secret",
		identity.SettingKeyAliyunCaptchaSceneID:         "scene-1",
		identity.SettingKeyAliyunCaptchaPrefix:          "prefix-1",
	}
}

// aliyunTestConfig returns enabled China-region credentials for direct Aliyun service tests.
func aliyunTestConfig() identity.AliyunCaptchaConfig {
	return identity.AliyunCaptchaConfig{
		Enabled:         true,
		AccessKeyID:     "ak-id",
		AccessKeySecret: "ak-secret",
		SceneID:         "scene-1",
		Region:          identity.AliyunCaptchaRegionCN,
	}
}

// TestAliyunCaptchaServiceVerifyParamDispatch submits a serialized browser challenge
// with complete Aliyun settings. It checks one verifier call and the forwarded
// challenge, access identifier, scene and default China endpoint.
func TestAliyunCaptchaServiceVerifyParamDispatch(t *testing.T) {
	spy := &aliyunVerifierSpy{}
	svc := identity.NewAliyunCaptchaService(nil, spy)

	err := svc.VerifyParamWithConfig(context.Background(), aliyunTestConfig(), "captcha-verify-param")

	require.NoError(t, err)
	require.Equal(t, 1, spy.called)
	require.Equal(t, "captcha-verify-param", spy.lastParam)
	require.Equal(t, "ak-id", spy.lastCred.AccessKeyID)
	require.Equal(t, "scene-1", spy.lastCred.SceneID)
	require.Equal(t, "captcha.cn-shanghai.aliyuncs.com", spy.lastCred.Endpoint)
}

// TestAliyunCaptchaServiceSgpEndpoint selects Singapore in an otherwise valid
// Aliyun configuration. The verifier must receive the Singapore endpoint and
// the service must accept its successful challenge response.
func TestAliyunCaptchaServiceSgpEndpoint(t *testing.T) {
	spy := &aliyunVerifierSpy{}
	svc := identity.NewAliyunCaptchaService(nil, spy)
	cfg := aliyunTestConfig()
	cfg.Region = identity.AliyunCaptchaRegionSGP

	err := svc.VerifyParamWithConfig(context.Background(), cfg, "captcha-verify-param")

	require.NoError(t, err)
	require.Equal(t, "captcha.ap-southeast-1.aliyuncs.com", spy.lastCred.Endpoint)
}

// TestAliyunCaptchaServiceFailsClosedOnVerifierError injects an external verification
// failure for complete credentials. The action must return the public Aliyun
// verification error rather than treating the failed call as acceptance.
func TestAliyunCaptchaServiceFailsClosedOnVerifierError(t *testing.T) {
	spy := &aliyunVerifierSpy{err: errors.New("network down")}
	svc := identity.NewAliyunCaptchaService(nil, spy)

	err := svc.VerifyParamWithConfig(context.Background(), aliyunTestConfig(), "captcha-verify-param")

	require.ErrorIs(t, err, identity.ErrAliyunCaptchaVerificationFailed)
}

// TestAliyunCaptchaServiceRejectsVerifyResultFalse supplies a decoded rejected
// challenge with a diagnostic code. It requires the public verification error
// even though the verifier call itself succeeds.
func TestAliyunCaptchaServiceRejectsVerifyResultFalse(t *testing.T) {
	spy := &aliyunVerifierSpy{result: &identity.AliyunCaptchaVerifyResult{VerifyResult: false, VerifyCode: "F001"}}
	svc := identity.NewAliyunCaptchaService(nil, spy)

	err := svc.VerifyParamWithConfig(context.Background(), aliyunTestConfig(), "captcha-verify-param")

	require.ErrorIs(t, err, identity.ErrAliyunCaptchaVerificationFailed)
}

// TestAliyunCaptchaServiceRejectsIncompleteCredentials removes the configured
// access secret from an enabled provider. It requires a configuration error and
// zero verifier calls before the challenge can be dispatched.
func TestAliyunCaptchaServiceRejectsIncompleteCredentials(t *testing.T) {
	spy := &aliyunVerifierSpy{}
	svc := identity.NewAliyunCaptchaService(nil, spy)
	cfg := aliyunTestConfig()
	cfg.AccessKeySecret = ""

	err := svc.VerifyParamWithConfig(context.Background(), cfg, "captcha-verify-param")

	require.ErrorIs(t, err, identity.ErrAliyunCaptchaNotConfigured)
	require.Zero(t, spy.called)
}

// TestAliyunCaptchaServiceRejectsEmptyParam supplies complete credentials and an
// empty browser challenge. The service must reject that action locally and leave
// the external verifier's call count at zero.
func TestAliyunCaptchaServiceRejectsEmptyParam(t *testing.T) {
	spy := &aliyunVerifierSpy{}
	svc := identity.NewAliyunCaptchaService(nil, spy)

	err := svc.VerifyParamWithConfig(context.Background(), aliyunTestConfig(), "")

	require.ErrorIs(t, err, identity.ErrAliyunCaptchaVerificationFailed)
	require.Zero(t, spy.called)
}

// TestAliyunCaptchaServiceValidateCredentials probes settings with credential
// errors, a transport failure and a rejected challenge result. It distinguishes
// invalid credentials from other failures and accepts a nil verifier error.
func TestAliyunCaptchaServiceValidateCredentials(t *testing.T) {
	t.Run("invalid credential code", func(t *testing.T) {
		spy := &aliyunVerifierSpy{err: &identity.AliyunCaptchaAPIError{Code: "SignatureDoesNotMatch", Message: "bad sk"}}
		svc := identity.NewAliyunCaptchaService(nil, spy)

		err := svc.ValidateCredentials(context.Background(), "id", "sk", "scene", "cn")
		require.ErrorIs(t, err, identity.ErrCaptchaInvalidCredentials)
	})

	t.Run("network error surfaces", func(t *testing.T) {
		spy := &aliyunVerifierSpy{err: errors.New("timeout")}
		svc := identity.NewAliyunCaptchaService(nil, spy)

		err := svc.ValidateCredentials(context.Background(), "id", "sk", "scene", "cn")
		require.Error(t, err)
		require.NotErrorIs(t, err, identity.ErrCaptchaInvalidCredentials)
	})

	t.Run("verify result false means credentials valid", func(t *testing.T) {
		spy := &aliyunVerifierSpy{result: &identity.AliyunCaptchaVerifyResult{VerifyResult: false}}
		svc := identity.NewAliyunCaptchaService(nil, spy)

		err := svc.ValidateCredentials(context.Background(), "id", "sk", "scene", "sgp")
		require.NoError(t, err)
		require.Equal(t, "captcha.ap-southeast-1.aliyuncs.com", spy.lastCred.Endpoint)
	})
}

// TestAuthServiceVerifyCaptchaDispatchesAliyun enables Aliyun in the shared
// authentication settings snapshot. It checks that the compatibility request
// field reaches Aliyun once as the serialized browser challenge.
func TestAuthServiceVerifyCaptchaDispatchesAliyun(t *testing.T) {
	spy := &aliyunVerifierSpy{}
	authService := newAliyunAuthServiceForTest(&identity.AuthOptions{}, aliyunEnabledSettings(), spy)

	// The shared request shape carries Aliyun's serialized challenge in
	// TurnstileToken, so provider dispatch forwards it intact to the Aliyun adapter.
	err := authService.VerifyCaptcha(context.Background(), identity.CaptchaProof{TurnstileToken: "captcha-verify-param"}, "127.0.0.1")

	require.NoError(t, err)
	require.Equal(t, 1, spy.called)
	require.Equal(t, "captcha-verify-param", spy.lastParam)
}

// TestAuthServiceVerifyCaptchaRejectsProviderConflict enables Turnstile alongside
// Aliyun in the same settings value. Authentication must report the provider
// conflict and leave the Aliyun verifier uncalled.
func TestAuthServiceVerifyCaptchaRejectsProviderConflict(t *testing.T) {
	settings := aliyunEnabledSettings()
	settings[identity.SettingKeyTurnstileEnabled] = "true"
	settings[identity.SettingKeyTurnstileSecretKey] = "secret"
	spy := &aliyunVerifierSpy{}
	authService := newAliyunAuthServiceForTest(&identity.AuthOptions{}, settings, spy)

	err := authService.VerifyCaptcha(context.Background(), identity.CaptchaProof{TurnstileToken: "param"}, "127.0.0.1")

	require.ErrorIs(t, err, identity.ErrCaptchaProviderConflict)
	require.Zero(t, spy.called)
}

// TestAuthServiceVerifyCaptchaRequiredModeWithAliyun enables required CAPTCHA
// with complete Aliyun credentials. Authentication must accept the verifier's
// successful challenge and dispatch exactly one Aliyun verification call.
func TestAuthServiceVerifyCaptchaRequiredModeWithAliyun(t *testing.T) {
	cfg := captchaAuthOptions(true)
	spy := &aliyunVerifierSpy{}
	authService := newAliyunAuthServiceForTest(cfg, aliyunEnabledSettings(), spy)

	// Required mode accepts the selected Aliyun provider when its complete
	// credentials permit a verified challenge, satisfying the authentication proof requirement.
	err := authService.VerifyCaptcha(context.Background(), identity.CaptchaProof{TurnstileToken: "captcha-verify-param"}, "127.0.0.1")

	require.NoError(t, err)
	require.Equal(t, 1, spy.called)
}

// TestAuthServiceVerifyActionCaptchaIfEnabledDispatchesAliyun selects Aliyun for an
// extended authentication action. It requires one successful dispatch with the
// submitted serialized challenge unchanged at the external verifier.
func TestAuthServiceVerifyActionCaptchaIfEnabledDispatchesAliyun(t *testing.T) {
	spy := &aliyunVerifierSpy{}
	authService := newAliyunAuthServiceForTest(&identity.AuthOptions{}, aliyunEnabledSettings(), spy)

	err := authService.VerifyActionCaptchaIfEnabled(context.Background(), identity.CaptchaProof{TurnstileToken: "captcha-verify-param"}, "127.0.0.1")

	require.NoError(t, err)
	require.Equal(t, 1, spy.called)
	require.Equal(t, "captcha-verify-param", spy.lastParam)
}

// TestAuthServiceVerifyActionCaptchaIfEnabledSkipsWhenOnlyTurnstile enables Turnstile
// with no extended-action CAPTCHA provider. That action succeeds with an empty
// proof and leaves the Aliyun verifier's call count at zero.
func TestAuthServiceVerifyActionCaptchaIfEnabledSkipsWhenOnlyTurnstile(t *testing.T) {
	spy := &aliyunVerifierSpy{}
	authService := newAliyunAuthServiceForTest(&identity.AuthOptions{}, map[string]string{
		identity.SettingKeyTurnstileEnabled:   "true",
		identity.SettingKeyTurnstileSecretKey: "secret",
	}, spy)

	// Extended OAuth and Passkey start actions dispatch Tencent or Aliyun; a
	// Turnstile-only snapshot leaves this check successful with no challenge verification.
	err := authService.VerifyActionCaptchaIfEnabled(context.Background(), identity.CaptchaProof{}, "127.0.0.1")

	require.NoError(t, err)
	require.Zero(t, spy.called)
}

// newAliyunAuthServiceForTest wires authentication to supplied CAPTCHA settings
// and an Aliyun spy. It also configures a Turnstile fixture and logging so tests
// can exercise provider selection through the shared AuthService entry points.
func newAliyunAuthServiceForTest(options *identity.AuthOptions, values map[string]string, verifier *aliyunVerifierSpy) *identity.AuthService {
	runtime := identity.NewRuntimeSettings(&captchaSettingsStore{values: values}, settingscore.ErrSettingNotFound)
	turnstile := identity.NewTurnstileService(runtime, &turnstileVerifierSpy{})
	turnstile.SetObserver(logging.LegacyPrintf)
	aliyun := identity.NewAliyunCaptchaService(runtime, verifier)
	aliyun.SetObserver(logging.LegacyPrintf)
	return identity.NewAuthService(&identity.AuthDependencies{Options: options, Settings: captchaAuthSettings{runtime: runtime}, Turnstile: turnstile, Aliyun: aliyun, Observer: identity.Observer{Log: logging.LegacyPrintf}}, nil)
}
