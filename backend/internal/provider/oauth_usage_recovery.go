package provider

import (
	"context"
	"errors"
	"strings"
)

// UsageRecoveryVersion captures the credential identity and error a successful usage query may clear.
type UsageRecoveryVersion struct {
	CredentialVersion
	ErrorMessage string `json:"-"`
}
type UsageRecoveryWriter interface {
	ClearUsageErrorIfUnchanged(context.Context, UsageRecoveryVersion) (bool, error)
}

// RecoverUsageProviderError updates the record after conditionally clearing a recoverable stored error.
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
	version := UsageRecoveryVersion{CredentialVersion: FailureVersion(value).CredentialVersion, ErrorMessage: value.ErrorMessage}
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
