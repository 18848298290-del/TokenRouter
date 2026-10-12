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

// GatewayRoutingSuite tests provider routing queries against PostgreSQL.
// It checks platform and group filtering used during provider selection.
type GatewayRoutingSuite struct {
	suite.Suite
	ctx          context.Context
	client       *dbent.Client
	providerRepo *providerpostgres.ProviderStore
}

func (s *GatewayRoutingSuite) SetupTest() {
	s.ctx = context.Background()
	tx := testEntTx(s.T())
	s.client = tx.Client()
	s.providerRepo = newProviderStoreContract(s.client, tx, nil)
}

func TestGatewayRoutingSuite(t *testing.T) {
	suite.Run(t, new(GatewayRoutingSuite))
}

// TestListSchedulableByPlatforms_GeminiAndAntigravity checks a multi-platform query.
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

// TestListSchedulableByGroupIDAndPlatforms_WithGroupBinding checks group membership filtering.
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

// TestListSchedulableByPlatform_Antigravity checks a single-platform query.
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

// TestSchedulableFilter_ExcludesInactive checks schedulability and status filters.
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

// TestPlatformRoutingDecision checks platform values read from storage
// against a simulated handler dispatch decision.
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
		name            string
		providerID      int64
		expectedService string
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
