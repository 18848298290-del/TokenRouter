package provider

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestProvider_IsAnthropicAPIKeyPassthroughEnabled(t *testing.T) {
	t.Run("Anthropic API Key 开启", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformAnthropic,
			Type:     capability.ProviderTypeAPIKey,
			Extra: map[string]any{
				"anthropic_passthrough": true,
			},
		}
		require.True(t, provider.IsAnthropicAPIKeyPassthroughEnabled())
	})

	t.Run("Anthropic API Key 关闭", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformAnthropic,
			Type:     capability.ProviderTypeAPIKey,
			Extra: map[string]any{
				"anthropic_passthrough": false,
			},
		}
		require.False(t, provider.IsAnthropicAPIKeyPassthroughEnabled())
	})

	t.Run("字段类型非法默认关闭", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformAnthropic,
			Type:     capability.ProviderTypeAPIKey,
			Extra: map[string]any{
				"anthropic_passthrough": "true",
			},
		}
		require.False(t, provider.IsAnthropicAPIKeyPassthroughEnabled())
	})

	t.Run("非 Anthropic API Key 提供商始终关闭", func(t *testing.T) {
		oauth := &Record{
			Platform: capability.PlatformAnthropic,
			Type:     capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"anthropic_passthrough": true,
			},
		}
		require.False(t, oauth.IsAnthropicAPIKeyPassthroughEnabled())

		openai := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Extra: map[string]any{
				"anthropic_passthrough": true,
			},
		}
		require.False(t, openai.IsAnthropicAPIKeyPassthroughEnabled())
	})
}

func TestGetBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		provider Record
		expected string
	}{
		{
			name: "non-apikey type returns empty",
			provider: Record{
				Type:     capability.ProviderTypeOAuth,
				Platform: capability.PlatformAnthropic,
			},
			expected: "",
		},
		{
			name: "apikey without base_url returns default anthropic",
			provider: Record{
				Type:        capability.ProviderTypeAPIKey,
				Platform:    capability.PlatformAnthropic,
				Credentials: map[string]any{},
			},
			expected: "https://api.anthropic.com",
		},
		{
			name: "apikey with custom base_url",
			provider: Record{
				Type:        capability.ProviderTypeAPIKey,
				Platform:    capability.PlatformAnthropic,
				Credentials: map[string]any{"base_url": "https://custom.example.com"},
			},
			expected: "https://custom.example.com",
		},
		{
			name: "antigravity non-apikey returns empty",
			provider: Record{
				Type:        capability.ProviderTypeOAuth,
				Platform:    capability.PlatformAntigravity,
				Credentials: map[string]any{"base_url": "https://upstream.example.com"},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.provider.GetBaseURL()
			if result != tt.expected {
				t.Errorf("GetBaseURL() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestGetGeminiBaseURL(t *testing.T) {
	const defaultGeminiURL = "https://generativelanguage.googleapis.com"

	tests := []struct {
		name     string
		provider Record
		expected string
	}{
		{
			name: "apikey without base_url returns default",
			provider: Record{
				Type:        capability.ProviderTypeAPIKey,
				Platform:    capability.PlatformGemini,
				Credentials: map[string]any{},
			},
			expected: defaultGeminiURL,
		},
		{
			name: "apikey with custom base_url",
			provider: Record{
				Type:        capability.ProviderTypeAPIKey,
				Platform:    capability.PlatformGemini,
				Credentials: map[string]any{"base_url": "https://custom-gemini.example.com"},
			},
			expected: "https://custom-gemini.example.com",
		},
		{
			name: "antigravity oauth does NOT append /antigravity",
			provider: Record{
				Type:        capability.ProviderTypeOAuth,
				Platform:    capability.PlatformAntigravity,
				Credentials: map[string]any{"base_url": "https://upstream.example.com"},
			},
			expected: "https://upstream.example.com",
		},
		{
			name: "oauth without base_url returns default",
			provider: Record{
				Type:        capability.ProviderTypeOAuth,
				Platform:    capability.PlatformAntigravity,
				Credentials: map[string]any{},
			},
			expected: defaultGeminiURL,
		},
		{
			name: "nil credentials returns default",
			provider: Record{
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformGemini,
			},
			expected: defaultGeminiURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.provider.GetGeminiBaseURL(defaultGeminiURL)
			if result != tt.expected {
				t.Errorf("GetGeminiBaseURL() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestHasGeminiThirdPartyBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		provider Record
		expected bool
	}{
		{
			name: "custom Gemini-compatible endpoint",
			provider: Record{
				Platform: capability.PlatformGemini,
				Type:     capability.ProviderTypeAPIKey,
				Credentials: map[string]any{
					GeminiProviderTypeCredentialKey: GeminiProviderTypeThirdParty,
					"base_url":                      "https://provider.example.test/v1beta",
				},
			},
			expected: true,
		},
		{
			name: "missing base URL",
			provider: Record{
				Platform: capability.PlatformGemini,
				Type:     capability.ProviderTypeAPIKey,
				Credentials: map[string]any{
					GeminiProviderTypeCredentialKey: GeminiProviderTypeThirdParty,
				},
			},
			expected: false,
		},
		{
			name: "official Gemini endpoint",
			provider: Record{
				Platform: capability.PlatformGemini,
				Type:     capability.ProviderTypeAPIKey,
				Credentials: map[string]any{
					GeminiProviderTypeCredentialKey: GeminiProviderTypeThirdParty,
					"base_url":                      "https://generativelanguage.googleapis.com/v1beta",
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.provider.HasGeminiThirdPartyBaseURL())
		})
	}
}

// TestGetOpenAIProtocolAPIKey_CNProviders checks API key access for Chinese providers.
// IsOpenAIApiKey remains OpenAI-specific for scheduling and WebSocket eligibility.
func TestGetOpenAIProtocolAPIKey_CNProviders(t *testing.T) {
	t.Parallel()

	kimi := &Record{
		Platform:    capability.PlatformKimi,
		Type:        capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-kimi"},
	}
	require.Equal(t, "sk-kimi", kimi.GetOpenAIProtocolAPIKey())
	require.False(t, kimi.IsOpenAIApiKey(), "IsOpenAIApiKey stays openai-only for scheduling gates")

	// A Chinese provider with a non-API-Key type must not expose its stored key.
	notAPIKey := &Record{
		Platform:    capability.PlatformDeepseek,
		Type:        capability.ProviderTypeOAuth,
		Credentials: map[string]any{"api_key": "sk-leak"},
	}
	require.Equal(t, "", notAPIKey.GetOpenAIProtocolAPIKey())

	// OpenAI uses its platform-specific API key accessor.
	openai := &Record{
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-openai"},
	}
	require.Equal(t, "sk-openai", openai.GetOpenAIProtocolAPIKey())
}

func TestGetPoolModeRetryCount(t *testing.T) {
	tests := []struct {
		name     string
		provider *Record
		expected int
	}{
		{
			name: "default_when_not_pool_mode",
			provider: &Record{
				Type:        capability.ProviderTypeAPIKey,
				Platform:    capability.PlatformOpenAI,
				Credentials: map[string]any{},
			},
			expected: DefaultPoolModeRetryCount,
		},
		{
			name: "default_when_missing_retry_count",
			provider: &Record{
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"pool_mode": true,
				},
			},
			expected: DefaultPoolModeRetryCount,
		},
		{
			name: "supports_float64_from_json_credentials",
			provider: &Record{
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"pool_mode":             true,
					"pool_mode_retry_count": float64(5),
				},
			},
			expected: 5,
		},
		{
			name: "supports_json_number",
			provider: &Record{
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"pool_mode":             true,
					"pool_mode_retry_count": json.Number("4"),
				},
			},
			expected: 4,
		},
		{
			name: "supports_string_value",
			provider: &Record{
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"pool_mode":             true,
					"pool_mode_retry_count": "2",
				},
			},
			expected: 2,
		},
		{
			name: "negative_value_is_clamped_to_zero",
			provider: &Record{
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"pool_mode":             true,
					"pool_mode_retry_count": -1,
				},
			},
			expected: 0,
		},
		{
			name: "oversized_value_is_clamped_to_max",
			provider: &Record{
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"pool_mode":             true,
					"pool_mode_retry_count": 99,
				},
			},
			expected: MaxPoolModeRetryCount,
		},
		{
			name: "invalid_value_falls_back_to_default",
			provider: &Record{
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"pool_mode":             true,
					"pool_mode_retry_count": "oops",
				},
			},
			expected: DefaultPoolModeRetryCount,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.provider.GetPoolModeRetryCount())
		})
	}
}

