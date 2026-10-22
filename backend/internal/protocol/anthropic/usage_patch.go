package anthropic

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// usage_patch.go extracts and applies presence-aware usage patches from
// Messages stream events.
// Messages streams report initial prompt usage at message_start and can revise
// usage in later message_delta events. The initial event establishes prompt
// counters even when fields are absent, whereas delta prompt and output totals
// replace earlier values only when positive.
//
// Cache-creation TTL buckets need a different rule: an explicit zero clears a
// previous bucket, because the same cache-write total can move from the one-hour
// category to the five-minute category. SseUsagePatch records each field's
// presence separately from its value before MergeSSEUsagePatch applies the
// observation. Numeric decoding accepts the representations used by both JSON
// decoders and manually constructed event maps, retaining the integer conversion
// behavior of each accepted Go type.

// SseUsagePatch pairs decoded usage values with the flags controlling replacement.
type SseUsagePatch struct {
	// InputTokens replaces uncached input when HasInputTokens is true. Start
	// events establish this pair even for missing counts; delta events require
	// a positive decoded input count before setting the flag.
	InputTokens    int  // uncached input tokens
	HasInputTokens bool // input replacement flag
	// OutputTokens replaces output when HasOutputTokens is true. Extraction
	// sets this pair for positive message_delta output counts, leaving the
	// destination unchanged for missing or zero output observations.
	OutputTokens    int  // generated output tokens
	HasOutputTokens bool // output replacement flag
	// CacheCreationInputTokens replaces total cache writes when
	// HasCacheCreationInput is true. Start events establish this pair even
	// when missing, while deltas require positive decoded cache-write counts.
	CacheCreationInputTokens int  // total cache-write tokens
	HasCacheCreationInput    bool // cache-write replacement flag
	// CacheReadInputTokens replaces cache reads when HasCacheReadInput is true.
	// Start events establish this pair even when missing; delta events require
	// positive decoded cache-read counts before setting the flag.
	CacheReadInputTokens int  // total cache-read tokens
	HasCacheReadInput    bool // cache-read replacement flag
	// CacheCreation5mTokens replaces the five-minute bucket when
	// HasCacheCreation5m is true. A successfully decoded zero sets the flag
	// so a later event can clear an earlier bucket observation.
	CacheCreation5mTokens int  // five-minute cache-write tokens
	HasCacheCreation5m    bool // five-minute replacement flag
	// CacheCreation1hTokens replaces the one-hour bucket when
	// HasCacheCreation1h is true. A successfully decoded zero sets the flag
	// so a later event can clear an earlier bucket observation.
	CacheCreation1hTokens int  // one-hour cache-write tokens
	HasCacheCreation1h    bool // one-hour replacement flag
	// Speed replaces the destination processing mode when HasSpeed is true.
	// Extraction sets the flag for nonblank strings and retains the supplied
	// string, including any surrounding whitespace, for the caller's usage record.
	Speed    string // upstream processing mode
	HasSpeed bool   // speed replacement flag
}

// ParseSSEUsage decodes a Messages event and merges its recognized usage fields
// into usage. A nil destination, malformed JSON, or an event without a usable
// usage patch leaves the destination unchanged.
func ParseSSEUsage(data string, usage *protocol.TokenUsage) {
	if usage == nil {
		return
	}

	var event map[string]any
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		// Content and terminal sentinels can reach the usage parser alongside
		// JSON events; failed decoding leaves the previously accumulated counters available.
		return
	}

	if patch := ExtractSSEUsagePatch(event); patch != nil {
		MergeSSEUsagePatch(usage, patch)
	}
}

