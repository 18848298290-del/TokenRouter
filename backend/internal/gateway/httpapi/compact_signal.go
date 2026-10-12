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

const (
	openAINativeCompactionV2Key     = "openai_native_compaction_v2"
	openAIRemoteCompactionV2Feature = "remote_compaction_v2"
)

// CompactFallbackErrorResponse converts a compact recovery signal to an HTTP error.
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
func MarkOpenAINativeCompactionV2(c *gin.Context) {
	if c != nil {
		c.Set(openAINativeCompactionV2Key, true)
	}
}

// IsOpenAINativeCompactionV2 reports whether the request was identified as native remote compaction v2.
func IsOpenAINativeCompactionV2(c *gin.Context) bool {
	return c != nil && c.GetBool(openAINativeCompactionV2Key)
}

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
// For other OAuth requests it supplies a default only when the client omitted the header.
// Other non-OAuth requests receive no additional session beta header.
func ApplyOpenAICodexBetaFeatures(c *gin.Context, oauthLike bool, h http.Header) {
	if h == nil {
		return
	}
	if IsOpenAINativeCompactionV2(c) {
		openai.EnsureRemoteCompactionV2Header(h)
		return
	}
	if !oauthLike {
		return
	}
	if hasOpenAICodexBetaFeaturesHeader(h) {
		return
	}
	h.Set("x-codex-beta-features", openAIRemoteCompactionV2Feature)
}
