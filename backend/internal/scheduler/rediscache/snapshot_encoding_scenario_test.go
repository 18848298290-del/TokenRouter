package rediscache

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache/codec"
)

// Candidate metadata needs protocol, subscription, model, quota and group
// identity before full provider hydration, while large execution-only values
// are filtered away. These cases compare retained eligibility behavior and
// historical full/metadata bytes, including nil/empty collections and
// configured model restrictions after JSON round trips.

// TestFilterSchedulerCredentialsKeepsSubscriptionPlanType supplies a
// subscription plan beside access and refresh tokens. It checks that the plan
// remains available while those execution token fields are filtered away.
func TestFilterSchedulerCredentialsKeepsSubscriptionPlanType(t *testing.T) {
	filtered := filterSchedulerCredentials(map[string]any{
		"plan_type":     "plus",
		"access_token":  "secret-access-token",
		"refresh_token": "secret-refresh-token",
	})

	require.Equal(t, "plus", filtered["plan_type"])
	require.NotContains(t, filtered, "access_token")
	require.NotContains(t, filtered, "refresh_token")
}

// TestSchedulerMetadataProviderKeepsOpenAISubscriptionIdentity builds
// metadata for an OAuth provider with a Plus plan. It checks subscription
// recognition and an empty access-token lookup on the filtered record.
func TestSchedulerMetadataProviderKeepsOpenAISubscriptionIdentity(t *testing.T) {
	provider := providercore.Record{
		ID:       24,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"plan_type":    "plus",
			"access_token": "secret-access-token",
		},
	}

	metadata := buildSchedulerMetadataProvider(provider)

	require.True(t, metadata.IsOpenAIChatGPTSubscription())
	require.Empty(t, metadata.GetCredential("access_token"))
}

// TestMarshalSchedulerCacheProviderKeepsEncodingJSONWireFormat covers nil
// collections, empty collections and nested values requiring JSON escaping.
// It checks full and metadata payload bytes against fixed historical fixtures
// for each named case.
func TestMarshalSchedulerCacheProviderKeepsEncodingJSONWireFormat(t *testing.T) {
	cases := []struct {
		// name and provider pair a wire-shape fixture with its subtest identity;
		// historicalSchedulerPayload uses that test name to locate the fixed full
		// and metadata byte files.
		name     string              // historical encoding scenario name
		provider providercore.Record // provider representation fixture input
	}{
		{name: "nil collections", provider: providercore.Record{ID: 801}},
		{name: "empty collections", provider: providercore.Record{
			ID:          802,
			Credentials: map[string]any{},
			Extra:       map[string]any{},
			GroupIDs:    []int64{},
			Groups:      []*accessview.GroupConfig{},
		}},
		{name: "nested maps and escaping", provider: providercore.Record{
			ID:          803,
			Credentials: map[string]any{"model_mapping": map[string]any{"z": "<last>", "a": "&first"}},
			Extra:       map[string]any{"openai_passthrough": true},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			full, meta, err := marshalSchedulerCacheProvider(tc.provider)
			require.NoError(t, err)
			wantFull := historicalSchedulerPayload(t, "wantFull")
			wantMeta := historicalSchedulerPayload(t, "wantMeta")
			require.Equal(t, wantFull, full)
			require.Equal(t, wantMeta, meta)
		})
	}
}

