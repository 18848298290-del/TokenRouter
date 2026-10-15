package ws

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
)

// parameters.go merges and validates stored WebSocket overrides against
// deployment parameters.
// Stored WebSocket settings contain overrides, while deployment parameters
// supply every omitted value. A patch can clear one override with null, retain
// other overrides by omission, or reset the whole stored object with a
// top-level null.
//
// Resolution decodes into a copied deployment value, rejects unknown stored
// fields and field-level null, then validates the combined result. This merged
// check matters for related capacity values: a valid individual connection
// limit can still be smaller than the retained idle settings. Patching
// validates the proposed stored object through the same resolver before
// returning it, so persistence callers receive both serialized overrides and
// any effective-value error.

// SettingKey names the persisted override object consumed by settings updates
// and Runtime refreshes for Responses WebSocket connection and turn parameters.
const SettingKey = "responses_ws"

// Parameters contains effective WebSocket settings copied by value for each
// connection or turn.
// @project-doc docs/interfaces/openai_upstream.md#responses_ws_runtime
type Parameters struct {
	// MaxConnsPerProvider, MinIdlePerProvider, and MaxIdlePerProvider are
	// validated together with min <= max idle <= the hard connection limit. Both
	// idle counts allow zero.
	MaxConnsPerProvider int `json:"max_conns_per_provider"` // provider connection hard limit
	MinIdlePerProvider  int `json:"min_idle_per_provider"`  // provider prewarmed idle minimum
	MaxIdlePerProvider  int `json:"max_idle_per_provider"`  // provider retained idle maximum
	// DynamicMaxConnsByProviderConcurrencyEnabled lets the upstream pool scale a
	// positive provider concurrency by its type factor, round upward, and clamp
	// within the hard limit.
	DynamicMaxConnsByProviderConcurrencyEnabled bool `json:"dynamic_max_conns_by_provider_concurrency_enabled"` // provider concurrency scaling enabled
	// OAuthMaxConnsFactor and APIKeyMaxConnsFactor select scaling by provider type
	// when dynamic sizing is enabled. Validate requires both to be finite and
	// positive.
	OAuthMaxConnsFactor  float64 `json:"oauth_max_conns_factor"`  // OAuth connection scaling factor
	APIKeyMaxConnsFactor float64 `json:"apikey_max_conns_factor"` // API Key connection scaling factor
	// QueueLimitPerConn scales the provider-wide waiting limit by effective
	// connection capacity. A specified continuation also observes the requested
	// connection's queue limit.
	QueueLimitPerConn                int     `json:"queue_limit_per_conn"`                 // waiters allowed per connection
	PoolTargetUtilization            float64 `json:"pool_target_utilization"`              // target connection utilization ratio
	PrewarmCooldownMS                int     `json:"prewarm_cooldown_ms"`                  // milliseconds
	ClientFirstMessageTimeoutSeconds int     `json:"client_first_message_timeout_seconds"` // seconds
	// IngressInterTurnIdleTimeoutSeconds permits zero to disable the between-turn
	// client timer. The first-message timeout remains positive and applies when a
	// connection is accepted.
	IngressInterTurnIdleTimeoutSeconds int `json:"ingress_inter_turn_idle_timeout_seconds"` // seconds
	// MaxIngressConnectionsPerAPIKey limits persistent client connection leases
	// separately from per-turn request slots. Zero disables this count limit for
	// newly accepted connections.
	MaxIngressConnectionsPerAPIKey int   `json:"max_ingress_connections_per_api_key"` // open client connections per key
	ClientReadLimitBytes           int64 `json:"client_read_limit_bytes"`             // bytes
	// HTTPBridgeThresholdBytes participates in first-payload bridge selection.
	// Forwarding through HTTP additionally requires the selected provider and
	// group to permit that transport.
	HTTPBridgeThresholdBytes int64 `json:"http_bridge_threshold_bytes"` // bytes
	DialTimeoutSeconds       int   `json:"dial_timeout_seconds"`        // seconds
	ReadTimeoutSeconds       int   `json:"read_timeout_seconds"`        // seconds
	WriteTimeoutSeconds      int   `json:"write_timeout_seconds"`       // seconds
	// IngressPreviousResponseRecoveryEnabled permits the pooled missing-response
	// replay path before downstream output when current and retained replay have
	// no tool outputs. Strict continuation can use that bounded path; idle ping
	// recovery and generic transport retry apply their separate rules.
	IngressPreviousResponseRecoveryEnabled bool `json:"ingress_previous_response_recovery_enabled"` // missing continuation recovery enabled
	// StickySessionTTLSeconds and StickyResponseIDTTLSeconds govern new or renewed
	// affinity records. Runtime changes apply to subsequent writes; existing
	// records retain their stored expiry.
	StickySessionTTLSeconds    int `json:"sticky_session_ttl_seconds"`     // seconds
	StickyResponseIDTTLSeconds int `json:"sticky_response_id_ttl_seconds"` // seconds
}

