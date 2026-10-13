package identity_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
)

// redeemCodeRepoStub models invitation reads, claim mutation and restoration
// observations for sequential email-flow tests.
type redeemCodeRepoStub struct {
	// codesByCode retains fixture invitation pointers under exact code keys.
	// Reads copy the outer record while Use and recorded updates can mutate
	// stored state.
	codesByCode map[string]*billing.RedeemCode
	// useCalls records claim arguments after the matching invitation is marked
	// used. This sequential log models delegation without conditional database
	// admission or locking.
	useCalls []struct {
		id     int64 // claimed invitation record identifier
		userID int64 // claiming fixture account identifier
	}
	// updateCalls retains the same shallow copies stored by Update. Their nested
	// pointers and later stored-record mutation remain visible through this call
	// log.
	updateCalls []*billing.RedeemCode
}

// Create panics when email-flow scenarios unexpectedly request new billing
// codes.
func (s *redeemCodeRepoStub) Create(context.Context, *billing.RedeemCode) error {
	panic("unexpected Create call")
}

// CreateBatch panics because these invitation scenarios do not create code
// batches.
func (s *redeemCodeRepoStub) CreateBatch(context.Context, []billing.RedeemCode) error {
	panic("unexpected CreateBatch call")
}

// GetByID panics because email completion resolves invitations by code text.
func (s *redeemCodeRepoStub) GetByID(context.Context, int64) (*billing.RedeemCode, error) {
	panic("unexpected GetByID call")
}

// GetByIDForUpdate panics because these scenarios use code-based invitation
// reads.
func (s *redeemCodeRepoStub) GetByIDForUpdate(context.Context, int64) (*billing.RedeemCode, error) {
	panic("unexpected GetByIDForUpdate call")
}

// GetByCode returns a shallow invitation copy for an exact code key. Missing
// map/key returns ErrRedeemCodeNotFound; fixtures provide nonnil records and
// nested timestamp/owner pointers remain shared with stored state.
func (s *redeemCodeRepoStub) GetByCode(_ context.Context, code string) (*billing.RedeemCode, error) {
	if s.codesByCode == nil {
		return nil, billing.ErrRedeemCodeNotFound
	}
	redeemCode, ok := s.codesByCode[code]
	if !ok {
		return nil, billing.ErrRedeemCodeNotFound
	}
	cloned := *redeemCode
	return &cloned, nil
}

// GetByCodeForUpdate delegates the exact-code shallow fixture read without
// locking. This stub does not reserve invitation usage, so callers use it for
// sequential email-flow assertions rather than concurrent conditional-claim
// guarantees.
func (s *redeemCodeRepoStub) GetByCodeForUpdate(ctx context.Context, code string) (*billing.RedeemCode, error) {
	return s.GetByCode(ctx, code)
}

// Update records a shallow copy and stores that same copy under its Code. Nil
// input succeeds without a call record; copied pointer fields stay shared,
// and later fixture mutation can change the recorded object.
func (s *redeemCodeRepoStub) Update(_ context.Context, code *billing.RedeemCode) error {
	if code == nil {
		return nil
	}
	cloned := *code
	s.updateCalls = append(s.updateCalls, &cloned)
	if s.codesByCode == nil {
		s.codesByCode = make(map[string]*billing.RedeemCode)
	}
	s.codesByCode[cloned.Code] = &cloned
	return nil
}

// BatchUpdate panics because these scenarios restore one invitation record at
// a time.
func (s *redeemCodeRepoStub) BatchUpdate(context.Context, []int64, billing.RedeemCodeBatchUpdateFields) (int64, error) {
	panic("unexpected BatchUpdate call")
}

// Delete panics because account compensation restores invitations rather than
// deleting them.
func (s *redeemCodeRepoStub) Delete(context.Context, int64) error {
	panic("unexpected Delete call")
}

