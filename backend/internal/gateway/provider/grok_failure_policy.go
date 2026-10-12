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

func GrokContentPolicyClientMessage(responseBody []byte) string {
	return grok.GrokContentPolicyClientMessage(logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(responseBody))))
}

// ShouldFailoverGrokResponse uses status and body to decide whether to switch providers.
// Content-policy refusals return to the caller on the current provider.
func ShouldFailoverGrokResponse(statusCode int, responseBody []byte) bool {
	if grok.IsGrokContentPolicyRejection(statusCode, responseBody) {
		return false
	}
	// A ModelInput decoding 422 may succeed on another compatible provider.
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

// GrokRetryableOnSameProvider identifies transient errors eligible for the shared retry loop.
// Capacity pressure permits bounded retry; quota and billing exhaustion require another provider.
func GrokRetryableOnSameProvider(provider *ExecutionProvider, statusCode int, responseBody []byte) bool {
	if provider == nil || !provider.View().IsGrok() {
		return false
	}
	// Configured error codes take precedence over pool retry defaults.
	if provider.View().IsCustomErrorCodesEnabled() && provider.View().ShouldHandleErrorCode(statusCode) {
		return false
	}
	decision := grok.ClassifyGrokUpstreamFailure(statusCode, responseBody, "")
	switch decision.Class {
	case grok.GrokFailureFreeUsage, grok.GrokFailureBilling, grok.GrokFailureCompatibility:
		// Replaying cannot restore exhausted quota or entitlement.
		return false
	case grok.GrokFailureModelCapacity:
		if statusCode == http.StatusTooManyRequests {
			return true
		}
	}
	return provider.View().IsPoolMode() && provider.View().IsPoolModeRetryableStatus(statusCode)
}

func GrokSameProviderRetryMetadata(provider *ExecutionProvider, statusCode int, responseBody []byte) (bool, time.Duration, time.Time, int) {
	if !GrokRetryableOnSameProvider(provider, statusCode, responseBody) {
		return false, 0, time.Time{}, 0
	}
	decision := grok.ClassifyGrokUpstreamFailure(statusCode, responseBody, "")
	if decision.Class != grok.GrokFailureModelCapacity {
		return true, 0, time.Time{}, 0
	}
	// Each attempt creates a new error deadline, so the deadline alone cannot bound the request.
	// Allow at most one capacity retry, even if the initial attempt took over 30 seconds.
	return true, 500 * time.Millisecond, time.Now().Add(30 * time.Second), 1
}

// GrokStreamIdleFailure builds a failover error for a Grok stream that has produced no upstream data.
func GrokStreamIdleFailure(provider *ExecutionProvider, idle time.Duration) *forwardcore.UpstreamFailoverError {
	msg := fmt.Sprintf("Grok stream idle timeout after %s with no upstream data", idle.Round(time.Second))
	return &forwardcore.UpstreamFailoverError{
		StatusCode:               502,
		ResponseBody:             []byte(`{"error":{"code":"empty_upstream","message":"` + strings.ReplaceAll(msg, `"`, `'`) + `"}}`),
		SafeToFailoverAfterWrite: true,
		// Use the same-provider retry budget before switching credentials.
		// The handler still enforces request-wide retry limits.
		RetryableOnSameProvider: provider != nil && provider.Record.Platform == capability.PlatformGrok,
		RequestScopedTransient:  true,
		// Allow one replay before the deadline measured from this failure.
		// After the deadline, switch providers.
		SameProviderRetryMax:      1,
		SameProviderRetryDeadline: time.Now().Add(idle),
	}
}
