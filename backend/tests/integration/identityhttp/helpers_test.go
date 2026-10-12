package identityhttp_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	"github.com/TokenFlux/TokenRouter/ent/enttest"
	"github.com/TokenFlux/TokenRouter/ent/redeemcode"
	"github.com/TokenFlux/TokenRouter/ent/redeemcodeusage"
	dbuser "github.com/TokenFlux/TokenRouter/ent/user"
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/identity/provider"
	identitytestkit "github.com/TokenFlux/TokenRouter/internal/identity/testkit"
	"github.com/TokenFlux/TokenRouter/internal/notification"
	"github.com/TokenFlux/TokenRouter/internal/notification/smtp"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	paymenthttp "github.com/TokenFlux/TokenRouter/internal/payment/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/server/runtimeconfig"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/settings/composite"
	"github.com/TokenFlux/TokenRouter/internal/site"
	"github.com/TokenFlux/TokenRouter/internal/team"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

var (
	pendingOAuthCreateAccountPreCommitHook func(context.Context, *dbent.PendingAuthSession) error
	wechatOAuthAccessTokenURL              = provider.DefaultWeChatTokenURL
	wechatOAuthUserInfoURL                 = provider.DefaultWeChatUserInfoURL
)

type oauthPendingFlowTestHandlerOptions struct {
	invitationEnabled  bool
	emailVerifyEnabled bool
	emailCache         identitycore.EmailCache
	settingValues      map[string]string
	defaultSubAssigner identitycore.DefaultSubscriptionAssigner
	affiliateRepo      promotion.AffiliateRepository
	totpCache          identitycore.TotpCache
	totpEncryptor      identitycore.SecretEncryptor
	userRepoOptions    oauthPendingFlowUserRepoOptions
}

type oauthPendingFlowSettingRepoStub struct {
	values map[string]string
}

type oauthPendingFlowRefreshTokenCacheStub struct{}

type oauthPendingFlowEmailCacheStub struct {
	verificationCodes map[string]*identitycore.VerificationCodeData
}

type oauthPendingFlowRedeemCodeRepo struct {
	client *dbent.Client
}

type oauthPendingFlowUserRepo struct {
	client  *dbent.Client
	options oauthPendingFlowUserRepoOptions
}

type oauthPendingFlowUserRepoOptions struct {
	rejectDeleteWhileAuthIdentityExists bool
}

type oauthPendingFlowDefaultSubAssignerStub struct {
	calls []billing.AssignSubscriptionInput
}

type oauthPendingFlowAffiliateRepo struct {
	profiles map[int64]*promotion.AffiliateSummary
	byCode   map[string]int64
}

type oauthPendingFlowTotpCacheStub struct {
	setupSessions  map[int64]*identitycore.TotpSetupSession
	loginSessions  map[string]*identitycore.TotpLoginSession
	verifyAttempts map[int64]int
}

type oauthPendingFlowTotpEncryptorStub struct{}

type wechatOAuthSettingRepoStub struct {
	values map[string]string
}

type wechatOAuthRefreshTokenCacheStub struct{}

// authHTTPFixture holds dependencies and the authentication and WeChat payment handlers.
type authHTTPFixture struct {
	*identityhttp.AuthenticationHandler
	*paymenthttp.WeChatPaymentHandler
	cfg                   *config.Config
	authDB                *dbent.Client
	authService           *identitycore.AuthService
	userService           *identitycore.UserService
	settingSvc            *authSettingsFixture
	promoService          *promotion.PromoService
	redeemService         *billing.RedeemService
	totpService           *identitycore.TotpService
	userAttributeService  *identitycore.UserAttributeService
	googleIDTokenVerifier provider.GoogleIDTokenVerifier
}

// googleVerifierFixture uses the injected verifier or GoogleAPIIDTokenVerifier when none is set.
type googleVerifierFixture struct{ h *authHTTPFixture }

// authSettingsFixture combines settings readers backed by the same test store.
type authSettingsFixture struct {
	*identitycore.RuntimeSettings
	*identitycore.GrantSettings
	*site.DisplaySettings
	oauth     *identitycore.OAuthSettings
	promotion *promotion.RuntimeSettings
	backend   *admission.BackendMode
	public    *site.PublicService
	composite *composite.Runtime
}

func newOAuthPendingFlowTestHandler(t *testing.T, invitationEnabled bool) (*authHTTPFixture, *dbent.Client) {
	t.Helper()

	return newOAuthPendingFlowTestHandlerWithOptions(t, invitationEnabled, false, nil)
}

func newOAuthPendingFlowTestHandlerWithEmailVerification(
	t *testing.T,
	invitationEnabled bool,
	email string,
	code string,
) (*authHTTPFixture, *dbent.Client) {
	t.Helper()

	cache := &oauthPendingFlowEmailCacheStub{
		verificationCodes: map[string]*identitycore.VerificationCodeData{
			email: {
				Code:      code,
				Attempts:  0,
				CreatedAt: time.Now().UTC(),
				ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
			},
		},
	}
	return newOAuthPendingFlowTestHandlerWithOptions(t, invitationEnabled, true, cache)
}

func newOAuthPendingFlowTestHandlerWithOptions(
	t *testing.T,
	invitationEnabled bool,
	emailVerifyEnabled bool,
	emailCache identitycore.EmailCache,
) (*authHTTPFixture, *dbent.Client) {
	return newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		invitationEnabled:  invitationEnabled,
		emailVerifyEnabled: emailVerifyEnabled,
		emailCache:         emailCache,
	})
}

func newOAuthPendingFlowTestHandlerWithDependencies(
	t *testing.T,
	options oauthPendingFlowTestHandlerOptions,
) (*authHTTPFixture, *dbent.Client) {
	t.Helper()

	db, err := sql.Open("sqlite", "file:auth_oauth_pending_flow_handler?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS user_provider_default_grants (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL,
	provider_type TEXT NOT NULL,
	grant_reason TEXT NOT NULL DEFAULT 'first_bind',
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(user_id, provider_type, grant_reason)
)`)
	require.NoError(t, err)
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS user_avatars (
	user_id INTEGER PRIMARY KEY,
	storage_provider TEXT NOT NULL,
	storage_key TEXT NOT NULL DEFAULT '',
	url TEXT NOT NULL,
	content_type TEXT NOT NULL DEFAULT '',
	byte_size INTEGER NOT NULL DEFAULT 0,
	sha256 TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)`)
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))

	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret:                   "test-secret",
			ExpireHour:               1,
			AccessTokenExpireMinutes: 60,
			RefreshTokenExpireDays:   7,
		},
		Default: config.DefaultConfig{
			UserBalance:     0,
			UserConcurrency: 1,
		},
	}
	settingValues := map[string]string{
		identitycore.SettingKeyRegistrationEnabled: "true",
		promotion.SettingKeyInvitationCodeEnabled:  boolSettingValue(options.invitationEnabled),
		identitycore.SettingKeyEmailVerifyEnabled:  boolSettingValue(options.emailVerifyEnabled),
	}
	maps.Copy(settingValues, options.settingValues)
	settingSvc := newAuthSettingsFixture(&oauthPendingFlowSettingRepoStub{values: settingValues}, cfg)
	userRepo := &oauthPendingFlowUserRepo{
		client:  client,
		options: options.userRepoOptions,
	}
	redeemRepo := &oauthPendingFlowRedeemCodeRepo{client: client}
	var emailService *identitycore.EmailChallenges
	if options.emailCache != nil {
		emailService = identitycore.NewEmailChallenges(options.emailCache, notification.NewMailer(&oauthPendingFlowSettingRepoStub{
			values: map[string]string{
				identitycore.SettingKeyEmailVerifyEnabled: boolSettingValue(options.emailVerifyEnabled),
			},
		}, smtp.New()))
	}
	affiliateRepo := options.affiliateRepo
	if affiliateRepo == nil {
		affiliateRepo = newOAuthPendingFlowAffiliateRepo()
	}
	affiliateSvc := promotion.NewAffiliateService(affiliateRepo, settingSvc.promotion, nil, nil, promotion.Runtime{})
	authSvc := identitytestkit.Auth(
		client, &identitycore.AuthDependencies{Users: userRepo, Redeem: redeemRepo, RefreshTokens: &oauthPendingFlowRefreshTokenCacheStub{}, Options: identitytestkit.AuthOptions(cfg), Settings: authContractSettings(settingSvc), Email: identitytestkit.Email(emailService), DefaultSubscriptions: options.defaultSubAssigner, Affiliate: identitytestkit.Affiliate(affiliateSvc)},
	)
	userSvc := identitycore.NewUserService(userRepo, nil, nil, nil, authBackgroundFixture(t))
	var totpSvc *identitycore.TotpService
	if options.totpCache != nil || options.totpEncryptor != nil {
		totpCache := options.totpCache
		if totpCache == nil {
			totpCache = &oauthPendingFlowTotpCacheStub{}
		}
		totpEncryptor := options.totpEncryptor
		if totpEncryptor == nil {
			totpEncryptor = oauthPendingFlowTotpEncryptorStub{}
		}
		totpSvc = identitycore.NewTotpService(userRepo, totpEncryptor, totpCache, settingSvc, nil, nil)
	}

	return newAuthHTTPFixture(t, &authHTTPFixture{
		authDB:      client,
		authService: authSvc,
		userService: userSvc,
		settingSvc:  settingSvc,
		totpService: totpSvc,
	}), client
}