func TestProvider_GetCodexCLIOnlyAllowedClients(t *testing.T) {
	t.Run("OAuth 提供商读取 []any 字符串列表", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Extra:    map[string]any{"codex_cli_only_allowed_clients": []any{"claude_code"}},
		}
		require.Equal(t, []string{"claude_code"}, provider.GetCodexCLIOnlyAllowedClients())
	})

	t.Run("OAuth 提供商读取 []string 列表", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Extra:    map[string]any{"codex_cli_only_allowed_clients": []string{"claude_code"}},
		}
		require.Equal(t, []string{"claude_code"}, provider.GetCodexCLIOnlyAllowedClients())
	})

	t.Run("[]string 跳过空白元素", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Extra:    map[string]any{"codex_cli_only_allowed_clients": []string{"claude_code", "", "  "}},
		}
		require.Equal(t, []string{"claude_code"}, provider.GetCodexCLIOnlyAllowedClients())
	})

	t.Run("跳过非字符串与空白元素", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Extra:    map[string]any{"codex_cli_only_allowed_clients": []any{"claude_code", 123, "", "  "}},
		}
		require.Equal(t, []string{"claude_code"}, provider.GetCodexCLIOnlyAllowedClients())
	})

	t.Run("非 OAuth 提供商返回空", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Extra:    map[string]any{"codex_cli_only_allowed_clients": []any{"claude_code"}},
		}
		require.Empty(t, provider.GetCodexCLIOnlyAllowedClients())
	})

	t.Run("Extra 为空返回空", func(t *testing.T) {
		provider := &Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}
		require.Empty(t, provider.GetCodexCLIOnlyAllowedClients())
	})

	t.Run("字段缺失返回空", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Extra:    map[string]any{},
		}
		require.Empty(t, provider.GetCodexCLIOnlyAllowedClients())
	})
}

func TestProvider_IsInterceptWarmupEnabled(t *testing.T) {
	tests := []struct {
		name        string
		credentials map[string]any
		expected    bool
	}{
		{
			name:        "nil credentials",
			credentials: nil,
			expected:    false,
		},
		{
			name:        "empty map",
			credentials: map[string]any{},
			expected:    false,
		},
		{
			name:        "field not present",
			credentials: map[string]any{"access_token": "tok"},
			expected:    false,
		},
		{
			name:        "field is true",
			credentials: map[string]any{"intercept_warmup_requests": true},
			expected:    true,
		},
		{
			name:        "field is false",
			credentials: map[string]any{"intercept_warmup_requests": false},
			expected:    false,
		},
		{
			name:        "field is string true",
			credentials: map[string]any{"intercept_warmup_requests": "true"},
			expected:    false,
		},
		{
			name:        "field is int 1",
			credentials: map[string]any{"intercept_warmup_requests": 1},
			expected:    false,
		},
		{
			name:        "field is nil",
			credentials: map[string]any{"intercept_warmup_requests": nil},
			expected:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Record{Credentials: tt.credentials}
			result := a.IsInterceptWarmupEnabled()
			require.Equal(t, tt.expected, result)
		})
	}
}

func intPtrHelper(v int) *int { return &v }

func TestEffectiveLoadFactor_NilProvider(t *testing.T) {
	var a *Record
	require.Equal(t, 1, a.EffectiveLoadFactor())
}

func TestEffectiveLoadFactor_NilLoadFactor_PositiveConcurrency(t *testing.T) {
	a := &Record{Concurrency: 5}
	require.Equal(t, 5, a.EffectiveLoadFactor())
}

func TestEffectiveLoadFactor_NilLoadFactor_ZeroConcurrency(t *testing.T) {
	a := &Record{Concurrency: 0}
	require.Equal(t, 1, a.EffectiveLoadFactor())
}

