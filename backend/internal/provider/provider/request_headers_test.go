package provider

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// TestHeaderOverrideResultIsolation checks that mutating one result leaves other requests unchanged.
func TestHeaderOverrideResultIsolation(t *testing.T) {
	provider := headerOverrideProvider()
	provider.HeaderOverrides()["x-test-marker"] = "changed-by-caller"
	require.Equal(t, "configured", provider.HeaderOverrides()["x-test-marker"])
}

// TestHeaderOverrideConcurrentReads checks that concurrent reads do not mutate shared configuration.
func TestHeaderOverrideConcurrentReads(t *testing.T) {
	provider := headerOverrideProvider()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			<-start
			for range 20 {
				if provider.HeaderOverrides()["x-test-marker"] != "configured" {
					t.Error("请求头覆写读取结果发生变化")
				}
			}
		})
	}
	close(start)
	wg.Wait()
}

func TestIsHeaderOverrideEligible(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		accType  string
		want     bool
	}{
		{"anthropic apikey", capability.PlatformAnthropic, capability.ProviderTypeAPIKey, true},
		{"openai apikey", capability.PlatformOpenAI, capability.ProviderTypeAPIKey, true},
		{"kimi apikey", capability.PlatformKimi, capability.ProviderTypeAPIKey, true},
		{"zhipu apikey", capability.PlatformZhipu, capability.ProviderTypeAPIKey, true},
		{"deepseek apikey", capability.PlatformDeepseek, capability.ProviderTypeAPIKey, true},
		{"kimi oauth", capability.PlatformKimi, capability.ProviderTypeOAuth, false},
		{"zhipu oauth", capability.PlatformZhipu, capability.ProviderTypeOAuth, false},
		{"deepseek oauth", capability.PlatformDeepseek, capability.ProviderTypeOAuth, false},
		{"anthropic oauth", capability.PlatformAnthropic, capability.ProviderTypeOAuth, false},
		{"openai oauth", capability.PlatformOpenAI, capability.ProviderTypeOAuth, false},
		{"gemini apikey", capability.PlatformGemini, capability.ProviderTypeAPIKey, false},
		{"grok apikey", capability.PlatformGrok, capability.ProviderTypeAPIKey, true},
		{"grok oauth", capability.PlatformGrok, capability.ProviderTypeOAuth, true},
		{"antigravity apikey", capability.PlatformAntigravity, capability.ProviderTypeAPIKey, false},
		{"anthropic bedrock", capability.PlatformAnthropic, capability.ProviderTypeBedrock, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc := headerOverrideTestProvider(tt.platform, tt.accType, nil)
			require.Equal(t, tt.want, acc.IsHeaderOverrideEligible())
		})
	}

	var nilProvider *provider.Record
	require.False(t, nilProvider.IsHeaderOverrideEligible())
	require.False(t, nilProvider.IsHeaderOverrideEnabled())
	require.Nil(t, nilProvider.HeaderOverrides())
}

func TestIsHeaderOverrideEnabled(t *testing.T) {
	acc := headerOverrideTestProvider(capability.PlatformAnthropic, capability.ProviderTypeAPIKey, map[string]any{
		"header_override_enabled": true,
	})
	require.True(t, acc.IsHeaderOverrideEnabled())

	// Absent, non-boolean, and false values disable overrides.
	require.False(t, headerOverrideTestProvider(capability.PlatformAnthropic, capability.ProviderTypeAPIKey, nil).IsHeaderOverrideEnabled())
	require.False(t, headerOverrideTestProvider(capability.PlatformAnthropic, capability.ProviderTypeAPIKey, map[string]any{
		"header_override_enabled": "true",
	}).IsHeaderOverrideEnabled())
	require.False(t, headerOverrideTestProvider(capability.PlatformAnthropic, capability.ProviderTypeAPIKey, map[string]any{
		"header_override_enabled": false,
	}).IsHeaderOverrideEnabled())

	// Unsupported platform/type combinations ignore the enabled flag.
	require.False(t, headerOverrideTestProvider(capability.PlatformAnthropic, capability.ProviderTypeOAuth, map[string]any{
		"header_override_enabled": true,
	}).IsHeaderOverrideEnabled())
	require.False(t, headerOverrideTestProvider(capability.PlatformGemini, capability.ProviderTypeAPIKey, map[string]any{
		"header_override_enabled": true,
	}).IsHeaderOverrideEnabled())
}

