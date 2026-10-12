package timing

import (
	"context"
	"net/http/httptrace"
	"sync"
	"time"
)

// One request can perform preflight HTTP work before the first upstream
// attempt, then retry across several connections. Trace state keeps the
// first retained timestamp for each transport stage and cumulative event counts,
// while connection reuse and write errors stay true after any attempt
// observes them. StartedAt remains the trace-installation time for
// elapsed-stage reporting.
//
// The first BeginHTTPTrace clears stage observations collected during
// preflight and marks upstream collection as begun. Later calls retain retry
// observations. One mutex coordinates this reset, stage callbacks, and
// snapshot copying so each snapshot contains one consistent state; callbacks
// already installed through httptrace remain attached to the shared context
// state.

// HTTPTraceSnapshot holds absolute timestamps for the first observed transport stages
// and event counts across attempts. StartedAt is the time trace collection was installed.
type HTTPTraceSnapshot struct {
	// StartedAt remains the installation reference even when BeginHTTPTrace clears
	// earlier stage observations before the first upstream attempt begins.
	StartedAt time.Time // trace installation time
	// GetConnAt, GotConnAt, WroteRequestAt, GotFirstResponseByteAt
	// retain the first respective stage timestamps. BeginHTTPTrace clears
	// preflight observations once; zero marks a hook without retained observations.
	GetConnAt              time.Time // first connection request time
	GotConnAt              time.Time // first acquired connection time
	WroteRequestAt         time.Time // first request write time
	GotFirstResponseByteAt time.Time // first response byte time
	// GetConnCount, GotConnCount, WroteRequestCount, FirstResponseByteCount
	// accumulate retained events across attempts while their matching timestamps
	// keep the first observation, distinguishing retry activity from elapsed stages.
	GetConnCount           int // connection request observation count
	GotConnCount           int // acquired connection observation count
	WroteRequestCount      int // request write observation count
	FirstResponseByteCount int // response byte observation count
	// ConnectionReused stays true after any retained GotConn hook reports reuse;
	// later fresh connections do not erase that request-wide observation.
	ConnectionReused bool // any reused connection observed
	// WroteRequestErrorObserved stays true after any retained WroteRequest hook
	// reports an error, including when a later attempt writes successfully.
	WroteRequestErrorObserved bool // any request write error observed
}

// httpTraceContextKey identifies shared HTTP-stage state in a request context.
type httpTraceContextKey struct{}

// httpTraceState synchronizes first-stage observations and cumulative flags across attempts.
type httpTraceState struct {
	// mu protects all timestamps, counters, flags, and the upstreamStarted
	// transition; callbacks and snapshots observe the reset through this same lock.
	mu sync.Mutex // trace observation mutex
	// startedAt remains the installation reference even when BeginHTTPTrace clears
	// earlier stage observations before the first upstream attempt begins.
	startedAt time.Time // trace installation time
	// getConnAt, gotConnAt, wroteRequestAt, gotFirstResponseByteAt
	// retain the first respective stage timestamps. BeginHTTPTrace clears
	// preflight observations once; zero marks a hook without retained observations.
	getConnAt              time.Time // first connection request time
	gotConnAt              time.Time // first acquired connection time
	wroteRequestAt         time.Time // first request write time
	gotFirstResponseByteAt time.Time // first response byte time
	// getConnCount, gotConnCount, wroteRequestCount, firstResponseByteCount
	// accumulate retained events across attempts while their matching timestamps
	// keep the first observation, distinguishing retry activity from elapsed stages.
	getConnCount           int // connection request observation count
	gotConnCount           int // acquired connection observation count
	wroteRequestCount      int // request write observation count
	firstResponseByteCount int // response byte observation count
	// connectionReused stays true after any retained GotConn hook reports reuse;
	// later fresh connections do not erase that request-wide observation.
	connectionReused bool // any reused connection observed
	// wroteRequestErrorObserved stays true after any retained WroteRequest hook
	// reports an error, including when a later attempt writes successfully.
	wroteRequestErrorObserved bool // any request write error observed
	upstreamStarted           bool // first upstream reset completed
}

