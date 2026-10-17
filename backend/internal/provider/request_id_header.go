package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// request_id_header.go validates and reads the configured upstream response
// header used for request-ID capture.
// Request diagnostics and usage capture read the administrator's selected
// response header from the direct upstream, using X-Request-ID when the setting
// is absent or blank. Saving validates header token syntax and size before
// replacing the Extra value used by those readers.

// Header-name size and Extra-key values govern administrator validation and the
// direct upstream Header read used by request-ID capture.
const (
	maxUpstreamRequestIDHeaderNameLen = 64

	// ProviderExtraUpstreamRequestIDHeader names the Extra field selecting the
	// direct upstream response header. An unset field lets request diagnostics
	// and usage capture select their default X-Request-ID header.
	ProviderExtraUpstreamRequestIDHeader = "upstream_request_id_header"
)

// UpstreamRequestIDHeaderName returns the trimmed response header override used
// for upstream request-ID capture. Nil providers or unset values return empty;
// request diagnostics and usage capture apply their default header selection.
func UpstreamRequestIDHeaderName(provider *Record) string {
	if provider == nil {
		return ""
	}
	return strings.TrimSpace(provider.GetExtraString(ProviderExtraUpstreamRequestIDHeader))
}

// ValidateUpstreamRequestIDHeaderExtra validates and normalizes a supplied
// response Header name in the caller-owned Extra map. Missing or nil values are
// ignored; blank strings remove the key, while invalid types, syntax or
// excessive length return a structured error.
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
	// Administrator saves validate the capture setting before observation uses it
	// directly; invalid names would otherwise select an unusable response Header
	// for request-ID lookup.
	if len(name) > maxUpstreamRequestIDHeaderNameLen || !egress.ValidHeaderName(name) {
		return infraerrors.BadRequest("INVALID_UPSTREAM_REQUEST_ID_HEADER",
			"upstream_request_id_header must be a valid HTTP header name of at most 64 bytes")
	}
	extra[ProviderExtraUpstreamRequestIDHeader] = name
	return nil
}
