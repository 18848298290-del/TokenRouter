package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Buffered usage fixtures can expose a canonical cache-read count or a
// compatible cached_tokens alias. These tests check JSON presentation after
// input reclassification and the inlined reconciliation cases, distinguishing
// those helper assertions from full response-reader execution.

// TestForceCacheBilling_TokenConversion checks helper-level downstream usage
// presentation for enabled, disabled, and zero-input cases. It reparses the
// edited JSON and asserts both input and cache-read counters against the table.
func TestForceCacheBilling_TokenConversion(t *testing.T) {
	tests := []struct {
		name              string // table scenario display name
		forceCacheBilling bool   // enable input reclassification path
		// inputTokens and cacheReadInputTokens supply the upstream counters.
		// When forceCacheBilling is true and inputTokens is positive,
		// expectedInputTokens is zero and expectedCacheReadTokens is their sum;
		// other cases retain the supplied counters.
		inputTokens             int // initial ordinary input count
		cacheReadInputTokens    int // initial cache-read token count
		expectedInputTokens     int // expected reclassified input count
		expectedCacheReadTokens int // expected reclassified cache-read count
	}{
		{
			name:                    "force cache billing converts input to cache_read",
			forceCacheBilling:       true,
			inputTokens:             1000,
			cacheReadInputTokens:    500,
			expectedInputTokens:     0,
			expectedCacheReadTokens: 1500, // 500 + 1000
		},
		{
			name:                    "no force cache billing keeps tokens unchanged",
			forceCacheBilling:       false,
			inputTokens:             1000,
			cacheReadInputTokens:    500,
			expectedInputTokens:     1000,
			expectedCacheReadTokens: 500,
		},
		{
			name:                    "force cache billing with zero input tokens does nothing",
			forceCacheBilling:       true,
			inputTokens:             0,
			cacheReadInputTokens:    500,
			expectedInputTokens:     0,
			expectedCacheReadTokens: 500,
		},
		{
			name:                    "force cache billing with zero cache_read tokens",
			forceCacheBilling:       true,
			inputTokens:             1000,
			cacheReadInputTokens:    0,
			expectedInputTokens:     0,
			expectedCacheReadTokens: 1000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build a response from the input usage to exercise
			// reclassification.
			usage := upstream.TokenUsage{
				InputTokens:          tt.inputTokens,
				CacheReadInputTokens: tt.cacheReadInputTokens,
			}

			// Read the reclassified response usage.
			body, err := json.Marshal(struct {
				Usage upstream.TokenUsage `json:"usage"` // serialized response token usage
			}{Usage: usage})
			if err != nil {
				t.Fatal(err)
			}
			if tt.forceCacheBilling && usage.InputTokens > 0 {
				body, err = ClassifyResponseInputAsCacheRead(body, &usage)
				if err != nil {
					t.Fatal(err)
				}
			}
			usage = *anthropic.ParseClaudeUsageFromResponseBody(body)

			if usage.InputTokens != tt.expectedInputTokens {
				t.Errorf("InputTokens = %d, want %d", usage.InputTokens, tt.expectedInputTokens)
			}
			if usage.CacheReadInputTokens != tt.expectedCacheReadTokens {
				t.Errorf("CacheReadInputTokens = %d, want %d", usage.CacheReadInputTokens, tt.expectedCacheReadTokens)
			}
		})
	}
}

// TestNonStreamingReconcile_KimiResponse decodes a compatible cached_tokens
// fixture and performs the inlined nonstream reconciliation. It checks local
// input/output/cache-read counts and the added canonical cache-read JSON field.
func TestNonStreamingReconcile_KimiResponse(t *testing.T) {
	// This compatible fixture supplies cache hits under cached_tokens.
	body := []byte(`{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "hello"}],
		"model": "kimi",
		"usage": {
			"input_tokens": 23,
			"output_tokens": 7,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens": 0,
			"cached_tokens": 23,
			"prompt_tokens": 23,
			"completion_tokens": 7
		}
	}`)

	// Exercise the nonstream cache-read reconciliation.
	var response struct {
		Usage upstream.TokenUsage `json:"usage"` // serialized response token usage
	}
	require.NoError(t, json.Unmarshal(body, &response))

	// The decoded fixture has no canonical cache-read count. A positive alias
	// can fill that selected usage and client path; an unsuccessful path edit
	// retains the prior response bytes.
	if response.Usage.CacheReadInputTokens == 0 {
		cachedTokens := gjson.GetBytes(body, "usage.cached_tokens").Int()
		if cachedTokens > 0 {
			response.Usage.CacheReadInputTokens = int(cachedTokens)
			if newBody, err := sjson.SetBytes(body, "usage.cache_read_input_tokens", cachedTokens); err == nil {
				body = newBody
			}
		}
	}

	// Check internal billing usage.
	assert.Equal(t, 23, response.Usage.CacheReadInputTokens)
	assert.Equal(t, 23, response.Usage.InputTokens)
	assert.Equal(t, 7, response.Usage.OutputTokens)

	// Check the client-visible JSON.
	assert.Equal(t, int64(23), gjson.GetBytes(body, "usage.cache_read_input_tokens").Int())
}

// TestNonStreamingReconcile_NativeClaude decodes a fixture containing a nonzero
// canonical cache-read count. It checks that decoded value is present and
// equals thirty, protecting the native-count precondition for reconciliation.
func TestNonStreamingReconcile_NativeClaude(t *testing.T) {
	// An existing Anthropic cache-read count takes precedence.
	body := []byte(`{
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cache_creation_input_tokens": 20,
			"cache_read_input_tokens": 30
		}
	}`)

	var response struct {
		Usage upstream.TokenUsage `json:"usage"` // serialized response token usage
	}
	require.NoError(t, json.Unmarshal(body, &response))

	// These assertions inspect the decoded native count directly, supplying
	// the nonzero precondition used by the response reader's reconciliation
	// branch.
	assert.NotZero(t, response.Usage.CacheReadInputTokens)
	assert.Equal(t, 30, response.Usage.CacheReadInputTokens)
}

// TestNonStreamingReconcile_NoCachedTokens applies the inlined reconciliation
// to a fixture lacking the compatible alias. It checks local and client-path
// cache-read counts stay zero for that response.
func TestNonStreamingReconcile_NoCachedTokens(t *testing.T) {
	// A response without cached_tokens keeps zero cache-read usage.
	body := []byte(`{
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens": 0
		}
	}`)

	var response struct {
		Usage upstream.TokenUsage `json:"usage"` // serialized response token usage
	}
	require.NoError(t, json.Unmarshal(body, &response))

	// The decoded fixture has no canonical cache-read count. A positive alias
	// can fill that selected usage and client path; an unsuccessful path edit
	// retains the prior response bytes.
	if response.Usage.CacheReadInputTokens == 0 {
		cachedTokens := gjson.GetBytes(body, "usage.cached_tokens").Int()
		if cachedTokens > 0 {
			response.Usage.CacheReadInputTokens = int(cachedTokens)
			if newBody, err := sjson.SetBytes(body, "usage.cache_read_input_tokens", cachedTokens); err == nil {
				body = newBody
			}
		}
	}

	assert.Equal(t, 0, response.Usage.CacheReadInputTokens)
	assert.Equal(t, int64(0), gjson.GetBytes(body, "usage.cache_read_input_tokens").Int())
}