// Use mutates the first fixture invitation with the supplied ID and records
// its claiming account. It sets used status/count and wall-clock use time
// without checking prior status or expiry; missing IDs return
// ErrRedeemCodeNotFound.
func (s *redeemCodeRepoStub) Use(_ context.Context, id, userID int64) error {
	for code, redeemCode := range s.codesByCode {
		if redeemCode.ID != id {
			continue
		}
		now := time.Now().UTC()
		redeemCode.Status = billing.StatusUsed
		redeemCode.UsedCount = 1
		redeemCode.UsedBy = &userID
		redeemCode.UsedAt = &now
		s.codesByCode[code] = redeemCode
		s.useCalls = append(s.useCalls, struct {
			id     int64 // claimed invitation record identifier
			userID int64 // claiming fixture account identifier
		}{id: id, userID: userID})
		return nil
	}
	return billing.ErrRedeemCodeNotFound
}

// List panics because email completion does not enumerate invitation records.
func (s *redeemCodeRepoStub) List(context.Context, pagination.PaginationParams) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

// ListWithFilters panics because these scenarios address invitations through
// exact codes.
func (s *redeemCodeRepoStub) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

// ListByUser panics because invitation admission does not enumerate user
// history.
func (s *redeemCodeRepoStub) ListByUser(context.Context, int64, int) ([]billing.RedeemCode, error) {
	panic("unexpected ListByUser call")
}

// ListByUserPaginated panics because pending signup does not query paginated
// invitation history.
func (s *redeemCodeRepoStub) ListByUserPaginated(context.Context, int64, pagination.PaginationParams, string) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected ListByUserPaginated call")
}

// SumPositiveBalanceByUser panics because these scenarios exercise invitation
// state, not recharge totals.
func (s *redeemCodeRepoStub) SumPositiveBalanceByUser(context.Context, int64) (float64, error) {
	panic("unexpected SumPositiveBalanceByUser call")
}

// CreateUsage panics because this fixture records invitation claims through
// Use.
func (s *redeemCodeRepoStub) CreateUsage(context.Context, *billing.RedeemCodeUsage) error {
	panic("unexpected CreateUsage call")
}

// GetUsageByRedeemCodeAndUser returns no recorded usage for any code/account
// pair. The stub supplies nil without error and does not track independent
// redemption history, leaving these scenarios focused on invitation state.
func (s *redeemCodeRepoStub) GetUsageByRedeemCodeAndUser(context.Context, int64, int64) (*billing.RedeemCodeUsage, error) {
	return nil, nil
}

// TestRegisterOAuthEmailAccountRollsBackCreatedUserWhenTokenPairGenerationFails
// omits refresh storage after valid email/invitation checks. It observes a
// created account ID followed by delete delegation and verifies that
// invitation use/restoration were not attempted.
func TestRegisterOAuthEmailAccountRollsBackCreatedUserWhenTokenPairGenerationFails(t *testing.T) {
	userRepo := &userRepoStub{nextID: 42}
	redeemRepo := &redeemCodeRepoStub{
		codesByCode: map[string]*billing.RedeemCode{
			"INVITE123": {
				ID:     7,
				Code:   "INVITE123",
				Type:   billing.RedeemTypeInvitation,
				Status: billing.StatusUnused,
			},
		},
	}
	emailCache := &emailCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "246810",
			Attempts:  0,
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
		},
	}
	// Leave refresh storage unavailable to force token-pair failure after
	// creation assigns an ID; the test then observes delete delegation without
	// invitation consumption.
	authService := newOAuthEmailFlowAuthService(
		userRepo,
		redeemRepo,
		nil,
		map[string]string{
			identity.SettingKeyRegistrationEnabled:    "true",
			promotion.SettingKeyInvitationCodeEnabled: "true",
			identity.SettingKeyEmailVerifyEnabled:     "true",
		},
		emailCache,
	)

	tokenPair, user, err := authService.RegisterOAuthEmailAccount(
		context.Background(),
		"fresh@example.com",
		"secret-123",
		"246810",
		"INVITE123",
		"oidc",
	)

	require.Nil(t, tokenPair)
	require.Nil(t, user)
	require.Error(t, err)
	require.Contains(t, err.Error(), "generate token pair")
	require.Equal(t, []int64{42}, userRepo.deletedIDs)
	require.Len(t, userRepo.created, 1)
	require.Empty(t, redeemRepo.useCalls)
	require.Empty(t, redeemRepo.updateCalls)
}