func boolSettingValue(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func boolPtr(v bool) *bool {
	return &v
}

func (s *oauthPendingFlowSettingRepoStub) Get(context.Context, string) (*settings.Setting, error) {
	return nil, settings.ErrSettingNotFound
}

func (s *oauthPendingFlowSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	value, ok := s.values[key]
	if !ok {
		return "", settings.ErrSettingNotFound
	}
	return value, nil
}

func (s *oauthPendingFlowSettingRepoStub) Set(context.Context, string, string) error {
	return nil
}

func (s *oauthPendingFlowSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (s *oauthPendingFlowSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	return nil
}

func (s *oauthPendingFlowSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	result := make(map[string]string, len(s.values))
	maps.Copy(result, s.values)
	return result, nil
}

func (s *oauthPendingFlowSettingRepoStub) Delete(context.Context, string) error {
	return nil
}

func (s *oauthPendingFlowEmailCacheStub) GetVerificationCode(_ context.Context, email string) (*identitycore.VerificationCodeData, error) {
	if s == nil || s.verificationCodes == nil {
		return nil, nil
	}
	return s.verificationCodes[email], nil
}

func (s *oauthPendingFlowEmailCacheStub) SetVerificationCode(_ context.Context, email string, data *identitycore.VerificationCodeData, _ time.Duration) error {
	if s.verificationCodes == nil {
		s.verificationCodes = map[string]*identitycore.VerificationCodeData{}
	}
	s.verificationCodes[email] = data
	return nil
}

func (s *oauthPendingFlowEmailCacheStub) DeleteVerificationCode(_ context.Context, email string) error {
	delete(s.verificationCodes, email)
	return nil
}

func (s *oauthPendingFlowEmailCacheStub) GetNotifyVerifyCode(context.Context, string) (*identitycore.VerificationCodeData, error) {
	return nil, nil
}

func (s *oauthPendingFlowEmailCacheStub) SetNotifyVerifyCode(context.Context, string, *identitycore.VerificationCodeData, time.Duration) error {
	return nil
}

func (s *oauthPendingFlowEmailCacheStub) DeleteNotifyVerifyCode(context.Context, string) error {
	return nil
}

func (s *oauthPendingFlowEmailCacheStub) GetPasswordResetToken(context.Context, string) (*identitycore.PasswordResetTokenData, error) {
	return nil, nil
}

func (s *oauthPendingFlowEmailCacheStub) SetPasswordResetToken(context.Context, string, *identitycore.PasswordResetTokenData, time.Duration) error {
	return nil
}

func (s *oauthPendingFlowEmailCacheStub) DeletePasswordResetToken(context.Context, string) error {
	return nil
}

func (s *oauthPendingFlowEmailCacheStub) IsPasswordResetEmailInCooldown(context.Context, string) bool {
	return false
}

func (s *oauthPendingFlowEmailCacheStub) SetPasswordResetEmailCooldown(context.Context, string, time.Duration) error {
	return nil
}

func (s *oauthPendingFlowEmailCacheStub) IncrNotifyCodeUserRate(context.Context, int64, time.Duration) (int64, error) {
	return 0, nil
}

func (s *oauthPendingFlowEmailCacheStub) GetNotifyCodeUserRate(context.Context, int64) (int64, error) {
	return 0, nil
}

func (s *oauthPendingFlowRefreshTokenCacheStub) StoreRefreshToken(context.Context, string, *identitycore.RefreshTokenData, time.Duration) error {
	return nil
}

func (s *oauthPendingFlowRefreshTokenCacheStub) GetRefreshToken(context.Context, string) (*identitycore.RefreshTokenData, error) {
	return nil, identitycore.ErrRefreshTokenNotFound
}

func (s *oauthPendingFlowRefreshTokenCacheStub) DeleteRefreshToken(context.Context, string) error {
	return nil
}

func (s *oauthPendingFlowRefreshTokenCacheStub) DeleteUserRefreshTokens(context.Context, int64) error {
	return nil
}

func (s *oauthPendingFlowRefreshTokenCacheStub) DeleteTokenFamily(context.Context, string) error {
	return nil
}

func (s *oauthPendingFlowRefreshTokenCacheStub) AddToUserTokenSet(context.Context, int64, string, time.Duration) error {
	return nil
}

func (s *oauthPendingFlowRefreshTokenCacheStub) AddToFamilyTokenSet(context.Context, string, string, time.Duration) error {
	return nil
}

func (s *oauthPendingFlowRefreshTokenCacheStub) GetUserTokenHashes(context.Context, int64) ([]string, error) {
	return nil, nil
}

func (s *oauthPendingFlowRefreshTokenCacheStub) GetFamilyTokenHashes(context.Context, string) ([]string, error) {
	return nil, nil
}

func (s *oauthPendingFlowRefreshTokenCacheStub) IsTokenInFamily(context.Context, string, string) (bool, error) {
	return false, nil
}

func (r *oauthPendingFlowRedeemCodeRepo) Create(context.Context, *billing.RedeemCode) error {
	panic("unexpected Create call")
}

func (r *oauthPendingFlowRedeemCodeRepo) CreateBatch(context.Context, []billing.RedeemCode) error {
	panic("unexpected CreateBatch call")
}

func (r *oauthPendingFlowRedeemCodeRepo) GetByID(context.Context, int64) (*billing.RedeemCode, error) {
	panic("unexpected GetByID call")
}

func (r *oauthPendingFlowRedeemCodeRepo) GetByIDForUpdate(context.Context, int64) (*billing.RedeemCode, error) {
	panic("unexpected GetByIDForUpdate call")
}

func (r *oauthPendingFlowRedeemCodeRepo) GetByCode(ctx context.Context, code string) (*billing.RedeemCode, error) {
	entity, err := r.client.RedeemCode.Query().Where(redeemcode.CodeEQ(code)).Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, billing.ErrRedeemCodeNotFound
		}
		return nil, err
	}
	notes := ""
	if entity.Notes != nil {
		notes = *entity.Notes
	}
	return &billing.RedeemCode{
		ID:        entity.ID,
		Code:      entity.Code,
		Type:      entity.Type,
		Value:     entity.Value,
		Status:    entity.Status,
		MaxUses:   entity.MaxUses,
		UsedCount: entity.UsedCount,
		ExpiresAt: entity.ExpiresAt,
		UsedBy:    entity.UsedBy,
		UsedAt:    entity.UsedAt,
		Notes:     notes,
		CreatedAt: entity.CreatedAt,
		PlanID:    entity.PlanID,
	}, nil
}

func (r *oauthPendingFlowRedeemCodeRepo) GetByCodeForUpdate(ctx context.Context, code string) (*billing.RedeemCode, error) {
	return r.GetByCode(ctx, code)
}

func (r *oauthPendingFlowRedeemCodeRepo) Update(ctx context.Context, code *billing.RedeemCode) error {
	if code == nil {
		return nil
	}
	update := r.client.RedeemCode.UpdateOneID(code.ID).
		SetCode(code.Code).
		SetType(code.Type).
		SetValue(code.Value).
		SetStatus(code.Status).
		SetMaxUses(code.MaxUses).
		SetUsedCount(code.UsedCount).
		SetNotes(code.Notes)
	if code.ExpiresAt != nil {
		update = update.SetExpiresAt(*code.ExpiresAt)
	} else {
		update = update.ClearExpiresAt()
	}
	if code.UsedBy != nil {
		update = update.SetUsedBy(*code.UsedBy)
	} else {
		update = update.ClearUsedBy()
	}
	if code.UsedAt != nil {
		update = update.SetUsedAt(*code.UsedAt)
	} else {
		update = update.ClearUsedAt()
	}
	if code.PlanID != nil {
		update = update.SetPlanID(*code.PlanID)
	} else {
		update = update.ClearPlanID()
	}
	_, err := update.Save(ctx)
	return err
}

