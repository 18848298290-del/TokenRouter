package identity_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitytestkit "github.com/TokenFlux/TokenRouter/internal/identity/testkit"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// invitationRaceUserRepo stores users for concurrent invitation-registration tests.
type invitationRaceUserRepo struct {
	identity.UserRepository

	mu      sync.Mutex
	nextID  int64
	byEmail map[string]*identity.User
}

// invitationRaceRedeemRepo models single-use invitations with a conditional update.
type invitationRaceRedeemRepo struct {
	billing.RedeemCodeRepository

	mu   sync.Mutex
	code billing.RedeemCode
}

func TestAuthService_RegisterSnapshotsDefaultUserAPIKeyLimit(t *testing.T) {
	tests := []struct {
		name   string
		value  *string
		expect int
	}{
		{name: "设置缺失回退内置值", expect: identity.DefaultUserAPIKeyLimit},
		{name: "显式不限量", value: stringPointer("0"), expect: 0},
		{name: "显式上限", value: stringPointer("23"), expect: 23},
		{name: "非法设置回退内置值", value: stringPointer("invalid"), expect: identity.DefaultUserAPIKeyLimit},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := map[string]string{identity.SettingKeyRegistrationEnabled: "true"}
			if tt.value != nil {
				settings[identity.SettingKeyDefaultUserAPIKeyLimit] = *tt.value
			}
			repo := &userRepoStub{nextID: int64(index + 1)}
			svc := newAuthService(repo, settings, nil)

			_, user, err := svc.RegisterWithVerification(context.Background(), fmt.Sprintf("api-limit-%d@example.com", index), "strong-pass", "", "", "", "")
			require.NoError(t, err)
			require.Equal(t, tt.expect, user.APIKeyLimit)
			require.Equal(t, tt.expect, repo.created[0].APIKeyLimit)
		})
	}
}

// TestAuthService_DefaultUserAPIKeyLimitDoesNotRetroactivelyChangeUsers checks that existing users retain their signup limit snapshot.
func TestAuthService_DefaultUserAPIKeyLimitDoesNotRetroactivelyChangeUsers(t *testing.T) {
	settings := map[string]string{
		identity.SettingKeyRegistrationEnabled:    "true",
		identity.SettingKeyDefaultUserAPIKeyLimit: "10",
	}
	repo := &userRepoStub{}
	svc := newAuthService(repo, settings, nil)

	_, first, err := svc.RegisterWithVerification(context.Background(), "api-limit-first@example.com", "strong-pass", "", "", "", "")
	require.NoError(t, err)
	require.Equal(t, 10, first.APIKeyLimit)

	settings[identity.SettingKeyDefaultUserAPIKeyLimit] = "20"
	_, second, err := svc.RegisterWithVerification(context.Background(), "api-limit-second@example.com", "strong-pass", "", "", "", "")
	require.NoError(t, err)
	require.Equal(t, 20, second.APIKeyLimit)
	require.Equal(t, 10, first.APIKeyLimit)
	require.Equal(t, 10, repo.created[0].APIKeyLimit)
}

// TestAuthService_AllOAuthSourcesSnapshotDefaultUserAPIKeyLimit checks that each provider snapshots the API key limit at signup.
func TestAuthService_AllOAuthSourcesSnapshotDefaultUserAPIKeyLimit(t *testing.T) {
	for index, signupSource := range []string{"linuxdo", "wechat", "oidc", "github", "google", "dingtalk"} {
		t.Run(signupSource, func(t *testing.T) {
			repo := &userRepoStub{}
			svc := newAuthService(repo, map[string]string{
				identity.SettingKeyRegistrationEnabled:    "true",
				identity.SettingKeyDefaultUserAPIKeyLimit: "29",
			}, nil)
			svc.RefreshTokens = &refreshTokenCacheStub{}
			rebuildSessionForTest(svc)

			_, user, err := svc.LoginOrRegisterOAuthWithTokenPair(context.Background(), fmt.Sprintf("api-limit-oauth-%d@example.com", index), "OAuth User", "", "", signupSource)
			require.NoError(t, err)
			require.Equal(t, 29, user.APIKeyLimit)
			require.Equal(t, 29, repo.created[0].APIKeyLimit)
		})
	}
}

func stringPointer(value string) *string {
	return &value
}

func newInvitationRaceUserRepo() *invitationRaceUserRepo {
	return &invitationRaceUserRepo{
		nextID:  1,
		byEmail: make(map[string]*identity.User),
	}
}

func (r *invitationRaceUserRepo) ExistsByEmail(_ context.Context, email string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, exists := r.byEmail[email]
	return exists, nil
}

func (r *invitationRaceUserRepo) ExistsByNormalizedEmail(context.Context, string) (bool, error) {
	return false, nil
}

func (r *invitationRaceUserRepo) Create(_ context.Context, user *identity.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byEmail[user.Email]; exists {
		return identity.ErrEmailExists
	}
	user.ID = r.nextID
	r.nextID++
	clone := *user
	r.byEmail[user.Email] = &clone
	return nil
}

func (r *invitationRaceRedeemRepo) GetByCode(_ context.Context, code string) (*billing.RedeemCode, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.code.Code != code {
		return nil, billing.ErrRedeemCodeNotFound
	}
	clone := r.code
	return &clone, nil
}

