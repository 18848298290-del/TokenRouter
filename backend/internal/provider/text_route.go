package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

const (
	// TextRouteModePreserveClientProtocol prefers the client protocol.
	TextRouteModePreserveClientProtocol TextRouteMode = "preserve_client_protocol"
	// TextRouteModeForceResponses selects Responses.
	TextRouteModeForceResponses TextRouteMode = "force_responses"
	// TextRouteModeForceChatCompletions selects Chat Completions.
	TextRouteModeForceChatCompletions TextRouteMode = "force_chat_completions"

	TextProtocolChatCompletions = openai.TextProtocolChatCompletions
	TextProtocolResponses       = openai.TextProtocolResponses

	// ExtraKeyTextRouteMode stores the administrator's text routing policy.
	ExtraKeyTextRouteMode = "openai_text_route_mode"
	// ExtraKeyResponsesContinuationSupported stores the administrator's HTTP continuation switch.
	ExtraKeyResponsesContinuationSupported = "openai_responses_continuation_supported"
)

// TextRouteMode selects the upstream protocol for ordinary text requests.
type TextRouteMode string

// TextProtocol aliases the text protocol identifiers defined by protocol/openai.
type TextProtocol = openai.TextProtocol

// NormalizeTextRouteMode defaults missing or invalid modes to preserving the client protocol.
func NormalizeTextRouteMode(mode string) TextRouteMode {
	switch TextRouteMode(mode) {
	case TextRouteModeForceResponses:
		return TextRouteModeForceResponses
	case TextRouteModeForceChatCompletions:
		return TextRouteModeForceChatCompletions
	default:
		return TextRouteModePreserveClientProtocol
	}
}

// ResolveTextRouteMode reads the administrator's text routing mode from provider extra.
func ResolveTextRouteMode(extra map[string]any) TextRouteMode {
	if extra == nil {
		return TextRouteModePreserveClientProtocol
	}
	mode, _ := extra[ExtraKeyTextRouteMode].(string)
	return NormalizeTextRouteMode(mode)
}

// ResolveResponsesContinuationSupported reads the HTTP continuation switch from provider extra.
// Missing values and values of the wrong type mean unsupported.
func ResolveResponsesContinuationSupported(extra map[string]any) bool {
	if extra == nil {
		return false
	}
	supported, _ := extra[ExtraKeyResponsesContinuationSupported].(bool)
	return supported
}

// ResolveUpstreamTextProtocol selects the text protocol from the client preference
// and administrator routing mode. Probe results do not affect the choice.
func ResolveUpstreamTextProtocol(extra map[string]any, preferred TextProtocol) TextProtocol {
	switch ResolveTextRouteMode(extra) {
	case TextRouteModeForceResponses:
		return TextProtocolResponses
	case TextRouteModeForceChatCompletions:
		return TextProtocolChatCompletions
	}

	if preferred == TextProtocolChatCompletions {
		return TextProtocolChatCompletions
	}
	return TextProtocolResponses
}
