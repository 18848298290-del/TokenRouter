package httpapi

import "github.com/TokenFlux/TokenRouter/internal/gateway/media"

// Responses endpoints use separate header policies for standard, passthrough, and raw Chat requests.
const (
	ChatgptCodexURL      = "https://chatgpt.com/backend-api/codex/responses"
	OpenaiPlatformAPIURL = "https://api.openai.com/v1/responses"
)

var (
	// openaiAllowedHeaders lists client headers accepted by standard Responses requests.
	openaiAllowedHeaders = map[string]bool{
		"accept-language": true,
		"content-type":    true,
		"conversation_id": true,
		"user-agent":      true,
		"originator":      true,
		"session_id":      true,
		// Keep Codex device and session identifiers for provider namespace isolation.
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

	// openaiPassthroughAllowedHeaders lists client headers accepted by passthrough requests.
	// Restrict forwarding to known headers because other headers can trigger upstream risk checks.
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

	// openaiCCRawAllowedHeaders lists client headers accepted by raw Chat Completions.
	// Codex identity and session headers belong to ChatGPT OAuth requests.
	// Compatible upstreams may ignore these headers or reject them with 400 unknown parameter.
	// Forward general HTTP headers here; request construction sets content-type, authorization, and accept.
	// Keep this list separate from Codex header policies because the upstream trust domains differ.
	openaiCCRawAllowedHeaders = map[string]bool{
		"accept-language": true,
		"user-agent":      true,
	}
)

// AllowOpenAIRawChatHeader shares the general header allowlist with media and native Chat entries.
func AllowOpenAIRawChatHeader(name string) bool { return openaiCCRawAllowedHeaders[name] }

// AllowOpenAIPassthroughHeader excludes client timeout headers from image requests.
func AllowOpenAIPassthroughHeader(name string) bool { return openaiPassthroughAllowedHeaders[name] }