func (r *invitationRaceRedeemRepo) Use(_ context.Context, id, userID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.code.ID != id {
		return billing.ErrRedeemCodeNotFound
	}
	if r.code.Status != billing.StatusUnused {
		return billing.ErrRedeemCodeUsed
	}
	now := time.Now().UTC()
	r.code.Status = billing.StatusUsed
	r.code.UsedBy = &userID
	r.code.UsedAt = &now
	r.code.UsedCount = 1
	return nil
}

// TestAuthService_Register_InvitationCodeSingleUseUnderConcurrency checks that concurrent registrations consume one invitation once.
func TestAuthService_Register_InvitationCodeSingleUseUnderConcurrency(t *testing.T) {
	const (
		code   = "INV-RACE-001"
		called = 8
	)
	userRepo := newInvitationRaceUserRepo()
	redeemRepo := &invitationRaceRedeemRepo{code: billing.RedeemCode{
		ID:     1,
		Code:   code,
		Type:   billing.RedeemTypeInvitation,
		Status: billing.StatusUnused,
	}}
	svc := newOAuthEmailFlowAuthService(
		userRepo,
		redeemRepo,
		&refreshTokenCacheStub{},
		map[string]string{
			identity.SettingKeyRegistrationEnabled:    "true",
			promotion.SettingKeyInvitationCodeEnabled: "true",
		},
		nil,
	)

	start := make(chan struct{})
	results := make(chan error, called)
	var group sync.WaitGroup
	for index := range called {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			_, _, err := svc.RegisterWithVerification(
				context.Background(),
				fmt.Sprintf("race-%d@example.com", index),
				"Password123!",
				"",
				"",
				code,
				"",
			)
			results <- err
		}(index)
	}
	close(start)
	group.Wait()
	close(results)

	successes := 0
	rejected := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, identity.ErrInvitationCodeInvalid):
			rejected++
		default:
			t.Fatalf("unexpected registration error: %v", err)
		}
	}

	require.Equal(t, 1, successes)
	require.Equal(t, called-1, rejected)
	redeemRepo.mu.Lock()
	require.Equal(t, billing.StatusUsed, redeemRepo.code.Status)
	redeemRepo.mu.Unlock()
}

func tencentCaptchaSettings() map[string]string {
	return map[string]string{
		identity.SettingKeyTencentCaptchaEnabled:        "true",
		identity.SettingKeyTencentCaptchaAppID:          "123456789",
		identity.SettingKeyTencentCaptchaAppSecretKey:   "app-secret",
		identity.SettingKeyTencentCaptchaCloudSecretID:  "cloud-secret-id",
		identity.SettingKeyTencentCaptchaCloudSecretKey: "cloud-secret-key",
	}
}

