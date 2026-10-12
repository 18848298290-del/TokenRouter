package forward

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Anthropic error handling needs the upstream status for policy and a separate
// client-facing status for display. Health decisions run before display rules,
// so a status or message override cannot turn a rejected exchange into a retry
// decision or undo its provider-health effects.
//
// Matching display rules start with the upstream status and extracted message,
// then apply optional code and body overrides independently. No matching rule
// retains the caller's defaults. A matching monitoring exclusion marks the
// request through the adapter, leaving final observation processing with its
// owner rather than changing provider recovery. Body readers and diagnostics
// remain adapter operations so the retry path can replace a consumed reader
// before health policy reads it again.

// ErrorInput supplies upstream response metadata and diagnostic choices to error handling.
type ErrorInput struct {
	ProviderID int64 // observed upstream provider ID
	// provider display name; provider authentication type; upstream platform identifier;
	// observed upstream request identifier
	ProviderName, ProviderType, Platform, RequestID string
	Status                                          int // observed upstream HTTP status
	// LogBody enables additional bounded error details in diagnostics and logs.
	// Client display uses its own rule and status handling independently of this flag.
	LogBody bool
	// LogBodyMaxBytes bounds body detail. Handlers choose 2048 bytes for a
	// nonpositive detail limit; the byte-log adapter applies its own default before escaping line breaks.
	LogBodyMaxBytes int
	RequestedModels []string // model candidates for health policy
}

// ErrorPorts supplies health updates, rule matching, and HTTP output used by error handling.
type ErrorPorts interface {
	// ScheduleActivity records activity for the selected provider before direct
	// HTTP error handling reads its body. The adapter owns any queued or background
	// work needed to publish that activity.
	ScheduleActivity()
	// ReadBody consumes the current upstream error reader and returns captured
	// bytes with its read error. A failed read can still supply partial evidence;
	// callers decide whether to retain it for classification and diagnostics.
	ReadBody() ([]byte, error)
	// ResetBody replaces the consumed upstream reader with the supplied captured
	// bytes. Retry handling invokes it before RetryHealth so that policy can read
	// the same captured response again.
	ResetBody([]byte)
	// Health evaluates upstream status and requested-model evidence against the
	// selected provider's policy. Direct error handling applies Generic and
	// Failover before display matching, carrying RetrySameProvider into the retry signal.
	Health(context.Context, int, []string) ErrorDecision
	// RetryHealth evaluates policy after the upstream retry path is exhausted.
	// It can consume the reset response reader; Generic delegates back to direct
	// handling, while Failover returns a provider-switch signal before client display.
	RetryHealth(context.Context, []string) ErrorDecision
	// Failover creates a signal retaining upstream status, captured body, and
	// same-provider retry permission. The request loop still owns whether its
	// output state and remaining attempt budget permit another exchange.
	Failover(int, []byte, bool) error
	// Commit marks the current response handling path as committed before client
	// error output. This local marker controls later execution cleanup and replay
	// choices; it does not establish client receipt of the response.
	Commit()
	// Message writes a structured Anthropic error using the selected client
	// status, kind, and message. The handler establishes commitment first and
	// chooses those fields through health and display policy.
	Message(int, string, string)
	// Raw writes the captured upstream error bytes with the selected status.
	// The unmatched 400 branch uses this path after commitment rather than
	// constructing a replacement structured error message.
	Raw(int, []byte)
	// MatchRule returns the first display rule matching platform, upstream status,
	// and captured body, or nil. Matching belongs to the configured rule service;
	// the handler separately applies that rule's display and monitoring fields.
	MatchRule(string, int, []byte) *errorpolicy.ErrorPassthroughRule
	// SkipMonitoring marks this request for the matching rule's monitoring
	// exclusion. It runs during display selection, after provider health decisions,
	// and leaves final monitoring consumption with the observation adapter.
	SkipMonitoring()
	// ScopeDiagnostic records caller-format details for a recognized authorization-scope
	// failure. The adapter chooses when debug state is available, using the
	// upstream summary, source status, and request identifier supplied here.
	ScopeDiagnostic(string, int, string)
	// SetError stores upstream status, summary, and optional body detail in request
	// diagnostics. These source observations remain separate from later client
	// status or message overrides selected by display rules.
	SetError(int, string, string)
	// Observe submits an upstream error notice to the request's observation adapter.
	Observe(Notice)
	// Log writes prepared error diagnostic text through the platform logger.
	Log(string)
	// Truncate bounds text by input bytes while retaining complete UTF-8 characters
	// for valid input. A nonpositive limit returns empty; handlers choose their
	// numeric defaults before requesting body-detail truncation.
	Truncate(string, int) string
	// TruncateBytes limits captured bytes and escapes CR/LF for single-line
	// logging. A nonpositive limit selects the adapter's default, and escaping can
	// make the returned string longer than the original input-byte budget.
	TruncateBytes([]byte, int) string
	// Sanitize masks recognized query credential values in diagnostic text.
	// The adapter recognizes key, client_secret, access_token, and refresh_token
	// query names; other message content retains its supplied text.
	Sanitize(string) string
}

// applyAnthropicDisplayRule returns client error fields from the first matched
// rule, or the supplied defaults with matched=false. A match uses kind
// upstream_error, starts with the upstream status and extracted body message,
// and applies nonnil configured overrides when their passthrough flags are false.
//
// Code and message overrides are independent, including an explicitly empty
// custom message. SkipMonitoring marks the request after a match. The caller
// invokes this display step after deciding health and failover, so its result
// controls the client envelope rather than reopening provider recovery.
func applyAnthropicDisplayRule(p ErrorPorts, platform string, upstreamStatus int, body []byte, defaultStatus int, defaultType, defaultMessage string) (int, string, string, bool) {
	rule := p.MatchRule(platform, upstreamStatus, body)
	if rule == nil {
		return defaultStatus, defaultType, defaultMessage, false
	}
	status := upstreamStatus
	if !rule.PassthroughCode && rule.ResponseCode != nil {
		status = *rule.ResponseCode
	}
	message := upstream.ExtractErrorMessage(body)
	if !rule.PassthroughBody && rule.CustomMessage != nil {
		message = *rule.CustomMessage
	}
	if rule.SkipMonitoring {
		p.SkipMonitoring()
	}
	return status, "upstream_error", message, true
}
