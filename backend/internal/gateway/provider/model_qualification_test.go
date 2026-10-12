package provider

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	protocolcore "github.com/TokenFlux/TokenRouter/internal/protocol"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

func TestProviderIsSchedulableForModel_AntigravityRateLimits(t *testing.T) {
	now := time.Now()
	future := now.Add(10 * time.Minute)

	provider := &providercore.Record{
		ID:          1,
		Name:        "acc",
		Platform:    capability.PlatformAntigravity,
		Status:      billing.StatusActive,
		Schedulable: true,
	}

	provider.RateLimitResetAt = &future
	require.False(t, (ModelPolicy{Record: provider}).Schedulable(context.Background(), "claude-sonnet-4-5"))
	require.False(t, (ModelPolicy{Record: provider}).Schedulable(context.Background(), "gemini-3-flash"))

	provider.RateLimitResetAt = nil
	require.True(t, (ModelPolicy{Record: provider}).Schedulable(context.Background(), "claude-sonnet-4-5"))
	require.True(t, (ModelPolicy{Record: provider}).Schedulable(context.Background(), "gemini-3-flash"))
}

// TestProtocolNativeMatrixAndSave checks the shared protocol matrix, empty protocol sets, and platform rejections.
func TestProtocolNativeMatrixAndSave(t *testing.T) {
	for _, tc := range []struct {
		platform, kind, auth string
		count                int
	}{
		{capability.PlatformAnthropic, capability.ProviderTypeAPIKey, "", 1},
		{capability.PlatformAnthropic, capability.ProviderTypeBedrock, "", 1},
		{capability.PlatformOpenAI, capability.ProviderTypeAPIKey, "", 8},
		{capability.PlatformOpenAI, capability.ProviderTypeOAuth, "", 5},
		{capability.PlatformOpenAI, capability.ProviderTypeOAuth, providercore.OpenAIAuthModePersonalAccessToken, 3},
		{capability.PlatformOpenAI, capability.ProviderTypeOAuth, providercore.OpenAIAuthModeAgentIdentity, 4},
		{capability.PlatformDeepseek, capability.ProviderTypeAPIKey, "", 3},
		{capability.PlatformKimi, capability.ProviderTypeAPIKey, "", 3},
		{capability.PlatformZhipu, capability.ProviderTypeAPIKey, "", 2},
		{capability.PlatformGemini, capability.ProviderTypeAPIKey, "", 2},
		{capability.PlatformGemini, capability.ProviderTypeServiceAccount, "", 2},
		{capability.PlatformGemini, capability.ProviderTypeOAuth, "", 1},
		{capability.PlatformAntigravity, capability.ProviderTypeOAuth, "", 1},
		{capability.PlatformAntigravity, capability.ProviderTypeAPIKey, "", 0},
		{capability.PlatformGrok, capability.ProviderTypeAPIKey, "", 11},
		{capability.PlatformGrok, capability.ProviderTypeOAuth, "", 11},
		{capability.PlatformQoder, capability.ProviderTypeCosy, "", 1},
	} {
		t.Run(tc.platform+"/"+tc.kind+"/"+tc.auth, func(t *testing.T) {
			provider := &providercore.Record{Platform: tc.platform, Type: tc.kind, Credentials: map[string]any{"auth_mode": tc.auth}}
			options := provider.NativeProtocolOptions()
			require.Len(t, options, tc.count)
			for _, protocol := range options {
				provider.Credentials[providercore.UpstreamProtocolsKey] = []string{string(protocol)}
				require.NoError(t, providercore.NormalizeProviderProtocols(provider))
				require.Equal(t, []protocolcore.ProtocolID{protocol}, provider.UpstreamProtocols())
				target, ok := (ModelPolicy{Record: provider}).ProtocolRoute(nil, protocol)
				require.True(t, ok)
				require.Equal(t, protocol, target)
			}
			provider.Credentials[providercore.UpstreamProtocolsKey] = []string{}
			require.NoError(t, providercore.NormalizeProviderProtocols(provider))
			require.Empty(t, provider.UpstreamProtocols())
			provider.Credentials[providercore.UpstreamProtocolsKey] = []string{"unknown"}
			require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(providercore.NormalizeProviderProtocols(provider)))
		})
	}
}