func TestVerifyCaptchaUsesTencentWhenEnabled(t *testing.T) {
	verifier := &tencentCaptchaVerifierStub{response: &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := newAuthServiceForCaptchaTest(tencentCaptchaSettings(), false, nil, verifier)

	err := svc.VerifyCaptcha(context.Background(), identity.CaptchaProof{
		TencentTicket:  "ticket",
		TencentRandstr: "@rand",
	}, "203.0.113.10")

	require.NoError(t, err)
	require.Equal(t, 1, verifier.calls)
}

func TestVerifyCaptchaRejectsDirtyDoubleEnabledSettings(t *testing.T) {
	settings := tencentCaptchaSettings()
	settings[identity.SettingKeyTurnstileEnabled] = "true"
	settings[identity.SettingKeyTurnstileSecretKey] = "turnstile-secret"
	turnstileVerifier := &turnstileVerifierSpy{}
	tencentVerifier := &tencentCaptchaVerifierStub{response: &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := newAuthServiceForCaptchaTest(settings, false, turnstileVerifier, tencentVerifier)

	err := svc.VerifyCaptcha(context.Background(), identity.CaptchaProof{
		TurnstileToken: "turnstile-token",
		TencentTicket:  "ticket",
		TencentRandstr: "@rand",
	}, "203.0.113.10")

	require.ErrorIs(t, err, identity.ErrCaptchaProviderConflict)
	require.Zero(t, turnstileVerifier.called)
	require.Zero(t, tencentVerifier.calls)
}

func TestVerifyCaptchaRequiredModeAcceptsCompleteTencentProvider(t *testing.T) {
	verifier := &tencentCaptchaVerifierStub{response: &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := newAuthServiceForCaptchaTest(tencentCaptchaSettings(), true, nil, verifier)

	err := svc.VerifyCaptcha(context.Background(), identity.CaptchaProof{
		TencentTicket:  "ticket",
		TencentRandstr: "@rand",
	}, "203.0.113.10")

	require.NoError(t, err)
}

func TestVerifyCaptchaForRegisterSkipsDuplicateTencentTicketAfterEmailCode(t *testing.T) {
	settings := tencentCaptchaSettings()
	settings[identity.SettingKeyEmailVerifyEnabled] = "true"
	verifier := &tencentCaptchaVerifierStub{response: &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := newAuthServiceForCaptchaTest(settings, true, nil, verifier)

	err := svc.VerifyCaptchaForRegister(context.Background(), identity.CaptchaProof{}, "203.0.113.10", "123456")

	require.NoError(t, err)
	require.Zero(t, verifier.calls)
}

func TestVerifyCaptchaFailsClosedWhenProviderSettingsCannotBeRead(t *testing.T) {
	repo := &captchaSettingsStore{err: errors.New("settings unavailable")}
	svc := newAuthServiceForCaptchaRepoTest(repo, false, &turnstileVerifierSpy{}, &tencentCaptchaVerifierStub{})

	err := svc.VerifyCaptcha(context.Background(), identity.CaptchaProof{}, "203.0.113.10")

	require.ErrorIs(t, err, identity.ErrServiceUnavailable)
}

func TestVerifyCaptchaReadsProviderConfigurationOnce(t *testing.T) {
	repo := &captchaSettingsStore{values: tencentCaptchaSettings()}
	verifier := &tencentCaptchaVerifierStub{response: &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := newAuthServiceForCaptchaRepoTest(repo, false, &turnstileVerifierSpy{}, verifier)

	err := svc.VerifyCaptcha(context.Background(), identity.CaptchaProof{
		TencentTicket:  "ticket",
		TencentRandstr: "@rand",
	}, "203.0.113.10")

	require.NoError(t, err)
	require.Equal(t, 1, repo.getMultipleCalls)
	require.Zero(t, repo.getValueCalls)
	require.Equal(t, 1, verifier.calls)
}

func TestVerifyCaptchaRejectsEnabledTencentProviderWithIncompleteCredentials(t *testing.T) {
	repo := &captchaSettingsStore{values: map[string]string{
		identity.SettingKeyTencentCaptchaEnabled: "true",
		identity.SettingKeyTencentCaptchaAppID:   "123456789",
	}}
	verifier := &tencentCaptchaVerifierStub{response: &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := newAuthServiceForCaptchaRepoTest(repo, false, &turnstileVerifierSpy{}, verifier)

	err := svc.VerifyCaptcha(context.Background(), identity.CaptchaProof{
		TencentTicket:  "ticket",
		TencentRandstr: "@rand",
	}, "203.0.113.10")

	require.ErrorIs(t, err, identity.ErrTencentCaptchaNotConfigured)
	require.Equal(t, 1, repo.getMultipleCalls)
	require.Zero(t, verifier.calls)
}

func TestVerifyActionCaptchaIfEnabledVerifiesTencentProof(t *testing.T) {
	verifier := &tencentCaptchaVerifierStub{response: &identity.TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := newAuthServiceForCaptchaTest(tencentCaptchaSettings(), false, nil, verifier)

	err := svc.VerifyActionCaptchaIfEnabled(context.Background(), identity.CaptchaProof{
		TencentTicket:  "ticket",
		TencentRandstr: "@rand",
	}, "203.0.113.10")

	require.NoError(t, err)
	require.Equal(t, 1, verifier.calls)
	require.Equal(t, identity.TencentCaptchaProof{Ticket: "ticket", Randstr: "@rand"}, verifier.proof)
}

func TestVerifyActionCaptchaIfEnabledDoesNotExpandTurnstileCoverage(t *testing.T) {
	settings := map[string]string{
		identity.SettingKeyTurnstileEnabled:   "true",
		identity.SettingKeyTurnstileSecretKey: "turnstile-secret",
	}
	turnstileVerifier := &turnstileVerifierSpy{}
	svc := newAuthServiceForCaptchaTest(settings, false, turnstileVerifier, nil)

	err := svc.VerifyActionCaptchaIfEnabled(context.Background(), identity.CaptchaProof{}, "203.0.113.10")

	require.NoError(t, err)
	require.Zero(t, turnstileVerifier.called)
}

func TestVerifyActionCaptchaIfEnabledFailsClosedOnSettingReadError(t *testing.T) {
	repo := &captchaSettingsStore{err: errors.New("settings unavailable")}
	svc := newAuthServiceForCaptchaRepoTest(repo, false, &turnstileVerifierSpy{}, &tencentCaptchaVerifierStub{})

	err := svc.VerifyActionCaptchaIfEnabled(context.Background(), identity.CaptchaProof{}, "203.0.113.10")

	require.ErrorIs(t, err, identity.ErrServiceUnavailable)
}

func TestAuthService_Register_Disabled(t *testing.T) {
	repo := &userRepoStub{}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled: "false",
	}, nil)

	_, _, err := service.RegisterWithVerification(context.Background(), "user@test.com", "password", "", "", "", "")
	require.ErrorIs(t, err, identity.ErrRegDisabled)
}

func TestAuthService_Register_DisabledByDefault(t *testing.T) {
	// Missing registration settings disable signup.
	repo := &userRepoStub{}
	service := newAuthService(repo, nil, nil)

	_, _, err := service.RegisterWithVerification(context.Background(), "user@test.com", "password", "", "", "", "")
	require.ErrorIs(t, err, identity.ErrRegDisabled)
}

func TestAuthService_Register_EmailVerifyEnabledButServiceNotConfigured(t *testing.T) {
	repo := &userRepoStub{}
	// Email verification is required but the email service is unavailable.
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled: "true",
		identity.SettingKeyEmailVerifyEnabled:  "true",
	}, nil)

	_, _, err := service.RegisterWithVerification(context.Background(), "user@test.com", "password", "any-code", "", "", "")
	require.ErrorIs(t, err, identity.ErrServiceUnavailable)
}

func TestAuthService_Register_EmailVerifyRequired(t *testing.T) {
	repo := &userRepoStub{}
	cache := &emailCacheStub{} // Configures the email service.
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled: "true",
		identity.SettingKeyEmailVerifyEnabled:  "true",
	}, cache)

	_, _, err := service.RegisterWithVerification(context.Background(), "user@test.com", "password", "", "", "", "")
	require.ErrorIs(t, err, identity.ErrEmailVerifyRequired)
}

func TestAuthService_Register_EmailVerifyInvalid(t *testing.T) {
	repo := &userRepoStub{}
	cache := &emailCacheStub{
		data: &identity.VerificationCodeData{Code: "expected", Attempts: 0},
	}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled: "true",
		identity.SettingKeyEmailVerifyEnabled:  "true",
	}, cache)

	_, _, err := service.RegisterWithVerification(context.Background(), "user@test.com", "password", "wrong", "", "", "")
	require.ErrorIs(t, err, identity.ErrInvalidVerifyCode)
	require.ErrorContains(t, err, "verify code")
}

