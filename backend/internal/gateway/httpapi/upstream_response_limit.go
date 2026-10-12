package httpapi

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
)

// TooLargeWriter writes a response-size error in the inbound protocol's format.
type TooLargeWriter func(*gin.Context)

// ReadUpstreamResponseBody enforces the byte limit and reports size errors through Ops and the supplied writer.
func ReadUpstreamResponseBody(reader io.Reader, maxBytes int64, c *gin.Context, onTooLarge TooLargeWriter) ([]byte, error) {
	body, err := httpclient.ReadResponseBodyLimited(reader, maxBytes)
	if err != nil {
		if errors.Is(err, httpclient.ErrResponseBodyTooLarge) {
			SetOpsUpstreamError(c, http.StatusBadGateway, "upstream response too large", "")
			if onTooLarge != nil {
				onTooLarge(c)
			}
		}
		return nil, err
	}
	return body, nil
}

func AnthropicResponseTooLarge(c *gin.Context) {
	c.JSON(http.StatusBadGateway, gin.H{"type": "error", "error": gin.H{"type": "upstream_error", "message": "Upstream response too large"}})
}

func OpenAIResponseTooLarge(c *gin.Context) {
	c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "upstream_error", "message": "Upstream response too large"}})
}
