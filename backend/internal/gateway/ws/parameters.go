package ws

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
)

const SettingKey = "responses_ws"

// Parameters contains runtime settings copied by value for each request or turn.
// @project-doc docs/interfaces/openai_upstream.md#responses_ws_runtime
type Parameters struct {
	MaxConnsPerProvider                         int     `json:"max_conns_per_provider"`
	MinIdlePerProvider                          int     `json:"min_idle_per_provider"`
	MaxIdlePerProvider                          int     `json:"max_idle_per_provider"`
	DynamicMaxConnsByProviderConcurrencyEnabled bool    `json:"dynamic_max_conns_by_provider_concurrency_enabled"`
	OAuthMaxConnsFactor                         float64 `json:"oauth_max_conns_factor"`
	APIKeyMaxConnsFactor                        float64 `json:"apikey_max_conns_factor"`
	QueueLimitPerConn                           int     `json:"queue_limit_per_conn"`
	PoolTargetUtilization                       float64 `json:"pool_target_utilization"`
	PrewarmCooldownMS                           int     `json:"prewarm_cooldown_ms"`
	ClientFirstMessageTimeoutSeconds            int     `json:"client_first_message_timeout_seconds"`
	IngressInterTurnIdleTimeoutSeconds          int     `json:"ingress_inter_turn_idle_timeout_seconds"`
	MaxIngressConnectionsPerAPIKey              int     `json:"max_ingress_connections_per_api_key"`
	ClientReadLimitBytes                        int64   `json:"client_read_limit_bytes"`
	HTTPBridgeThresholdBytes                    int64   `json:"http_bridge_threshold_bytes"`
	DialTimeoutSeconds                          int     `json:"dial_timeout_seconds"`
	ReadTimeoutSeconds                          int     `json:"read_timeout_seconds"`
	WriteTimeoutSeconds                         int     `json:"write_timeout_seconds"`
	IngressPreviousResponseRecoveryEnabled      bool    `json:"ingress_previous_response_recovery_enabled"`
	StickySessionTTLSeconds                     int     `json:"sticky_session_ttl_seconds"`
	StickyResponseIDTTLSeconds                  int     `json:"sticky_response_id_ttl_seconds"`
}

// DefaultParameters returns defaults used when deployment settings are absent.
func DefaultParameters() Parameters {
	return Parameters{MaxConnsPerProvider: 128, MinIdlePerProvider: 4, MaxIdlePerProvider: 12, DynamicMaxConnsByProviderConcurrencyEnabled: true, OAuthMaxConnsFactor: 1, APIKeyMaxConnsFactor: 1, QueueLimitPerConn: 64, PoolTargetUtilization: 0.7, PrewarmCooldownMS: 300, ClientFirstMessageTimeoutSeconds: 30, IngressInterTurnIdleTimeoutSeconds: 300, MaxIngressConnectionsPerAPIKey: 64, ClientReadLimitBytes: 64 * 1024 * 1024, HTTPBridgeThresholdBytes: 15 * 1024 * 1024, DialTimeoutSeconds: 10, ReadTimeoutSeconds: 900, WriteTimeoutSeconds: 120, IngressPreviousResponseRecoveryEnabled: true, StickySessionTTLSeconds: 3600, StickyResponseIDTTLSeconds: 3600}
}

