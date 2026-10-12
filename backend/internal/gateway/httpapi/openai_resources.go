package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// OpenAIImageAdmissionOptions holds static image admission settings; nil options bypass admission.
type OpenAIImageAdmissionOptions struct {
	Enabled    bool
	Limit      int
	Wait       bool
	Timeout    time.Duration
	MaxWaiting int
}

// OpenAIHTTPResources references the app-owned concurrency service and local image limiter.
type OpenAIHTTPResources struct {
	Concurrency  *ConcurrencyHelper
	Images       *scheduler.ImageConcurrencyLimiter
	ImageOptions *OpenAIImageAdmissionOptions
}

func (r *OpenAIHTTPResources) AcquireUser(c *gin.Context, userID int64, limit int, stream bool, started *bool, log *zap.Logger) (func(), bool) {
	ctx := c.Request.Context()
	release, err := r.Concurrency.AcquireUserSlotWithWait(c, userID, limit, stream, started)
	if err != nil {
		log.Warn("openai.user_slot_acquire_failed", zap.Error(err))
		status, kind, code, message := ConcurrencyErrorResponse(err, "user")
		DefaultOpenAIErrorOutput().WriteStreamingErrorWithCode(c, status, kind, code, message, *started, false)
		return nil, false
	}
	return scheduler.WrapRelease(ctx, scheduler.ReleaseOnCancel, release), true
}

func (r *OpenAIHTTPResources) AcquireImage(c *gin.Context, started bool) (func(), bool) {
	if r == nil || r.Images == nil || r.ImageOptions == nil {
		return nil, true
	}
	options := r.ImageOptions
	release, ok := r.Images.Acquire(c.Request.Context(), options.Enabled, options.Limit, options.Wait, options.Timeout, options.MaxWaiting)
	if ok {
		return release, true
	}
	DefaultOpenAIErrorOutput().StreamError(c, http.StatusTooManyRequests, "rate_limit_error", "Image generation concurrency limit exceeded, please retry later", started)
	return nil, false
}
