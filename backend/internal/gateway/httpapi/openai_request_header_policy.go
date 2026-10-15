package httpapi

import "github.com/TokenFlux/TokenRouter/internal/gateway/media"

// openai_request_header_policy.go defines allowed outgoing headers for
// Responses passthrough and raw Chat requests.
// Outgoing header permissions differ between Codex Responses and raw Chat.
// Standard and passthrough Responses retain session and device metadata for
// later identity rewriting, while raw Chat forwards general client headers and
// lets request construction supply authentication and protocol headers.

// ChatgptCodexURL and OpenaiPlatformAPIURL select the default OAuth and API-key
// Responses targets before provider-specific base URLs and inbound path
// suffixes are applied.
const (
	// ChatgptCodexURL is the ChatGPT OAuth Responses endpoint.
	ChatgptCodexURL = "https://chatgpt.com/backend-api/codex/responses"
	// OpenaiPlatformAPIURL is the public API-key Responses endpoint.
	OpenaiPlatformAPIURL = "https://api.openai.com/v1/responses"
)

var (
	// openaiAllowedHeaders permits standard Responses client metadata for
	// subsequent session isolation, provider identity rewriting, and
	// required upstream header completion.
	openaiAllowedHeaders = map[string]bool{
		"accept-language": true,
		"content-type":    true,
		"conversation_id": true,
		"user-agent":      true,
		"originator":      true,
		"session_id":      true,
		// Request builders rewrite device and session identifiers after
		// this admission check, so client metadata reaches the
		// provider-namespace isolation step.
		"installation_id":            true,
		"x-codex-installation-id":    true,
		"session-id":                 true,
		"thread_id":                  true,
		"thread-id":                  true,
		"turn_id":                    true,
		"turn-id":                    true,
		"window_id":                  true,
		"window-id":                  true,
		"x-codex-window-id":          true,
		"x-client-request-id":        true,
		"x-codex-beta-features":      true,
		"x-codex-turn-state":         true,
		"x-codex-turn-metadata":      true,
		media.ResponsesLiteHeaderKey: true,
	}

	// openaiPassthroughAllowedHeaders permits passthrough Responses
	// metadata, including explicit beta negotiation. Client timeout names
	// are admitted separately according to the request service's
	// timeout-header permission.
	openaiPassthroughAllowedHeaders = map[string]bool{
		"accept":                     true,
		"accept-language":            true,
		"content-type":               true,
		"conversation_id":            true,
		"openai-beta":                true,
		"user-agent":                 true,
		"originator":                 true,
		"session_id":                 true,
		"installation_id":            true,
		"x-codex-installation-id":    true,
		"session-id":                 true,
		"thread_id":                  true,
		"thread-id":                  true,
		"turn_id":                    true,
		"turn-id":                    true,
		"window_id":                  true,
		"window-id":                  true,
		"x-codex-window-id":          true,
		"x-client-request-id":        true,
		"x-codex-beta-features":      true,
		"x-codex-turn-state":         true,
		"x-codex-turn-metadata":      true,
		media.ResponsesLiteHeaderKey: true,
	}

	// openaiCCRawAllowedHeaders permits language and user-agent metadata on
	// raw Chat. SendChatRequest supplies authentication, content type, and
	// the stream-dependent Accept header before provider overrides are
	// applied.
	openaiCCRawAllowedHeaders = map[string]bool{
		"accept-language": true,
		"user-agent":      true,
	}
)

// AllowOpenAIRawChatHeader checks the lowercase general-client header policy
// for raw Chat.
func AllowOpenAIRawChatHeader(name string) bool { return openaiCCRawAllowedHeaders[name] }

// AllowOpenAIPassthroughHeader checks the lowercase passthrough header
// allowlist used by images. Timeout names have no entries in this map, so image
// callers receive no additional timeout-header permission through this
// function.
func AllowOpenAIPassthroughHeader(name string) bool { return openaiPassthroughAllowedHeaders[name] }
