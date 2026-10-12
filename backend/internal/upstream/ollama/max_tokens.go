package ollama

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

const (
	MaxTokensCapExtraKey = modelcatalog.OllamaOutputCapExtraKey
	DefaultMaxTokensCap  = modelcatalog.OllamaDefaultOutputLimit
)

// MaxTokensCap resolves a configured numeric cap. Missing or invalid values use the default.
// It accepts float64, int64, int, and integer json.Number values.
// Zero or negative caps disable clamping.
func MaxTokensCap(value any, present bool) int64 {
	return modelcatalog.OllamaOutputCap(value, present)
}

// ClampMaxTokens reduces max_tokens and max_completion_tokens when above cap.
// Nonpositive caps, invalid JSON, and update errors return the original body.
// The boolean reports whether either field changed.
func ClampMaxTokens(body []byte, cap int64) ([]byte, bool) {
	if cap <= 0 || !gjson.ValidBytes(body) {
		return body, false
	}
	clamped := false
	out := body
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		result := gjson.GetBytes(out, key)
		if !result.Exists() || result.Type != gjson.Number || result.Int() <= cap {
			continue
		}
		updated, err := sjson.SetBytes(out, key, cap)
		if err != nil {
			return body, false
		}
		out = updated
		clamped = true
	}
	return out, clamped
}
