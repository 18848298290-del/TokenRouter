package antigravity

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Structured Google RPC retries require matching status/reason observations
// and model metadata, with delay syntax parsed independently from HTTP
// error-code shape. Fixtures pin short/long/capacity decisions using the
// native boolean and normalize resource names, while exact internal-500
// matching remains separate.

// TestIsAntigravityInternalServerError compares the complete internal-500
// fixture with mismatched statuses, messages, codes, and body shapes. Only the
// asserted complete combination must select the provider penalty classifier.
func TestIsAntigravityInternalServerError(t *testing.T) {
	t.Run("匹配完整的 INTERNAL 500 body", func(t *testing.T) {
		body := []byte(`{"error":{"code":500,"message":"Internal error encountered.","status":"INTERNAL"}}`)
		require.True(t, IsAntigravityInternalServerError(500, body))
	})

	t.Run("statusCode 不是 500", func(t *testing.T) {
		body := []byte(`{"error":{"code":500,"message":"Internal error encountered.","status":"INTERNAL"}}`)
		require.False(t, IsAntigravityInternalServerError(429, body))
		require.False(t, IsAntigravityInternalServerError(503, body))
		require.False(t, IsAntigravityInternalServerError(200, body))
	})

	t.Run("body 中 message 不匹配", func(t *testing.T) {
		body := []byte(`{"error":{"code":500,"message":"Some other error","status":"INTERNAL"}}`)
		require.False(t, IsAntigravityInternalServerError(500, body))
	})

	t.Run("body 中 status 不匹配", func(t *testing.T) {
		body := []byte(`{"error":{"code":500,"message":"Internal error encountered.","status":"UNAVAILABLE"}}`)
		require.False(t, IsAntigravityInternalServerError(500, body))
	})

	t.Run("body 中 code 不匹配", func(t *testing.T) {
		body := []byte(`{"error":{"code":503,"message":"Internal error encountered.","status":"INTERNAL"}}`)
		require.False(t, IsAntigravityInternalServerError(500, body))
	})

	t.Run("空 body", func(t *testing.T) {
		require.False(t, IsAntigravityInternalServerError(500, []byte{}))
		require.False(t, IsAntigravityInternalServerError(500, nil))
	})

	t.Run("其他 500 错误格式（纯文本）", func(t *testing.T) {
		body := []byte(`Internal Server Error`)
		require.False(t, IsAntigravityInternalServerError(500, body))
	})

	t.Run("其他 500 错误格式（不同 JSON 结构）", func(t *testing.T) {
		body := []byte(`{"message":"Internal Server Error","statusCode":500}`)
		require.False(t, IsAntigravityInternalServerError(500, body))
	})
}

