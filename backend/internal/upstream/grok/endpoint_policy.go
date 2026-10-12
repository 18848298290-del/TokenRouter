package grok

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

func RedactedBaseURLValidator(validator BaseURLValidator) BaseURLValidator {
	return func(raw string) (string, error) {
		validated, err := validator(raw)
		if err != nil {
			return "", errors.New("base URL rejected by URL security policy")
		}
		return validated, nil
	}
}

func BuildBillingEndpointURL(baseURL string, weekly bool, validator BaseURLValidator) (string, error) {
	// Public and regional API hosts do not expose Grok Build billing endpoints.
	// Custom relays may proxy both inference and billing, so retain their configured host.
	if IsOfficialBaseURL(baseURL) && !(MediaCodec{}).IsGrokCLIProxyTarget(baseURL) {
		baseURL = DefaultCLIBaseURL
	}
	return BuildBillingURLWithValidator(baseURL, weekly, validator)
}

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

func BuildVoiceEndpointURL(base string, endpoint string, validator BaseURLValidator) (string, error) {
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

// ResolveProviderBaseURL normalizes official OAuth URLs and retains configured relay URLs.
// The caller validates the target against outbound policy when building a request.
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
	if validated, err := ValidateTrustedBaseURL(base); err == nil {
		return validated
	}
	if parsed, err := url.Parse(base); err == nil && parsed.Scheme != "" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" {
		return strings.TrimRight(base, "/")
	}
	return fallback
}
