package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	opscore "github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// An upstream stream can fail while its HTTP status remains 200, and a terminal
// error can be split across writes or exceed the captured-body prefix. Bounded
// line/frame probes retain enough terminal evidence to classify that failure
// separately from the bytes stored for diagnostics. Capture observes attempted
// response input before delegated writes, so captured bytes do not confirm
// delivery.
//
// Fresh writer handles bind to a generation of reusable state. Release
// invalidates that generation before waiting for registered network work, waits
// outside the state mutex, then resets and possibly pools bounded storage. This
// keeps a stale handle from reading or writing a later request while allowing
// an already registered network operation to finish.
//
// Final middleware chooses terminal, marked-stream or recovered-attempt
// attribution and captures identity before queue submission. Logical failure
// status remains separate from committed wire status, and the last relevant
// attempt can replace stale top-level error fields. Queue implementations can
// retain and prepare mutable entries, so submitters stop changing the captured
// input afterward.

// These keys identify request model, stream and provider observations, while
// matching strings and capture budgets support final error extraction and filtering.
const (
	OpsModelKey                  = "ops_model"
	OpsStreamKey                 = "ops_stream"
	OpsProviderIDKey             = "ops_provider_id"
	opsRoutingCapacityLimitedKey = "ops_routing_capacity_limited"
	opsDedicatedErrorRecordedKey = "ops_dedicated_error_recorded"

	opsUpstreamModelKey = OpsUpstreamModelKey
	opsRequestTypeKey   = "ops_request_type"

	// Error filtering and classification compare these shared spellings against
	// diagnostic text to distinguish cancellation, credentials, quota and provider availability.
	opsErrContextCanceled             = "context canceled"
	opsErrNoAvailableProviders        = "no available providers"
	opsErrInvalidAPIKey               = "invalid_api_key"
	opsErrAPIKeyRequired              = "api_key_required"
	opsErrInsufficientBalance         = "insufficient balance"
	opsErrInsufficientProviderBalance = "insufficient account balance"
	opsErrInsufficientQuota           = "insufficient_quota"

	// Body capture and pool retention follow the Ops queue byte budget, while
	// terminal metadata uses a separate bounded frame probe across split writes.
	opsCaptureWriterLimit         = opscore.OpsErrorLogQueueBodyMaxBytes
	opsTerminalSSEFrameProbeLimit = 16 * 1024

	opsCaptureWriterPoolMaxRetainedCapacity = opscore.OpsErrorLogQueueBodyMaxBytes
)

// The shared state pool reuses bounded capture allocations; the interface
// assertion checks that fresh leased handles retain Gin writer capabilities.
var (
	opsCaptureWriterPool opsCaptureWriterStatePool = &sync.Pool{
		New: func() any {
			return &opsCaptureWriterState{limit: opsCaptureWriterLimit}
		},
	}

	_ gin.ResponseWriter = (*opsCaptureWriter)(nil)
)

// OpsErrorLogQueue accepts request observations for application-managed
// asynchronous error persistence.
type OpsErrorLogQueue interface {
	// Enqueue accepts a service and completed observation whose pointer may be
	// retained and prepared by the queue. Callers stop mutating the entry and its
	// referenced identifiers or event storage after submission; this method returns
	// no admission or persistence confirmation.
	Enqueue(*opscore.OpsService, *opscore.OpsInsertErrorLogInput)
}

// OpsObservationAccess supplies identity and admission-rejection data for error logging.
type OpsObservationAccess struct {
	// APIKey optionally supplies loaded identity for final observation; nil yields
	// no key. Queued entries can borrow Key, user and group ID pointers from this
	// accessor-owned data, whose referenced identifiers remain immutable after
	// submission.
	APIKey func(*gin.Context) *apikey.APIKey
	// Rejected optionally identifies ingress denial before normal logging. Nil
	// permits capture; writer paths call this predicate while holding state
	// ownership, so it avoids writer reentry.
	Rejected func(*gin.Context) bool
}

// opsCaptureWriter binds one fresh HTTP handle to a reusable capture-state
// generation.
type opsCaptureWriter struct {
	// state and generation bind this fresh handle to one pooled lease. pool
	// receives reusable state only after release invalidation and delegated work
	// finish; older generations cannot access that later lease.
	state      *opsCaptureWriterState    // borrowed leased capture storage
	generation uint64                    // handle lease generation value
	pool       opsCaptureWriterStatePool // captured state return destination
}

// opsCaptureWriterState retains bounded parser evidence and delegated-operation
// ownership for one lease.
type opsCaptureWriterState struct {
	// mu protects generation, responseWriter and parser mutation, while inFlight
	// counts accepted delegated operations. Registration and lease validation
	// share mu; release invalidates generation before waiting with mu unlocked.
	mu             sync.RWMutex       // capture lease and parser lock
	inFlight       sync.WaitGroup     // registered delegated operation join
	generation     uint64             // current capture lease generation
	responseWriter gin.ResponseWriter // active downstream writer reference
	limit          int                // captured response byte limit
	buf            bytes.Buffer       // bounded attempted response bytes
	// probe, lineProbe, frameLineLen, frameTruncated, lineTruncated and skipLF
	// retain one split SSE parser position across chunks, with independent frame
	// and line byte budgets.
	probe          []byte // bounded terminal frame bytes
	lineProbe      []byte // bounded current SSE line bytes
	frameLineLen   int    // current SSE line byte count
	frameTruncated bool   // frame probe exceeded budget
	lineTruncated  bool   // line probe exceeded budget
	skipLF         bool   // pending CRLF tail suppression
	// sseCapturing, terminalError and terminalFound retain known failure
	// separately from body prefix storage. Final probe decoding can improve
	// metadata without erasing an already recognized terminal event.
	sseCapturing  bool           // terminal failure body capture enabled
	terminalError parsedOpsError // retained parsed terminal metadata
	terminalFound bool           // terminal failure evidence present
	// ctx retains the request for the active lease. Release clears it before
	// waiting, and a later acquisition installs new observation context only
	// through setContext.
	ctx *gin.Context
	// rejected is a read-only ingress predicate called under state mutation
	// ownership. It avoids capture-writer reentry, and release clears it before
	// joining delegated calls.
	rejected func(*gin.Context) bool
}

// opsCaptureWriterStatePool supplies released capture states for bounded writer
// reuse.
type opsCaptureWriterStatePool interface {
	// Get supplies an available unowned state for a new lease. The writer
	// constructor replaces unexpected or typed nil values with fresh state; shared
	// implementations coordinate concurrent pool access.
	Get() any
	// Put accepts released reusable state after all registered writer calls finish
	// and capacity checks pass. Shared implementations coordinate concurrent
	// returns, and callers keep active states outside the pool.
	Put(any)
}

// parsedOpsError carries extracted client failure fields and terminal stream
// evidence.
type parsedOpsError struct {
	ErrorType  string // extracted client error category
	Message    string // extracted failure message text
	Code       string // extracted client error code
	StatusCode int    // optional logical failure HTTP status
	// StreamFailure records recognized terminal evidence even when payload parsing
	// is incomplete. Middleware can infer a logical failure status despite already
	// successful HTTP commitment.
	StreamFailure bool
}

// key reads the observed API Key through the optional request accessor. Missing
// accessors return nil; the returned key remains accessor-owned input, and
// final error capture takes its relevant identity fields before queue
// submission.
func (a OpsObservationAccess) key(c *gin.Context) *apikey.APIKey {
	if a.APIKey == nil {
		return nil
	}
	return a.APIKey(c)
}

// rejected reports the optional ingress rejection predicate for this request.
// Missing predicates return false, leaving normal capture and monitoring checks
// active; callers supply a read-only predicate that is safe during writer
// capture.
func (a OpsObservationAccess) rejected(c *gin.Context) bool {
	return a.Rejected != nil && a.Rejected(c)
}

// keyPrefix returns at most n leading key bytes for persisted identification.
// Callers supply a nonnegative n; shorter keys return whole, and a byte cut
// does not validate UTF-8 because this is credential-prefix evidence.
func keyPrefix(key string, n int) string {
	if len(key) <= n {
		return key
	}
	// Keep only credential identification bytes for diagnostics; callers use this
	// prefix after the full Key may have been deleted.
	return key[:n]
}

// SetOpsRequestContext records client model attribution and stream intent,
// preferring nonblank telemetry ClientModel before composite display identity
// and the supplied model. Nil context skips updates. Supplied and telemetry
// names are trimmed; a selected composite display value is stored as supplied
// by its context getter.
//
// The raw client name can differ from mapped provider input, so this precedence
// keeps request error attribution tied to the original client signal. A
// composite name fills the gap only when telemetry supplied no usable client
// value. The chosen model is also published to request telemetry when a live
// request and nonblank name exist; blank updates can still replace the Gin
// model value.
func SetOpsRequestContext(c *gin.Context, model string, stream bool) {
	if c == nil {
		return
	}
	model = strings.TrimSpace(model)
	hasClientModel := false
	if c.Request != nil {
		if clientModel, ok := c.Request.Context().Value(telemetry.ClientModel).(string); ok && strings.TrimSpace(clientModel) != "" {
			model = strings.TrimSpace(clientModel)
			hasClientModel = true
		}
	}
	// Use the composite client display name only after telemetry provided no
	// usable identity, keeping a mapped dispatch model from replacing available
	// client attribution.
	if clientModel, _, ok := GetCompositeModelFromContext(c); ok && !hasClientModel {
		model = clientModel
	}
	c.Set(OpsModelKey, model)
	c.Set(OpsStreamKey, stream)
	if c.Request != nil && model != "" {
		ctx := context.WithValue(c.Request.Context(), telemetry.Model, model)
		c.Request = c.Request.WithContext(ctx)
	}
}

// SetOpsEndpointContext stores the attempt upstream model and request type for
// final capture. Nil context is ignored; blank model retains earlier model
// state while the supplied request type is always stored for its later typed
// reader.
func SetOpsEndpointContext(c *gin.Context, upstreamModel string, requestType int16) {
	if c == nil {
		return
	}
	if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel != "" {
		c.Set(opsUpstreamModelKey, upstreamModel)
	}
	c.Set(opsRequestTypeKey, requestType)
}

// SetOpsSelectedProvider records positive provider identity and clears prior
// upstream model attribution. Missing context or nonpositive ID skips all
// updates. Only platform[0] is inspected and its nonblank trimmed value enters
// request telemetry. Later variadic entries are ignored, and a blank first
// value retains previous platform evidence.
func SetOpsSelectedProvider(c *gin.Context, providerID int64, platform ...string) {
	if c == nil || providerID <= 0 {
		return
	}
	// Clear the previous attempt model before publishing a new provider, so
	// credential or preparation failures cannot inherit the earlier provider
	// model.
	ClearOpsUpstreamModel(c)
	c.Set(OpsProviderIDKey, providerID)
	if c.Request != nil {
		ctx := context.WithValue(c.Request.Context(), telemetry.ProviderID, providerID)
		if len(platform) > 0 {
			p := strings.TrimSpace(platform[0])
			if p != "" {
				ctx = context.WithValue(ctx, telemetry.Platform, p)
			}
		}
		c.Request = c.Request.WithContext(ctx)
	}
}

// MarkOpsRoutingCapacityLimited marks local routing capacity evidence for Ops
// classification. Nil context is ignored; the classifier reads this marker
// separately from message text so masked capacity errors can retain their SLA
// exclusion.
func MarkOpsRoutingCapacityLimited(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(opsRoutingCapacityLimitedKey, true)
}