// TestBuildSchedulerMetadataProvider_KeepsOpenAIAPIKeyProtocolFields supplies
// current routing/compaction fields beside legacy flags and an access token.
// It checks retained selection inputs and filtering of obsolete or unrelated
// values.
func TestBuildSchedulerMetadataProvider_KeepsOpenAIAPIKeyProtocolFields(t *testing.T) {
	provider := providercore.Record{
		ID:       42,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"openai_workload_capabilities": []any{"text_generation"},
			"access_token":                 "drop-me",
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
			"responses_ws_connection_mode":                  "per_session",
			"openai_ws_force_http":                          true,
			"openai_text_route_mode":                        "force_chat_completions",
			"openai_responses_probe_status":                 "unsupported",
			"openai_compact_mode":                           "force_off",
			"openai_native_compaction_v2_mode":              "force_on",
			"openai_responses_continuation_supported":       true,
			"mixed_scheduling":                              true,
			"unused_large_field":                            "drop-me",
		},
	}

	got := buildSchedulerMetadataProvider(provider)

	require.NotContains(t, got.Extra, "openai_apikey_responses_websockets_v2_enabled")
	require.Equal(t, "per_session", got.Extra["responses_ws_connection_mode"])
	require.NotContains(t, got.Extra, "openai_ws_force_http")
	require.Equal(t, "force_chat_completions", got.Extra["openai_text_route_mode"])
	require.NotContains(t, got.Extra, "openai_responses_probe_status")
	require.Equal(t, "force_off", got.Extra["openai_compact_mode"])
	require.Equal(t, "force_on", got.Extra["openai_native_compaction_v2_mode"])
	require.Equal(t, true, got.Extra["openai_responses_continuation_supported"])
	require.NotContains(t, got.Extra, "mixed_scheduling")
	require.Nil(t, got.Extra["unused_large_field"])
	require.Equal(t, []any{"text_generation"}, got.Credentials["openai_workload_capabilities"])
	require.Nil(t, got.Credentials["access_token"])
}

// TestBuildSchedulerMetadataProvider_KeepsGrokMediaEligibility supplies an
// administrator denial and a forbidden billing observation in separate cases.
// It checks their filtered records still reject media generation with the
// corresponding reasons.
func TestBuildSchedulerMetadataProvider_KeepsGrokMediaEligibility(t *testing.T) {
	t.Run("explicit override", func(t *testing.T) {
		provider := providercore.Record{
			ID:       43,
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeOAuth,
			Extra: map[string]any{
				providercore.GrokMediaEligibleExtraKey: false,
				"unused_large_field":                   "drop-me",
			},
		}

		got := buildSchedulerMetadataProvider(provider)

		eligible, reason := providercore.GrokMediaGenerationEligibility(&got, provideradapter.GrokTierRules())
		require.False(t, eligible)
		require.Equal(t, "override_disabled", reason)
		require.Equal(t, false, got.Extra[providercore.GrokMediaEligibleExtraKey])
		require.Nil(t, got.Extra["unused_large_field"])
	})

	t.Run("forbidden billing observation", func(t *testing.T) {
		provider := providercore.Record{
			ID:       44,
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"grok_billing_snapshot": map[string]any{
					"status_code":         200,
					"weekly_status_code":  403,
					"monthly_status_code": 200,
				},
			},
		}

		got := buildSchedulerMetadataProvider(provider)

		eligible, reason := providercore.GrokMediaGenerationEligibility(&got, provideradapter.GrokTierRules())
		require.False(t, eligible)
		require.Equal(t, "billing_forbidden", reason)
		require.NotNil(t, got.Extra["grok_billing_snapshot"])
	})
}

// TestBuildSchedulerMetadataProvider_KeepsSlimGroupMembership supplies
// duplicate/invalid direct IDs plus hydrated membership records. It checks
// merged positive group IDs and scalar memberships whose nested
// provider/group graphs are filtered away.
func TestBuildSchedulerMetadataProvider_KeepsSlimGroupMembership(t *testing.T) {
	provider := providercore.Record{
		ID:       42,
		Platform: capability.PlatformAnthropic,
		GroupIDs: []int64{7, 9, 7, 0},
		ProviderGroups: []providercore.GroupMembership{
			{
				ProviderID: 42,
				GroupID:    7,
				Provider:   &providercore.Record{ID: 42, Name: "drop-from-metadata"},
				Group:      &accessview.GroupConfig{ID: 7, Name: "drop-from-metadata"},
			},
			{
				ProviderID: 42,
				GroupID:    11,
				Group:      &accessview.GroupConfig{ID: 11, Name: "drop-from-metadata"},
			},
			{
				ProviderID: 42,
				GroupID:    0,
			},
		},
	}

	got := buildSchedulerMetadataProvider(provider)

	require.Equal(t, []int64{7, 9, 11}, got.GroupIDs)
	require.Len(t, got.ProviderGroups, 2)
	require.Equal(t, int64(42), got.ProviderGroups[0].ProviderID)
	require.Equal(t, int64(7), got.ProviderGroups[0].GroupID)
	require.Nil(t, got.ProviderGroups[0].Provider)
	require.Nil(t, got.ProviderGroups[0].Group)
	require.Equal(t, int64(11), got.ProviderGroups[1].GroupID)
	require.Nil(t, got.Groups)
}