func TestAuthService_Register_EmailExists(t *testing.T) {
	repo := &userRepoStub{exists: true}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled: "true",
	}, nil)

	_, _, err := service.RegisterWithVerification(context.Background(), "user@test.com", "password", "", "", "", "")
	require.ErrorIs(t, err, identity.ErrEmailExists)
}

func TestAuthService_Register_CheckEmailError(t *testing.T) {
	repo := &userRepoStub{existsErr: errors.New("db down")}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled: "true",
	}, nil)

	_, _, err := service.RegisterWithVerification(context.Background(), "user@test.com", "password", "", "", "", "")
	require.ErrorIs(t, err, identity.ErrServiceUnavailable)
}

func TestAuthService_Register_ReservedEmail(t *testing.T) {
	repo := &userRepoStub{}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled: "true",
	}, nil)

	_, _, err := service.RegisterWithVerification(context.Background(), "linuxdo-123@linuxdo-connect.invalid", "password", "", "", "", "")
	require.ErrorIs(t, err, identity.ErrEmailReserved)
}

func TestAuthService_Register_EmailDomainRegistrationLimit(t *testing.T) {
	repo := &userRepoStub{domainCounts: map[string]int{"other.com": 1}}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled:                 "true",
		identity.SettingKeyRegistrationEmailSuffixWhitelist:    `["@example.com","@company.com"]`,
		identity.SettingKeyRegistrationEmailDomainQuotaEnabled: "true",
	}, nil)

	_, _, err := service.RegisterWithVerification(context.Background(), "user@other.com", "password", "", "", "", "")
	require.ErrorIs(t, err, identity.ErrEmailDomainRegistrationLimit)
	appErr := apperror.FromError(err)
	require.Equal(t, "EMAIL_DOMAIN_REGISTRATION_LIMIT", appErr.Reason)
}

func TestAuthService_Register_NonWhitelistDomainAllowsFirstAccount(t *testing.T) {
	repo := &userRepoStub{nextID: 9, domainCounts: map[string]int{"custom.example": 0}}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled:                 "true",
		identity.SettingKeyRegistrationEmailSuffixWhitelist:    `["@example.com"]`,
		identity.SettingKeyRegistrationEmailDomainQuotaEnabled: "true",
	}, nil)

	_, user, err := service.RegisterWithVerification(context.Background(), "first@sub.custom.example", "password", "", "", "", "")
	require.NoError(t, err)
	require.Equal(t, int64(9), user.ID)
	require.Equal(t, []string{"custom.example"}, repo.domainGuardCalls)
}

func TestAuthService_Register_NonWhitelistDomainRejectedWhenQuotaDisabledByDefault(t *testing.T) {
	repo := &userRepoStub{nextID: 9, domainCounts: map[string]int{"custom.example": 0}}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled:              "true",
		identity.SettingKeyRegistrationEmailSuffixWhitelist: `["@example.com"]`,
	}, nil)

	_, _, err := service.RegisterWithVerification(context.Background(), "first@custom.example", "password", "", "", "", "")
	require.ErrorIs(t, err, identity.ErrEmailSuffixNotAllowed)
	require.Equal(t, "EMAIL_SUFFIX_NOT_ALLOWED", apperror.FromError(err).Reason)
	require.Empty(t, repo.created)
	require.Empty(t, repo.domainGuardCalls)
}

func TestAuthService_Register_NonWhitelistDomainRejectedWhenQuotaExplicitlyDisabled(t *testing.T) {
	repo := &userRepoStub{domainCounts: map[string]int{"custom.example": 0}}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled:                 "true",
		identity.SettingKeyRegistrationEmailSuffixWhitelist:    `["@example.com"]`,
		identity.SettingKeyRegistrationEmailDomainQuotaEnabled: "false",
	}, nil)

	_, _, err := service.RegisterWithVerification(context.Background(), "first@custom.example", "password", "", "", "", "")
	require.ErrorIs(t, err, identity.ErrEmailSuffixNotAllowed)
}