// MarkOpsRoutingCapacityLimitedIfNoAvailable marks recognized no-provider
// selection errors as local capacity limits. Nil or unrelated errors leave
// state unchanged; shared sentinel and message classification determine whether
// the routing marker should be added.
func MarkOpsRoutingCapacityLimitedIfNoAvailable(c *gin.Context, err error) {
	if !IsOpsNoAvailableProviderError(err) {
		return
	}
	MarkOpsRoutingCapacityLimited(c)
}

// isOpsRoutingCapacityLimited reads the typed local capacity marker from Gin
// context. Nil context, absent values or another stored type returns false,
// allowing other classification evidence to determine the final error owner.
func isOpsRoutingCapacityLimited(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, ok := c.Get(opsRoutingCapacityLimitedKey)
	if !ok {
		return false
	}
	marked, _ := v.(bool)
	return marked
}

// IsOpsNoAvailableProviderError recognizes standard no-provider sentinels and
// shared message classification. Nil returns false; wrapped ordinary or Compact
// selection sentinels match before textual diagnosis, preserving their typed
// availability meaning.
func IsOpsNoAvailableProviderError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, scheduler.ErrNoAvailableProviders) || errors.Is(err, scheduler.ErrNoAvailableCompactProviders) {
		return true
	}
	// When typed selection sentinels are absent, shared message recognition still
	// classifies wrapped or externally constructed availability failures for local
	// capacity marking.
	return opscore.IsNoAvailableProviderMessage(err.Error())
}

// acquireOpsCaptureWriter obtains a fresh handle from the shared capture-state
// pool. Callers release the handle after capture and delegated operations
// finish; the pool-aware constructor owns state reset and generation-based
// isolation from older handles.
func acquireOpsCaptureWriter(rw gin.ResponseWriter) *opsCaptureWriter {
	return acquireOpsCaptureWriterFromPool(opsCaptureWriterPool, rw)
}

// acquireOpsCaptureWriterFromPool creates a fresh handle bound to reset capture
// state and its writer. Nil pools, wrong pooled types and typed nil values
// allocate fresh state; supplied pools return unowned states suitable for
// reuse.
//
// State reset and generation increment occur under the state mutex before the
// handle is returned. That generation lets later access reject an older handle
// pointing to the same pooled storage. All response, parser, context and
// rejection fields are reset for this lease, while buffer capacity can remain
// allocated until release decides whether the state meets retention limits.
func acquireOpsCaptureWriterFromPool(pool opsCaptureWriterStatePool, rw gin.ResponseWriter) *opsCaptureWriter {
	var pooled any
	if pool != nil {
		pooled = pool.Get()
	}
	state, ok := pooled.(*opsCaptureWriterState)
	if !ok || state == nil {
		state = &opsCaptureWriterState{}
	}
	// Reset the unowned state under its mutex and advance generation before
	// returning a fresh handle, isolating this request from retained older
	// references.
	state.mu.Lock()
	state.generation++
	state.responseWriter = rw
	state.limit = opsCaptureWriterLimit
	state.buf.Reset()
	state.probe = state.probe[:0]
	state.lineProbe = state.lineProbe[:0]
	state.frameLineLen = 0
	state.frameTruncated = false
	state.lineTruncated = false
	state.skipLF = false
	state.sseCapturing = false
	state.terminalError = parsedOpsError{}
	state.terminalFound = false
	state.ctx = nil
	state.rejected = nil
	generation := state.generation
	state.mu.Unlock()
	return &opsCaptureWriter{state: state, generation: generation, pool: pool}
}

// releaseOpsCaptureWriter invalidates an active handle, waits for registered
// delegated work and resets its pooled state. Nil or stale handles return
// immediately. Invalidation clears writer and context references under the
// mutex before waiting, so no later operation can register through that handle.
//
// The wait runs with the mutex released, allowing existing network calls and
// their completion callbacks to finish. State reset follows that join, and
// capacity checks decide whether reusable storage is returned to the captured
// pool. Oversized buffers are left unpooled. A stale repeated release cannot
// return a state now owned by another generation.
func releaseOpsCaptureWriter(w *opsCaptureWriter) {
	if w == nil || w.state == nil {
		return
	}
	state := w.state
	state.mu.Lock()
	if state.generation != w.generation {
		state.mu.Unlock()
		return
	}
	// Invalidate the lease before waiting. No new delegated calls can start for
	// this handle, while calls that already copied the writer keep it alive via
	// inFlight until their network operation returns.
	state.generation++
	state.responseWriter = nil
	state.ctx = nil
	state.rejected = nil
	state.mu.Unlock()
	// Wait with state.mu released because accepted writer calls finish outside
	// that mutex; generation invalidation already prevents this handle from
	// registering additional work.
	state.inFlight.Wait()
	state.mu.Lock()
	state.limit = opsCaptureWriterLimit
	state.probe = state.probe[:0]
	state.lineProbe = state.lineProbe[:0]
	state.frameLineLen = 0
	state.frameTruncated = false
	state.lineTruncated = false
	state.skipLF = false
	state.sseCapturing = false
	state.terminalError = parsedOpsError{}
	state.terminalFound = false
	poolable := shouldPoolOpsCaptureWriterState(state)
	state.buf.Reset()
	state.mu.Unlock()
	if poolable && w.pool != nil {
		w.pool.Put(state)
	}
}

// shouldPoolOpsCaptureWriterState checks retained body and parser capacities
// against pool budgets. Nil state fails; callers inspect isolated or locked
// state so concurrent buffer growth cannot race with this release-time
// retention decision.
func shouldPoolOpsCaptureWriterState(state *opsCaptureWriterState) bool {
	return state != nil && state.buf.Cap() <= opsCaptureWriterPoolMaxRetainedCapacity &&
		cap(state.probe) <= opsTerminalSSEFrameProbeLimit && cap(state.lineProbe) <= 256
}

// lockActive acquires a read lock for the handle's current generation and
// returns its writer. Invalid or missing leases yield nil values with no held
// lock; successful callers release state.mu.RUnlock after reading the returned
// state or writer.
func (w *opsCaptureWriter) lockActive() (*opsCaptureWriterState, gin.ResponseWriter) {
	if w == nil || w.state == nil {
		return nil, nil
	}
	state := w.state
	state.mu.RLock()
	if state.generation != w.generation || state.responseWriter == nil {
		state.mu.RUnlock()
		return nil, nil
	}
	return state, state.responseWriter
}

// lockActiveWrite acquires the state write lock for the current handle
// generation. Invalid or absent writer leases return nil with the lock
// released; successful callers own state.mu.Unlock after their mutation or
// capture work.
func (w *opsCaptureWriter) lockActiveWrite() (*opsCaptureWriterState, gin.ResponseWriter) {
	if w == nil || w.state == nil {
		return nil, nil
	}
	state := w.state
	state.mu.Lock()
	if state.generation != w.generation || state.responseWriter == nil {
		state.mu.Unlock()
		return nil, nil
	}
	return state, state.responseWriter
}

// beginDelegatedCall registers a writer operation under the active generation
// and returns with state.mu held. Missing or stale handles return nil with no
// lock obligation; accepted callers unlock before blocking writer work and call
// finishDelegatedCall when it ends.
//
// Registration and generation validation share the mutex used by release
// invalidation. This prevents a new network operation from joining after
// release has started its WaitGroup join. The copied writer stays reachable
// through the registered call, so cleanup can clear its state reference while
// the operation finishes outside the mutex.
func (w *opsCaptureWriter) beginDelegatedCall() (*opsCaptureWriterState, gin.ResponseWriter) {
	if w == nil || w.state == nil {
		return nil, nil
	}
	state := w.state
	state.mu.Lock()
	if state.generation != w.generation || state.responseWriter == nil {
		state.mu.Unlock()
		return nil, nil
	}
	// Validate and register under the same mutex used by release invalidation,
	// preventing an operation from joining after cleanup has begun waiting.
	rw := state.responseWriter
	state.inFlight.Add(1)
	return state, rw
}

// finishDelegatedCall ends one accepted delegated operation for capture-state
// release waiting. Nil state is ignored; callers invoke it exactly once for a
// successful beginDelegatedCall after the copied writer operation has finished.
func finishDelegatedCall(state *opsCaptureWriterState) {
	if state != nil {
		state.inFlight.Done()
	}
}

// setContext binds request observation to the active capture lease under its
// write lock. Invalid handles are ignored. Only the first optional rejection
// callback is retained; callers provide a read-only predicate that avoids
// capture-writer reentry when later writes hold this mutex.
func (w *opsCaptureWriter) setContext(ctx *gin.Context, rejected ...func(*gin.Context) bool) {
	state, _ := w.lockActiveWrite()
	if state == nil {
		return
	}
	state.ctx = ctx
	if len(rejected) > 0 {
		state.rejected = rejected[0]
	}
	state.mu.Unlock()
}

// capturedBytes copies the bounded attempted-response buffer from an active
// lease. Invalid handles return nil; the returned bytes have independent
// storage, allowing middleware to parse or queue them after releasing the read
// lock.
func (w *opsCaptureWriter) capturedBytes() []byte {
	state, _ := w.lockActive()
	if state == nil {
		return nil
	}
	defer state.mu.RUnlock()
	return append([]byte(nil), state.buf.Bytes()...)
}

// capturedTerminalError reads retained parsed failure metadata and its presence
// flag under the active lease lock. Invalid handles return empty and false;
// scalar values copy out separately from the bounded captured body.
func (w *opsCaptureWriter) capturedTerminalError() (parsedOpsError, bool) {
	state, _ := w.lockActive()
	if state == nil {
		return parsedOpsError{}, false
	}
	defer state.mu.RUnlock()
	return state.terminalError, state.terminalFound
}

// finalizeCapture asks active state to finish terminal parsing before
// middleware attribution. Invalid handles are ignored, and the write lock
// protects any final probe parsing or buffer update from concurrent capture
// operations.
func (w *opsCaptureWriter) finalizeCapture() {
	state, _ := w.lockActiveWrite()
	if state == nil {
		return
	}
	defer state.mu.Unlock()
	state.finalizeResponseCapture()
}

// Header returns the active writer's shared header map under lease validation.
// Invalid handles return a fresh empty map; callers coordinate later map
// mutations because the read lock protects obtaining the reference, not
// accessing it after return.
func (w *opsCaptureWriter) Header() http.Header {
	state, rw := w.lockActive()
	if state == nil {
		return http.Header{}
	}
	defer state.mu.RUnlock()
	return rw.Header()
}

// WriteHeader delegates status presentation through a registered active writer
// call. Invalid handles do nothing; the state lock is released before writer
// work, and release waits until its deferred delegated completion has run.
func (w *opsCaptureWriter) WriteHeader(code int) {
	state, rw := w.beginDelegatedCall()
	if state == nil {
		return
	}
	state.mu.Unlock()
	defer finishDelegatedCall(state)
	rw.WriteHeader(code)
}

// WriteHeaderNow delegates immediate header commitment through active
// registered ownership. Invalid handles do nothing, while accepted calls run
// outside the state mutex and remain counted until the writer operation
// returns.
func (w *opsCaptureWriter) WriteHeaderNow() {
	state, rw := w.beginDelegatedCall()
	if state == nil {
		return
	}
	state.mu.Unlock()
	defer finishDelegatedCall(state)
	rw.WriteHeaderNow()
}