// TestParseAntigravitySmartRetryInfo compares complete RPC limits with missing
// reason/status/model and invalid bodies. Parsed fixtures must retain selected
// delays, normalized model identity, and capacity observations as asserted.
func TestParseAntigravitySmartRetryInfo(t *testing.T) {
	tests := []struct {
		name                             string        // human-readable table case name
		body                             string        // encoded supplier error fixture
		expectedDelay                    time.Duration // expected parsed retry duration
		expectedModel                    string        // expected normalized model identity
		expectedNil                      bool          // expected absent parsing result
		expectedIsModelCapacityExhausted bool          // expected accumulated capacity observation
	}{
		{
			name: "valid complete response with RATE_LIMIT_EXCEEDED",
			body: `{
				"error": {
					"code": 429,
					"details": [
						{
							"@type": "type.googleapis.com/google.rpc.ErrorInfo",
							"domain": "cloudcode-pa.googleapis.com",
							"metadata": {
								"model": "claude-sonnet-4-5",
								"quotaResetDelay": "201.506475ms"
							},
							"reason": "RATE_LIMIT_EXCEEDED"
						},
						{
							"@type": "type.googleapis.com/google.rpc.RetryInfo",
							"retryDelay": "0.201506475s"
						}
					],
					"message": "You have exhausted your capacity on this model.",
					"status": "RESOURCE_EXHAUSTED"
				}
			}`,
			expectedDelay: 201506475 * time.Nanosecond,
			expectedModel: "claude-sonnet-4-5",
		},
		{
			name: "429 RESOURCE_EXHAUSTED without RATE_LIMIT_EXCEEDED - should return nil",
			body: `{
				"error": {
					"code": 429,
					"status": "RESOURCE_EXHAUSTED",
					"details": [
						{
							"@type": "type.googleapis.com/google.rpc.ErrorInfo",
							"metadata": {"model": "claude-sonnet-4-5"},
							"reason": "QUOTA_EXCEEDED"
						},
						{
							"@type": "type.googleapis.com/google.rpc.RetryInfo",
							"retryDelay": "3s"
						}
					]
				}
			}`,
			expectedNil: true,
		},
		{
			name: "503 UNAVAILABLE with MODEL_CAPACITY_EXHAUSTED - long delay",
			body: `{
				"error": {
					"code": 503,
					"status": "UNAVAILABLE",
					"details": [
						{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "gemini-3-pro-high"}, "reason": "MODEL_CAPACITY_EXHAUSTED"},
						{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "39s"}
					],
					"message": "No capacity available for model gemini-3-pro-high on the server"
				}
			}`,
			expectedDelay:                    39 * time.Second,
			expectedModel:                    "gemini-3-pro-high",
			expectedIsModelCapacityExhausted: true,
		},
		{
			name: "503 UNAVAILABLE without MODEL_CAPACITY_EXHAUSTED - should return nil",
			body: `{
				"error": {
					"code": 503,
					"status": "UNAVAILABLE",
					"details": [
						{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "gemini-pro"}, "reason": "SERVICE_UNAVAILABLE"},
						{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "5s"}
					]
				}
			}`,
			expectedNil: true,
		},
		{
			name: "wrong status - should return nil",
			body: `{
				"error": {
					"code": 429,
					"status": "INVALID_ARGUMENT",
					"details": [
						{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "3s"}
					]
				}
			}`,
			expectedNil: true,
		},
		{
			name: "missing status - should return nil",
			body: `{
				"error": {
					"code": 429,
					"details": [
						{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "3s"}
					]
				}
			}`,
			expectedNil: true,
		},
		{
			name: "milliseconds format is now supported",
			body: `{
				"error": {
					"code": 429,
					"status": "RESOURCE_EXHAUSTED",
					"details": [
						{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "test-model"}, "reason": "RATE_LIMIT_EXCEEDED"},
						{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "500ms"}
					]
				}
			}`,
			expectedDelay: 500 * time.Millisecond,
			expectedModel: "test-model",
		},
		{
			name: "minutes format is supported",
			body: `{
				"error": {
					"code": 429,
					"status": "RESOURCE_EXHAUSTED",
					"details": [
						{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "gemini-3-pro"}, "reason": "RATE_LIMIT_EXCEEDED"},
						{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "4m50s"}
					]
				}
			}`,
			expectedDelay: 4*time.Minute + 50*time.Second,
			expectedModel: "gemini-3-pro",
		},
		{
			name: "missing model name - should return nil",
			body: `{
				"error": {
					"code": 429,
					"status": "RESOURCE_EXHAUSTED",
					"details": [
						{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "RATE_LIMIT_EXCEEDED"},
						{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "3s"}
					]
				}
			}`,
			expectedNil: true,
		},
		{
			name:        "invalid JSON",
			body:        `not json`,
			expectedNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseAntigravitySmartRetryInfo([]byte(tt.body))
			if tt.expectedNil {
				if result != nil {
					t.Errorf("expected nil, got %+v", result)
				}
				return
			}
			if result == nil {
				t.Errorf("expected non-nil result")
				return
			}
			if result.RetryDelay != tt.expectedDelay {
				t.Errorf("RetryDelay = %v, want %v", result.RetryDelay, tt.expectedDelay)
			}
			if result.ModelName != tt.expectedModel {
				t.Errorf("ModelName = %q, want %q", result.ModelName, tt.expectedModel)
			}
			if result.IsModelCapacityExhausted != tt.expectedIsModelCapacityExhausted {
				t.Errorf("IsModelCapacityExhausted = %v, want %v", result.IsModelCapacityExhausted, tt.expectedIsModelCapacityExhausted)
			}
		})
	}
}

