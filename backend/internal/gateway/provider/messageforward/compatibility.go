package messageforward

import (
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Messages compatibility failures can be specific to beta support, thinking
// signatures, or tools. The status-400 classifier recognizes those message
// fragments for provider switching, while counting has a separate status-404
// test for an unavailable count_tokens route.

// messageFailover400 reports whether an extracted error mentions recognized
// Messages compatibility features. Empty and unrecognized messages return
// false; the caller separately decides whether status 400 may fail over.
func messageFailover400(respBody []byte) bool {
	msg := strings.ToLower(strings.TrimSpace(upstream.ExtractErrorMessage(respBody)))
	if msg == "" {
		return false
	}

	// A beta rejection can depend on the selected upstream, so these
	// recognized message fragments let the outer attempt policy consider
	// another provider.
	if strings.Contains(msg, "anthropic-beta") ||
		strings.Contains(msg, "beta feature") ||
		strings.Contains(msg, "requires beta") {
		return true
	}

	// Thinking, signature, and tool failures select the compatibility path
	// because the outer gateway can rebuild their request state for a
	// different provider.
	if strings.Contains(msg, "thinking") || strings.Contains(msg, "thought_signature") || strings.Contains(msg, "signature") {
		return true
	}
	if strings.Contains(msg, "tool_use") || strings.Contains(msg, "tool_result") || strings.Contains(msg, "tools") {
		return true
	}

	return false
}

// isCountTokensUnsupported404 recognizes a missing count_tokens route in an
// HTTP 404 response. Generic missing-resource messages and other statuses
// return false, allowing the caller to use ordinary error handling.
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
