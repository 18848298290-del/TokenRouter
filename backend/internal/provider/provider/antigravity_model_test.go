package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestAntigravityFinalModelChecksWhitelistAfterThinking checks the final thinking suffix after one mapping.
func TestAntigravityFinalModelChecksWhitelistAfterThinking(t *testing.T) {
	enabled := true
	disabled := false
	value := &providercore.Record{Platform: providercore.PlatformAntigravity, Type: providercore.ProviderTypeOAuth, Credentials: map[string]any{
		"model_mapping":   map[string]any{"public": "claude-sonnet-4-5", "claude-sonnet-4-5-thinking": "must-not-map-again"},
		"model_whitelist": []string{"claude-sonnet-4-5-thinking"},
	}}
	require.Equal(t, "claude-sonnet-4-5-thinking", FinalAntigravityModel(value, "public", &enabled))
	require.Empty(t, FinalAntigravityModel(value, "public", &disabled))
	require.Empty(t, MapAntigravityModel(value, "public"))
	value.Credentials["model_whitelist"] = []string{"claude-sonnet-4-5"}
	require.Empty(t, FinalAntigravityModel(value, "public", &enabled))
	require.Equal(t, "claude-sonnet-4-5", MapAntigravityModel(value, "public"))
}

func TestAntigravityMappedTargetCannotBypassExplicitWhitelist(t *testing.T) {
	value := &providercore.Record{Platform: providercore.PlatformAntigravity, Credentials: map[string]any{
		"model_mapping":   map[string]any{"public": "custom-upstream"},
		"model_whitelist": []string{"different-model"},
	}}
	require.Empty(t, MapAntigravityModel(value, "public"))
	value.Credentials["model_whitelist"] = []string{"custom-*"}
	require.Equal(t, "custom-upstream", MapAntigravityModel(value, "public"))
}

func TestAntigravityGatewayService_GetMappedModel(t *testing.T) {
	tests := []struct {
		name            string
		requestedModel  string
		providerMapping map[string]string
		expected        string
	}{
		// Configured mappings determine the upstream model.
		{
			name:            "提供商映射优先",
			requestedModel:  "claude-3-5-sonnet-20241022",
			providerMapping: map[string]string{"claude-3-5-sonnet-20241022": "custom-model"},
			expected:        "custom-model",
		},
		{
			name:            "提供商映射 - 可覆盖默认映射的模型",
			requestedModel:  "claude-sonnet-4-5",
			providerMapping: map[string]string{"claude-sonnet-4-5": "my-custom-sonnet"},
			expected:        "my-custom-sonnet",
		},
		{
			name:            "提供商映射 - 可覆盖未知模型",
			requestedModel:  "claude-opus-4",
			providerMapping: map[string]string{"claude-opus-4": "my-opus"},
			expected:        "my-opus",
		},

		// Unmapped names pass through unchanged.
		{
			name:            "未配置映射时透传 claude-opus-4-6",
			requestedModel:  "claude-opus-4-6",
			providerMapping: nil,
			expected:        "claude-opus-4-6",
		},
		{
			name:            "未配置映射时透传 claude-opus-4-5-20251101",
			requestedModel:  "claude-opus-4-5-20251101",
			providerMapping: nil,
			expected:        "claude-opus-4-5-20251101",
		},
		{
			name:            "未配置映射时透传 claude-opus-4-5-thinking",
			requestedModel:  "claude-opus-4-5-thinking",
			providerMapping: nil,
			expected:        "claude-opus-4-5-thinking",
		},
		{
			name:            "未配置映射时透传 claude-haiku-4-5",
			requestedModel:  "claude-haiku-4-5",
			providerMapping: nil,
			expected:        "claude-haiku-4-5",
		},
		{
			name:            "未配置映射时透传 claude-haiku-4-5-20251001",
			requestedModel:  "claude-haiku-4-5-20251001",
			providerMapping: nil,
			expected:        "claude-haiku-4-5-20251001",
		},
		{
			name:            "未配置映射时透传 claude-sonnet-4-5-20250929",
			requestedModel:  "claude-sonnet-4-5-20250929",
			providerMapping: nil,
			expected:        "claude-sonnet-4-5-20250929",
		},

		{
			name:            "默认映射透传 - claude-fable-5-1",
			requestedModel:  "claude-fable-5-1",
			providerMapping: nil,
			expected:        "claude-fable-5-1",
		},
		{
			name:            "默认映射透传 - claude-fable-5",
			requestedModel:  "claude-fable-5",
			providerMapping: nil,
			expected:        "claude-fable-5",
		},
		{
			name:            "默认映射透传 - claude-sonnet-4-6",
			requestedModel:  "claude-sonnet-4-6",
			providerMapping: nil,
			expected:        "claude-sonnet-4-6",
		},
		{
			name:            "默认映射透传 - claude-sonnet-4-5",
			requestedModel:  "claude-sonnet-4-5",
			providerMapping: nil,
			expected:        "claude-sonnet-4-5",
		},
		{
			name:            "默认映射透传 - claude-opus-4-8",
			requestedModel:  "claude-opus-4-8",
			providerMapping: nil,
			expected:        "claude-opus-4-8",
		},
		{
			name:            "默认映射透传 - claude-opus-4-7",
			requestedModel:  "claude-opus-4-7",
			providerMapping: nil,
			expected:        "claude-opus-4-7",
		},
		{
			name:            "默认映射透传 - claude-opus-4-6-thinking",
			requestedModel:  "claude-opus-4-6-thinking",
			providerMapping: nil,
			expected:        "claude-opus-4-6-thinking",
		},
		{
			name:            "默认映射透传 - claude-sonnet-4-5-thinking",
			requestedModel:  "claude-sonnet-4-5-thinking",
			providerMapping: nil,
			expected:        "claude-sonnet-4-5-thinking",
		},
		{
			name:            "默认映射透传 - gemini-2.5-flash",
			requestedModel:  "gemini-2.5-flash",
			providerMapping: nil,
			expected:        "gemini-2.5-flash",
		},
		{
			name:            "默认映射透传 - gemini-2.5-pro",
			requestedModel:  "gemini-2.5-pro",
			providerMapping: nil,
			expected:        "gemini-2.5-pro",
		},
		{
			name:            "默认映射透传 - gemini-3-flash",
			requestedModel:  "gemini-3-flash",
			providerMapping: nil,
			expected:        "gemini-3-flash",
		},

		// An empty whitelist permits unknown upstream models.
		{
			name:            "未知模型 - claude-unknown 保持原 ID",
			requestedModel:  "claude-unknown",
			providerMapping: nil,
			expected:        "claude-unknown",
		},
		{
			name:            "未知模型 - claude-3-5-sonnet-20241022 保持原 ID",
			requestedModel:  "claude-3-5-sonnet-20241022",
			providerMapping: nil,
			expected:        "claude-3-5-sonnet-20241022",
		},
		{
			name:            "未知模型 - claude-3-opus-20240229 保持原 ID",
			requestedModel:  "claude-3-opus-20240229",
			providerMapping: nil,
			expected:        "claude-3-opus-20240229",
		},
		{
			name:            "未知模型 - claude-opus-4 保持原 ID",
			requestedModel:  "claude-opus-4",
			providerMapping: nil,
			expected:        "claude-opus-4",
		},
		{
			name:            "未知模型 - gemini-future-model 保持原 ID",
			requestedModel:  "gemini-future-model",
			providerMapping: nil,
			expected:        "gemini-future-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &providercore.Record{
				Platform: capability.PlatformAntigravity,
			}
			if tt.providerMapping != nil {
				// Model mapping credentials use map[string]any, as decoded from JSON.
				mappingAny := make(map[string]any)
				for k, v := range tt.providerMapping {
					mappingAny[k] = v
				}
				provider.Credentials = map[string]any{
					"model_mapping": mappingAny,
				}
			}

			got := MapAntigravityModel(provider, tt.requestedModel)
			require.Equal(t, tt.expected, got, "model: %s", tt.requestedModel)
		})
	}
}

