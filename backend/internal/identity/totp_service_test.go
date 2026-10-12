package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// totpVMUserRepoStub supplies user operations for verification-method tests.
// Calls to unimplemented methods on the embedded nil interface panic.
type totpVMUserRepoStub struct {
	UserRepository

	user          *User
	totpDisabled  bool
	disableCalled bool
}

type totpVMSettingRepoStub struct {
	settingscore.Repository
	values map[string]string
}

// totpVerificationSettings supplies the email-verification switch for method-selection tests.
type totpVerificationSettings struct {
	TotpSettings
	runtime *RuntimeSettings
}

func (s *totpVMUserRepoStub) GetByID(ctx context.Context, id int64) (*User, error) {
	if s.user == nil {
		return nil, errors.New("user not found")
	}
	return s.user, nil
}

func (s *totpVMUserRepoStub) DisableTotp(ctx context.Context, userID int64) error {
	s.disableCalled = true
	s.totpDisabled = true
	return nil
}

func (s *totpVMSettingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	v, ok := s.values[key]
	if !ok {
		return "", errors.New("setting not found")
	}
	return v, nil
}

func newTotpVMService(t *testing.T, user *User, emailVerifyEnabled bool) (*TotpService, *totpVMUserRepoStub) {
	t.Helper()
	userRepo := &totpVMUserRepoStub{user: user}
	values := map[string]string{}
	if emailVerifyEnabled {
		values[SettingKeyEmailVerifyEnabled] = "true"
	}
	settingSvc := NewRuntimeSettings(&totpVMSettingRepoStub{values: values}, settingscore.ErrSettingNotFound)
	return NewTotpService(userRepo, nil, nil, totpVerificationSettings{runtime: settingSvc}, nil, nil), userRepo
}

func TestGetVerificationMethodAdminAlwaysPassword(t *testing.T) {
	admin := &User{ID: 1, Email: "admin@example.com", Role: RoleAdmin}
	svc, _ := newTotpVMService(t, admin, true)

	method, err := svc.GetVerificationMethod(context.Background(), admin.ID)
	require.NoError(t, err)
	require.Equal(t, "password", method.Method)
}

func TestGetVerificationMethodRegularUserFollowsEmailVerifySetting(t *testing.T) {
	user := &User{ID: 2, Email: "user@example.com", Role: RoleUser}

	svcEmailOn, _ := newTotpVMService(t, user, true)
	method, err := svcEmailOn.GetVerificationMethod(context.Background(), user.ID)
	require.NoError(t, err)
	require.Equal(t, "email", method.Method)

	svcEmailOff, _ := newTotpVMService(t, user, false)
	method, err = svcEmailOff.GetVerificationMethod(context.Background(), user.ID)
	require.NoError(t, err)
	require.Equal(t, "password", method.Method)
}

func TestTotpDisableAdminUsesPasswordEvenWithEmailVerifyEnabled(t *testing.T) {
	admin := &User{ID: 1, Email: "admin@example.com", Role: RoleAdmin, TotpEnabled: true}
	require.NoError(t, admin.SetPassword("correct-password"))
	svc, userRepo := newTotpVMService(t, admin, true)

	// An empty password is rejected.
	err := svc.Disable(context.Background(), admin.ID, "", "")
	require.ErrorIs(t, err, ErrPasswordRequired)

	// An incorrect password cannot disable TOTP.
	err = svc.Disable(context.Background(), admin.ID, "", "wrong-password")
	require.ErrorIs(t, err, ErrPasswordIncorrect)

	// The correct password disables TOTP; the nil email service would panic if email verification were selected.
	err = svc.Disable(context.Background(), admin.ID, "", "correct-password")
	require.NoError(t, err)
	require.True(t, userRepo.disableCalled)
}

func TestTotpDisableRegularUserStillRequiresEmailCode(t *testing.T) {
	user := &User{ID: 2, Email: "user@example.com", Role: RoleUser, TotpEnabled: true}
	require.NoError(t, user.SetPassword("whatever"))
	svc, _ := newTotpVMService(t, user, true)

	err := svc.Disable(context.Background(), user.ID, "", "whatever")
	require.ErrorIs(t, err, ErrVerifyCodeRequired)
}

func (s totpVerificationSettings) IsEmailVerifyEnabled(ctx context.Context) bool {
	return s.runtime.IsEmailVerifyEnabled(ctx)
}
