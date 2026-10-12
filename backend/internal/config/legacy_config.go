package config

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/viper"
)

// legacyConfigKeys maps retired deployment keys to their replacements.
var legacyConfigKeys = []struct {
	oldKey string
	newKey string
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

// applyLegacyConfigCompatibility resolves old names in memory during configuration loading.
// Environment values precede YAML; new names win within each source. Presence checks retain zero and false.
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
		// Merge into the in-memory YAML layer so environment variables retain precedence over the aliases.
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

// normalizeLegacyConnectionPoolIsolation maps old names to the same pool isolation granularity.
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

// warnLegacyResponsesWSSettings directs operators to group protocols for client WS permissions.
func warnLegacyResponsesWSSettings() {
	for _, key := range []string{"gateway.openai_ws.allow_store_recovery", "gateway.openai_ws.apikey_enabled", "gateway.openai_ws.enabled", "gateway.openai_ws.event_flush_batch_size", "gateway.openai_ws.event_flush_interval_ms", "gateway.openai_ws.force_http", "gateway.openai_ws.http_bridge_enabled", "gateway.openai_ws.ingress_mode_default", "gateway.openai_ws.mode_router_v2_enabled", "gateway.openai_ws.oauth_enabled", "gateway.openai_ws.payload_log_sample_rate", "gateway.openai_ws.prewarm_generate_enabled", "gateway.openai_ws.responses_websockets", "gateway.openai_ws.responses_websockets_v2", "gateway.openai_ws.retry_backoff_initial_ms", "gateway.openai_ws.retry_backoff_max_ms", "gateway.openai_ws.retry_jitter_ratio", "gateway.openai_ws.retry_total_budget_ms", "gateway.openai_ws.store_disabled_conn_mode", "gateway.openai_ws.store_disabled_force_new_conn"} {
		_ = viper.BindEnv(key)
		if viper.IsSet(key) {
			slog.Warn("旧 Responses WS 参数已停用，请在分组协议中设置客户端权限，在网关设置中调整连接参数", "key", key)
		}
	}
}
