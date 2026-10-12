package openai

import (
	"github.com/tidwall/gjson"
)

// ForwardUsage holds token and cache usage from OpenAI responses.
type ForwardUsage struct {
	InputTokens              int `json:"input_tokens"`
	ImageInputTokens         int `json:"image_input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	ImageOutputTokens        int `json:"image_output_tokens,omitempty"`
}

func CopyForwardUsage(usage *ResponsesUsage) ForwardUsage {
	if usage == nil {
		return ForwardUsage{}
	}
	result := ForwardUsage{
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
	}
	if usage.InputTokensDetails != nil {
		result.CacheReadInputTokens = usage.InputTokensDetails.CachedTokens
	}
	return result
}

func AddForwardUsage(dst *ForwardUsage, usage ForwardUsage) {
	if dst == nil {
		return
	}
	dst.InputTokens += usage.InputTokens
	dst.ImageInputTokens += usage.ImageInputTokens
	dst.OutputTokens += usage.OutputTokens
	dst.CacheCreationInputTokens += usage.CacheCreationInputTokens
	dst.CacheReadInputTokens += usage.CacheReadInputTokens
	dst.ImageOutputTokens += usage.ImageOutputTokens
}

// ExtractEmbeddingsUsage reads input, output, and cache token counts from an Embeddings response.
func ExtractEmbeddingsUsage(body []byte) ForwardUsage {
	usage := gjson.GetBytes(body, "usage")
	if !usage.Exists() || !usage.IsObject() {
		return ForwardUsage{}
	}
	inputTokens := FirstPositiveGJSONInt(
		usage.Get("prompt_tokens"),
		usage.Get("input_tokens"),
		usage.Get("total_tokens"),
	)
	outputTokens := FirstPositiveGJSONInt(
		usage.Get("completion_tokens"),
		usage.Get("output_tokens"),
	)
	cacheReadTokens := OpenAICacheReadTokensFromUsage(usage)
	cacheCreationTokens := OpenAICacheCreationTokensFromUsage(usage)
	// Multimodal embeddings can report image tokens separately for image and text pricing.
	imageInputTokens := FirstPositiveGJSONInt(
		usage.Get("prompt_tokens_details.image_tokens"),
		usage.Get("input_tokens_details.image_tokens"),
	)
	return ForwardUsage{
		InputTokens:              inputTokens,
		ImageInputTokens:         imageInputTokens,
		OutputTokens:             outputTokens,
		CacheReadInputTokens:     cacheReadTokens,
		CacheCreationInputTokens: cacheCreationTokens,
	}
}

// MergeImageResponseUsage updates the aggregate with positive token counts from an image response.
func MergeImageResponseUsage(dst *ForwardUsage, body []byte) {
	if dst == nil {
		return
	}
	if parsed, ok := ExtractOpenAIUsageFromJSONBytes(body); ok {
		if parsed.InputTokens > 0 {
			dst.InputTokens = parsed.InputTokens
		}
		if parsed.OutputTokens > 0 {
			dst.OutputTokens = parsed.OutputTokens
		}
		if parsed.CacheReadInputTokens > 0 {
			dst.CacheReadInputTokens = parsed.CacheReadInputTokens
		}
		if parsed.ImageInputTokens > 0 {
			dst.ImageInputTokens = parsed.ImageInputTokens
		}
		if parsed.ImageOutputTokens > 0 {
			dst.ImageOutputTokens = parsed.ImageOutputTokens
		}
	}
}
