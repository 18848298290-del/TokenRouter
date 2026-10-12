package httpapi

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// UserMsgQueueHelper adapts the user's serial message queue to HTTP requests.
// It reports wait events and emits SSE pings through the shared wait observer.
type UserMsgQueueHelper struct {
	queueService *scheduler.UserMessageQueueService
	pingFormat   SSEPingFormat
	pingInterval time.Duration
}

// NewUserMsgQueueHelper creates a queue adapter with a default ping interval when the supplied interval is nonpositive.
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

// AcquireWithWait waits for the serial lease and emits configured SSE pings for streaming requests.
// The returned release function is idempotent.
func (h *UserMsgQueueHelper) AcquireWithWait(c *gin.Context, providerID int64, baseRPM int, isStream bool, streamStarted *bool, timeout time.Duration, reqLog *zap.Logger) (func(), error) {
	lease, err := h.queueService.AcquireWithWait(c.Request.Context(), providerID, baseRPM, timeout, h.observer(c, providerID, isStream, streamStarted, reqLog))
	if err != nil {
		return nil, err
	}
	return lease.Release, nil
}

// observer binds request logging and SSE output to scheduler-owned waits and leases.
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

// ThrottleWithPing waits for soft rate limiting while emitting configured SSE pings.
func (h *UserMsgQueueHelper) ThrottleWithPing(c *gin.Context, providerID int64, baseRPM int, isStream bool, streamStarted *bool, timeout time.Duration, reqLog *zap.Logger) error {
	return h.queueService.ThrottleWithWait(c.Request.Context(), providerID, baseRPM, timeout, h.observer(c, providerID, isStream, streamStarted, reqLog))
}
