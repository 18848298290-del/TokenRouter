package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

const (
	gatewayStreamHeartbeatBytesKey = "gateway_stream_heartbeat_bytes"
	maxConcurrencyWait             = 30 * time.Second
	DefaultPingInterval            = 10 * time.Second

	// SSEPingFormatClaude is the Claude/Anthropic SSE ping format.
	SSEPingFormatClaude SSEPingFormat = "data: {\"type\": \"ping\"}\n\n"
	// SSEPingFormatNone disables ping output.
	SSEPingFormatNone SSEPingFormat = ""
	// SSEPingFormatComment is an SSE comment ping for OpenAI/Codex CLI clients.
	SSEPingFormatComment SSEPingFormat = ":\n\n"
)

// SSEPingFormat defines the wire format of a keepalive event.
type SSEPingFormat string

// ConcurrencyError aliases the scheduler error for HTTP callers, including errors.As matching.
type ConcurrencyError = scheduler.ConcurrencyError

type WaitQueueFullError = scheduler.WaitQueueFullError

// ConcurrencyHelper adapts concurrency slot management to gateway HTTP handlers.
type ConcurrencyHelper struct {
	concurrencyService *scheduler.ConcurrencyService
	keyID              func(*gin.Context) int64
	pingFormat         SSEPingFormat
	pingInterval       time.Duration
}

func RecordStreamHeartbeat(c *gin.Context, written int) {
	if c == nil || written <= 0 {
		return
	}
	total, _ := c.Get(gatewayStreamHeartbeatBytesKey)
	bytes, _ := total.(int)
	c.Set(gatewayStreamHeartbeatBytesKey, bytes+written)
}

func StreamHasOnlyHeartbeats(c *gin.Context) bool {
	if c == nil || c.Writer == nil {
		return false
	}
	value, ok := c.Get(gatewayStreamHeartbeatBytesKey)
	if !ok {
		return false
	}
	heartbeatBytes, _ := value.(int)
	return heartbeatBytes > 0 && c.Writer.Size() == heartbeatBytes
}

// NewConcurrencyHelper binds slot management, ping settings, and an optional API key resolver.
func NewConcurrencyHelper(concurrencyService *scheduler.ConcurrencyService, pingFormat SSEPingFormat, pingInterval time.Duration, keyID ...func(*gin.Context) int64) *ConcurrencyHelper {
	if pingInterval <= 0 {
		pingInterval = DefaultPingInterval
	}
	resolveKey := func(c *gin.Context) int64 {
		if key, ok := EffectiveAPIKey(c); ok && key != nil {
			return key.ID
		}
		return 0
	}
	if len(keyID) > 0 && keyID[0] != nil {
		resolveKey = keyID[0]
	}
	return &ConcurrencyHelper{
		keyID:              resolveKey,
		concurrencyService: concurrencyService,
		pingFormat:         pingFormat,
		pingInterval:       pingInterval,
	}
}

// EnterUserWait returns an owned scheduler wait result; callers must release an entered wait.
func (h *ConcurrencyHelper) EnterUserWait(ctx context.Context, id int64, limit int) (scheduler.WaitResult, error) {
	return h.concurrencyService.EnterUserWait(ctx, id, limit)
}

func (h *ConcurrencyHelper) EnterProviderWait(ctx context.Context, id int64, limit int) (scheduler.WaitResult, error) {
	return h.concurrencyService.EnterProviderWait(ctx, id, limit)
}

// TryAcquireUserSlot attempts immediate acquisition of a user slot.
// It returns the release function, whether a slot was acquired, and any store error.
func (h *ConcurrencyHelper) TryAcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int) (func(), bool, error) {
	result, err := h.concurrencyService.AcquireUserSlot(ctx, userID, maxConcurrency)
	if err != nil {
		return nil, false, err
	}
	if !result.Acquired {
		return nil, false, nil
	}
	return result.ReleaseFunc, true, nil
}

// TryAcquireUserSlotForAPIKey acquires a user slot and tracks the API key's statistics slot.
func (h *ConcurrencyHelper) TryAcquireUserSlotForAPIKey(ctx context.Context, userID int64, maxConcurrency int, apiKeyID int64) (func(), bool, error) {
	releaseFunc, acquired, err := h.TryAcquireUserSlot(ctx, userID, maxConcurrency)
	if err != nil || !acquired {
		return releaseFunc, acquired, err
	}
	return h.withAPIKeySlot(ctx, apiKeyID, releaseFunc), true, nil
}

// AcquireOpenAIWSIngressLease limits the whole client connection independently of per-turn user and provider slots.
func (h *ConcurrencyHelper) AcquireOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, maxConnections int) (*scheduler.OpenAIWSIngressLease, bool, error) {
	if h == nil || h.concurrencyService == nil {
		return nil, false, fmt.Errorf("concurrency service is unavailable")
	}
	return h.concurrencyService.AcquireOpenAIWSIngressLease(ctx, apiKeyID, maxConnections)
}

// TryAcquireProviderSlot attempts immediate acquisition of a provider slot.
// It returns the release function, whether a slot was acquired, and any store error.
func (h *ConcurrencyHelper) TryAcquireProviderSlot(ctx context.Context, providerID int64, maxConcurrency int) (func(), bool, error) {
	result, err := h.concurrencyService.AcquireProviderSlot(ctx, providerID, maxConcurrency)
	if err != nil {
		return nil, false, err
	}
	if !result.Acquired {
		return nil, false, nil
	}
	return result.ReleaseFunc, true, nil
}