// TestShouldTriggerAntigravitySmartRetry selects policy using the native
// boolean despite historical provider labels in fixture names. Short, long,
// missing-delay, and capacity bodies must choose their configured retry or
// model-limit observations.
func TestShouldTriggerAntigravitySmartRetry(t *testing.T) {
	oauthProvider := true
	setupTokenProvider := true
	upstreamProvider := true
	apiKeyProvider := false

	tests := []struct {
		name                             string        // human-readable table case name
		provider                         bool          // input native-retry eligibility flag
		body                             string        // encoded supplier error fixture
		expectedShouldRetry              bool          // expected same-provider retry selection
		expectedShouldRateLimit          bool          // expected model-limit selection result
		expectedIsModelCapacityExhausted bool          // expected accumulated capacity observation
		minWait                          time.Duration // minimum expected selected wait
		modelName                        string        // expected selected model identity
	}{
		{
			name:     "OAuth provider with short delay (< 7s) - smart retry",
			provider: oauthProvider,
			body: `{
				"error": {
					"status": "RESOURCE_EXHAUSTED",
					"details": [
						{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "claude-opus-4"}, "reason": "RATE_LIMIT_EXCEEDED"},
						{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "0.5s"}
					]
				}
			}`,
			expectedShouldRetry:     true,
			expectedShouldRateLimit: false,
			minWait:                 1 * time.Second, // a 0.5-second delay is raised to the one-second minimum
			modelName:               "claude-opus-4",
		},
		{
			name:     "SetupToken provider with short delay - smart retry",
			provider: setupTokenProvider,
			body: `{
				"error": {
					"status": "RESOURCE_EXHAUSTED",
					"details": [
						{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "gemini-3-flash"}, "reason": "RATE_LIMIT_EXCEEDED"},
						{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "3s"}
					]
				}
			}`,
			expectedShouldRetry:     true,
			expectedShouldRateLimit: false,
			minWait:                 3 * time.Second,
			modelName:               "gemini-3-flash",
		},
		{
			name:     "OAuth provider with long delay (>= 7s) - direct rate limit",
			provider: oauthProvider,
			body: `{
				"error": {
					"status": "RESOURCE_EXHAUSTED",
					"details": [
						{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "claude-sonnet-4-5"}, "reason": "RATE_LIMIT_EXCEEDED"},
						{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "15s"}
					]
				}
			}`,
			expectedShouldRetry:     false,
			expectedShouldRateLimit: true,
			modelName:               "claude-sonnet-4-5",
		},
		{
			name:     "Upstream provider with short delay - smart retry",
			provider: upstreamProvider,
			body: `{
				"error": {
					"status": "RESOURCE_EXHAUSTED",
					"details": [
						{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "claude-sonnet-4-5"}, "reason": "RATE_LIMIT_EXCEEDED"},
						{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "2s"}
					]
				}
			}`,
			expectedShouldRetry:     true,
			expectedShouldRateLimit: false,
			minWait:                 2 * time.Second,
			modelName:               "claude-sonnet-4-5",
		},
		{
			name:     "API Key provider - should not trigger",
			provider: apiKeyProvider,
			body: `{
				"error": {
					"status": "RESOURCE_EXHAUSTED",
					"details": [
						{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "test"}, "reason": "RATE_LIMIT_EXCEEDED"},
						{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "0.5s"}
					]
				}
			}`,
			expectedShouldRetry:     false,
			expectedShouldRateLimit: false,
		},
		{
			name:     "OAuth provider with exactly 7s delay - direct rate limit",
			provider: oauthProvider,
			body: `{
				"error": {
					"status": "RESOURCE_EXHAUSTED",
					"details": [
						{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "gemini-pro"}, "reason": "RATE_LIMIT_EXCEEDED"},
						{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "7s"}
					]
				}
			}`,
			expectedShouldRetry:     false,
			expectedShouldRateLimit: true,
			minWait:                 7 * time.Second,
			modelName:               "gemini-pro",
		},
		{
			name:     "503 UNAVAILABLE with MODEL_CAPACITY_EXHAUSTED - long delay",
			provider: oauthProvider,
			body: `{
				"error": {
					"code": 503,
					"status": "UNAVAILABLE",
					"details": [
						{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "gemini-3-pro-high"}, "reason": "MODEL_CAPACITY_EXHAUSTED"},
						{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "39s"}
					]
				}
			}`,
			expectedShouldRetry:              true,
			expectedShouldRateLimit:          false,
			expectedIsModelCapacityExhausted: true,
			minWait:                          1 * time.Second,
			modelName:                        "gemini-3-pro-high",
		},
		{
			name:     "503 UNAVAILABLE with MODEL_CAPACITY_EXHAUSTED - no retryDelay - use fixed wait",
			provider: oauthProvider,
			body: `{
				"error": {
					"code": 503,
					"status": "UNAVAILABLE",
					"details": [
						{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "gemini-2.5-flash"}, "reason": "MODEL_CAPACITY_EXHAUSTED"}
					],
					"message": "No capacity available for model gemini-2.5-flash on the server"
				}
			}`,
			expectedShouldRetry:              true,
			expectedShouldRateLimit:          false,
			expectedIsModelCapacityExhausted: true,
			minWait:                          1 * time.Second,
			modelName:                        "gemini-2.5-flash",
		},
		{
			name:     "429 RESOURCE_EXHAUSTED with RATE_LIMIT_EXCEEDED - no retryDelay - use default rate limit",
			provider: oauthProvider,
			body: `{
				"error": {
					"code": 429,
					"status": "RESOURCE_EXHAUSTED",
					"details": [
						{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "claude-sonnet-4-5"}, "reason": "RATE_LIMIT_EXCEEDED"}
					],
					"message": "You have exhausted your capacity on this model."
				}
			}`,
			expectedShouldRetry:     false,
			expectedShouldRateLimit: true,
			minWait:                 30 * time.Second,
			modelName:               "claude-sonnet-4-5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shouldRetry, shouldRateLimit, wait, model, isModelCapacityExhausted := ShouldTriggerAntigravitySmartRetry(tt.provider, []byte(tt.body))
			if shouldRetry != tt.expectedShouldRetry {
				t.Errorf("shouldRetry = %v, want %v", shouldRetry, tt.expectedShouldRetry)
			}
			if shouldRateLimit != tt.expectedShouldRateLimit {
				t.Errorf("shouldRateLimit = %v, want %v", shouldRateLimit, tt.expectedShouldRateLimit)
			}
			if isModelCapacityExhausted != tt.expectedIsModelCapacityExhausted {
				t.Errorf("isModelCapacityExhausted = %v, want %v", isModelCapacityExhausted, tt.expectedIsModelCapacityExhausted)
			}
			if shouldRetry {
				if wait < tt.minWait {
					t.Errorf("wait = %v, want >= %v", wait, tt.minWait)
				}
			}
			if shouldRateLimit && tt.minWait > 0 {
				if wait < tt.minWait {
					t.Errorf("rate limit wait = %v, want >= %v", wait, tt.minWait)
				}
			}
			if (shouldRetry || shouldRateLimit) && model != tt.modelName {
				t.Errorf("modelName = %q, want %q", model, tt.modelName)
			}
		})
	}
}

