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

// MaxTokensCap 返回提供商配置的 max_tokens 上限。提供商为 nil 或 extra 中
// 无该键时返回默认值；键值为数值类型（float64/int64/int/json.Number）时返回其整数
// 值（0 或负数表示显式禁用 clamp）；其它类型回退默认值。
func MaxTokensCap(value any, present bool) int64 {
	return modelcatalog.OllamaOutputCap(value, present)
}

// ClampMaxTokens 把 body 中超过 cap 的 max_tokens / max_completion_tokens
// 单向压到 cap。cap <= 0 或 body 不是合法 JSON 时原样返回；sjson 出错时返回原始 body。
// 有任一字段被 clamp 时记录一条 Debug 日志。
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
