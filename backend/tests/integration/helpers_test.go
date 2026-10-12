package integration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

type promotionAuthSettings = promotion.RuntimeSettings

// authSettingsFixture combines authentication settings readers with a fixed test site name.
type authSettingsFixture struct {
	*identity.RuntimeSettings
	*identity.GrantSettings
	*promotionAuthSettings
	oauth *identity.OAuthSettings
}

func (s authSettingsFixture) GetSiteName(context.Context) string { return "TokenRouter" }

func (s authSettingsFixture) GetDingTalkConnectOAuthConfig(ctx context.Context) (identity.DingTalkRegistrationPolicy, error) {
	value, err := s.oauth.GetDingTalkConnectOAuthConfig(ctx)
	return identity.DingTalkRegistrationPolicy{
		Enabled: value.Enabled, BypassRegistration: value.BypassRegistration,
		CorpRestrictionPolicy: value.CorpRestrictionPolicy,
	}, err
}

// newIdentityAuthForTest constructs authentication services from the supplied database, settings, and
// dependencies.
func newIdentityAuthForTest(
	client *dbent.Client,
	users identity.UserRepository,
	options *identity.AuthOptions,
	store settings.Repository,
	email identity.EmailCache,
	refresh identity.RefreshTokenCache,
	subscriptions identity.DefaultSubscriptionAssigner,
) *identity.AuthService {
	deps := &identity.AuthDependencies{
		Users: users, Options: options, RefreshTokens: refresh,
		DefaultSubscriptions: subscriptions,
	}
	if store != nil {
		deps.Settings = authSettingsFixture{
			RuntimeSettings: identity.NewRuntimeSettings(store, settings.ErrSettingNotFound),
			GrantSettings: identity.NewGrantSettings(store, identity.GrantSettingsOptions{
				DefaultBalance: options.Default.UserBalance, DefaultConcurrency: options.Default.UserConcurrency,
			}),
			promotionAuthSettings: promotion.NewRuntimeSettings(store),
			oauth:                 identity.NewOAuthSettings(store, nil, nil),
		}
	}
	if email != nil {
		deps.Email = identity.NewEmailChallenges(email, nil)
	}
	deps.DomainRegistration, _ = users.(identity.RegistrationEmailDomainRepository)
	deps.NormalizedEmailConflict, _ = users.(identity.AuthNormalizedEmailBindingConflictChecker)
	deps.EmailAliasGuard, _ = users.(identity.AuthEmailIdentityAliasGuardRepository)
	deps.AliasLookup, _ = users.(identity.EmailAliasLookupRepository)
	deps.AliasOwner, _ = users.(identity.EmailAliasOwnerLookupRepository)
	state := identitypostgres.NewAuthState(client, deps)
	core := identity.NewAuthService(deps, &identitypostgres.AuthRepository{State: state})
	state.Rules = core
	return core
}

func countProviderGrantRecords(
	t *testing.T,
	client *dbent.Client,
	userID int64,
	providerType string,
	grantReason string,
) int {
	t.Helper()

	var count int
	rows, err := client.QueryContext(
		context.Background(),
		`SELECT COUNT(*) FROM user_provider_default_grants WHERE user_id = ? AND provider_type = ? AND grant_reason = ?`,
		userID,
		providerType,
		grantReason,
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&count))
	require.NoError(t, rows.Err())
	return count
}
