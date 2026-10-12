package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

func TestModelAttributeTargetOllamaLimits(t *testing.T) {
	for _, limit := range []int{-1, 0, 32000, 90000} {
		value := &Record{Platform: "openai", Type: "apikey", Credentials: map[string]any{"base_url": "https://ollama.com"}, Extra: map[string]any{"ollama_max_tokens_cap": limit}}
		target := value.ModelAttributeTarget("custom", "openai_chat_completions")
		require.True(t, target.Ollama)
		require.Equal(t, limit, *target.OllamaOutputCap)
		result := (modelcatalog.View{}).Resolve(target, modelcatalog.Attributes{})
		require.Equal(t, limit > 0, result.Rules.ClampOutput)
		if limit > 0 {
			require.Equal(t, limit, *result.Attributes.OutputLimit)
		}
		require.False(t, value.ModelAttributeTarget("custom", "openai_responses").Ollama)
	}
}