func (r *oauthPendingFlowRedeemCodeRepo) BatchUpdate(context.Context, []int64, billing.RedeemCodeBatchUpdateFields) (int64, error) {
	panic("unexpected BatchUpdate call")
}

func (r *oauthPendingFlowRedeemCodeRepo) Delete(context.Context, int64) error {
	panic("unexpected Delete call")
}

func (r *oauthPendingFlowRedeemCodeRepo) Use(ctx context.Context, id, userID int64) error {
	affected, err := r.client.RedeemCode.Update().
		Where(redeemcode.IDEQ(id), redeemcode.StatusEQ(billing.StatusUnused)).
		SetStatus(billing.StatusUsed).
		SetUsedCount(1).
		SetUsedBy(userID).
		SetUsedAt(time.Now().UTC()).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return billing.ErrRedeemCodeUsed
	}
	return nil
}

func (r *oauthPendingFlowRedeemCodeRepo) CreateUsage(ctx context.Context, usage *billing.RedeemCodeUsage) error {
	if usage.UsedAt.IsZero() {
		usage.UsedAt = time.Now().UTC()
	}
	entity, err := r.client.RedeemCodeUsage.Create().
		SetRedeemCodeID(usage.RedeemCodeID).
		SetUserID(usage.UserID).
		SetUsedAt(usage.UsedAt).
		Save(ctx)
	if err != nil {
		return err
	}
	usage.ID = entity.ID
	return nil
}

func (r *oauthPendingFlowRedeemCodeRepo) GetUsageByRedeemCodeAndUser(ctx context.Context, redeemCodeID, userID int64) (*billing.RedeemCodeUsage, error) {
	entity, err := r.client.RedeemCodeUsage.Query().
		Where(
			redeemcodeusage.RedeemCodeIDEQ(redeemCodeID),
			redeemcodeusage.UserIDEQ(userID),
		).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &billing.RedeemCodeUsage{
		ID:           entity.ID,
		RedeemCodeID: entity.RedeemCodeID,
		UserID:       entity.UserID,
		UsedAt:       entity.UsedAt,
	}, nil
}

func (r *oauthPendingFlowRedeemCodeRepo) List(context.Context, pagination.PaginationParams) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

func (r *oauthPendingFlowRedeemCodeRepo) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

func (r *oauthPendingFlowRedeemCodeRepo) ListByUser(context.Context, int64, int) ([]billing.RedeemCode, error) {
	panic("unexpected ListByUser call")
}

func (r *oauthPendingFlowRedeemCodeRepo) ListByUserPaginated(context.Context, int64, pagination.PaginationParams, string) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected ListByUserPaginated call")
}

func (r *oauthPendingFlowRedeemCodeRepo) SumPositiveBalanceByUser(context.Context, int64) (float64, error) {
	panic("unexpected SumPositiveBalanceByUser call")
}

func decodeJSONResponseData(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	return envelope.Data
}

func decodeJSONBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	return payload
}

func (r *oauthPendingFlowUserRepo) Create(ctx context.Context, user *identitycore.User) error {
	entity, err := r.client.User.Create().
		SetEmail(user.Email).
		SetUsername(user.Username).
		SetNotes(user.Notes).
		SetPasswordHash(user.PasswordHash).
		SetRole(user.Role).
		SetBalance(user.Balance).
		SetConcurrency(user.Concurrency).
		SetStatus(user.Status).
		SetNillableTotpSecretEncrypted(user.TotpSecretEncrypted).
		SetTotpEnabled(user.TotpEnabled).
		SetNillableTotpEnabledAt(user.TotpEnabledAt).
		SetTotalRecharged(user.TotalRecharged).
		SetSignupSource(user.SignupSource).
		SetNillableLastLoginAt(user.LastLoginAt).
		SetNillableLastActiveAt(user.LastActiveAt).
		Save(ctx)
	if err != nil {
		return err
	}
	user.ID = entity.ID
	user.CreatedAt = entity.CreatedAt
	user.UpdatedAt = entity.UpdatedAt
	return nil
}

func (r *oauthPendingFlowUserRepo) CreateWithNormalizedEmailGuard(ctx context.Context, user *identitycore.User, _ string) error {
	return r.Create(ctx, user)
}

func (r *oauthPendingFlowUserRepo) GetByID(ctx context.Context, id int64) (*identitycore.User, error) {
	entity, err := r.client.User.Get(ctx, id)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, identitycore.ErrUserNotFound
		}
		return nil, err
	}
	return oauthPendingFlowServiceUser(entity), nil
}

func (r *oauthPendingFlowUserRepo) GetByEmail(ctx context.Context, email string) (*identitycore.User, error) {
	entity, err := r.client.User.Query().Where(dbuser.EmailEQ(email)).Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, identitycore.ErrUserNotFound
		}
		return nil, err
	}
	return oauthPendingFlowServiceUser(entity), nil
}

func (r *oauthPendingFlowUserRepo) GetFirstAdmin(context.Context) (*identitycore.User, error) {
	panic("unexpected GetFirstAdmin call")
}

func (r *oauthPendingFlowUserRepo) Update(ctx context.Context, user *identitycore.User, fields identitycore.UserUpdateFields) error {
	entity, err := r.client.User.UpdateOneID(user.ID).
		SetEmail(user.Email).
		SetUsername(user.Username).
		SetNotes(user.Notes).
		SetPasswordHash(user.PasswordHash).
		SetRole(user.Role).
		SetBalance(user.Balance).
		SetConcurrency(user.Concurrency).
		SetStatus(user.Status).
		SetNillableTotpSecretEncrypted(user.TotpSecretEncrypted).
		SetTotpEnabled(user.TotpEnabled).
		SetNillableTotpEnabledAt(user.TotpEnabledAt).
		SetTotalRecharged(user.TotalRecharged).
		SetSignupSource(user.SignupSource).
		SetNillableLastLoginAt(user.LastLoginAt).
		SetNillableLastActiveAt(user.LastActiveAt).
		Save(ctx)
	if err != nil {
		return err
	}
	user.UpdatedAt = entity.UpdatedAt
	return nil
}

func (r *oauthPendingFlowUserRepo) UpdateWithNormalizedEmailGuard(ctx context.Context, user *identitycore.User, _ string, fields identitycore.UserUpdateFields) error {
	return r.Update(ctx, user, fields)
}

func (r *oauthPendingFlowUserRepo) UpdateUserLastActiveAt(ctx context.Context, userID int64, activeAt time.Time) error {
	return r.client.User.UpdateOneID(userID).SetLastActiveAt(activeAt).Exec(ctx)
}

func (r *oauthPendingFlowUserRepo) Delete(ctx context.Context, id int64) error {
	if r.options.rejectDeleteWhileAuthIdentityExists {
		count, err := r.client.AuthIdentity.Query().Where(authidentity.UserIDEQ(id)).Count(ctx)
		if err != nil {
			return err
		}
		if count > 0 {
			return errors.New("cannot delete user while auth identities still exist")
		}
	}
	return r.client.User.DeleteOneID(id).Exec(ctx)
}

func (r *oauthPendingFlowUserRepo) GetUserAvatar(ctx context.Context, userID int64) (*identitycore.UserAvatar, error) {
	driver := r.client.Driver()
	if tx := dbent.TxFromContext(ctx); tx != nil {
		driver = tx.Client().Driver()
	}

	var rows entsql.Rows
	if err := driver.Query(
		ctx,
		`SELECT storage_provider, storage_key, url, content_type, byte_size, sha256 FROM user_avatars WHERE user_id = ?`,
		[]any{userID},
		&rows,
	); err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		return nil, rows.Err()
	}

	var avatar identitycore.UserAvatar
	if err := rows.Scan(
		&avatar.StorageProvider,
		&avatar.StorageKey,
		&avatar.URL,
		&avatar.ContentType,
		&avatar.ByteSize,
		&avatar.SHA256,
	); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &avatar, nil
}

