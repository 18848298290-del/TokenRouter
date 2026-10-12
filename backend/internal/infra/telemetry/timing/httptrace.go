package timing

import (
	"context"
	"net/http/httptrace"
	"sync"
	"time"
)

// HTTPTraceSnapshot holds absolute timestamps for the first observed transport stages
// and event counts across attempts. StartedAt is the time trace collection was installed.
type HTTPTraceSnapshot struct {
	StartedAt                 time.Time
	GetConnAt                 time.Time
	GotConnAt                 time.Time
	WroteRequestAt            time.Time
	GotFirstResponseByteAt    time.Time
	GetConnCount              int
	GotConnCount              int
	WroteRequestCount         int
	FirstResponseByteCount    int
	ConnectionReused          bool
	WroteRequestErrorObserved bool
}

type httpTraceContextKey struct{}

type httpTraceState struct {
	mu                        sync.Mutex
	startedAt                 time.Time
	getConnAt                 time.Time
	gotConnAt                 time.Time
	wroteRequestAt            time.Time
	gotFirstResponseByteAt    time.Time
	getConnCount              int
	gotConnCount              int
	wroteRequestCount         int
	firstResponseByteCount    int
	connectionReused          bool
	wroteRequestErrorObserved bool
	upstreamStarted           bool
}

// WithHTTPTrace installs net/http stage callbacks, reusing state already in context.
func WithHTTPTrace(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(httpTraceContextKey{}).(*httpTraceState); ok {
		return ctx
	}
	state := &httpTraceState{startedAt: time.Now()}
	ctx = context.WithValue(ctx, httpTraceContextKey{}, state)
	return withHTTPTraceClient(ctx, state)
}

// BeginHTTPTrace clears previously recorded events on the first upstream attempt.
// Later calls retain the same timestamps and counters to aggregate retries.
func BeginHTTPTrace(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	state, ok := ctx.Value(httpTraceContextKey{}).(*httpTraceState)
	if !ok || state == nil {
		ctx = WithHTTPTrace(ctx)
		state, _ = ctx.Value(httpTraceContextKey{}).(*httpTraceState)
	}
	if state == nil {
		return ctx
	}
	state.mu.Lock()
	if !state.upstreamStarted {
		state.getConnAt = time.Time{}
		state.gotConnAt = time.Time{}
		state.wroteRequestAt = time.Time{}
		state.gotFirstResponseByteAt = time.Time{}
		state.getConnCount = 0
		state.gotConnCount = 0
		state.wroteRequestCount = 0
		state.firstResponseByteCount = 0
		state.connectionReused = false
		state.wroteRequestErrorObserved = false
		state.upstreamStarted = true
	}
	state.mu.Unlock()
	return ctx
}

func withHTTPTraceClient(ctx context.Context, state *httpTraceState) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn: func(string) {
			state.markGetConn(time.Now())
		},
		GotConn: func(info httptrace.GotConnInfo) {
			state.markGotConn(time.Now(), info.Reused)
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			state.markWroteRequest(time.Now(), info.Err != nil)
		},
		GotFirstResponseByte: func() {
			state.markFirstResponseByte(time.Now())
		},
	})
}

func (s *httpTraceState) markGetConn(at time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.getConnAt.IsZero() {
		s.getConnAt = at
	}
	s.getConnCount++
	s.mu.Unlock()
}

func (s *httpTraceState) markGotConn(at time.Time, reused bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.gotConnAt.IsZero() {
		s.gotConnAt = at
	}
	s.gotConnCount++
	s.connectionReused = s.connectionReused || reused
	s.mu.Unlock()
}

func (s *httpTraceState) markWroteRequest(at time.Time, hasError bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.wroteRequestAt.IsZero() {
		s.wroteRequestAt = at
	}
	s.wroteRequestCount++
	s.wroteRequestErrorObserved = s.wroteRequestErrorObserved || hasError
	s.mu.Unlock()
}

func (s *httpTraceState) markFirstResponseByte(at time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.gotFirstResponseByteAt.IsZero() {
		s.gotFirstResponseByteAt = at
	}
	s.firstResponseByteCount++
	s.mu.Unlock()
}

// HTTPTraceSnapshotFromContext returns transport stages and reports whether any were observed.
func HTTPTraceSnapshotFromContext(ctx context.Context) (HTTPTraceSnapshot, bool) {
	if ctx == nil {
		return HTTPTraceSnapshot{}, false
	}
	state, ok := ctx.Value(httpTraceContextKey{}).(*httpTraceState)
	if !ok || state == nil {
		return HTTPTraceSnapshot{}, false
	}
	state.mu.Lock()
	snapshot := HTTPTraceSnapshot{
		StartedAt:                 state.startedAt,
		GetConnAt:                 state.getConnAt,
		GotConnAt:                 state.gotConnAt,
		WroteRequestAt:            state.wroteRequestAt,
		GotFirstResponseByteAt:    state.gotFirstResponseByteAt,
		GetConnCount:              state.getConnCount,
		GotConnCount:              state.gotConnCount,
		WroteRequestCount:         state.wroteRequestCount,
		FirstResponseByteCount:    state.firstResponseByteCount,
		ConnectionReused:          state.connectionReused,
		WroteRequestErrorObserved: state.wroteRequestErrorObserved,
	}
	state.mu.Unlock()
	if snapshot.GetConnAt.IsZero() && snapshot.GotConnAt.IsZero() &&
		snapshot.WroteRequestAt.IsZero() && snapshot.GotFirstResponseByteAt.IsZero() &&
		snapshot.GotConnCount == 0 && snapshot.WroteRequestCount == 0 {
		return snapshot, false
	}
	return snapshot, true
}

// HTTPTraceElapsedMs converts an absolute stage timestamp to milliseconds since trace installation.
func HTTPTraceElapsedMs(snapshot HTTPTraceSnapshot, at time.Time) (int64, bool) {
	if snapshot.StartedAt.IsZero() || at.IsZero() || at.Before(snapshot.StartedAt) {
		return 0, false
	}
	return at.Sub(snapshot.StartedAt).Milliseconds(), true
}
