package provider

import (
	"strings"
)

// model_sync_error.go represents model synchronization failures with separate
// response-safe and internal diagnostics.
// Model synchronization errors carry a category for HTTP status selection and a
// message safe for the administrator response. The detailed underlying error
// remains available to internal formatting and errors.Unwrap, while SafeMessage
// returns the supplied response text or a generic default.
//
// HTTP model-sync handlers map configuration and unsupported failures to a bad
// request and other failures to a bad gateway response. Callers constructing
// this error therefore keep private upstream details in Err and supply Message
// as the text suitable for those responses.

// Synchronization error kinds select the management HTTP status while the
// separate message and cause determine public text and internal diagnostics.
const (
	// UpstreamModelSyncErrorConfiguration indicates missing provider or server
	// configuration.
	UpstreamModelSyncErrorConfiguration UpstreamModelSyncErrorKind = "configuration"
	// UpstreamModelSyncErrorUnsupported indicates a provider type that cannot sync
	// models live.
	UpstreamModelSyncErrorUnsupported UpstreamModelSyncErrorKind = "unsupported"
	// UpstreamModelSyncErrorUpstream indicates an upstream failure or unusable
	// response.
	UpstreamModelSyncErrorUpstream UpstreamModelSyncErrorKind = "upstream"
)

// UpstreamModelSyncErrorKind classifies sync failures for HTTP status mapping.
type UpstreamModelSyncErrorKind string

// UpstreamModelSyncError separates internal error details from a client-safe
// message.
type UpstreamModelSyncError struct {
	Kind UpstreamModelSyncErrorKind // management HTTP response category
	// Message is returned by SafeMessage when nonblank. Constructors keep this
	// text suitable for administrators because HTTP handlers expose it directly.
	Message string
	// Err carries the underlying diagnostic cause for Error and Unwrap.
	// HTTP handlers expose Message and do not derive response text from Err.
	Err error
}

// Error combines the client message and underlying diagnostic error for
// internal formatting. A nil receiver returns an empty string, and an absent
// cause returns Message directly.
func (e *UpstreamModelSyncError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return e.Message
	}
	return e.Message + ": " + e.Err.Error()
}

// Unwrap returns the underlying diagnostic cause for standard error-chain
// inspection. A nil receiver returns nil, allowing the error wrapper to
// participate safely in cause checks even when absent.
func (e *UpstreamModelSyncError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// SafeMessage returns the supplied response message when it contains
// nonwhitespace text. Nil errors and empty or whitespace-only messages use the
// generic sync failure text; a nonblank message retains its original spacing.
func (e *UpstreamModelSyncError) SafeMessage() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "Failed to sync upstream models"
	}
	return e.Message
}
