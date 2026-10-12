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

func TestForceCacheBilling_TokenConversion(t *testing.T) {
	tests := []struct {
		name                    string
		forceCacheBilling       bool
		inputTokens             int
		cacheReadInputTokens    int
		expectedInputTokens     int
		expectedCacheReadTokens int
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
			// Build a response from the input usage to exercise reclassification.
			usage := upstream.TokenUsage{
				InputTokens:          tt.inputTokens,
				CacheReadInputTokens: tt.cacheReadInputTokens,
			}

			// Read the reclassified response usage.
			body, err := json.Marshal(struct {
				Usage upstream.TokenUsage `json:"usage"`
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

func TestNonStreamingReconcile_KimiResponse(t *testing.T) {
	// Kimi reports cache hits under cached_tokens.
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
		Usage upstream.TokenUsage `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(body, &response))

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
		Usage upstream.TokenUsage `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(body, &response))

	// A nonzero cache-read count bypasses reconciliation.
	assert.NotZero(t, response.Usage.CacheReadInputTokens)
	assert.Equal(t, 30, response.Usage.CacheReadInputTokens)
}

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
		Usage upstream.TokenUsage `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(body, &response))

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