// AcquireUserSlotWithWait waits for a user slot when immediate acquisition fails.
// Streaming waits emit configured pings and mark streamStarted when output begins.
func (h *ConcurrencyHelper) AcquireUserSlotWithWait(c *gin.Context, userID int64, maxConcurrency int, isStream bool, streamStarted *bool) (func(), error) {
	return h.AcquireUserSlotWithWaitTimeout(c, userID, maxConcurrency, maxConcurrencyWait, isStream, streamStarted)
}

func (h *ConcurrencyHelper) AcquireUserSlotWithWaitTimeout(c *gin.Context, userID int64, maxConcurrency int, timeout time.Duration, isStream bool, streamStarted *bool) (func(), error) {
	apiKeyID := h.keyID(c)
	lease, _, err := h.concurrencyService.AcquireUser(c.Request.Context(), scheduler.UserAcquireOptions{
		UserID: userID, APIKeyID: apiKeyID, Limit: maxConcurrency, Timeout: timeout,
		Mode: scheduler.ReleaseOnCompletion, Observer: WaitObserver(c, h.pingFormat, h.pingInterval, isStream, streamStarted, true),
	})
	if err != nil {
		return nil, err
	}
	c.Request = c.Request.WithContext(scheduler.WithRequestLease(c.Request.Context(), lease))
	return lease.Release, nil
}

func (h *ConcurrencyHelper) withAPIKeySlot(ctx context.Context, apiKeyID int64, releaseFunc func()) func() {
	if h == nil || h.concurrencyService == nil || apiKeyID <= 0 {
		return releaseFunc
	}
	apiKeyReleaseFunc := h.concurrencyService.TrackAPIKeySlot(ctx, apiKeyID)
	return func() {
		if releaseFunc != nil {
			releaseFunc()
		}
		if apiKeyReleaseFunc != nil {
			apiKeyReleaseFunc()
		}
	}
}

// AcquireProviderSlotWithWait waits for a provider slot when immediate acquisition fails.
// Streaming waits emit the configured ping format.
// streamStarted lets callers choose an in-stream error after a ping has committed the response.
func (h *ConcurrencyHelper) AcquireProviderSlotWithWait(c *gin.Context, providerID int64, maxConcurrency int, isStream bool, streamStarted *bool) (func(), error) {
	ctx := c.Request.Context()

	releaseFunc, acquired, err := h.TryAcquireProviderSlot(ctx, providerID, maxConcurrency)
	if err != nil {
		return nil, err
	}

	if acquired {
		return releaseFunc, nil
	}

	return h.waitForSlotWithPing(c, "provider", providerID, maxConcurrency, isStream, streamStarted)
}

// waitForSlotWithPing waits for a slot using the default concurrency timeout.
// The observer updates streamStarted when ping output begins.
func (h *ConcurrencyHelper) waitForSlotWithPing(c *gin.Context, slotType string, id int64, maxConcurrency int, isStream bool, streamStarted *bool) (func(), error) {
	return h.WaitForSlotWithPingTimeout(c, slotType, id, maxConcurrency, maxConcurrencyWait, isStream, streamStarted, false)
}

// WaitForSlotWithPingTimeout waits with a custom timeout and an optional immediate acquisition attempt.
func (h *ConcurrencyHelper) WaitForSlotWithPingTimeout(c *gin.Context, slotType string, id int64, maxConcurrency int, timeout time.Duration, isStream bool, streamStarted *bool, tryImmediate bool) (func(), error) {
	return h.concurrencyService.WaitForSlot(c.Request.Context(), slotType, id, maxConcurrency, timeout, tryImmediate,
		WaitObserver(c, h.pingFormat, h.pingInterval, isStream, streamStarted, true))
}

// AcquireProviderSlotWithWaitTimeout waits for a provider slot with a custom timeout and configured SSE pings.
func (h *ConcurrencyHelper) AcquireProviderSlotWithWaitTimeout(c *gin.Context, providerID int64, maxConcurrency int, timeout time.Duration, isStream bool, streamStarted *bool) (func(), error) {
	return h.WaitForSlotWithPingTimeout(c, "provider", providerID, maxConcurrency, timeout, isStream, streamStarted, true)
}

// WaitObserver emits HTTP keepalives and marks first output; strict mode rejects writers that cannot flush.
func WaitObserver(c *gin.Context, format SSEPingFormat, interval time.Duration, isStream bool, started *bool, strict bool) scheduler.WaitObserver {
	if !isStream || format == "" {
		return scheduler.WaitObserver{}
	}
	var flusher http.Flusher
	return scheduler.WaitObserver{Interval: interval, Begin: func() error {
		flusher, _ = c.Writer.(http.Flusher)
		if flusher == nil && strict {
			return fmt.Errorf("streaming not supported")
		}
		return nil
	}, Heartbeat: func() error {
		if flusher == nil {
			return nil
		}
		if !*started {
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
			c.Header("Connection", "keep-alive")
			c.Header("X-Accel-Buffering", "no")
			*started = true
		}
		written, err := fmt.Fprint(c.Writer, string(format))
		if err != nil {
			return err
		}
		RecordStreamHeartbeat(c, written)
		flusher.Flush()
		return nil
	}}
}

// Service returns the shared concurrency service used by compatible handlers.
func (h *ConcurrencyHelper) Service() *scheduler.ConcurrencyService {
	if h == nil {
		return nil
	}
	return h.concurrencyService
}