func TestAuthService_Register_EmailSuffixAllowed(t *testing.T) {
	repo := &userRepoStub{nextID: 8}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled:              "true",
		identity.SettingKeyRegistrationEmailSuffixWhitelist: `["example.com"]`,
	}, nil)

	_, user, err := service.RegisterWithVerification(context.Background(), "user@example.com", "password", "", "", "", "")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, int64(8), user.ID)
}

func TestAuthService_SendVerifyCode_EmailDomainRegistrationLimit(t *testing.T) {
	repo := &userRepoStub{domainCounts: map[string]int{"other.com": 1}}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled:                 "true",
		identity.SettingKeyRegistrationEmailSuffixWhitelist:    `["@example.com","@company.com"]`,
		identity.SettingKeyRegistrationEmailDomainQuotaEnabled: "true",
	}, nil)

	err := service.SendVerifyCode(context.Background(), "user@other.com")
	require.ErrorIs(t, err, identity.ErrEmailDomainRegistrationLimit)
	appErr := apperror.FromError(err)
	require.Equal(t, "EMAIL_DOMAIN_REGISTRATION_LIMIT", appErr.Reason)
}

func TestAuthService_SendVerifyCode_NonWhitelistDomainRejectedWhenQuotaDisabled(t *testing.T) {
	repo := &userRepoStub{domainCounts: map[string]int{"custom.example": 0}}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled:              "true",
		identity.SettingKeyRegistrationEmailSuffixWhitelist: `["@example.com"]`,
	}, nil)

	err := service.SendVerifyCode(context.Background(), "user@custom.example")
	require.ErrorIs(t, err, identity.ErrEmailSuffixNotAllowed)
}

func TestAuthService_SendVerifyCodeAsync_NonWhitelistDomainRejectedWhenQuotaDisabled(t *testing.T) {
	repo := &userRepoStub{domainCounts: map[string]int{"custom.example": 0}}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled:              "true",
		identity.SettingKeyRegistrationEmailSuffixWhitelist: `["@example.com"]`,
	}, nil)

	_, err := service.SendVerifyCodeAsync(context.Background(), "user@custom.example")
	require.ErrorIs(t, err, identity.ErrEmailSuffixNotAllowed)
}

func TestAuthService_Register_CreateError(t *testing.T) {
	repo := &userRepoStub{createErr: errors.New("create failed")}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled: "true",
	}, nil)

	_, _, err := service.RegisterWithVerification(context.Background(), "user@test.com", "password", "", "", "", "")
	require.ErrorIs(t, err, identity.ErrServiceUnavailable)
}

func TestAuthService_Register_CreateEmailExistsRace(t *testing.T) {
	// The preflight lookup succeeds, but the insert loses a uniqueness race.
	repo := &userRepoStub{createErr: identity.ErrEmailExists}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled: "true",
	}, nil)

	_, _, err := service.RegisterWithVerification(context.Background(), "user@test.com", "password", "", "", "", "")
	require.ErrorIs(t, err, identity.ErrEmailExists)
}

func TestAuthService_Register_Success(t *testing.T) {
	repo := &userRepoStub{nextID: 5}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled:                 "true",
		identity.SettingKeyAuthSourceDefaultEmailGrantOnSignup: "false",
	}, nil)

	token, user, err := service.RegisterWithVerification(context.Background(), "user@test.com", "password", "", "", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.NotNil(t, user)
	require.Equal(t, int64(5), user.ID)
	require.Equal(t, "user@test.com", user.Email)
	require.Equal(t, identity.RoleUser, user.Role)
	require.Equal(t, billing.StatusActive, user.Status)
	require.Equal(t, 3.5, user.Balance)
	require.Equal(t, 2, user.Concurrency)
	require.Len(t, repo.created, 1)
	require.True(t, user.CheckPassword("password"))
}

func TestAuthService_Register_AssignsDefaultSubscriptions(t *testing.T) {
	repo := &userRepoStub{nextID: 42}
	assigner := &defaultSubscriptionAssignerStub{}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled:                 "true",
		billing.SettingKeyDefaultSubscriptions:                 `[{"plan_id":11},{"plan_id":12}]`,
		identity.SettingKeyAuthSourceDefaultEmailGrantOnSignup: "false",
	}, nil)
	service.DefaultSubscriptions = assigner

	_, user, err := service.RegisterWithVerification(context.Background(), "default-sub@test.com", "password", "", "", "", "")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.Len(t, assigner.calls, 2)
	require.Equal(t, int64(42), assigner.calls[0].UserID)
	require.Equal(t, int64(11), assigner.calls[0].PlanID)
	require.Equal(t, int64(12), assigner.calls[1].PlanID)
}

func TestAuthService_Register_UsesEmailAuthSourceDefaultsWhenGrantEnabled(t *testing.T) {
	repo := &userRepoStub{nextID: 52}
	assigner := &defaultSubscriptionAssignerStub{}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled:                 "true",
		billing.SettingKeyDefaultSubscriptions:                 `[{"plan_id":91}]`,
		identity.SettingKeyAuthSourceDefaultEmailBalance:       "12.5",
		identity.SettingKeyAuthSourceDefaultEmailConcurrency:   "7",
		identity.SettingKeyAuthSourceDefaultEmailSubscriptions: `[{"plan_id":11}]`,
		identity.SettingKeyAuthSourceDefaultEmailGrantOnSignup: "true",
	}, nil)
	service.DefaultSubscriptions = assigner

	_, user, err := service.RegisterWithVerification(context.Background(), "email-defaults@test.com", "password", "", "", "", "")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, 12.5, user.Balance)
	require.Equal(t, 7, user.Concurrency)
	require.Len(t, assigner.calls, 1)
	require.Equal(t, int64(11), assigner.calls[0].PlanID)
}

