package identity

import (
	"context"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
)

// clockEmailCache captures notification challenge timestamps written by clock tests.
type clockEmailCache struct {
	EmailCache // unimplemented cache method surface
	// value retains the supplied record pointer so the test can inspect both
	// timestamps after ProfileSaveNotifyVerifyCodeWithClock completes its writes.
	value *VerificationCodeData
}

// TestSessionClockControlsSigningAndValidation signs and validates a JWT using a
// mutable injected clock. It checks both stored timestamps, then advances beyond
// expiry and requires ErrTokenExpired from validation.
func TestSessionClockControlsSigningAndValidation(t *testing.T) {
	now := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	svc := NewSessionService(SessionOptions{Secret: "test-clock-fixture", ExpireHour: 1, Now: func() time.Time { return now }}, nil, nil, nil, nil)
	token, err := svc.GenerateAccessToken(&User{ID: 1, Email: "clock@example.com", Role: "user"}, "sid", "")
	require.NoError(t, err)
	claims, err := svc.ValidateToken(token)
	require.NoError(t, err)
	require.Equal(t, now.Unix(), claims.IssuedAt.Unix())
	require.Equal(t, now.Add(time.Hour).Unix(), claims.ExpiresAt.Unix())
	now = now.Add(2 * time.Hour)
	_, err = svc.ValidateToken(token)
	require.ErrorIs(t, err, ErrTokenExpired)
}

// TestTotpClockUsesOriginalValidationWindow generates a TOTP code at a fixed
// injected time. One later period remains valid under the configured skew,
// while moving three periods from generation rejects that code.
func TestTotpClockUsesOriginalValidationWindow(t *testing.T) {
	now := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	secret := "JBSWY3DPEHPK3PXP"
	svc := NewTotpService(nil, nil, nil, nil, nil, nil, func() time.Time { return now })
	code, err := totp.GenerateCode(secret, now)
	require.NoError(t, err)
	require.True(t, svc.validateCode(code, secret))
	now = now.Add(30 * time.Second)
	require.True(t, svc.validateCode(code, secret))
	now = now.Add(60 * time.Second)
	require.False(t, svc.validateCode(code, secret))
}

// SetNotifyVerifyCode retains the notification record for timestamp assertions.
// The fixture ignores the address and TTL because this test observes the
// service's clock reads through the supplied record pointer.
func (c *clockEmailCache) SetNotifyVerifyCode(_ context.Context, _ string, data *VerificationCodeData, _ time.Duration) error {
	c.value = data
	return nil
}

// TestNotifyClockKeepsSeparateReadPoints advances the injected clock by one
// second on every read. It requires two reads and verifies that the expiry
// includes the second read's extra second plus VerifyCodeTTL.
func TestNotifyClockKeepsSeparateReadPoints(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	calls := 0
	clock := func() time.Time { v := base.Add(time.Duration(calls) * time.Second); calls++; return v }
	cache := &clockEmailCache{}
	require.NoError(t, ProfileSaveNotifyVerifyCodeWithClock(context.Background(), cache, "clock@example.com", "123456", clock))
	require.Equal(t, 2, calls)
	require.Equal(t, base, cache.value.CreatedAt)
	require.Equal(t, base.Add(time.Second+VerifyCodeTTL), cache.value.ExpiresAt)
}
