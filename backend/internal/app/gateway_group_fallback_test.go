package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/apikey/testkit"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

type fallbackGroupRepository struct {
	routing.GroupRepository
	group  *routing.Group
	groups map[int64]*routing.Group
}

type fallbackUserRepository struct {
	identity.UserRepository
	user *identity.User
}

type fallbackFundingCheck struct {
	calls  int
	input  billing.CheckInput
	denied error
}

type fallbackSubscriptionRepository struct {
	billing.UserSubscriptionRepository
	value *billing.UserSubscription
}

// Test stores supply data; production operations enforce target-group authorization, subscription coverage, and
// session isolation.
type fallbackIsolationCache struct {
	session.GatewayCache
	ownerID, userID int64
	source, hash    string
}

type clientFallbackRPM struct{ calls int }

func (r fallbackGroupRepository) GetByIDLite(_ context.Context, id int64) (*routing.Group, error) {
	if r.groups != nil {
		return r.groups[id], nil
	}
	return r.group, nil
}

func (r fallbackUserRepository) GetByID(context.Context, int64) (*identity.User, error) {
	return r.user, nil
}

func (f *fallbackFundingCheck) Check(_ context.Context, input billing.CheckInput) error {
	f.calls++
	f.input = input
	return f.denied
}

func (r fallbackSubscriptionRepository) GetByID(context.Context, int64) (*billing.UserSubscription, error) {
	return r.value, nil
}

func (c *fallbackIsolationCache) SetSessionOwnerGroupID(_ context.Context, userID int64, source, hash string, groupID int64, _ time.Duration) (bool, error) {
	c.userID, c.source, c.hash = userID, source, hash
	return false, nil
}

func (c *fallbackIsolationCache) GetSessionOwnerGroupID(context.Context, int64, string, string) (int64, error) {
	return c.ownerID, nil
}

func (c *fallbackIsolationCache) RefreshSessionOwnerTTL(context.Context, int64, string, string, time.Duration) error {
	return nil
}

func TestRuntimeGroupFallbackRechecksAuthorizationBeforeFunding(t *testing.T) {
	for _, restriction := range []string{"exclusive", "disabled_public", "protocol", "inactive"} {
		t.Run(restriction, func(t *testing.T) {
			user := &identity.User{ID: 7}
			group := &routing.Group{ID: 20, Status: "active", AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolAnthropicMessages}}
			switch restriction {
			case "exclusive":
				group.IsExclusive = true
			case "disabled_public":
				user.DisabledPublicGroups = []int64{20}
			case "protocol":
				group.AllowedProtocols = []protocol.ProtocolID{protocol.ProtocolOpenAIResponses}
			case "inactive":
				group.Status = "inactive"
			}
			keys := testkit.NewService(nil, fallbackUserRepository{user: user}, fallbackGroupRepository{group: group}, nil, nil, nil, nil)
			funds := &fallbackFundingCheck{}
			resolver := provideRuntimeGroupFallbackResolver(keys, admission.NewFundingAdmission(funds, nil), nil, nil)
			key, sub, err := resolver(context.Background(), &apikey.APIKey{UserID: 7, User: user}, 20, protocol.ProtocolAnthropicMessages)
			require.Error(t, err)
			require.Nil(t, key)
			require.Nil(t, sub)
			require.Zero(t, funds.calls)
		})
	}
}

func TestRuntimeGroupFallbackChecksSelectedSubscriptionCoverage(t *testing.T) {
	user := &identity.User{ID: 7}
	target := &routing.Group{ID: 20, Status: "active", AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolAnthropicMessages}}
	keys := testkit.NewService(nil, fallbackUserRepository{user: user}, fallbackGroupRepository{group: target}, nil, nil, nil, nil)
	subscriptionID := int64(99)
	subscription := &billing.UserSubscription{ID: subscriptionID, UserID: 7, Status: "active", StartsAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour), Plan: &billing.SubscriptionPlan{GroupIDs: []int64{10}}}
	subscriptions := billing.NewSubscriptionService(nil, fallbackSubscriptionRepository{value: subscription}, nil)
	funds := &fallbackFundingCheck{}
	resolver := provideRuntimeGroupFallbackResolver(keys, admission.NewFundingAdmission(funds, nil), subscriptions, nil)
	_, _, err := resolver(context.Background(), &apikey.APIKey{UserID: 7, User: user, BillingMode: "subscription", PreferredSubscriptionID: &subscriptionID}, 20, protocol.ProtocolAnthropicMessages)
	require.ErrorIs(t, err, billing.ErrPreferredSubscriptionGroup)
	require.Zero(t, funds.calls)
}

