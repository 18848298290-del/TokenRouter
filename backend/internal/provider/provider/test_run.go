package provider

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// test_run.go coordinates probe output, cancellation after sink failures, and
// shared execution flags.
// Provider probes emit events while upstream execution can still be in
// progress. The first sink failure is retained and cancels the run, so
// execution can stop even when an output call does not directly return its
// error to the platform routine.
//
// One mutex serializes sink calls, the retained failure, and published route
// and completion flags. Result combines a platform error with the sink failure
// unless that platform error already contains it. Request headers and
// synchronous attempt flags remain owned by the execution flow, outside that
// locking rule. Callers cancel the derived context when the run finishes, and
// sinks avoid reentering methods that acquire the same mutex during a write.

// TestRun retains cancellation, event output, and per-probe request and
// execution state.
type TestRun struct {
	ProtocolRoute provider.TestProtocolRoute // selected group probe route
	// GroupID selects model attributes for this probe. Zero uses catalogue defaults;
	// the synchronous executor reads this value after its target initializes it.
	GroupID int64
	// Context and Cancel refer to the derived probe lifetime. A first output
	// failure invokes Cancel, and the target also cancels after finishing to
	// release context resources.
	Context context.Context    // cancellable probe execution context
	Cancel  context.CancelFunc // probe lifetime cancellation function
	// Headers retains the supplied mutable header map. The synchronous execution
	// flow owns header reads and writes; the run mutex does not protect them.
	Headers http.Header
	// RequestedProtocol selects the OpenAI API-key text route when nonempty.
	// Otherwise Execute uses the record's resolved text protocol.
	RequestedProtocol provider.TextProtocol
	// TaskRecoveryTried records a task replay in this synchronous run. Recovery
	// paths consult it to allow one retry and suppress another test_start event.
	TaskRecoveryTried bool
	// Automatic enables automatic preparation and routing rules. The execution
	// flow assigns it before policy callbacks inspect this run.
	Automatic bool
	// sink receives Begin and Emit while mu is held. Configure a sink that does
	// not reenter the run's locked methods from a write.
	sink provider.TestEventSink
	// mu protects err, automaticRoute, hasAutomaticRoute, and suppressCompletion
	// while serializing output. Request headers and synchronous recovery fields
	// belong to the caller's execution flow.
	mu sync.Mutex // output and publication lock
	// err retains the first sink failure. It cancels Context and prevents later
	// output callbacks from reaching the sink.
	err error
	// automaticRoute and hasAutomaticRoute are published and read under mu. The
	// availability flag distinguishes completed preparation from a zero or
	// unmatched routing result.
	automaticRoute    egress.TLSFingerprintRouterMatchResult // prepared automatic router result
	hasAutomaticRoute bool                                   // automatic route preparation completed
	// suppressCompletion hides test_complete events while adaptive endpoints are
	// being checked. Content and error output remain eligible during that phase.
	suppressCompletion bool
}

// NewTestRun derives a cancellable probe context and retains headers and sink.
// Nil headers create a new map; supplied headers remain shared with the caller,
// who cancels the run after execution and supplies a nonnil sink. Validated test
// protocol metadata is copied from ctx for endpoint and attribute preparation.
func NewTestRun(ctx context.Context, headers http.Header, sink provider.TestEventSink) *TestRun {
	ctx, cancel := context.WithCancel(ctx)
	if headers == nil {
		headers = make(http.Header)
	}
	return &TestRun{Context: ctx, Cancel: cancel, Headers: headers, sink: sink, ProtocolRoute: provider.TestProtocolRouteFromContext(ctx)}
}

// GetHeader returns the current value from the run's request headers.
func (r *TestRun) GetHeader(name string) string { return r.Headers.Get(name) }

// SetAutomaticRoute publishes a prepared TLS router result and marks it
// available under the run mutex. AutomaticRoute reads both values together,
// including a prepared result whose Matched flag is false.
func (r *TestRun) SetAutomaticRoute(value egress.TLSFingerprintRouterMatchResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Publish the route and its availability under the same lock, so readers
	// cannot combine a new flag with an earlier routing result.
	r.automaticRoute = value
	r.hasAutomaticRoute = true
}

// AutomaticRoute returns the prepared router result and whether preparation
// published it. A false availability flag distinguishes an unprepared run from
// a prepared automatic run whose router did not match.
func (r *TestRun) AutomaticRoute() (egress.TLSFingerprintRouterMatchResult, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.automaticRoute, r.hasAutomaticRoute
}

// SetSuppressCompletion changes whether stream output emits test_complete
// events under the run mutex. Adaptive probes set it while checking
// intermediate endpoints and clear it before publishing their final completion.
func (r *TestRun) SetSuppressCompletion(value bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Stream output reads this flag through the same mutex. Publish each phase
	// change before an adaptive probe can emit an intermediate or final
	// completion.
	r.suppressCompletion = value
}

// SuppressCompletion reports the current completion-suppression flag under the
// run mutex. Stream output checks it before emitting test_complete, while
// content, status, and error events follow their ordinary output path.
func (r *TestRun) SuppressCompletion() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.suppressCompletion
}

// output runs one sink operation while retaining the first write failure. Later
// calls return that failure and skip their write, so output cannot continue
// after the sink becomes unusable.
//
// The mutex covers the sink callback, error publication, and cancellation
// together, ordering competing writes against that first failure. The callback
// must not reenter run methods that take this mutex. Cancelling Context stops
// cooperative execution; Result later combines the retained sink error with a
// platform error that arose during cleanup.
func (r *TestRun) output(write func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Keep sink invocation, first-error publication, and cancellation serialized.
	// Another event that arrives after a failed write must return that failure
	// without invoking the sink again.
	if r.err == nil {
		if err := write(); err != nil {
			r.err = err
			r.Cancel()
		}
	}
	return r.err
}

// Begin asks the sink to start output with the supplied commit flag. It retains
// any write error and cancels execution; callers use Result to obtain that
// failure after the platform path returns.
func (r *TestRun) Begin(commit bool) {
	// Begin has no error return, so output retains the failure and cancels
	// execution here; the enclosing target retrieves it through Result.
	_ = r.output(func() error { return r.sink.Begin(r.Context, commit) })
}

// Emit sends a typed test event through serialized output and returns the
// retained sink error. The first failed event cancels execution, and subsequent
// events skip the sink operation.
func (r *TestRun) Emit(event provider.TestEvent) error {
	return r.output(func() error { return r.sink.Emit(r.Context, event) })
}

// Result combines the execution result with the run's retained sink failure. It
// returns the supplied error unchanged when no write failed or when that error
// already contains the sink failure through errors.Is.
//
// Otherwise errors.Join retains both causes, including the sink error when the
// platform returned nil. Reading the retained failure under the output mutex
// orders final reporting after an in-progress sink callback, so the target can
// return the output failure that triggered cancellation during execution.
func (r *TestRun) Result(err error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil && !errors.Is(err, r.err) {
		return errors.Join(err, r.err)
	}
	return err
}
