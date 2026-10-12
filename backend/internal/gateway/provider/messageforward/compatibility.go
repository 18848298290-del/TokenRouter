package messageforward

import (
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

func messageFailover400(respBody []byte) bool {
	// Allow failover for recognized compatibility errors that another provider may accept.
	// Unrecognized 400 responses remain terminal.
	msg := strings.ToLower(strings.TrimSpace(upstream.ExtractErrorMessage(respBody)))
	if msg == "" {
		return false
	}

	// A different provider may support the requested beta features.
	if strings.Contains(msg, "anthropic-beta") ||
		strings.Contains(msg, "beta feature") ||
		strings.Contains(msg, "requires beta") {
		return true
	}

	// Thinking signatures and tool support can differ between upstreams.
	if strings.Contains(msg, "thinking") || strings.Contains(msg, "thought_signature") || strings.Contains(msg, "signature") {
		return true
	}
	if strings.Contains(msg, "tool_use") || strings.Contains(msg, "tool_result") || strings.Contains(msg, "tools") {
		return true
	}

	return false
}

func isCountTokensUnsupported404(statusCode int, body []byte) bool {
	if statusCode != http.StatusNotFound {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(upstream.ExtractErrorMessage(body)))
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "/v1/messages/count_tokens") {
		return true
	}
	return strings.Contains(msg, "count_tokens") && strings.Contains(msg, "not found")
}
