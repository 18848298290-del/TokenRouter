package provider

import (
	"maps"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

const (
	ResponsesWSConnectionModeKey = "responses_ws_connection_mode"
	ResponsesWSPooled            = "pooled"
	ResponsesWSPerSession        = "per_session"
)

// ResponsesWSConnectionMode returns the saved connection mode, defaulting to pooled connections.
func (r *Record) ResponsesWSConnectionMode() string {
	if r != nil && r.Extra[ResponsesWSConnectionModeKey] == ResponsesWSPerSession {
		return ResponsesWSPerSession
	}
	return ResponsesWSPooled
}

// SupportsResponsesWS reports whether an OpenAI provider advertises the Responses WebSocket protocol.
func (r *Record) SupportsResponsesWS() bool {
	return r != nil && r.IsOpenAI() && slices.Contains(r.UpstreamProtocols(), capability.ProtocolResponsesWebSocket)
}

// parseResponsesWSConnectionMode validates the mode accepted by persistence paths.
func parseResponsesWSConnectionMode(raw any) (string, error) {
	value, ok := raw.(string)
	if !ok || (value != ResponsesWSPooled && value != ResponsesWSPerSession) {
		return "", apperror.BadRequest("INVALID_RESPONSES_WS_CONNECTION_MODE", "responses_ws_connection_mode must be pooled or per_session")
	}
	return value, nil
}

// validateResponsesWSConnectionModePatch validates the mode when supplied in an Extra patch.
func validateResponsesWSConnectionModePatch(extra map[string]any) error {
	if value, present := extra[ResponsesWSConnectionModeKey]; present {
		_, err := parseResponsesWSConnectionMode(value)
		return err
	}
	return nil
}

// normalizeResponsesWS converts legacy connection settings after protocol validation.
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
	if bridge {
		protocols := slices.Clone(a.UpstreamProtocols())
		protocols = slices.DeleteFunc(protocols, func(id capability.ProtocolID) bool { return id == capability.ProtocolResponsesWebSocket })
		a.Credentials = maps.Clone(a.Credentials)
		if a.Credentials == nil {
			a.Credentials = map[string]any{}
		}
		a.Credentials[UpstreamProtocolsKey] = protocols
	}
	for _, key := range []string{"openai_oauth_responses_websockets_v2_mode", "openai_apikey_responses_websockets_v2_mode", "openai_oauth_responses_websockets_v2_enabled", "openai_apikey_responses_websockets_v2_enabled", "responses_websockets_v2_enabled", "openai_ws_enabled", "openai_ws_force_http"} {
		delete(extra, key)
	}
	extra[ResponsesWSConnectionModeKey] = mode
	a.Extra = extra
	return nil
}