// TestBuildSchedulerMetadataProvider_KeepsQuotaAutoPauseFields supplies usage
// percentages, resets, observation time and pause overrides. It checks each
// supplied observation survives metadata filtering for later quota-based
// candidate checks.
func TestBuildSchedulerMetadataProvider_KeepsQuotaAutoPauseFields(t *testing.T) {
	provider := providercore.Record{
		ID: 88,
		Extra: map[string]any{
			"codex_5h_used_percent":        12.34,
			"codex_7d_used_percent":        56.78,
			"codex_5h_reset_at":            "2026-05-29T10:00:00Z",
			"codex_7d_reset_at":            "2026-06-01T10:00:00Z",
			"codex_5h_reset_after_seconds": 300,
			"codex_7d_reset_after_seconds": 600,
			"codex_usage_updated_at":       "2026-05-29T09:00:00Z",
			"auto_pause_5h_threshold":      0.95,
			"auto_pause_7d_threshold":      0.96,
			"auto_pause_5h_disabled":       true,
			"auto_pause_7d_disabled":       false,
		},
	}

	got := buildSchedulerMetadataProvider(provider)

	require.Equal(t, 12.34, got.Extra["codex_5h_used_percent"])
	require.Equal(t, 56.78, got.Extra["codex_7d_used_percent"])
	require.Equal(t, "2026-05-29T10:00:00Z", got.Extra["codex_5h_reset_at"])
	require.Equal(t, "2026-06-01T10:00:00Z", got.Extra["codex_7d_reset_at"])
	require.Equal(t, 300, got.Extra["codex_5h_reset_after_seconds"])
	require.Equal(t, 600, got.Extra["codex_7d_reset_after_seconds"])
	require.Equal(t, "2026-05-29T09:00:00Z", got.Extra["codex_usage_updated_at"])
	require.Equal(t, 0.95, got.Extra["auto_pause_5h_threshold"])
	require.Equal(t, 0.96, got.Extra["auto_pause_7d_threshold"])
	require.Equal(t, true, got.Extra["auto_pause_5h_disabled"])
	require.Equal(t, false, got.Extra["auto_pause_7d_disabled"])
}

// TestBuildSchedulerMetadataProvider_KeepsModelRateLimits supplies two keyed
// model cooldown observations and an unrelated extra value. It checks both
// model keys remain while the unrelated field is filtered away.
func TestBuildSchedulerMetadataProvider_KeepsModelRateLimits(t *testing.T) {
	provider := providercore.Record{
		ID:       90,
		Platform: capability.PlatformAntigravity,
		Extra: map[string]any{
			"model_rate_limits": map[string]any{
				"gemini-3-flash": map[string]any{
					"rate_limit_reset_at": "2026-05-30T10:10:00Z",
				},
				"antigravity:gemini": map[string]any{
					"rate_limit_reset_at": "2026-05-30T10:10:00Z",
				},
			},
			"unused_large_field": "drop-me",
		},
	}

	got := buildSchedulerMetadataProvider(provider)

	limits, ok := got.Extra["model_rate_limits"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, limits, "gemini-3-flash")
	require.Contains(t, limits, "antigravity:gemini")
	require.Nil(t, got.Extra["unused_large_field"])
}

// TestBuildSchedulerMetadataProvider_KeepsSparkShadowRoutingIdentity supplies
// shadow-parent identity, quota dimension and ordinary/compact model
// mappings. It checks retained routing associations while filtering the
// fixture access token.
func TestBuildSchedulerMetadataProvider_KeepsSparkShadowRoutingIdentity(t *testing.T) {
	parentID := int64(100)
	provider := providercore.Record{
		ID:               200,
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		ParentProviderID: &parentID,
		QuotaDimension:   providercore.QuotaDimensionSpark,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gpt-5.3-codex-spark": "gpt-5.3-codex-spark",
			},
			"compact_model_mapping": map[string]any{
				"gpt-5.4": "gpt-5.4-openai-compact",
			},
			"access_token": "drop-me",
		},
	}

	got := buildSchedulerMetadataProvider(provider)

	require.NotNil(t, got.ParentProviderID)
	require.Equal(t, parentID, *got.ParentProviderID)
	require.Equal(t, providercore.QuotaDimensionSpark, got.QuotaDimension)
	require.Equal(t, map[string]any{"gpt-5.3-codex-spark": "gpt-5.3-codex-spark"}, got.Credentials["model_mapping"])
	require.Equal(t, map[string]any{"gpt-5.4": "gpt-5.4-openai-compact"}, got.Credentials["compact_model_mapping"])
	require.Nil(t, got.Credentials["access_token"])
}

