package tierpolicy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEvaluateOpenAIFastPolicy_DefaultPassesKnownTiers checks several model
// and tier pairs with default empty settings. Every evaluation must return
// pass, including the empty-tier case.
func TestEvaluateOpenAIFastPolicy_DefaultPassesKnownTiers(t *testing.T) {
	settings := Default()
	require.Empty(t, Default().Rules, "default policy must not rewrite service_tier unless admin configured rules")

	action, _ := Evaluate(settings, 0, false, false, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, "pass", action)

	action, _ = Evaluate(settings, 0, false, false, "gpt-5.5-turbo", OpenAIFastTierPriority)
	require.Equal(t, "pass", action)

	action, _ = Evaluate(settings, 0, false, false, "gpt-4", OpenAIFastTierPriority)
	require.Equal(t, "pass", action)

	action, _ = Evaluate(settings, 0, false, false, "gpt-5.5", OpenAIFastTierFlex)
	require.Equal(t, "pass", action)

	action, _ = Evaluate(settings, 0, false, false, "gpt-5.6-sol", OpenAIFastTierUltrafast)
	require.Equal(t, "pass", action)

	action, _ = Evaluate(settings, 0, false, false, "gpt-5.5", "")
	require.Equal(t, "pass", action)
}

// TestEvaluateOpenAIFastPolicy_BlockRuleCarriesMessage selects a matching
// model in a blocking priority rule. The result must retain the administrator
// message together with the block action.
func TestEvaluateOpenAIFastPolicy_BlockRuleCarriesMessage(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:    OpenAIFastTierPriority,
			Action:         "block",
			Scope:          "all",
			ErrorMessage:   "fast mode is not allowed",
			ModelWhitelist: []string{"gpt-5.5"},
			FallbackAction: "pass",
		}},
	}

	action, msg := Evaluate(settings, 0, false, false, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, "block", action)
	require.Equal(t, "fast mode is not allowed", msg)
}

// TestEvaluateOpenAIFastPolicy_ScopeFiltersOAuth evaluates one OAuth-scoped
// filter against two authentication classes. The OAuth input must filter while
// the API Key input passes.
func TestEvaluateOpenAIFastPolicy_ScopeFiltersOAuth(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierAny,
			Action:      "filter",
			Scope:       "oauth",
		}},
	}

	action, _ := Evaluate(settings, 0, true, false, "gpt-4", OpenAIFastTierPriority)
	require.Equal(t, "filter", action)

	action, _ = Evaluate(settings, 0, false, false, "gpt-4", OpenAIFastTierPriority)
	require.Equal(t, "pass", action)
}

// TestEvaluateOpenAIFastPolicy_UserScopedRuleOverridesGlobalRule places a
// global filter before a user pass exception. The matching user receives pass,
// while another user still receives the global filter.
func TestEvaluateOpenAIFastPolicy_UserScopedRuleOverridesGlobalRule(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{
			{
				ServiceTier: OpenAIFastTierPriority,
				Action:      "filter",
				Scope:       "all",
			},
			{
				ServiceTier: OpenAIFastTierPriority,
				Action:      "pass",
				Scope:       "all",
				UserIDs:     []int64{42},
			},
		},
	}

	action, _ := Evaluate(settings, 42, false, false, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, "pass", action)

	action, _ = Evaluate(settings, 43, false, false, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, "filter", action)

	action, _ = Evaluate(settings, 0, false, false, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, "filter", action)
}
