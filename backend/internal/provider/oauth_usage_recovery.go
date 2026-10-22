package provider

import (
	"context"
	"errors"
	"strings"
)

// oauth_usage_recovery.go conditionally clears recoverable provider errors
// after successful usage queries.
// Usage recovery clears one observed credential error after the writer
// accepts its compared identity and message. Newer status, credentials, proxy,
// or error text can reject that recovery while keeping the caller record
// unchanged; category matching and persistence use different message forms.
//
// The writer compares platform, type, status, complete credentials, proxy, and
// the original error text. Category matching trims and lowercases the message,
// while persistence retains its exact spelling for comparison. Accepted
// storage precedes local status/error changes, so a rejected observation leaves
// the caller record unchanged. These steps do not undo another health window
// or the scheduling switch. Callers synchronize borrowed records during
// snapshot capture and mutation; supported-container copies in
// ObserveCredentialVersion leave unsupported mutable values shared with their
// owner.

// UsageRecoveryVersion associates observed credential identity with the exact
// stored recovery error.
type UsageRecoveryVersion struct {
	CredentialVersion        // observed provider credential identity
	ErrorMessage      string `json:"-"` // exact observed recovery error
}

// UsageRecoveryWriter clears a matching stored error after observed identity
// comparison.
type UsageRecoveryWriter interface {
	// ClearUsageErrorIfUnchanged clears the observed credential error after
	// comparison. False reports a miss so callers keep their local record
	// unchanged. The PostgreSQL writer compares ID, platform, type, status,
	// complete credentials, proxy, and exact error text.
	ClearUsageErrorIfUnchanged(context.Context, UsageRecoveryVersion) (bool, error)
}

// RecoverUsageProviderError conditionally recovers a stable caller-owned error
// record. Nil, other statuses, blank errors, and unrecognized error text skip
// work; a recognized error requires the conditional writer.
//
// Matching uses lowercased trimmed text to select eligible error categories,
// but the writer receives the exact stored error and credential version for
// comparison. Writer failure or a false match leaves the local record
// unchanged. Only accepted persistence sets the supplied record active and
// clears its error, so callers synchronize that record throughout recovery.
func RecoverUsageProviderError(ctx context.Context, value *Record, writer UsageRecoveryWriter) (bool, error) {
	if value == nil || value.Status != StatusError {
		return false, nil
	}
	msg := strings.ToLower(strings.TrimSpace(value.ErrorMessage))
	if msg == "" {
		return false, nil
	}
	if !strings.Contains(msg, "token refresh failed") && !strings.Contains(msg, "invalid_client") && !strings.Contains(msg, "missing_project_id") && !strings.Contains(msg, "unauthenticated") {
		return false, nil
	}
	if writer == nil {
		return false, errors.New("usage recovery conditional writer is not configured")
	}
	version := UsageRecoveryVersion{CredentialVersion: ObserveCredentialVersion(value), ErrorMessage: value.ErrorMessage}
	// Compare observed identity and exact error before changing local health.
	// An administrator edit can reject this recovery while leaving its newer
	// state authoritative.
	applied, err := writer.ClearUsageErrorIfUnchanged(ctx, version)
	if err != nil {
		return false, err
	}
	if applied {
		value.Status = StatusActive
		value.ErrorMessage = ""
	}
	return applied, nil
}
