package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Text routing combines an administrator mode with the caller's preferred
// OpenAI text protocol. A forced mode selects its endpoint regardless of that
// preference. Preserving the preference chooses Chat only for the exact Chat
// identifier; other values resolve to Responses, including unknown identifiers.
//
// Mode parsing accepts exact enum strings and treats missing, mistyped, or
// unrecognized values as preserving the caller's preference. Stored Responses
// probe results do not participate in endpoint choice. HTTP continuation is a
// separate boolean setting consumed by compatibility forwarding, where provider
// type and model checks can further restrict continuation. Routing choice alone
// therefore does not establish continuation availability.

// Text route modes control whether endpoint selection follows the caller's
// preference or a fixed administrator choice for ordinary OpenAI text requests.
const (
	// TextRouteModePreserveClientProtocol prefers the client protocol.
	TextRouteModePreserveClientProtocol TextRouteMode = "preserve_client_protocol"
	// TextRouteModeForceResponses selects Responses.
	TextRouteModeForceResponses TextRouteMode = "force_responses"
	// TextRouteModeForceChatCompletions selects Chat Completions.
	TextRouteModeForceChatCompletions TextRouteMode = "force_chat_completions"

	// Text protocol aliases share the wire identifiers used by protocol/openai,
	// allowing callers to compare endpoint preferences with the selected upstream route.
	TextProtocolChatCompletions = openai.TextProtocolChatCompletions
	TextProtocolResponses       = openai.TextProtocolResponses

	// Extra keys separate endpoint policy from HTTP continuation capability. The
	// latter requires a boolean value and does not change endpoint preference resolution.
	// ExtraKeyTextRouteMode stores the administrator's text routing policy.
	ExtraKeyTextRouteMode = "openai_text_route_mode"
	// ExtraKeyResponsesContinuationSupported stores the administrator's HTTP continuation switch.
	ExtraKeyResponsesContinuationSupported = "openai_responses_continuation_supported"
)

// TextRouteMode selects the upstream protocol for ordinary text requests.
type TextRouteMode string

// TextProtocol aliases the text protocol identifiers defined by protocol/openai.
type TextProtocol = openai.TextProtocol

// NormalizeTextRouteMode resolves an exact configured mode or the preference
// preserving default. Empty, unrecognized, differently cased, and padded strings
// select that default; parsing does not trim or fold the supplied value.
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

// ResolveTextRouteMode returns the administrator's text mode from provider Extra.
// Nil maps, missing keys, and nonstring values resolve to preserving preference.
// String values follow NormalizeTextRouteMode's exact enum matching rules.
func ResolveTextRouteMode(extra map[string]any) TextRouteMode {
	if extra == nil {
		return TextRouteModePreserveClientProtocol
	}
	// Only string mode values participate in selection. Mistyped settings use the
	// preference default through normalization rather than converting their representation.
	mode, _ := extra[ExtraKeyTextRouteMode].(string)
	return NormalizeTextRouteMode(mode)
}

// ResolveResponsesContinuationSupported reports the explicit boolean HTTP
// continuation switch from provider Extra. Missing keys, nil maps, and nonboolean
// values return false. Execution callers additionally check provider type and
// model suitability before enabling continuation.
func ResolveResponsesContinuationSupported(extra map[string]any) bool {
	if extra == nil {
		return false
	}
	// Continuation requires a boolean setting. String values such as "true" do
	// not become capability evidence for the compatibility forwarding consumer.
	supported, _ := extra[ExtraKeyResponsesContinuationSupported].(bool)
	return supported
}

// ResolveUpstreamTextProtocol selects the upstream endpoint from administrator
// mode and caller preference. A forced Responses or Chat mode overrides preferred;
// otherwise the exact Chat identifier chooses Chat and every other value chooses
// Responses, including an unknown preference.
//
// Missing or invalid mode configuration preserves preference through the same
// rule. Stored probe statuses and the continuation switch do not alter selection,
// so observations cannot silently replace an administrator's endpoint policy.
// Callers separately apply protocol capability and continuation eligibility checks.
func ResolveUpstreamTextProtocol(extra map[string]any, preferred TextProtocol) TextProtocol {
	switch ResolveTextRouteMode(extra) {
	case TextRouteModeForceResponses:
		return TextProtocolResponses
	case TextRouteModeForceChatCompletions:
		return TextProtocolChatCompletions
	}

	// A caller without an exact Chat preference uses Responses as the text
	// endpoint, including unknown identifiers after a missing or invalid administrator mode.
	if preferred == TextProtocolChatCompletions {
		return TextProtocolChatCompletions
	}
	return TextProtocolResponses
}