func TestEffectiveLoadFactor_PositiveLoadFactor(t *testing.T) {
	a := &Record{Concurrency: 5, LoadFactor: intPtrHelper(20)}
	require.Equal(t, 20, a.EffectiveLoadFactor())
}

func TestEffectiveLoadFactor_ZeroLoadFactor_FallbackToConcurrency(t *testing.T) {
	a := &Record{Concurrency: 5, LoadFactor: intPtrHelper(0)}
	require.Equal(t, 5, a.EffectiveLoadFactor())
}

func TestEffectiveLoadFactor_NegativeLoadFactor_FallbackToConcurrency(t *testing.T) {
	a := &Record{Concurrency: 3, LoadFactor: intPtrHelper(-1)}
	require.Equal(t, 3, a.EffectiveLoadFactor())
}

func TestEffectiveLoadFactor_ZeroLoadFactor_ZeroConcurrency(t *testing.T) {
	a := &Record{Concurrency: 0, LoadFactor: intPtrHelper(0)}
	require.Equal(t, 1, a.EffectiveLoadFactor())
}

// TestProviderCompactionControlledByAdministrator checks independent switches that override historical probe results.
func TestProviderCompactionControlledByAdministrator(t *testing.T) {
	for _, mode := range []string{"", "force_on", "force_off"} {
		for _, probed := range []bool{false, true} {
			a := &Record{Platform: capability.PlatformOpenAI, Extra: map[string]any{"openai_compact_mode": mode, "openai_compact_supported": probed, OpenAINativeCompactionV2ModeExtraKey: mode, "openai_native_compaction_v2_supported": probed}}
			want := mode != "force_off"
			if a.AllowsOpenAICompact() != want || a.AllowsOpenAINativeCompactionV2() != want {
				t.Fatalf("mode=%s probe=%v ignored administrator", mode, probed)
			}
			a.Extra[OpenAINativeCompactionV2ModeExtraKey] = "force_off"
			if a.AllowsOpenAINativeCompactionV2() || a.AllowsOpenAICompact() != want {
				t.Fatal("independent switches required")
			}
		}
	}
	for _, a := range []*Record{nil, {Platform: capability.PlatformAnthropic}} {
		if a.AllowsOpenAICompact() || a.AllowsOpenAINativeCompactionV2() {
			t.Fatal("non OpenAI must be excluded")
		}
	}
}

func TestProviderGetCompactModelMapping(t *testing.T) {
	tests := []struct {
		name     string
		provider *Record
		want     map[string]string
	}{
		{
			name: "nil provider returns nil",
			want: nil,
		},
		{
			name: "missing credentials returns nil",
			provider: &Record{
				Platform: capability.PlatformOpenAI,
			},
			want: nil,
		},
		{
			name: "map any is converted",
			provider: &Record{
				Credentials: map[string]any{
					"compact_model_mapping": map[string]any{
						"gpt-5.4": "gpt-5.4-openai-compact",
						"invalid": 1,
					},
				},
			},
			want: map[string]string{
				"gpt-5.4": "gpt-5.4-openai-compact",
			},
		},
		{
			name: "map string string is copied",
			provider: &Record{
				Credentials: map[string]any{
					"compact_model_mapping": map[string]string{
						"gpt-*": "compact-*",
					},
				},
			},
			want: map[string]string{
				"gpt-*": "compact-*",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.provider.GetCompactModelMapping()
			if !equalStringMap(got, tt.want) {
				t.Fatalf("GetCompactModelMapping() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestProviderResolveCompactMappedModel(t *testing.T) {
	tests := []struct {
		name           string
		credentials    map[string]any
		requestedModel string
		expectedModel  string
		expectedMatch  bool
	}{
		{
			name:           "no compact mapping reports unmatched",
			credentials:    nil,
			requestedModel: "gpt-5.4",
			expectedModel:  "gpt-5.4",
			expectedMatch:  false,
		},
		{
			name: "exact compact mapping matches",
			credentials: map[string]any{
				"compact_model_mapping": map[string]any{
					"gpt-5.4": "gpt-5.4-openai-compact",
				},
			},
			requestedModel: "gpt-5.4",
			expectedModel:  "gpt-5.4-openai-compact",
			expectedMatch:  true,
		},
		{
			name: "exact passthrough counts as match",
			credentials: map[string]any{
				"compact_model_mapping": map[string]any{
					"gpt-5.4": "gpt-5.4",
				},
			},
			requestedModel: "gpt-5.4",
			expectedModel:  "gpt-5.4",
			expectedMatch:  true,
		},
		{
			name: "longest wildcard wins",
			credentials: map[string]any{
				"compact_model_mapping": map[string]any{
					"gpt-*":         "fallback-compact",
					"gpt-5.4*":      "gpt-5.4-openai-compact",
					"gpt-5.4-mini*": "gpt-5.4-mini-openai-compact",
				},
			},
			requestedModel: "gpt-5.4-mini",
			expectedModel:  "gpt-5.4-mini-openai-compact",
			expectedMatch:  true,
		},
		{
			name: "missing compact mapping reports unmatched",
			credentials: map[string]any{
				"compact_model_mapping": map[string]any{
					"gpt-5.3": "gpt-5.3-openai-compact",
				},
			},
			requestedModel: "gpt-5.4",
			expectedModel:  "gpt-5.4",
			expectedMatch:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &Record{
				Platform:    capability.PlatformOpenAI,
				Credentials: tt.credentials,
			}
			gotModel, gotMatch := provider.ResolveCompactMappedModel(tt.requestedModel)
			if gotModel != tt.expectedModel || gotMatch != tt.expectedMatch {
				t.Fatalf("ResolveCompactMappedModel(%q) = (%q, %v), want (%q, %v)", tt.requestedModel, gotModel, gotMatch, tt.expectedModel, tt.expectedMatch)
			}
		})
	}
}

func equalStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, want := range right {
		if got, ok := left[key]; !ok || got != want {
			return false
		}
	}
	return true
}

func TestProvider_IsOpenAIPassthroughEnabled(t *testing.T) {
	t.Run("新字段开启", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Extra: map[string]any{
				"openai_passthrough": true,
			},
		}
		require.True(t, provider.IsOpenAIPassthroughEnabled())
	})

	t.Run("兼容旧字段", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"openai_oauth_passthrough": true,
			},
		}
		require.True(t, provider.IsOpenAIPassthroughEnabled())
	})

	t.Run("非OpenAI提供商始终关闭", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformAnthropic,
			Type:     capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"openai_passthrough": true,
			},
		}
		require.False(t, provider.IsOpenAIPassthroughEnabled())
	})

	t.Run("空额外配置默认关闭", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
		}
		require.False(t, provider.IsOpenAIPassthroughEnabled())
	})
}