// Status returns the active writer's HTTP status after generation validation.
// An absent or released handle returns zero; callers read that default as
// unavailable writer state rather than proof of an actual client response.
func (w *opsCaptureWriter) Status() int {
	state, rw := w.lockActive()
	if state == nil {
		return 0
	}
	defer state.mu.RUnlock()
	return rw.Status()
}

// Size returns the active writer's byte count under generation validation.
// Missing or stale leases return Gin's negative unwritten sentinel, keeping
// released state from resembling a successfully committed zero-byte body.
func (w *opsCaptureWriter) Size() int {
	state, rw := w.lockActive()
	if state == nil {
		return -1
	}
	defer state.mu.RUnlock()
	return rw.Size()
}

// Written reports active writer commitment after validating the handle
// generation. A missing or stale lease returns false, so old handles cannot
// observe commitment belonging to a later request using their pooled state.
func (w *opsCaptureWriter) Written() bool {
	state, rw := w.lockActive()
	if state == nil {
		return false
	}
	defer state.mu.RUnlock()
	return rw.Written()
}

// Flush delegates active writer flushing outside the capture-state mutex. A
// released handle makes no call; accepted flushes remain registered until
// return so state release waits for blocking transport flush work.
func (w *opsCaptureWriter) Flush() {
	state, rw := w.beginDelegatedCall()
	if state == nil {
		return
	}
	state.mu.Unlock()
	defer finishDelegatedCall(state)
	rw.Flush()
}

// Hijack delegates connection takeover through a registered writer operation
// outside the state lock. Released handles return a response-writer-released
// error with no connection; accepted callers receive the original writer result
// and own any successful hijacked connection.
func (w *opsCaptureWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	state, rw := w.beginDelegatedCall()
	if state == nil {
		return nil, nil, errors.New("response writer released")
	}
	state.mu.Unlock()
	defer finishDelegatedCall(state)
	return rw.Hijack()
}

// CloseNotify returns the active writer's close notification channel after
// lease validation. Released handles return a newly closed channel, making
// reads immediately ready with its zero boolean value; no active connection
// notification is retained in that fallback.
func (w *opsCaptureWriter) CloseNotify() <-chan bool {
	state, rw := w.lockActive()
	if state == nil {
		ch := make(chan bool)
		// With no active connection to notify, make fallback reads immediately ready
		// so a caller cannot wait indefinitely for a notification this handle cannot
		// produce.
		close(ch)
		return ch
	}
	defer state.mu.RUnlock()
	return rw.CloseNotify()
}

// Pusher returns the active writer's optional HTTP push interface after lease
// validation. An absent or released handle returns nil, so callers can inspect
// support without reaching writer state reused by another request.
func (w *opsCaptureWriter) Pusher() http.Pusher {
	state, rw := w.lockActive()
	if state == nil {
		return nil
	}
	defer state.mu.RUnlock()
	return rw.Pusher()
}

// Write captures eligible attempted bytes before delegating the active network
// write. Invalid handles return zero with nil error. Capture happens under the
// state lock and includes supplied bytes even when downstream later reports
// failure or a short write.
//
// The lock is released before network work, while delegated registration keeps
// release waiting until that operation returns. This separates bounded
// diagnostic observation from actual delivery evidence: callers use the
// returned byte count and error for transport handling. The release path
// invalidates new calls first and pools state only after registered work
// finishes.
func (w *opsCaptureWriter) Write(b []byte) (int, error) {
	state, rw := w.beginDelegatedCall()
	if state == nil {
		return 0, nil
	}
	if state.shouldCapture() {
		state.captureResponseChunk(b, rw.Status())
	}
	// Keep capture mutation inside the lease lock, then release it before network
	// delivery; the registered operation still prevents cleanup from pooling this
	// state.
	state.mu.Unlock()
	defer finishDelegatedCall(state)
	return rw.Write(b)
}

// WriteString captures eligible attempted string bytes before delegating to the
// active writer. Missing or stale handles return zero with nil error. A later
// writer failure leaves this attempted-input capture available; it does not
// imply that all captured bytes reached the client.
//
// Capture and generation validation occur under the state mutex, then network
// work runs outside it with delegated registration retained. Cleanup can
// invalidate the handle and clear live references while waiting for that
// registered operation. The returned count and error remain the underlying
// writer's delivery result, separate from bounded diagnostic storage.
func (w *opsCaptureWriter) WriteString(s string) (int, error) {
	state, rw := w.beginDelegatedCall()
	if state == nil {
		return 0, nil
	}
	if state.shouldCapture() {
		state.captureResponseChunk([]byte(s), rw.Status())
	}
	// Keep capture mutation inside the lease lock, then release it before network
	// delivery; the registered operation still prevents cleanup from pooling this
	// state.
	state.mu.Unlock()
	defer finishDelegatedCall(state)
	return rw.WriteString(s)
}

// isOpsTerminalSSEFrame recognizes response.failed or error from event metadata
// or a valid payload type. Explicit terminal event lines qualify even when data
// cannot decode; other frames use a cheap marker check before JSON type
// validation.
func isOpsTerminalSSEFrame(frame []byte) bool {
	eventType, payload := parseOpsSSEFrameEnvelope(frame)
	if bytes.Equal(eventType, []byte("response.failed")) || bytes.Equal(eventType, []byte("error")) {
		return true
	}
	if len(payload) == 0 {
		return false
	}
	// Most successful frames cannot be terminal. Avoid JSON decoding on this
	// hot path while still validating any plausible terminal payload below.
	if !bytes.Contains(payload, []byte("response.failed")) && !bytes.Contains(payload, []byte(`"error"`)) {
		return false
	}
	var event struct {
		Type string `json:"type"` // decoded SSE payload type
	}
	return json.Unmarshal(payload, &event) == nil &&
		(event.Type == "response.failed" || event.Type == "error")
}

// parseOpsSSEFrameEnvelope extracts the last event value and newline-joined
// data from one frame. It accepts LF, CRLF and CR separators, trims fields and
// values, and ignores comments or empty lines.
//
// The first data value can reference frame storage directly. When another data
// line arrives, that value is copied before concatenation so joining cannot
// overwrite the input frame. Subsequent data lines append with a newline,
// allowing split JSON data fields to be decoded as one payload. Returned event
// bytes and single-line data can remain borrowed, so callers keep frame storage
// stable while using them.
func parseOpsSSEFrameEnvelope(frame []byte) ([]byte, []byte) {
	var eventType []byte
	var data []byte
	dataOwned := false
	dataSeen := false
	for len(frame) > 0 {
		line := frame
		lf := bytes.IndexByte(frame, '\n')
		cr := bytes.IndexByte(frame, '\r')
		idx := lf
		if idx < 0 || (cr >= 0 && cr < idx) {
			idx = cr
		}
		if idx >= 0 {
			line = frame[:idx]
			consume := idx + 1
			if frame[idx] == '\r' && consume < len(frame) && frame[consume] == '\n' {
				consume++
			}
			frame = frame[consume:]
		} else {
			frame = nil
		}
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		field, value, found := bytes.Cut(line, []byte{':'})
		if !found {
			value = nil
		}
		field = bytes.TrimSpace(field)
		value = bytes.TrimSpace(value)
		switch {
		case bytes.Equal(field, []byte("event")):
			eventType = value
		case bytes.Equal(field, []byte("data")):
			if !dataSeen {
				data = value
				dataSeen = true
				continue
			}
			// The first data line borrows the input frame; copy before joining later
			// lines so append cannot overwrite bytes still belonging to that frame.
			if !dataOwned {
				data = append([]byte(nil), data...)
				dataOwned = true
			}
			data = append(data, '\n')
			data = append(data, value...)
		}
	}
	return bytes.TrimSpace(eventType), data
}

// captureResponseChunk observes attempted output under caller-held capture
// ownership. Nil state, nonpositive limits and empty input skip work; HTTP
// error status appends a bounded body directly. Successful status starts with
// bounded SSE line/frame probes.
//
// Complete chunks that cannot contain a terminal marker skip byte parsing when
// no prior split fragment exists. Otherwise parser state carries partial lines
// across writes and recognizes CRLF as one separator. A terminal event line
// establishes generic failure evidence before its data is complete; a valid
// terminal frame can supply structured metadata. Capturing then retains bounded
// failure bytes and a separate probe for richer final metadata, so body
// truncation cannot turn a known failure into an apparently successful stream.
func (state *opsCaptureWriterState) captureResponseChunk(chunk []byte, status int) {
	if state == nil || state.limit <= 0 || len(chunk) == 0 {
		return
	}
	if status >= 400 {
		state.appendCapturedResponse(chunk)
		return
	}
	if state.sseCapturing {
		state.appendTerminalProbe(chunk)
		state.appendCapturedResponse(chunk)
		return
	}
	// Most stream writes contain one or more complete successful SSE frames.
	// Skip the byte-wise frame parser when the chunk cannot contain a terminal
	// event and leaves no split frame to carry into the next write.
	if len(state.probe) == 0 && len(state.lineProbe) == 0 && endsAtOpsSSEFrameBoundary(chunk) &&
		!mayContainOpsTerminalSSE(chunk) {
		return
	}
	for i, b := range chunk {
		// A carriage return may end the previous chunk. Consume its following LF once
		// so CRLF cannot create an extra empty frame across writes.
		if state.skipLF {
			state.skipLF = false
			if b == '\n' {
				continue
			}
		}
		// Bound line and frame probes independently while still counting separators,
		// so oversized successful data cannot grow diagnostic parser storage
		// indefinitely.
		if !state.lineTruncated {
			if len(state.lineProbe) < 256 {
				state.lineProbe = append(state.lineProbe, b)
			} else {
				state.lineTruncated = true
			}
		}
		if !state.frameTruncated {
			if len(state.probe) < opsTerminalSSEFrameProbeLimit {
				state.probe = append(state.probe, b)
			} else {
				state.frameTruncated = true
			}
		}
		if b != '\n' && b != '\r' {
			state.frameLineLen++
			continue
		}
		if b == '\r' {
			state.skipLF = true
		}
		// A terminal event line establishes failure before the data body arrives;
		// retain generic metadata and start bounded capture even if later JSON is
		// truncated.
		if !state.lineTruncated && isOpsTerminalSSEEventLine(state.lineProbe) {
			state.sseCapturing = true
			state.terminalError = parsedOpsError{ErrorType: "upstream_error", StreamFailure: true}
			state.terminalFound = true
			state.appendCapturedResponse(state.lineProbe)
			state.probe = state.probe[:0]
			state.appendTerminalProbe(state.lineProbe)
			state.lineProbe = state.lineProbe[:0]
			state.frameLineLen = 0
			state.frameTruncated = false
			state.lineTruncated = false
			state.skipLF = false
			state.appendTerminalProbe(chunk[i+1:])
			state.appendCapturedResponse(chunk[i+1:])
			return
		}
		if state.frameLineLen != 0 {
			state.frameLineLen = 0
			state.lineProbe = state.lineProbe[:0]
			state.lineTruncated = false
			continue
		}
		// Data-only terminal frames need the complete bounded frame for type
		// validation, allowing their decoded failure to be retained after this probe
		// is reset.
		if !state.frameTruncated && isOpsTerminalSSEFrame(state.probe) {
			state.sseCapturing = true
			state.terminalError, state.terminalFound = parseOpsSSEFailure(state.probe)
			state.appendCapturedResponse(state.probe)
			state.probe = state.probe[:0]
			state.appendCapturedResponse(chunk[i+1:])
			return
		}
		state.probe = state.probe[:0]
		state.lineProbe = state.lineProbe[:0]
		state.frameTruncated = false
		state.lineTruncated = false
	}
}

