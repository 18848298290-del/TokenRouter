package forward

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// ErrorInput contains response metadata and diagnostic settings for error handling.
type ErrorInput struct {
	ProviderID                                      int64
	ProviderName, ProviderType, Platform, RequestID string
	Status                                          int
	LogBody                                         bool
	LogBodyMaxBytes                                 int
	RequestedModels                                 []string
}

// ErrorPorts supplies health updates, rule matching, and HTTP output used by error handling.
type ErrorPorts interface {
	ScheduleActivity()
	ReadBody() ([]byte, error)
	ResetBody([]byte)
	Health(context.Context, int, []string) ErrorDecision
	RetryHealth(context.Context, []string) ErrorDecision
	Failover(int, []byte, bool) error
	Commit()
	Message(int, string, string)
	Raw(int, []byte)
	MatchRule(string, int, []byte) *errorpolicy.ErrorPassthroughRule
	SkipMonitoring()
	ScopeDiagnostic(string, int, string)
	SetError(int, string, string)
	Observe(Notice)
	Log(string)
	Truncate(string, int) string
	TruncateBytes([]byte, int) string
	Sanitize(string) string
}

// applyAnthropicDisplayRule selects status and message overrides from the first matching rule.
// Passthrough uses the extracted message; a matched SkipMonitoring rule marks the request accordingly.
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