func (r *oauthPendingFlowUserRepo) UpsertUserAvatar(ctx context.Context, userID int64, input identitycore.UpsertUserAvatarInput) (*identitycore.UserAvatar, error) {
	driver := r.client.Driver()
	if tx := dbent.TxFromContext(ctx); tx != nil {
		driver = tx.Client().Driver()
	}

	var result entsql.Result
	if err := driver.Exec(
		ctx,
		`INSERT INTO user_avatars (user_id, storage_provider, storage_key, url, content_type, byte_size, sha256, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(user_id) DO UPDATE SET
	storage_provider = excluded.storage_provider,
	storage_key = excluded.storage_key,
	url = excluded.url,
	content_type = excluded.content_type,
	byte_size = excluded.byte_size,
	sha256 = excluded.sha256,
	updated_at = CURRENT_TIMESTAMP`,
		[]any{
			userID,
			input.StorageProvider,
			input.StorageKey,
			input.URL,
			input.ContentType,
			input.ByteSize,
			input.SHA256,
		},
		&result,
	); err != nil {
		return nil, err
	}

	return &identitycore.UserAvatar{
		StorageProvider: input.StorageProvider,
		StorageKey:      input.StorageKey,
		URL:             input.URL,
		ContentType:     input.ContentType,
		ByteSize:        input.ByteSize,
		SHA256:          input.SHA256,
	}, nil
}

func (r *oauthPendingFlowUserRepo) DeleteUserAvatar(ctx context.Context, userID int64) error {
	driver := r.client.Driver()
	if tx := dbent.TxFromContext(ctx); tx != nil {
		driver = tx.Client().Driver()
	}

	var result entsql.Result
	return driver.Exec(ctx, `DELETE FROM user_avatars WHERE user_id = ?`, []any{userID}, &result)
}

