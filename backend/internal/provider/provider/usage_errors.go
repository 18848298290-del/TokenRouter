package provider

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// Usage display can combine a failed quota fetch with an existing provider
// restriction. Antigravity fetch errors distinguish authentication, rate
// limiting, and network classification, while an error-state record can
// separately carry forbidden or verification evidence.
//
// Stored forbidden evidence takes precedence when the response is enriched,
// including over a fetch error that requested reauthorization. The resulting
// flags point administrators toward verification or a ban instead of another
// token refresh. Enrichment requires both the provider error state and
// recognized restriction text; ordinary provider errors leave the existing
// usage result unchanged, and the caller retains ownership of that result for
// mutation.

// AntigravityDegradedUsage builds a partial usage response from a nonnil
// quota-fetch error and the supplied observation time. It logs the failure and
// classifies authentication or rate-limit fragments, treating other messages as
// network errors.
func AntigravityDegradedUsage(err error, now time.Time) *providercore.UsageInfo {
	errMsg := fmt.Sprintf("usage API error: %v", err)
	slog.Warn("antigravity usage fetch failed, returning degraded response", "error", err)

	info := &providercore.UsageInfo{
		UpdatedAt: &now,
		Error:     errMsg,
	}

	// Classify the text returned by the Antigravity quota client.
	// HTTP status fragments survive the client's contextual error wrapping.
	errStr := err.Error()
	switch {
	case strings.Contains(errStr, "HTTP 401") ||
		strings.Contains(errStr, "UNAUTHENTICATED") ||
		strings.Contains(errStr, "invalid_grant"):
		info.ErrorCode = providercore.ErrorCodeUnauthenticated
		info.NeedsReauth = true
	case strings.Contains(errStr, "HTTP 429"):
		info.ErrorCode = providercore.ErrorCodeRateLimited
	default:
		info.ErrorCode = providercore.ErrorCodeNetworkError
	}

	return info
}

// EnrichUsageWithProviderError overlays stored forbidden evidence onto the
// caller's mutable usage response. Nil inputs, records outside error state, and
// messages without recognized restriction fragments leave the response
// unchanged.
//
// A matched message supplies the forbidden type, reason, and validation URL.
// Its ban or verification classification replaces the response error code and
// clears NeedsReauth, so an existing known restriction remains visible even
// when a concurrent quota fetch succeeds or reports an authentication error.
// Other usage measurements and fetch timestamps remain in the response.
func EnrichUsageWithProviderError(info *providercore.UsageInfo, value *providercore.Record) {
	if info == nil || value == nil || value.Status != providercore.StatusError {
		return
	}
	msg := strings.ToLower(value.ErrorMessage)
	if !strings.Contains(msg, "403") && !strings.Contains(msg, "forbidden") &&
		!strings.Contains(msg, "violation") && !strings.Contains(msg, "validation") {
		return
	}
	fbType := antigravity.ClassifyForbiddenType(value.ErrorMessage)
	info.IsForbidden = true
	info.ForbiddenType = fbType
	info.ForbiddenReason = value.ErrorMessage
	info.NeedsVerify = fbType == providercore.ForbiddenTypeValidation
	info.IsBanned = fbType == providercore.ForbiddenTypeViolation
	info.ValidationURL = antigravity.ExtractValidationURL(value.ErrorMessage)
	info.ErrorCode = providercore.ErrorCodeForbidden
	info.NeedsReauth = false
}
