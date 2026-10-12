package modelidentity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsageBillingModelCandidates_BareGPT56ExcludesSol(t *testing.T) {
	require.Equal(t,
		[]string{"gpt-5.6"},
		UsageCandidates("gpt-5.6"),
	)
	require.Equal(t,
		[]string{"openai/gpt-5.6"},
		UsageCandidates("openai/gpt-5.6"),
	)
}

// TestUsageCandidatesPreserveModelID checks billing identities with prefixes, dates, and effort suffixes.
func TestUsageCandidatesPreserveModelID(t *testing.T) {
	models := []string{
		"gpt-6-astra",
		"openai/gpt-6-astra",
		"gpt-6-astra-preview",
		"openai/gpt-6-astra-20260901",
		"gpt-6-astral",
		"gpt5.6",
		"gpt-5.6-high",
		"gpt-5.6-max",
		"gpt-5.6-2026-07-09",
		"gpt-5.6-20260709",
		"openai/gpt-5.6-max",
	}

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			require.Equal(t, []string{model}, UsageCandidates(model))
		})
	}
}

func TestUsageBillingModelCandidatesPreserveCodexAutoReviewModel(t *testing.T) {
	candidates := UsageCandidates("codex-auto-review")

	expected := []string{"codex-auto-review"}
	if len(candidates) != len(expected) {
		t.Fatalf("usageBillingModelCandidates(codex-auto-review) = %#v, want %#v", candidates, expected)
	}
	for i := range expected {
		if candidates[i] != expected[i] {
			t.Fatalf("usageBillingModelCandidates(codex-auto-review) = %#v, want %#v", candidates, expected)
		}
	}
}

func TestUsageBillingModelCandidatesPreserveGPT55ProModel(t *testing.T) {
	candidates := UsageCandidates("openai/gpt-5.5-pro")

	expected := []string{"openai/gpt-5.5-pro"}
	if len(candidates) != len(expected) {
		t.Fatalf("usageBillingModelCandidates(openai/gpt-5.5-pro) = %#v, want %#v", candidates, expected)
	}
	for i := range expected {
		if candidates[i] != expected[i] {
			t.Fatalf("usageBillingModelCandidates(openai/gpt-5.5-pro) = %#v, want %#v", candidates, expected)
		}
	}
}
