package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Image requests contend for a process-local limit before upstream execution.
// A one-slot fixture keeps its first acquisition active while making a second
// request, checking that saturation produces the OpenAI rate-limit response.

// TestOpenAIGatewayHandlerAcquireImageGenerationSlot_Returns429WhenFull holds
// the only image slot while attempting another acquisition. It checks the
// rejected release callback, HTTP 429, error type, and client-visible
// concurrency message.
func TestOpenAIGatewayHandlerAcquireImageGenerationSlot_Returns429WhenFull(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	resource := &OpenAIHTTPResources{Images: &scheduler.ImageConcurrencyLimiter{}, ImageOptions: &OpenAIImageAdmissionOptions{Enabled: true, Limit: 1}}

	release, acquired := resource.AcquireImage(c, false)
	require.True(t, acquired)
	require.NotNil(t, release)
	defer release()

	blockedRelease, blocked := resource.AcquireImage(c, false)

	require.False(t, blocked)
	require.Nil(t, blockedRelease)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "rate_limit_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Contains(t, rec.Body.String(), "Image generation concurrency limit exceeded")
}