// TestRegisterOAuthEmailAccountRejectsExhaustedNonWhitelistDomain submits a
// subdomain of an occupied custom registration domain. It requires the shared
// domain-limit error and verifies that the account creation fixture remains
// unused.
func TestRegisterOAuthEmailAccountRejectsExhaustedNonWhitelistDomain(t *testing.T) {
	userRepo := &userRepoStub{domainCounts: map[string]int{"custom.example": 1}}
	authService := newOAuthEmailFlowAuthService(
		userRepo,
		&redeemCodeRepoStub{},
		&refreshTokenCacheStub{},
		map[string]string{
			identity.SettingKeyRegistrationEnabled:                 "true",
			identity.SettingKeyRegistrationEmailSuffixWhitelist:    `["@example.com"]`,
			identity.SettingKeyRegistrationEmailDomainQuotaEnabled: "true",
		},
		&emailCacheStub{data: &identity.VerificationCodeData{
			Code:      "246810",
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
		}},
	)

	_, _, err := authService.RegisterOAuthEmailAccount(
		context.Background(),
		"second@sub.custom.example",
		"secret-123",
		"246810",
		"",
		"oidc",
	)

	require.ErrorIs(t, err, identity.ErrEmailDomainRegistrationLimit)
	require.Empty(t, userRepo.created)
}

// TestSendPendingOAuthVerifyCodeRejectsExhaustedNonWhitelistDomain supplies
// an occupied custom domain under quota policy. It checks that pending challenge
// preparation returns the shared domain-limit rejection for that address.
func TestSendPendingOAuthVerifyCodeRejectsExhaustedNonWhitelistDomain(t *testing.T) {
	userRepo := &userRepoStub{domainCounts: map[string]int{"custom.example": 1}}
	authService := newOAuthEmailFlowAuthService(
		userRepo,
		nil,
		nil,
		map[string]string{
			identity.SettingKeyRegistrationEnabled:                 "true",
			identity.SettingKeyRegistrationEmailSuffixWhitelist:    `["@example.com"]`,
			identity.SettingKeyRegistrationEmailDomainQuotaEnabled: "true",
		},
		&emailCacheStub{},
	)

	_, err := authService.SendPendingOAuthVerifyCode(context.Background(), "second@custom.example")
	require.ErrorIs(t, err, identity.ErrEmailDomainRegistrationLimit)
}

// TestSendPendingOAuthVerifyCodeRejectsNonWhitelistDomainWhenQuotaDisabled
// supplies an unoccupied custom domain with quota disabled and a different
// whitelist. It requires suffix-policy rejection during pending challenge
// preparation.
func TestSendPendingOAuthVerifyCodeRejectsNonWhitelistDomainWhenQuotaDisabled(t *testing.T) {
	userRepo := &userRepoStub{domainCounts: map[string]int{"custom.example": 0}}
	authService := newOAuthEmailFlowAuthService(
		userRepo,
		nil,
		nil,
		map[string]string{
			identity.SettingKeyRegistrationEnabled:              "true",
			identity.SettingKeyRegistrationEmailSuffixWhitelist: `["@example.com"]`,
		},
		&emailCacheStub{},
	)

	_, err := authService.SendPendingOAuthVerifyCode(context.Background(), "first@custom.example")
	require.ErrorIs(t, err, identity.ErrEmailSuffixNotAllowed)
}