func TestIsModelRateLimited(t *testing.T) {
	now := time.Now()
	future := now.Add(10 * time.Minute).Format(time.RFC3339)
	past := now.Add(-10 * time.Minute).Format(time.RFC3339)

	tests := []struct {
		name           string
		provider       *providercore.Record
		requestedModel string
		expected       bool
	}{
		{
			name: "official model ID hit - claude-sonnet-4-5",
			provider: &providercore.Record{
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"claude-sonnet-4-5": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "claude-sonnet-4-5",
			expected:       true,
		},
		{
			name: "official model ID hit via mapping - request claude-3-5-sonnet, mapped to claude-sonnet-4-5",
			provider: &providercore.Record{
				Credentials: map[string]any{
					"model_mapping": map[string]any{
						"claude-3-5-sonnet": "claude-sonnet-4-5",
					},
				},
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"claude-sonnet-4-5": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "claude-3-5-sonnet",
			expected:       true,
		},
		{
			name: "no rate limit - expired",
			provider: &providercore.Record{
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"claude-sonnet-4-5": map[string]any{
							"rate_limit_reset_at": past,
						},
					},
				},
			},
			requestedModel: "claude-sonnet-4-5",
			expected:       false,
		},
		{
			name: "no rate limit - no matching key",
			provider: &providercore.Record{
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"gemini-3-flash": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "claude-sonnet-4-5",
			expected:       false,
		},
		{
			name:           "no rate limit - unsupported model",
			provider:       &providercore.Record{},
			requestedModel: "gpt-4",
			expected:       false,
		},
		{
			name:           "no rate limit - empty model",
			provider:       &providercore.Record{},
			requestedModel: "",
			expected:       false,
		},
		{
			name: "gemini model hit",
			provider: &providercore.Record{
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"gemini-3-pro-high": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "gemini-3-pro-high",
			expected:       true,
		},
		{
			name: "antigravity platform - gemini-3-pro-preview mapped to gemini-3-pro-high",
			provider: &providercore.Record{
				Platform: capability.PlatformAntigravity,
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"gemini-3-pro-high": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "gemini-3-pro-preview",
			expected:       false,
		},
		{
			name: "antigravity platform - gemini family rate limit blocks mapped preview",
			provider: &providercore.Record{
				Platform: capability.PlatformAntigravity,
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"antigravity:gemini": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "gemini-3-pro-preview",
			expected:       true,
		},
		{
			name: "antigravity platform - gemini family rate limit does not block claude",
			provider: &providercore.Record{
				Platform: capability.PlatformAntigravity,
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"antigravity:gemini": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "claude-sonnet-4-5",
			expected:       false,
		},
		{
			name: "non-antigravity platform - gemini-3-pro-preview NOT mapped",
			provider: &providercore.Record{
				Platform: capability.PlatformGemini,
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"gemini-3-pro-high": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "gemini-3-pro-preview",
			expected:       false, // Gemini does not apply Antigravity model mapping.
		},
		{
			name: "antigravity platform - claude-opus-4-5-thinking mapped to opus-4-6-thinking",
			provider: &providercore.Record{
				Platform: capability.PlatformAntigravity,
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"claude-opus-4-6-thinking": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "claude-opus-4-5-thinking",
			expected:       false,
		},
		{
			name: "no scope fallback - claude_sonnet should not match",
			provider: &providercore.Record{
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"claude_sonnet": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "claude-3-5-sonnet-20241022",
			expected:       false,
		},
		{
			name: "openai image generation family key blocks image model",
			provider: &providercore.Record{
				Platform: capability.PlatformOpenAI,
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						providercore.OpenAIImageGenerationRateLimitKey: map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "gpt-image-2",
			expected:       true,
		},
		{
			name: "openai image generation family key does not block text model",
			provider: &providercore.Record{
				Platform: capability.PlatformOpenAI,
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						providercore.OpenAIImageGenerationRateLimitKey: map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "gpt-5.4",
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := (ModelPolicy{Record: tt.provider}).Limited(context.Background(), tt.requestedModel)
			if result != tt.expected {
				t.Errorf("isModelRateLimited(%q) = %v, want %v", tt.requestedModel, result, tt.expected)
			}
		})
	}
}

func TestIsModelRateLimited_OpenAIImageGenerationIntentBlocksTextModelImageTool(t *testing.T) {
	future := time.Now().Add(10 * time.Minute).Format(time.RFC3339)
	provider := &providercore.Record{
		Platform: capability.PlatformOpenAI,
		Extra: map[string]any{
			"model_rate_limits": map[string]any{
				providercore.OpenAIImageGenerationRateLimitKey: map[string]any{
					"rate_limit_reset_at": future,
				},
			},
		},
	}

	require.False(t, (ModelPolicy{Record: provider}).Limited(context.Background(), "gpt-5.4"))
	require.True(t, (ModelPolicy{Record: provider}).Limited(requeststate.WithOpenAIImageGenerationIntent(context.Background()), "gpt-5.4"))
}

func TestIsModelRateLimited_Antigravity_ThinkingAffectsModelKey(t *testing.T) {
	now := time.Now()
	future := now.Add(10 * time.Minute).Format(time.RFC3339)

	provider := &providercore.Record{
		Platform: capability.PlatformAntigravity,
		Extra: map[string]any{
			"model_rate_limits": map[string]any{
				"claude-sonnet-4-5-thinking": map[string]any{
					"rate_limit_reset_at": future,
				},
			},
		},
	}

	ctx := requeststate.WithThinkingEnabled(context.Background(), true)
	if !(ModelPolicy{Record: provider}).Limited(ctx, "claude-sonnet-4-5") {
		t.Errorf("expected model to be rate limited")
	}
}

func TestGetModelRateLimitRemainingTime(t *testing.T) {
	now := time.Now()
	future10m := now.Add(10 * time.Minute).Format(time.RFC3339)
	future5m := now.Add(5 * time.Minute).Format(time.RFC3339)
	past := now.Add(-10 * time.Minute).Format(time.RFC3339)

	tests := []struct {
		name           string
		provider       *providercore.Record
		requestedModel string
		minExpected    time.Duration
		maxExpected    time.Duration
	}{
		{
			name:           "nil provider",
			provider:       nil,
			requestedModel: "claude-sonnet-4-5",
			minExpected:    0,
			maxExpected:    0,
		},
		{
			name: "model rate limited - direct hit",
			provider: &providercore.Record{
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"claude-sonnet-4-5": map[string]any{
							"rate_limit_reset_at": future10m,
						},
					},
				},
			},
			requestedModel: "claude-sonnet-4-5",
			minExpected:    9 * time.Minute,
			maxExpected:    11 * time.Minute,
		},
		{
			name: "model rate limited - via mapping",
			provider: &providercore.Record{
				Credentials: map[string]any{
					"model_mapping": map[string]any{
						"claude-3-5-sonnet": "claude-sonnet-4-5",
					},
				},
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"claude-sonnet-4-5": map[string]any{
							"rate_limit_reset_at": future5m,
						},
					},
				},
			},
			requestedModel: "claude-3-5-sonnet",
			minExpected:    4 * time.Minute,
			maxExpected:    6 * time.Minute,
		},
		{
			name: "expired rate limit",
			provider: &providercore.Record{
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"claude-sonnet-4-5": map[string]any{
							"rate_limit_reset_at": past,
						},
					},
				},
			},
			requestedModel: "claude-sonnet-4-5",
			minExpected:    0,
			maxExpected:    0,
		},
		{
			name:           "no rate limit data",
			provider:       &providercore.Record{},
			requestedModel: "claude-sonnet-4-5",
			minExpected:    0,
			maxExpected:    0,
		},
		{
			name: "no scope fallback",
			provider: &providercore.Record{
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"claude_sonnet": map[string]any{
							"rate_limit_reset_at": future5m,
						},
					},
				},
			},
			requestedModel: "claude-3-5-sonnet-20241022",
			minExpected:    0,
			maxExpected:    0,
		},
		{
			name: "antigravity platform - claude-opus-4-5-thinking mapped to opus-4-6-thinking",
			provider: &providercore.Record{
				Platform: capability.PlatformAntigravity,
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"claude-opus-4-6-thinking": map[string]any{
							"rate_limit_reset_at": future5m,
						},
					},
				},
			},
			requestedModel: "claude-opus-4-5-thinking",
			minExpected:    0,
			maxExpected:    0,
		},
		{
			name: "antigravity platform - gemini family rate limit remaining",
			provider: &providercore.Record{
				Platform: capability.PlatformAntigravity,
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"antigravity:gemini": map[string]any{
							"rate_limit_reset_at": future10m,
						},
					},
				},
			},
			requestedModel: "gemini-3-pro-high",
			minExpected:    9 * time.Minute,
			maxExpected:    11 * time.Minute,
		},
		{
			name: "antigravity platform - gemini family remaining ignored for claude",
			provider: &providercore.Record{
				Platform: capability.PlatformAntigravity,
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"antigravity:gemini": map[string]any{
							"rate_limit_reset_at": future10m,
						},
					},
				},
			},
			requestedModel: "claude-sonnet-4-5",
			minExpected:    0,
			maxExpected:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := (ModelPolicy{Record: tt.provider}).LimitRemaining(context.Background(), tt.requestedModel)
			if result < tt.minExpected || result > tt.maxExpected {
				t.Errorf("GetModelRateLimitRemainingTime() = %v, want between %v and %v", result, tt.minExpected, tt.maxExpected)
			}
		})
	}
}