// endsAtOpsSSEFrameBoundary reports LF, CRLF or CR blank-line termination for
// one chunk.
func endsAtOpsSSEFrameBoundary(chunk []byte) bool {
	return bytes.HasSuffix(chunk, []byte("\n\n")) ||
		bytes.HasSuffix(chunk, []byte("\r\n\r\n")) ||
		bytes.HasSuffix(chunk, []byte("\r\r"))
}

// mayContainOpsTerminalSSE checks raw terminal marker spellings before detailed
// frame parsing. It is a byte heuristic, not proof of a failure event;
// plausible chunks still pass protocol parsing and JSON type validation.
func mayContainOpsTerminalSSE(chunk []byte) bool {
	if bytes.Contains(chunk, []byte("response.failed")) {
		return true
	}
	return bytes.Contains(chunk, []byte("error")) &&
		(bytes.Contains(chunk, []byte("event")) || bytes.Contains(chunk, []byte(`"type"`)))
}

// isOpsTerminalSSEEventLine checks an event field for terminal response.failed
// or error values. Surrounding whitespace is trimmed and a colon is required;
// this event-line evidence can identify failure before its data payload
// finishes.
func isOpsTerminalSSEEventLine(line []byte) bool {
	line = bytes.TrimSpace(line)
	field, value, found := bytes.Cut(line, []byte{':'})
	return found && bytes.Equal(bytes.TrimSpace(field), []byte("event")) &&
		(bytes.Equal(bytes.TrimSpace(value), []byte("response.failed")) ||
			bytes.Equal(bytes.TrimSpace(value), []byte("error")))
}

// appendTerminalProbe appends at most the remaining terminal metadata probe
// budget. Overflow sets frameTruncated while retaining its prefix; callers hold
// state mutation ownership, and the bounded body buffer remains separate from
// this metadata probe.
func (state *opsCaptureWriterState) appendTerminalProbe(chunk []byte) {
	remaining := opsTerminalSSEFrameProbeLimit - len(state.probe)
	if remaining <= 0 {
		state.frameTruncated = true
		return
	}
	// Retain the available metadata prefix and mark truncation so EOF handling
	// does not treat an incomplete probe as a newly complete frame.
	if len(chunk) > remaining {
		chunk = chunk[:remaining]
		state.frameTruncated = true
	}
	state.probe = append(state.probe, chunk...)
}

// finalizeResponseCapture recovers structured metadata from a retained terminal
// probe or detects an unfinished terminal frame at request end. Nil state skips
// processing; caller-held state ownership protects updates.
//
// Already known terminal evidence remains present even when probe decoding
// fails, so a truncated body cannot erase the failure classification. A new EOF
// probe is considered only when untruncated and recognizable as terminal.
// Successful parsing supplies its details; otherwise the recognized terminal
// frame receives generic upstream failure metadata before middleware chooses
// its logical error status.
func (state *opsCaptureWriterState) finalizeResponseCapture() {
	if state == nil {
		return
	}
	// An earlier event line already proves failure. Improve its details when the
	// final probe parses, and retain existing classification when decoding cannot
	// recover more.
	if state.terminalFound {
		if parsed, ok := parseOpsSSEFailure(state.probe); ok {
			state.terminalError = parsed
		}
		return
	}
	if state.frameTruncated || len(state.probe) == 0 || !isOpsTerminalSSEFrame(state.probe) {
		return
	}
	state.sseCapturing = true
	state.appendCapturedResponse(state.probe)
	state.terminalError, state.terminalFound = parseOpsSSEFailure(state.probe)
	// Recognized terminal shape survives an unsuccessful metadata parse, so a body
	// ending before complete JSON still receives generic upstream failure
	// attribution.
	if !state.terminalFound {
		state.terminalError = parsedOpsError{ErrorType: "upstream_error", StreamFailure: true}
		state.terminalFound = true
	}
}

// appendCapturedResponse appends attempted bytes within the current body limit.
// Overflow retains only the remaining prefix, and exhausted storage makes no
// change; caller-held state ownership keeps buffer mutation serialized with
// other capture operations.
func (state *opsCaptureWriterState) appendCapturedResponse(chunk []byte) {
	remaining := state.limit - state.buf.Len()
	if remaining <= 0 {
		return
	}
	// Retain only the remaining diagnostic prefix; terminal metadata has a
	// separate probe and can survive after this body buffer reaches its budget.
	if len(chunk) > remaining {
		chunk = chunk[:remaining]
	}
	_, _ = state.buf.Write(chunk)
}

// shouldCapture checks whether current request rejection permits new body
// capture. Missing context or predicate allows capture; a present predicate
// runs under the caller's capture ownership and suppresses copying when it
// reports ingress rejection.
func (state *opsCaptureWriterState) shouldCapture() bool {
	if state.ctx == nil {
		return true
	}
	return state.rejected == nil || !state.rejected(state.ctx)
}

// OpsErrorLoggerMiddleware observes bounded attempted output and records final,
// in-band or recovered upstream failures. It wraps a valid request writer
// through c.Next and restores the original writer when its own wrapper is still
// installed before releasing capture state. A queue is required when monitoring
// admits an entry.
//
// Terminal SSE evidence can replace an earlier attempt error and infer logical
// failure status despite successful HTTP commitment. Telemetry failure marking
// occurs before ingress rejection and monitoring checks. Rejected ingress,
// disabled monitoring, dedicated recording, rule suppression and configured
// filters can stop normal error queueing. Successful recovered attempts retain
// their wire status as diagnostics; marked stream failures use their own
// severity/SLA rules. Metadata is captured synchronously before Enqueue, whose
// implementation can retain and prepare the mutable entry after submission.
func OpsErrorLoggerMiddleware(ops *opscore.OpsService, queue OpsErrorLogQueue, access OpsObservationAccess) gin.HandlerFunc {
	return func(c *gin.Context) {
		originalWriter := c.Writer
		w := acquireOpsCaptureWriter(originalWriter)
		w.setContext(c, access.Rejected)
		defer func() {
			// Restore the original writer so outer middleware cannot access a wrapper
			// whose pooled state lease has been released.
			if c.Writer == w {
				c.Writer = originalWriter
			}
			releaseOpsCaptureWriter(w)
		}()
		c.Writer = w
		c.Next()
		w.finalizeCapture()

		captured := parseOpsErrorResponse(w.capturedBytes())
		if terminal, ok := w.capturedTerminalError(); ok {
			captured = terminal
		}
		// Mark request failure telemetry from terminal evidence before monitoring and
		// ingress filters, so filtering an Ops entry does not erase request outcome
		// observation.
		if captured.StreamFailure {
			telemetry.UpdateRequest(c.Request.Context(), func(record *telemetry.RequestRecord) {
				record.State = "failed"
				record.ErrorCode = "stream_error"
				record.ErrorCode = "stream_error"
			})
		}
		if access.rejected(c) {
			return
		}

		if ops == nil {
			return
		}
		if !ops.IsMonitoringEnabled(c.Request.Context()) {
			return
		}
		if c.GetBool(opsDedicatedErrorRecordedKey) {
			return
		}

		status := c.Writer.Status()
		body := w.capturedBytes()
		parsed := parseOpsErrorResponse(body)
		if !parsed.StreamFailure {
			if terminal, ok := w.capturedTerminalError(); ok {
				parsed = terminal
			}
		}
		if status < 400 {
			if parsed.StreamFailure {
				status = inferStreamFailureStatus(c, parsed)
			} else {
				// Record marked stream errors even after HTTP 200 commitment. Recovered retries and switches
				// retain their successful wire status as upstream health diagnostics.
				if len(GetOpsStreamErrors(c)) > 0 {
					logOpsStreamError(c, ops, status, queue, access)
				} else {
					logOpsRecoveredUpstream(c, ops, status, queue, access)
				}
				return
			}
		}

		// Final rule evidence decides whether this failure enters normal monitoring;
		// an intermediate recovered attempt alone cannot hide a later visible error.
		if shouldSkipFinalOpsFailure(c) {
			return
		}

		// Apply the current diagnostic filters after final failure selection, so
		// disabled categories skip this queue while request telemetry remains recorded.
		if shouldSkipOpsErrorLog(c.Request.Context(), ops, parsed.Message, string(body), c.Request.URL.Path) {
			return
		}

		apiKey := access.key(c)

		clientRequestID, _ := c.Request.Context().Value(telemetry.ClientRequestID).(string)

		model, _ := c.Get(OpsModelKey)
		streamV, _ := c.Get(OpsStreamKey)
		providerIDV, _ := c.Get(OpsProviderIDKey)

		var modelName string
		if s, ok := model.(string); ok {
			modelName = s
		}
		stream := false
		if b, ok := streamV.(bool); ok {
			stream = b
		}
		var providerID *int64
		if v, ok := providerIDV.(int64); ok && v > 0 {
			providerID = &v
		}

		fallbackPlatform := selectedOpsPlatform(c)
		platform := resolveOpsPlatform(apiKey, fallbackPlatform)

		requestID, _ := c.Request.Context().Value(telemetry.RequestID).(string)
		requestID = strings.TrimSpace(requestID)
		// When the platform request context has no ID, use the response header
		// already attached to this request so its queued error remains correlatable.
		if requestID == "" {
			requestID = c.Writer.Header().Get("X-Request-Id")
			if requestID == "" {
				requestID = c.Writer.Header().Get("x-request-id")
			}
		}

		normalizedType := opscore.NormalizeErrorType(parsed.ErrorType, parsed.Code, parsed.Message)

		phase, isBusinessLimited, errorOwner, errorSource := classifyOpsErrorLog(c, normalizedType, parsed.Message, parsed.Code, status)

		entry := &opscore.OpsInsertErrorLogInput{
			RequestID:       requestID,
			ClientRequestID: clientRequestID,

			ProviderID: providerID,
			Platform:   platform,
			Model:      modelName,
			RequestPath: func() string {
				if c.Request != nil && c.Request.URL != nil {
					return c.Request.URL.Path
				}
				return ""
			}(),
			Stream:           stream,
			InboundEndpoint:  GetInboundEndpoint(c),
			UpstreamEndpoint: GetUpstreamEndpoint(c, platform),
			RequestedModel:   modelName,
			UpstreamModel: func() string {
				if v, ok := c.Get(opsUpstreamModelKey); ok {
					if s, ok := v.(string); ok {
						return strings.TrimSpace(s)
					}
				}
				return ""
			}(),
			RequestType: func() *int16 {
				if v, ok := c.Get(opsRequestTypeKey); ok {
					switch t := v.(type) {
					case int16:
						return &t
					case int:
						// Persisted request modes use int16; producers supply valid enum values.
						v16 := int16(t)
						return &v16
					}
				}
				return nil
			}(),
			UserAgent: c.GetHeader("User-Agent"),

			ErrorPhase:        phase,
			ErrorType:         normalizedType,
			Severity:          opscore.ClassifySeverity(normalizedType, status),
			StatusCode:        status,
			IsBusinessLimited: isBusinessLimited,
			IsCountTokens:     isCountTokensRequest(c),

			ErrorMessage: parsed.Message,
			// Join and sanitize each SSE JSON payload before asynchronous body preparation,
			// including bounded fragments whose incomplete JSON can still contain credential text.
			ErrorBody:   sanitizeOpsSSEDataForPersistence(body),
			ErrorSource: errorSource,
			ErrorOwner:  errorOwner,

			CreatedAt: time.Now(),
		}
		applyOpsLatencyFieldsFromContext(c, entry)
		applyOpsUpstreamFieldsFromContext(c, entry)
		// Terminal failure details replace previous attempt status and message after
		// context attribution, preventing an earlier recovered error from becoming
		// this final stream summary.
		if parsed.StreamFailure {
			if message := strings.TrimSpace(parsed.Message); message != "" {
				entry.UpstreamErrorMessage = &message
			}
			if status >= 400 {
				finalStatus := status
				entry.UpstreamStatusCode = &finalStatus
			}
		}
		suppressOpsUpstreamAttributionForLocalModelConfiguration(c, entry)

		if apiKey != nil {
			entry.APIKeyID = &apiKey.ID
			// Snapshot the key prefix so it remains available after the key is deleted.
			entry.APIKeyPrefix = keyPrefix(apiKey.Key, 8)
			if apiKey.User != nil {
				entry.UserID = &apiKey.User.ID
			}
			if apiKey.GroupID != nil {
				entry.GroupID = apiKey.GroupID
			}
		}

		var clientIP string
		if ip := strings.TrimSpace(clientip.GetClientIP(c)); ip != "" {
			clientIP = ip
			entry.ClientIP = &clientIP
		}

		// Submit after synchronous attribution is complete; the queue can retain and
		// sanitize this mutable entry, so the request stops editing it after
		// submission.
		queue.Enqueue(ops, entry)
	}
}