func TestAuthService_Register_GrantOnSignupFalseFallsBackToGlobalDefaults(t *testing.T) {
	repo := &userRepoStub{nextID: 53}
	assigner := &defaultSubscriptionAssignerStub{}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled:                 "true",
		billing.SettingKeyDefaultSubscriptions:                 `[{"plan_id":31}]`,
		identity.SettingKeyAuthSourceDefaultEmailBalance:       "99",
		identity.SettingKeyAuthSourceDefaultEmailConcurrency:   "88",
		identity.SettingKeyAuthSourceDefaultEmailSubscriptions: `[{"plan_id":32}]`,
		identity.SettingKeyAuthSourceDefaultEmailGrantOnSignup: "false",
	}, nil)
	service.DefaultSubscriptions = assigner

	_, user, err := service.RegisterWithVerification(context.Background(), "email-global@test.com", "password", "", "", "", "")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, 3.5, user.Balance)
	require.Equal(t, 2, user.Concurrency)
	require.Len(t, assigner.calls, 1)
	require.Equal(t, int64(31), assigner.calls[0].PlanID)
}

func TestAuthService_Register_GrantOnSignupMergesSourceOverridesWithGlobalDefaults(t *testing.T) {
	repo := &userRepoStub{nextID: 54}
	assigner := &defaultSubscriptionAssignerStub{}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled:                 "true",
		billing.SettingKeyDefaultSubscriptions:                 `[{"plan_id":31}]`,
		identity.SettingKeyAuthSourceDefaultEmailBalance:       "9.5",
		identity.SettingKeyAuthSourceDefaultEmailConcurrency:   "5",
		identity.SettingKeyAuthSourceDefaultEmailSubscriptions: `[]`,
		identity.SettingKeyAuthSourceDefaultEmailGrantOnSignup: "true",
	}, nil)
	service.DefaultSubscriptions = assigner

	_, user, err := service.RegisterWithVerification(context.Background(), "email-merged@test.com", "password", "", "", "", "")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, 9.5, user.Balance)
	require.Equal(t, 5, user.Concurrency)
	require.Len(t, assigner.calls, 1)
	require.Equal(t, int64(31), assigner.calls[0].PlanID)
}

func TestAuthService_LoginOrRegisterOAuthWithTokenPair_UsesLinuxDoAuthSourceDefaultsOnSignup(t *testing.T) {
	repo := &userRepoStub{nextID: 61}
	assigner := &defaultSubscriptionAssignerStub{}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled:                   "true",
		billing.SettingKeyDefaultSubscriptions:                   `[{"plan_id":81}]`,
		identity.SettingKeyAuthSourceDefaultLinuxDoBalance:       "21.75",
		identity.SettingKeyAuthSourceDefaultLinuxDoConcurrency:   "9",
		identity.SettingKeyAuthSourceDefaultLinuxDoSubscriptions: `[{"plan_id":22}]`,
		identity.SettingKeyAuthSourceDefaultLinuxDoGrantOnSignup: "true",
	}, nil)
	service.DefaultSubscriptions = assigner
	service.RefreshTokens = &refreshTokenCacheStub{}
	rebuildSessionForTest(service)

	tokenPair, user, err := service.LoginOrRegisterOAuthWithTokenPair(context.Background(), "linuxdo-123@linuxdo-connect.invalid", "linuxdo_user", "", "", "linuxdo")
	require.NoError(t, err)
	require.NotNil(t, tokenPair)
	require.NotNil(t, user)
	require.Equal(t, int64(61), user.ID)
	require.Equal(t, 21.75, user.Balance)
	require.Equal(t, 9, user.Concurrency)
	require.Len(t, repo.created, 1)
	require.Len(t, assigner.calls, 1)
	require.Equal(t, int64(22), assigner.calls[0].PlanID)
}

func TestAuthService_LoginOrRegisterOAuthWithTokenPair_ExistingUserDoesNotGrantAgain(t *testing.T) {
	existing := &identity.User{
		ID:           88,
		Email:        "linuxdo-123@linuxdo-connect.invalid",
		Username:     "existing-linuxdo",
		Role:         identity.RoleUser,
		Status:       billing.StatusActive,
		Balance:      4,
		Concurrency:  1,
		TokenVersion: 2,
	}
	repo := &userRepoStub{user: existing}
	assigner := &defaultSubscriptionAssignerStub{}
	service := newAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled:                   "true",
		identity.SettingKeyAuthSourceDefaultLinuxDoBalance:       "21.75",
		identity.SettingKeyAuthSourceDefaultLinuxDoConcurrency:   "9",
		identity.SettingKeyAuthSourceDefaultLinuxDoSubscriptions: `[{"plan_id":22}]`,
		identity.SettingKeyAuthSourceDefaultLinuxDoGrantOnSignup: "true",
	}, nil)
	service.DefaultSubscriptions = assigner
	service.RefreshTokens = &refreshTokenCacheStub{}
	rebuildSessionForTest(service)

	tokenPair, user, err := service.LoginOrRegisterOAuthWithTokenPair(context.Background(), existing.Email, "linuxdo_user", "", "", "linuxdo")
	require.NoError(t, err)
	require.NotNil(t, tokenPair)
	require.Equal(t, existing.ID, user.ID)
	require.Equal(t, 4.0, user.Balance)
	require.Equal(t, 1, user.Concurrency)
	require.Empty(t, repo.created)
	require.Empty(t, assigner.calls)
}

