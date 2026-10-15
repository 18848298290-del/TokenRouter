package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/compact"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// compact_signal.go carries native compaction markers into Codex headers and
// adapts captured failures for HTTP error handling.
// Native compaction is recognized from request content before outbound headers
// are built. Its saved marker requires the remote_compaction_v2 feature even
// when ordinary OAuth defaulting would leave a client-supplied feature list alone.
// Non-native OAuth requests receive a default only when every header value is
// blank.
//
// Captured compact failures also need an HTTP-shaped response for existing error
// handling. The adapter promotes the captured payload through compact's error
// normalizer, clones headers from a supplied response, and constructs a bad-request
// body. This synthetic response is separate from the rejected exchange: callers
// still release that exchange and treat the returned payload as the bytes shared
// by the new body's reader.

// Native compaction values link the request recognition marker to the beta
// feature supplied during outbound Codex header construction.
const (
	openAINativeCompactionV2Key     = "openai_native_compaction_v2" // native request marker key
	openAIRemoteCompactionV2Feature = "remote_compaction_v2"        // native beta feature token
)

// CompactFallbackErrorResponse converts a compact recovery signal to an HTTP error.
// A nil response uses fresh JSON headers. Callers supplying a response also
// supply an initialized Header: cloning a nil map leaves the default
// Content-Type assignment with no writable map and panics. Status is 400, and
// a nil signal produces an empty payload. Returned payload bytes back the new
// body reader, so callers leave them unchanged while reading.
// The original response body remains under its caller's cleanup ownership.
func CompactFallbackErrorResponse(resp *http.Response, signal *compact.Failure) (*http.Response, []byte) {
	headers := make(http.Header)
	if resp != nil {
		headers = resp.Header.Clone()
	}
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "application/json")
	}
	payload := compact.NormalizeHTTPErrorPayload(signal)
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     headers,
		Body:       io.NopCloser(bytes.NewReader(payload)),
	}, payload
}

// MarkOpenAINativeCompactionV2 marks the request as native compaction v2.
// A nil context is ignored. Recognition callers set this before request
// construction so capability selection and outbound beta-feature handling can
// distinguish native compaction from the legacy unary path.
func MarkOpenAINativeCompactionV2(c *gin.Context) {
	if c != nil {
		c.Set(openAINativeCompactionV2Key, true)
	}
}

// IsOpenAINativeCompactionV2 reports whether the request was identified as native
// remote compaction v2.
// Nil contexts and missing or nonboolean values return false through Gin's
// boolean lookup. Callers use the recognition marker independently of any beta
// header the client may have supplied.
func IsOpenAINativeCompactionV2(c *gin.Context) bool {
	return c != nil && c.GetBool(openAINativeCompactionV2Key)
}

// hasOpenAICodexBetaFeaturesHeader reports whether any beta-feature header value
// is nonblank.
// Nil maps and entirely whitespace values return false. Inspecting all values
// prevents an empty first value from causing OAuth defaulting over a later
// client-supplied feature list.
func hasOpenAICodexBetaFeaturesHeader(h http.Header) bool {
	if h == nil {
		return false
	}
	for _, value := range h.Values("x-codex-beta-features") {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// ApplyOpenAICodexBetaFeatures adds remote_compaction_v2 for native v2 requests.
// A nil header map is ignored. Native recognition takes precedence over provider
// classification and the existing header list, asking the upstream helper to
// merge the required feature before ordinary OAuth defaulting is considered.
//
// For other requests, the selected provider's OAuth eligibility permits a
// default only when every supplied header value is blank. Checking the full
// value list retains a later nonblank client value even when the first is empty.
// Non-OAuth providers keep their supplied feature values. Callers apply this
// after recognizing request content and forwarding the client's negotiation.
func ApplyOpenAICodexBetaFeatures(c *gin.Context, oauthLike bool, h http.Header) {
	if h == nil {
		return
	}
	if IsOpenAINativeCompactionV2(c) {
		// Native compaction's wire format needs its negotiated feature for any
		// provider type, so merge it before considering ordinary OAuth header defaulting.
		openai.EnsureRemoteCompactionV2Header(h)
		return
	}
	if !oauthLike {
		return
	}
	if hasOpenAICodexBetaFeaturesHeader(h) {
		return
	}
	// Blank OAuth negotiation receives the compaction feature used by Codex.
	// Existing nonblank lists returned earlier so unrelated client feature choices
	// remain available.
	h.Set("x-codex-beta-features", openAIRemoteCompactionV2Feature)
}
