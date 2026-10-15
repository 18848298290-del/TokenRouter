package egress

import (
	"net/textproto"
	"regexp"
	"strings"
)

// cloudflare.go detects Cloudflare challenges and extracts diagnostic ray
// identifiers from response evidence.
// Cloudflare challenge detection is limited to forbidden and rate-limited
// responses. A mitigation header can establish a challenge before body
// inspection, while recognized text markers also identify pages whose metadata
// omits that header. Broader HTML matching requires content type, HTML framing,
// and challenge text together so a single generic word does not determine that
// result.
//
// Body inspection uses bounded, trimmed previews that can omit later evidence.
// Ray extraction separately prefers the canonical response header, then tries
// the cf-ray and cRay body patterns in order. These helpers classify available
// response evidence for quality checks and diagnostics; their callers supply
// canonical relevant header keys.

// Cloudflare body patterns identify challenge pages and ray text when canonical
// response headers do not supply the needed evidence.
var (
	cfRayPattern  = regexp.MustCompile(`(?i)cf-ray[:\s=]+([a-z0-9-]+)`)
	cRayPattern   = regexp.MustCompile(`(?i)cRay:\s*'([a-z0-9-]+)'`)
	htmlChallenge = []string{
		"window._cf_chl_opt",
		"just a moment",
		"enable javascript and cookies to continue",
		"__cf_chl_",
		"challenge-platform",
	}
)

// IsCloudflareChallengeResponse reports whether a 403 or 429 response has
// recognized challenge evidence. Other statuses return false before inspecting
// headers or body. A canonical cf-mitigated header with the challenge value
// takes precedence over body inspection.
//
// Body matching uses a lowercase preview of the first 4096 trimmed bytes plus
// the truncation marker. Known challenge markers can match directly; the
// broader HTML check additionally requires an HTML content type, HTML framing,
// and Cloudflare or challenge text.
func IsCloudflareChallengeResponse(statusCode int, headers map[string][]string, body []byte) bool {
	if statusCode != 403 && statusCode != 429 {
		return false
	}

	if headers != nil && strings.EqualFold(strings.TrimSpace(responseHeaderValue(headers, "cf-mitigated")), "challenge") {
		return true
	}

	// When mitigation headers provide no challenge result, inspect bounded body
	// evidence. Known markers can match directly; broader HTML evidence also
	// requires the response content type and framing checks below.
	preview := strings.ToLower(TruncateBody(body, 4096))
	for _, marker := range htmlChallenge {
		if strings.Contains(preview, marker) {
			return true
		}
	}

	contentType := ""
	if headers != nil {
		contentType = strings.ToLower(strings.TrimSpace(responseHeaderValue(headers, "content-type")))
	}
	if strings.Contains(contentType, "text/html") &&
		(strings.Contains(preview, "<html") || strings.Contains(preview, "<!doctype html")) &&
		(strings.Contains(preview, "cloudflare") || strings.Contains(preview, "challenge")) {
		return true
	}

	return false
}

// ExtractCloudflareRayID returns the first canonical cf-ray header value after
// trimming, or the first matching body pattern. Body extraction uses an
// 8192-byte trimmed preview and returns an empty string when neither pattern
// supplies a ray.
func ExtractCloudflareRayID(headers map[string][]string, body []byte) string {
	if headers != nil {
		rayID := strings.TrimSpace(responseHeaderValue(headers, "cf-ray"))
		if rayID != "" {
			return rayID
		}
		rayID = strings.TrimSpace(responseHeaderValue(headers, "Cf-Ray"))
		if rayID != "" {
			return rayID
		}
	}

	// Responses without a ray header can carry diagnostic ray text in HTML.
	// Try the cf-ray spelling before the cRay script spelling on a bounded preview.
	preview := TruncateBody(body, 8192)
	if matches := cfRayPattern.FindStringSubmatch(preview); len(matches) >= 2 {
		return strings.TrimSpace(matches[1])
	}
	if matches := cRayPattern.FindStringSubmatch(preview); len(matches) >= 2 {
		return strings.TrimSpace(matches[1])
	}
	return ""
}

// TruncateBody trims whitespace and limits body text to max bytes before adding a truncation marker.
// Nonpositive limits use 512 bytes; truncation can split a UTF-8 sequence.
func TruncateBody(body []byte, max int) string {
	if max <= 0 {
		max = 512
	}
	raw := strings.TrimSpace(string(body))
	if len(raw) <= max {
		return raw
	}
	// The marker is added after slicing and can make the result exceed max.
	return raw[:max] + "...(truncated)"
}

// responseHeaderValue reads the first value stored under the canonical MIME
// spelling of key. Missing or empty value slices return an empty string, and
// callers supply maps whose relevant keys use canonical spelling.
func responseHeaderValue(headers map[string][]string, key string) string {
	values := headers[textproto.CanonicalMIMEHeaderKey(key)]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