func TestGetHeaderOverrides(t *testing.T) {
	acc := headerOverrideTestProvider(capability.PlatformOpenAI, capability.ProviderTypeAPIKey, map[string]any{
		"header_override_enabled": true,
		"header_overrides": map[string]any{
			"User-Agent":    "my-agent/1.0",  // Normalize uppercase names to lowercase.
			" X-App ":       "cli",           // Trim header names.
			"x-empty":       "",              // Skip empty template placeholders.
			"authorization": "Bearer leaked", // Skip forbidden overrides.
			"bad name":      "value",         // Skip invalid header names.
			"x-padded":      "  padded  ",    // Trim values.
		},
	})
	overrides := acc.HeaderOverrides()
	require.Equal(t, map[string]string{
		"user-agent": "my-agent/1.0",
		"x-app":      "cli",
		"x-padded":   "padded",
	}, overrides)

	// Disabled overrides return nil.
	disabled := headerOverrideTestProvider(capability.PlatformOpenAI, capability.ProviderTypeAPIKey, map[string]any{
		"header_overrides": map[string]any{"user-agent": "x"},
	})
	require.Nil(t, disabled.HeaderOverrides())

	// An enabled configuration with only empty values returns nil.
	empty := headerOverrideTestProvider(capability.PlatformOpenAI, capability.ProviderTypeAPIKey, map[string]any{
		"header_override_enabled": true,
		"header_overrides":        map[string]any{"user-agent": ""},
	})
	require.Nil(t, empty.HeaderOverrides())

	// Reject oversized values and WebSocket handshake headers even when storage bypassed normalization.
	oversizedValue := strings.Repeat("a", 8192+1)
	defensive := headerOverrideTestProvider(capability.PlatformOpenAI, capability.ProviderTypeAPIKey, map[string]any{
		"header_override_enabled": true,
		"header_overrides": map[string]any{
			"x-big":                    oversizedValue,
			"sec-websocket-key":        "forged",
			"content-type":             "application/json", // Reject stored headers that became forbidden after the configuration was saved.
			"x-claude-code-session-id": "pinned-session",
			"x-ok":                     "ok",
		},
	})
	require.Equal(t, map[string]string{"x-ok": "ok"}, defensive.HeaderOverrides())
}

func TestApplyHeaderOverrides(t *testing.T) {
	acc := headerOverrideTestProvider(capability.PlatformAnthropic, capability.ProviderTypeAPIKey, map[string]any{
		"header_override_enabled": true,
		"header_overrides": map[string]any{
			"user-agent":     "override-agent/2.0",
			"anthropic-beta": "custom-beta-1",
			"x-custom":       "custom-value",
		},
	})

	h := http.Header{}
	// Model forwarded headers containing both canonical and wire-cased keys.
	h.Set("User-Agent", "claude-cli/2.1.161 (external, cli)")
	h["anthropic-beta"] = []string{"claude-code-20250219,oauth-2025-04-20"} // noncanonical wire key
	h.Set("Content-Type", "application/json")
	ApplyProviderHeaderOverrides(acc, h)

	// The UA override replaces all existing values and uses the known wire casing.
	require.Equal(t, []string{"override-agent/2.0"}, h["User-Agent"])
	// Remove noncanonical anthropic-beta entries and write one lowercase wire key.
	require.Equal(t, []string{"custom-beta-1"}, map[string][]string(h)["anthropic-beta"])
	require.Empty(t, h["Anthropic-Beta"])
	// Unknown header names use lowercase wire keys.
	require.Equal(t, []string{"custom-value"}, map[string][]string(h)["x-custom"])
	require.Equal(t, "custom-value", anthropic.GetHeaderRaw(h, "x-custom"))
	// Unchanged headers retain their values.
	require.Equal(t, "application/json", h.Get("Content-Type"))

	// Overrides leave no duplicate keys with different casing.
	count := 0
	for k := range h {
		if k == "anthropic-beta" || k == "Anthropic-Beta" {
			count++
		}
	}
	require.Equal(t, 1, count)
}