func TestProvider_IsOpenAIOAuthPassthroughEnabled(t *testing.T) {
	t.Run("仅OAuth类型允许返回开启", func(t *testing.T) {
		oauthProvider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"openai_passthrough": true,
			},
		}
		require.True(t, oauthProvider.IsOpenAIOAuthPassthroughEnabled())

		apiKeyProvider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Extra: map[string]any{
				"openai_passthrough": true,
			},
		}
		require.False(t, apiKeyProvider.IsOpenAIOAuthPassthroughEnabled())
	})
}

func TestProvider_IsCodexCLIOnlyEnabled(t *testing.T) {
	t.Run("OpenAI OAuth 开启", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"codex_cli_only": true,
			},
		}
		require.True(t, provider.IsCodexCLIOnlyEnabled())
	})

	t.Run("OpenAI OAuth 关闭", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"codex_cli_only": false,
			},
		}
		require.False(t, provider.IsCodexCLIOnlyEnabled())
	})

	t.Run("字段缺失默认关闭", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Extra:    map[string]any{},
		}
		require.False(t, provider.IsCodexCLIOnlyEnabled())
	})

	t.Run("类型非法默认关闭", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"codex_cli_only": "true",
			},
		}
		require.False(t, provider.IsCodexCLIOnlyEnabled())
	})

	t.Run("非 OAuth 提供商始终关闭", func(t *testing.T) {
		apiKeyProvider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Extra: map[string]any{
				"codex_cli_only": true,
			},
		}
		require.False(t, apiKeyProvider.IsCodexCLIOnlyEnabled())

		otherPlatform := &Record{
			Platform: capability.PlatformAnthropic,
			Type:     capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"codex_cli_only": true,
			},
		}
		require.False(t, otherPlatform.IsCodexCLIOnlyEnabled())
	})

	t.Run("新策略字段优先于旧字段", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"openai_oauth_client_policy": OpenAIOAuthClientPolicyAny,
				"codex_cli_only":             true,
			},
		}
		require.False(t, provider.IsCodexCLIOnlyEnabled())
		require.Equal(t, OpenAIOAuthClientPolicyAny, provider.GetOpenAIOAuthClientPolicy())
	})

	t.Run("TLS 路由器策略不等同于 Codex-only", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"openai_oauth_client_policy": OpenAIOAuthClientPolicyTLSRouterMatchedOnly,
				"tls_fingerprint_router_id":  int64(12),
			},
		}
		require.False(t, provider.IsCodexCLIOnlyEnabled())
		require.True(t, provider.IsOpenAIOAuthTLSRouterMatchedOnly())
		require.Equal(t, int64(12), provider.GetTLSFingerprintRouterID())
	})
}

func TestProvider_IsTLSFingerprintEnabled(t *testing.T) {
	tests := []struct {
		name     string
		provider *Record
		want     bool
	}{
		{
			name: "Anthropic OAuth 开启",
			provider: &Record{
				Platform: capability.PlatformAnthropic,
				Type:     capability.ProviderTypeOAuth,
				Extra:    map[string]any{"enable_tls_fingerprint": true},
			},
			want: true,
		},
		{
			name: "Anthropic SetupToken 开启",
			provider: &Record{
				Platform: capability.PlatformAnthropic,
				Type:     capability.ProviderTypeSetupToken,
				Extra:    map[string]any{"enable_tls_fingerprint": true},
			},
			want: true,
		},
		{
			name: "OpenAI OAuth 开启",
			provider: &Record{
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeOAuth,
				Extra:    map[string]any{"enable_tls_fingerprint": true},
			},
			want: true,
		},
		{
			name: "OpenAI API Key 不支持",
			provider: &Record{
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeAPIKey,
				Extra:    map[string]any{"enable_tls_fingerprint": true},
			},
			want: false,
		},
		{
			name: "非法类型按关闭处理",
			provider: &Record{
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeOAuth,
				Extra:    map[string]any{"enable_tls_fingerprint": "true"},
			},
			want: false,
		},
		{
			name: "字段缺失按关闭处理",
			provider: &Record{
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeOAuth,
				Extra:    map[string]any{},
			},
			want: false,
		},
		{
			name:     "nil 提供商按关闭处理",
			provider: nil,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.provider.IsTLSFingerprintEnabled())
		})
	}
}

func TestGetPoolModeRetryStatusCodes(t *testing.T) {
	tests := []struct {
		name     string
		provider *Record
		expected []int
	}{
		{
			name:     "nil_provider_returns_nil",
			provider: nil,
			expected: nil,
		},
		{
			name: "nil_credentials_returns_nil",
			provider: &Record{
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
			},
			expected: nil,
		},
		{
			name: "missing_key_returns_nil",
			provider: &Record{
				Type:        capability.ProviderTypeAPIKey,
				Platform:    capability.PlatformOpenAI,
				Credentials: map[string]any{"pool_mode": true},
			},
			expected: nil,
		},
		{
			name: "empty_slice_is_preserved",
			provider: &Record{
				Credentials: map[string]any{
					"pool_mode_retry_status_codes": []any{},
				},
			},
			expected: []int{},
		},
		{
			name: "float64_values_from_json_are_normalized",
			provider: &Record{
				Credentials: map[string]any{
					"pool_mode_retry_status_codes": []any{float64(429), float64(401), float64(403)},
				},
			},
			expected: []int{401, 403, 429},
		},
		{
			name: "json_number_values_supported",
			provider: &Record{
				Credentials: map[string]any{
					"pool_mode_retry_status_codes": []any{json.Number("502"), json.Number("503")},
				},
			},
			expected: []int{502, 503},
		},
		{
			name: "string_values_supported",
			provider: &Record{
				Credentials: map[string]any{
					"pool_mode_retry_status_codes": []any{"520", "529"},
				},
			},
			expected: []int{520, 529},
		},
		{
			name: "duplicates_are_deduped",
			provider: &Record{
				Credentials: map[string]any{
					"pool_mode_retry_status_codes": []any{float64(429), float64(429), float64(401)},
				},
			},
			expected: []int{401, 429},
		},
		{
			name: "out_of_range_values_dropped",
			provider: &Record{
				Credentials: map[string]any{
					"pool_mode_retry_status_codes": []any{float64(99), float64(600), float64(429)},
				},
			},
			expected: []int{429},
		},
		{
			name: "invalid_string_dropped",
			provider: &Record{
				Credentials: map[string]any{
					"pool_mode_retry_status_codes": []any{"oops", float64(429)},
				},
			},
			expected: []int{429},
		},
		{
			name: "non_array_value_returns_nil",
			provider: &Record{
				Credentials: map[string]any{
					"pool_mode_retry_status_codes": "not-an-array",
				},
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.provider.GetPoolModeRetryStatusCodes())
		})
	}
}

