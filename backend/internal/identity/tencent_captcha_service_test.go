package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

func newTencentCaptchaTestService(verifier identity.TencentCaptchaVerifier) *identity.TencentCaptchaService {
	return newTencentCaptchaTestServiceWithRegion(verifier, "")
}

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

// TestTencentCaptchaServiceRoutesVerifyEndpointByRegion checks that region selection reaches the verifier.
// International applications require the international verification endpoint.
func TestTencentCaptchaServiceRoutesVerifyEndpointByRegion(t *testing.T) {
	cases := []struct {
		name         string
		region       string
		wantEndpoint string
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

func TestTencentCaptchaServiceAcceptsCaptchaCodeOne(t *testing.T) {
	verifier := &tencentCaptchaVerifierStub{response: &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := newTencentCaptchaTestService(verifier)

	err := svc.VerifyTicket(context.Background(), "ticket", "@rand", "203.0.113.10")

	require.NoError(t, err)
	require.Equal(t, 1, verifier.calls)
	require.Equal(t, identity.TencentCaptchaProof{Ticket: "ticket", Randstr: "@rand"}, verifier.proof)
	require.Equal(t, "203.0.113.10", verifier.remoteIP)
}

func TestTencentCaptchaServiceRejectsDisasterRecoveryTicketWithoutCallingVerifier(t *testing.T) {
	verifier := &tencentCaptchaVerifierStub{response: &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := newTencentCaptchaTestService(verifier)

	err := svc.VerifyTicket(context.Background(), "trerror_1001_123456789_1", "@rand", "203.0.113.10")

	require.ErrorIs(t, err, identity.ErrTencentCaptchaVerificationFailed)
	require.Zero(t, verifier.calls)
}

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

func TestTencentCaptchaServiceFailsClosedOnVerifierError(t *testing.T) {
	verifier := &tencentCaptchaVerifierStub{err: errors.New("sdk unavailable")}
	svc := newTencentCaptchaTestService(verifier)

	err := svc.VerifyTicket(context.Background(), "ticket", "@rand", "203.0.113.10")

	require.Error(t, err)
	require.ErrorIs(t, err, identity.ErrTencentCaptchaVerificationFailed)
}

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

func TestTencentCaptchaServiceFailsClosedOnSettingsReadError(t *testing.T) {
	settings := identity.NewRuntimeSettings(&captchaSettingsStore{err: errors.New("settings unavailable")}, settingscore.ErrSettingNotFound)
	verifier := &tencentCaptchaVerifierStub{response: &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := identity.NewTencentCaptchaService(settings, verifier)

	err := svc.VerifyTicket(context.Background(), "ticket", "@rand", "203.0.113.10")

	require.ErrorIs(t, err, identity.ErrServiceUnavailable)
	require.Zero(t, verifier.calls)
}