// TestRegisterOAuthEmailAccountRejectsExpiredInvitation supplies an unused
// invitation with an already elapsed expiry alongside a valid local email
// code. It requires invitation rejection and no account creation.
func TestRegisterOAuthEmailAccountRejectsExpiredInvitation(t *testing.T) {
	userRepo := &userRepoStub{nextID: 42}
	expiredAt := time.Now().UTC().Add(-time.Minute)
	redeemRepo := &redeemCodeRepoStub{
		codesByCode: map[string]*billing.RedeemCode{
			"INVITE-EXPIRED": {
				ID:        7,
				Code:      "INVITE-EXPIRED",
				Type:      billing.RedeemTypeInvitation,
				Status:    billing.StatusUnused,
				MaxUses:   1,
				ExpiresAt: &expiredAt,
			},
		},
	}
	emailCache := &emailCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "246810",
			Attempts:  0,
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
		},
	}
	authService := newOAuthEmailFlowAuthService(
		userRepo,
		redeemRepo,
		&refreshTokenCacheStub{},
		map[string]string{
			identity.SettingKeyRegistrationEnabled:    "true",
			promotion.SettingKeyInvitationCodeEnabled: "true",
			identity.SettingKeyEmailVerifyEnabled:     "true",
		},
		emailCache,
	)

	tokenPair, user, err := authService.RegisterOAuthEmailAccount(
		context.Background(),
		"fresh@example.com",
		"secret-123",
		"246810",
		"INVITE-EXPIRED",
		"oidc",
	)

	require.Nil(t, tokenPair)
	require.Nil(t, user)
	require.ErrorIs(t, err, identity.ErrInvitationCodeInvalid)
	require.Empty(t, userRepo.created)
}

// TestRegisterOAuthEmailAccountSetsNormalizedSignupSourceOnCreatedUser
// submits padded uppercase OIDC source text after local mailbox proof. It
// checks successful token/account results and the normalized source recorded
// on the created account.
func TestRegisterOAuthEmailAccountSetsNormalizedSignupSourceOnCreatedUser(t *testing.T) {
	userRepo := &userRepoStub{nextID: 42}
	emailCache := &emailCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "246810",
			Attempts:  0,
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
		},
	}
	authService := newOAuthEmailFlowAuthService(
		userRepo,
		&redeemCodeRepoStub{},
		&refreshTokenCacheStub{},
		map[string]string{
			identity.SettingKeyRegistrationEnabled: "true",
			identity.SettingKeyEmailVerifyEnabled:  "true",
		},
		emailCache,
	)

	tokenPair, user, err := authService.RegisterOAuthEmailAccount(
		context.Background(),
		"fresh@example.com",
		"secret-123",
		"246810",
		"",
		" OIDC ",
	)

	require.NoError(t, err)
	require.NotNil(t, tokenPair)
	require.NotNil(t, user)
	require.Len(t, userRepo.created, 1)
	require.Equal(t, "oidc", userRepo.created[0].SignupSource)
}

// TestRegisterOAuthEmailAccountKeepsGitHubAndGoogleSignupSource submits
// padded mixed-case labels for both recognized email providers. It checks
// that successful local-email creation stores each matching normalized
// provider source.
func TestRegisterOAuthEmailAccountKeepsGitHubAndGoogleSignupSource(t *testing.T) {
	tests := []struct {
		name         string // provider source scenario label
		email        string // scenario registration mailbox text
		signupSource string // submitted provider source text
		want         string // expected normalized source text
	}{
		{
			name:         "github",
			email:        "github@example.com",
			signupSource: " GitHub ",
			want:         "github",
		},
		{
			name:         "google",
			email:        "google@example.com",
			signupSource: " Google ",
			want:         "google",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userRepo := &userRepoStub{nextID: 43}
			emailCache := &emailCacheStub{
				data: &identity.VerificationCodeData{
					Code:      "246810",
					Attempts:  0,
					CreatedAt: time.Now().UTC(),
					ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
				},
			}
			authService := newOAuthEmailFlowAuthService(
				userRepo,
				&redeemCodeRepoStub{},
				&refreshTokenCacheStub{},
				map[string]string{
					identity.SettingKeyRegistrationEnabled: "true",
					identity.SettingKeyEmailVerifyEnabled:  "true",
				},
				emailCache,
			)

			tokenPair, user, err := authService.RegisterOAuthEmailAccount(
				context.Background(),
				tt.email,
				"secret-123",
				"246810",
				"",
				tt.signupSource,
			)

			require.NoError(t, err)
			require.NotNil(t, tokenPair)
			require.NotNil(t, user)
			require.Len(t, userRepo.created, 1)
			require.Equal(t, tt.want, userRepo.created[0].SignupSource)
		})
	}
}

