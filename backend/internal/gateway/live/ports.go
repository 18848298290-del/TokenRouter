package live

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Live session control uses synchronous frame I/O while session records and
// concurrency leases can outlast the creation HTTP request. The service
// obtains shared adapters on demand and keeps controller coordination separate
// from HTTP authentication, request parsing, and protocol response formatting.

// TextFrame identifies text frames; the HTTP adapter maps it to the WebSocket
// library's type.
const TextFrame = 1

// FrameConn provides synchronous frame reads and writes for the control connection.
type FrameConn interface {
	// ReadFrame reads one control frame using the supplied cancellation context.
	// Returned bytes belong to that completed read; the Live runtime forwards or
	// rewrites them before issuing another read on that direction.
	ReadFrame(context.Context) (int, []byte, error)
	// WriteFrame synchronously sends one control frame under the supplied
	// context. Controller expiry can issue session.close while forwarding is
	// active, so connection adapters support those concurrent write callers.
	WriteFrame(context.Context, int, []byte) error
	// SetReadLimit applies the runtime's byte limit where the adapter owns client
	// reads. The native upstream wrapper accepts the method without changing its
	// transport settings; the sideband proxy calls it on downstream.
	SetReadLimit(int64)
	// Close releases the underlying control connection after its runtime
	// finishes. A successful upstream Dial transfers this cleanup to the runtime
	// caller; the accepted downstream connection remains owned by its HTTP entry.
	Close() error
}

// Target connects to a validated provider and rewrites routed model data.
// Provider credentials remain in the adapter, outside session records and
// public output.
type Target interface {
	// Dial opens the validated provider's control connection under the supplied
	// context. A successful caller owns Close after controller use, while dialing
	// errors return to observer or proxy recovery.
	Dial(context.Context) (FrameConn, error)
	// Rewrite returns an upstream client frame with its original client model and
	// internal model stages. Errors stop that forwarding direction; the proxy
	// uses nonempty client identity to replace its restoration state.
	Rewrite(context.Context, []byte) ([]byte, string, []string, error)
}

// Ports supplies session storage, scheduling leases, and lifecycle operations.
type Ports interface {
	// Store returns a usable shared Live call store or an availability error. The
	// service retains that adapter while it manages controller claims, closure
	// markers, and call-record lookup.
	Store() (session.LiveCallStore, error)
	// Leases returns the shared Live concurrency cache or an availability error.
	// Acquisition, renewal, and release use this adapter to account for a call
	// beyond its creation request.
	Leases() (scheduler.LiveConcurrencyCache, error)
	// Target validates the provider recorded for this call and returns its
	// control operations. Lookup or capability errors stop setup before dialing;
	// callers retain the record for the target's subsequent rewriting.
	Target(context.Context, *session.LiveCallRecord) (Target, error)
	// BeginObserver registers a unique active owner and returns a detached
	// context with cleanup. A false result rejects the task; accepted callers
	// invoke cleanup exactly once after their observer finishes.
	BeginObserver(string) (context.Context, func(), bool)
	// RecordZeroUsage records zero-cost Live observations with the supplied
	// elapsed duration in milliseconds. Finalize invokes it after winning closure
	// and attempting lease release; delivery behavior belongs to the recording
	// adapter.
	RecordZeroUsage(context.Context, *session.LiveCallRecord, int)
}

// Service manages Live control ownership, lease renewal, expiry, and observers.
// The application supplies shared session and observer state; construction
// starts no tasks.
type Service struct {
	ports         Ports         // shared Live runtime operation adapters
	retryInterval time.Duration // observer store-read retry wait duration
	readLimit     int64         // inbound sideband frame limit bytes
}

// New stores shared adapters, the observer store-read retry interval, and
// inbound sideband byte limit as supplied. Construction starts no tasks; fixed
// controller polling and observer reconnect waits use their own durations.
func New(ports Ports, retryInterval time.Duration, readLimit int64) *Service {
	return &Service{ports: ports, retryInterval: retryInterval, readLimit: readLimit}
}

// dial obtains a validated target and opens its control connection for this
// record. Target errors propagate before dialing; a successful caller owns
// connection closure after observing the session.
func (s *Service) dial(ctx context.Context, record *session.LiveCallRecord) (FrameConn, error) {
	target, err := s.ports.Target(ctx, record)
	if err != nil {
		return nil, err
	}
	return target.Dial(ctx)
}
