package httpapi

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// serial_queue.go connects Anthropic serialization and RPM waits to HTTP
// heartbeats and request logging.
// Anthropic message serialization and soft RPM spacing use the scheduler's
// shared queue service. The HTTP adapter supplies synchronous request logging
// and wait heartbeats, so an acquired serial lease can be released at upstream
// acceptance while the queue owns admission and timing rules.

// UserMsgQueueHelper adapts the user's serial message queue to HTTP requests.
// It reports wait events and emits SSE pings through the shared wait observer.
type UserMsgQueueHelper struct {
	queueService *scheduler.UserMessageQueueService // shared serial queue service
	pingFormat   SSEPingFormat                      // configured SSE heartbeat representation
	pingInterval time.Duration                      // wait heartbeat time interval
}

// NewUserMsgQueueHelper binds the shared message queue and keepalive settings.
// Nonpositive intervals select DefaultPingInterval, while an empty ping format
// disables wait output; the queue service is required by acquisition and
// throttling methods.
func NewUserMsgQueueHelper(
	queueService *scheduler.UserMessageQueueService,
	pingFormat SSEPingFormat,
	pingInterval time.Duration,
) *UserMsgQueueHelper {
	if pingInterval <= 0 {
		pingInterval = DefaultPingInterval
	}
	return &UserMsgQueueHelper{
		queueService: queueService,
		pingFormat:   pingFormat,
		pingInterval: pingInterval,
	}
}

// AcquireWithWait obtains a serial message lease within the supplied timeout
// and request context. Streaming waits emit configured pings. Callers invoke
// the returned idempotent release after acceptance or cleanup so the shared
// queue can admit later work.
func (h *UserMsgQueueHelper) AcquireWithWait(c *gin.Context, providerID int64, baseRPM int, isStream bool, streamStarted *bool, timeout time.Duration, reqLog *zap.Logger) (func(), error) {
	lease, err := h.queueService.AcquireWithWait(c.Request.Context(), providerID, baseRPM, timeout, h.observer(c, providerID, isStream, streamStarted, reqLog))
	if err != nil {
		return nil, err
	}
	return lease.Release, nil
}

// observer binds synchronous wait logging and optional SSE output for this
// request. Callers provide the logger and stream state for enabled output;
// non-strict waits tolerate a writer with no flusher and leave heartbeat output
// disabled.
func (h *UserMsgQueueHelper) observer(c *gin.Context, providerID int64, isStream bool, started *bool, reqLog *zap.Logger) scheduler.QueueObserver {
	return scheduler.QueueObserver{
		Wait: WaitObserver(c, h.pingFormat, h.pingInterval, isStream, started, false),
		Event: func(name string, id int64, err error) {
			if err != nil {
				reqLog.Warn(name, zap.Int64("provider_id", id), zap.Error(err))
			} else {
				reqLog.Debug(name, zap.Int64("provider_id", id))
			}
		}, Delay: func(delay time.Duration) {
			reqLog.Debug("gateway.umq_throttle_delay", zap.Int64("provider_id", providerID), zap.Duration("delay", delay))
		},
	}
}

// ThrottleWithPing applies the shared queue's RPM-based delay within the
// request context and timeout. Streaming waits emit configured pings, while
// cancellation or observation failure returns the scheduler error for the
// caller to handle.
func (h *UserMsgQueueHelper) ThrottleWithPing(c *gin.Context, providerID int64, baseRPM int, isStream bool, streamStarted *bool, timeout time.Duration, reqLog *zap.Logger) error {
	return h.queueService.ThrottleWithWait(c.Request.Context(), providerID, baseRPM, timeout, h.observer(c, providerID, isStream, streamStarted, reqLog))
}