// TestRegisterOAuthEmailAccountFallsBackUnknownSignupSourceToEmail submits an
// unrecognized provider label after local mailbox proof. It checks successful
// account/token results and the email source stored on that created account.
func TestRegisterOAuthEmailAccountFallsBackUnknownSignupSourceToEmail(t *testing.T) {
	userRepo := &userRepoStub{nextID: 43}
	emailCache := &emailCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "246810",
			Attempts:  0,
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
		},
	}
	authService := newOAuthEmailFlowAuthService(
		userRepo,
		&redeemCodeRepoStub{},
		&refreshTokenCacheStub{},
		map[string]string{
			identity.SettingKeyRegistrationEnabled: "true",
			identity.SettingKeyEmailVerifyEnabled:  "true",
		},
		emailCache,
	)

	tokenPair, user, err := authService.RegisterOAuthEmailAccount(
		context.Background(),
		"fallback@example.com",
		"secret-123",
		"246810",
		"",
		"twitter",
	)

	require.NoError(t, err)
	require.NotNil(t, tokenPair)
	require.NotNil(t, user)
	require.Len(t, userRepo.created, 1)
	require.Equal(t, "email", userRepo.created[0].SignupSource)
}

// TestRollbackOAuthEmailAccountCreationRestoresInvitationUsage supplies a
// used invitation belonging to the created account. It checks restoration to
// unused, cleared owner/use timestamps and recorded account deletion
// delegation.
func TestRollbackOAuthEmailAccountCreationRestoresInvitationUsage(t *testing.T) {
	userRepo := &userRepoStub{}
	redeemRepo := &redeemCodeRepoStub{
		codesByCode: map[string]*billing.RedeemCode{
			"INVITE123": {
				ID:     7,
				Code:   "INVITE123",
				Type:   billing.RedeemTypeInvitation,
				Status: billing.StatusUsed,
				UsedBy: func() *int64 {
					v := int64(42)
					return &v
				}(),
				UsedAt: func() *time.Time {
					v := time.Now().UTC()
					return &v
				}(),
			},
		},
	}
	authService := newOAuthEmailFlowAuthService(
		userRepo,
		redeemRepo,
		&refreshTokenCacheStub{},
		map[string]string{
			identity.SettingKeyRegistrationEnabled:    "true",
			promotion.SettingKeyInvitationCodeEnabled: "true",
		},
		&emailCacheStub{},
	)

	err := authService.RollbackOAuthEmailAccountCreation(context.Background(), 42, "INVITE123")

	require.NoError(t, err)
	require.Equal(t, []int64{42}, userRepo.deletedIDs)
	require.Len(t, redeemRepo.updateCalls, 1)
	require.Equal(t, billing.StatusUnused, redeemRepo.updateCalls[0].Status)
	require.Nil(t, redeemRepo.updateCalls[0].UsedBy)
	require.Nil(t, redeemRepo.updateCalls[0].UsedAt)
}

// TestRollbackOAuthEmailAccountCreationPropagatesDeleteError configures
// account deletion to fail during cleanup with no invitation code. It
// requires the wrapped deletion error to remain visible to the caller.
func TestRollbackOAuthEmailAccountCreationPropagatesDeleteError(t *testing.T) {
	userRepo := &userRepoStub{deleteErr: errors.New("delete failed")}
	authService := newOAuthEmailFlowAuthService(
		userRepo,
		&redeemCodeRepoStub{},
		&refreshTokenCacheStub{},
		map[string]string{
			identity.SettingKeyRegistrationEnabled: "true",
		},
		&emailCacheStub{},
	)

	err := authService.RollbackOAuthEmailAccountCreation(context.Background(), 42, "")

	require.Error(t, err)
	require.Contains(t, err.Error(), "delete created oauth user")
}
