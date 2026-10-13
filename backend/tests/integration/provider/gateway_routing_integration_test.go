//go:build integration

package provider_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/suite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Provider routing queries select active, schedulable records within the
// requested platform set and group membership. PostgreSQL fixtures distinguish
// Gemini, Antigravity, and Anthropic records; the final dispatch test compares
// stored platform values with a local branch that represents a handler
// decision.

// GatewayRoutingSuite exercises platform and group candidate queries within
// rollback-owned PostgreSQL transactions.
type GatewayRoutingSuite struct {
	suite.Suite // test assertions and runner
	// ctx, client, and providerRepo share the transaction created by SetupTest.
	// Each suite method creates fixture rows on that client, and testEntTx
	// registers rollback after the method finishes.
	ctx          context.Context                 // current test query context
	client       *dbent.Client                   // transaction-bound Ent client
	providerRepo *providerpostgres.ProviderStore // provider query store under test
}

// SetupTest creates a rollback-owned PostgreSQL transaction and binds the suite
// client and provider store to it. Each test receives its own transaction, with
// rollback registered by testEntTx.
func (s *GatewayRoutingSuite) SetupTest() {
	s.ctx = context.Background()
	tx := testEntTx(s.T())
	s.client = tx.Client()
	s.providerRepo = newProviderStoreContract(s.client, tx, nil)
}

// TestGatewayRoutingSuite runs PostgreSQL query cases with transaction-owned
// fixtures for each suite test. The suite checks platform and group membership
// filters, scheduling exclusions, and stored platform values used by a
// simulated dispatch branch.
func TestGatewayRoutingSuite(t *testing.T) {
	suite.Run(t, new(GatewayRoutingSuite))
}

// TestListSchedulableByPlatforms_GeminiAndAntigravity creates providers from
// three platforms and queries a two-platform set. The results contain the
// Gemini and Antigravity fixture IDs and exclude the Anthropic record.
func (s *GatewayRoutingSuite) TestListSchedulableByPlatforms_GeminiAndAntigravity() {
	geminiAcc := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "gemini-oauth",
		Platform:    capability.PlatformGemini,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Priority:    1,
	})

	antigravityAcc := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "antigravity-oauth",
		Platform:    capability.PlatformAntigravity,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Priority:    2,
		Credentials: map[string]any{
			"access_token":  "test-token",
			"refresh_token": "test-refresh",
			"project_id":    "test-project",
		},
	})

	// Anthropic is outside the requested platform set.
	mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "anthropic-oauth",
		Platform:    capability.PlatformAnthropic,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Priority:    0,
	})

	providers, err := s.providerRepo.ListSchedulableByPlatforms(s.ctx, []string{
		capability.PlatformGemini,
		capability.PlatformAntigravity,
	})

	s.Require().NoError(err)
	s.Require().Len(providers, 2, "应返回 gemini 和 antigravity 两个提供商")

	platforms := make(map[string]bool)
	for _, acc := range providers {
		platforms[acc.Platform] = true
	}
	s.Require().True(platforms[capability.PlatformGemini], "应包含 gemini 提供商")
	s.Require().True(platforms[capability.PlatformAntigravity], "应包含 antigravity 提供商")
	s.Require().False(platforms[capability.PlatformAnthropic], "不应包含 anthropic 提供商")

	ids := make(map[int64]bool)
	for _, acc := range providers {
		ids[acc.ID] = true
	}
	s.Require().True(ids[geminiAcc.ID])
	s.Require().True(ids[antigravityAcc.ID])
}

// TestListSchedulableByGroupIDAndPlatforms_WithGroupBinding creates bound and
// unbound Antigravity providers in a mixed-platform query. The result contains
// exactly the group member and excludes the otherwise matching unbound record.
func (s *GatewayRoutingSuite) TestListSchedulableByGroupIDAndPlatforms_WithGroupBinding() {
	// The group can contain providers from different platforms.
	group := mustCreateGroup(s.T(), s.client, &routing.Group{
		Name:   "mixed-group",
		Status: billing.StatusActive,
	})

	boundAcc := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "bound-antigravity",
		Platform:    capability.PlatformAntigravity,
		Status:      billing.StatusActive,
		Schedulable: true,
	})
	unboundAcc := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "unbound-antigravity",
		Platform:    capability.PlatformAntigravity,
		Status:      billing.StatusActive,
		Schedulable: true,
	})

	// Bind one provider and leave the other outside the group.
	mustBindProviderToGroup(s.T(), s.client, boundAcc.ID, group.ID)

	providers, err := s.providerRepo.ListSchedulableByGroupIDAndPlatforms(s.ctx, group.ID, []string{
		capability.PlatformGemini,
		capability.PlatformAntigravity,
	})

	s.Require().NoError(err)
	s.Require().Len(providers, 1, "应只返回绑定到分组的提供商")
	s.Require().Equal(boundAcc.ID, providers[0].ID)

	for _, acc := range providers {
		s.Require().NotEqual(unboundAcc.ID, acc.ID, "不应包含未绑定的提供商")
	}
}