// WithHTTPTrace attaches shared transport-stage state and net/http callbacks
// to ctx, using background for nil. A context already containing the state
// pointer is returned unchanged, letting detached or repeated request paths
// reuse collection.
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

// BeginHTTPTrace starts upstream-stage collection, installing trace state
// when ctx has no trace-state value. Its first call clears preflight
// timestamps, counts, and flags under the state mutex and marks collection
// begun; a stored nil state remains unchanged.
//
// Later calls retain the first stored upstream timestamps and accumulated retry
// observations. StartedAt stays at trace installation, so elapsed
// milliseconds keep that reference time. Stage callbacks and snapshots use
// the same mutex; callers begin collection before the first upstream attempt
// whose events should be retained.
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
	// Reset preflight once while excluding callbacks and snapshots. Keeping
	// the installation time lets retained upstream stages use the same elapsed reference.
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

// withHTTPTraceClient installs stage callbacks referencing state through
// httptrace.WithClientTrace. The library composes existing callbacks; the
// returned context shares state and records callback times without recording
// host strings or request data.
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

// markGetConn retains the first connection-request timestamp and increments
// its observation count under the state mutex. Nil state ignores the event;
// later attempts retain that first timestamp while adding counts.
func (s *httpTraceState) markGetConn(at time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	// Retries can request another connection. Keep the first stored stage marker
	// for elapsed reporting while the separate count exposes repeated connection work.
	if s.getConnAt.IsZero() {
		s.getConnAt = at
	}
	s.getConnCount++
	s.mu.Unlock()
}

// markGotConn retains the first acquired-connection timestamp and increments
// its count under the mutex. Nil state ignores the event; any observed reused
// connection makes the accumulated reuse flag true for snapshots.
func (s *httpTraceState) markGotConn(at time.Time, reused bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	// Later connections can be fresh or reused. Keep the first stage marker and
	// accumulate reuse so a reconnect cannot erase evidence of an earlier reused connection.
	if s.gotConnAt.IsZero() {
		s.gotConnAt = at
	}
	s.gotConnCount++
	s.connectionReused = s.connectionReused || reused
	s.mu.Unlock()
}

// markWroteRequest retains the first write timestamp and increments its
// observation count under the mutex. Nil state ignores the event; any failed
// write leaves the accumulated error flag true across later attempts.
func (s *httpTraceState) markWroteRequest(at time.Time, hasError bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	// A successful retry still follows any earlier failed write. Keep the first
	// write marker and cumulative error evidence so the snapshot exposes
	// attempt history alongside elapsed progress.
	if s.wroteRequestAt.IsZero() {
		s.wroteRequestAt = at
	}
	s.wroteRequestCount++
	s.wroteRequestErrorObserved = s.wroteRequestErrorObserved || hasError
	s.mu.Unlock()
}

// markFirstResponseByte retains the first response-byte timestamp and
// increments its count under the state mutex. Nil state ignores the event;
// later attempts add observations while keeping the first timestamp available
// for reporting.
func (s *httpTraceState) markFirstResponseByte(at time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	// Another attempt may deliver its own first byte. Retain the first stored
	// response marker for elapsed reporting while counting each retained hook invocation.
	if s.gotFirstResponseByteAt.IsZero() {
		s.gotFirstResponseByteAt = at
	}
	s.firstResponseByteCount++
	s.mu.Unlock()
}

// HTTPTraceSnapshotFromContext copies trace timestamps, counts, and flags
// under the shared mutex. Missing state returns an empty false result;
// installed state can return StartedAt with false until a transport-stage
// observation satisfies the presence check.
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

// HTTPTraceElapsedMs reports whole milliseconds from snapshot.StartedAt to
// at. Missing timestamps or a stage before installation return false; a stage
// at installation is valid with zero elapsed milliseconds.
func HTTPTraceElapsedMs(snapshot HTTPTraceSnapshot, at time.Time) (int64, bool) {
	if snapshot.StartedAt.IsZero() || at.IsZero() || at.Before(snapshot.StartedAt) {
		return 0, false
	}
	// Whole milliseconds match the access-log stage fields and truncate fractional milliseconds.
	return at.Sub(snapshot.StartedAt).Milliseconds(), true
}