func TestGetRateLimitRemainingTime(t *testing.T) {
	now := time.Now()
	future15m := now.Add(15 * time.Minute).Format(time.RFC3339)
	future5m := now.Add(5 * time.Minute).Format(time.RFC3339)

	tests := []struct {
		name           string
		provider       *providercore.Record
		requestedModel string
		minExpected    time.Duration
		maxExpected    time.Duration
	}{
		{
			name:           "nil provider",
			provider:       nil,
			requestedModel: "claude-sonnet-4-5",
			minExpected:    0,
			maxExpected:    0,
		},
		{
			name: "model rate limited - 15 minutes",
			provider: &providercore.Record{
				Platform: capability.PlatformAntigravity,
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"claude-sonnet-4-5": map[string]any{
							"rate_limit_reset_at": future15m,
						},
					},
				},
			},
			requestedModel: "claude-sonnet-4-5",
			minExpected:    14 * time.Minute,
			maxExpected:    16 * time.Minute,
		},
		{
			name: "only model rate limited",
			provider: &providercore.Record{
				Platform: capability.PlatformAntigravity,
				Extra: map[string]any{
					"model_rate_limits": map[string]any{
						"claude-sonnet-4-5": map[string]any{
							"rate_limit_reset_at": future5m,
						},
					},
				},
			},
			requestedModel: "claude-sonnet-4-5",
			minExpected:    4 * time.Minute,
			maxExpected:    6 * time.Minute,
		},
		{
			name: "neither rate limited",
			provider: &providercore.Record{
				Platform: capability.PlatformAntigravity,
			},
			requestedModel: "claude-sonnet-4-5",
			minExpected:    0,
			maxExpected:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := (ModelPolicy{Record: tt.provider}).LimitRemaining(context.Background(), tt.requestedModel)
			if result < tt.minExpected || result > tt.maxExpected {
				t.Errorf("GetRateLimitRemainingTime() = %v, want between %v and %v", result, tt.minExpected, tt.maxExpected)
			}
		})
	}
}

