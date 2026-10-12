package completion

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

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

func TestSubscriptionPlanIncludesGroup_EmptyGroupIDsIsGlobal(t *testing.T) {
	t.Parallel()

	if !SubscriptionPlanIncludesGroup(&billing.SubscriptionPlan{ID: 30}, 7) {
		t.Fatal("empty plan group ids should make the plan globally available")
	}
}

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

// TestResponseModelMismatchUsesOutboundIdentity checks comparison against the model actually sent upstream.
func TestResponseModelMismatchUsesOutboundIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, sent, response string
		known, want          bool
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

func TestForwardResultBillingModelPrefersRequestedModel(t *testing.T) {
	require.Equal(t, "claude-opus-4-6", ForwardResultBillingModel("claude-opus-4-6", "ultimate"))
	require.Equal(t, "ultimate", ForwardResultBillingModel("", "ultimate"))
}