// TestBuildSchedulerMetadataProvider_KeepsExplicitModelScopeForPassthrough
// enables each passthrough flag with a restrictive model map and whitelist.
// It checks the unsupported model remains rejected after filtered metadata is
// serialized and decoded.
func TestBuildSchedulerMetadataProvider_KeepsExplicitModelScopeForPassthrough(t *testing.T) {
	for _, key := range []string{"openai_passthrough", "openai_oauth_passthrough"} {
		t.Run(key, func(t *testing.T) {
			provider := providercore.Record{
				ID:       383,
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeOAuth,
				Credentials: map[string]any{
					// A passthrough provider can still have a model whitelist and mapping that omit the requested model.
					"model_whitelist": []string{"gpt-5.5"},
					"model_mapping":   map[string]any{"gpt-5.5": "gpt-5.5"},
					"access_token":    "drop-me",
				},
				Extra: map[string]any{key: true},
			}
			require.False(t, provider.IsModelSupported("gpt-5.6-sol", provideradapter.ModelDefaults(), provideradapter.ModelRules(&provider)),
				"透传提供商仍受明确的模型范围限制")

			meta := buildSchedulerMetadataProvider(provider)

			// Serialize the filtered record before checking eligibility so the
			// assertion includes cached restoration of passthrough flags, configured
			// mappings and whitelist scope.
			payload, err := codec.MarshalProviderRecord(&meta)
			require.NoError(t, err)
			restored, err := codec.UnmarshalProviderRecord(payload)
			require.NoError(t, err)

			require.Equal(t, true, restored.Extra[key])
			require.True(t, restored.IsOpenAIPassthroughEnabled())
			require.False(t, restored.IsModelSupported("gpt-5.6-sol", provideradapter.ModelDefaults(), provideradapter.ModelRules(restored)),
				"缓存恢复不能放宽提供商模型范围")
			require.Equal(t, map[string]any{"gpt-5.5": "gpt-5.5"}, restored.Credentials["model_mapping"])
		})
	}
}

// TestSchedulerProtocolProjection supplies WebSocket protocol and
// personal-access-token authentication metadata beside a secret token. It
// checks retained protocol/authentication behavior, token filtering, Live
// exclusion and an empty protocol list after replacement.
func TestSchedulerProtocolProjection(t *testing.T) {
	provider := providercore.Record{ID: 72, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"upstream_protocols": []string{"openai_responses_websocket"}, "auth_mode": "personalAccessToken", "access_token": "hidden"}}
	metadata := buildSchedulerMetadataProvider(provider)
	require.Equal(t, []protocol.ProtocolID{"openai_responses_websocket"}, metadata.UpstreamProtocols())
	require.True(t, metadata.IsOpenAIPersonalAccessToken())
	require.NotContains(t, metadata.Credentials, "access_token")
	require.NotContains(t, metadata.NativeProtocolOptions(), protocol.ProtocolID("openai_live"))
	provider.Credentials["upstream_protocols"] = []string{}
	metadata = buildSchedulerMetadataProvider(provider)
	require.Empty(t, metadata.UpstreamProtocols())
}

// filterSchedulerCredentials delegates credential selection through
// ProviderCodec.Metadata for these fixtures. The returned outer map contains
// selected values, while any retained nested mutable values stay shared with
// the supplied input.
func filterSchedulerCredentials(value map[string]any) map[string]any {
	return buildSchedulerMetadataProvider(providercore.Record{Credentials: value}).Credentials
}

// buildSchedulerMetadataProvider delegates fixture metadata creation to the
// production provider codec. Its output retains selected nested values and
// scalar pointers, so callers treat those references as shared.
func buildSchedulerMetadataProvider(value providercore.Record) providercore.Record {
	return (codec.ProviderCodec{}).Metadata(value)
}