func TestApplyHeaderOverridesNoOpPaths(t *testing.T) {
	baseline := func() http.Header {
		h := http.Header{}
		h.Set("User-Agent", "orig")
		return h
	}

	// OAuth providers ignore configured overrides.
	oauth := headerOverrideTestProvider(capability.PlatformAnthropic, capability.ProviderTypeOAuth, map[string]any{
		"header_override_enabled": true,
		"header_overrides":        map[string]any{"user-agent": "hacked"},
	})
	h := baseline()
	ApplyProviderHeaderOverrides(oauth, h)
	require.Equal(t, "orig", h.Get("User-Agent"))

	// Overrides disabled.
	off := headerOverrideTestProvider(capability.PlatformAnthropic, capability.ProviderTypeAPIKey, map[string]any{
		"header_overrides": map[string]any{"user-agent": "hacked"},
	})
	h = baseline()
	ApplyProviderHeaderOverrides(off, h)
	require.Equal(t, "orig", h.Get("User-Agent"))

	// Authentication and host headers cannot be overridden.
	blocked := headerOverrideTestProvider(capability.PlatformOpenAI, capability.ProviderTypeAPIKey, map[string]any{
		"header_override_enabled": true,
		"header_overrides": map[string]any{
			"Authorization":  "Bearer evil",
			"X-Api-Key":      "evil",
			"Host":           "evil.example.com",
			"Content-Length": "0",
		},
	})
	h = http.Header{}
	h.Set("Authorization", "Bearer real-key")
	ApplyProviderHeaderOverrides(blocked, h)
	require.Equal(t, "Bearer real-key", h.Get("Authorization"))
	require.Empty(t, h.Get("X-Api-Key"))
	require.Empty(t, h.Get("Host"))

	// A nil header map is safe.
	ApplyProviderHeaderOverrides(blocked, nil)
}