// TestNormalizeAntigravityModelName compares plain, prefixed,
// publisher-resource, and mixed-case names. Each fixture must return its
// expected lowercase model identity after supported resource-prefix removal
// and trimming.
func TestNormalizeAntigravityModelName(t *testing.T) {
	tests := []struct {
		name     string // human-readable table case name
		model    string // input supplied model spelling
		expected string // expected normalized model spelling
	}{
		{
			name:     "plain model name",
			model:    "gemini-1.5-pro",
			expected: "gemini-1.5-pro",
		},
		{
			name:     "models/ prefix",
			model:    "models/gemini-1.5-pro",
			expected: "gemini-1.5-pro",
		},
		{
			name:     "publishers/google/models/ prefix",
			model:    "publishers/google/models/gemini-1.5-pro",
			expected: "gemini-1.5-pro",
		},
		{
			name:     "projects/.../publishers/google/models/ path",
			model:    "projects/my-proj/locations/us-central1/publishers/google/models/gemini-2.5-flash",
			expected: "gemini-2.5-flash",
		},
		{
			name:     "publishers/anthropic/models/ prefix",
			model:    "publishers/anthropic/models/claude-sonnet-4-5",
			expected: "claude-sonnet-4-5",
		},
		{
			name:     "projects/.../publishers/anthropic/models/ path",
			model:    "projects/my-proj/locations/global/publishers/anthropic/models/claude-sonnet-4-5",
			expected: "claude-sonnet-4-5",
		},
		{
			name:     "mixed case and spaces",
			model:    "  Models/Gemini-1.5-Pro  ",
			expected: "gemini-1.5-pro",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := NormalizeAntigravityModelName(tt.model)
			require.Equal(t, tt.expected, actual)
		})
	}
}
