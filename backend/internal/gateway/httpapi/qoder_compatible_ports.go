package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Qoder HTTP compatibility uses target and selection ports to keep provider
// execution separate from admission. Selected targets supply refresh and
// synchronous completion capture, while the runtime retains shared services
// and creates per-request attempt state before the serial text loop runs.

// QoderCompatibleTarget supplies selected execution, credential recovery and
// synchronous completion capture.
type QoderCompatibleTarget interface {
	// Snapshot returns provider metadata used for slot handling and
	// observations.
	Snapshot() provider.ProviderSnapshot
	// Forward executes one target with the requested client wire protocol
	// and response model. The caller owns recovery; an error can accompany
	// measured partial output, whose completion data must survive terminal
	// handling.
	Forward(context.Context, *gin.Context, []byte, protocol.ProtocolID, string) (*forward.MessagesResult, error)
	// Refresh returns a usable target after credential recovery or its
	// error. Callers bound the context and distinguish in-progress recovery
	// from ready credentials before reacquiring resources for a
	// same-provider attempt.
	Refresh(context.Context) (QoderCompatibleTarget, error)
	// Completion synchronously builds recorder input from the supplied
	// request/funding capture. It runs before asynchronous submission,
	// allowing workers to use provider-specific completion data without
	// accessing the active Gin request later.
	Completion(context.Context, QoderCompletionCapture) *completion.Input
}

// QoderCompatibleSelection exposes target resources and scheduling feedback
// for one selection.
type QoderCompatibleSelection interface {
	// Target returns the execution target retained by this provider
	// selection.
	Target() QoderCompatibleTarget
	// Acquired reports whether selection already holds a provider slot. A
	// true value lets initial acquisition reuse ReleaseFunc; otherwise the
	// caller requires WaitPlan before trying to wait for that resource.
	Acquired() bool
	// ReleaseFunc returns cleanup for the selection’s already acquired
	// resource, when present. Attempt acquisition wraps cleanup for
	// stream/cancellation lifetime, and forwarding releases it before
	// reporting selection outcome or attempting recovery.
	ReleaseFunc() func()
	// WaitPlan returns the configured wait plan when selection lacks an
	// acquired slot. A nil plan with Acquired=false makes initial
	// compatible acquisition fail rather than forwarding without its
	// required provider resource.
	WaitPlan() *scheduler.ProviderWaitPlan
	// Report submits provider outcome and optional measured completion
	// observations to selection feedback.
	Report(int64, bool, *forward.MessagesResult)
	// Switched observes replacement of this selection by a later provider.
	Switched()
}

// QoderCompatibleExecution supplies routing, provider selection and persistent
// sticky-session bindings.
type QoderCompatibleExecution interface {
	// Select returns a target/resource handle under group, session, model,
	// excluded provider and subject inputs. Callers use each successful
	// handle serially and retain its feedback and release obligations
	// through the attempt.
	Select(context.Context, *int64, string, string, map[int64]struct{}, int64) (QoderCompatibleSelection, error)
	// Plan resolves requested/group-mapped model routing for the supplied
	// request Key. Compatible admission retains the returned plan and
	// installs it on context so forwarding and completion share the group
	// decision.
	Plan(context.Context, *apikey.APIKey, string) routing.RoutePlan
	// BindStickySession stores a group/session-to-provider association and
	// returns any persistence error. Compatible runtime calls it under a
	// detached bounded context; failures are advisory and do not prevent
	// completion recording.
	BindStickySession(context.Context, *int64, string, int64) error
}

