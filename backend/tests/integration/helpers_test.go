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

// Authentication scenarios combine identity rules with their PostgreSQL adapter
// on SQLite, or with a nil Ent client for memory repositories. Settings and
// optional repository capabilities are assembled from the supplied fixtures so
// the same service constructors serve both storage shapes.

// promotionAuthSettings names the promotion reader embedded alongside identity
// settings in tests.
type promotionAuthSettings = promotion.RuntimeSettings

// authSettingsFixture joins identity and promotion readers for authentication
// test dependency assembly.
type authSettingsFixture struct {
	// RuntimeSettings, GrantSettings, promotionAuthSettings and oauth read
	// the same supplied settings repository. newIdentityAuthForTest
	// supplies all four together when a store is present, keeping provider
	// policy and grant defaults aligned.
	*identity.RuntimeSettings                         // auth setting reader
	*identity.GrantSettings                           // grant rule reader
	*promotionAuthSettings                            // promo rule reader
	oauth                     *identity.OAuthSettings // OAuth rule reader
}

// GetSiteName supplies the fixed site label used by authentication challenge
// fixtures.
func (s authSettingsFixture) GetSiteName(context.Context) string { return "TokenRouter" }

// GetDingTalkConnectOAuthConfig selects the DingTalk registration fields from
// the configured nonnil OAuth reader. The reader error passes through with the
// policy; newIdentityAuthForTest supplies that reader during settings assembly.
func (s authSettingsFixture) GetDingTalkConnectOAuthConfig(ctx context.Context) (identity.DingTalkRegistrationPolicy, error) {
	value, err := s.oauth.GetDingTalkConnectOAuthConfig(ctx)
	return identity.DingTalkRegistrationPolicy{
		Enabled: value.Enabled, BypassRegistration: value.BypassRegistration,
		CorpRestrictionPolicy: value.CorpRestrictionPolicy,
	}, err
}

// newIdentityAuthForTest binds authentication rules to the supplied account
// repository and Ent client. A nil client selects adapter fixture paths; a
// nonnil settings store requires options and uses its grant defaults. Supplied
// repositories and caches remain shared with the caller.
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
	// Optional account capabilities are discovered from the supplied
	// repository; failed assertions leave them nil, so memory fixtures
	// exercise the service paths available to that repository.
	deps.DomainRegistration, _ = users.(identity.RegistrationEmailDomainRepository)
	deps.NormalizedEmailConflict, _ = users.(identity.AuthNormalizedEmailBindingConflictChecker)
	deps.EmailAliasGuard, _ = users.(identity.AuthEmailIdentityAliasGuardRepository)
	deps.AliasLookup, _ = users.(identity.EmailAliasLookupRepository)
	deps.AliasOwner, _ = users.(identity.EmailAliasOwnerLookupRepository)
	state := identitypostgres.NewAuthState(client, deps)
	core := identity.NewAuthService(deps, &identitypostgres.AuthRepository{State: state})
	// AuthState needs the completed service for later business checks.
	// Assign Rules after constructing core so both participants share this
	// assembled dependency object.
	state.Rules = core
	return core
}

// countProviderGrantRecords returns the SQLite grant count for one user,
// provider and grant reason. Query, scan and row-close failures stop or fail
// the test; callers compare the returned count with their scenario's expectation.
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
	// Close rows even when a later assertion exits the test; treating close
	// failure as an assertion keeps fixture query cleanup errors visible.
	defer func() { require.NoError(t, rows.Close()) }()
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&count))
	require.NoError(t, rows.Err())
	return count
}
