package modelcatalog

import "encoding/json"

const OllamaOutputCapExtraKey = "ollama_max_tokens_cap"

// OllamaOutputCap 读取提供商的输出参数上限，缺失或无效的类型使用默认值。
// 非正数表示关闭参数裁剪。
func OllamaOutputCap(value any, present bool) int64 {
	if !present {
		return OllamaDefaultOutputLimit
	}

	switch number := value.(type) {
	case float64:
		return int64(number)
	case int64:
		return number
	case int:
		return int64(number)
	case json.Number:
		parsed, err := number.Int64()
		if err != nil {
			return OllamaDefaultOutputLimit
		}
		return parsed
	default:
		return OllamaDefaultOutputLimit
	}
}
