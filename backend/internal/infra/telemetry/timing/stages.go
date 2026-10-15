package timing

import (
	"context"
	"io"
	"maps"
	"net/http"
	"sync"
	"time"
)

// stages.go collects HTTP text admission intervals and stream milestones for
// one logical request. Body reads are observed on the original reader, while
// memory replays retain the context without inheriting that reader. Admission
// freezes the initial breakdown; later provider waits belong to retries rather
// than changing the explanation of the first accepted provider.
//
// A mutex coordinates detached upstream work with request completion and
// snapshots. Each milestone keeps its first observation, and completion freezes
// unfinished admission without inventing a successful slot acquisition. The
// collector stores durations rather than payloads. Optional response callbacks
// let gateway adapters observe platform framing without placing protocol or
// HTTP framework dependencies in this shared timing package.

// stageKey isolates the request collector from other timing context values.
type stageKey struct{}

// Stages owns admission counters and first-event observations for one HTTP request.
type Stages struct {
	// mu protects end, admitted, body, user, provider, retry and events. start
	// remains immutable; end freezes initial durations, while admitted decides
	// whether later provider waits belong to retries or a completed failure.
	mu       sync.Mutex       // protects counters and milestones
	start    time.Time        // monotonic ingress reference
	end      time.Time        // frozen admission endpoint
	admitted bool             // successful admission observed
	body     time.Duration    // original reader duration
	user     time.Duration    // initial user queue duration
	provider time.Duration    // initial provider queue duration
	retry    time.Duration    // subsequent provider queue duration
	stream   bool             // client streaming applicability
	events   map[string]int64 // first observed offsets
	// response observes each opened upstream response without consuming it. Set
	// before sharing the context; readers invoke it synchronously on their exchange.
	response func(*http.Response)
}

// stageBody measures original reads without buffering or retaining request bytes.
type stageBody struct {
	io.ReadCloser         // original caller-owned body
	stages        *Stages // shared request collector
}

// WithStages installs one request collector and optional response observer.
// An existing collector is reused. Callers install observation before sharing
// the context and retain the original request start for all subsequent offsets.
// A nonnil context and nonzero start are required before upstream execution.
//
// Gateway correlation can install HTTP tracing before this collector, while
// direct execution can install it afterward. Both orders use the supplied
// ingress time. An existing trace adopts that reference only before its first
// upstream exchange, so installing observation late cannot move timestamps
// belonging to an exchange already in progress.
func WithStages(ctx context.Context, start time.Time, response func(*http.Response)) (context.Context, *Stages) {
	if s := StagesFromContext(ctx); s != nil {
		return ctx, s
	}
	s := &Stages{start: start, events: make(map[string]int64), response: response}
	// Correlation installs tracing before authentication. Share its reference
	// with admission before any exchange, retaining existing upstream observations.
	if trace, ok := ctx.Value(httpTraceContextKey{}).(*httpTraceState); ok && trace != nil {
		trace.mu.Lock()
		if !trace.upstreamStarted {
			trace.startedAt = start
		}
		trace.mu.Unlock()
	}
	return context.WithValue(ctx, stageKey{}, s), s
}

// StagesFromContext returns the optional HTTP text collector from context.
func StagesFromContext(ctx context.Context) *Stages {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(stageKey{}).(*Stages)
	return s
}

// ObserveResponse invokes the installed protocol observer for an upstream response.
// It performs no reads itself; absent collectors and failed exchanges require
// no observation and leave response ownership with the original caller.
func ObserveResponse(ctx context.Context, response *http.Response) {
	if s := StagesFromContext(ctx); s != nil && s.response != nil && response != nil {
		s.response(response)
	}
}

// SetStream records client streaming applicability without changing transport behavior.
func (s *Stages) SetStream(stream bool) { s.mu.Lock(); defer s.mu.Unlock(); s.stream = stream }

// Streaming reports whether the client requested streaming HTTP text output.
func (s *Stages) Streaming() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream
}

