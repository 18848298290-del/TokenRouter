package protocol

// AudioUsage records the audio processing mode and billable quantity.
type AudioUsage struct {
	Mode            string  // realtime | tts | stt
	DurationOrUnits float64 // minutes / million-chars / hours
}

// TokenUsage holds input, output, and cache token counts.
type TokenUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreation5mTokens    int // 5-minute cache creation tokens from the nested cache_creation object
	CacheCreation1hTokens    int // 1-hour cache creation tokens from the nested cache_creation object
	ImageOutputTokens        int `json:"image_output_tokens,omitempty"`
	// Speed records the speed returned by Claude; "fast" maps to internal priority billing.
	Speed string `json:"speed,omitempty"`
}

// ApplyCacheTTLOverride reclassifies cache creation tokens into the requested TTL bucket. usage must be non-nil.
// The target "1h" selects the one-hour bucket; all other values select five minutes.
// It reports reclassification, excluding the initial fill of missing five-minute details from the aggregate.
func ApplyCacheTTLOverride(usage *TokenUsage, target string) bool {
	// When TTL details are absent, treat the aggregate as five-minute cache creation.
	if usage.CacheCreation5mTokens == 0 && usage.CacheCreation1hTokens == 0 && usage.CacheCreationInputTokens > 0 {
		usage.CacheCreation5mTokens = usage.CacheCreationInputTokens
	}

	total := usage.CacheCreation5mTokens + usage.CacheCreation1hTokens
	if total == 0 {
		return false
	}
	switch target {
	case "1h":
		if usage.CacheCreation1hTokens == total {
			return false // already entirely in the one-hour bucket
		}
		usage.CacheCreation1hTokens = total
		usage.CacheCreation5mTokens = 0
	default: // "5m"
		if usage.CacheCreation5mTokens == total {
			return false // already entirely in the five-minute bucket
		}
		usage.CacheCreation5mTokens = total
		usage.CacheCreation1hTokens = 0
	}
	return true
}

// IncludeIndependentReasoningTokens adds reasoning tokens to output up to the gap
// between total and input+output. Missing or inconsistent counts leave output unchanged.
// An xAI Chat example is prompt=32, completion=9, reasoning=94, total=135;
// a Responses example is input=32, output=9, reasoning=110, total=151. Standard
// OpenAI completion_tokens includes reasoning, with total equal to input+output.
func IncludeIndependentReasoningTokens(input, output, total, reasoning int64) int64 {
	if input < 0 || output < 0 || reasoning <= 0 || total <= 0 {
		return output
	}
	if total == input+output {
		return output
	}
	gap := total - input - output
	if gap <= 0 {
		return output
	}
	if reasoning < gap {
		gap = reasoning
	}
	return output + gap
}

// HasObservedTokens reports whether any token count is positive.
func (u *TokenUsage) HasObservedTokens() bool {
	if u == nil {
		return false
	}
	return u.InputTokens > 0 || u.OutputTokens > 0 ||
		u.CacheCreationInputTokens > 0 || u.CacheReadInputTokens > 0 ||
		u.CacheCreation5mTokens > 0 || u.CacheCreation1hTokens > 0 ||
		u.ImageOutputTokens > 0
}
