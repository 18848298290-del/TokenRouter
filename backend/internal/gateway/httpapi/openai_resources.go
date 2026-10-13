package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// OpenAI HTTP entries share user concurrency and a process-local image limit.
// User admission returns cleanup bound to the arriving request context, while
// image admission can bypass limiting or reject with the protocol's streaming
// error format according to startup settings.

// OpenAIImageAdmissionOptions configures process-local admission for
// image-generation requests. The limiter bypasses admission when disabled or
// when Limit is nonpositive.
type OpenAIImageAdmissionOptions struct {
	Enabled bool // image admission enabled flag
	Limit   int  // concurrent image-request ceiling
	Wait    bool // full-limit waiting enabled flag
	// Timeout bounds a waiting acquisition. The image limiter rejects
	// waiting requests when this duration is nonpositive, even if no wait
	// has occurred.
	Timeout    time.Duration
	MaxWaiting int // positive waiting-request ceiling
}

// OpenAIHTTPResources exposes app-owned user concurrency and process-local
// image admission.
type OpenAIHTTPResources struct {
	Concurrency *ConcurrencyHelper                 // shared user-slot admission helper
	Images      *scheduler.ImageConcurrencyLimiter // process-local image-slot admission limiter
	// ImageOptions supplies the limiter's startup rules. A nil value
	// bypasses image admission, independently of whether Images has been
	// installed.
	ImageOptions *OpenAIImageAdmissionOptions
}

// AcquireUser waits for a user slot and returns its release callback on
// success. Callers provide a request, started flag, logger, and configured
// concurrency helper. Failure writes the appropriate streaming error;
// cancellation also releases the slot.
func (r *OpenAIHTTPResources) AcquireUser(c *gin.Context, userID int64, limit int, stream bool, started *bool, log *zap.Logger) (func(), bool) {
	ctx := c.Request.Context()
	release, err := r.Concurrency.AcquireUserSlotWithWait(c, userID, limit, stream, started)
	if err != nil {
		log.Warn("openai.user_slot_acquire_failed", zap.Error(err))
		status, kind, code, message := ConcurrencyErrorResponse(err, "user")
		DefaultOpenAIErrorOutput().WriteStreamingErrorWithCode(c, status, kind, code, message, *started, false)
		return nil, false
	}
	// Bind cleanup to the context captured before waiting, so disconnects
	// can release user capacity while repeated completion cleanup remains
	// harmless.
	return scheduler.WrapRelease(ctx, scheduler.ReleaseOnCancel, release), true
}

// AcquireImage obtains an image slot using the request context and startup
// limits. Missing resource, limiter, or options bypasses admission with a nil
// release. Callers invoke a returned release after image work ends; rejection
// writes 429.
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
