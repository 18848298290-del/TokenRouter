package compact

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

// observation.go builds diagnostics for a rejected Compact exchange before the
// replacement model is dispatched. Body details are included only when logging
// is enabled and are bounded by the caller's truncation function, while
// recognized query credential values are masked in the summary message.

// RetryObservation snapshots provider, response, and diagnostic settings for one retry.
type RetryObservation struct {
	// ProviderPresent controls whether Notice can produce a provider diagnostic.
	// A false value returns nil even when the response and message are available.
	ProviderPresent bool
	Platform        string // rejected provider platform
	ProviderID      int64  // rejected provider ID
	ProviderName    string // rejected provider display name
	Status          int    // rejected HTTP status
	RequestID       string // upstream request identifier
	// Passthrough records the exchange mode, and LogBody enables response detail.
	// Notice carries both flags to the logger, but invokes truncation only when
	// LogBody is enabled.
	Passthrough, LogBody bool // captured exchange mode; response detail logging
	// LogBodyMaxBytes bounds logged payload details when LogBody is enabled.
	// Nonpositive values select 2048 bytes, interpreted by the supplied truncation callback.
	LogBodyMaxBytes int
}

// RetryNotice contains classified compact recovery diagnostics for the HTTP observer.
type RetryNotice struct {
	RetryObservation // rejected exchange diagnostic snapshot
	// Message is query-sanitized, while Detail uses the caller's configured body
	// truncation. Body contents require the logging policy already captured in RetryObservation.
	Message, Detail string // query-sanitized summary message; truncated response detail
	Kind, Reason    string // retry diagnostic kind; compact recovery reason
}

// Notice builds retry diagnostics, including a bounded payload when body logging is enabled.
// An absent provider returns nil. LogBody selects payload inclusion and requires
// a usable truncation callback; nonpositive body limits use 2048 bytes, and the
// summary message is trimmed and query-sanitized.
func Notice(in RetryObservation, payload []byte, message string, truncate func(string, int) string) *RetryNotice {
	if !in.ProviderPresent {
		return nil
	}
	detail := ""
	if in.LogBody {
		limit := in.LogBodyMaxBytes
		if limit <= 0 {
			limit = 2048
		}
		// Detailed response logging follows the caller's body policy and byte
		// limit. Truncation bounds the diagnostic even when the captured failure
		// contains a large terminal response.
		detail = truncate(string(payload), limit)
	}
	return &RetryNotice{RetryObservation: in, Message: logredact.SanitizeUpstreamQueries(strings.TrimSpace(message)), Detail: detail, Kind: "retry", Reason: "compact_model_fallback"}
}