// TestListSchedulableByPlatform_Antigravity creates Gemini and Antigravity
// providers before issuing a single-platform query. Exactly the Antigravity
// fixture is returned with its stored platform value.
func (s *GatewayRoutingSuite) TestListSchedulableByPlatform_Antigravity() {
	mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "gemini-1",
		Platform:    capability.PlatformGemini,
		Status:      billing.StatusActive,
		Schedulable: true,
	})

	antigravity := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "antigravity-1",
		Platform:    capability.PlatformAntigravity,
		Status:      billing.StatusActive,
		Schedulable: true,
	})

	providers, err := s.providerRepo.ListSchedulableByPlatform(s.ctx, capability.PlatformAntigravity)

	s.Require().NoError(err)
	s.Require().Len(providers, 1)
	s.Require().Equal(antigravity.ID, providers[0].ID)
	s.Require().Equal(capability.PlatformAntigravity, providers[0].Platform)
}

// TestSchedulableFilter_ExcludesInactive disables scheduling on one active
// provider and creates another with error status. The query returns exactly the
// remaining active, schedulable fixture.
func (s *GatewayRoutingSuite) TestSchedulableFilter_ExcludesInactive() {
	activeAcc := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "active-antigravity",
		Platform:    capability.PlatformAntigravity,
		Status:      billing.StatusActive,
		Schedulable: true,
	})

	// The fixture defaults Schedulable to true, so disable it after creation.
	inactiveAcc := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:     "inactive-antigravity",
		Platform: capability.PlatformAntigravity,
		Status:   billing.StatusActive,
	})
	s.Require().NoError(s.client.Provider.UpdateOneID(inactiveAcc.ID).SetSchedulable(false).Exec(s.ctx))

	// An error status also excludes an otherwise schedulable provider.
	mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "error-antigravity",
		Platform:    capability.PlatformAntigravity,
		Status:      providercore.StatusError,
		Schedulable: true,
	})

	providers, err := s.providerRepo.ListSchedulableByPlatform(s.ctx, capability.PlatformAntigravity)

	s.Require().NoError(err)
	s.Require().Len(providers, 1, "应只返回可调度的 active 提供商")
	s.Require().Equal(activeAcc.ID, providers[0].ID)
}

// TestPlatformRoutingDecision reads persisted Gemini and Antigravity platform
// values and applies a local dispatch branch. The resulting service-name
// strings match the table's expected labels; the fixture does not invoke those
// forwarding services.
func (s *GatewayRoutingSuite) TestPlatformRoutingDecision() {
	geminiAcc := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "gemini-route-test",
		Platform:    capability.PlatformGemini,
		Status:      billing.StatusActive,
		Schedulable: true,
	})

	antigravityAcc := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "antigravity-route-test",
		Platform:    capability.PlatformAntigravity,
		Status:      billing.StatusActive,
		Schedulable: true,
	})

	tests := []struct {
		name            string // simulated dispatch scenario label
		providerID      int64  // persisted provider fixture ID
		expectedService string // expected simulated service label
	}{
		{
			name:            "Gemini提供商路由到ForwardNative",
			providerID:      geminiAcc.ID,
			expectedService: "GeminiMessagesCompatService.ForwardNative",
		},
		{
			name:            "Antigravity提供商路由到ForwardGemini",
			providerID:      antigravityAcc.ID,
			expectedService: "AntigravityGatewayService.ForwardGemini",
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			provider, err := s.providerRepo.GetByID(s.ctx, tt.providerID)
			s.Require().NoError(err)

			// Simulate handler dispatch using the stored platform.
			var routedService string
			if provider.Platform == capability.PlatformAntigravity {
				routedService = "AntigravityGatewayService.ForwardGemini"
			} else {
				routedService = "GeminiMessagesCompatService.ForwardNative"
			}

			s.Require().Equal(tt.expectedService, routedService)
		})
	}
}
