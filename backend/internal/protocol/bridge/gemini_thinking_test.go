package bridge

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveGeminiThinkingWireControls(t *testing.T) {
	zero, limit, output, padding := 0, 24576, 64000, 1000
	yes := true
	budgetOptions := GeminiThinkingOptions{ControlsKnown: true, Reasoning: &yes, Toggle: true, Budget: true, BudgetMin: &zero, BudgetMax: &limit}
	levelOptions := GeminiThinkingOptions{ControlsKnown: true, Reasoning: &yes, Efforts: []string{"low", "medium", "high"}}
	privateOptions := GeminiThinkingOptions{ControlsKnown: true, Reasoning: &yes, ForceBudget: true, AdaptiveBudget: 24576, OutputLimit: &output, BudgetPadding: padding, Efforts: []string{"low", "medium", "high", "max"}}
	cases := []struct {
		name, body string
		options    GeminiThinkingOptions
		want       string
		failure    bool
	}{
		{"absent", `{"model":"any"}`, levelOptions, `null`, false},
		{"budget high", `{"reasoning":{"effort":"high"}}`, budgetOptions, `{"includeThoughts":true,"thinkingBudget":10240}`, false},
		{"budget max capped", `{"reasoning_effort":"max"}`, budgetOptions, `{"includeThoughts":true,"thinkingBudget":24576}`, false},
		{"dynamic", `{"thinking":{"type":"enabled","budget_tokens":-1}}`, budgetOptions, `{"includeThoughts":true,"thinkingBudget":-1}`, false},
		{"disabled zero retained", `{"thinking":{"type":"disabled"},"output_config":{"effort":"high"}}`, budgetOptions, `{"includeThoughts":false,"thinkingBudget":0}`, false},
		{"none cannot disable level", `{"reasoning":{"effort":"none"}}`, levelOptions, ``, true},
		{"level high", `{"output_config":{"effort":"high"}}`, levelOptions, `{"includeThoughts":true,"thinkingLevel":"high"}`, false},
		{"level minimal maps low", `{"reasoning_effort":"minimal"}`, levelOptions, `{"includeThoughts":true,"thinkingLevel":"low"}`, false},
		{"level xhigh maps high", `{"reasoning_effort":"xhigh"}`, levelOptions, `{"includeThoughts":true,"thinkingLevel":"high"}`, false},
		{"budget translated to level", `{"thinking":{"type":"enabled","budget_tokens":4096}}`, levelOptions, `{"includeThoughts":true,"thinkingLevel":"medium"}`, false},
		{"private adaptive", `{"thinking":{"type":"adaptive","budget_tokens":1024}}`, privateOptions, `{"includeThoughts":true,"thinkingBudget":24576}`, false},
		{"private effort", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`, privateOptions, `{"includeThoughts":true,"thinkingBudget":32768}`, false},
		{"unknown controls preserved", `{"reasoning_effort":"future-level"}`, GeminiThinkingOptions{}, `{"includeThoughts":true,"thinkingLevel":"future-level"}`, false},
		{"known no controls", `{"reasoning_effort":"high"}`, GeminiThinkingOptions{ControlsKnown: true, Reasoning: &yes}, ``, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result, err := ResolveGeminiThinking(ParseGeminiThinkingInput([]byte(test.body)), test.options)
			if test.failure {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			wire, err := json.Marshal(result)
			require.NoError(t, err)
			require.JSONEq(t, test.want, string(wire))
		})
	}
}

func TestGeminiThinkingBudgetAndOutputLimit(t *testing.T) {
	minBudget, output := 1024, 9000
	options := GeminiThinkingOptions{ControlsKnown: true, ForceBudget: true, BudgetMin: &minBudget, OutputLimit: &output, BudgetPadding: 1000}
	result, err := ResolveGeminiThinking(GeminiThinkingInput{Effort: "max"}, options)
	require.NoError(t, err)
	require.Equal(t, 8000, result.ThinkingBudget)
	output = 500
	_, err = ResolveGeminiThinking(GeminiThinkingInput{Effort: "max"}, options)
	require.ErrorContains(t, err, "no valid thinking budget")
	var decoded GeminiThinkingConfig
	require.NoError(t, json.Unmarshal([]byte(`{"includeThoughts":false,"thinkingBudget":0}`), &decoded))
	wire, err := json.Marshal(decoded)
	require.NoError(t, err)
	require.JSONEq(t, `{"includeThoughts":false,"thinkingBudget":0}`, string(wire))
}