// ExtractSSEUsagePatch returns replacement instructions from a message_start
// or message_delta map. Empty, unrelated, and missing-usage events return nil.
// Start events establish input and aggregate cache counters with zero defaults;
// their output count is ignored.
//
// Delta aggregate counters require positive decoded values so an omitted or
// zero field retains earlier stream observations. TTL cache buckets accept
// decoded zero values on either event type, allowing category changes to clear
// an earlier bucket. Nonblank speed observations retain their supplied text.
func ExtractSSEUsagePatch(event map[string]any) *SseUsagePatch {
	if len(event) == 0 {
		return nil
	}

	eventType, _ := event["type"].(string)
	var usageObj map[string]any
	switch eventType {
	case "message_start":
		msg, _ := event["message"].(map[string]any)
		usageObj, _ = msg["usage"].(map[string]any)
	case "message_delta":
		usageObj, _ = event["usage"].(map[string]any)
	default:
		return nil
	}
	if len(usageObj) == 0 {
		return nil
	}

	patch := &SseUsagePatch{}
	if speed, ok := usageObj["speed"].(string); ok && strings.TrimSpace(speed) != "" {
		patch.Speed = speed
		patch.HasSpeed = true
	}
	switch eventType {
	case "message_start":
		// message_start initializes aggregate prompt buckets as one observation.
		// Missing counters therefore establish zero rather than retaining another message's values.
		patch.HasInputTokens = true
		if v, ok := ParseSSEUsageInt(usageObj["input_tokens"]); ok {
			patch.InputTokens = v
		}
		patch.HasCacheCreationInput = true
		if v, ok := ParseSSEUsageInt(usageObj["cache_creation_input_tokens"]); ok {
			patch.CacheCreationInputTokens = v
		}
		patch.HasCacheReadInput = true
		if v, ok := ParseSSEUsageInt(usageObj["cache_read_input_tokens"]); ok {
			patch.CacheReadInputTokens = v
		}
	case "message_delta":
		// Compatible providers can repeat aggregate counters in message_delta.
		// Positive values replace earlier observations; absent and zero aggregates retain them.
		if v, ok := ParseSSEUsageInt(usageObj["input_tokens"]); ok && v > 0 {
			patch.InputTokens = v
			patch.HasInputTokens = true
		}
		if v, ok := ParseSSEUsageInt(usageObj["output_tokens"]); ok && v > 0 {
			patch.OutputTokens = v
			patch.HasOutputTokens = true
		}
		if v, ok := ParseSSEUsageInt(usageObj["cache_creation_input_tokens"]); ok && v > 0 {
			patch.CacheCreationInputTokens = v
			patch.HasCacheCreationInput = true
		}
		if v, ok := ParseSSEUsageInt(usageObj["cache_read_input_tokens"]); ok && v > 0 {
			patch.CacheReadInputTokens = v
			patch.HasCacheReadInput = true
		}
	}
	// TTL counts are presence-sensitive: a decoded zero is authoritative,
	// allowing a cache-write observation to move between billing categories.
	if cc, ok := usageObj["cache_creation"].(map[string]any); ok {
		if v, exists := ParseSSEUsageInt(cc["ephemeral_5m_input_tokens"]); exists {
			patch.CacheCreation5mTokens = v
			patch.HasCacheCreation5m = true
		}
		if v, exists := ParseSSEUsageInt(cc["ephemeral_1h_input_tokens"]); exists {
			patch.CacheCreation1hTokens = v
			patch.HasCacheCreation1h = true
		}
	}
	return patch
}

// MergeSSEUsagePatch replaces destination fields selected by patch's presence
// flags, including selected zero values. Nil arguments are ignored; callers
// apply patches in stream order and serialize access to the mutable destination.
func MergeSSEUsagePatch(usage *protocol.TokenUsage, patch *SseUsagePatch) {
	if usage == nil || patch == nil {
		return
	}

	if patch.HasInputTokens {
		usage.InputTokens = patch.InputTokens
	}
	if patch.HasSpeed {
		usage.Speed = patch.Speed
	}
	if patch.HasCacheCreationInput {
		usage.CacheCreationInputTokens = patch.CacheCreationInputTokens
	}
	if patch.HasCacheReadInput {
		usage.CacheReadInputTokens = patch.CacheReadInputTokens
	}
	if patch.HasOutputTokens {
		usage.OutputTokens = patch.OutputTokens
	}
	if patch.HasCacheCreation5m {
		usage.CacheCreation5mTokens = patch.CacheCreation5mTokens
	}
	if patch.HasCacheCreation1h {
		usage.CacheCreation1hTokens = patch.CacheCreation1hTokens
	}
}

// ParseSSEUsageInt decodes supported numeric Go values or trimmed integer
// strings. Representable floating values are truncated toward zero; numeric
// types convert directly to the platform's int, so callers supply counts in its
// range. Unsupported or unparseable values return zero and false.
func ParseSSEUsageInt(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		// JSON's default number representation may include a fractional part;
		// retain the parser's truncation behavior when converting it into token counters.
		return int(v), true
	case float32:
		// Discard fractional token units before storing whole-number usage counters.
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case int32:
		return int(v), true
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i), true
		}
		// A json.Number that fails integer parsing may still encode a floating
		// value; decode that form before classifying the counter as unparseable.
		if f, err := v.Float64(); err == nil {
			return int(f), true
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return parsed, true
		}
	}
	return 0, false
}
