package config

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// TestLoadLegacyDeploymentConfig checks legacy values and unchanged mounted configuration files.
func TestLoadLegacyDeploymentConfig(t *testing.T) {
	body := `gateway:
  max_account_switches: 0
  max_account_switches_gemini: 7
  connection_pool_isolation: account_proxy
  openai_ws:
    max_conns_per_account: 48
    min_idle_per_account: 0
    max_idle_per_account: 9
    dynamic_max_conns_by_account_concurrency_enabled: false
    lb_top_k: 3
    scheduler_score_weights:
      priority: 1.1
      load: 1.2
      queue: 1.3
      error_rate: 1.4
      ttft: 1.5
      reset: 1.6
      quota_headroom: 1.7
      previous_response: 1.8
      session_sticky: 1.9
  openai_scheduler:
    sticky_escape_enabled: false
    sticky_escape_ttft_ms: 321
    sticky_escape_error_rate: 0
`
	for _, bootstrap := range []bool{false, true} {
		name := "normal"
		if bootstrap {
			name = "bootstrap"
		}
		t.Run(name, func(t *testing.T) {
			path := prepareLegacyConfigTest(t, body)
			loader := Load
			if bootstrap {
				loader = LoadForBootstrap
			}
			cfg, err := loader()
			require.NoError(t, err)
			require.Zero(t, cfg.Gateway.MaxProviderSwitches)
			require.Equal(t, 7, cfg.Gateway.MaxProviderSwitchesGemini)
			require.Equal(t, ConnectionPoolIsolationProviderProxy, cfg.Gateway.ConnectionPoolIsolation)
			require.Equal(t, 48, cfg.Gateway.OpenAIWS.MaxConnsPerProvider)
			require.Zero(t, cfg.Gateway.OpenAIWS.MinIdlePerProvider)
			require.Equal(t, 9, cfg.Gateway.OpenAIWS.MaxIdlePerProvider)
			require.False(t, cfg.Gateway.OpenAIWS.DynamicMaxConnsByProviderConcurrencyEnabled)
			require.Equal(t, 3, cfg.Gateway.AdvancedScheduler.LBTopK)
			require.False(t, cfg.Gateway.AdvancedScheduler.StickyEscapeEnabled)
			require.EqualValues(t, 321, cfg.Gateway.AdvancedScheduler.StickyEscapeTTFTMs)
			require.Zero(t, cfg.Gateway.AdvancedScheduler.StickyEscapeErrorRate)
			weights := cfg.Gateway.AdvancedScheduler.ScoreWeights
			require.Equal(t, []float64{1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 1.8, 1.9}, []float64{
				weights.Priority, weights.Load, weights.Queue, weights.ErrorRate, weights.TTFT,
				weights.Reset, weights.QuotaHeadroom, weights.PreviousResponse, weights.SessionSticky,
			})
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, body, string(content))
		})
	}
}

// TestLoadLegacyConfigPrecedence checks environment-over-YAML and new-name-over-old-name precedence.
func TestLoadLegacyConfigPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, oldEnv, newEnv string
		want                       int
	}{
		{"default", "{}", "", "", 10},
		{"old YAML", "gateway:\n  max_account_switches: 6\n", "", "", 6},
		{"new YAML zero", "gateway:\n  max_account_switches: 6\n  max_provider_switches: 0\n", "", "", 0},
		{"old env zero overrides old YAML", "gateway:\n  max_account_switches: 6\n", "0", "", 0},
		{"old env overrides new YAML", "gateway:\n  max_provider_switches: 6\n", "8", "", 8},
		{"new env overrides old YAML", "gateway:\n  max_account_switches: 6\n", "", "9", 9},
		{"new env zero wins", "gateway:\n  max_provider_switches: 6\n", "8", "0", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareLegacyConfigTest(t, tc.yaml)
			t.Setenv("GATEWAY_MAX_ACCOUNT_SWITCHES", tc.oldEnv)
			t.Setenv("GATEWAY_MAX_PROVIDER_SWITCHES", tc.newEnv)
			cfg, err := Load()
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg.Gateway.MaxProviderSwitches)
		})
	}
}

