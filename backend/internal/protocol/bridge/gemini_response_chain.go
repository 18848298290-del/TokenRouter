package bridge

import (
	"encoding/json"
)

// gemini_response_chain.go converts buffered Gemini responses into Responses
// or Chat replies through Messages.
// Buffered Gemini answers reach OpenAI clients through the existing Anthropic
// and Responses converters, with the requested model restored at the Responses
// stage. The native usage value travels beside that converted answer, allowing
// the upstream adapter to keep its own supplied usage object when an override
// is selected.
//
// An override is selected when at least one text or cache count is positive;
// an image-only or all-zero override does not qualify. Selection replaces the
// entire returned native usage pointer and inserts input, output, and cache
// counts into the intermediate message. The JSON round trip then supplies
// typed Anthropic data to downstream converters, so encoding failures abort
// the chain instead of returning a partially converted answer.

// NativeGeminiResponseToResponses converts a buffered Gemini answer through
// Anthropic into Responses and reports usage-override selection. Both runtimes
// provide the ID callbacks their converters require, and rawData supplies
// observed usage independently of the decoded response.
//
// A nonnil override qualifies when input, output, or cache counts are
// positive. The qualifying pointer is returned directly and its three counts
// replace intermediate message usage; image-only and all-zero values leave
// extraction in control. The intermediate JSON round trip can fail, and the
// requested model replaces the converted Responses model before success.
func NativeGeminiResponseToResponses(runtime Runtime, native NativeGeminiRuntime,
	geminiResp map[string]any,
	originalModel string,
	rawData []byte,
	usageOverride *NativeGeminiUsage,
) (*ResponsesResponse, *NativeGeminiUsage, bool, error) {
	claudeRespMap, usage := NativeConvertGeminiToClaudeMessage(native, geminiResp, originalModel, rawData, true)
	usedOverride := false
	// Collected Code Assist usage can accompany a content-bearing payload with
	// different metadata; positive text/cache observations select that complete
	// supplied usage object.
	if usageOverride != nil && (usageOverride.InputTokens > 0 || usageOverride.OutputTokens > 0 || usageOverride.CacheReadInputTokens > 0) {
		usage = usageOverride
		usedOverride = true
		if usageMap, ok := claudeRespMap["usage"].(map[string]any); ok {
			usageMap["input_tokens"] = usage.InputTokens
			usageMap["output_tokens"] = usage.OutputTokens
			usageMap["cache_read_input_tokens"] = usage.CacheReadInputTokens
		}
	}

	// Decode the generic intermediate message into the shared Anthropic type
	// before Responses conversion, making unsupported values an error from this
	// chain.
	claudeBytes, err := json.Marshal(claudeRespMap)
	if err != nil {
		return nil, nil, false, err
	}
	var anthropicResp AnthropicResponse
	if err := json.Unmarshal(claudeBytes, &anthropicResp); err != nil {
		return nil, nil, false, err
	}
	responsesResp := AnthropicToResponsesResponse(runtime, &anthropicResp)
	responsesResp.Model = originalModel
	return responsesResp, usage, usedOverride, nil
}

// NativeGeminiResponseToChatCompletions delegates buffered Gemini conversion
// to the Responses chain, then produces Chat Completions. It returns the same
// native usage pointer and override flag, and propagates intermediate JSON
// conversion errors.
func NativeGeminiResponseToChatCompletions(runtime Runtime, native NativeGeminiRuntime,
	geminiResp map[string]any,
	originalModel string,
	rawData []byte,
	usageOverride *NativeGeminiUsage,
) (*ChatCompletionsResponse, *NativeGeminiUsage, bool, error) {
	responsesResp, usage, usedOverride, err := NativeGeminiResponseToResponses(runtime, native, geminiResp, originalModel, rawData, usageOverride)
	if err != nil {
		return nil, nil, false, err
	}
	return ResponsesToChatCompletions(runtime, responsesResp, originalModel), usage, usedOverride, nil
}
