package provider

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// grok_failure_policy.go classifies Grok failover and same-provider retry
// eligibility from response evidence.
// Grok response failures separate request-content refusal, temporary model
// capacity, exhausted quota, and provider endpoint compatibility.
// Same-provider retries use platform classification before pool defaults, with
// a single explicit retry cap for capacity. Idle-stream errors carry replay
// eligibility separately from provider-health scope so the caller can budget
// recovery after empty output.
//
// Custom handled error codes prevent same-provider retry before capacity and
// pool rules are considered. A capacity rejection with status 429 permits a
// bounded retry even outside the pool rule, while billing, Free usage, and
// compatibility failures do not. Retry metadata carries its delay and deadline
// separately so the outer provider loop can apply its request budget.

// GrokContentPolicyClientMessage extracts and sanitizes upstream text before
// applying Grok content-refusal wording.
func GrokContentPolicyClientMessage(responseBody []byte) string {
	return grok.GrokContentPolicyClientMessage(logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(responseBody))))
}

// ShouldFailoverGrokResponse classifies response eligibility for another
// provider from status and body. Content-policy refusals stay on the current
// request; decoder compatibility, method rejection, and selected Grok failure
// classes permit their specific failover decisions.
func ShouldFailoverGrokResponse(statusCode int, responseBody []byte) bool {
	if grok.IsGrokContentPolicyRejection(statusCode, responseBody) {
		return false
	}
	// Decoder rejection identifies a wire compatibility issue for this endpoint;
	// another compatible target can use a different decoder for the same request.
	if grok.IsGrokDecoderCompatibilityError(statusCode, responseBody) {
		return true
	}
	// Some xAI-compatible endpoints use 405 for unsupported interfaces.
	// Another provider may support the interface, so permit failover.
	if statusCode == http.StatusMethodNotAllowed {
		return true
	}
	decision := grok.ClassifyGrokUpstreamFailure(statusCode, responseBody, "")
	switch decision.Class {
	case grok.GrokFailureFreeUsage, grok.GrokFailureEmptyUpstream, grok.GrokFailureBilling, grok.GrokFailureModelCapacity, grok.GrokFailureCompatibility:
		return decision.ShouldFailover
	}
	return ShouldFailoverUpstreamStatus(statusCode)
}

// GrokRetryableOnSameProvider checks Grok-specific same-provider eligibility
// before pool defaults. Custom handled codes, Free quota, billing, and
// compatibility failures reject replay, while HTTP 429 model capacity permits
// bounded retry even outside pool mode.
func GrokRetryableOnSameProvider(provider *ExecutionProvider, statusCode int, responseBody []byte) bool {
	if provider == nil || !provider.View().IsGrok() {
		return false
	}
	// A configured handled status already selects explicit provider error policy.
	// Keep that policy ahead of pool defaults so this failure cannot silently replay.
	if provider.View().IsCustomErrorCodesEnabled() && provider.View().ShouldHandleErrorCode(statusCode) {
		return false
	}
	decision := grok.ClassifyGrokUpstreamFailure(statusCode, responseBody, "")
	switch decision.Class {
	case grok.GrokFailureFreeUsage, grok.GrokFailureBilling, grok.GrokFailureCompatibility:
		// These failures identify quota, billing, or compatibility conditions that
		// another attempt on the same target does not resolve through pool retry policy.
		return false
	case grok.GrokFailureModelCapacity:
		if statusCode == http.StatusTooManyRequests {
			return true
		}
	}
	return provider.View().IsPoolMode() && provider.View().IsPoolModeRetryableStatus(statusCode)
}

// GrokSameProviderRetryMetadata returns same-provider eligibility plus
// optional delay, deadline, and explicit retry cap. Ineligible failures return
// empty metadata; eligible ordinary pool failures use caller budgets, while
// model capacity adds 500 milliseconds, thirty seconds, and one retry.
func GrokSameProviderRetryMetadata(provider *ExecutionProvider, statusCode int, responseBody []byte) (bool, time.Duration, time.Time, int) {
	if !GrokRetryableOnSameProvider(provider, statusCode, responseBody) {
		return false, 0, time.Time{}, 0
	}
	decision := grok.ClassifyGrokUpstreamFailure(statusCode, responseBody, "")
	if decision.Class != grok.GrokFailureModelCapacity {
		return true, 0, time.Time{}, 0
	}
	// Each failed attempt creates another deadline, which cannot by itself bound
	// the whole request. The explicit one-retry cap limits capacity replay across attempts.
	return true, 500 * time.Millisecond, time.Now().Add(30 * time.Second), 1
}

// GrokStreamIdleFailure constructs an empty-upstream failover error for a
// stream with no data. Grok platform targets allow one same-provider replay
// before a deadline measured by idle from now; the handler also enforces its
// request-wide budget.
func GrokStreamIdleFailure(provider *ExecutionProvider, idle time.Duration) *forwardcore.UpstreamFailoverError {
	msg := fmt.Sprintf("Grok stream idle timeout after %s with no upstream data", idle.Round(time.Second))
	return &forwardcore.UpstreamFailoverError{
		StatusCode:               502,
		ResponseBody:             []byte(`{"error":{"code":"empty_upstream","message":"` + strings.ReplaceAll(msg, `"`, `'`) + `"}}`),
		SafeToFailoverAfterWrite: true,
		// Grok platform targets permit one bounded replay for this empty-stream
		// observation; the request owner retains its separate retry and provider-switch limits.
		RetryableOnSameProvider: provider != nil && provider.Record.Platform == capability.PlatformGrok,
		RequestScopedTransient:  true,
		SameProviderRetryMax:    1,
		// The deadline starts at failure observation using idle as its allowance.
		// Eligibility after that deadline is decided by the caller's provider loop.
		SameProviderRetryDeadline: time.Now().Add(idle),
	}
}
