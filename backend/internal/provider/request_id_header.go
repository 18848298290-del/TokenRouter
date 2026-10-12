package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	maxUpstreamRequestIDHeaderNameLen = 64

	// ProviderExtraUpstreamRequestIDHeader names the Extra field containing the direct upstream request-ID
	// response header.
	// An unset field disables upstream request-ID recording.
	ProviderExtraUpstreamRequestIDHeader = "upstream_request_id_header"
)

// UpstreamRequestIDHeaderName returns the configured upstream request-ID header, or an empty string when unset.
func UpstreamRequestIDHeaderName(provider *Record) string {
	if provider == nil {
		return ""
	}
	return strings.TrimSpace(provider.GetExtraString(ProviderExtraUpstreamRequestIDHeader))
}

// ValidateUpstreamRequestIDHeaderExtra trims and validates the header name in place.
// Names must be valid HTTP header fields of at most 64 bytes; blank strings remove the Extra entry.
func ValidateUpstreamRequestIDHeaderExtra(extra map[string]any) error {
	if extra == nil {
		return nil
	}
	raw, ok := extra[ProviderExtraUpstreamRequestIDHeader]
	if !ok || raw == nil {
		return nil
	}
	name, ok := raw.(string)
	if !ok {
		return infraerrors.BadRequest("INVALID_UPSTREAM_REQUEST_ID_HEADER",
			"upstream_request_id_header must be a string")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		delete(extra, ProviderExtraUpstreamRequestIDHeader)
		return nil
	}
	if len(name) > maxUpstreamRequestIDHeaderNameLen || !egress.ValidHeaderName(name) {
		return infraerrors.BadRequest("INVALID_UPSTREAM_REQUEST_ID_HEADER",
			"upstream_request_id_header must be a valid HTTP header name of at most 64 bytes")
	}
	extra[ProviderExtraUpstreamRequestIDHeader] = name
	return nil
}
