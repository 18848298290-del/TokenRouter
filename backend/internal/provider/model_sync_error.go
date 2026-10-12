package provider

import (
	"strings"
)

const (
	// UpstreamModelSyncErrorConfiguration indicates missing provider or server configuration.
	UpstreamModelSyncErrorConfiguration UpstreamModelSyncErrorKind = "configuration"
	// UpstreamModelSyncErrorUnsupported indicates a provider type that cannot sync models live.
	UpstreamModelSyncErrorUnsupported UpstreamModelSyncErrorKind = "unsupported"
	// UpstreamModelSyncErrorUpstream indicates an upstream failure or unusable response.
	UpstreamModelSyncErrorUpstream UpstreamModelSyncErrorKind = "upstream"
)

// UpstreamModelSyncErrorKind classifies sync failures for HTTP status mapping.
type UpstreamModelSyncErrorKind string

// UpstreamModelSyncError separates internal error details from a client-safe message.
type UpstreamModelSyncError struct {
	Kind    UpstreamModelSyncErrorKind
	Message string
	Err     error
}

func (e *UpstreamModelSyncError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return e.Message
	}
	return e.Message + ": " + e.Err.Error()
}

func (e *UpstreamModelSyncError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// SafeMessage returns the supplied client-safe message, or a generic message when empty.
func (e *UpstreamModelSyncError) SafeMessage() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "Failed to sync upstream models"
	}
	return e.Message
}