// logOpsRecoveredUpstream queues visible upstream diagnostics while retaining a
// successful final wire status. Missing context/service or an error final
// status is ignored. Hidden attempt events are filtered before choosing the
// last remaining event for attribution.
//
// A final provider-auth event changes the diagnostic phase, while usage_invalid
// receives its own missing-usage message. No remaining upstream evidence
// produces no entry. Request and Key metadata are captured before submission,
// so successful client delivery stays separate from prior attempt health
// evidence and cannot become an SLA failure merely because a retry was needed.
func logOpsRecoveredUpstream(c *gin.Context, ops *opscore.OpsService, finalStatus int, queue OpsErrorLogQueue, access OpsObservationAccess) {
	if c == nil || ops == nil || finalStatus >= 400 {
		return
	}

	entry := &opscore.OpsInsertErrorLogInput{StatusCode: finalStatus}
	applyOpsUpstreamFieldsFromContext(c, entry)
	// Filter hidden attempts before selecting recovered attribution, so an
	// excluded final attempt does not override the last visible diagnostic event.
	if len(entry.UpstreamErrors) > 0 {
		visibleEvents := make([]*opscore.OpsUpstreamErrorEvent, 0, len(entry.UpstreamErrors))
		for _, event := range entry.UpstreamErrors {
			if event != nil && !event.SkipMonitoring {
				visibleEvents = append(visibleEvents, event)
			}
		}
		if len(visibleEvents) == 0 {
			return
		}
		applyOpsUpstreamErrorEvents(entry, visibleEvents)
	}
	if entry.UpstreamStatusCode == nil && entry.UpstreamErrorMessage == nil &&
		entry.UpstreamErrorDetail == nil && len(entry.UpstreamErrors) == 0 {
		return
	}

	lastStatus := 0
	if entry.UpstreamStatusCode != nil {
		lastStatus = *entry.UpstreamStatusCode
	}
	lastStage := ""
	lastKind := ""
	for _, event := range slices.Backward(entry.UpstreamErrors) {
		if event != nil {
			lastStage = event.Stage
			lastKind = event.Kind
			if event.ProviderID > 0 {
				providerID := event.ProviderID
				entry.ProviderID = &providerID
			}
			break
		}
	}
	// If no visible final event supplied provider identity, use the
	// selected-provider marker for this request while retaining its successful
	// wire status.
	if entry.ProviderID == nil {
		if providerID, ok := c.Get(OpsProviderIDKey); ok {
			if value, ok := providerID.(int64); ok && value > 0 {
				entry.ProviderID = &value
			}
		}
	}

	entry.ErrorPhase = "upstream"
	entry.ErrorType = "upstream_error"
	entry.ErrorSource = "upstream_http"
	entry.ErrorOwner = "provider"
	entry.Severity = opscore.ClassifySeverity(entry.ErrorType, lastStatus)
	entry.IsCountTokens = isCountTokensRequest(c)
	entry.CreatedAt = time.Now()
	entry.ErrorMessage = "Recovered upstream error"
	if lastKind == "usage_invalid" {
		entry.ErrorMessage = "SystemOne usage is missing or invalid; billing skipped"
	}
	if lastKind != "usage_invalid" && lastStage == opscore.ErrorPhaseProviderAuth {
		entry.ErrorPhase = opscore.ErrorPhaseProviderAuth
		entry.ErrorMessage = "Recovered provider authentication failure"
	} else if lastKind != "usage_invalid" && lastStatus > 0 {
		entry.ErrorMessage += " " + strconv.Itoa(lastStatus)
	}
	if lastKind != "usage_invalid" && entry.UpstreamErrorMessage != nil && strings.TrimSpace(*entry.UpstreamErrorMessage) != "" {
		entry.ErrorMessage += ": " + strings.TrimSpace(*entry.UpstreamErrorMessage)
	}
	// Bound recovered summary bytes independently of the retained attempt list,
	// keeping the diagnostic message within its persistence limit.
	entry.ErrorMessage = truncateString(entry.ErrorMessage, 2048)

	if c.Request != nil {
		entry.UserAgent = c.GetHeader("User-Agent")
		if c.Request.URL != nil {
			entry.RequestPath = c.Request.URL.Path
		}
		if c.Request.Context() != nil {
			entry.ClientRequestID, _ = c.Request.Context().Value(telemetry.ClientRequestID).(string)
			entry.RequestID, _ = c.Request.Context().Value(telemetry.RequestID).(string)
		}
	}
	entry.RequestID = strings.TrimSpace(entry.RequestID)
	// Use the request response header when telemetry has no platform ID, keeping
	// recovered diagnostics attached to the same successful client exchange.
	if entry.RequestID == "" {
		entry.RequestID = c.Writer.Header().Get("X-Request-Id")
	}
	entry.Model = c.GetString(OpsModelKey)
	entry.RequestedModel = entry.Model
	entry.Stream = c.GetBool(OpsStreamKey)
	entry.InboundEndpoint = GetInboundEndpoint(c)
	entry.UpstreamModel = c.GetString(opsUpstreamModelKey)
	entry.RequestType = opsRequestTypeFromContext(c)

	apiKey := access.key(c)
	fallbackPlatform := selectedOpsPlatform(c)
	entry.Platform = resolveOpsPlatform(apiKey, fallbackPlatform)
	entry.UpstreamEndpoint = GetUpstreamEndpoint(c, entry.Platform)
	if apiKey != nil {
		entry.APIKeyID = &apiKey.ID
		entry.APIKeyPrefix = keyPrefix(apiKey.Key, 8)
		if apiKey.User != nil {
			entry.UserID = &apiKey.User.ID
		}
		if apiKey.GroupID != nil {
			entry.GroupID = apiKey.GroupID
		}
	}
	if clientIP := strings.TrimSpace(clientip.GetClientIP(c)); clientIP != "" {
		entry.ClientIP = &clientIP
	}
	applyOpsLatencyFieldsFromContext(c, entry)
	// Finish request and attempt attribution before queue submission; retained
	// mutable input is subsequently prepared by the queue and no longer edited
	// here.
	queue.Enqueue(ops, entry)
}

// opsRequestTypeFromContext copies a stored int16 request type or casts a
// stored int into new int16 pointer storage. Missing or other types return nil;
// the int conversion performs no range validation, leaving admissible request
// types with their producer.
func opsRequestTypeFromContext(c *gin.Context) *int16 {
	if c == nil {
		return nil
	}
	if value, ok := c.Get(opsRequestTypeKey); ok {
		switch typed := value.(type) {
		case int16:
			result := typed
			return &result
		case int:
			result := int16(typed)
			return &result
		}
	}
	return nil
}

// logOpsStreamError processes each retained in-band failure through the stream
// logging rule. Call with valid request/service state after wire commitment;
// each captured turn carries its own monitoring decision and may select
// intended status for SLA accounting.
func logOpsStreamError(c *gin.Context, ops *opscore.OpsService, wireStatus int, queue OpsErrorLogQueue, access OpsObservationAccess) {
	for _, streamErr := range GetOpsStreamErrors(c) {
		logOpsStreamErrorValue(c, ops, wireStatus, streamErr, queue, access)
	}
}