func TestAntigravityGatewayService_GetMappedModel_EdgeCases(t *testing.T) {
	tests := []struct {
		name           string
		requestedModel string
		expected       string
	}{
		{"空字符串", "", ""},
		{"非claude/gemini前缀 - gpt", "gpt-4", "gpt-4"},
		{"非claude/gemini前缀 - llama", "llama-3", "llama-3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &providercore.Record{Platform: capability.PlatformAntigravity}
			got := MapAntigravityModel(provider, tt.requestedModel)
			require.Equal(t, tt.expected, got)
		})
	}
}

// TestMapAntigravityModel_WildcardTargetEqualsRequest accepts a wildcard whose target equals the
// requested model.
func TestMapAntigravityModel_WildcardTargetEqualsRequest(t *testing.T) {
	tests := []struct {
		name           string
		modelMapping   map[string]any
		requestedModel string
		expected       string
	}{
		{
			name:           "wildcard target equals request model",
			modelMapping:   map[string]any{"claude-*": "claude-sonnet-4-5"},
			requestedModel: "claude-sonnet-4-5",
			expected:       "claude-sonnet-4-5",
		},
		{
			name:           "wildcard target differs from request model",
			modelMapping:   map[string]any{"claude-*": "claude-sonnet-4-5"},
			requestedModel: "claude-opus-4-6",
			expected:       "claude-sonnet-4-5",
		},
		{
			name:           "wildcard no match",
			modelMapping:   map[string]any{"claude-*": "claude-sonnet-4-5"},
			requestedModel: "gpt-4o",
			expected:       "gpt-4o",
		},
		{
			name:           "explicit passthrough same name",
			modelMapping:   map[string]any{"claude-sonnet-4-5": "claude-sonnet-4-5"},
			requestedModel: "claude-sonnet-4-5",
			expected:       "claude-sonnet-4-5",
		},
		{
			name:           "multiple wildcards target equals one request",
			modelMapping:   map[string]any{"claude-*": "claude-sonnet-4-5", "gemini-*": "gemini-2.5-flash"},
			requestedModel: "gemini-2.5-flash",
			expected:       "gemini-2.5-flash",
		},
		{
			name:           "customtools alias falls back to normalized preview mapping",
			modelMapping:   map[string]any{"gemini-3.1-pro-preview": "gemini-3.1-pro-high"},
			requestedModel: "gemini-3.1-pro-preview-customtools",
			expected:       "gemini-3.1-pro-preview-customtools",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &providercore.Record{
				Platform: capability.PlatformAntigravity,
				Credentials: map[string]any{
					"model_mapping": tt.modelMapping,
				},
			}
			got := MapAntigravityModel(provider, tt.requestedModel)
			require.Equal(t, tt.expected, got, "MapAntigravityModel(%q) = %q, want %q", tt.requestedModel, got, tt.expected)
		})
	}
}
