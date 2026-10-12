package completion

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// TestApplyClientModelOnlyChangesRequestedModel supplies a client display alias
// for a row with distinct internal, outbound, and mapping-chain data. It checks
// the requested display name changes while those routing observations retain their values.
func TestApplyClientModelOnlyChangesRequestedModel(t *testing.T) {
	upstreamModel := "gpt-5.6-luna-upstream"
	mappingChain := "codex-auto-review→gpt-5.6-luna→gpt-5.6-luna-upstream"
	log := &usage.UsageLog{
		Model:             "gpt-5.6-luna",
		RequestedModel:    "gpt-5.6-luna",
		UpstreamModel:     &upstreamModel,
		ModelMappingChain: &mappingChain,
	}
	ctx := context.WithValue(context.Background(), telemetry.ClientModel, "codex-auto-review")

	applyClientModel(ctx, log)

	require.Equal(t, "codex-auto-review", log.RequestedModel)
	require.Equal(t, "gpt-5.6-luna", log.Model)
	require.Equal(t, upstreamModel, *log.UpstreamModel)
	require.Equal(t, mappingChain, *log.ModelMappingChain)
}

// TestResolveUsageRateMultiplier_SubscriptionUsesPlanGroupRate supplies a covered
// subscription plan with a positive group override. It checks that preliminary
// pricing selects that factor over the smaller group and system defaults.
func TestResolveUsageRateMultiplier_SubscriptionUsesPlanGroupRate(t *testing.T) {
	t.Parallel()

	groupID := int64(7)
	got := ResolveUsageRateMultiplier(
		context.Background(),
		101,
		&groupID,
		&GroupSnapshot{ID: groupID, RateMultiplier: 0.17},
		0.17,
		&billing.UserSubscription{
			ID: 20,
			Plan: &billing.SubscriptionPlan{
				ID:       30,
				GroupIDs: []int64{groupID},
				GroupRateMultipliers: map[int64]float64{
					groupID: 1,
				},
			},
		},
		nil,
	)

	if got != 1 {
		t.Fatalf("completion.ResolveUsageRateMultiplier() = %v, want 1", got)
	}
}

// TestResolveUsageRateMultiplier_SubscriptionFallsBackToGroupRateWhenPlanGroupRateMissing
// supplies a covered plan with no group-rate entry. It checks that preliminary
// subscription pricing uses the group rate when the plan has no positive override.
func TestResolveUsageRateMultiplier_SubscriptionFallsBackToGroupRateWhenPlanGroupRateMissing(t *testing.T) {
	t.Parallel()

	groupID := int64(7)
	got := ResolveUsageRateMultiplier(
		context.Background(),
		101,
		&groupID,
		&GroupSnapshot{ID: groupID, RateMultiplier: 0.17},
		0.17,
		&billing.UserSubscription{
			ID: 20,
			Plan: &billing.SubscriptionPlan{
				ID:                   30,
				GroupIDs:             []int64{groupID},
				GroupRateMultipliers: map[int64]float64{},
			},
		},
		nil,
	)

	if got != 0.17 {
		t.Fatalf("completion.ResolveUsageRateMultiplier() = %v, want 0.17", got)
	}
}

// TestSubscriptionPlanIncludesGroup_EmptyGroupIDsIsGlobal supplies a plan with
// no declared group list and a positive selected group. It checks global coverage
// for completion's preliminary subscription multiplier decision.
func TestSubscriptionPlanIncludesGroup_EmptyGroupIDsIsGlobal(t *testing.T) {
	t.Parallel()

	if !SubscriptionPlanIncludesGroup(&billing.SubscriptionPlan{ID: 30}, 7) {
		t.Fatal("empty plan group ids should make the plan globally available")
	}
}

// TestResolveUsageRateMultiplier_GlobalSubscriptionFallsBackToGroupRate supplies
// a global plan with no group-specific override and differing group/system rates.
// It checks that coverage selects the group factor for preliminary subscription pricing.
func TestResolveUsageRateMultiplier_GlobalSubscriptionFallsBackToGroupRate(t *testing.T) {
	t.Parallel()

	groupID := int64(7)
	got := ResolveUsageRateMultiplier(
		context.Background(),
		101,
		&groupID,
		&GroupSnapshot{ID: groupID, RateMultiplier: 0.17},
		0.25,
		&billing.UserSubscription{
			ID:   20,
			Plan: &billing.SubscriptionPlan{ID: 30},
		},
		nil,
	)

	if got != 0.17 {
		t.Fatalf("completion.ResolveUsageRateMultiplier() = %v, want 0.17", got)
	}
}

// TestResponseModelMismatchUsesOutboundIdentity compares sent and raw response
// models across case, whitespace, versions, prefixes, and absent evidence. It checks
// nullable mismatch results and retains the separate billing model for every fixture.
func TestResponseModelMismatchUsesOutboundIdentity(t *testing.T) {
	for _, tc := range []struct {
		// name: comparison scenario label. sent: actual outbound model.
		// response: raw upstream declaration.
		name, sent, response string
		// known: declaration presence expectation. want: expected mismatch result.
		known, want bool
	}{
		{"same", "runtime", "runtime", true, false},
		{"case and whitespace", " Runtime ", "runtime", true, false},
		{"version", "runtime", "runtime-20260101", true, true},
		{"prefix", "vendor/runtime", "runtime", true, true},
		{"missing", "runtime", "", false, false},
		{"no mapping", "", "client", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Result{Model: "client", BillingModel: "price", UpstreamModel: tc.sent, UpstreamResponseModel: tc.response}
			got := responseModelMismatch(r)
			if !tc.known {
				require.Nil(t, got)
			} else {
				require.NotNil(t, got)
				require.Equal(t, tc.want, *got)
			}
			require.Equal(t, "price", r.BillingModel)
		})
	}
}

// TestForwardResultBillingModelPrefersRequestedModel supplies a logical model
// and a different outbound route key, then omits the logical name. It checks the
// logical candidate first and the outbound candidate only for missing logical evidence.
func TestForwardResultBillingModelPrefersRequestedModel(t *testing.T) {
	require.Equal(t, "claude-opus-4-6", ForwardResultBillingModel("claude-opus-4-6", "ultimate"))
	require.Equal(t, "ultimate", ForwardResultBillingModel("", "ultimate"))
}