func (r *oauthPendingFlowUserRepo) List(context.Context, pagination.PaginationParams) ([]identitycore.User, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

func (r *oauthPendingFlowUserRepo) ListWithFilters(context.Context, pagination.PaginationParams, identitycore.UserListFilters) ([]identitycore.User, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

func (r *oauthPendingFlowUserRepo) UpdateBalance(context.Context, int64, float64) error {
	panic("unexpected UpdateBalance call")
}

func (r *oauthPendingFlowUserRepo) AddBalance(ctx context.Context, id int64, amount float64) error {
	return r.client.User.UpdateOneID(id).AddBalance(amount).Exec(ctx)
}

func (r *oauthPendingFlowUserRepo) DeductBalance(ctx context.Context, id int64, amount float64) (float64, error) {
	if amount == 0 {
		return 0, nil
	}
	if err := r.client.User.UpdateOneID(id).AddBalance(-amount).Exec(ctx); err != nil {
		return 0, err
	}
	return amount, nil
}

func (r *oauthPendingFlowUserRepo) AdjustBalance(ctx context.Context, id int64, delta float64) (identitycore.BalanceChange, error) {
	panic("unexpected AdjustBalance call")
}

func (r *oauthPendingFlowUserRepo) SetBalance(ctx context.Context, id int64, value float64) (identitycore.BalanceChange, error) {
	panic("unexpected SetBalance call")
}

func (r *oauthPendingFlowUserRepo) UpdateConcurrency(context.Context, int64, int) error {
	panic("unexpected UpdateConcurrency call")
}

func (r *oauthPendingFlowUserRepo) BatchSetConcurrency(context.Context, []int64, int) (int, error) {
	panic("unexpected BatchSetConcurrency call")
}

func (r *oauthPendingFlowUserRepo) BatchAddConcurrency(context.Context, []int64, int) (int, error) {
	panic("unexpected BatchAddConcurrency call")
}

func (r *oauthPendingFlowUserRepo) BatchUpdateLimits(context.Context, []int64, *int, *int) (int, error) {
	panic("unexpected BatchUpdateLimits call")
}

func (r *oauthPendingFlowUserRepo) GetLatestUsedAtByUserIDs(context.Context, []int64) (map[int64]*time.Time, error) {
	return map[int64]*time.Time{}, nil
}

func (r *oauthPendingFlowUserRepo) GetLatestUsedAtByUserID(context.Context, int64) (*time.Time, error) {
	return nil, nil
}

func (r *oauthPendingFlowUserRepo) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	count, err := r.client.User.Query().Where(dbuser.EmailEQ(email)).Count(ctx)
	return count > 0, err
}

func (r *oauthPendingFlowUserRepo) ExistsByNormalizedEmail(ctx context.Context, normalizedEmail string) (bool, error) {
	users, err := r.client.User.Query().All(ctx)
	if err != nil {
		return false, err
	}
	for _, user := range users {
		if identitycore.NormalizeRegistrationEmailAddress(user.Email) == normalizedEmail {
			return true, nil
		}
	}
	return false, nil
}

func (r *oauthPendingFlowUserRepo) LockRegistrationEmail(context.Context, string) error {
	return nil
}

func (r *oauthPendingFlowUserRepo) RemoveGroupFromAllowedGroups(context.Context, int64) (int64, error) {
	panic("unexpected RemoveGroupFromAllowedGroups call")
}

func (r *oauthPendingFlowUserRepo) AddGroupToAllowedGroups(context.Context, int64, int64) error {
	panic("unexpected AddGroupToAllowedGroups call")
}

func (r *oauthPendingFlowUserRepo) RemoveGroupFromUserAllowedGroups(context.Context, int64, int64) error {
	panic("unexpected RemoveGroupFromUserAllowedGroups call")
}

func (r *oauthPendingFlowUserRepo) ListUserAuthIdentities(ctx context.Context, userID int64) ([]identitycore.UserAuthIdentityRecord, error) {
	identities, err := r.client.AuthIdentity.Query().
		Where(authidentity.UserIDEQ(userID)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	records := make([]identitycore.UserAuthIdentityRecord, 0, len(identities))
	for _, identity := range identities {
		if identity == nil {
			continue
		}
		records = append(records, identitycore.UserAuthIdentityRecord{
			ProviderType:    identity.ProviderType,
			ProviderKey:     identity.ProviderKey,
			ProviderSubject: identity.ProviderSubject,
			VerifiedAt:      identity.VerifiedAt,
			Issuer:          identity.Issuer,
			Metadata:        identity.Metadata,
			CreatedAt:       identity.CreatedAt,
			UpdatedAt:       identity.UpdatedAt,
		})
	}
	return records, nil
}

func (r *oauthPendingFlowUserRepo) UnbindUserAuthProvider(context.Context, int64, string) error {
	panic("unexpected UnbindUserAuthProvider call")
}

func (r *oauthPendingFlowUserRepo) UpdateTotpSecret(ctx context.Context, userID int64, encryptedSecret *string) error {
	update := r.client.User.UpdateOneID(userID)
	if encryptedSecret == nil {
		update = update.ClearTotpSecretEncrypted()
	} else {
		update = update.SetTotpSecretEncrypted(*encryptedSecret)
	}
	return update.Exec(ctx)
}

func (r *oauthPendingFlowUserRepo) EnableTotp(ctx context.Context, userID int64) error {
	return r.client.User.UpdateOneID(userID).
		SetTotpEnabled(true).
		SetTotpEnabledAt(time.Now().UTC()).
		Exec(ctx)
}

func (r *oauthPendingFlowUserRepo) DisableTotp(ctx context.Context, userID int64) error {
	return r.client.User.UpdateOneID(userID).
		SetTotpEnabled(false).
		ClearTotpSecretEncrypted().
		ClearTotpEnabledAt().
		Exec(ctx)
}

func (r *oauthPendingFlowUserRepo) GetByIDIncludeDeleted(ctx context.Context, id int64) (*identitycore.User, error) {
	return r.GetByID(ctx, id)
}

func oauthPendingFlowServiceUser(entity *dbent.User) *identitycore.User {
	if entity == nil {
		return nil
	}
	return &identitycore.User{
		ID:                  entity.ID,
		Email:               entity.Email,
		Username:            entity.Username,
		Notes:               entity.Notes,
		PasswordHash:        entity.PasswordHash,
		Role:                entity.Role,
		Balance:             entity.Balance,
		Concurrency:         entity.Concurrency,
		Status:              entity.Status,
		SignupSource:        entity.SignupSource,
		LastLoginAt:         entity.LastLoginAt,
		LastActiveAt:        entity.LastActiveAt,
		TotpSecretEncrypted: entity.TotpSecretEncrypted,
		TotpEnabled:         entity.TotpEnabled,
		TotpEnabledAt:       entity.TotpEnabledAt,
		TotalRecharged:      entity.TotalRecharged,
		CreatedAt:           entity.CreatedAt,
		UpdatedAt:           entity.UpdatedAt,
	}
}

func newOAuthPendingFlowAffiliateRepo() *oauthPendingFlowAffiliateRepo {
	return &oauthPendingFlowAffiliateRepo{
		profiles: make(map[int64]*promotion.AffiliateSummary),
		byCode:   make(map[string]int64),
	}
}

func (r *oauthPendingFlowAffiliateRepo) EnsureUserAffiliate(_ context.Context, userID int64) (*promotion.AffiliateSummary, error) {
	if userID <= 0 {
		return nil, identitycore.ErrUserNotFound
	}
	if profile := r.profiles[userID]; profile != nil {
		cloned := *profile
		return &cloned, nil
	}
	code := "AFF" + strconv.FormatInt(userID, 10)
	profile := &promotion.AffiliateSummary{
		UserID:    userID,
		AffCode:   code,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	r.profiles[userID] = profile
	r.byCode[code] = userID
	cloned := *profile
	return &cloned, nil
}

func (r *oauthPendingFlowAffiliateRepo) GetAffiliateByCode(_ context.Context, code string) (*promotion.AffiliateSummary, error) {
	userID := r.byCode[strings.ToUpper(strings.TrimSpace(code))]
	if userID <= 0 {
		return nil, promotion.ErrAffiliateProfileNotFound
	}
	return r.EnsureUserAffiliate(context.Background(), userID)
}

func (r *oauthPendingFlowAffiliateRepo) BindInviter(_ context.Context, userID, inviterID int64) (bool, error) {
	profile, err := r.EnsureUserAffiliate(context.Background(), userID)
	if err != nil {
		return false, err
	}
	inviter, err := r.EnsureUserAffiliate(context.Background(), inviterID)
	if err != nil {
		return false, err
	}
	if profile.InviterID != nil {
		return false, nil
	}
	stored := r.profiles[userID]
	stored.InviterID = &inviterID
	stored.UpdatedAt = time.Now().UTC()
	r.profiles[inviterID].AffCount = inviter.AffCount + 1
	return true, nil
}

func (r *oauthPendingFlowAffiliateRepo) setCode(t *testing.T, userID int64, code string) {
	t.Helper()
	profile, err := r.EnsureUserAffiliate(context.Background(), userID)
	require.NoError(t, err)
	delete(r.byCode, profile.AffCode)
	normalized := strings.ToUpper(strings.TrimSpace(code))
	stored := r.profiles[userID]
	stored.AffCode = normalized
	stored.AffCodeCustom = true
	r.byCode[normalized] = userID
}

func (r *oauthPendingFlowAffiliateRepo) inviterIDFor(t *testing.T, userID int64) int64 {
	t.Helper()
	profile := r.profiles[userID]
	require.NotNil(t, profile)
	require.NotNil(t, profile.InviterID)
	return *profile.InviterID
}

func (r *oauthPendingFlowAffiliateRepo) affCountFor(t *testing.T, userID int64) int {
	t.Helper()
	profile := r.profiles[userID]
	require.NotNil(t, profile)
	return profile.AffCount
}

func (r *oauthPendingFlowAffiliateRepo) AccrueQuota(context.Context, int64, int64, float64, int, *int64) (bool, error) {
	panic("unexpected AccrueQuota call")
}

func (r *oauthPendingFlowAffiliateRepo) GetAccruedRebateFromInvitee(context.Context, int64, int64) (float64, error) {
	panic("unexpected GetAccruedRebateFromInvitee call")
}

func (r *oauthPendingFlowAffiliateRepo) ThawFrozenQuota(context.Context, int64) (float64, error) {
	return 0, nil
}

func (r *oauthPendingFlowAffiliateRepo) TransferQuotaToBalance(context.Context, int64) (float64, float64, error) {
	panic("unexpected TransferQuotaToBalance call")
}

func (r *oauthPendingFlowAffiliateRepo) ListInvitees(context.Context, int64, int) ([]promotion.AffiliateInvitee, error) {
	return nil, nil
}

func (r *oauthPendingFlowAffiliateRepo) UpdateUserAffCode(context.Context, int64, string) error {
	panic("unexpected UpdateUserAffCode call")
}

func (r *oauthPendingFlowAffiliateRepo) ResetUserAffCode(context.Context, int64) (string, error) {
	panic("unexpected ResetUserAffCode call")
}

func (r *oauthPendingFlowAffiliateRepo) SetUserRebateRate(context.Context, int64, *float64) error {
	panic("unexpected SetUserRebateRate call")
}

func (r *oauthPendingFlowAffiliateRepo) BatchSetUserRebateRate(context.Context, []int64, *float64) error {
	panic("unexpected BatchSetUserRebateRate call")
}

func (r *oauthPendingFlowAffiliateRepo) ListUsersWithCustomSettings(context.Context, promotion.AffiliateAdminFilter) ([]promotion.AffiliateAdminEntry, int64, error) {
	panic("unexpected ListUsersWithCustomSettings call")
}

func (r *oauthPendingFlowAffiliateRepo) ListAffiliateInviteRecords(context.Context, promotion.AffiliateRecordFilter) ([]promotion.AffiliateInviteRecord, int64, error) {
	panic("unexpected ListAffiliateInviteRecords call")
}

func (r *oauthPendingFlowAffiliateRepo) ListAffiliateRebateRecords(context.Context, promotion.AffiliateRecordFilter) ([]promotion.AffiliateRebateRecord, int64, error) {
	panic("unexpected ListAffiliateRebateRecords call")
}

func (r *oauthPendingFlowAffiliateRepo) ListAffiliateTransferRecords(context.Context, promotion.AffiliateRecordFilter) ([]promotion.AffiliateTransferRecord, int64, error) {
	panic("unexpected ListAffiliateTransferRecords call")
}

func (r *oauthPendingFlowAffiliateRepo) GetAffiliateUserOverview(context.Context, int64) (*promotion.AffiliateUserOverview, error) {
	panic("unexpected GetAffiliateUserOverview call")
}

func (s *oauthPendingFlowDefaultSubAssignerStub) AssignOrExtendSubscription(
	_ context.Context,
	input *billing.AssignSubscriptionInput,
) (*billing.UserSubscription, bool, error) {
	if input != nil {
		s.calls = append(s.calls, *input)
	}
	return nil, false, nil
}

func (s *oauthPendingFlowTotpCacheStub) GetSetupSession(_ context.Context, userID int64) (*identitycore.TotpSetupSession, error) {
	if s == nil || s.setupSessions == nil {
		return nil, nil
	}
	return s.setupSessions[userID], nil
}

func (s *oauthPendingFlowTotpCacheStub) SetSetupSession(_ context.Context, userID int64, session *identitycore.TotpSetupSession, _ time.Duration) error {
	if s.setupSessions == nil {
		s.setupSessions = map[int64]*identitycore.TotpSetupSession{}
	}
	s.setupSessions[userID] = session
	return nil
}

func (s *oauthPendingFlowTotpCacheStub) DeleteSetupSession(_ context.Context, userID int64) error {
	delete(s.setupSessions, userID)
	return nil
}

func (s *oauthPendingFlowTotpCacheStub) GetLoginSession(_ context.Context, tempToken string) (*identitycore.TotpLoginSession, error) {
	if s == nil || s.loginSessions == nil {
		return nil, nil
	}
	return s.loginSessions[tempToken], nil
}

func (s *oauthPendingFlowTotpCacheStub) SetLoginSession(_ context.Context, tempToken string, session *identitycore.TotpLoginSession, _ time.Duration) error {
	if s.loginSessions == nil {
		s.loginSessions = map[string]*identitycore.TotpLoginSession{}
	}
	s.loginSessions[tempToken] = session
	return nil
}

func (s *oauthPendingFlowTotpCacheStub) DeleteLoginSession(_ context.Context, tempToken string) error {
	delete(s.loginSessions, tempToken)
	return nil
}

func (s *oauthPendingFlowTotpCacheStub) IncrementVerifyAttempts(_ context.Context, userID int64) (int, error) {
	if s.verifyAttempts == nil {
		s.verifyAttempts = map[int64]int{}
	}
	s.verifyAttempts[userID]++
	return s.verifyAttempts[userID], nil
}

func (s *oauthPendingFlowTotpCacheStub) GetVerifyAttempts(_ context.Context, userID int64) (int, error) {
	if s == nil || s.verifyAttempts == nil {
		return 0, nil
	}
	return s.verifyAttempts[userID], nil
}

func (s *oauthPendingFlowTotpCacheStub) ClearVerifyAttempts(_ context.Context, userID int64) error {
	delete(s.verifyAttempts, userID)
	return nil
}

func (s *oauthPendingFlowTotpCacheStub) SetStepUpGrant(_ context.Context, _ int64, _ string, _ time.Duration) error {
	return nil
}

func (s *oauthPendingFlowTotpCacheStub) HasStepUpGrant(_ context.Context, _ int64, _ string) (bool, error) {
	return false, nil
}

func (oauthPendingFlowTotpEncryptorStub) Encrypt(plaintext string) (string, error) {
	return plaintext, nil
}

func (oauthPendingFlowTotpEncryptorStub) Decrypt(ciphertext string) (string, error) {
	return ciphertext, nil
}

// ConsumeRefreshToken reports a missing refresh credential.
func (s *oauthPendingFlowRefreshTokenCacheStub) ConsumeRefreshToken(context.Context, string) (bool, error) {
	return false, nil
}

// WithLockedInviter runs the callback synchronously; PostgreSQL integration tests cover row locking.
func (r *oauthPendingFlowAffiliateRepo) WithLockedInviter(ctx context.Context, _ int64, fn func(context.Context) error) error {
	return fn(ctx)
}

func buildEncodedOAuthBindUserCookie(t *testing.T, userID int64, secret string) string {
	t.Helper()
	value, err := identitycore.BuildOAuthBindUserCookieValue(userID, secret)
	require.NoError(t, err)
	return value
}

func encodedCookie(name, value string) *http.Cookie {
	return &http.Cookie{
		Name:  name,
		Value: identityhttp.EncodeCookieValue(value),
		Path:  "/",
	}
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func requireCookieCleared(t *testing.T, recorder *httptest.ResponseRecorder, name string) {
	t.Helper()
	cookie := findCookie(recorder.Result().Cookies(), name)
	require.NotNil(t, cookie)
	require.Equal(t, -1, cookie.MaxAge)
}

func decodeCookieValueForTest(t *testing.T, value string) string {
	t.Helper()
	decoded, err := identityhttp.DecodeCookieValue(value)
	require.NoError(t, err)
	return decoded
}

func assertOAuthRedirectError(t *testing.T, location string, errorCode string, errorMessage string) {
	t.Helper()
	values := parseOAuthRedirectFragment(t, location)
	require.Equal(t, errorCode, values.Get("error"))
	require.Equal(t, errorMessage, values.Get("error_message"))
}

func parseOAuthRedirectFragment(t *testing.T, location string) url.Values {
	t.Helper()
	require.NotEmpty(t, location)

	parsed, err := url.Parse(location)
	require.NoError(t, err)

	rawValues := parsed.RawQuery
	if rawValues == "" {
		rawValues = parsed.Fragment
	}
	values, err := url.ParseQuery(rawValues)
	require.NoError(t, err)
	return values
}

func newWeChatOAuthTestHandler(t *testing.T, invitationEnabled bool) (*authHTTPFixture, *dbent.Client) {
	return newWeChatOAuthTestHandlerWithSettings(t, invitationEnabled, nil)
}

func wechatOAuthTestSettings(mode, appID, secret, frontendRedirect string) map[string]string {
	return map[string]string{
		identitycore.SettingKeyWeChatConnectEnabled:             "true",
		identitycore.SettingKeyWeChatConnectAppID:               appID,
		identitycore.SettingKeyWeChatConnectAppSecret:           secret,
		identitycore.SettingKeyWeChatConnectMode:                mode,
		identitycore.SettingKeyWeChatConnectScopes:              identitycore.SettingsDefaultWeChatConnectScopesForMode(mode),
		identitycore.SettingKeyWeChatConnectRedirectURL:         "https://api.example.com/api/v1/auth/oauth/wechat/callback",
		identitycore.SettingKeyWeChatConnectFrontendRedirectURL: frontendRedirect,
	}
}

func newWeChatOAuthTestHandlerWithSettings(t *testing.T, invitationEnabled bool, extraSettings map[string]string) (*authHTTPFixture, *dbent.Client) {
	t.Helper()

	db, err := sql.Open("sqlite", "file:auth_wechat_oauth?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))

	userRepo := &oauthPendingFlowUserRepo{client: client}
	redeemRepo := billingpostgres.NewRedeemCodeRepository(client)
	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret:                   "test-secret",
			ExpireHour:               1,
			AccessTokenExpireMinutes: 60,
			RefreshTokenExpireDays:   7,
		},
		Default: config.DefaultConfig{
			UserBalance:     0,
			UserConcurrency: 1,
		},
	}
	values := map[string]string{
		identitycore.SettingKeyRegistrationEnabled: "true",
		promotion.SettingKeyInvitationCodeEnabled:  boolSettingValue(invitationEnabled),
	}
	maps.Copy(values, wechatOAuthTestSettings("open", "wx-open-app", "wx-open-secret", "/auth/wechat/callback"))
	maps.Copy(values, extraSettings)
	settingSvc := newAuthSettingsFixture(&wechatOAuthSettingRepoStub{values: values}, cfg)

	authSvc := identitytestkit.Auth(
		client, &identitycore.AuthDependencies{Users: userRepo, Redeem: redeemRepo, RefreshTokens: &wechatOAuthRefreshTokenCacheStub{}, Options: identitytestkit.AuthOptions(cfg), Settings: authContractSettings(settingSvc)},
	)

	return newAuthHTTPFixture(t, &authHTTPFixture{
		authDB:      client,
		authService: authSvc,
		settingSvc:  settingSvc,
		cfg:         cfg,
	}), client
}

func (s *wechatOAuthSettingRepoStub) Get(context.Context, string) (*settings.Setting, error) {
	return nil, settings.ErrSettingNotFound
}

func (s *wechatOAuthSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	value, ok := s.values[key]
	if !ok {
		return "", settings.ErrSettingNotFound
	}
	return value, nil
}

func (s *wechatOAuthSettingRepoStub) Set(context.Context, string, string) error {
	return nil
}

func (s *wechatOAuthSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (s *wechatOAuthSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	return nil
}

func (s *wechatOAuthSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	result := make(map[string]string, len(s.values))
	maps.Copy(result, s.values)
	return result, nil
}

func (s *wechatOAuthSettingRepoStub) Delete(context.Context, string) error {
	return nil
}

func (s *wechatOAuthRefreshTokenCacheStub) StoreRefreshToken(context.Context, string, *identitycore.RefreshTokenData, time.Duration) error {
	return nil
}

func (s *wechatOAuthRefreshTokenCacheStub) GetRefreshToken(context.Context, string) (*identitycore.RefreshTokenData, error) {
	return nil, identitycore.ErrRefreshTokenNotFound
}

func (s *wechatOAuthRefreshTokenCacheStub) DeleteRefreshToken(context.Context, string) error {
	return nil
}

func (s *wechatOAuthRefreshTokenCacheStub) DeleteUserRefreshTokens(context.Context, int64) error {
	return nil
}

func (s *wechatOAuthRefreshTokenCacheStub) DeleteTokenFamily(context.Context, string) error {
	return nil
}

func (s *wechatOAuthRefreshTokenCacheStub) AddToUserTokenSet(context.Context, int64, string, time.Duration) error {
	return nil
}

func (s *wechatOAuthRefreshTokenCacheStub) AddToFamilyTokenSet(context.Context, string, string, time.Duration) error {
	return nil
}

func (s *wechatOAuthRefreshTokenCacheStub) GetUserTokenHashes(context.Context, int64) ([]string, error) {
	return nil, nil
}

func (s *wechatOAuthRefreshTokenCacheStub) GetFamilyTokenHashes(context.Context, string) ([]string, error) {
	return nil, nil
}

func (s *wechatOAuthRefreshTokenCacheStub) IsTokenInFamily(context.Context, string, string) (bool, error) {
	return false, nil
}

// ConsumeRefreshToken reports a missing refresh credential.
func (s *wechatOAuthRefreshTokenCacheStub) ConsumeRefreshToken(context.Context, string) (bool, error) {
	return false, nil
}

// authBackgroundFixture waits for accepted background work before test database cleanup.
func authBackgroundFixture(t *testing.T) func(string, func()) bool {
	t.Helper()
	tasks := lifecycle.NewTasks()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := tasks.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return tasks.Go
}

func newAuthHTTPFixture(t *testing.T, input *authHTTPFixture) *authHTTPFixture {
	t.Helper()
	bindAuthHTTPFixture(t, input)
	return input
}

// bindAuthHTTPFixture rebuilds handlers after a test replaces dependencies.
func bindAuthHTTPFixture(t *testing.T, h *authHTTPFixture) {
	t.Helper()
	var client *dbent.Client
	if h.authService != nil {
		client = h.authDB
	}
	flow := &identitycore.PendingFlow{Store: identitypostgres.NewPendingRepository(client), Database: &identitypostgres.PendingFlowDatabase{Client: client, Auth: h.authService, Profiles: h.userService}, Auth: h.authService, Profiles: h.userService}
	var sessionSettings identityhttp.SessionHTTPSettings
	if h.settingSvc != nil {
		sessionSettings = h.settingSvc
	}
	secret := ""
	if h.cfg != nil {
		secret = strings.TrimSpace(h.cfg.JWT.Secret)
	}
	var pending *identityhttp.PendingHandler
	session := identityhttp.NewSessionHandler(h.authService, h.userService, sessionSettings, h.redeemService, h.totpService, flow, identityhttp.SessionHTTPOptions{AuditActor: middleware.SetAuditActor, BackendMode: func(ctx context.Context) bool {
		if h.settingSvc == nil {
			return false
		}
		v, e := h.settingSvc.public.GetPublicSettings(ctx)
		if e == nil && v != nil {
			return v.BackendModeEnabled
		}
		return h.settingSvc.IsBackendModeEnabled(ctx)
	}, ClearPendingCookies: func(c *gin.Context) {
		secure := identityhttp.IsRequestHTTPS(c)
		identityhttp.ClearOAuthPendingSessionCookie(c, secure)
		identityhttp.ClearOAuthPendingBrowserCookie(c, secure)
	}, LogoutPending: func(c *gin.Context) {
		pending.ConsumePendingOAuthSessionOnLogout(c)
		identityhttp.ClearOAuthLoginCookies(c)
		paymenthttp.ClearWeChatPaymentCookies(c)
	}, PreviewPromotion: func(ctx context.Context, code string) identityhttp.PromotionPreview {
		v := h.promoService.PreviewRegistrationPromotion(ctx, code)
		return identityhttp.PromotionPreview{Valid: v.Valid, BonusAmount: v.BonusAmount, ErrorCode: v.ErrorCode}
	}})
	bind := identityhttp.NewOAuthBindHandler(session, identitycore.NewOAuthBindingSigner(secret))
	linux := func(ctx context.Context) (identitycore.LinuxDoOAuthOptions, error) {
		if h.settingSvc != nil {
			v, e := h.settingSvc.oauth.GetLinuxDoConnectOAuthConfig(ctx)
			return identitycore.LinuxDoOAuthOptions(v), e
		}
		if h.cfg == nil {
			return identitycore.LinuxDoOAuthOptions{}, apperror.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
		}
		if !h.cfg.LinuxDo.Enabled {
			return identitycore.LinuxDoOAuthOptions{}, apperror.NotFound("OAUTH_DISABLED", "oauth login is disabled")
		}
		return identitycore.LinuxDoOAuthOptions(h.cfg.LinuxDo), nil
	}
	oidc := func(ctx context.Context) (identitycore.OIDCOAuthOptions, error) {
		if h.settingSvc != nil {
			v, e := h.settingSvc.oauth.GetOIDCConnectOAuthConfig(ctx)
			return identitycore.OIDCOAuthOptions(v), e
		}
		if h.cfg == nil {
			return identitycore.OIDCOAuthOptions{}, apperror.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
		}
		if !h.cfg.OIDC.Enabled {
			return identitycore.OIDCOAuthOptions{}, apperror.NotFound("OAUTH_DISABLED", "oauth login is disabled")
		}
		return identitycore.OIDCOAuthOptions(h.cfg.OIDC), nil
	}
	dingConfig := func(ctx context.Context) (identitycore.DingTalkOAuthOptions, error) {
		if h.settingSvc != nil {
			v, e := h.settingSvc.oauth.GetDingTalkConnectOAuthConfig(ctx)
			return identitycore.DingTalkOAuthOptions(v), e
		}
		if h.cfg == nil {
			return identitycore.DingTalkOAuthOptions{}, apperror.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
		}
		if !h.cfg.DingTalk.Enabled {
			return identitycore.DingTalkOAuthOptions{}, apperror.NotFound("OAUTH_DISABLED", "dingtalk oauth login is disabled")
		}
		return identitycore.DingTalkOAuthOptions(h.cfg.DingTalk), nil
	}
	clients := &provider.DingTalkClients{}
	syncer := &identitycore.DingTalkSyncRuntime{LoadConfig: dingConfig, Client: func(v identitycore.DingTalkOAuthOptions) identitycore.DingTalkOAuthClient {
		return clients.ForConfig(provider.DingTalkClientConfig{ClientID: v.ClientID, ClientSecret: v.ClientSecret, TokenURL: v.TokenURL, UserInfoURL: v.UserInfoURL})
	}, Profiles: &identitycore.DingTalkProfileSync{Users: h.userService, Attributes: h.userAttributeService}, Run: authBackgroundFixture(t)}
	pending = identityhttp.NewPendingHandler(session, flow, identityhttp.PendingHTTPOptions{ForceEmailOnSignup: func(ctx context.Context) bool {
		if h.settingSvc == nil {
			return false
		}
		v, e := h.settingSvc.GetAuthSourceDefaultSettings(ctx)
		return e == nil && v != nil && v.ForceEmailOnThirdPartySignup
	}, AfterLogin: func(ctx context.Context, p *identitycore.PendingAuthSession, id int64) {
		syncer.Pending(ctx, p, id, false)
	}, AfterRegistration: func(ctx context.Context, p *identitycore.PendingAuthSession, id int64) {
		syncer.Pending(ctx, p, id, true)
	}, BeforeAccountCommit: func(ctx context.Context, p *identitycore.PendingAuthSession) error {
		if pendingOAuthCreateAccountPreCommitHook == nil {
			return nil
		}
		return pendingOAuthCreateAccountPreCommitHook(ctx, identitypostgres.PendingAuthSessionToEntity(p))
	}})
	email := identityhttp.NewEmailOAuthHandler(pending, provider.EmailOAuthClientAdapter{}, func(ctx context.Context, name string) (identitycore.EmailOAuthOptions, error) {
		if h.settingSvc == nil {
			return identitycore.EmailOAuthOptions{}, apperror.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
		}
		v, e := h.settingSvc.oauth.GetEmailOAuthProviderConfig(ctx, name)
		return identitycore.EmailOAuthOptions(v), e
	})
	var registration func(context.Context) bool
	if h.settingSvc != nil {
		registration = h.settingSvc.IsRegistrationEnabled
	}
	google := identityhttp.NewGoogleOneTapHandler(pending, googleVerifierFixture{h}, identityhttp.GoogleOneTapHTTPOptions{RegistrationEnabled: registration, LoadConfig: func(ctx context.Context) (identityhttp.GoogleOneTapOptions, error) {
		if h.settingSvc == nil {
			return identityhttp.GoogleOneTapOptions{}, apperror.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
		}
		v, e := h.settingSvc.oauth.GetGoogleOneTapConfig(ctx)
		return identityhttp.GoogleOneTapOptions{ClientID: v.ClientID, FrontendRedirectURL: v.FrontendRedirectURL}, e
	}})
	apiBase := func(ctx context.Context) string {
		if h.settingSvc == nil {
			return ""
		}
		v, e := h.settingSvc.composite.GetAllSettings(ctx)
		if e != nil || v == nil {
			return ""
		}
		return strings.TrimSpace(v.APIBaseURL)
	}
	wechatOptions := identityhttp.WeChatHTTPOptions{FrontendCallback: func(ctx context.Context) string {
		if h.settingSvc != nil {
			v, e := h.settingSvc.oauth.GetWeChatConnectOAuthConfig(ctx)
			if e == nil && strings.TrimSpace(v.FrontendRedirectURL) != "" {
				return strings.TrimSpace(v.FrontendRedirectURL)
			}
		}
		return identityhttp.WechatOAuthDefaultFrontendCB
	}}
	if h.settingSvc != nil {
		wechatOptions.LoadConfig = func(ctx context.Context, mode string) (identitycore.WeChatOAuthOptions, error) {
			base := apiBase(ctx)
			v, e := h.settingSvc.oauth.GetWeChatConnectOAuthConfig(ctx)
			if e != nil {
				return identitycore.WeChatOAuthOptions{}, e
			}
			return identitycore.WeChatOAuthOptions{Mode: mode, AppID: v.AppIDForMode(mode), AppSecret: v.AppSecretForMode(mode), Scope: v.ScopeForMode(mode), RedirectURI: v.RedirectURL, FrontendCallback: v.FrontendRedirectURL, APIBaseURL: base, OpenEnabled: v.OpenEnabled, MPEnabled: v.MPEnabled}, nil
		}
	}
	wechat := identityhttp.NewWeChatHandler(pending, bind, provider.WeChatClient{TokenURL: wechatOAuthAccessTokenURL, UserInfoURL: wechatOAuthUserInfoURL}, wechatOptions)
	h.AuthenticationHandler = &identityhttp.AuthenticationHandler{Session: session, Pending: pending, Bind: bind, LinuxDo: identityhttp.NewLinuxDoHandler(pending, bind, provider.LinuxDoClient{}, linux), OIDC: identityhttp.NewOIDCHandler(pending, bind, provider.OIDCClient{}, oidc), Email: email, Google: google, WeChat: wechat, DingTalk: identityhttp.NewDingTalkHandler(pending, bind, syncer, identityhttp.DingTalkHTTPOptions{LoadConfig: dingConfig, RegistrationEnabled: registration})}
	h.WeChatPaymentHandler = paymenthttp.NewWeChatPaymentHandler(paymenthttp.WeChatPaymentHTTPOptions{Config: wechat.GetConfig, CallbackURL: func(ctx context.Context, c *gin.Context) string {
		return identityhttp.ResolveWeChatOAuthAbsoluteURL(apiBase(ctx), c, "/api/v1/auth/oauth/wechat/payment/callback")
	}, Resume: h.paymentResume, Exchange: func(ctx context.Context, v identitycore.WeChatOAuthOptions, code string) (paymenthttp.WeChatPaymentToken, error) {
		value, err := provider.ExchangeWeChatOAuthCode(ctx, provider.WeChatOptions{AppID: v.AppID, AppSecret: v.AppSecret, TokenURL: wechatOAuthAccessTokenURL}, code)
		if err != nil {
			return paymenthttp.WeChatPaymentToken{}, err
		}
		return paymenthttp.WeChatPaymentToken{OpenID: value.OpenID, Scope: value.Scope}, nil
	}})
}

func (v googleVerifierFixture) Verify(ctx context.Context, credential, audience string) (*provider.GoogleIDTokenClaims, error) {
	verifier := v.h.googleIDTokenVerifier
	if verifier == nil {
		verifier = provider.GoogleAPIIDTokenVerifier{}
	}
	return verifier.Verify(ctx, credential, audience)
}

// paymentResume resolves the environment signing key and configured encryption-key fallback.
func (h *authHTTPFixture) paymentResume() *payment.PaymentResumeService {
	var raw string
	var configured bool
	if h.cfg != nil {
		raw = h.cfg.Totp.EncryptionKey
		configured = h.cfg.Totp.EncryptionKeyConfigured
	}
	key, warning, err := payment.ConfiguredEncryptionKey(raw, configured)
	if warning != "" {
		slog.Warn(warning)
	}
	var legacy []byte
	if err == nil {
		legacy = []byte(key)
	}
	signing, fallbacks := payment.ResolvePaymentResumeSigningKeys(os.Getenv("PAYMENT_RESUME_SIGNING_KEY"), legacy)
	return payment.NewPaymentResumeService(signing, fallbacks...)
}

func newAuthSettingsFixture(repo settings.Repository, cfg *config.Config) *authSettingsFixture {
	if cfg == nil {
		cfg = &config.Config{}
	}
	store := settings.New(repo)
	oauth := identitycore.NewOAuthSettings(store, &identitycore.OAuthSettingsDefaults{LinuxDo: cfg.LinuxDo, DingTalk: cfg.DingTalk, OIDC: cfg.OIDC, WeChat: cfg.WeChat, GitHubOAuth: cfg.GitHubOAuth, GoogleOAuth: cfg.GoogleOAuth}, provider.ResolveSettingsOIDCMetadata)
	grants := identitycore.NewGrantSettings(store, identitycore.GrantSettingsOptions{DefaultBalance: cfg.Default.UserBalance, DefaultConcurrency: cfg.Default.UserConcurrency})
	value := &authSettingsFixture{RuntimeSettings: identitycore.NewRuntimeSettings(store, settings.ErrSettingNotFound), GrantSettings: grants, DisplaySettings: site.NewDisplaySettings(store, func() string { return cfg.Server.FrontendURL }), oauth: oauth, promotion: promotion.NewRuntimeSettings(store), backend: admission.NewBackendMode(store, nil)}
	value.public = site.NewPublicService(site.NewInputSource(store, site.PublicInputOptions{Auth: func(raw map[string]string) site.PublicAuth {
		v := oauth.PublicSettingsFromValues(raw)
		enabled, selfService := team.PublicSettings(raw, cfg.Team.Enabled, cfg.Team.SelfServiceEnabled)
		return site.PublicAuth{LinuxDo: v.LinuxDo, DingTalk: v.DingTalk, OIDC: v.OIDC, OIDCName: v.OIDCName, WeChat: v.WeChat, WeChatOpen: v.WeChatOpen, WeChatMP: v.WeChatMP, WeChatMobile: v.WeChatMobile, GitHub: v.GitHub, Google: v.Google, GoogleOneTap: v.GoogleOneTap, GoogleClientID: v.GoogleClientID, Team: enabled, TeamSelfService: selfService, Passkey: cfg.WebAuthn.Enabled, TencentRegion: v.TencentRegion, AliyunRegion: v.AliyunRegion, RegistrationSuffixes: v.RegistrationSuffixes}
	}, Usage: func(raw map[string]string) site.PublicUsage {
		v := usage.ParseRankingSettings(raw)
		return site.PublicUsage{Limit: v.Limit, Enabled: v.Enabled, SortBy: string(v.SortBy), ShowTotalTokens: v.ShowTotalTokens, ShowRequests: v.ShowRequests, ShowActualCost: v.ShowActualCost}
	}, Version: store.Version}), timezone.NewCalendar(time.Local), "")
	rules := gateway.AdminSettingsRules{GrokDefaultTextModel: grok.DefaultTextModel, NormalizeUserAgentVersion: antigravity.NormalizeUserAgentVersion, ValidateClaudePromptBlocks: anthropic.ValidateClaudeOAuthSystemPromptBlocksConfig}
	read := composite.ReadOptions{OAuth: oauth, Gateway: rules, Scheduler: scheduler.DefaultAdminSettingsDefaults(), DefaultBalance: func() float64 { return cfg.Default.UserBalance }, DefaultConcurrency: func() int { return cfg.Default.UserConcurrency }, Forwarded: func() runtimeconfig.ForwardedInput {
		v := cfg.ForwardedClientIPSettings()
		return runtimeconfig.ForwardedInput{APIKeyACLTrustForwardedIP: v.TrustForwardedIP, ForwardedClientIPHeaders: v.Headers}
	}, PublishModel: func(model string) {
	}}
	value.composite = composite.NewRuntime(store, read, composite.PrepareOptions{}, grants, nil, cfg.Totp.EncryptionKeyConfigured, nil)
	return value
}

func (s *authSettingsFixture) GetDingTalkConnectOAuthConfig(ctx context.Context) (identitycore.DingTalkRegistrationPolicy, error) {
	v, e := s.oauth.GetDingTalkConnectOAuthConfig(ctx)
	return identitycore.DingTalkRegistrationPolicy{Enabled: v.Enabled, BypassRegistration: v.BypassRegistration, CorpRestrictionPolicy: v.CorpRestrictionPolicy}, e
}

func (s *authSettingsFixture) IsInvitationCodeEnabled(ctx context.Context) bool {
	return s.promotion.IsInvitationCodeEnabled(ctx)
}

func (s *authSettingsFixture) IsPromoCodeEnabled(ctx context.Context) bool {
	return s.promotion.IsPromoCodeEnabled(ctx)
}

func (s *authSettingsFixture) IsBackendModeEnabled(ctx context.Context) bool {
	return s.backend.Enabled(ctx)
}

func authContractSettings(s *authSettingsFixture) identitycore.AuthSettings {
	if s == nil {
		return nil
	}
	return s
}
