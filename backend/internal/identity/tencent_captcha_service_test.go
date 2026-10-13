package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// newTencentCaptchaTestService creates a Tencent fixture with the default site.
// It delegates settings construction to the region-aware helper, leaving the
// stored region absent so runtime normalization selects mainland China.
func newTencentCaptchaTestService(verifier identity.TencentCaptchaVerifier) *identity.TencentCaptchaService {
	return newTencentCaptchaTestServiceWithRegion(verifier, "")
}

// newTencentCaptchaTestServiceWithRegion creates enabled settings with complete
// application and cloud credentials. Empty region input leaves the key absent;
// other values are stored for the runtime reader to normalize before verification.
func newTencentCaptchaTestServiceWithRegion(verifier identity.TencentCaptchaVerifier, region string) *identity.TencentCaptchaService {
	values := map[string]string{
		identity.SettingKeyTencentCaptchaEnabled:        "true",
		identity.SettingKeyTencentCaptchaAppID:          "123456789",
		identity.SettingKeyTencentCaptchaAppSecretKey:   "app-secret",
		identity.SettingKeyTencentCaptchaCloudSecretID:  "cloud-secret-id",
		identity.SettingKeyTencentCaptchaCloudSecretKey: "cloud-secret-key",
	}
	if region != "" {
		values[identity.SettingKeyTencentCaptchaRegion] = region
	}
	settings := identity.NewRuntimeSettings(&captchaSettingsStore{values: values}, settingscore.ErrSettingNotFound)
	return identity.NewTencentCaptchaService(settings, verifier)
}

// TestTencentCaptchaServiceRoutesVerifyEndpointByRegion supplies missing, China,
// international and unrecognized settings regions. It checks the resulting
// verifier endpoint after the runtime reader normalizes each selected site.
func TestTencentCaptchaServiceRoutesVerifyEndpointByRegion(t *testing.T) {
	cases := []struct {
		name         string // site selection case label
		region       string // stored site selection value
		wantEndpoint string // expected verification server hostname
	}{
		{"未配置回落中国站", "", "captcha.tencentcloudapi.com"},
		{"中国站", identity.TencentCaptchaRegionCN, "captcha.tencentcloudapi.com"},
		{"国际站", identity.TencentCaptchaRegionINTL, "captcha.intl.tencentcloudapi.com"},
		{"非法值回落中国站", "sgp", "captcha.tencentcloudapi.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verifier := &tencentCaptchaVerifierStub{response: &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}}
			svc := newTencentCaptchaTestServiceWithRegion(verifier, tc.region)

			require.NoError(t, svc.VerifyTicket(context.Background(), "ticket", "@rand", "203.0.113.10"))
			require.Equal(t, tc.wantEndpoint, verifier.credentials.Endpoint)
		})
	}
}

// TestTencentCaptchaServiceAcceptsCaptchaCodeOne submits a complete browser
// proof and receives the accepted decision code. It checks successful dispatch
// and the exact ticket, random string and client IP forwarded.
func TestTencentCaptchaServiceAcceptsCaptchaCodeOne(t *testing.T) {
	verifier := &tencentCaptchaVerifierStub{response: &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := newTencentCaptchaTestService(verifier)

	err := svc.VerifyTicket(context.Background(), "ticket", "@rand", "203.0.113.10")

	require.NoError(t, err)
	require.Equal(t, 1, verifier.calls)
	require.Equal(t, identity.TencentCaptchaProof{Ticket: "ticket", Randstr: "@rand"}, verifier.proof)
	require.Equal(t, "203.0.113.10", verifier.remoteIP)
}

// TestTencentCaptchaServiceRejectsDisasterRecoveryTicketWithoutCallingVerifier uses
// a ticket carrying the browser error prefix. It requires the public verification
// error and zero external verifier calls despite complete server settings.
func TestTencentCaptchaServiceRejectsDisasterRecoveryTicketWithoutCallingVerifier(t *testing.T) {
	verifier := &tencentCaptchaVerifierStub{response: &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := newTencentCaptchaTestService(verifier)

	err := svc.VerifyTicket(context.Background(), "trerror_1001_123456789_1", "@rand", "203.0.113.10")

	require.ErrorIs(t, err, identity.ErrTencentCaptchaVerificationFailed)
	require.Zero(t, verifier.calls)
}

// TestTencentCaptchaServiceRejectsEveryNonOneCode returns several rejected vendor
// decision codes after a valid dispatch. Each sampled non-one code must produce
// the public Tencent verification error for that action.
func TestTencentCaptchaServiceRejectsEveryNonOneCode(t *testing.T) {
	for _, code := range []int64{0, 7, 8, 9, 15, 16, 21, 100} {
		t.Run(string(rune(code)), func(t *testing.T) {
			verifier := &tencentCaptchaVerifierStub{response: &identity.TencentCaptchaVerifyResponse{CaptchaCode: code}}
			svc := newTencentCaptchaTestService(verifier)

			err := svc.VerifyTicket(context.Background(), "ticket", "@rand", "203.0.113.10")

			require.ErrorIs(t, err, identity.ErrTencentCaptchaVerificationFailed)
		})
	}
}

// TestTencentCaptchaServiceFailsClosedOnVerifierError injects an adapter failure
// after complete configuration and proof checks. It requires an error matching
// ErrTencentCaptchaVerificationFailed so the action cannot treat that failure as acceptance.
func TestTencentCaptchaServiceFailsClosedOnVerifierError(t *testing.T) {
	verifier := &tencentCaptchaVerifierStub{err: errors.New("sdk unavailable")}
	svc := newTencentCaptchaTestService(verifier)

	err := svc.VerifyTicket(context.Background(), "ticket", "@rand", "203.0.113.10")

	require.Error(t, err)
	require.ErrorIs(t, err, identity.ErrTencentCaptchaVerificationFailed)
}

// TestTencentCaptchaServiceRejectsIncompleteConfiguration enables Tencent with
// an application ID and missing secrets. It requires a configuration error and
// zero verifier calls before the browser challenge is dispatched.
func TestTencentCaptchaServiceRejectsIncompleteConfiguration(t *testing.T) {
	settings := identity.NewRuntimeSettings(&captchaSettingsStore{values: map[string]string{
		identity.SettingKeyTencentCaptchaEnabled: "true",
		identity.SettingKeyTencentCaptchaAppID:   "123456789",
	}}, settingscore.ErrSettingNotFound)
	verifier := &tencentCaptchaVerifierStub{response: &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := identity.NewTencentCaptchaService(settings, verifier)

	err := svc.VerifyTicket(context.Background(), "ticket", "@rand", "203.0.113.10")

	require.ErrorIs(t, err, identity.ErrTencentCaptchaNotConfigured)
	require.Zero(t, verifier.calls)
}

// TestTencentCaptchaServiceFailsClosedOnSettingsReadError makes the batch settings
// read fail before provider validation. Authentication must return service
// unavailable and leave the external verifier's call count at zero.
func TestTencentCaptchaServiceFailsClosedOnSettingsReadError(t *testing.T) {
	settings := identity.NewRuntimeSettings(&captchaSettingsStore{err: errors.New("settings unavailable")}, settingscore.ErrSettingNotFound)
	verifier := &tencentCaptchaVerifierStub{response: &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := identity.NewTencentCaptchaService(settings, verifier)

	err := svc.VerifyTicket(context.Background(), "ticket", "@rand", "203.0.113.10")

	require.ErrorIs(t, err, identity.ErrServiceUnavailable)
	require.Zero(t, verifier.calls)
}