// DefaultParameters returns the complete built-in deployment defaults for
// WebSocket runtime settings.
func DefaultParameters() Parameters {
	return Parameters{MaxConnsPerProvider: 128, MinIdlePerProvider: 4, MaxIdlePerProvider: 12, DynamicMaxConnsByProviderConcurrencyEnabled: true, OAuthMaxConnsFactor: 1, APIKeyMaxConnsFactor: 1, QueueLimitPerConn: 64, PoolTargetUtilization: 0.7, PrewarmCooldownMS: 300, ClientFirstMessageTimeoutSeconds: 30, IngressInterTurnIdleTimeoutSeconds: 300, MaxIngressConnectionsPerAPIKey: 64, ClientReadLimitBytes: 64 * 1024 * 1024, HTTPBridgeThresholdBytes: 15 * 1024 * 1024, DialTimeoutSeconds: 10, ReadTimeoutSeconds: 900, WriteTimeoutSeconds: 120, IngressPreviousResponseRecoveryEnabled: true, StickySessionTTLSeconds: 3600, StickyResponseIDTTLSeconds: 3600}
}

// Validate checks a complete effective parameter value and returns its first
// constraint error. It accepts zero idle counts, inter-turn idle timeout, and
// ingress connection limit while requiring positive I/O budgets and finite
// scaling values.
//
// Idle minimum, idle maximum, and connection limit are checked together after
// scalar validation because stored overrides can combine with deployment
// defaults. Pool utilization must be finite, positive, and at most one. This
// method validates the supplied value; applying it to the pool and publishing a
// runtime snapshot remain separate operations owned by Runtime.
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

// ResolveParameters overlays stored settings on a copied deployment value and
// validates the effective result. Empty raw selects an empty override object,
// while a top-level null decodes as no overrides.
//
// Unknown fields and field-level null are rejected before a value can be
// accepted. Decoding into defaults keeps omitted fields available, and Validate
// then checks the merged scalar and capacity constraints. A malformed object,
// incompatible field type, or invalid effective default returns an error;
// callers publish only validated results.
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

// PatchParameters merges a partial override update and returns serialized
// storage plus validation error. Omitted fields retain stored overrides,
// field-level null removes one override, and top-level null clears every
// override, even when stored JSON is malformed.
//
// Each supplied patch name is checked against the parameter JSON fields before
// merge. Effective validation then resolves the proposed object against
// deployment defaults, catching type errors and cross-field capacity conflicts.
// Callers persist only after the returned error is nil; serialized bytes can
// accompany a failed effective validation and do not imply the update is
// accepted.
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
	// A whole-object reset bypasses stored decoding and rebuilds empty overrides,
	// allowing callers to repair malformed storage while restoring deployment
	// defaults.
	if bytes.Equal(bytes.TrimSpace(patch), []byte("null")) {
		values = map[string]json.RawMessage{}
	} else {
		var updates map[string]json.RawMessage
		if err := json.Unmarshal(patch, &updates); err != nil {
			return "", err
		}
		// Discover field names from JSON-encodable defaults, ignoring schema-only
		// encoding errors. NaN or infinite float defaults leave known empty and
		// make supplied patch fields fail the following unknown-field check.
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
