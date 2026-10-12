package live

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// TextFrame identifies text frames; the HTTP adapter maps it to the WebSocket library's type.
const TextFrame = 1

// FrameConn provides synchronous frame reads and writes for the control connection.
type FrameConn interface {
	ReadFrame(context.Context) (int, []byte, error)
	WriteFrame(context.Context, int, []byte) error
	SetReadLimit(int64)
	Close() error
}

// Target connects to a validated provider and rewrites routed model data.
// Provider credentials remain in the adapter, outside session records and public output.
type Target interface {
	Dial(context.Context) (FrameConn, error)
	Rewrite(context.Context, []byte) ([]byte, string, []string, error)
}

// Ports supplies session storage, scheduling leases, and lifecycle operations.
type Ports interface {
	Store() (session.LiveCallStore, error)
	Leases() (scheduler.LiveConcurrencyCache, error)
	Target(context.Context, *session.LiveCallRecord) (Target, error)
	BeginObserver(string) (context.Context, func(), bool)
	RecordZeroUsage(context.Context, *session.LiveCallRecord, int)
}

// Service manages Live control ownership, lease renewal, expiry, and observers.
// The application supplies shared session and observer state; construction starts no tasks.
type Service struct {
	ports         Ports
	retryInterval time.Duration
	readLimit     int64
}

// New configures the service's observer retry interval and frame size limit.
func New(ports Ports, retryInterval time.Duration, readLimit int64) *Service {
	return &Service{ports: ports, retryInterval: retryInterval, readLimit: readLimit}
}

func (s *Service) dial(ctx context.Context, record *session.LiveCallRecord) (FrameConn, error) {
	target, err := s.ports.Target(ctx, record)
	if err != nil {
		return nil, err
	}
	return target.Dial(ctx)
}
