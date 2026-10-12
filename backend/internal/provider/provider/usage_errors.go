package provider

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// AntigravityDegradedUsage builds a partial UsageInfo from a quota-fetch error.
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

// EnrichUsageWithProviderError adds stored forbidden or verification state to usage data.
// A successful usage fetch can coexist with a provider disabled by a 403.
// Stored ban or verification evidence also overrides a degraded 401's needs_reauth flag,
// so the response describes the provider's known restriction.
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