func TestRuntimeGroupFallbackPreservesSessionNamespace(t *testing.T) {
	for _, isolated := range []bool{false, true} {
		t.Run(map[bool]string{false: "allowed", true: "isolated"}[isolated], func(t *testing.T) {
			user := &identity.User{ID: 7}
			target := &routing.Group{ID: 20, Status: "active", SessionIsolationEnabled: isolated, AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolAnthropicMessages}}
			keys := testkit.NewService(nil, fallbackUserRepository{user: user}, fallbackGroupRepository{group: target}, nil, nil, nil, nil)
			funds := &fallbackFundingCheck{}
			cache := &fallbackIsolationCache{ownerID: 10}
			resolver := provideRuntimeGroupFallbackResolver(keys, admission.NewFundingAdmission(funds, nil), nil, cache)
			ctx := requeststate.WithSessionIsolation(context.Background(), session.SessionIsolationSourceGateway, "explicit-session")
			originalGroupID := int64(10)
			original := &apikey.APIKey{ID: 1, UserID: 7, User: &identity.User{ID: 8}, GroupID: &originalGroupID, Group: &routing.Group{ID: 10}, BillingMode: "auto"}
			resolved, subscription, err := resolver(ctx, original, 20, protocol.ProtocolAnthropicMessages)
			require.Equal(t, 1, funds.calls)
			require.Equal(t, int64(20), funds.input.Group.ID)
			require.Equal(t, int64(7), funds.input.Payer.ID)
			require.Equal(t, int64(8), cache.userID)
			require.Equal(t, session.SessionIsolationSourceGateway, cache.source)
			require.Equal(t, "explicit-session", cache.hash)
			require.Equal(t, int64(10), *original.GroupID)
			if isolated {
				require.ErrorIs(t, err, session.ErrSessionIsolationConflict)
				require.Nil(t, resolved)
			} else {
				require.NoError(t, err)
				require.Nil(t, subscription)
				require.Equal(t, int64(20), *resolved.GroupID)
				require.Equal(t, int64(7), resolved.User.ID)
			}
		})
	}
}

func TestRuntimeGroupFallbackStopsOnFundingDenial(t *testing.T) {
	user := &identity.User{ID: 7}
	target := &routing.Group{ID: 20, Status: "active", AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolAnthropicMessages}}
	keys := testkit.NewService(nil, fallbackUserRepository{user: user}, fallbackGroupRepository{group: target}, nil, nil, nil, nil)
	funds := &fallbackFundingCheck{denied: billing.ErrAPIKeyRateLimit1dExceeded}
	cache := &fallbackIsolationCache{ownerID: 10}
	resolver := provideRuntimeGroupFallbackResolver(keys, admission.NewFundingAdmission(funds, nil), nil, cache)
	ctx := requeststate.WithSessionIsolation(context.Background(), session.SessionIsolationSourceGateway, "explicit-session")
	resolved, subscription, err := resolver(ctx, &apikey.APIKey{ID: 1, UserID: 7, User: user, BillingMode: "balance"}, 20, protocol.ProtocolAnthropicMessages)
	require.ErrorIs(t, err, billing.ErrAPIKeyRateLimit1dExceeded)
	require.Nil(t, resolved)
	require.Nil(t, subscription)
	require.Equal(t, 1, funds.calls)
	require.Zero(t, cache.userID)
}

func (r *clientFallbackRPM) Check(context.Context, *scheduler.RPMUser, *scheduler.RPMGroup) error {
	r.calls++
	return nil
}

// TestClientGroupFallbackAuthorizesWholeChain checks every hop, defers RPM accounting to the final handler, and
// leaves the shared key unchanged on rejection.
func TestClientGroupFallbackAuthorizesWholeChain(t *testing.T) {
	for _, outcome := range []string{"allowed", "exclusive", "protocol", "subscription", "cycle", "composite", "missing_target"} {
		t.Run(outcome, func(t *testing.T) {
			firstID, secondID, thirdID := int64(1), int64(2), int64(3)
			allowed := []protocol.ProtocolID{protocol.ProtocolOpenAIResponses}
			first := &routing.Group{ID: firstID, Status: "active", AllowedProtocols: allowed, ClaudeCodeOnly: true, FallbackGroupID: &secondID}
			second := &routing.Group{ID: secondID, Status: "active", AllowedProtocols: allowed, ClaudeCodeOnly: true, FallbackGroupID: &thirdID}
			third := &routing.Group{ID: thirdID, Status: "active", AllowedProtocols: allowed}
			user := &identity.User{ID: 7}
			key := &apikey.APIKey{ID: 8, UserID: 7, User: user, GroupID: &firstID, Group: first, BillingMode: "balance"}
			var subscriptions *billing.SubscriptionService
			switch outcome {
			case "exclusive":
				second.IsExclusive = true
			case "protocol":
				second.AllowedProtocols = []protocol.ProtocolID{protocol.ProtocolAnthropicMessages}
			case "subscription":
				subID := int64(99)
				key.BillingMode, key.PreferredSubscriptionID = "subscription", &subID
				subscriptions = billing.NewSubscriptionService(nil, fallbackSubscriptionRepository{value: &billing.UserSubscription{ID: subID, UserID: 7, Plan: &billing.SubscriptionPlan{GroupIDs: []int64{firstID}}}}, nil)
			case "cycle":
				second.FallbackGroupID = &firstID
			case "composite":
				key.IsComposite = true
				key.CompositeGroups = []apikey.APIKeyCompositeGroup{{GroupID: firstID}}
			case "missing_target":
				second.FallbackGroupID = nil
			}
			repo := fallbackGroupRepository{groups: map[int64]*routing.Group{firstID: first, secondID: second, thirdID: third}}
			keys := testkit.NewService(nil, fallbackUserRepository{user: user}, repo, nil, nil, nil, nil)
			funds, rpm := &fallbackFundingCheck{}, &clientFallbackRPM{}
			resolve := provideClientGroupFallbackResolver(keys, admission.NewFundingAdmission(funds, rpm), subscriptions, nil)
			resolved, _, err := resolve(context.Background(), key, protocol.ProtocolOpenAIResponses)
			require.Zero(t, rpm.calls)
			require.Equal(t, firstID, *key.GroupID)
			if outcome == "allowed" {
				require.NoError(t, err)
				require.Equal(t, thirdID, *resolved.GroupID)
				require.Equal(t, 2, funds.calls)
			} else {
				require.Error(t, err)
				require.Nil(t, resolved)
			}
		})
	}
}