func TestIsPoolModeRetryableStatus_Provider(t *testing.T) {
	tests := []struct {
		name       string
		provider   *Record
		statusCode int
		expected   bool
	}{
		{
			name:       "nil_provider_falls_back_to_default_401",
			provider:   nil,
			statusCode: 401,
			expected:   true,
		},
		{
			name:       "nil_provider_falls_back_to_default_500",
			provider:   nil,
			statusCode: 500,
			expected:   false,
		},
		{
			name: "unconfigured_uses_default_403",
			provider: &Record{
				Credentials: map[string]any{"pool_mode": true},
			},
			statusCode: 403,
			expected:   true,
		},
		{
			name: "unconfigured_uses_default_502_false",
			provider: &Record{
				Credentials: map[string]any{"pool_mode": true},
			},
			statusCode: 502,
			expected:   false,
		},
		{
			name: "configured_list_overrides_default_401_dropped",
			provider: &Record{
				Credentials: map[string]any{
					"pool_mode_retry_status_codes": []any{float64(502), float64(503)},
				},
			},
			statusCode: 401,
			expected:   false,
		},
		{
			name: "configured_list_overrides_default_502_added",
			provider: &Record{
				Credentials: map[string]any{
					"pool_mode_retry_status_codes": []any{float64(502), float64(503)},
				},
			},
			statusCode: 502,
			expected:   true,
		},
		{
			name: "empty_list_disables_all_default_codes",
			provider: &Record{
				Credentials: map[string]any{
					"pool_mode_retry_status_codes": []any{},
				},
			},
			statusCode: 429,
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.provider.IsPoolModeRetryableStatus(tt.statusCode))
		})
	}
}

func TestProviderIsSchedulable_QuotaExceeded(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		provider *Record
		want     bool
	}{
		{
			name: "apikey daily quota exceeded",
			provider: &Record{
				LoadLocation: time.LoadLocation,
				Status:       StatusActive,
				Schedulable:  true,
				Type:         capability.ProviderTypeAPIKey,
				Extra: map[string]any{
					"quota_daily_limit": 10.0,
					"quota_daily_used":  10.0,
					"quota_daily_start": now.Add(-1 * time.Hour).Format(time.RFC3339),
				},
			},
			want: false,
		},
		{
			name: "apikey weekly quota exceeded",
			provider: &Record{
				LoadLocation: time.LoadLocation,
				Status:       StatusActive,
				Schedulable:  true,
				Type:         capability.ProviderTypeAPIKey,
				Extra: map[string]any{
					"quota_weekly_limit": 50.0,
					"quota_weekly_used":  50.0,
					"quota_weekly_start": now.Add(-2 * 24 * time.Hour).Format(time.RFC3339),
				},
			},
			want: false,
		},
		{
			name: "apikey total quota exceeded",
			provider: &Record{
				LoadLocation: time.LoadLocation,
				Status:       StatusActive,
				Schedulable:  true,
				Type:         capability.ProviderTypeAPIKey,
				Extra: map[string]any{
					"quota_limit": 100.0,
					"quota_used":  100.0,
				},
			},
			want: false,
		},
		{
			name: "apikey quota not exceeded",
			provider: &Record{
				LoadLocation: time.LoadLocation,
				Status:       StatusActive,
				Schedulable:  true,
				Type:         capability.ProviderTypeAPIKey,
				Extra: map[string]any{
					"quota_daily_limit": 10.0,
					"quota_daily_used":  5.0,
					"quota_daily_start": now.Add(-1 * time.Hour).Format(time.RFC3339),
				},
			},
			want: true,
		},
		{
			name: "apikey expired daily period restores schedulable",
			provider: &Record{
				LoadLocation: time.LoadLocation,
				Status:       StatusActive,
				Schedulable:  true,
				Type:         capability.ProviderTypeAPIKey,
				Extra: map[string]any{
					"quota_daily_limit": 10.0,
					"quota_daily_used":  10.0,
					"quota_daily_start": now.Add(-25 * time.Hour).Format(time.RFC3339),
				},
			},
			want: true,
		},
		{
			name: "oauth ignores quota exceeded",
			provider: &Record{
				LoadLocation: time.LoadLocation,
				Status:       StatusActive,
				Schedulable:  true,
				Type:         capability.ProviderTypeOAuth,
				Extra: map[string]any{
					"quota_daily_limit": 10.0,
					"quota_daily_used":  10.0,
					"quota_daily_start": now.Add(-1 * time.Hour).Format(time.RFC3339),
				},
			},
			want: true,
		},
		{
			name: "bedrock quota exceeded",
			provider: &Record{
				LoadLocation: time.LoadLocation,
				Status:       StatusActive,
				Schedulable:  true,
				Type:         capability.ProviderTypeBedrock,
				Extra: map[string]any{
					"quota_limit": 200.0,
					"quota_used":  200.0,
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.provider.IsSchedulable())
		})
	}
}