func TestIsModelRateLimited_AnthropicFableFamilyKey(t *testing.T) {
	now := time.Now()
	future := now.Add(48 * time.Hour).Format(time.RFC3339)

	provider := &providercore.Record{
		Platform: capability.PlatformAnthropic,
		Extra: map[string]any{
			"model_rate_limits": map[string]any{
				providercore.AnthropicFableRateLimitKey: map[string]any{
					"rate_limit_reset_at": future,
				},
			},
		},
	}

	tests := []struct {
		requestedModel string
		expected       bool
	}{
		{"claude-fable-5", true},
		{"claude-fable-5[1m]", true},      // The family key covers variants.
		{"Claude-Fable-5-20260601", true}, // Case-insensitive match.
		{"claude-sonnet-4-6", false},      // Other model families remain available.
		{"claude-opus-4-8", false},
	}

	for _, tc := range tests {
		t.Run(tc.requestedModel, func(t *testing.T) {
			got := (ModelPolicy{Record: provider}).Limited(context.Background(), tc.requestedModel)
			require.Equal(t, tc.expected, got)
			remaining := (ModelPolicy{Record: provider}).LimitRemaining(context.Background(), tc.requestedModel)
			require.Equal(t, tc.expected, remaining > 0)
		})
	}
}

func TestIsAnthropicFableModel(t *testing.T) {
	require.True(t, anthropic.IsAnthropicFableModel("claude-fable-5"))
	require.True(t, anthropic.IsAnthropicFableModel("claude-fable-5[1m]"))
	require.True(t, anthropic.IsAnthropicFableModel("Claude-Fable-5"))
	require.False(t, anthropic.IsAnthropicFableModel("claude-sonnet-4-6"))
	require.False(t, anthropic.IsAnthropicFableModel(""))
}