// logOpsStreamErrorValue queues one retained stream failure after suppression
// and advanced filtering. Intended status determines classification when
// positive; wire status supplies the fallback. Ordinary errors retain wire
// status, while SLA failures can record their intended error status.
//
// Current context supplies request metadata, then a positive captured turn
// replaces provider/upstream fields with its snapshot. This prevents later
// WebSocket state from rewriting the failure's attempt attribution. Key
// identity and client IP are captured before enqueue, and the queued entry
// remains mutable input owned by queue preparation after submission. A missing
// explicit error code can leave its synthetic ErrorBody empty.
func logOpsStreamErrorValue(c *gin.Context, ops *opscore.OpsService, wireStatus int, streamErr OpsStreamError, queue OpsErrorLogQueue, access OpsObservationAccess) {
	// A captured WS turn uses its own suppression decision; ordinary HTTP
	// markers can still read final request evidence before this failure is queued.
	if streamErr.SkipMonitoring || (streamErr.Turn == 0 && shouldSkipFinalOpsFailure(c)) {
		return
	}

	// Evaluate diagnostic filters against this visible stream failure so
	// category suppression applies independently of the already committed wire status.
	if shouldSkipOpsErrorLog(c.Request.Context(), ops, streamErr.Message, streamErr.Message, c.Request.URL.Path) {
		return
	}

	// Use intended status for failure severity when present, keeping ordinary
	// markers at their wire status while SLA-counted markers can record a logical error.
	classifyStatus := streamErr.IntendedStatus
	if classifyStatus <= 0 {
		classifyStatus = wireStatus
	}
	normalizedType := opscore.NormalizeErrorType(streamErr.ErrType, streamErr.Code, streamErr.Message)
	phase, isBusinessLimited, errorOwner, errorSource := classifyOpsErrorLog(c, normalizedType, streamErr.Message, streamErr.Code, classifyStatus)
	recordedStatus := wireStatus
	if streamErr.CountTowardsSLA && streamErr.IntendedStatus >= 400 {
		recordedStatus = streamErr.IntendedStatus
	}
	errorBody := ""
	// An explicit code enables a structured diagnostic body. If serialization
	// cannot produce it, the message and classification fields still remain
	// available for queueing.
	if streamErr.Code != "" {
		if payload, err := json.Marshal(gin.H{"error": gin.H{
			"type": normalizedType, "code": streamErr.Code, "message": streamErr.Message,
		}}); err == nil {
			errorBody = string(payload)
		}
	}

	apiKey := access.key(c)
	clientRequestID, _ := c.Request.Context().Value(telemetry.ClientRequestID).(string)

	model, _ := c.Get(OpsModelKey)
	var modelName string
	if s, ok := model.(string); ok {
		modelName = s
	}
	providerIDV, _ := c.Get(OpsProviderIDKey)
	var providerID *int64
	if v, ok := providerIDV.(int64); ok && v > 0 {
		providerID = &v
	}

	fallbackPlatform := selectedOpsPlatform(c)
	platform := resolveOpsPlatform(apiKey, fallbackPlatform)

	requestID, _ := c.Request.Context().Value(telemetry.RequestID).(string)
	requestID = strings.TrimSpace(requestID)
	// Use the response request-ID header when context correlation is absent,
	// keeping an in-band failure linked to its already committed HTTP exchange.
	if requestID == "" {
		requestID = c.Writer.Header().Get("X-Request-Id")
		if requestID == "" {
			requestID = c.Writer.Header().Get("x-request-id")
		}
	}

	entry := &opscore.OpsInsertErrorLogInput{
		RequestID:       requestID,
		ClientRequestID: clientRequestID,

		ProviderID: providerID,
		Platform:   platform,
		Model:      modelName,
		RequestPath: func() string {
			if c.Request != nil && c.Request.URL != nil {
				return c.Request.URL.Path
			}
			return ""
		}(),
		// In-band errors belong to streaming requests.
		Stream:           true,
		InboundEndpoint:  GetInboundEndpoint(c),
		UpstreamEndpoint: GetUpstreamEndpoint(c, platform),
		RequestedModel:   modelName,
		UpstreamModel: func() string {
			if v, ok := c.Get(opsUpstreamModelKey); ok {
				if s, ok := v.(string); ok {
					return strings.TrimSpace(s)
				}
			}
			return ""
		}(),
		RequestType: func() *int16 {
			if v, ok := c.Get(opsRequestTypeKey); ok {
				switch t := v.(type) {
				case int16:
					return &t
				case int:
					// Persisted request modes use int16; producers supply valid enum values.
					v16 := int16(t)
					return &v16
				}
			}
			return nil
		}(),
		UserAgent: c.GetHeader("User-Agent"),

		ErrorPhase:        phase,
		ErrorType:         normalizedType,
		Severity:          opscore.ClassifySeverity(normalizedType, classifyStatus),
		StatusCode:        recordedStatus,
		IsBusinessLimited: isBusinessLimited,
		IsCountTokens:     isCountTokensRequest(c),

		ErrorMessage: streamErr.Message,
		ErrorBody:    errorBody,
		ErrorSource:  errorSource,
		ErrorOwner:   errorOwner,

		CreatedAt: time.Now(),
	}
	applyOpsLatencyFieldsFromContext(c, entry)
	applyOpsUpstreamFieldsFromContext(c, entry)
	// Replace live attempt attribution with this captured WS turn, so subsequent
	// provider selection cannot rewrite an earlier failure being logged at
	// connection end.
	if streamErr.Turn > 0 {
		applyOpsStreamErrorSnapshot(entry, streamErr)
	}

	if apiKey != nil {
		entry.APIKeyID = &apiKey.ID
		entry.APIKeyPrefix = keyPrefix(apiKey.Key, 8)
		if apiKey.User != nil {
			entry.UserID = &apiKey.User.ID
		}
		if apiKey.GroupID != nil {
			entry.GroupID = apiKey.GroupID
		}
	}

	if clientIP := strings.TrimSpace(clientip.GetClientIP(c)); clientIP != "" {
		entry.ClientIP = &clientIP
	}

	// Submit the completed stream observation for queue preparation after
	// attribution; the mutable entry may be retained and the request stops
	// changing it here.
	queue.Enqueue(ops, entry)
}

// applyOpsStreamErrorSnapshot replaces an entry's upstream fields from a
// captured turn. Nil entries are ignored; positive snapshot provider identity
// replaces its ID, while absent snapshot status/message/detail clears those
// optional upstream fields.
//
// The last nonnil event determines provider-auth precedence; other positive
// status or event-list evidence marks upstream provider ownership. The event
// slice is retained by reference until queue preparation serializes it.
// Snapshot model text is trimmed and replaces current context attribution,
// keeping later turn observations from filling missing evidence in this earlier
// failure.
func applyOpsStreamErrorSnapshot(entry *opscore.OpsInsertErrorLogInput, streamErr OpsStreamError) {
	if entry == nil {
		return
	}
	if streamErr.ProviderID > 0 {
		providerID := streamErr.ProviderID
		entry.ProviderID = &providerID
	}
	// Replace optional upstream fields as one captured-turn sequence, clearing
	// absent values so later live context cannot fill evidence missing from this
	// earlier failure.
	entry.UpstreamModel = strings.TrimSpace(streamErr.UpstreamModel)
	entry.UpstreamStatusCode = nil
	if streamErr.UpstreamStatus > 0 {
		status := streamErr.UpstreamStatus
		entry.UpstreamStatusCode = &status
	}
	entry.UpstreamErrorMessage = nil
	if message := strings.TrimSpace(streamErr.UpstreamMessage); message != "" {
		entry.UpstreamErrorMessage = &message
	}
	entry.UpstreamErrorDetail = nil
	if detail := strings.TrimSpace(streamErr.UpstreamDetail); detail != "" {
		entry.UpstreamErrorDetail = &detail
	}
	entry.UpstreamErrors = streamErr.UpstreamErrors
	lastStage := ""
	for _, v := range slices.Backward(streamErr.UpstreamErrors) {
		if v != nil {
			lastStage = v.Stage
			break
		}
	}
	if lastStage == opscore.ErrorPhaseProviderAuth {
		entry.ErrorPhase = opscore.ErrorPhaseProviderAuth
		entry.ErrorOwner = "provider"
		entry.ErrorSource = "gateway"
		entry.IsBusinessLimited = false
	} else if streamErr.UpstreamStatus > 0 || len(streamErr.UpstreamErrors) > 0 {
		entry.ErrorPhase = "upstream"
		entry.ErrorOwner = "provider"
		entry.ErrorSource = "upstream_http"
		entry.IsBusinessLimited = false
	}
}

// shouldSkipFinalOpsFailure selects suppression from the request flag and final
// attempt. Nil context or no usable evidence returns false. A true typed
// request flag wins; absent or false flags defer to the last nonnil event.
//
// Earlier attempt decisions can differ from the visible failure after retry or
// provider switching. When the request flag has not already selected
// suppression, the final event prevents an earlier event's skip flag from hiding
// this later failure. A true request flag remains authoritative over all event
// decisions.
func shouldSkipFinalOpsFailure(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if v, ok := c.Get(OpsSkipPassthroughKey); ok {
		if skip, _ := v.(bool); skip {
			return true
		}
	}
	if v, ok := c.Get(OpsUpstreamErrorsKey); ok {
		if events, ok := v.([]*opscore.OpsUpstreamErrorEvent); ok {
			for _, event := range slices.Backward(events) {
				if event != nil {
					return event.SkipMonitoring
				}
			}
		}
	}
	return false
}

// isCountTokensRequest recognizes count_tokens paths and validated Responses
// input-token preflight. Missing request paths return false; path counting is
// independent from the advanced IgnoreCountTokensErrors filter, whose current
// matching rule handles count_tokens text only.
func isCountTokensRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	return strings.Contains(c.Request.URL.Path, "/count_tokens") ||
		IsOpenAIResponsesInputTokensRequestPath(c)
}

// applyOpsLatencyFieldsFromContext fills optional latency pointers using
// supported stored numeric values. Missing context or entry is ignored; each
// absent or unusable stage observation becomes nil independently, leaving other
// measured stages available for final diagnostics.
func applyOpsLatencyFieldsFromContext(c *gin.Context, entry *opscore.OpsInsertErrorLogInput) {
	if c == nil || entry == nil {
		return
	}
	entry.AuthLatencyMs = getContextLatencyMs(c, OpsAuthLatencyMsKey)
	entry.RoutingLatencyMs = getContextLatencyMs(c, OpsRoutingLatencyMsKey)
	entry.UpstreamLatencyMs = getContextLatencyMs(c, OpsUpstreamLatencyMsKey)
	entry.ResponseLatencyMs = getContextLatencyMs(c, OpsResponseLatencyMsKey)
	entry.TimeToFirstTokenMs = getContextLatencyMs(c, OpsTimeToFirstTokenMsKey)
}

// applyOpsUpstreamFieldsFromContext copies optional context status, message and
// detail into an entry. A nonempty typed attempt list then takes precedence
// through applyOpsUpstreamErrorEvents; nil context/entry is ignored, and the
// event list stays shared until queue preparation.
func applyOpsUpstreamFieldsFromContext(c *gin.Context, entry *opscore.OpsInsertErrorLogInput) {
	if c == nil || entry == nil {
		return
	}
	if v, ok := c.Get(OpsUpstreamStatusCodeKey); ok {
		switch t := v.(type) {
		case int:
			if t > 0 {
				code := t
				entry.UpstreamStatusCode = &code
			}
		case int64:
			if t > 0 {
				code := int(t)
				entry.UpstreamStatusCode = &code
			}
		}
	}
	if v, ok := c.Get(OpsUpstreamErrorMessageKey); ok {
		if value, ok := v.(string); ok {
			if message := strings.TrimSpace(value); message != "" {
				entry.UpstreamErrorMessage = &message
			}
		}
	}
	if v, ok := c.Get(OpsUpstreamErrorDetailKey); ok {
		if value, ok := v.(string); ok {
			if detail := strings.TrimSpace(value); detail != "" {
				entry.UpstreamErrorDetail = &detail
			}
		}
	}
	if v, ok := c.Get(OpsUpstreamErrorsKey); ok {
		if events, ok := v.([]*opscore.OpsUpstreamErrorEvent); ok && len(events) > 0 {
			applyOpsUpstreamErrorEvents(entry, events)
		}
	}
}

// applyOpsUpstreamErrorEvents retains the supplied event list and selects its
// last nonnil observation for top-level upstream fields. The entry is required.
// An all-nil list keeps existing top-level values while still replacing the
// retained list.
//
// A chosen final observation first clears older status, message and detail.
// Provider-auth uses an explicit zero status even when prior inference failed;
// other positive statuses become the final value, and blank strings remain
// absent. Earlier event evidence remains in the list, so the top-level summary
// can represent a statusless final transport/auth failure without borrowing an
// older inference response.
func applyOpsUpstreamErrorEvents(entry *opscore.OpsInsertErrorLogInput, events []*opscore.OpsUpstreamErrorEvent) {
	entry.UpstreamErrors = events
	var last *opscore.OpsUpstreamErrorEvent
	for _, event := range slices.Backward(events) {
		if event != nil {
			last = event
			break
		}
	}
	if last == nil {
		return
	}

	// Clear the prior summary before applying the last nonnil event; statusless
	// transport or auth failures must not inherit an earlier inference HTTP
	// response.
	entry.UpstreamStatusCode = nil
	entry.UpstreamErrorMessage = nil
	entry.UpstreamErrorDetail = nil
	if last.Stage == opscore.ErrorPhaseProviderAuth {
		code := 0
		entry.UpstreamStatusCode = &code
	} else if last.UpstreamStatusCode > 0 {
		code := last.UpstreamStatusCode
		entry.UpstreamStatusCode = &code
	}
	if message := strings.TrimSpace(last.Message); message != "" {
		entry.UpstreamErrorMessage = &message
	}
	if detail := strings.TrimSpace(last.Detail); detail != "" {
		entry.UpstreamErrorDetail = &detail
	}
}