func TestProviderSparkShadowHelpers(t *testing.T) {
	pid := int64(100)
	normal := &Record{ID: 100}
	require.False(t, normal.IsShadow())
	require.False(t, normal.IsCredentialShadow())
	require.Equal(t, QuotaDimensionGlobal, normal.QuotaDimensionOrDefault())
	shadow := &Record{ID: 200, ParentProviderID: &pid, QuotaDimension: QuotaDimensionSpark}
	require.True(t, shadow.IsShadow())
	require.True(t, shadow.IsCredentialShadow())
	require.Equal(t, QuotaDimensionSpark, shadow.QuotaDimensionOrDefault())
}

func TestLastFixedDailyReset_BeforeResetHour(t *testing.T) {
	tz := time.UTC
	now := time.Date(2026, 3, 14, 6, 0, 0, 0, tz)
	got := LastFixedDailyReset(9, tz, now)
	// Before today's 09:00 reset, the last reset is yesterday at 09:00.
	want := time.Date(2026, 3, 13, 9, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

func TestLastFixedDailyReset_AtResetHour(t *testing.T) {
	tz := time.UTC
	now := time.Date(2026, 3, 14, 9, 0, 0, 0, tz)
	got := LastFixedDailyReset(9, tz, now)
	// At the reset instant, today's 09:00 reset has taken effect.
	want := time.Date(2026, 3, 14, 9, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

func TestLastFixedDailyReset_AfterResetHour(t *testing.T) {
	tz := time.UTC
	now := time.Date(2026, 3, 14, 15, 0, 0, 0, tz)
	got := LastFixedDailyReset(9, tz, now)
	// After 09:00, the last reset is today at 09:00.
	want := time.Date(2026, 3, 14, 9, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

func TestLastFixedWeeklyReset_SameDay_AfterHour(t *testing.T) {
	tz := time.UTC
	// Monday, 2026-03-16 at 15:00, with a Monday 09:00 reset.
	now := time.Date(2026, 3, 16, 15, 0, 0, 0, tz)
	got := LastFixedWeeklyReset(1, 9, tz, now)
	// The reset is today at 09:00.
	want := time.Date(2026, 3, 16, 9, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

func TestLastFixedWeeklyReset_SameDay_BeforeHour(t *testing.T) {
	tz := time.UTC
	// Monday, 2026-03-16 at 06:00, before the Monday 09:00 reset.
	now := time.Date(2026, 3, 16, 6, 0, 0, 0, tz)
	got := LastFixedWeeklyReset(1, 9, tz, now)
	// The last reset is Monday, 2026-03-09 at 09:00.
	want := time.Date(2026, 3, 9, 9, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

func TestLastFixedWeeklyReset_DifferentDay(t *testing.T) {
	tz := time.UTC
	// Wednesday, 2026-03-18, with a Monday reset.
	now := time.Date(2026, 3, 18, 10, 0, 0, 0, tz)
	got := LastFixedWeeklyReset(1, 9, tz, now)
	// The preceding Monday is 2026-03-16.
	want := time.Date(2026, 3, 16, 9, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

func TestIsFixedDailyPeriodExpired_ZeroPeriodStart(t *testing.T) {
	a := &Record{LoadLocation: time.LoadLocation, Extra: map[string]any{
		"quota_daily_reset_mode": "fixed",
		"quota_daily_reset_hour": float64(9),
		"quota_reset_timezone":   "UTC",
	}}
	assert.True(t, a.IsFixedDailyPeriodExpired(time.Time{}))
}

func TestIsFixedDailyPeriodExpired_NotExpired(t *testing.T) {
	a := &Record{LoadLocation: time.LoadLocation, Extra: map[string]any{
		"quota_daily_reset_mode": "fixed",
		"quota_daily_reset_hour": float64(9),
		"quota_reset_timezone":   "UTC",
	}}
	// Start the period at noon UTC today, after the latest daily reset.
	now := time.Now().UTC()
	periodStart := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)
	assert.False(t, a.IsFixedDailyPeriodExpired(periodStart))
}

func TestIsFixedDailyPeriodExpired_Expired(t *testing.T) {
	a := &Record{LoadLocation: time.LoadLocation, Extra: map[string]any{
		"quota_daily_reset_mode": "fixed",
		"quota_daily_reset_hour": float64(9),
		"quota_reset_timezone":   "UTC",
	}}
	// A start three days ago is outside the current daily period.
	periodStart := time.Now().Add(-72 * time.Hour)
	assert.True(t, a.IsFixedDailyPeriodExpired(periodStart))
}

func TestIsFixedDailyPeriodExpired_InvalidTimezone(t *testing.T) {
	a := &Record{LoadLocation: time.LoadLocation, Extra: map[string]any{
		"quota_daily_reset_mode": "fixed",
		"quota_daily_reset_hour": float64(9),
		"quota_reset_timezone":   "Invalid/Timezone",
	}}
	// An invalid timezone falls back to UTC.
	periodStart := time.Now().Add(-72 * time.Hour)
	assert.True(t, a.IsFixedDailyPeriodExpired(periodStart))
}

func TestIsFixedWeeklyPeriodExpired_ZeroPeriodStart(t *testing.T) {
	a := &Record{LoadLocation: time.LoadLocation, Extra: map[string]any{
		"quota_weekly_reset_mode": "fixed",
		"quota_weekly_reset_day":  float64(1),
		"quota_weekly_reset_hour": float64(9),
		"quota_reset_timezone":    "UTC",
	}}
	assert.True(t, a.IsFixedWeeklyPeriodExpired(time.Time{}))
}

func TestIsFixedWeeklyPeriodExpired_NotExpired(t *testing.T) {
	a := &Record{LoadLocation: time.LoadLocation, Extra: map[string]any{
		"quota_weekly_reset_mode": "fixed",
		"quota_weekly_reset_day":  float64(1),
		"quota_weekly_reset_hour": float64(9),
		"quota_reset_timezone":    "UTC",
	}}
	// Start the period at noon UTC today, after the latest Monday 09:00 reset.
	now := time.Now().UTC()
	periodStart := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)
	assert.False(t, a.IsFixedWeeklyPeriodExpired(periodStart))
}

func TestIsFixedWeeklyPeriodExpired_Expired(t *testing.T) {
	a := &Record{LoadLocation: time.LoadLocation, Extra: map[string]any{
		"quota_weekly_reset_mode": "fixed",
		"quota_weekly_reset_day":  float64(1),
		"quota_weekly_reset_hour": float64(9),
		"quota_reset_timezone":    "UTC",
	}}
	// A start ten days ago is outside the current weekly period.
	periodStart := time.Now().Add(-240 * time.Hour)
	assert.True(t, a.IsFixedWeeklyPeriodExpired(periodStart))
}

func TestValidateQuotaResetConfig_NilExtra(t *testing.T) {
	assert.NoError(t, ValidateQuotaResetConfig(nil, time.LoadLocation))
}

func TestValidateQuotaResetConfig_EmptyExtra(t *testing.T) {
	assert.NoError(t, ValidateQuotaResetConfig(map[string]any{}, time.LoadLocation))
}

func TestValidateQuotaResetConfig_ValidFixed(t *testing.T) {
	extra := map[string]any{
		"quota_daily_reset_mode":  "fixed",
		"quota_daily_reset_hour":  float64(9),
		"quota_weekly_reset_mode": "fixed",
		"quota_weekly_reset_day":  float64(1),
		"quota_weekly_reset_hour": float64(0),
		"quota_reset_timezone":    "Asia/Shanghai",
	}
	assert.NoError(t, ValidateQuotaResetConfig(extra, time.LoadLocation))
}

func TestValidateQuotaResetConfig_ValidRolling(t *testing.T) {
	extra := map[string]any{
		"quota_daily_reset_mode":  "rolling",
		"quota_weekly_reset_mode": "rolling",
	}
	assert.NoError(t, ValidateQuotaResetConfig(extra, time.LoadLocation))
}

func TestValidateQuotaResetConfig_InvalidTimezone(t *testing.T) {
	extra := map[string]any{
		"quota_reset_timezone": "Not/A/Timezone",
	}
	err := ValidateQuotaResetConfig(extra, time.LoadLocation)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota_reset_timezone")
}

func TestValidateQuotaResetConfig_InvalidDailyMode(t *testing.T) {
	extra := map[string]any{
		"quota_daily_reset_mode": "invalid",
	}
	err := ValidateQuotaResetConfig(extra, time.LoadLocation)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota_daily_reset_mode")
}

func TestValidateQuotaResetConfig_InvalidDailyHour_TooHigh(t *testing.T) {
	extra := map[string]any{
		"quota_daily_reset_hour": float64(24),
	}
	err := ValidateQuotaResetConfig(extra, time.LoadLocation)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota_daily_reset_hour")
}

func TestValidateQuotaResetConfig_InvalidDailyHour_Negative(t *testing.T) {
	extra := map[string]any{
		"quota_daily_reset_hour": float64(-1),
	}
	err := ValidateQuotaResetConfig(extra, time.LoadLocation)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota_daily_reset_hour")
}

func TestValidateQuotaResetConfig_InvalidWeeklyMode(t *testing.T) {
	extra := map[string]any{
		"quota_weekly_reset_mode": "unknown",
	}
	err := ValidateQuotaResetConfig(extra, time.LoadLocation)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota_weekly_reset_mode")
}

func TestValidateQuotaResetConfig_InvalidWeeklyDay_TooHigh(t *testing.T) {
	extra := map[string]any{
		"quota_weekly_reset_day": float64(7),
	}
	err := ValidateQuotaResetConfig(extra, time.LoadLocation)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota_weekly_reset_day")
}

func TestValidateQuotaResetConfig_InvalidWeeklyDay_Negative(t *testing.T) {
	extra := map[string]any{
		"quota_weekly_reset_day": float64(-1),
	}
	err := ValidateQuotaResetConfig(extra, time.LoadLocation)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota_weekly_reset_day")
}

func TestValidateQuotaResetConfig_InvalidWeeklyHour(t *testing.T) {
	extra := map[string]any{
		"quota_weekly_reset_hour": float64(25),
	}
	err := ValidateQuotaResetConfig(extra, time.LoadLocation)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota_weekly_reset_hour")
}

func TestValidateQuotaResetConfig_BoundaryValues(t *testing.T) {
	// Both ends of each accepted range are valid.
	extra := map[string]any{
		"quota_daily_reset_hour":  float64(23),
		"quota_weekly_reset_day":  float64(0), // Sunday
		"quota_weekly_reset_hour": float64(0),
		"quota_reset_timezone":    "UTC",
	}
	assert.NoError(t, ValidateQuotaResetConfig(extra, time.LoadLocation))

	extra2 := map[string]any{
		"quota_daily_reset_hour":  float64(0),
		"quota_weekly_reset_day":  float64(6), // Saturday
		"quota_weekly_reset_hour": float64(23),
	}
	assert.NoError(t, ValidateQuotaResetConfig(extra2, time.LoadLocation))
}

// TestProviderIsSchedulable_TempUnschedulable checks temporary scheduling blocks and expiry.
func TestProviderIsSchedulable_TempUnschedulable(t *testing.T) {
	future := time.Now().Add(10 * time.Minute)
	past := time.Now().Add(-10 * time.Minute)

	tests := []struct {
		name     string
		provider *Record
		want     bool
	}{
		{
			name: "temp_unschedulable_active",
			provider: &Record{
				LoadLocation: time.LoadLocation, Status: StatusActive,
				Schedulable:            true,
				TempUnschedulableUntil: &future,
			},
			want: false,
		},
		{
			name: "temp_unschedulable_expired",
			provider: &Record{
				LoadLocation: time.LoadLocation, Status: StatusActive,
				Schedulable:            true,
				TempUnschedulableUntil: &past,
			},
			want: true,
		},
		{
			name: "no_temp_unschedulable",
			provider: &Record{
				LoadLocation: time.LoadLocation, Status: StatusActive,
				Schedulable:            true,
				TempUnschedulableUntil: nil,
			},
			want: true,
		},
		{
			name: "temp_unschedulable_with_rate_limit",
			provider: &Record{
				LoadLocation: time.LoadLocation, Status: StatusActive,
				Schedulable:            true,
				TempUnschedulableUntil: &future,
				RateLimitResetAt:       &past,
			},
			want: false, // The temporary block still applies after the rate limit expires.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.provider.IsSchedulable()
			require.Equal(t, tt.want, got)
		})
	}
}

// TestProvider_IsTempUnschedulableEnabled checks the temporary scheduling switch.
func TestProvider_IsTempUnschedulableEnabled(t *testing.T) {
	tests := []struct {
		name     string
		provider *Record
		want     bool
	}{
		{
			name: "enabled",
			provider: &Record{LoadLocation: time.LoadLocation, Credentials: map[string]any{
				"temp_unschedulable_enabled": true,
			}},
			want: true,
		},
		{
			name: "disabled",
			provider: &Record{LoadLocation: time.LoadLocation, Credentials: map[string]any{
				"temp_unschedulable_enabled": false,
			}},
			want: false,
		},
		{
			name:     "not_set",
			provider: &Record{LoadLocation: time.LoadLocation, Credentials: map[string]any{}},
			want:     false,
		},
		{
			name:     "nil_credentials",
			provider: &Record{LoadLocation: time.LoadLocation},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.provider.IsTempUnschedulableEnabled()
			require.Equal(t, tt.want, got)
		})
	}
}

// TestProvider_GetTempUnschedulableRules checks configured and absent rule lists.
func TestProvider_GetTempUnschedulableRules(t *testing.T) {
	tests := []struct {
		name      string
		provider  *Record
		wantCount int
	}{
		{
			name: "has_rules",
			provider: &Record{LoadLocation: time.LoadLocation, Credentials: map[string]any{
				"temp_unschedulable_rules": []any{
					map[string]any{
						"error_code":       float64(503),
						"keywords":         []any{"overloaded"},
						"duration_minutes": float64(5),
					},
					map[string]any{
						"error_code":       float64(500),
						"keywords":         []any{"internal"},
						"duration_minutes": float64(10),
					},
				},
			}},
			wantCount: 2,
		},
		{
			name: "empty_rules",
			provider: &Record{LoadLocation: time.LoadLocation, Credentials: map[string]any{
				"temp_unschedulable_rules": []any{},
			}},
			wantCount: 0,
		},
		{
			name:      "no_rules",
			provider:  &Record{LoadLocation: time.LoadLocation, Credentials: map[string]any{}},
			wantCount: 0,
		},
		{
			name:      "nil_credentials",
			provider:  &Record{LoadLocation: time.LoadLocation},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := tt.provider.GetTempUnschedulableRules()
			require.Len(t, rules, tt.wantCount)
		})
	}
}

// TestTempUnschedulableRule_Parse checks status, keywords, and duration decoded from credentials.
func TestTempUnschedulableRule_Parse(t *testing.T) {
	provider := &Record{LoadLocation: time.LoadLocation, Credentials: map[string]any{
		"temp_unschedulable_rules": []any{
			map[string]any{
				"error_code":       float64(503),
				"keywords":         []any{"overloaded", "capacity"},
				"duration_minutes": float64(5),
			},
		},
	}}

	rules := provider.GetTempUnschedulableRules()
	require.Len(t, rules, 1)

	rule := rules[0]
	require.Equal(t, 503, rule.ErrorCode)
	require.Equal(t, []string{"overloaded", "capacity"}, rule.Keywords)
	require.Equal(t, 5, rule.DurationMinutes)
}

// TestProvider_TempUnschedulableUntil checks active, expired, and absent temporary blocks.
func TestProvider_TempUnschedulableUntil(t *testing.T) {
	future := time.Now().Add(10 * time.Minute)
	past := time.Now().Add(-10 * time.Minute)

	tests := []struct {
		name        string
		provider    *Record
		schedulable bool
	}{
		{
			name: "active_temp_unsched_not_schedulable",
			provider: &Record{
				LoadLocation: time.LoadLocation, Status: StatusActive,
				Schedulable:            true,
				TempUnschedulableUntil: &future,
			},
			schedulable: false,
		},
		{
			name: "expired_temp_unsched_is_schedulable",
			provider: &Record{
				LoadLocation: time.LoadLocation, Status: StatusActive,
				Schedulable:            true,
				TempUnschedulableUntil: &past,
			},
			schedulable: true,
		},
		{
			name: "nil_temp_unsched_is_schedulable",
			provider: &Record{
				LoadLocation: time.LoadLocation, Status: StatusActive,
				Schedulable:            true,
				TempUnschedulableUntil: nil,
			},
			schedulable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.provider.IsSchedulable()
			require.Equal(t, tt.schedulable, got)
		})
	}
}

func TestProvider_GetCredentialAsInt64(t *testing.T) {
	tests := []struct {
		name        string
		credentials map[string]any
		key         string
		expected    int64
	}{
		{
			name:        "int64_value",
			credentials: map[string]any{"_token_version": int64(1737654321000)},
			key:         "_token_version",
			expected:    1737654321000,
		},
		{
			name:        "float64_value",
			credentials: map[string]any{"_token_version": float64(1737654321000)},
			key:         "_token_version",
			expected:    1737654321000,
		},
		{
			name:        "int_value",
			credentials: map[string]any{"_token_version": 12345},
			key:         "_token_version",
			expected:    12345,
		},
		{
			name:        "string_value",
			credentials: map[string]any{"_token_version": "1737654321000"},
			key:         "_token_version",
			expected:    1737654321000,
		},
		{
			name:        "string_with_spaces",
			credentials: map[string]any{"_token_version": "  1737654321000  "},
			key:         "_token_version",
			expected:    1737654321000,
		},
		{
			name:        "nil_credentials",
			credentials: nil,
			key:         "_token_version",
			expected:    0,
		},
		{
			name:        "missing_key",
			credentials: map[string]any{"other_key": 123},
			key:         "_token_version",
			expected:    0,
		},
		{
			name:        "nil_value",
			credentials: map[string]any{"_token_version": nil},
			key:         "_token_version",
			expected:    0,
		},
		{
			name:        "invalid_string",
			credentials: map[string]any{"_token_version": "not_a_number"},
			key:         "_token_version",
			expected:    0,
		},
		{
			name:        "empty_string",
			credentials: map[string]any{"_token_version": ""},
			key:         "_token_version",
			expected:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &Record{Credentials: tt.credentials}
			result := provider.GetCredentialAsInt64(tt.key)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestProvider_GetCredentialAsInt64_NilProvider(t *testing.T) {
	var provider *Record
	result := provider.GetCredentialAsInt64("_token_version")
	require.Equal(t, int64(0), result)
}
