package provider

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// Grok media eligibility requires usable paid evidence for OAuth providers,
// while API-key providers and administrator overrides follow separate rules.
// These cases exercise grok_tier.go's bound parsers through the core eligibility
// function and check both the admission result and its diagnostic reason.

// TestGrokMediaGenerationEligibility compares provider kinds, billing observations
// and administrator overrides. It checks admission and reason for paid, free,
// forbidden, partial, malformed and missing billing evidence as well as explicit
// enable and disable choices.
func TestGrokMediaGenerationEligibility(t *testing.T) {
	weeklyUsagePercent := 12.5
	forbiddenBilling := &xai.BillingSummary{
		StatusCode:        http.StatusForbidden,
		WeeklyStatusCode:  http.StatusForbidden,
		MonthlyStatusCode: http.StatusForbidden,
	}
	weeklyAllowance := &xai.BillingSummary{
		PeriodType:       "weekly",
		UsagePercent:     &weeklyUsagePercent,
		StatusCode:       http.StatusOK,
		WeeklyStatusCode: http.StatusOK,
	}
	freeBilling := &xai.BillingSummary{
		PeriodType:        "monthly",
		StatusCode:        http.StatusOK,
		WeeklyStatusCode:  http.StatusOK,
		MonthlyStatusCode: http.StatusOK,
		MonthlyUpdatedAt:  "2026-07-17T00:00:00Z",
	}
	inconclusiveBilling := &xai.BillingSummary{
		StatusCode:        http.StatusOK,
		WeeklyStatusCode:  http.StatusOK,
		MonthlyStatusCode: http.StatusBadGateway,
		Partial:           true,
		FailedWindows:     []string{"monthly"},
	}
	weeklyForbidden := &xai.BillingSummary{
		StatusCode:        http.StatusOK,
		WeeklyStatusCode:  http.StatusForbidden,
		MonthlyStatusCode: http.StatusOK,
	}
	monthlyForbidden := &xai.BillingSummary{
		StatusCode:        http.StatusOK,
		WeeklyStatusCode:  http.StatusOK,
		MonthlyStatusCode: http.StatusForbidden,
	}

	tests := []struct {
		name       string               // subtest display label
		provider   *providercore.Record // input provider fixture
		want       bool                 // expected media admission
		wantReason string               // expected eligibility reason
	}{
		{name: "nil provider", provider: nil, want: false, wantReason: "not_grok"},
		{name: "non grok provider", provider: &providercore.Record{Platform: capability.PlatformOpenAI}, want: false, wantReason: "not_grok"},
		{name: "non oauth grok provider stays eligible", provider: &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey}, want: true, wantReason: "non_oauth"},
		{name: "unobserved oauth fails closed", provider: &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}, want: false, wantReason: "billing_unobserved"},
		{name: "weekly paid usage is eligible without inferring from period type", provider: &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Extra: map[string]any{providercore.GrokUsageBillingExtraKey: weeklyAllowance}}, want: true, wantReason: "eligible"},
		{name: "observed free provider is rejected", provider: &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Extra: map[string]any{providercore.GrokUsageBillingExtraKey: freeBilling}}, want: false, wantReason: "billing_free_tier"},
		{name: "inconclusive billing fails closed", provider: &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Extra: map[string]any{providercore.GrokUsageBillingExtraKey: inconclusiveBilling}}, want: false, wantReason: "billing_inconclusive"},
		{name: "billing forbidden is rejected", provider: &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Extra: map[string]any{providercore.GrokUsageBillingExtraKey: forbiddenBilling}}, want: false, wantReason: "billing_forbidden"},
		{name: "weekly billing forbidden is rejected after partial success", provider: &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Extra: map[string]any{providercore.GrokUsageBillingExtraKey: weeklyForbidden}}, want: false, wantReason: "billing_forbidden"},
		{name: "monthly billing forbidden is rejected after partial success", provider: &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Extra: map[string]any{providercore.GrokUsageBillingExtraKey: monthlyForbidden}}, want: false, wantReason: "billing_forbidden"},
		{name: "malformed billing observation fails closed", provider: &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Extra: map[string]any{providercore.GrokUsageBillingExtraKey: make(chan int)}}, want: false, wantReason: "billing_unobserved"},
		{name: "malformed override falls back to observations", provider: &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Extra: map[string]any{providercore.GrokMediaEligibleExtraKey: "false", providercore.GrokUsageBillingExtraKey: weeklyAllowance}}, want: true, wantReason: "eligible"},
		{name: "explicit disable wins", provider: &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Extra: map[string]any{providercore.GrokMediaEligibleExtraKey: false}}, want: false, wantReason: "override_disabled"},
		{name: "explicit enable wins over forbidden probe", provider: &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Extra: map[string]any{providercore.GrokMediaEligibleExtraKey: true, providercore.GrokUsageBillingExtraKey: forbiddenBilling}}, want: true, wantReason: "override_enabled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := providercore.GrokMediaGenerationEligibility(tt.provider, GrokTierRules())
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantReason, reason)
		})
	}
}