// ReadBody wraps the original request reader while preserving its Close ownership.
// Install this once before authentication can consume the body. Replaced readers
// intentionally remain unwrapped so decoding and memory replay count as processing.
func (s *Stages) ReadBody(body io.ReadCloser) io.ReadCloser {
	if body == nil {
		return nil
	}
	return &stageBody{ReadCloser: body, stages: s}
}

// Read forwards bytes and errors unchanged and records elapsed reader work.
// Failed and partial reads still consume time. Admission clipping excludes
// any late body activity from an already frozen initial breakdown.
//
// The underlying read runs outside the collector lock so cancellation and
// snapshot publication do not wait behind network input. The completed interval
// is clipped under that lock against admission or request completion; only its
// elapsed portion before the endpoint contributes to the original body duration.
func (r *stageBody) Read(p []byte) (int, error) {
	start := time.Now()
	n, err := r.ReadCloser.Read(p)
	end := time.Now()
	r.stages.mu.Lock()
	if !r.stages.end.IsZero() && end.After(r.stages.end) {
		end = r.stages.end
	}
	if end.After(start) {
		r.stages.body += end.Sub(start)
	}
	r.stages.mu.Unlock()
	return n, err
}

// Wait adds an actual scheduler queue interval to its admission phase.
// Successful initial admission freezes the split, so subsequent provider waits
// accumulate separately. Callers report each completed wait exactly once.
//
// The admission decision and counter update share one lock. A provider wait
// reported after successful admission contributes to retry time, while a wait
// reported after failure-only completion is ignored. User admission remains a
// request-level resource, so no subsequent user wait can extend a frozen
// initial breakdown or borrow time from upstream execution.
func (s *Stages) Wait(kind string, elapsed time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind == "provider" && s.admitted {
		s.retry += elapsed
		return
	}
	if !s.end.IsZero() {
		return
	}
	if kind == "user" {
		s.user += elapsed
	} else {
		s.provider += elapsed
	}
}

// Admit freezes the initial breakdown at the first accepted provider.
// Repeated calls retain the original endpoint; failure-only completion must
// never become a successful admission because detached work arrives later.
func (s *Stages) Admit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.end.IsZero() {
		s.end = time.Now()
		s.admitted = true
		s.events["provider_slot_acquired_ms"] = s.end.Sub(s.start).Milliseconds()
	}
}

// Mark retains the first request-relative observation for a stream event.
// Callers provide a recognized metric name only after their event condition
// holds. Repeated events across attempts cannot replace the first observation.
func (s *Stages) Mark(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.events[name]; !ok {
		s.events[name] = time.Since(s.start).Milliseconds()
	}
}

// Finish closes an unsuccessful admission interval at request completion.
// Successful admission retains its earlier endpoint. Repeated completion is
// harmless and does not create a provider-acquired milestone on failures.
func (s *Stages) Finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.end.IsZero() {
		s.end = time.Now()
	}
}

// Snapshot copies admission durations and event offsets for persistence.
// Whole-millisecond remainder belongs to processing so the four initial
// durations add to their endpoint. Timings never contain payloads or credentials.
//
// The snapshot owns its map independently of the collector. Missing stream
// events stay absent, while known queue counters retain numeric zero. Admission
// and failure completion use their frozen endpoint; an in-progress snapshot
// uses the current instant without freezing further observation. Each component
// is clipped to the remaining interval before calculating processing time.
func (s *Stages) Snapshot() map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	end := s.end
	if end.IsZero() {
		end = time.Now()
	}
	total := end.Sub(s.start).Milliseconds()
	body := min(s.body.Milliseconds(), total)
	user := min(s.user.Milliseconds(), total-body)
	provider := min(s.provider.Milliseconds(), total-body-user)
	out := map[string]int64{
		"timing_stream":   0,
		"timing_admitted": 0,

		"request_body_read_ms":        body,
		"preprocess_ms":               total - body - user - provider,
		"user_slot_wait_ms":           user,
		"provider_slot_wait_ms":       provider,
		"retry_provider_slot_wait_ms": s.retry.Milliseconds(),
	}
	if s.stream {
		out["timing_stream"] = 1
	}
	if s.admitted {
		out["timing_admitted"] = 1
	}
	maps.Copy(out, s.events)
	return out
}
