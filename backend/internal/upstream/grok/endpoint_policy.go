package grok

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// endpoint_policy.go selects Grok billing, media, and Voice endpoints from
// provider bases and caller URL policy.
// Grok subscription inference, billing and media do not all use the same
// official host. Billing selects the CLI base for official public or regional
// endpoints, while Voice uses the public base when the supplied base is missing
// or is the CLI proxy. Custom relays retain their configured endpoint and still
// pass the supplied validator.
//
// OAuth base resolution applies the selected trusted-base policy before
// considering a structurally valid custom relay. A malformed value selects the
// trimmed fallback, whose default depends on credential kind. That selection
// establishes a base rather than an outbound authorization decision: request
// builders apply the caller's URL policy when constructing the actual endpoint,
// and path segments are escaped where resource identifiers are appended.

// RedactedBaseURLValidator wraps a required validator and replaces rejected-URL
// diagnostics with a fixed policy message. Successful normalized values pass
// through unchanged, allowing request errors to report rejection without
// exposing the validator's raw target details.
func RedactedBaseURLValidator(validator BaseURLValidator) BaseURLValidator {
	return func(raw string) (string, error) {
		validated, err := validator(raw)
		if err != nil {
			// Validator errors can expose target details, so this wrapper reports the
			// fixed rejection message before gateway error presentation.
			return "", errors.New("base URL rejected by URL security policy")
		}
		return validated, nil
	}
}

// BuildBillingEndpointURL selects the CLI billing base for official public or
// regional hosts. IsOfficialBaseURL also classifies blank, unparseable and
// hostless input as official, so those inputs select the CLI default. Parsed
// custom relays retain their base. The shared builder selects weekly when
// requested and monthly otherwise; a nil validator uses platform base policy.
func BuildBillingEndpointURL(baseURL string, weekly bool, validator BaseURLValidator) (string, error) {
	// The permissive official-host classifier also supplies a default for unusable
	// bases here; parsed custom relay hosts keep their configured billing route.
	if IsOfficialBaseURL(baseURL) && !(MediaCodec{}).IsGrokCLIProxyTarget(baseURL) {
		baseURL = DefaultCLIBaseURL
	}
	return BuildBillingURLWithValidator(baseURL, weekly, validator)
}

// BuildMediaEndpointURL selects the requested media route through the shared URL
// builders. A nil validator uses platform base policy. Video status/content
// encode requestID as one path segment; content appends its suffix, and
// unsupported endpoint values return an error.
func BuildMediaEndpointURL(baseURL string, endpoint GrokMediaEndpoint, requestID string, validator BaseURLValidator) (string, error) {
	switch endpoint {
	case GrokMediaEndpointImagesGenerations:
		return BuildImagesGenerationsURLWithValidator(baseURL, validator)
	case GrokMediaEndpointImagesEdits:
		return BuildImagesEditsURLWithValidator(baseURL, validator)
	case GrokMediaEndpointVideosGenerations:
		return BuildVideosGenerationsURLWithValidator(baseURL, validator)
	case GrokMediaEndpointVideosEdits:
		return BuildVideosEditsURLWithValidator(baseURL, validator)
	case GrokMediaEndpointVideosExtensions:
		return BuildVideosExtensionsURLWithValidator(baseURL, validator)
	case GrokMediaEndpointVideoStatus:
		return BuildVideoURLWithValidator(baseURL, requestID, validator)
	case GrokMediaEndpointVideoContent:
		videoURL, err := BuildVideoURLWithValidator(baseURL, requestID, validator)
		if err != nil {
			return "", err
		}
		return videoURL + "/content", nil
	default:
		return "", fmt.Errorf("unsupported grok media endpoint: %s", endpoint)
	}
}

// BuildVoiceEndpointURL appends individually escaped Voice path segments to a
// validated base. Missing or CLI-proxy bases select DefaultBaseURL. Callers
// provide validator and apply ValidateVoiceEndpoint for operation-specific
// route limits. This builder rejects empty and dot segments while encoding the
// remaining path text.
func BuildVoiceEndpointURL(base string, endpoint string, validator BaseURLValidator) (string, error) {
	// Voice endpoints use the public base when inference selects the CLI proxy;
	// retain custom bases for relays that expose Voice.
	if strings.TrimSpace(base) == "" || (MediaCodec{}).IsGrokCLIProxyTarget(base) {
		base = DefaultBaseURL
	}
	validated, err := validator(base)
	if err != nil {
		return "", err
	}
	ep := strings.Trim(strings.TrimSpace(endpoint), "/")
	if ep == "" {
		return "", fmt.Errorf("voice endpoint is required")
	}
	parts := strings.Split(ep, "/")
	encoded := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid voice endpoint path")
		}
		encoded = append(encoded, url.PathEscape(part))
	}
	return strings.TrimRight(validated, "/") + "/" + strings.Join(encoded, "/"), nil
}

// ResolveProviderBaseURL chooses a configured provider base or a
// credential-specific fallback. Blank fallback defaults to CLI for OAuth and
// public API for API keys. A configured API-key base is returned trimmed; OAuth
// first accepts any base admitted by ValidateTrustedBaseURL, whose unsafe
// override relaxes official-host and known-path restrictions.
//
// Structurally valid custom OAuth relay URLs retain their configured base after
// trailing-slash removal. A malformed OAuth URL selects fallback instead. This
// selection preserves relay identity for later request construction; callers
// validate the final endpoint against outbound policy because neither custom
// acceptance nor fallback selection authorizes a network destination.
func ResolveProviderBaseURL(oauth bool, configured, fallback string) string {
	fallback = strings.TrimRight(strings.TrimSpace(fallback), "/")
	if fallback == "" {
		if oauth {
			fallback = DefaultCLIBaseURL
		} else {
			fallback = DefaultBaseURL
		}
	}
	base := strings.TrimSpace(configured)
	if base == "" {
		return fallback
	}
	if !oauth {
		return base
	}
	// Apply the selected trusted-base policy first, including its unsafe override.
	// Rejected input can still qualify structurally as a relay before outbound
	// validation.
	if validated, err := ValidateTrustedBaseURL(base); err == nil {
		return validated
	}
	if parsed, err := url.Parse(base); err == nil && parsed.Scheme != "" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" {
		return strings.TrimRight(base, "/")
	}
	// Malformed configured OAuth bases select the caller's fallback, which still
	// needs outbound validation when the final request is constructed.
	return fallback
}
