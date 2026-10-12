package modelcatalog

import "encoding/json"

const OllamaOutputCapExtraKey = "ollama_max_tokens_cap"

// OllamaOutputCap reads the provider output cap, using the default for missing or invalid values.
// A nonpositive cap disables output-parameter clamping.
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