// TestLoadLegacyConfigEnvironment checks legacy pool values and false switches in environment-only deployments.
func TestLoadLegacyConfigEnvironment(t *testing.T) {
	prepareLegacyConfigTest(t, "{}")
	for key, value := range map[string]string{
		"GATEWAY_MAX_ACCOUNT_SWITCHES_GEMINI":                                "7",
		"GATEWAY_OPENAI_WS_MAX_CONNS_PER_ACCOUNT":                            "48",
		"GATEWAY_OPENAI_WS_MIN_IDLE_PER_ACCOUNT":                             "0",
		"GATEWAY_OPENAI_WS_MAX_IDLE_PER_ACCOUNT":                             "9",
		"GATEWAY_OPENAI_WS_DYNAMIC_MAX_CONNS_BY_ACCOUNT_CONCURRENCY_ENABLED": "false",
		"GATEWAY_OPENAI_WS_LB_TOP_K":                                         "3",
		"GATEWAY_OPENAI_WS_SCHEDULER_SCORE_WEIGHTS_LOAD":                     "0",
		"GATEWAY_OPENAI_SCHEDULER_STICKY_ESCAPE_ENABLED":                     "false",
		"GATEWAY_OPENAI_SCHEDULER_STICKY_ESCAPE_TTFT_MS":                     "321",
		"GATEWAY_OPENAI_SCHEDULER_STICKY_ESCAPE_ERROR_RATE":                  "0",
		"GATEWAY_CONNECTION_POOL_ISOLATION":                                  "account",
	} {
		t.Setenv(key, value)
	}
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 7, cfg.Gateway.MaxProviderSwitchesGemini)
	require.Equal(t, 48, cfg.Gateway.OpenAIWS.MaxConnsPerProvider)
	require.Zero(t, cfg.Gateway.OpenAIWS.MinIdlePerProvider)
	require.Equal(t, 9, cfg.Gateway.OpenAIWS.MaxIdlePerProvider)
	require.False(t, cfg.Gateway.OpenAIWS.DynamicMaxConnsByProviderConcurrencyEnabled)
	require.Equal(t, 3, cfg.Gateway.AdvancedScheduler.LBTopK)
	require.Zero(t, cfg.Gateway.AdvancedScheduler.ScoreWeights.Load)
	require.False(t, cfg.Gateway.AdvancedScheduler.StickyEscapeEnabled)
	require.EqualValues(t, 321, cfg.Gateway.AdvancedScheduler.StickyEscapeTTFTMs)
	require.Zero(t, cfg.Gateway.AdvancedScheduler.StickyEscapeErrorRate)
	require.Equal(t, ConnectionPoolIsolationProvider, cfg.Gateway.ConnectionPoolIsolation)
}

// TestLoadLegacyConfigStillValidatesValues checks that aliases still reject invalid limits and modes.
func TestLoadLegacyConfigStillValidatesValues(t *testing.T) {
	for _, tc := range []struct{ key, value, message string }{
		{"GATEWAY_OPENAI_WS_MAX_CONNS_PER_ACCOUNT", "-1", "max_conns_per_provider must be positive"},
		{"GATEWAY_OPENAI_WS_MAX_CONNS_PER_ACCOUNT", "invalid", "unmarshal config error"},
		{"GATEWAY_CONNECTION_POOL_ISOLATION", "invalid", "connection_pool_isolation must be one of"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			prepareLegacyConfigTest(t, "{}")
			t.Setenv(tc.key, tc.value)
			_, err := Load()
			require.ErrorContains(t, err, tc.message)
		})
	}
}

// TestLegacyConfigPreservesCustomProfileNames checks that account in a custom name is not renamed.
func TestLegacyConfigPreservesCustomProfileNames(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.SetConfigType("yaml")
	require.NoError(t, viper.ReadConfig(strings.NewReader("gateway:\n  tls_fingerprint:\n    profiles:\n      account_gateway:\n        name: custom\n")))
	require.NoError(t, applyLegacyConfigCompatibility())
	require.Equal(t, "custom", viper.GetString("gateway.tls_fingerprint.profiles.account_gateway.name"))
}

// TestLoadLegacyConfigMixedNestedFields checks field-wise merging, explicit zero/false values, and sibling fields.
func TestLoadLegacyConfigMixedNestedFields(t *testing.T) {
	prepareLegacyConfigTest(t, `gateway:
  openai_ws:
    max_conns_per_account: 48
    max_conns_per_provider: 64
    min_idle_per_account: 0
    dynamic_max_conns_by_account_concurrency_enabled: true
    dynamic_max_conns_by_provider_concurrency_enabled: false
    scheduler_score_weights:
      priority: 11
      load: 1.2
      queue: 3
  openai_scheduler:
    sticky_escape_enabled: true
  advanced_scheduler:
    ewma_ttft_alpha: 0.7
    sticky_escape_enabled: false
    score_weights:
      priority: 0
      queue: 5
`)
	t.Setenv("GATEWAY_OPENAI_WS_SCHEDULER_SCORE_WEIGHTS_QUEUE", "7")
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 64, cfg.Gateway.OpenAIWS.MaxConnsPerProvider)
	require.Zero(t, cfg.Gateway.OpenAIWS.MinIdlePerProvider)
	require.False(t, cfg.Gateway.OpenAIWS.DynamicMaxConnsByProviderConcurrencyEnabled)
	require.False(t, cfg.Gateway.AdvancedScheduler.StickyEscapeEnabled)
	require.Equal(t, 0.7, cfg.Gateway.AdvancedScheduler.EWMATTFTAlpha)
	require.Zero(t, cfg.Gateway.AdvancedScheduler.ScoreWeights.Priority)
	require.Equal(t, 1.2, cfg.Gateway.AdvancedScheduler.ScoreWeights.Load)
	require.Equal(t, 7.0, cfg.Gateway.AdvancedScheduler.ScoreWeights.Queue)

	// Reload from the changed file instead of reusing in-memory values from the previous load.
	require.NoError(t, os.WriteFile(os.Getenv("CONFIG_FILE"), []byte("gateway:\n  max_provider_switches: 4\n"), 0o600))
	cfg, err = Load()
	require.NoError(t, err)
	require.Equal(t, 4, cfg.Gateway.MaxProviderSwitches)
	require.Equal(t, 128, cfg.Gateway.OpenAIWS.MaxConnsPerProvider)
	require.True(t, cfg.Gateway.OpenAIWS.DynamicMaxConnsByProviderConcurrencyEnabled)
	require.Equal(t, 1.0, cfg.Gateway.AdvancedScheduler.ScoreWeights.Load)
}