func TestNormalizeHeaderOverrideCredentials(t *testing.T) {
	t.Run("nil credentials no-op", func(t *testing.T) {
		require.NoError(t, egress.NormalizeHeaderOverrideCredentials(nil))
	})

	t.Run("missing keys no-op", func(t *testing.T) {
		creds := map[string]any{"api_key": "sk-xxx"}
		require.NoError(t, egress.NormalizeHeaderOverrideCredentials(creds))
		_, exists := creds["header_overrides"]
		require.False(t, exists)
	})

	t.Run("normalizes names and values", func(t *testing.T) {
		creds := map[string]any{
			"header_override_enabled": true,
			"header_overrides": map[string]any{
				" User-Agent ": " my-agent ",
				"X-App":        "",
				"":             "", // Drop entirely empty rows.
			},
		}
		require.NoError(t, egress.NormalizeHeaderOverrideCredentials(creds))
		require.Equal(t, map[string]any{
			"user-agent": "my-agent",
			"x-app":      "",
		}, creds["header_overrides"])
	})

	t.Run("accepts map[string]string input", func(t *testing.T) {
		creds := map[string]any{
			"header_overrides": map[string]string{"X-App": "cli"},
		}
		require.NoError(t, egress.NormalizeHeaderOverrideCredentials(creds))
		require.Equal(t, map[string]any{"x-app": "cli"}, creds["header_overrides"])
	})

	t.Run("rejects non-bool enabled", func(t *testing.T) {
		err := egress.NormalizeHeaderOverrideCredentials(map[string]any{
			"header_override_enabled": "yes",
		})
		require.Error(t, err)
	})

	t.Run("rejects non-object overrides", func(t *testing.T) {
		err := egress.NormalizeHeaderOverrideCredentials(map[string]any{
			"header_overrides": []any{"user-agent"},
		})
		require.Error(t, err)
	})

	t.Run("rejects non-string value", func(t *testing.T) {
		err := egress.NormalizeHeaderOverrideCredentials(map[string]any{
			"header_overrides": map[string]any{"x-app": 123},
		})
		require.Error(t, err)
	})

	t.Run("rejects invalid header name", func(t *testing.T) {
		for _, name := range []string{"bad name", "bad:name", "bad\nname", "值"} {
			err := egress.NormalizeHeaderOverrideCredentials(map[string]any{
				"header_overrides": map[string]any{name: "v"},
			})
			require.Error(t, err, "name %q should be rejected", name)
		}
	})

	t.Run("rejects empty name with value", func(t *testing.T) {
		err := egress.NormalizeHeaderOverrideCredentials(map[string]any{
			"header_overrides": map[string]any{"  ": "v"},
		})
		require.Error(t, err)
	})

	t.Run("rejects blocked headers", func(t *testing.T) {
		for _, name := range []string{
			"Authorization", "x-api-key", "Host", "content-length", "Transfer-Encoding",
			"connection", "accept-encoding", "Sec-WebSocket-Key", "session_id",
			"conversation_id", "x-codex-turn-state", "chatgpt-account-id",
			"Content-Type", "Cookie", "x-goog-api-key",
			"X-Claude-Code-Session-Id", "x-client-request-id",
		} {
			err := egress.NormalizeHeaderOverrideCredentials(map[string]any{
				"header_overrides": map[string]any{name: "v"},
			})
			require.Error(t, err, "blocked header %q should be rejected", name)
		}
	})

	t.Run("allows tab inside value", func(t *testing.T) {
		creds := map[string]any{
			"header_overrides": map[string]any{"x-app": "a\tb"},
		}
		require.NoError(t, egress.NormalizeHeaderOverrideCredentials(creds))
		require.Equal(t, map[string]any{"x-app": "a\tb"}, creds["header_overrides"])
	})

	t.Run("rejects invalid value", func(t *testing.T) {
		err := egress.NormalizeHeaderOverrideCredentials(map[string]any{
			"header_overrides": map[string]any{"x-app": "bad\nvalue"},
		})
		require.Error(t, err)
	})

	t.Run("rejects duplicate names case-insensitively", func(t *testing.T) {
		err := egress.NormalizeHeaderOverrideCredentials(map[string]any{
			"header_overrides": map[string]any{
				"User-Agent": "a",
				"user-agent": "b",
			},
		})
		require.Error(t, err)
	})

	t.Run("rejects too many entries", func(t *testing.T) {
		entries := make(map[string]any, 64+1)
		for i := range 65 {
			entries["x-h-"+string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+string(rune('a'+(i/676)%26))] = "v"
		}
		err := egress.NormalizeHeaderOverrideCredentials(map[string]any{
			"header_overrides": entries,
		})
		require.Error(t, err)
	})

	t.Run("rejects oversized value", func(t *testing.T) {
		big := make([]byte, 8192+1)
		for i := range big {
			big[i] = 'a'
		}
		err := egress.NormalizeHeaderOverrideCredentials(map[string]any{
			"header_overrides": map[string]any{"x-app": string(big)},
		})
		require.Error(t, err)
	})
}

func headerOverrideProvider() *provider.Record {
	return &provider.Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{
		"header_override_enabled": true, "header_overrides": map[string]any{"x-test-marker": "configured"},
	}}
}

func headerOverrideTestProvider(platform, providerType string, credentials map[string]any) *provider.Record {
	return &provider.Record{
		Platform:    platform,
		Type:        providerType,
		Credentials: credentials,
	}
}
