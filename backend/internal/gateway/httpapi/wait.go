package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Waiting for user or provider capacity can commit SSE output before generation
// begins. HTTP observers account for the bytes written as heartbeats, allowing
// later error presentation to distinguish a body containing only recorded
// keepalive bytes from output that has additional content. The byte comparison
// uses the current writer size and the positive request counter.
//
// The scheduler owns queue admission, retry timing and acquired leases. HTTP
// adapters supply request contexts, Key-statistics tracking and synchronous
// heartbeats. Waiting cancellation stops admission, while accepted user work is
// published in request context with completion cleanup. Callers retain the
// release obligation and propagate stream commitment state to later protocol
// errors.

// These values bind keepalive byte accounting, default wait budgets and SSE
// heartbeat encodings used by synchronous HTTP wait observers.
const (
	gatewayStreamHeartbeatBytesKey = "gateway_stream_heartbeat_bytes" // request heartbeat byte count
	maxConcurrencyWait             = 30 * time.Second                 // default slot waiting budget
	DefaultPingInterval            = 10 * time.Second                 // default wait heartbeat interval

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

// WaitQueueFullError exposes scheduler queue denial to HTTP error matching.
type WaitQueueFullError = scheduler.WaitQueueFullError

// ConcurrencyHelper adapts concurrency slot management to gateway HTTP handlers.
type ConcurrencyHelper struct {
	concurrencyService *scheduler.ConcurrencyService // shared scheduler capacity service
	keyID              func(*gin.Context) int64      // request Key identity resolver
	pingFormat         SSEPingFormat                 // configured SSE heartbeat bytes
	pingInterval       time.Duration                 // wait heartbeat time interval
}

// RecordStreamHeartbeat adds a positive written byte count to request-local
// keepalive accounting. Nil contexts and nonpositive counts are ignored.
// Callers serialize recording around their output writes because the
// read-and-add operation is not an atomic counter update.
func RecordStreamHeartbeat(c *gin.Context, written int) {
	if c == nil || written <= 0 {
		return
	}
	total, _ := c.Get(gatewayStreamHeartbeatBytesKey)
	bytes, _ := total.(int)
	c.Set(gatewayStreamHeartbeatBytesKey, bytes+written)
}

// StreamHasOnlyHeartbeats reports whether positive recorded keepalive bytes
// account for the complete writer size. Missing contexts, writers or accounting
// return false, so an unrecorded output cannot qualify merely because its body
// resembles a heartbeat.
//
// The comparison combines the request counter with the writer's current byte
// count. Any extra byte makes it false; it classifies output by recorded size
// rather than parsing event contents. Callers coordinate recording with output
// before using the result to decide whether a terminal failure may be appended.
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

// NewConcurrencyHelper binds slot management and wait output to HTTP requests.
// Nonpositive intervals use DefaultPingInterval. A nonnil first supplied
// resolver replaces effective-Key lookup; later variadic entries are ignored
// even when the first is nil, and absent effective keys resolve to zero.
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

// EnterUserWait delegates user queue registration to the shared concurrency
// service. Call Release when waiting ends; WaitResult owns only confirmed
// counts, while uncertain store increments allow admission with no matching
// decrement.
func (h *ConcurrencyHelper) EnterUserWait(ctx context.Context, id int64, limit int) (scheduler.WaitResult, error) {
	return h.concurrencyService.EnterUserWait(ctx, id, limit)
}

// EnterProviderWait delegates provider queue registration and returns its owned
// wait result. Call Release when waiting ends; denied entries own no count, and
// uncertain increments can proceed without inventing a cleanup decrement.
func (h *ConcurrencyHelper) EnterProviderWait(ctx context.Context, id int64, limit int) (scheduler.WaitResult, error) {
	return h.concurrencyService.EnterProviderWait(ctx, id, limit)
}

// TryAcquireUserSlot attempts immediate user admission and returns its release
// callback when acquired. A busy slot yields nil release and false with no
// error; callers invoke accepted releases after work and handle store errors
// separately.
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

// TryAcquireUserSlotForAPIKey obtains user capacity before adding
// Key-statistics tracking. Failed or busy user admission returns directly.
// Successful callers release the combined callback after work, which ends user
// admission before the auxiliary Key count.
func (h *ConcurrencyHelper) TryAcquireUserSlotForAPIKey(ctx context.Context, userID int64, maxConcurrency int, apiKeyID int64) (func(), bool, error) {
	releaseFunc, acquired, err := h.TryAcquireUserSlot(ctx, userID, maxConcurrency)
	if err != nil || !acquired {
		return releaseFunc, acquired, err
	}
	return h.withAPIKeySlot(ctx, apiKeyID, releaseFunc), true, nil
}

// AcquireOpenAIWSIngressLease acquires a connection-wide lease independent of
// per-turn slots. An absent helper or service returns an availability error;
// successful callers manage renewal and release through the returned scheduler
// lease.
func (h *ConcurrencyHelper) AcquireOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, maxConnections int) (*scheduler.OpenAIWSIngressLease, bool, error) {
	if h == nil || h.concurrencyService == nil {
		return nil, false, fmt.Errorf("concurrency service is unavailable")
	}
	return h.concurrencyService.AcquireOpenAIWSIngressLease(ctx, apiKeyID, maxConnections)
}

// TryAcquireProviderSlot attempts immediate provider admission and returns its
// owned release on success. Busy capacity yields false with nil error and
// release; callers keep the callback until the provider attempt has finished.
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

