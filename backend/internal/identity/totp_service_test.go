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
	// UserRepository remains nil for unimplemented methods. These tests exercise
	// GetByID and DisableTotp directly; any other delegated call panics.
	UserRepository

	// user is shared by fixture lookups regardless of ID. Tests initialize it
	// before service calls and keep other account state stable.
	user *User
	// totpDisabled and disableCalled are set together by DisableTotp for every
	// ID. They record successful fixture delegation; the shared user retains its
	// previous security fields.
	totpDisabled  bool // observed repository disable effect
	disableCalled bool // repository disable invocation flag
}

// totpVMSettingRepoStub supplies runtime switch reads while leaving other
// settings operations unavailable.
type totpVMSettingRepoStub struct {
	// Repository remains nil for unimplemented operations. RuntimeSettings uses
	// this fixture's GetValue override, so other delegated settings calls would
	// panic.
	settingscore.Repository
	values map[string]string // configured runtime setting text
}

// totpVerificationSettings delegates the email switch used by TOTP
// proof-selection tests.
type totpVerificationSettings struct {
	// TotpSettings remains nil for unused settings methods. Only the overridden
	// email-switch read is available to these method-selection and disable
	// tests.
	TotpSettings
	runtime *RuntimeSettings // runtime email switch reader
}

// GetByID returns the fixture's shared user pointer regardless of requested
// ID. An absent fixture user returns an error; tests configure this pointer
// before use and do not model repository copying or concurrent writes.
func (s *totpVMUserRepoStub) GetByID(ctx context.Context, id int64) (*User, error) {
	if s.user == nil {
		return nil, errors.New("user not found")
	}
	return s.user, nil
}

// DisableTotp records both disable-observation flags and returns success for
// any ID. It leaves the fixture account fields unchanged, so the tests assert
// service delegation rather than persistent TOTP-state clearing.
func (s *totpVMUserRepoStub) DisableTotp(ctx context.Context, userID int64) error {
	s.disableCalled = true
	s.totpDisabled = true
	return nil
}

// GetValue reads the fixture map for a runtime setting key. Missing keys
// return an error that RuntimeSettings converts to its default; tests
// configure the map before service calls.
func (s *totpVMSettingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	v, ok := s.values[key]
	if !ok {
		return "", errors.New("setting not found")
	}
	return v, nil
}

// newTotpVMService assembles method-selection fixtures for the supplied
// account and email switch. Other TOTP/settings methods and cache/encryption
// services remain unavailable, so callers use the returned service only for
// proof selection or the tested password disable path.
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

// TestGetVerificationMethodAdminAlwaysPassword enables email verification for
// an administrator account. It checks that the service still selects password
// proof for the account's TOTP settings changes.
func TestGetVerificationMethodAdminAlwaysPassword(t *testing.T) {
	admin := &User{ID: 1, Email: "admin@example.com", Role: RoleAdmin}
	svc, _ := newTotpVMService(t, admin, true)

	method, err := svc.GetVerificationMethod(context.Background(), admin.ID)
	require.NoError(t, err)
	require.Equal(t, "password", method.Method)
}

// TestGetVerificationMethodRegularUserFollowsEmailVerifySetting changes the
// runtime email switch for an ordinary account. It checks email selection
// when enabled and password selection when disabled.
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

// TestTotpDisableAdminUsesPasswordEvenWithEmailVerifyEnabled exercises
// missing, wrong and correct administrator passwords with email verification
// enabled. It checks the two rejection errors and successful repository
// delegation without invoking the nil email verifier.
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

	// Successful password proof reaches the disable stub. The nil email verifier
	// makes an accidental email-selection change fail before that observation.
	err = svc.Disable(context.Background(), admin.ID, "", "correct-password")
	require.NoError(t, err)
	require.True(t, userRepo.disableCalled)
}

// TestTotpDisableRegularUserStillRequiresEmailCode supplies a password to an
// ordinary account while email verification is enabled. It checks that a
// missing-email-code rejection is returned even when a local password is
// supplied.
func TestTotpDisableRegularUserStillRequiresEmailCode(t *testing.T) {
	user := &User{ID: 2, Email: "user@example.com", Role: RoleUser, TotpEnabled: true}
	require.NoError(t, user.SetPassword("whatever"))
	svc, _ := newTotpVMService(t, user, true)

	err := svc.Disable(context.Background(), user.ID, "", "whatever")
	require.ErrorIs(t, err, ErrVerifyCodeRequired)
}

// IsEmailVerifyEnabled delegates the fixture's runtime email-verification
// switch to RuntimeSettings.
func (s totpVerificationSettings) IsEmailVerifyEnabled(ctx context.Context) bool {
	return s.runtime.IsEmailVerifyEnabled(ctx)
}