// suppressOpsUpstreamAttributionForLocalModelConfiguration removes provider and
// upstream evidence when both the local business marker and model-configuration
// reason apply. Missing entries or incomplete markers leave attribution intact;
// clearing includes any retained prior attempt list in this final entry.
func suppressOpsUpstreamAttributionForLocalModelConfiguration(c *gin.Context, entry *opscore.OpsInsertErrorLogInput) {
	if entry == nil || !HasOpsClientBusinessLimited(c) || OpsClientBusinessLimitedReason(c) != OpsClientBusinessLimitedReasonLocalModelConfiguration {
		return
	}
	// A marked local model-configuration failure has no selected execution owner;
	// discard stale attempt fields together so queue diagnostics cannot blame an
	// earlier provider.
	entry.ProviderID = nil
	entry.UpstreamEndpoint = ""
	entry.UpstreamModel = ""
	entry.UpstreamStatusCode = nil
	entry.UpstreamErrorMessage = nil
	entry.UpstreamErrorDetail = nil
	entry.UpstreamErrors = nil
}

// getContextLatencyMs converts supported stored numeric values to an int64
// millisecond pointer. Missing keys, other types and negative converted values
// return nil. Float fractions truncate before that sign check, so a negative
// fraction can become accepted zero.
func getContextLatencyMs(c *gin.Context, key string) *int64 {
	if c == nil || strings.TrimSpace(key) == "" {
		return nil
	}
	v, ok := c.Get(key)
	if !ok {
		return nil
	}
	var ms int64
	switch t := v.(type) {
	case int:
		ms = int64(t)
	case int32:
		ms = int64(t)
	case int64:
		ms = t
	// Stored float observations truncate fractional milliseconds before
	// negative-value validation.
	case float64:
		ms = int64(t)
	default:
		return nil
	}
	if ms < 0 {
		return nil
	}
	return &ms
}

// parseOpsErrorResponse extracts terminal SSE failure before generic JSON error
// shapes. Empty bodies return empty metadata. Supported JSON forms include
// nested error objects, string-valued errors and top-level code/message fields,
// with their available category and scalar code retained.
//
// A terminal event can arrive after the HTTP status is committed, so its
// structured evidence takes precedence over treating the whole stream as an
// opaque message. Undecodable or unrecognized bodies retain a bounded
// diagnostic prefix, allowing final attribution even when the upstream response
// uses another error shape.
func parseOpsErrorResponse(body []byte) parsedOpsError {
	if len(body) == 0 {
		return parsedOpsError{}
	}
	if parsed, ok := parseOpsSSEFailure(body); ok {
		return parsed
	}

	// After terminal SSE recognition fails, generic JSON decoding supports gateway
	// error shapes; undecodable bodies retain a bounded diagnostic prefix for attribution.
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return parsedOpsError{Message: truncateString(string(body), 1024)}
	}

	// Claude/OpenAI-style gateway error: { type:"error", error:{ type, message } }
	if errObj, ok := m["error"].(map[string]any); ok {
		t, _ := errObj["type"].(string)
		msg, _ := errObj["message"].(string)
		if t == "" {
			t = "api_error"
		}
		code := opsJSONScalarString(errObj["code"])
		return parsedOpsError{ErrorType: t, Message: msg, Code: code}
	}
	if errMessage, ok := m["error"].(string); ok && strings.TrimSpace(errMessage) != "" {
		t, _ := m["type"].(string)
		if t == "" || t == "error" {
			t = "api_error"
		}
		return parsedOpsError{ErrorType: t, Message: strings.TrimSpace(errMessage), Code: opsJSONScalarString(m["code"])}
	}

	// APIKeyAuth-style: { code:"INSUFFICIENT_BALANCE", message:"..." }
	code := opsJSONScalarString(m["code"])
	msg, _ := m["message"].(string)
	if code != "" || msg != "" {
		t, _ := m["type"].(string)
		if t == "" || t == "error" {
			t = "api_error"
		}
		return parsedOpsError{ErrorType: t, Message: msg, Code: code}
	}

	// Unrecognized JSON still supplies a bounded body prefix, allowing diagnostic
	// capture when the upstream error uses another response shape.
	return parsedOpsError{Message: truncateString(string(body), 1024)}
}

// opsJSONScalarString converts supported string and numeric error codes to
// text. Strings and json.Number are trimmed, float64 values convert through int
// and lose fractions, and unsupported values yield empty code evidence.
func opsJSONScalarString(value any) string {
	switch value := value.(type) {
	case string:
		return strings.TrimSpace(value)
	// JSON numeric codes use integer text, discarding any fraction.
	case float64:
		return strconv.Itoa(int(value))
	case json.Number:
		return strings.TrimSpace(value.String())
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	default:
		return ""
	}
}

// opsJSONInt converts supported status values into int observations with no
// range validation. Text parsing errors are discarded: invalid syntax yields
// zero, while range errors can retain a clamped integer. Numeric casts can
// truncate fractions or overflow their target representation; terminal status
// inference validates usable failure statuses separately.
func opsJSONInt(value any) int {
	switch value := value.(type) {
	case float64:
		return int(value)
	// Discard parsing errors here because terminal inference separately checks
	// usable statuses and can choose recognized code or type when status evidence
	// is zero.
	case json.Number:
		parsed, _ := strconv.Atoi(value.String())
		return parsed
	// String status parsing also discards its error result; inference uses the
	// returned integer only when it is a usable HTTP failure status.
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(value))
		return parsed
	case int:
		return value
	case int64:
		return int(value)
	default:
		return 0
	}
}