func TestProtocolConversionProviderConstraints(t *testing.T) {
	for _, tc := range []struct {
		platform, kind, auth string
		source, target       protocolcore.ProtocolID
		want                 bool
	}{
		{capability.PlatformOpenAI, capability.ProviderTypeOAuth, "", protocolcore.ProtocolImagesEdits, protocolcore.ProtocolOpenAIResponses, true},
		{capability.PlatformOpenAI, capability.ProviderTypeAPIKey, "", protocolcore.ProtocolImagesEdits, protocolcore.ProtocolOpenAIResponses, false},
		{capability.PlatformGrok, capability.ProviderTypeAPIKey, "", protocolcore.ProtocolResponsesWebSocket, protocolcore.ProtocolOpenAIResponses, true},
		{capability.PlatformGrok, capability.ProviderTypeOAuth, "", protocolcore.ProtocolWebSearch, protocolcore.ProtocolOpenAIResponses, true},
		{capability.PlatformOpenAI, capability.ProviderTypeOAuth, providercore.OpenAIAuthModePersonalAccessToken, protocolcore.ProtocolAlphaSearch, protocolcore.ProtocolOpenAIResponses, true},
		{capability.PlatformOpenAI, capability.ProviderTypeOAuth, "", protocolcore.ProtocolAlphaSearch, protocolcore.ProtocolOpenAIResponses, false},
		{capability.PlatformOpenAI, capability.ProviderTypeAPIKey, "", protocolcore.ProtocolEmbeddings, protocolcore.ProtocolOpenAIResponses, false},
		{capability.PlatformGrok, capability.ProviderTypeAPIKey, "", protocolcore.ProtocolTTS, protocolcore.ProtocolOpenAIResponses, false},
	} {
		t.Run(string(tc.source)+"/"+tc.platform+"/"+tc.kind+"/"+tc.auth, func(t *testing.T) {
			require.Equal(t, tc.want, capability.SupportsProtocolConversion(tc.platform, tc.kind, tc.auth, tc.source, tc.target))
		})
	}
}