// AcquireUserSlotWithWait waits for user capacity using the default
// thirty-second budget. It delegates Key-statistics tracking and request-lease
// publication; callers release accepted work at completion, while streaming
// waits can commit keepalive output.
func (h *ConcurrencyHelper) AcquireUserSlotWithWait(c *gin.Context, userID int64, maxConcurrency int, isStream bool, streamStarted *bool) (func(), error) {
	return h.AcquireUserSlotWithWaitTimeout(c, userID, maxConcurrency, maxConcurrencyWait, isStream, streamStarted)
}

// AcquireUserSlotWithWaitTimeout acquires a user/Key lease with request
// cancellation governing waiting. It binds the accepted lease into the request
// context using completion release mode; callers release it explicitly after
// work. Enabled heartbeat output updates the supplied stream state.
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

// withAPIKeySlot adds auxiliary Key-statistics cleanup to an acquired user
// release. Missing service or nonpositive Key IDs retain the original callback.
// The combined callback invokes user cleanup first; repeated-call protection
// depends on the callbacks it receives.
func (h *ConcurrencyHelper) withAPIKeySlot(ctx context.Context, apiKeyID int64, releaseFunc func()) func() {
	if h == nil || h.concurrencyService == nil || apiKeyID <= 0 {
		return releaseFunc
	}
	apiKeyReleaseFunc := h.concurrencyService.TrackAPIKeySlot(ctx, apiKeyID)
	// The Key count tracks admitted user work; finish user capacity cleanup before
	// removing that auxiliary observation from concurrency statistics.
	return func() {
		if releaseFunc != nil {
			releaseFunc()
		}
		if apiKeyReleaseFunc != nil {
			apiKeyReleaseFunc()
		}
	}
}

// AcquireProviderSlotWithWait first attempts provider admission, then delegates
// waiting with the default budget when busy. Accepted callers own the release
// callback; streaming heartbeats update streamStarted so later errors can use
// the committed response format.
func (h *ConcurrencyHelper) AcquireProviderSlotWithWait(c *gin.Context, providerID int64, maxConcurrency int, isStream bool, streamStarted *bool) (func(), error) {
	ctx := c.Request.Context()

	releaseFunc, acquired, err := h.TryAcquireProviderSlot(ctx, providerID, maxConcurrency)
	if err != nil {
		return nil, err
	}

	if acquired {
		return releaseFunc, nil
	}

	// Immediate provider admission found no capacity, so use the scheduler wait
	// budget while carrying any heartbeat commitment into subsequent error output.
	return h.waitForSlotWithPing(c, "provider", providerID, maxConcurrency, isStream, streamStarted)
}

// waitForSlotWithPing delegates waiting using the default thirty-second
// concurrency budget. It skips an immediate acquisition attempt because its
// caller has already tried, and the observer tracks keepalive output during the
// remaining wait.
func (h *ConcurrencyHelper) waitForSlotWithPing(c *gin.Context, slotType string, id int64, maxConcurrency int, isStream bool, streamStarted *bool) (func(), error) {
	return h.WaitForSlotWithPingTimeout(c, slotType, id, maxConcurrency, maxConcurrencyWait, isStream, streamStarted, false)
}

// WaitForSlotWithPingTimeout delegates bounded slot waiting and optional
// immediate admission to the scheduler. Slot type user selects user capacity;
// other values use provider capacity. Accepted callers release ownership, and
// strict enabled streaming waits reject writers without Flush.
func (h *ConcurrencyHelper) WaitForSlotWithPingTimeout(c *gin.Context, slotType string, id int64, maxConcurrency int, timeout time.Duration, isStream bool, streamStarted *bool, tryImmediate bool) (func(), error) {
	return h.concurrencyService.WaitForSlot(c.Request.Context(), slotType, id, maxConcurrency, timeout, tryImmediate,
		WaitObserver(c, h.pingFormat, h.pingInterval, isStream, streamStarted, true))
}

// AcquireProviderSlotWithWaitTimeout waits for provider capacity with a custom
// budget and an immediate first attempt. It returns the acquired release
// callback; enabled streaming observation updates commitment state and can fail
// if flushing is unavailable.
func (h *ConcurrencyHelper) AcquireProviderSlotWithWaitTimeout(c *gin.Context, providerID int64, maxConcurrency int, timeout time.Duration, isStream bool, streamStarted *bool) (func(), error) {
	return h.WaitForSlotWithPingTimeout(c, "provider", providerID, maxConcurrency, timeout, isStream, streamStarted, true)
}

// WaitObserver constructs synchronous scheduler callbacks for streaming wait
// heartbeats. Nonstreaming requests or empty formats yield no callbacks; strict
// mode rejects a writer without Flush during Begin.
//
// Heartbeat uses the flusher captured by Begin and requires a nonnil started
// pointer when output is enabled. It sets SSE headers and the started flag
// before writing the first ping, records successful byte counts, then flushes.
// A write error returns before byte accounting, leaving the started flag set
// for error handling.
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
		// Non-strict callers can wait using writers without Flush; skip heartbeat
		// output because their transport cannot perform the synchronous keepalive
		// delivery.
		if flusher == nil {
			return nil
		}
		// Mark the stream before its first heartbeat write, so even a write error
		// leaves later error output aware of attempted SSE commitment.
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
		// Record successful keepalive bytes before flushing so the later size-based
		// failure check can identify output containing those pings alone.
		RecordStreamHeartbeat(c, written)
		flusher.Flush()
		return nil
	}}
}

// Service returns the shared concurrency service bound to this helper. A nil
// helper returns nil so assembly consumers can inspect an optional binding
// before using the service directly.
func (h *ConcurrencyHelper) Service() *scheduler.ConcurrencyService {
	if h == nil {
		return nil
	}
	return h.concurrencyService
}