// parseOpsSSEFailure extracts logical failure from normalized LF, CRLF or CR
// frames. It accepts event-line terminal names or decoded payload types, joins
// multiline data and recognizes nested or flattened error fields.
//
// The first response.failed frame wins immediately, while ordinary error frames
// retain the last candidate if no Responses terminal appears. Explicit status
// fields take their nested-to-top-level precedence: zero permits the next
// source, while a nonzero out-of-range value blocks later status sources and
// proceeds to code/type inference. Known codes can infer type when absent. JSON-looking payloads without usable messages receive generic
// safe text, while other payload text can provide a bounded message. The
// returned boolean confirms terminal evidence even when truncated or invalid
// JSON prevented richer details.
func parseOpsSSEFailure(body []byte) (parsedOpsError, bool) {
	// Normalize supported line endings before frame splitting so CR-only and CRLF
	// upstreams share the same event/data extraction and terminal precedence.
	normalized := strings.ReplaceAll(string(body), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	var errorCandidate *parsedOpsError
	for frame := range strings.SplitSeq(normalized, "\n\n") {
		frame = strings.TrimSpace(frame)
		if frame == "" {
			continue
		}
		eventTypeBytes, payloadBytes := parseOpsSSEFrameEnvelope([]byte(frame))
		eventType := string(eventTypeBytes)
		if eventType != "response.failed" && eventType != "error" && len(payloadBytes) == 0 {
			continue
		}

		payload := string(payloadBytes)
		var event map[string]any
		if err := json.Unmarshal(payloadBytes, &event); err == nil {
			if eventType == "" {
				eventType, _ = event["type"].(string)
			}
		}
		if eventType != "response.failed" && eventType != "error" {
			continue
		}

		// Recognized terminal event metadata remains evidence when JSON is
		// incomplete; begin with its event-specific category before extracting any
		// available payload details.
		parsed := parsedOpsError{ErrorType: "upstream_error", StreamFailure: true}
		if eventType == "error" {
			parsed.ErrorType = "api_error"
		}
		errObj := opsSSEErrorObject(event)
		if errObj == nil && event != nil && (event["message"] != nil || event["code"] != nil) {
			errObj = event
		}
		if errObj != nil {
			parsed.ErrorType, _ = errObj["type"].(string)
			if parsed.ErrorType == "error" || parsed.ErrorType == "response.failed" {
				parsed.ErrorType = ""
			}
			parsed.Message, _ = errObj["message"].(string)
			switch code := errObj["code"].(type) {
			case string:
				parsed.Code = strings.TrimSpace(code)
			case float64:
				parsed.Code = strconv.Itoa(int(code))
			}
			// Resolve nested status_code, nested status, then top-level alternatives.
			// Only zero advances to the next source; nonzero malformed-range evidence
			// remains for inferStreamFailureStatus to validate against code and type.
			parsed.StatusCode = opsJSONInt(errObj["status_code"])
			if parsed.StatusCode == 0 {
				parsed.StatusCode = opsJSONInt(errObj["status"])
			}
			if parsed.StatusCode == 0 {
				parsed.StatusCode = opsJSONInt(event["status_code"])
			}
			if parsed.StatusCode == 0 {
				parsed.StatusCode = opsJSONInt(event["status"])
			}
			if parsed.ErrorType == "" {
				parsed.ErrorType = inferResponsesFailedOpsErrorType(parsed.Code)
			}
			if parsed.ErrorType == "" {
				if eventType == "error" {
					parsed.ErrorType = "api_error"
				} else {
					parsed.ErrorType = "upstream_error"
				}
			}
		}
		// A JSON-looking payload without a usable message receives generic text,
		// preventing truncated credential-bearing JSON from becoming the diagnostic
		// message while plaintext retains a bounded prefix.
		if strings.TrimSpace(parsed.Message) == "" && payload != "" {
			trimmedPayload := strings.TrimSpace(payload)
			if strings.HasPrefix(trimmedPayload, "{") || strings.HasPrefix(trimmedPayload, "[") {
				parsed.Message = "upstream stream failed"
			} else {
				parsed.Message = truncateString(trimmedPayload, 1024)
			}
		}
		// A Responses terminal failure wins immediately over ordinary error
		// candidates; otherwise retain the latest generic error frame for the final
		// diagnostic result.
		if eventType == "response.failed" {
			return parsed, true
		}
		candidate := parsed
		errorCandidate = &candidate
	}
	if errorCandidate != nil {
		return *errorCandidate, true
	}
	return parsedOpsError{}, false
}

// opsSSEErrorObject selects direct error, nested response.error, then a
// flattened event typed error. Missing event or unsupported shapes return nil;
// selected maps remain borrowed from the decoded payload for the parser's
// read-only extraction.
func opsSSEErrorObject(event map[string]any) map[string]any {
	if event == nil {
		return nil
	}
	if errObj, ok := event["error"].(map[string]any); ok {
		return errObj
	}
	if response, ok := event["response"].(map[string]any); ok {
		if errObj, ok := response["error"].(map[string]any); ok {
			return errObj
		}
	}
	// A decoded event typed error can carry its code and message directly.
	// Accept that flattened shape when nested error objects are absent, matching data-only terminal streams.
	if eventType, _ := event["type"].(string); eventType == "error" {
		return event
	}
	return nil
}

// sanitizeOpsSSEDataForPersistence normalizes SSE separators and sanitizes
// joined JSON data before queue submission. Empty bodies or bodies lacking the
// literal data byte sequence return unchanged. Once that raw marker appears,
// line endings normalize even when no data field exists; valid JSON payloads
// use the shared credential sanitizer, and JSON-looking fragments become a
// truncation marker.
//
// For a replaced frame, one data line carries the sanitized joined payload and
// later data lines are omitted, while other frame lines stay available. This
// keeps multiline JSON redaction together and prevents a bounded terminal
// capture from persisting an unredacted JSON fragment. Non-JSON data remains as
// supplied here and follows the queue's separate body-preparation rule.
func sanitizeOpsSSEDataForPersistence(body []byte) string {
	if len(body) == 0 || !bytes.Contains(body, []byte("data")) {
		return string(body)
	}
	normalized := bytes.ReplaceAll(body, []byte("\r\n"), []byte{'\n'})
	normalized = bytes.ReplaceAll(normalized, []byte{'\r'}, []byte{'\n'})
	frames := bytes.Split(normalized, []byte("\n\n"))
	var out bytes.Buffer
	out.Grow(len(body))
	for frameIndex, frame := range frames {
		if frameIndex > 0 {
			_, _ = out.WriteString("\n\n")
		}
		_, payload := parseOpsSSEFrameEnvelope(frame)
		trimmedPayload := bytes.TrimSpace(payload)
		replacement := ""
		if json.Valid(trimmedPayload) {
			replacement, _ = opscore.SanitizeOpsErrorBodyForQueue(string(trimmedPayload))
		} else if len(trimmedPayload) > 0 && (trimmedPayload[0] == '{' || trimmedPayload[0] == '[') {
			// Captured terminal frames can be truncated at the queue bound. Never
			// persist a JSON-looking fragment that could contain an unredacted key.
			replacement = `{"payload_truncated":true}`
		}
		if replacement == "" {
			_, _ = out.Write(frame)
			continue
		}
		// Replace all joined data with one sanitized line while retaining other frame
		// lines, keeping multiline JSON fragments from bypassing the complete-payload
		// sanitizer.
		wroteData := false
		emittedLine := false
		for line := range bytes.SplitSeq(frame, []byte{'\n'}) {
			field, _, found := bytes.Cut(line, []byte{':'})
			if found && bytes.Equal(bytes.TrimSpace(field), []byte("data")) {
				if wroteData {
					continue
				}
				line = append([]byte("data: "), replacement...)
				wroteData = true
			}
			if emittedLine {
				_ = out.WriteByte('\n')
			}
			_, _ = out.Write(line)
			emittedLine = true
		}
	}
	return out.String()
}

// inferResponsesFailedOpsErrorType maps recognized trimmed failure codes to Ops
// error categories. Unknown codes yield empty, allowing the parser to choose
// its event-specific default after retaining the original code for diagnostics.
func inferResponsesFailedOpsErrorType(code string) string {
	switch strings.TrimSpace(code) {
	case "rate_limit_exceeded":
		return "rate_limit_error"
	case "permission_denied", "permission_error", "insufficient_permissions", "cyber_policy", "content_policy":
		return "permission_error"
	case "invalid_request", "context_length_exceeded":
		return "invalid_request_error"
	case "server_is_overloaded":
		return "overloaded_error"
	case "service_unavailable", "service_unavailable_error", "server_error":
		return "service_unavailable_error"
	case "authentication_failed":
		return "authentication_error"
	default:
		return ""
	}
}

// inferStreamFailureStatus chooses a logical HTTP failure status from terminal
// evidence. An explicit 400 through 599 status wins, then recognized error
// code, then error type; missing or unknown evidence defaults to 502.
//
// The Gin argument is unused because previous attempt state cannot establish
// the status of this terminal event. A stream may have already committed 200,
// and its latest attempted provider may have a different error status. Reading
// the captured terminal fields alone keeps client-visible failure attribution
// from inheriting those unrelated wire or earlier-attempt observations.
func inferStreamFailureStatus(_ *gin.Context, parsed parsedOpsError) int {
	if parsed.StatusCode >= 400 && parsed.StatusCode <= 599 {
		return parsed.StatusCode
	}
	switch strings.TrimSpace(parsed.Code) {
	case "rate_limit_exceeded":
		return http.StatusTooManyRequests
	case "permission_denied", "permission_error", "insufficient_permissions", "cyber_policy", "content_policy":
		return http.StatusForbidden
	case "invalid_request", "context_length_exceeded":
		return http.StatusBadRequest
	case "server_is_overloaded":
		return http.StatusServiceUnavailable
	case "service_unavailable", "service_unavailable_error", "server_error":
		return http.StatusServiceUnavailable
	case "authentication_failed":
		return http.StatusUnauthorized
	}

	switch strings.TrimSpace(parsed.ErrorType) {
	case "rate_limit_error":
		return http.StatusTooManyRequests
	case "permission_error", "forbidden_error":
		return http.StatusForbidden
	case "authentication_error":
		return http.StatusUnauthorized
	case "invalid_request_error":
		return http.StatusBadRequest
	case "overloaded_error", "service_unavailable_error":
		return http.StatusServiceUnavailable
	}

	// Unknown terminal status, code and type classify as bad gateway, retaining
	// failure accounting even when upstream metadata cannot identify a more
	// specific category.
	return http.StatusBadGateway
}

// selectedOpsPlatform returns nonblank telemetry platform text or unknown when
// absent. It validates trimmed nonemptiness but returns stored spelling
// unchanged, so producers normalize labels before request attribution expects
// canonical platform names.
func selectedOpsPlatform(c *gin.Context) string {
	if c != nil && c.Request != nil {
		if platform, ok := c.Request.Context().Value(telemetry.Platform).(string); ok && strings.TrimSpace(platform) != "" {
			return platform
		}
	}
	return "unknown"
}

// resolveOpsPlatform returns supplied actual platform text when nonblank,
// otherwise unknown. API Key data is unused because selected execution evidence
// determines attribution; accepted spelling remains as supplied after its
// nonempty check.
func resolveOpsPlatform(_ *apikey.APIKey, actual string) string {
	if strings.TrimSpace(actual) == "" {
		return "unknown"
	}
	return actual
}

// classifyOpsErrorLog delegates error ownership and phase using local
// restriction and upstream context evidence. Request markers distinguish
// routing/model configuration from actual provider-auth or inference failures;
// the Ops classifier owns precedence among those supplied observations.
func classifyOpsErrorLog(c *gin.Context, errType, message, code string, status int) (phase string, isBusinessLimited bool, errorOwner string, errorSource string) {
	return opscore.ClassifyRequestError(opscore.ErrorClassificationInput{
		Type: errType, Message: message, Code: code, Status: status,
		RoutingCapacityLimited:       isOpsRoutingCapacityLimited(c),
		ClientBusinessLimited:        HasOpsClientBusinessLimited(c),
		LocalModelConfiguration:      OpsClientBusinessLimitedReason(c) == OpsClientBusinessLimitedReasonLocalModelConfiguration,
		UpstreamError:                hasOpsUpstreamErrorContext(c),
		UpstreamClientInvalidRequest: isOpsUpstreamClientInvalidRequest(c, errType, code, status),
		ProviderAuthFailure:          hasOpsProviderAuthFailure(c),
	})
}

// isOpsUpstreamClientInvalidRequest recognizes the property-name-length
// exception with both client and stored upstream 400 status. Other codes, types
// or missing status evidence return false, leaving ordinary upstream invalid
// requests available for SLA failure classification.
func isOpsUpstreamClientInvalidRequest(c *gin.Context, errType, code string, status int) bool {
	if c == nil || status != http.StatusBadRequest || errType != "invalid_request_error" ||
		!strings.EqualFold(strings.TrimSpace(code), openai.OpenAIPropertyNameAboveMaxLengthCode) {
		return false
	}
	v, ok := c.Get(OpsUpstreamStatusCodeKey)
	if !ok {
		return false
	}
	switch upstreamStatus := v.(type) {
	case int:
		return upstreamStatus == http.StatusBadRequest
	case int64:
		return upstreamStatus == http.StatusBadRequest
	default:
		return false
	}
}

// hasOpsUpstreamErrorContext reports a positive stored status or nonempty typed
// attempt slice. Nil context returns false; a nonempty list qualifies even when
// its elements are nil, because this predicate checks observation presence
// rather than final-event selection.
func hasOpsUpstreamErrorContext(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if v, ok := c.Get(OpsUpstreamStatusCodeKey); ok {
		switch code := v.(type) {
		case int:
			if code > 0 {
				return true
			}
		case int64:
			if code > 0 {
				return true
			}
		}
	}
	if v, ok := c.Get(OpsUpstreamErrorsKey); ok {
		if events, ok := v.([]*opscore.OpsUpstreamErrorEvent); ok && len(events) > 0 {
			return true
		}
	}
	return false
}

// hasOpsProviderAuthFailure checks the last nonnil attempt for provider-auth
// stage attribution. Missing context, no usable event or a final event from
// another stage returns false, even when an earlier event carried
// authentication failure.
func hasOpsProviderAuthFailure(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if v, ok := c.Get(OpsUpstreamErrorsKey); ok {
		if events, ok := v.([]*opscore.OpsUpstreamErrorEvent); ok {
			for _, event := range slices.Backward(events) {
				if event != nil {
					return event.Stage == opscore.ErrorPhaseProviderAuth
				}
			}
		}
	}
	return false
}

// truncateString delegates byte limiting with UTF-8 repair at an actual cut.
// Nonpositive limits return empty; already fitting input retains its supplied
// bytes, including invalid UTF-8, so callers use this for bounded diagnostic
// text rather than universal validation.
func truncateString(s string, max int) string { return logredact.TruncateUTF8(s, max) }

// shouldSkipOpsErrorLog applies the current advanced filter snapshot to
// message, body and request path. Nil service disables filtering and ctx is
// unused. Count filtering checks count_tokens path text; other configured
// categories use case-folded diagnostic substrings in their designated
// message/body sources.
func shouldSkipOpsErrorLog(ctx context.Context, ops *opscore.OpsService, message, body, requestPath string) bool {
	if ops == nil {
		return false
	}

	_ = ctx
	settings := ops.OpsAdvancedSettingsSnapshot()

	msgLower := strings.ToLower(message)
	bodyLower := strings.ToLower(body)

	if settings.IgnoreCountTokensErrors && strings.Contains(requestPath, "/count_tokens") {
		return true
	}

	if settings.IgnoreContextCanceled {
		if strings.Contains(msgLower, opsErrContextCanceled) || strings.Contains(bodyLower, opsErrContextCanceled) {
			return true
		}
	}

	if settings.IgnoreNoAvailableProviders {
		if strings.Contains(msgLower, opsErrNoAvailableProviders) || strings.Contains(bodyLower, opsErrNoAvailableProviders) {
			return true
		}
	}

	if settings.IgnoreInvalidApiKeyErrors {
		if strings.Contains(bodyLower, opsErrInvalidAPIKey) || strings.Contains(bodyLower, opsErrAPIKeyRequired) {
			return true
		}
	}

	if settings.IgnoreInsufficientBalanceErrors {
		if strings.Contains(bodyLower, opsErrInsufficientBalance) || strings.Contains(bodyLower, opsErrInsufficientProviderBalance) ||
			strings.Contains(bodyLower, opsErrInsufficientQuota) ||
			strings.Contains(msgLower, opsErrInsufficientBalance) || strings.Contains(msgLower, opsErrInsufficientProviderBalance) {
			return true
		}
	}

	return false
}