// newAuthServiceWithDingTalkCfg configures DingTalk options for tests of
// AuthCanBypassRegistrationDisabledForOAuth.
func newAuthServiceWithDingTalkCfg(settings map[string]string, dtCfg config.DingTalkConnectConfig) *identity.AuthService {
	cfg := &config.Config{
		JWT:      config.JWTConfig{Secret: "test-secret", ExpireHour: 1},
		Default:  config.DefaultConfig{UserBalance: 3.5, UserConcurrency: 2},
		DingTalk: dtCfg,
	}
	settingService := newAuthSettingsFixture(&settingRepoStub{values: settings}, cfg)
	return identitytestkit.Auth(nil, &identity.AuthDependencies{Options: identitytestkit.AuthOptions(cfg), Settings: authSettingsPort(settingService)})
}

// minDingTalkURLs returns the required DingTalk configuration fields.
func minDingTalkURLs() config.DingTalkConnectConfig {
	return config.DingTalkConnectConfig{
		ClientID:            "test-client",
		ClientSecret:        "test-secret",
		AuthorizeURL:        "https://example.com/oauth2/auth",
		TokenURL:            "https://example.com/oauth2/token",
		UserInfoURL:         "https://example.com/oauth2/userinfo",
		RedirectURL:         "https://example.com/callback",
		FrontendRedirectURL: "https://example.com/auth/callback",
		DingTalkAppKind:     "internal_app",
		AppType:             "internal",
	}
}