// QoderCompletionCapture carries request funding and metadata into synchronous
// provider completion capture.
type QoderCompletionCapture struct {
	// Result, Key, Subscription, Body and Pricing are assembled before
	// worker submission. Execution copies Body while provider Completion
	// defines the remaining capture; workers consume that input without
	// rereading Gin. measured forwarding completion result
	Result *forward.MessagesResult
	Key    *apikey.APIKey // captured effective funding key
	// optional funding subscription pointer
	Subscription *billing.UserSubscription
	// InboundEndpoint: observed client entry endpoint
	// UpstreamEndpoint: observed provider wire endpoint
	// UserAgent: observed client agent header
	// ClientIP: resolved request client address
	// PayloadHash: original client payload hash
	InboundEndpoint, UpstreamEndpoint, UserAgent, ClientIP, PayloadHash string
	// copied original request body
	Body []byte
	// client and mapped pricing identities
	Pricing routing.PricingUsageFields
}

// QoderCompatibleOptions retains shared Qoder admission, execution and
// recording dependencies.
type QoderCompatibleOptions struct {
	// Execution owns real request selection and sticky binding. Plan and
	// AttemptBody have absent-service defaults, but running compatible
	// attempts requires this service.
	Execution QoderCompatibleExecution // selection and sticky routing service
	// Funding is optional; absent funding permits admission. Configured
	// checks receive a copied request Key and optional subscription before
	// the user wait.
	Funding interface {
		// CheckKey checks funding with the supplied Key, optional
		// subscription and model. Compatible admission passes empty
		// model and skip-RPM=false; any returned error stops before
		// user-slot acquisition and platform attempts.
		CheckKey(context.Context, *apikey.APIKey, *billing.UserSubscription, string, bool) error
	} // optional funding admission service
	Recorder interface {
		// Record persists captured provider completion input using the
		// supplied work context. Compatible recording passes false for
		// its final flag and reports returned errors through request
		// diagnostics after submission.
		Record(context.Context, *completion.Input, bool) error
	} // completion persistence service
	// Pool is optional completion submission state; the shared submission
	// policy interprets its configuration and running status when accepting
	// captured usage work.
	Pool *completion.UsageRecordWorkerPool // optional completion worker pool
	// Slots supplies provider concurrency when configured; nil lets the
	// compatible resource helpers return success without acquiring or
	// releasing a provider slot.
	Slots *ConcurrencyHelper // optional provider concurrency helper
	// Enter is optional; without it, runtime admission supplies a no-op
	// cleanup. Configured admission callers release the returned barrier
	// when handling finishes.
	Enter      func() (func(), error)                    // optional application request barrier
	ReadAccess func(*gin.Context) (*apikey.APIKey, bool) // middleware authentication key reader
	Errors     QoderErrorPresenter                       // required Qoder failure presenter
	// Rules is bound to the active request when present. Nil leaves
	// existing bindings unchanged; ordinary protocol defaults apply when no
	// caller has bound a service.
	Rules *errorpolicy.ErrorPassthroughService // optional bound error rules
	// PlatformAvailable gates credential refresh together with stream
	// progress; false prevents invoking MayRefresh or asking a target to
	// refresh its credentials. configured credential refresh capability
	PlatformAvailable bool
	// MayRefresh is required when platform refresh is available and output
	// has not started; its classification alone does not authorize replay
	// after output changes. refresh eligibility classification callback
	MayRefresh func(error) bool
	// MaySwitch classifies every forwarded outcome, including nil error,
	// before the text loop interprets recovery. Real request execution
	// requires this callback to exist.
	MaySwitch func(error) bool // provider switch classification callback
}

// QoderCompatibleRuntime retains shared services while building request-local
// compatible attempt state.
type QoderCompatibleRuntime struct {
	options           QoderCompatibleOptions // retained shared runtime bindings
	concurrencyHelper *ConcurrencyHelper     // configured provider slot helper
}

// NewQoderCompatibleRuntime retains the supplied shared services and slot
// helper. It allocates no request state; callers configure execution,
// recording and error classification before running the compatible handler,
// while optional bindings keep their documented defaults.
func NewQoderCompatibleRuntime(options QoderCompatibleOptions) *QoderCompatibleRuntime {
	return &QoderCompatibleRuntime{options: options, concurrencyHelper: options.Slots}
}
