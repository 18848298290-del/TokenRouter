package config

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/viper"
)

// legacy_config.go maps retired configuration keys to current fields while
// honoring configured precedence.
// Deployment files and environment variables can still contain retired field
// names that describe provider pools or scheduling values. Alias binding checks
// the new environment name before the old name, and normal Viper precedence
// puts usable environment values ahead of the YAML layer. Within YAML, an
// existing new field wins even when its value is zero or false.
//
// Absent new YAML keys receive a field-sized nested patch assembled from the
// old key. Merging that patch into configuration storage keeps sibling fields
// and custom map names intact, while the mounted file stays unchanged.
// Pool-isolation enum aliases are normalized separately, and removed Responses
// WS options are registered for presence warnings rather than copied into
// current fields.

// legacyConfigKeys pairs each supported retired dotted configuration key with
// the current field whose loader accepts its value, covering pool limits,
// provider switching, and scheduler defaults.
var legacyConfigKeys = []struct {
	oldKey string // accepted retired dotted key
	newKey string // replacement current dotted key
}{
	{"pricing.hash_check_interval_minutes", "pricing.check_interval_minutes"},
	{"gateway.max_account_switches", "gateway.max_provider_switches"},
	{"gateway.max_account_switches_gemini", "gateway.max_provider_switches_gemini"},
	{"gateway.openai_ws.max_conns_per_account", "gateway.openai_ws.max_conns_per_provider"},
	{"gateway.openai_ws.min_idle_per_account", "gateway.openai_ws.min_idle_per_provider"},
	{"gateway.openai_ws.max_idle_per_account", "gateway.openai_ws.max_idle_per_provider"},
	{"gateway.openai_ws.dynamic_max_conns_by_account_concurrency_enabled", "gateway.openai_ws.dynamic_max_conns_by_provider_concurrency_enabled"},
	{"gateway.openai_ws.lb_top_k", "gateway.advanced_scheduler.lb_top_k"},
	{"gateway.openai_ws.scheduler_score_weights.priority", "gateway.advanced_scheduler.score_weights.priority"},
	{"gateway.openai_ws.scheduler_score_weights.load", "gateway.advanced_scheduler.score_weights.load"},
	{"gateway.openai_ws.scheduler_score_weights.queue", "gateway.advanced_scheduler.score_weights.queue"},
	{"gateway.openai_ws.scheduler_score_weights.error_rate", "gateway.advanced_scheduler.score_weights.error_rate"},
	{"gateway.openai_ws.scheduler_score_weights.ttft", "gateway.advanced_scheduler.score_weights.ttft"},
	{"gateway.openai_ws.scheduler_score_weights.reset", "gateway.advanced_scheduler.score_weights.reset"},
	{"gateway.openai_ws.scheduler_score_weights.quota_headroom", "gateway.advanced_scheduler.score_weights.quota_headroom"},
	{"gateway.openai_ws.scheduler_score_weights.previous_response", "gateway.advanced_scheduler.score_weights.previous_response"},
	{"gateway.openai_ws.scheduler_score_weights.session_sticky", "gateway.advanced_scheduler.score_weights.session_sticky"},
	{"gateway.openai_scheduler.sticky_escape_enabled", "gateway.advanced_scheduler.sticky_escape_enabled"},
	{"gateway.openai_scheduler.sticky_escape_ttft_ms", "gateway.advanced_scheduler.sticky_escape_ttft_ms"},
	{"gateway.openai_scheduler.sticky_escape_error_rate", "gateway.advanced_scheduler.sticky_escape_error_rate"},
}

// applyLegacyConfigCompatibility binds supported aliases and merges missing
// current YAML fields in the process Viper instance. Call it after reading
// configuration and before Unmarshal, with global Viper use serialized by the
// caller.
//
// New and old environment names are bound in that order; Viper's normal
// empty-environment behavior still applies. New YAML key presence blocks old
// YAML replacement, including zero and false values. Otherwise a nested
// per-field patch is merged into memory, leaving sibling values and the
// configuration file available as supplied. Bind or merge failure returns an
// error after any earlier bindings or patches already applied.
// @project-doc docs/interfaces/configuration.md#configuration_sources
func applyLegacyConfigCompatibility() error {
	for _, item := range legacyConfigKeys {
		newEnv := strings.ToUpper(strings.ReplaceAll(item.newKey, ".", "_"))
		oldEnv := strings.ToUpper(strings.ReplaceAll(item.oldKey, ".", "_"))
		if err := viper.BindEnv(item.newKey, newEnv, oldEnv); err != nil {
			return fmt.Errorf("bind configuration aliases for %s: %w", item.newKey, err)
		}
		if viper.InConfig(item.newKey) || !viper.InConfig(item.oldKey) {
			continue
		}
		// Build a patch for this missing current YAML field alone. MergeConfigMap
		// updates that layer while environment lookup remains higher priority and sibling fields remain present.
		parts := strings.Split(item.newKey, ".")
		patch := map[string]any{}
		parent := patch
		for _, part := range parts[:len(parts)-1] {
			child := map[string]any{}
			parent[part] = child
			parent = child
		}
		parent[parts[len(parts)-1]] = viper.Get(item.oldKey)
		if err := viper.MergeConfigMap(patch); err != nil {
			return fmt.Errorf("map configuration %s to %s: %w", item.oldKey, item.newKey, err)
		}
	}
	return nil
}

// normalizeLegacyConnectionPoolIsolation maps the exact retired account and
// account_proxy values to provider equivalents. Other text is returned
// unchanged so normal loading normalization and validation determine whether
// that value is accepted.
func normalizeLegacyConnectionPoolIsolation(value string) string {
	switch value {
	case "account":
		return ConnectionPoolIsolationProvider
	case "account_proxy":
		return ConnectionPoolIsolationProviderProxy
	default:
		return value
	}
}

// warnLegacyResponsesWSSettings registers retired Responses WS keys for
// environment lookup and logs each present key. Presence can include false or
// zero; the function warns about these values while current configuration
// decoding ignores their removed fields.
func warnLegacyResponsesWSSettings() {
	for _, key := range []string{"gateway.openai_ws.allow_store_recovery", "gateway.openai_ws.apikey_enabled", "gateway.openai_ws.enabled", "gateway.openai_ws.event_flush_batch_size", "gateway.openai_ws.event_flush_interval_ms", "gateway.openai_ws.force_http", "gateway.openai_ws.http_bridge_enabled", "gateway.openai_ws.ingress_mode_default", "gateway.openai_ws.mode_router_v2_enabled", "gateway.openai_ws.oauth_enabled", "gateway.openai_ws.payload_log_sample_rate", "gateway.openai_ws.prewarm_generate_enabled", "gateway.openai_ws.responses_websockets", "gateway.openai_ws.responses_websockets_v2", "gateway.openai_ws.retry_backoff_initial_ms", "gateway.openai_ws.retry_backoff_max_ms", "gateway.openai_ws.retry_jitter_ratio", "gateway.openai_ws.retry_total_budget_ms", "gateway.openai_ws.store_disabled_conn_mode", "gateway.openai_ws.store_disabled_force_new_conn"} {
		// A single nonempty key is a valid BindEnv call. Discard its error while
		// registering presence lookup so retired environment variables can still produce operator warnings.
		_ = viper.BindEnv(key)
		if viper.IsSet(key) {
			slog.Warn("Legacy Responses WS parameters are disabled; configure client permissions in group protocols and connection parameters in gateway settings", "key", key)
		}
	}
}
