package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/modelmap"
)

// TestDefaultBetaPolicy_Context1M_Sonnet5Whitelist resolves the default
// context-1m action for direct, Vertex, and Bedrock model spellings. Listed
// Sonnet 5 forms pass, while other families and similar names use the filter
// fallback.
func TestDefaultBetaPolicy_Context1M_Sonnet5Whitelist(t *testing.T) {
	settings := DefaultBetaPolicySettings()

	var rule *BetaPolicyRule
	for i := range settings.Rules {
		if settings.Rules[i].BetaToken == BetaContext1M {
			rule = &settings.Rules[i]
			break
		}
	}
	require.NotNil(t, rule, "default policy must include context-1m-2025-08-07 rule")
	require.Equal(t, BetaPolicyActionPass, rule.Action, "primary action for whitelisted models is pass")
	require.Equal(t, BetaPolicyActionFilter, rule.FallbackAction, "non-whitelisted models must be filtered")
	require.NotEmpty(t, rule.ModelWhitelist, "context-1m must be scoped to sonnet-5 via whitelist")

	cases := []struct {
		model      string // selected model spelling
		wantAction string // expected resolved action
		desc       string // assertion failure explanation
	}{
		// Direct API forms.
		{"claude-sonnet-5", BetaPolicyActionPass, "sonnet-5 canonical"},
		{"claude-sonnet-5-20260701", BetaPolicyActionPass, "sonnet-5 dated variant matches wildcard"},
		{"claude-sonnet-5-thinking", BetaPolicyActionPass, "sonnet-5 thinking variant matches wildcard"},
		// Vertex date forms.
		{"claude-sonnet-5@20260701", BetaPolicyActionPass, "sonnet-5 Vertex-normalized dated form"},
		// Bedrock inference IDs.
		{"us.anthropic.claude-sonnet-5", BetaPolicyActionPass, "bedrock us. canonical sonnet-5"},
		{"eu.anthropic.claude-sonnet-5", BetaPolicyActionPass, "bedrock eu. canonical sonnet-5"},
		{"au.anthropic.claude-sonnet-5", BetaPolicyActionPass, "bedrock au. canonical sonnet-5"},
		{"global.anthropic.claude-sonnet-5", BetaPolicyActionPass, "bedrock global. canonical sonnet-5"},
		{"anthropic.claude-sonnet-5", BetaPolicyActionPass, "bedrock canonical sonnet-5 without region"},
		{"us.anthropic.claude-sonnet-5-v1", BetaPolicyActionPass, "bedrock us. sonnet-5"},
		{"eu.anthropic.claude-sonnet-5-20260701-v1:0", BetaPolicyActionPass, "bedrock eu. sonnet-5 dated"},
		{"apac.anthropic.claude-sonnet-5-v1", BetaPolicyActionPass, "bedrock apac. sonnet-5"},
		{"jp.anthropic.claude-sonnet-5-v1", BetaPolicyActionPass, "bedrock jp. sonnet-5"},
		{"au.anthropic.claude-sonnet-5-v1", BetaPolicyActionPass, "bedrock au. sonnet-5"},
		{"us-gov.anthropic.claude-sonnet-5-v1", BetaPolicyActionPass, "bedrock us-gov. sonnet-5"},
		{"global.anthropic.claude-sonnet-5-v1", BetaPolicyActionPass, "bedrock global. sonnet-5"},
		{"anthropic.claude-sonnet-5-v1", BetaPolicyActionPass, "bedrock no-region sonnet-5"},

		// Earlier Sonnet models are filtered.
		{"claude-sonnet-4-6", BetaPolicyActionFilter, "sonnet-4.6 must be filtered"},
		{"claude-sonnet-4-5-20250929", BetaPolicyActionFilter, "sonnet-4.5 dated must be filtered"},
		{"claude-sonnet-4", BetaPolicyActionFilter, "sonnet-4 must be filtered"},
		{"claude-sonnet-4-5@20250929", BetaPolicyActionFilter, "sonnet-4.5 Vertex format must be filtered"},
		{"us.anthropic.claude-sonnet-4-6", BetaPolicyActionFilter, "bedrock us. sonnet-4.6 must be filtered"},
		{"us.anthropic.claude-sonnet-4-5-20250929-v1:0", BetaPolicyActionFilter, "bedrock us. sonnet-4.5 must be filtered"},
		// Opus and Haiku are filtered.
		{"claude-opus-4-8", BetaPolicyActionFilter, "opus must be filtered"},
		{"claude-opus-4-7", BetaPolicyActionFilter, "opus 4.7 must be filtered"},
		{"us.anthropic.claude-opus-4-8-v1", BetaPolicyActionFilter, "bedrock opus 4.8 must be filtered"},
		{"claude-opus-5", BetaPolicyActionFilter, "opus 5 must be filtered"},
		{"us.anthropic.claude-opus-5", BetaPolicyActionFilter, "bedrock canonical opus 5 must be filtered"},
		{"claude-haiku-4-5", BetaPolicyActionFilter, "haiku must be filtered"},
		{"us.anthropic.claude-haiku-4-5-20251001-v1:0", BetaPolicyActionFilter, "bedrock haiku must be filtered"},
		{"claude-3-5-sonnet-20241022", BetaPolicyActionFilter, "legacy sonnet 3.5 must be filtered"},
		// Similar names outside the whitelist remain filtered.
		{"claude-sonnet-50", BetaPolicyActionFilter, "must not over-match a hypothetical sonnet-50"},
		{"claude-sonnet-5.1", BetaPolicyActionFilter, "must not over-match a hypothetical sonnet-5.1"},
		{"us.anthropic.claude-sonnet-50-v1", BetaPolicyActionFilter, "bedrock sonnet-50 must be filtered"},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			action, _ := modelmap.ResolveAction(modelmap.ActionRule{Action: rule.Action, ErrorMessage: rule.ErrorMessage, ModelWhitelist: rule.ModelWhitelist, FallbackAction: rule.FallbackAction, FallbackErrorMessage: rule.FallbackErrorMessage}, tc.model)
			require.Equal(t, tc.wantAction, action, tc.desc)
		})
	}
}