// Validate checks a complete effective value, including constraints between merged fields.
func (v Parameters) Validate() error {
	if v.MaxConnsPerProvider <= 0 {
		return fmt.Errorf("max_conns_per_provider must be positive")
	}
	if v.MinIdlePerProvider < 0 {
		return fmt.Errorf("min_idle_per_provider must be non-negative")
	}
	if v.MaxIdlePerProvider < 0 {
		return fmt.Errorf("max_idle_per_provider must be non-negative")
	}
	if v.QueueLimitPerConn <= 0 {
		return fmt.Errorf("queue_limit_per_conn must be positive")
	}
	if v.PrewarmCooldownMS < 0 {
		return fmt.Errorf("prewarm_cooldown_ms must be non-negative")
	}
	if v.ClientFirstMessageTimeoutSeconds <= 0 {
		return fmt.Errorf("client_first_message_timeout_seconds must be positive")
	}
	if v.IngressInterTurnIdleTimeoutSeconds < 0 {
		return fmt.Errorf("ingress_inter_turn_idle_timeout_seconds must be non-negative")
	}
	if v.MaxIngressConnectionsPerAPIKey < 0 {
		return fmt.Errorf("max_ingress_connections_per_api_key must be non-negative")
	}
	if v.ClientReadLimitBytes <= 0 {
		return fmt.Errorf("client_read_limit_bytes must be positive")
	}
	if v.HTTPBridgeThresholdBytes <= 0 {
		return fmt.Errorf("http_bridge_threshold_bytes must be positive")
	}
	if v.DialTimeoutSeconds <= 0 {
		return fmt.Errorf("dial_timeout_seconds must be positive")
	}
	if v.ReadTimeoutSeconds <= 0 {
		return fmt.Errorf("read_timeout_seconds must be positive")
	}
	if v.WriteTimeoutSeconds <= 0 {
		return fmt.Errorf("write_timeout_seconds must be positive")
	}
	if v.StickySessionTTLSeconds <= 0 {
		return fmt.Errorf("sticky_session_ttl_seconds must be positive")
	}
	if v.StickyResponseIDTTLSeconds <= 0 {
		return fmt.Errorf("sticky_response_id_ttl_seconds must be positive")
	}
	if math.IsNaN(v.OAuthMaxConnsFactor) || math.IsInf(v.OAuthMaxConnsFactor, 0) || v.OAuthMaxConnsFactor <= 0 {
		return fmt.Errorf("OAuthMaxConnsFactor must be finite and positive")
	}
	if math.IsNaN(v.APIKeyMaxConnsFactor) || math.IsInf(v.APIKeyMaxConnsFactor, 0) || v.APIKeyMaxConnsFactor <= 0 {
		return fmt.Errorf("APIKeyMaxConnsFactor must be finite and positive")
	}
	if math.IsNaN(v.PoolTargetUtilization) || math.IsInf(v.PoolTargetUtilization, 0) || v.PoolTargetUtilization <= 0 {
		return fmt.Errorf("PoolTargetUtilization must be finite and positive")
	}

	if v.PoolTargetUtilization > 1 {
		return fmt.Errorf("pool_target_utilization must be at most 1")
	}
	if v.MinIdlePerProvider > v.MaxIdlePerProvider || v.MaxIdlePerProvider > v.MaxConnsPerProvider {
		return fmt.Errorf("idle connections must satisfy min <= max <= connection limit")
	}
	return nil
}

// ResolveParameters applies stored overrides to deployment defaults and validates the result.
func ResolveParameters(defaults Parameters, raw string) (Parameters, error) {
	if raw == "" {
		raw = "{}"
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return Parameters{}, err
	}
	for name, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Parameters{}, fmt.Errorf("stored setting %s is null", name)
		}
	}
	value := defaults
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return Parameters{}, err
	}
	return value, value.Validate()
}

// PatchParameters merges a partial update; null clears an override and omitted fields retain their values.
func PatchParameters(defaults Parameters, stored string, patch json.RawMessage) (string, error) {
	values := map[string]json.RawMessage{}
	if stored != "" && !bytes.Equal(bytes.TrimSpace(patch), []byte("null")) {
		if err := json.Unmarshal([]byte(stored), &values); err != nil {
			return "", err
		}
	}
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	if bytes.Equal(bytes.TrimSpace(patch), []byte("null")) {
		values = map[string]json.RawMessage{}
	} else {
		var updates map[string]json.RawMessage
		if err := json.Unmarshal(patch, &updates); err != nil {
			return "", err
		}
		knownJSON, _ := json.Marshal(defaults)
		var known map[string]json.RawMessage
		_ = json.Unmarshal(knownJSON, &known)
		for name, value := range updates {
			if _, ok := known[name]; !ok {
				return "", fmt.Errorf("unknown responses_ws setting %q", name)
			}
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				delete(values, name)
			} else {
				values[name] = value
			}
		}
	}
	body, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	_, err = ResolveParameters(defaults, string(body))
	return string(body), err
}
