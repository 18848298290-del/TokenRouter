package provider

import (
	"maps"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// responses_ws.go normalizes Responses WebSocket connection settings and
// converts legacy provider flags.
// Responses WebSocket enablement comes from the provider protocol set, while
// connection mode controls pooled versus per-session connections. An explicit
// mode takes precedence over legacy flags. Legacy passthrough selects
// per-session mode, and legacy HTTP bridge removes the WebSocket protocol while
// leaving other enabled protocols available.
//
// Normalization clones the Extra map before clearing legacy keys and publishing
// the resolved mode. A bridge also clones the credential map and protocol slice
// before replacement so the input collections can remain shared elsewhere.
// Original protocol validation runs before this conversion; malformed protocol
// input cannot become valid by having WebSocket entries removed.

// Connection-mode values are saved in provider Extra and consumed independently
// from the protocol set that determines Responses WebSocket eligibility.
const (
	ResponsesWSConnectionModeKey = "responses_ws_connection_mode" // saved connection mode key
	ResponsesWSPooled            = "pooled"                       // provider connection pool
	ResponsesWSPerSession        = "per_session"                  // dedicated session connection
)

// ResponsesWSConnectionMode returns the exact saved per-session mode or the
// pooled default. Nil records, absent settings and any other stored value use
// pooled connections; validation belongs to persistence normalization.
func (r *Record) ResponsesWSConnectionMode() string {
	if r != nil && r.Extra[ResponsesWSConnectionModeKey] == ResponsesWSPerSession {
		return ResponsesWSPerSession
	}
	return ResponsesWSPooled
}

// SupportsResponsesWS reports whether an OpenAI record enables Responses
// WebSocket in its resolved upstream protocol set. Nil and other-platform
// records return false; callers keep credentials stable during protocol
// resolution.
func (r *Record) SupportsResponsesWS() bool {
	return r != nil && r.IsOpenAI() && slices.Contains(r.UpstreamProtocols(), capability.ProtocolResponsesWebSocket)
}

// parseResponsesWSConnectionMode validates an exact string mode used by
// persistence paths. Values other than pooled or per_session, including
// nonstrings, return a structured error before configuration is saved.
func parseResponsesWSConnectionMode(raw any) (string, error) {
	value, ok := raw.(string)
	if !ok || (value != ResponsesWSPooled && value != ResponsesWSPerSession) {
		return "", apperror.BadRequest("INVALID_RESPONSES_WS_CONNECTION_MODE", "responses_ws_connection_mode must be pooled or per_session")
	}
	return value, nil
}

// validateResponsesWSConnectionModePatch validates the mode when an Extra patch
// contains its key. An omitted key succeeds, while a present nil or invalid
// value is rejected by the shared mode parser.
func validateResponsesWSConnectionModePatch(extra map[string]any) error {
	if value, present := extra[ResponsesWSConnectionModeKey]; present {
		_, err := parseResponsesWSConnectionMode(value)
		return err
	}
	return nil
}

// normalizeResponsesWS converts legacy OpenAI WebSocket choices into connection
// mode and protocol configuration. Nil and non-OpenAI records are skipped. Call
// after validating the original protocol set and keep the record stable while
// normalization runs.
//
// A present connection mode is authoritative; otherwise legacy passthrough chooses
// per-session and HTTP-bridge flags remove WebSocket support. Extra is
// shallow-cloned before legacy keys are removed. Bridge conversion additionally
// clones credentials and the protocol slice before assigning replacements,
// retaining other nested values through the maps.
func normalizeResponsesWS(a *Record) error {
	if a == nil || !a.IsOpenAI() {
		return nil
	}
	extra := maps.Clone(a.Extra)
	if extra == nil {
		extra = map[string]any{}
	}
	mode := ResponsesWSPooled
	bridge := false
	// A saved connection mode already resolves the connection choice. Read legacy
	// bridge flags only when that value is absent, preventing a legacy flag from
	// overriding it.
	if raw, exists := extra[ResponsesWSConnectionModeKey]; exists {
		value, err := parseResponsesWSConnectionMode(raw)
		if err != nil {
			return err
		}
		mode = value
	} else {
		key := "openai_oauth_responses_websockets_v2_mode"
		if a.IsOpenAIApiKey() {
			key = "openai_apikey_responses_websockets_v2_mode"
		}
		legacy, _ := extra[key].(string)
		switch strings.ToLower(strings.TrimSpace(legacy)) {
		case "passthrough":
			mode = ResponsesWSPerSession
		case "http_bridge":
			bridge = true
		}
		if forced, _ := extra["openai_ws_force_http"].(bool); forced {
			bridge = true
		}
	}
	// Bridge conversion drops WebSocket from a copied protocol slice and replaces
	// copied credential storage, keeping other protocols and shared input maps available.
	if bridge {
		protocols := slices.Clone(a.UpstreamProtocols())
		protocols = slices.DeleteFunc(protocols, func(id capability.ProtocolID) bool { return id == capability.ProtocolResponsesWebSocket })
		a.Credentials = maps.Clone(a.Credentials)
		if a.Credentials == nil {
			a.Credentials = map[string]any{}
		}
		a.Credentials[UpstreamProtocolsKey] = protocols
	}
	// Remove superseded keys from the copied Extra object before publishing the
	// resolved mode, so subsequent normalization reads that saved choice directly.
	for _, key := range []string{"openai_oauth_responses_websockets_v2_mode", "openai_apikey_responses_websockets_v2_mode", "openai_oauth_responses_websockets_v2_enabled", "openai_apikey_responses_websockets_v2_enabled", "responses_websockets_v2_enabled", "openai_ws_enabled", "openai_ws_force_http"} {
		delete(extra, key)
	}
	extra[ResponsesWSConnectionModeKey] = mode
	a.Extra = extra
	return nil
}