func TestCanBypassRegistrationDisabledForOAuth(t *testing.T) {
	cases := []struct {
		name         string
		signupSource string
		settings     map[string]string
		dtCfg        config.DingTalkConnectConfig
		want         bool
	}{
		{
			name:         "non-dingtalk source → false",
			signupSource: "linuxdo",
			settings:     map[string]string{},
			dtCfg:        minDingTalkURLs(),
			want:         false,
		},
		{
			name:         "dingtalk but cfg.Enabled=false → false",
			signupSource: "dingtalk",
			settings: map[string]string{
				identity.SettingKeyDingTalkConnectEnabled:               "false",
				identity.SettingKeyDingTalkConnectBypassRegistration:    "true",
				identity.SettingKeyDingTalkConnectCorpRestrictionPolicy: "internal_only",
			},
			dtCfg: minDingTalkURLs(),
			want:  false,
		},
		{
			name:         "dingtalk enabled but BypassRegistration=false → false",
			signupSource: "dingtalk",
			settings: map[string]string{
				identity.SettingKeyDingTalkConnectEnabled:               "true",
				identity.SettingKeyDingTalkConnectBypassRegistration:    "false",
				identity.SettingKeyDingTalkConnectCorpRestrictionPolicy: "internal_only",
			},
			dtCfg: minDingTalkURLs(),
			want:  false,
		},
		{
			name:         "dingtalk enabled + bypass=true but policy=none → false",
			signupSource: "dingtalk",
			settings: map[string]string{
				identity.SettingKeyDingTalkConnectEnabled:               "true",
				identity.SettingKeyDingTalkConnectBypassRegistration:    "true",
				identity.SettingKeyDingTalkConnectCorpRestrictionPolicy: "none",
			},
			dtCfg: minDingTalkURLs(),
			want:  false,
		},
		{
			name:         "dingtalk enabled + bypass=true + policy=internal_only → true",
			signupSource: "dingtalk",
			settings: map[string]string{
				identity.SettingKeyDingTalkConnectEnabled:               "true",
				identity.SettingKeyDingTalkConnectBypassRegistration:    "true",
				identity.SettingKeyDingTalkConnectCorpRestrictionPolicy: "internal_only",
			},
			dtCfg: minDingTalkURLs(),
			want:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newAuthServiceWithDingTalkCfg(tc.settings, tc.dtCfg)
			got := svc.AuthCanBypassRegistrationDisabledForOAuth(context.Background(), tc.signupSource)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestAuthService_VerifyTurnstileForRegister_SkipWhenEmailVerifyCodeProvided(t *testing.T) {
	verifier := &turnstileVerifierSpy{}
	service := newAuthServiceForRegisterTurnstileTest(map[string]string{
		identity.SettingKeyEmailVerifyEnabled:  "true",
		identity.SettingKeyTurnstileEnabled:    "true",
		identity.SettingKeyTurnstileSecretKey:  "secret",
		identity.SettingKeyRegistrationEnabled: "true",
	}, verifier)

	err := service.VerifyCaptchaForRegister(context.Background(), identity.CaptchaProof{TurnstileToken: ""}, "127.0.0.1", "123456")
	require.NoError(t, err)
	require.Equal(t, 0, verifier.called)
}

func TestAuthService_VerifyTurnstileForRegister_RequireWhenVerifyCodeMissing(t *testing.T) {
	verifier := &turnstileVerifierSpy{}
	service := newAuthServiceForRegisterTurnstileTest(map[string]string{
		identity.SettingKeyEmailVerifyEnabled: "true",
		identity.SettingKeyTurnstileEnabled:   "true",
		identity.SettingKeyTurnstileSecretKey: "secret",
	}, verifier)

	err := service.VerifyCaptchaForRegister(context.Background(), identity.CaptchaProof{TurnstileToken: ""}, "127.0.0.1", "")
	require.ErrorIs(t, err, identity.ErrTurnstileVerificationFailed)
}

func TestAuthService_VerifyTurnstileForRegister_NoSkipWhenEmailVerifyDisabled(t *testing.T) {
	verifier := &turnstileVerifierSpy{}
	service := newAuthServiceForRegisterTurnstileTest(map[string]string{
		identity.SettingKeyEmailVerifyEnabled: "false",
		identity.SettingKeyTurnstileEnabled:   "true",
		identity.SettingKeyTurnstileSecretKey: "secret",
	}, verifier)

	err := service.VerifyCaptchaForRegister(context.Background(), identity.CaptchaProof{TurnstileToken: "turnstile-token"}, "127.0.0.1", "123456")
	require.NoError(t, err)
	require.Equal(t, 1, verifier.called)
	require.Equal(t, "turnstile-token", verifier.lastToken)
}

func newAuthServiceForCaptchaRepoTest(repo *captchaSettingsStore, required bool, turnstileVerifier identity.TurnstileVerifier, tencentVerifier identity.TencentCaptchaVerifier) *identity.AuthService {
	runtime := identity.NewRuntimeSettings(repo, settingscore.ErrSettingNotFound)
	turnstile := identity.NewTurnstileService(runtime, turnstileVerifier)
	turnstile.SetObserver(logging.LegacyPrintf)
	tencent := identity.NewTencentCaptchaService(runtime, tencentVerifier)
	tencent.SetObserver(logging.LegacyPrintf)
	return identity.NewAuthService(&identity.AuthDependencies{Options: captchaAuthOptions(required), Settings: captchaAuthSettings{runtime: runtime}, Turnstile: turnstile, Tencent: tencent, Observer: identity.Observer{Log: logging.LegacyPrintf}}, nil)
}

func newAuthServiceForCaptchaTest(values map[string]string, required bool, turnstileVerifier identity.TurnstileVerifier, tencentVerifier identity.TencentCaptchaVerifier) *identity.AuthService {
	svc := newAuthServiceForCaptchaRepoTest(&captchaSettingsStore{values: values}, required, turnstileVerifier, tencentVerifier)
	if turnstileVerifier == nil {
		svc.Turnstile = nil
	}
	if tencentVerifier == nil {
		svc.Tencent = nil
	}
	return svc
}

func newAuthServiceForRegisterTurnstileTest(values map[string]string, verifier identity.TurnstileVerifier) *identity.AuthService {
	return newAuthServiceForCaptchaTest(values, true, verifier, nil)
}

func newEmailNormalizationAuthService(repo identity.UserRepository, settings map[string]string) *identity.AuthService {
	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret:     "test-secret",
			ExpireHour: 1,
		},
		Default: config.DefaultConfig{
			UserBalance:     3.5,
			UserConcurrency: 2,
		},
	}

	return identitytestkit.Auth(
		nil, &identity.AuthDependencies{Users: repo, Options: identitytestkit.AuthOptions(cfg), Settings: authSettingsPort(newAuthSettingsFixture(&settingRepoStub{values: settings}, cfg))},
	)
}

func TestAuthService_Register_UsesNormalizedEmailLookupWhenEnabled(t *testing.T) {
	repo := &emailNormalizationRepoStub{existsByNormalized: true}
	svc := newEmailNormalizationAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled:            "true",
		identity.SettingKeyRegistrationEmailNormalization: "true",
	})

	_, _, err := svc.RegisterWithVerification(context.Background(), "Y.o.u.r.N.a.m.e+abc@googlemail.com.", "password", "", "", "", "")
	require.ErrorIs(t, err, identity.ErrEmailExists)
	require.Equal(t, []string{"Y.o.u.r.N.a.m.e+abc@googlemail.com."}, repo.existsByEmailCalls)
	require.Equal(t, []string{"yourname@gmail.com"}, repo.existsByNormalizedCalls)
	require.Empty(t, repo.createCalls)
}

func TestAuthService_Register_SkipsNormalizedLookupWhenDisabled(t *testing.T) {
	repo := &emailNormalizationRepoStub{}
	svc := newEmailNormalizationAuthService(repo, map[string]string{
		identity.SettingKeyRegistrationEnabled: "true",
	})

	_, user, err := svc.RegisterWithVerification(context.Background(), "Y.o.u.r.N.a.m.e+abc@example.com", "password", "", "", "", "")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, []string{"Y.o.u.r.N.a.m.e+abc@example.com"}, repo.existsByEmailCalls)
	require.Empty(t, repo.existsByNormalizedCalls)
}
