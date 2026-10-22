package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/live"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	gatewaysession "github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	openaicore "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai/liveattestation"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// cancelingLiveStore injects cancellation immediately after the underlying
// controller claim operation.
type cancelingLiveStore struct {
	liveTestStore // underlying controller state fixture
	// cancel is invoked after every base claim result, including failure. Tests
	// provide the active proxy context's cancellation function before calling Claim.
	cancel context.CancelFunc
}

// countingLiveProviders counts provider reads and reports the supplied
// context state.
type countingLiveProviders struct {
	// ExecutionProviderStore leaves unneeded repository operations unset; the
	// cancellation test observes only the GetByID override below.
	gatewayprovider.ExecutionProviderStore
	reads atomic.Int32 // observed provider lookup count
}

// liveFixtureInputs supplies shared transport, identity and storage bindings
// for Live fixtures.
type liveFixtureInputs struct {
	transport httpclient.UpstreamTransport           // upstream HTTP exchange fixture
	providers gatewayprovider.ExecutionProviderStore // saved provider lookup fixture
	store     gatewaysession.LiveCallStore           // saved call storage fixture
	// concurrency optionally supplies persistent Live leases. Nil leaves lease
	// access unavailable for tests that exercise storage or policy in isolation.
	concurrency *scheduler.ConcurrencyService
	// logs optionally enables zero-cost usage recording. Nil leaves finalization
	// observation disabled while controller and saved-call behavior remains testable.
	logs        usage.UsageLogRepository
	profiles    *egressadapter.TLSProfiles          // preloaded TLS profile service
	routers     *egress.TLSFingerprintRouterService // preloaded TLS routing service
	dialer      openai.WSClientDialer               // control connection dial fixture
	attestation liveattestation.Provider            // creation header generation fixture
	cipher      identity.SecretEncryptor            // saved header encryption fixture
	duration    time.Duration                       // configured logical session lifetime
}

// liveProfileStore supplies fixed TLS profile objects to production snapshot
// loading.
type liveProfileStore struct {
	// TLSFingerprintProfileRepository leaves unused operations unset; snapshot
	// loading obtains the configured fixture values through List.
	egress.TLSFingerprintProfileRepository
	// values and pointed profiles are returned directly. Callers keep that fixture
	// storage stable while the production profile service loads its snapshot.
	values []*egress.TLSFingerprintProfile
}

// liveRouterStore supplies fixed TLS router objects to production snapshot
// loading.
type liveRouterStore struct {
	// TLSFingerprintRouterRepository leaves unused operations unset; snapshot
	// loading obtains the configured fixture values through List.
	egress.TLSFingerprintRouterRepository
	// values and pointed routers are returned directly. The test configures them
	// before starting production router snapshot loading and keeps them stable.
	values []*egress.TLSFingerprintRouter
}

// liveTestFrame contains one injected frame observation or captured outbound
// frame.
type liveTestFrame struct {
	messageType coderws.MessageType // observed WebSocket frame kind
	// payload can retain injected fixture bytes. WriteFrame creates its own copy
	// for captured output, while ReadFrame returns the queued observation directly.
	payload []byte
	err     error // injected frame read error
}

// liveTestFrameConn coordinates injected reads, captured writes and transport
// close notification.
type liveTestFrameConn struct {
	// reads and writes carry separate inbound and outbound fixture observations.
	// closed wakes waiting operations; closeOnce closes that notification once,
	// while the data channels remain open and can retain buffered observations.
	reads     chan liveTestFrame // queued inbound frame observations
	writes    chan liveTestFrame // captured outbound frame observations
	closed    chan struct{}      // transport close notification channel
	closeOnce sync.Once          // close notification execution guard
}

// liveTestDialer captures control dialing inputs and returns its configured
// frame connection.
type liveTestDialer struct {
	conn *liveTestFrameConn // configured returned frame connection
	// url, headers and tlsProfile capture one dial operation. Headers are cloned;
	// the profile pointer stays shared. Relay channel exchanges precede assertions
	// on these fields in the proxy test.
	url        string                  // captured control WebSocket URL
	headers    http.Header             // cloned dial authentication headers
	tlsProfile *tlsfingerprint.Profile // retained selected TLS profile
}

// liveTestProviderRepo supplies the configured execution target for
// saved-provider lookup.
type liveTestProviderRepo struct {
	// ExecutionProviderStore leaves unused operations unset. GetByID returns the
	// configured provider directly for this fixture's saved-target lookup.
	gatewayprovider.ExecutionProviderStore
	provider *gatewayprovider.ExecutionProvider // configured saved provider target
}

// liveTestStore models saved-call and controller updates under one fixture
// mutex.
type liveTestStore struct {
	// GatewayCache leaves unrelated session operations unset. This fixture
	// implements the Live call and controller methods needed by the tests.
	gatewaysession.GatewayCache
	// mu protects record and injected errors during runtime reads and controller
	// updates. Injection is configured before observation starts, and record
	// snapshots copy the struct while retaining any nested model-map storage.
	mu     sync.Mutex                     // call and controller state lock
	record *gatewaysession.LiveCallRecord // retained saved call state
	// claimErr, getCallErr and getControllerErr override their respective operations
	// before normal missing-record checks, separating a store outage from absent
	// ownership. They are set before the observation loop starts.
	claimErr         error // injected controller claim failure
	getCallErr       error // injected saved-call read failure
	getControllerErr error // injected controller read failure
}

// liveObserverStore counts controller claims after local observer registration
// for shutdown assertions.
type liveObserverStore struct {
	liveTestStore              // underlying controller state fixture
	claims        atomic.Int32 // observed controller claim count
}

// liveTestConcurrencyCache accepts Live leases and counts their requested
// releases.
type liveTestConcurrencyCache struct {
	// ConcurrencyCache leaves ordinary request operations unset. The Live fixture
	// accepts acquisition and refresh, and counts releases through its overrides.
	scheduler.ConcurrencyCache
	// mu protects releases while finalization and test assertions may progress
	// independently. It keeps the observed cleanup count consistent for assertions.
	mu       sync.Mutex // lease release observation lock
	releases int        // observed Live lease releases
}

// liveTestUsageRepo records shallow usage-row copies for finalization
// assertions.
type liveTestUsageRepo struct {
	// UsageLogRepository leaves unrelated log queries unset. Create appends the
	// finalization row used by assertions in this fixture.
	usage.UsageLogRepository
	// mu protects logs while observation and assertions can run independently.
	// Create copies row structs but nested pointers remain shared, so fixture
	// callers leave retained row data stable after insertion.
	mu   sync.Mutex        // usage row observation lock
	logs []*usage.UsageLog // captured shallow usage rows
}

// liveTestBestEffortUsageRepo adds injected queue-write failure to
// synchronous usage capture.
type liveTestBestEffortUsageRepo struct {
	liveTestUsageRepo // inherited synchronous usage capture
	// bestEffortErr supplies each queue attempt's result, while bestEffortCalls
	// counts those attempts under the inherited mu. Failure lets production
	// WriteUsage reach the inherited synchronous Create fallback.
	bestEffortErr   error // configured queue insertion error
	bestEffortCalls int   // observed queue insertion attempts
}

// liveHTTPUpstreamStub captures creation requests and returns fixed SDP call
// metadata.
type liveHTTPUpstreamStub struct {
	// request, body and tlsProfile retain one intercepted creation exchange. request remains
	// shared, body is read into new storage, and TLS inspection retains its
	// provided profile pointer for assertions after the call returns.
	request    *http.Request           // retained intercepted creation request
	body       []byte                  // captured creation request bytes
	tlsProfile *tlsfingerprint.Profile // retained selected TLS profile
}

// liveAttestationStub supplies a configured creation header or generation
// failure.
type liveAttestationStub struct {
	header string // configured plaintext attestation header
	err    error  // configured provider attestation error
}

// ClaimLiveController performs the base claim and then cancels the proxy
// context before returning. Cancellation occurs for every claim result, so
// tests install a usable cancel function and observe where runtime handoff
// stops.
func (s *cancelingLiveStore) ClaimLiveController(ctx context.Context, hash, controller, owner string) (bool, error) {
	ok, err := s.liveTestStore.ClaimLiveController(ctx, hash, controller, owner)
	s.cancel()
	return ok, err
}

// GetByID increments an atomic read counter and returns the supplied context
// error with no provider. An active context therefore returns nil and nil;
// the handoff test expects this operation to remain uncalled.
func (r *countingLiveProviders) GetByID(ctx context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	r.reads.Add(1)
	return nil, ctx.Err()
}

// TestLiveHandoffCancellationStopsBeforeProviderLookup cancels immediately
// after a successful control claim. Proxy handoff returns cancellation within
// the asserted short interval and performs zero subsequent provider
// repository reads.
func TestLiveHandoffCancellationStopsBeforeProviderLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	record := &gatewaysession.LiveCallRecord{CallHash: "lifecycle-test", Controller: gatewaysession.LiveControllerPending, ProviderID: 7, ExpiresAt: time.Now().Add(time.Minute)}
	store := &cancelingLiveStore{liveTestStore: liveTestStore{record: record}, cancel: cancel}
	providers := &countingLiveProviders{}
	s := newLiveFixture(liveFixtureInputs{store: store, providers: providers})
	require.NoError(t, s.StopLiveObservers(context.Background()))
	start := time.Now()
	err := s.Proxy(ctx, record, &coderws.Conn{})
	if err != context.Canceled {
		t.Errorf("expected cancellation, got %v", err)
	}
	// This budget is below the observer handoff interval, exposing a cancellation
	// path that would still wait for the full handoff delay before provider lookup.
	if time.Since(start) >= 100*time.Millisecond {
		t.Error("cancelled Live handoff still slept for the observer interval")
	}
	if providers.reads.Load() != 0 {
		t.Errorf("cancelled Live handoff still queried execution provider: %d", providers.reads.Load())
	}
}

// newLiveFixture constructs Live execution with the supplied shared fixture
// services. Optional concurrency and logs enable leases and usage recording;
// other tests leave them unavailable. Its Background callback launches the
// supplied function directly, so observer tests arrange completion and
// shutdown around their own synchronization.
func newLiveFixture(v liveFixtureInputs) *OpenAILiveExecutor {
	aux := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: v.transport, store: v.providers, profiles: v.profiles})
	aux.Requests.Routers = v.routers
	aux.Requests.ClientPolicy.Routers = v.routers
	out := &OpenAILiveExecutor{Options: OpenAILiveOptions{MaxSessionDuration: v.duration, ObserverRetryInterval: time.Second}, Requests: aux.Requests, Store: v.store, Dialer: v.dialer, Attestation: v.attestation, AttestationCipher: v.cipher, Selection: selection.NewCompatible(selection.CompatibleDependencies{}, selection.Options{}), Routes: gatewayprovider.NewRoutePlanner(nil), Background: func(_ string, fn func()) bool {
		// The fixture starts callbacks directly; observer tests synchronize their own
		// completion and shutdown before releasing state read by these goroutines.
		go fn()
		return true
	}}
	if v.concurrency != nil {
		out.Leases = v.concurrency.LiveLeases()
	}
	if v.logs != nil {
		out.Usage = completion.NewRecorder(completion.Dependencies{Logs: completion.SnapshotLogWriter(v.logs), Observe: telemetry.ObserveCompletion}, completion.RecorderOptions{})
	}
	return out
}

// List returns the configured profile slice and pointers directly with nil
// error. The production loader consumes them while the test keeps fixture
// storage stable; nil values represent an empty configured list.
func (s *liveProfileStore) List(context.Context) ([]*egress.TLSFingerprintProfile, error) {
	return s.values, nil
}

// List returns the configured router slice and pointers directly with nil
// error. The production loader consumes them while the test keeps fixture
// storage stable; nil values represent an empty configured list.
func (s *liveRouterStore) List(context.Context) ([]*egress.TLSFingerprintRouter, error) {
	return s.values, nil
}

// newLiveTestFrameConn creates independent buffered frame queues and an open
// close-notification channel. Each queue holds eight fixture observations,
// allowing ordinary relay cases to exchange frames while the test controls
// transport closure.
func newLiveTestFrameConn() *liveTestFrameConn {
	// Buffer both fixture directions so a relay and its assertions can progress
	// independently while the test decides when to supply the next transport observation.
	return &liveTestFrameConn{
		reads:  make(chan liveTestFrame, 8),
		writes: make(chan liveTestFrame, 8),
		closed: make(chan struct{}),
	}
}

// ReadFrame waits for an injected frame, close notification or context
// completion and returns the corresponding observation. Queued payload
// storage is returned directly; fixture producers keep it stable while
// consumers inspect it.
//
// Transport close returns a normal WebSocket close error for the runtime
// adapter to interpret as call termination. Context completion retains its
// cause. The ordinary select decides among simultaneously ready conditions,
// so queued frames can still be observed when closure or cancellation is also
// ready; this fixture does not impose a priority between them.
func (c *liveTestFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	select {
	case frame := <-c.reads:
		return frame.messageType, frame.payload, frame.err
	case <-c.closed:
		return coderws.MessageText, nil, coderws.CloseError{Code: coderws.StatusNormalClosure}
	case <-ctx.Done():
		return coderws.MessageText, nil, context.Cause(ctx)
	}
}

// WriteFrame copies payload bytes before waiting to enqueue one captured
// outbound frame. The copy separates later caller mutation from assertions on
// the write queue. Context completion retains its cause, while closed
// transport returns the fixture close error.
//
// The output queue, close notification and cancellation are independently
// ready select cases. An available queue can accept a frame even when another
// condition is ready, so callers interpret this as one selected fixture
// observation rather than strict close-first ordering. No data channel is
// closed by Close, preventing send-on-closed-channel panics during those
// races.
func (c *liveTestFrameConn) WriteFrame(ctx context.Context, messageType coderws.MessageType, payload []byte) error {
	// Capture independent bytes before enqueueing so a sender can reuse its buffer
	// after WriteFrame returns while assertions still inspect the original payload.
	frame := liveTestFrame{messageType: messageType, payload: append([]byte(nil), payload...)}
	select {
	case c.writes <- frame:
		return nil
	case <-c.closed:
		return errors.New("connection closed")
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// WriteJSON serializes a fixture value and queues it as a text frame. Marshal
// errors return before enqueueing, while frame output follows WriteFrame's
// copied-payload, close and cancellation behavior.
func (c *liveTestFrameConn) WriteJSON(ctx context.Context, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.WriteFrame(ctx, coderws.MessageText, payload)
}

// ReadMessage returns the next queued frame payload and error while
// discarding its message kind. It shares ReadFrame's injected byte storage
// and closure/cancellation handling, matching the narrower WSClientConn
// fixture interface.
func (c *liveTestFrameConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_, payload, err := c.ReadFrame(ctx)
	return payload, err
}

// Ping accepts fixture liveness checks without another network operation.
func (c *liveTestFrameConn) Ping(context.Context) error { return nil }

// Close closes transport notification once and returns nil for repeated
// calls. The frame queues stay open, so waiting ReadFrame and WriteFrame
// operations can observe closure alongside any other ready select case.
func (c *liveTestFrameConn) Close() error {
	// Close notification once across controller and deferred transport cleanup,
	// keeping repeated teardown from closing the same channel a second time.
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

// Dial records URL, cloned headers and the supplied profile, then returns the
// configured frame connection with upgrade status. Fixture callers use the
// returned connection and close it through the runtime ownership path.
func (d *liveTestDialer) Dial(
	_ context.Context,
	wsURL string,
	headers http.Header,
	_ string,
	profile *tlsfingerprint.Profile,
) (openai.WSClientConn, int, http.Header, error) {
	d.url = wsURL
	d.headers = headers.Clone()
	d.tlsProfile = profile
	return d.conn, http.StatusSwitchingProtocols, nil, nil
}

// GetByID returns the configured provider pointer with nil error for any
// identifier. The target remains shared fixture storage, so tests supply the
// needed record and keep it stable through control operations.
func (r *liveTestProviderRepo) GetByID(context.Context, int64) (*gatewayprovider.ExecutionProvider, error) {
	return r.provider, nil
}

// SaveLiveCall stores a shallow record value copy under the fixture lock.
// Nested model-map storage remains shared and the supplied TTL is unused;
// tests use ExpiresAt to exercise runtime logical expiry separately.
func (s *liveTestStore) SaveLiveCall(_ context.Context, record *gatewaysession.LiveCallRecord, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *record
	s.record = &copy
	return nil
}

// GetLiveCall returns a shallow copy of the matching record under the fixture
// lock. Injected read failure wins before missing-record checks; absent or
// mismatched hashes return not found, while nested mapping storage remains
// shared.
func (s *liveTestStore) GetLiveCall(_ context.Context, callHash string) (*gatewaysession.LiveCallRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getCallErr != nil {
		return nil, s.getCallErr
	}
	if s.record == nil || s.record.CallHash != callHash {
		return nil, gatewaysession.ErrLiveCallNotFound
	}
	copy := *s.record
	return &copy, nil
}

// ClaimLiveController atomically tests and installs controller ownership for
// the retained record. Injected claim errors return first; missing,
// mismatched or closed calls reject the claim. Observer claims require
// pending state, while proxy claims also allow observer takeover.
//
// Controller and ControllerOwner are changed under the same mutex after those
// checks. This lets concurrent runtime participants observe one ownership
// transition and lets ReleaseLiveController compare the owner that actually
// won. Tests configure injected errors before starting observation and use
// separate owner values for competing controller operations.
func (s *liveTestStore) ClaimLiveController(_ context.Context, callHash, controller, owner string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimErr != nil {
		return false, s.claimErr
	}
	if s.record == nil || s.record.CallHash != callHash || s.record.Controller == gatewaysession.LiveControllerClosed {
		return false, nil
	}
	if controller == gatewaysession.LiveControllerObserver && s.record.Controller != gatewaysession.LiveControllerPending {
		return false, nil
	}
	if controller == gatewaysession.LiveControllerProxy && s.record.Controller != gatewaysession.LiveControllerPending && s.record.Controller != gatewaysession.LiveControllerObserver {
		return false, nil
	}
	// Publish controller and owner within the same locked transition so release
	// compares the participant whose allowed claim actually updated this record.
	s.record.Controller = controller
	s.record.ControllerOwner = owner
	return true, nil
}

// ClaimLiveController records each observer's entry into shared storage and
// delegates the controller claim. Tests configure the embedded store before
// observation begins and inspect the count concurrently.
func (s *liveObserverStore) ClaimLiveController(ctx context.Context, callHash, controller, owner string) (bool, error) {
	s.claims.Add(1)
	return s.liveTestStore.ClaimLiveController(ctx, callHash, controller, owner)
}

// ReleaseLiveController resets matching ownership to pending and clears its
// owner under the fixture lock. Missing record, mismatched hash or another
// owner returns false and leaves current controller state available.
//
// The owner check and both field updates share one critical section. A stale
// controller therefore cannot release a newer participant's claim, and
// subsequent observer acquisition sees the pending state with its cleared
// owner together. The operation models ownership arbitration while
// persistence and TTL behavior stay with production storage.
func (s *liveTestStore) ReleaseLiveController(_ context.Context, callHash, owner string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record == nil || s.record.CallHash != callHash || s.record.ControllerOwner != owner {
		return false, nil
	}
	// Clear the matching claim as one locked transition; a subsequent observer
	// must see pending state and no remaining controller owner together.
	s.record.Controller = gatewaysession.LiveControllerPending
	s.record.ControllerOwner = ""
	return true, nil
}

// GetLiveController returns current controller state under the fixture mutex.
// Injected store failure is returned before missing-record checks; an absent
// or mismatched hash returns not found instead of a controller label.
func (s *liveTestStore) GetLiveController(_ context.Context, callHash string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getControllerErr != nil {
		return "", s.getControllerErr
	}
	if s.record == nil || s.record.CallHash != callHash {
		return "", gatewaysession.ErrLiveCallNotFound
	}
	return s.record.Controller, nil
}

// MarkLiveCallClosed atomically marks a matching open record closed and
// clears controller ownership. Missing, mismatched or already closed records
// return false, allowing Finalize to recognize that another completion path
// already won.
//
// Both fields change under the same lock after the closed-state check.
// Repeated finalization therefore obtains true once for this fixture and can
// release its lease and write zero usage once. The supplied TTL is unused;
// runtime expiry is represented by the record's ExpiresAt value.
func (s *liveTestStore) MarkLiveCallClosed(_ context.Context, callHash string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record == nil || s.record.CallHash != callHash || s.record.Controller == gatewaysession.LiveControllerClosed {
		return false, nil
	}
	// Mark closure and clear ownership together after the one-time state check,
	// allowing competing finalizers to release and record this call once in the fixture.
	s.record.Controller = gatewaysession.LiveControllerClosed
	s.record.ControllerOwner = ""
	return true, nil
}

// AcquireLiveLease accepts every fixture request with nil error, leaving real
// quota decisions outside this replacement. Finalization tests exercise
// release counts separately, so acquisition does not maintain capacity or
// ownership state here.
func (c *liveTestConcurrencyCache) AcquireLiveLease(
	context.Context,
	int64,
	int,
	int64,
	int,
	int64,
	string,
	bool,
) (bool, error) {
	return true, nil
}

// RefreshLiveLease accepts every fixture renewal with nil error. The
// replacement keeps expiry and controller tests focused on runtime decisions,
// while terminal session behavior is asserted through the separately counted
// release operation.
func (c *liveTestConcurrencyCache) RefreshLiveLease(
	context.Context,
	int64,
	int64,
	int64,
	string,
) (bool, error) {
	return true, nil
}

// ReleaseLiveLease increments the observed lease cleanup count under its
// mutex.
func (c *liveTestConcurrencyCache) ReleaseLiveLease(
	context.Context,
	int64,
	int64,
	int64,
	string,
) error {
	c.mu.Lock()
	c.releases++
	c.mu.Unlock()
	return nil
}

// Create appends a shallow usage-row copy under the fixture mutex and reports
// successful insertion. Nested pointers remain shared; assertions inspect
// captured values after recording, and callers keep retained data stable.
func (r *liveTestUsageRepo) Create(_ context.Context, log *usage.UsageLog) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *log
	r.logs = append(r.logs, &copy)
	return true, nil
}

// TestRunLiveControllerClosesExpiredSession supplies a near-expired call to
// the controller loop. Expiry returns deadline exceeded and queues the
// expected session.close text frame to the upstream fixture.
func TestRunLiveControllerClosesExpiredSession(t *testing.T) {
	upstream := newLiveTestFrameConn()
	// Keep expiry below the lease-refresh tick to isolate maximum-session handling.
	record := &gatewaysession.LiveCallRecord{ExpiresAt: time.Now().Add(20 * time.Millisecond)}
	service := newLiveFixture(liveFixtureInputs{})

	err := service.liveRuntime().RunController(context.Background(), record, liveUpstreamFrames{upstream}, make(chan error))
	require.ErrorIs(t, err, context.DeadlineExceeded)

	select {
	case frame := <-upstream.writes:
		require.Equal(t, coderws.MessageText, frame.messageType)
		require.JSONEq(t, `{"type":"session.close"}`, string(frame.payload))
	case <-time.After(time.Second):
		t.Fatal("没有向上游发送 session.close")
	}
}

// TestFinalizeLiveCallIsIdempotentAndWritesZeroUsage finalizes the same saved
// call twice. The store claim permits one lease release and one zero-cost
// usage row with separate hashed financial identity and retained model trace.
func TestFinalizeLiveCallIsIdempotentAndWritesZeroUsage(t *testing.T) {
	record := &gatewaysession.LiveCallRecord{
		CallID:            "call_secret",
		CallHash:          live.HashCallID("call_secret"),
		ProviderID:        11,
		APIKeyID:          22,
		UserID:            33,
		GroupID:           44,
		LeaseID:           "lease-1",
		Model:             "gpt-live-test",
		RequestedModel:    "live-alias",
		UpstreamModel:     "gpt-live-upstream",
		ModelMappingChain: "live-alias→gpt-live-test→gpt-live-upstream",
		CreatedAt:         time.Now().Add(-time.Second),
		ExpiresAt:         time.Now().Add(time.Hour),
		Controller:        gatewaysession.LiveControllerPending,
		InboundEndpoint:   "/v1/live",
	}
	store := &liveTestStore{}
	require.NoError(t, store.SaveLiveCall(context.Background(), record, time.Hour))
	concurrencyCache := &liveTestConcurrencyCache{}
	usageRepo := &liveTestUsageRepo{}
	service := newLiveFixture(liveFixtureInputs{store: store, concurrency: scheduler.NewConcurrencyService(concurrencyCache, scheduler.Diagnostics{
		Logf:  logging.LegacyPrintf,
		Event: logging.Event,
	},
	), logs: usageRepo})

	service.liveRuntime().Finalize(record)
	service.liveRuntime().Finalize(record)

	concurrencyCache.mu.Lock()
	require.Equal(t, 1, concurrencyCache.releases)
	concurrencyCache.mu.Unlock()
	usageRepo.mu.Lock()
	require.Len(t, usageRepo.logs, 1)
	log := usageRepo.logs[0]
	usageRepo.mu.Unlock()
	require.Equal(t, usage.RequestTypeLive, log.RequestType)
	require.Equal(t, record.CallHash, log.BillingKey)
	require.Len(t, log.RequestID, 36)
	require.NotEqual(t, record.CallID, log.RequestID)
	require.NotNil(t, log.DurationMs)
	require.Zero(t, log.InputTokens)
	require.Zero(t, log.OutputTokens)
	require.Zero(t, log.TotalCost)
	require.Zero(t, log.ActualCost)
	require.Equal(t, record.Model, log.Model)
	require.Equal(t, record.RequestedModel, log.RequestedModel)
	require.NotNil(t, log.UpstreamModel)
	require.Equal(t, record.UpstreamModel, *log.UpstreamModel)
	require.NotNil(t, log.ModelMappingChain)
	require.Equal(t, record.ModelMappingChain, *log.ModelMappingChain)
}

// TestGetLiveCallForIdentityRejectsMismatchedCaller queries one saved call
// using another Key and then its matching identity. Lookup rejects the
// mismatched credential and returns the saved provider for the matching
// request.
func TestGetLiveCallForIdentityRejectsMismatchedCaller(t *testing.T) {
	groupID := int64(44)
	record := &gatewaysession.LiveCallRecord{
		CallID:     "call_identity",
		CallHash:   live.HashCallID("call_identity"),
		APIKeyID:   22,
		UserID:     33,
		GroupID:    groupID,
		Controller: gatewaysession.LiveControllerPending,
	}
	store := &liveTestStore{}
	require.NoError(t, store.SaveLiveCall(context.Background(), record, time.Hour))
	service := newLiveFixture(liveFixtureInputs{store: store})

	_, err := service.Lookup(context.Background(), record.CallID, gatewaysession.LiveCallIdentity{
		APIKeyID: 99,
		UserID:   record.UserID,
		GroupID:  &groupID,
	})
	require.ErrorIs(t, err, gatewaysession.ErrLiveIdentityMismatch)

	loaded, err := service.Lookup(context.Background(), record.CallID, gatewaysession.LiveCallIdentity{
		APIKeyID: record.APIKeyID,
		UserID:   record.UserID,
		GroupID:  &groupID,
	})
	require.NoError(t, err)
	require.Equal(t, record.ProviderID, loaded.ProviderID)
}

// TestLiveSidebandRewritesEachSessionModelAndRestoresResponse sends a saved
// Key alias and tool alias through provider mapping. Protocol model metadata
// changes and restores to the client name while instruction text stays
// intact.
func TestLiveSidebandRewritesEachSessionModelAndRestoresResponse(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 11,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"model_whitelist": []string{"gpt-5.4"},
				"model_mapping":   map[string]any{"gpt-5": "gpt-5.4"},
			},
		},
	}
	upstreamModel := gatewayprovider.ExecutionModelPolicy(provider).OpenAIUpstream("gpt-5", false)
	record := &gatewaysession.LiveCallRecord{
		GroupID:            44,
		Model:              "gpt-5",
		RequestedModel:     "live-alias",
		UpstreamModel:      upstreamModel,
		APIKeyModelMapping: map[string]string{"live-alias": "gpt-5", "tool-alias": "tool-target"},
	}
	service := newLiveFixture(liveFixtureInputs{})
	payload := []byte(`{"type":"session.update","session":{"model":"live-alias","tools":[{"model":"tool-alias"}],"instructions":"keep live-alias and gpt-5.1-codex"}}`)

	rewritten, clientModel, internalModels, err := service.rewriteLiveSidebandClientPayload(context.Background(), record, provider, payload)

	require.NoError(t, err)
	require.Equal(t, "live-alias", clientModel)
	require.Equal(t, upstreamModel, gjson.GetBytes(rewritten, "session.model").String())
	require.Equal(t, "tool-target", gjson.GetBytes(rewritten, "session.tools.0.model").String())
	require.Equal(t, "keep live-alias and gpt-5.1-codex", gjson.GetBytes(rewritten, "session.instructions").String())

	response := live.RestoreServerPayload(
		[]byte(fmt.Sprintf(`{"type":"session.updated","session":{"model":%q,"instructions":"keep gpt-5.1-codex"}}`, upstreamModel)),
		clientModel,
		internalModels,
	)
	require.Equal(t, "live-alias", gjson.GetBytes(response, "session.model").String())
	require.Equal(t, "keep gpt-5.1-codex", gjson.GetBytes(response, "session.instructions").String())
}

// TestProxyLiveSidebandForwardsTextAndBinary relays real client WebSocket
// frames through the production Live proxy and fake upstream. Both frame
// kinds round-trip, while dialing uses saved attestation, account identity
// and routed TLS headers.
func TestProxyLiveSidebandForwardsTextAndBinary(t *testing.T) {
	profileService, routerService := newLiveTLSRoutingServices()
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 11,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 2,
			Credentials: map[string]any{
				"access_token":       "test-access-token",
				"chatgpt_account_id": "acct_test",
			},
			Extra: map[string]any{
				"enable_tls_fingerprint":    true,
				"tls_fingerprint_router_id": int64(9),
			},
		},
	}
	record := &gatewaysession.LiveCallRecord{
		CallID:     "call_proxy",
		CallHash:   live.HashCallID("call_proxy"),
		ProviderID: provider.Record.ID,
		APIKeyID:   22,
		UserID:     33,
		LeaseID:    "lease-1",
		CreatedAt:  time.Now(),
		ExpiresAt:  time.Now().Add(time.Minute),
		Controller: gatewaysession.LiveControllerPending,
		UserAgent:  "test-live-client",
	}
	attestationCipher := openai.NewLiveAttestationCipher("live-sideband-test-secret")
	var err error
	record.AttestationCiphertext, err = attestationCipher.Encrypt(`{"v":1,"s":0,"t":"v1.sideband"}`)
	require.NoError(t, err)
	store := &liveTestStore{}
	require.NoError(t, store.SaveLiveCall(context.Background(), record, time.Hour))
	upstream := newLiveTestFrameConn()
	dialer := &liveTestDialer{conn: upstream}
	service := newLiveFixture(liveFixtureInputs{providers: &liveTestProviderRepo{provider: provider}, store: store, dialer: dialer, cipher: attestationCipher, profiles: profileService, routers: routerService})
	// The server owns its accepted transport and publishes one proxy outcome.
	// Buffered reporting lets teardown finish while the client drives frame assertions.
	proxyResult := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		downstream, err := coderws.Accept(writer, request, nil)
		if err != nil {
			proxyResult <- err
			return
		}
		defer func() { _ = downstream.CloseNow() }()
		proxyResult <- service.Proxy(request.Context(), record, downstream)
	}))
	defer server.Close()

	client, _, err := coderws.Dial(
		context.Background(),
		"ws"+strings.TrimPrefix(server.URL, "http"),
		nil,
	)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"client.text"}`)))
	clientText := <-upstream.writes
	require.Equal(t, coderws.MessageText, clientText.messageType)
	require.JSONEq(t, `{"type":"client.text"}`, string(clientText.payload))

	require.NoError(t, client.Write(ctx, coderws.MessageBinary, []byte{1, 2, 3}))
	clientBinary := <-upstream.writes
	require.Equal(t, coderws.MessageBinary, clientBinary.messageType)
	require.Equal(t, []byte{1, 2, 3}, clientBinary.payload)

	upstream.reads <- liveTestFrame{messageType: coderws.MessageText, payload: []byte(`{"type":"server.text"}`)}
	messageType, payload, err := client.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, coderws.MessageText, messageType)
	require.JSONEq(t, `{"type":"server.text"}`, string(payload))

	upstream.reads <- liveTestFrame{messageType: coderws.MessageBinary, payload: []byte{4, 5, 6}}
	messageType, payload, err = client.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, coderws.MessageBinary, messageType)
	require.Equal(t, []byte{4, 5, 6}, payload)

	require.Equal(t, "wss://chatgpt.com/backend-api/codex/call_proxy", dialer.url)
	require.Equal(t, "Bearer test-access-token", dialer.headers.Get("Authorization"))
	require.Equal(t, "acct_test", dialer.headers.Get("Chatgpt-Account-Id"))
	require.Equal(t, `{"v":1,"s":0,"t":"v1.sideband"}`, dialer.headers.Get(openai.LiveAttestationHeader))
	require.Equal(t, "codex_vscode/0.144.1 live-test", dialer.headers.Get("User-Agent"))
	require.Equal(t, "codex_vscode", dialer.headers.Get("Originator"))
	require.NotNil(t, dialer.tlsProfile)
	require.Equal(t, "live-routed", dialer.tlsProfile.Name)
	upstream.reads <- liveTestFrame{err: coderws.CloseError{Code: coderws.StatusNormalClosure}}
	require.ErrorIs(t, <-proxyResult, gatewaysession.ErrLiveCallNotFound)
}

// TestLiveSessionEndedTreatsLeaseLossAsTerminal compares lease loss,
// closed-call and expiry errors with takeover and transient failures. Only
// the tested terminal causes qualify for finalization, including wrapped
// lease loss.
func TestLiveSessionEndedTreatsLeaseLossAsTerminal(t *testing.T) {
	cases := []struct {
		name string // terminal classification case label
		err  error  // supplied session failure cause
		want bool   // expected session-ended classification
	}{
		{"租约丢失", gatewaysession.ErrLiveUnavailable, true},
		{"租约丢失（被包装）", fmt.Errorf("refresh live lease: %w", gatewaysession.ErrLiveUnavailable), true},
		{"上游报告会话已关闭", gatewaysession.ErrLiveCallNotFound, true},
		{"到达会话时长上限", context.DeadlineExceeded, true},
		{"控制权被他人接管", gatewaysession.ErrLiveControllerChanged, false},
		{"临时读错误", errors.New("unexpected EOF"), false},
		{"无错误", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, live.SessionEnded(tc.err))
		})
	}
}

// TestWaitForLiveObserverRetryLeavesExpiryToLoopFinalize retains an expired
// observer-owned call for the loop's finalization check. After proxy
// takeover, retry eligibility ends because that observer no longer owns
// control.
func TestWaitForLiveObserverRetryLeavesExpiryToLoopFinalize(t *testing.T) {
	record := &gatewaysession.LiveCallRecord{
		CallID:     "call_expired",
		CallHash:   live.HashCallID("call_expired"),
		Controller: gatewaysession.LiveControllerObserver,
		ExpiresAt:  time.Now().Add(-time.Minute),
	}
	store := &liveTestStore{}
	require.NoError(t, store.SaveLiveCall(context.Background(), record, time.Hour))
	svc := newLiveFixture(liveFixtureInputs{store: store})

	require.True(t, svc.liveRuntime().WaitForObserverRetry(context.Background(), record),
		"过期判定必须留给循环顶部，否则不会写 usage log")

	// After takeover, the new proxy owns control. Observer retry stops so that
	// participant does not continue dialing a connection for another controller's claim.
	require.NoError(t, store.SaveLiveCall(context.Background(), &gatewaysession.LiveCallRecord{
		CallID:     record.CallID,
		CallHash:   record.CallHash,
		Controller: gatewaysession.LiveControllerProxy,
		ExpiresAt:  time.Now().Add(time.Hour),
	}, time.Hour))
	require.False(t, svc.liveRuntime().WaitForObserverRetry(context.Background(), record))
}

// TestWaitForLiveObserverRetryTreatsStoreErrorAsRetryable injects
// controller-read outage beside a missing-record case. The outage permits
// another observation-loop pass, while an absent call ends observer retries.
func TestWaitForLiveObserverRetryTreatsStoreErrorAsRetryable(t *testing.T) {
	record := &gatewaysession.LiveCallRecord{
		CallID:     "call_flaky_store",
		CallHash:   live.HashCallID("call_flaky_store"),
		Controller: gatewaysession.LiveControllerObserver,
		ExpiresAt:  time.Now().Add(time.Hour),
	}
	store := &liveTestStore{getControllerErr: errors.New("redis: connection refused")}
	require.NoError(t, store.SaveLiveCall(context.Background(), record, time.Hour))
	svc := newLiveFixture(liveFixtureInputs{store: store})

	require.True(t, svc.liveRuntime().WaitForObserverRetry(context.Background(), record),
		"store 报错时必须继续重试，否则会话会静默结束")
	require.False(t, newLiveFixture(liveFixtureInputs{store: &liveTestStore{}}).liveRuntime().WaitForObserverRetry(context.Background(), record),
		"记录不存在时应停止重试")
}

// TestObserveLiveCallStoreOutageFallsBackToExpiryFinalize injects saved-call
// or controller-claim failure for an already expired creation snapshot.
// Observation still releases the lease and records one Live usage row in each
// tested outage.
func TestObserveLiveCallStoreOutageFallsBackToExpiryFinalize(t *testing.T) {
	cases := []struct {
		name   string               // injected store outage scenario
		inject func(*liveTestStore) // configure pre-observation store failure
	}{
		{"GetLiveCall 持续报错", func(store *liveTestStore) {
			store.getCallErr = errors.New("redis: i/o timeout")
		}},
		{"ClaimLiveController 报错", func(store *liveTestStore) {
			store.claimErr = errors.New("redis: i/o timeout")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := &gatewaysession.LiveCallRecord{
				CallID:     "call_store_outage",
				CallHash:   live.HashCallID("call_store_outage"),
				ProviderID: 11,
				APIKeyID:   22,
				UserID:     33,
				LeaseID:    "lease-1",
				Model:      "gpt-live-test",
				CreatedAt:  time.Now().Add(-time.Minute),
				ExpiresAt:  time.Now().Add(-time.Second),
				Controller: gatewaysession.LiveControllerPending,
			}
			store := &liveTestStore{}
			require.NoError(t, store.SaveLiveCall(context.Background(), record, time.Hour))
			tc.inject(store)
			concurrencyCache := &liveTestConcurrencyCache{}
			usageRepo := &liveTestUsageRepo{}
			svc := newLiveFixture(liveFixtureInputs{store: store, concurrency: scheduler.NewConcurrencyService(concurrencyCache, scheduler.Diagnostics{
				Logf:  logging.LegacyPrintf,
				Event: logging.Event,
			},
			), logs: usageRepo})

			svc.Options.ObserverRetryInterval = time.Millisecond
			svc.observeLiveCall(record)

			concurrencyCache.mu.Lock()
			require.Equal(t, 1, concurrencyCache.releases, "store 故障时租约释放不能丢")
			concurrencyCache.mu.Unlock()
			usageRepo.mu.Lock()
			require.Len(t, usageRepo.logs, 1, "store 故障时 usage log 不能丢")
			require.Equal(t, usage.RequestTypeLive, usageRepo.logs[0].RequestType)
			usageRepo.mu.Unlock()
		})
	}
}

// CreateBestEffort counts one queue insertion attempt and returns its
// configured error under the inherited mutex. It does not append a captured
// row; the injected error exercises production synchronous Create fallback.
func (r *liveTestBestEffortUsageRepo) CreateBestEffort(_ context.Context, _ *usage.UsageLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bestEffortCalls++
	return r.bestEffortErr
}

// TestFinalizeLiveCallUsageLogFallsBackToSyncCreate rejects best-effort
// analysis insertion during call finalization. Production WriteUsage makes
// one synchronous insertion retaining the call's financial identity and
// separate request ID.
func TestFinalizeLiveCallUsageLogFallsBackToSyncCreate(t *testing.T) {
	record := &gatewaysession.LiveCallRecord{
		CallID:     "call_usage_fallback",
		CallHash:   live.HashCallID("call_usage_fallback"),
		ProviderID: 11,
		APIKeyID:   22,
		UserID:     33,
		LeaseID:    "lease-1",
		Model:      "gpt-live-test",
		CreatedAt:  time.Now().Add(-time.Second),
		ExpiresAt:  time.Now().Add(time.Hour),
		Controller: gatewaysession.LiveControllerPending,
	}
	store := &liveTestStore{}
	require.NoError(t, store.SaveLiveCall(context.Background(), record, time.Hour))
	usageRepo := &liveTestBestEffortUsageRepo{bestEffortErr: errors.New("usage log queue dropped")}
	svc := newLiveFixture(liveFixtureInputs{store: store, concurrency: scheduler.NewConcurrencyService(&liveTestConcurrencyCache{}, scheduler.Diagnostics{
		Logf:  logging.LegacyPrintf,
		Event: logging.Event,
	},
	), logs: usageRepo})

	svc.liveRuntime().Finalize(record)

	usageRepo.mu.Lock()
	defer usageRepo.mu.Unlock()
	require.Equal(t, 1, usageRepo.bestEffortCalls)
	require.Len(t, usageRepo.logs, 1, "best-effort 失败后必须同步兜底落库")
	require.Equal(t, record.CallHash, usageRepo.logs[0].BillingKey)
	require.Len(t, usageRepo.logs[0].RequestID, 36)
}

// TestStopLiveObserversPreservesRemoteCall stops observers from two runtime
// facades while their initial store claims fail. Shutdown drains both tasks,
// keeps the saved remote call pending, and rejects later work from every facade.
func TestStopLiveObserversPreservesRemoteCall(t *testing.T) {
	record := &gatewaysession.LiveCallRecord{CallHash: "test-shutdown", Controller: gatewaysession.LiveControllerPending, ExpiresAt: time.Now().Add(time.Hour)}
	store := &liveObserverStore{liveTestStore: liveTestStore{record: record, claimErr: errors.New("temporary store failure")}}
	svc := newLiveFixture(liveFixtureInputs{store: store})
	first := svc.liveRuntime()
	second := svc.liveRuntime()
	// Both facades reach storage after registration. Join their observers after
	// stopping so saved-call assertions cover the entire shared local shutdown.
	done := make(chan struct{}, 2)
	for _, runtime := range []*live.Service{first, second} {
		go func() {
			runtime.Observe(record)
			done <- struct{}{}
		}()
	}
	require.Eventually(t, func() bool {
		return store.claims.Load() == 2
	}, time.Second, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, svc.StopLiveObservers(ctx))
	<-done
	<-done
	require.NoError(t, svc.StopLiveObservers(ctx))
	stored, err := store.GetLiveCall(context.Background(), record.CallHash)
	require.NoError(t, err)
	require.Equal(t, gatewaysession.LiveControllerPending, stored.Controller)
	first.Observe(record)
	second.Observe(record)
	svc.observeLiveCall(record)
	require.EqualValues(t, 2, store.claims.Load())
}

// TestLiveObserverManagerSharedInitialization races first use of a zero-value
// executor across callers. Every caller receives the same manager, and stopping
// that instance keeps it installed with admission closed.
func TestLiveObserverManagerSharedInitialization(t *testing.T) {
	svc := &OpenAILiveExecutor{}
	managers := make([]*live.ObserverManager, 16)
	var callers sync.WaitGroup
	start := make(chan struct{})
	// Release first callers together and join their writes before checking pointer
	// identity, so the race detector covers publication without racing the assertions.
	for index := range managers {
		callers.Go(func() {
			<-start
			managers[index] = svc.liveObserverManager()
		})
	}
	close(start)
	callers.Wait()
	for _, manager := range managers {
		require.Same(t, managers[0], manager)
	}
	require.NoError(t, svc.StopLiveObservers(context.Background()))
	require.Same(t, managers[0], svc.liveObserverManager())
	run, finish, ok := svc.beginLiveObserver("late")
	require.False(t, ok)
	require.Nil(t, run)
	require.Nil(t, finish)
}

// newLiveTLSRoutingServices starts production TLS profile and router
// snapshots using fixed fixture repositories. The exact test-live-client rule
// selects the named profile and Codex identity headers; returned services are
// reused for creation and control assertions.
func newLiveTLSRoutingServices() (*egressadapter.TLSProfiles, *egress.TLSFingerprintRouterService) {
	profileService := egressadapter.NewTLSProfiles(egress.NewTLSFingerprintProfileService(&liveProfileStore{values: []*egress.TLSFingerprintProfile{{ID: 20, Name: "live-routed"}}}, nil))
	profileService.Start()

	router := &egress.TLSFingerprintRouter{
		ID:      9,
		Name:    "live-router",
		Enabled: true,
		Rules: []egress.TLSFingerprintRouterRule{{
			Name:                    "live-client",
			Enabled:                 true,
			MatchType:               egress.TLSRouterMatchExact,
			Pattern:                 "test-live-client",
			TLSFingerprintProfileID: 20,
			UpstreamUserAgent:       "codex_vscode/0.144.1 live-test",
			UpstreamOriginator:      "codex_vscode",
		}},
	}
	routerService := egress.NewTLSFingerprintRouterService(&liveRouterStore{values: []*egress.TLSFingerprintRouter{router}}, nil)
	routerService.Start()

	return profileService, routerService
}

// Check returns the configured fixture attestation availability error
// directly.
func (s liveAttestationStub) Check(context.Context) error {
	return s.err
}

// Generate returns configured plaintext attestation and its injected error
// together. Tests provide either a usable header or a provider failure;
// native preparation decides whether encryption and upstream creation may
// continue.
func (s liveAttestationStub) Generate(context.Context) (string, error) {
	return s.header, s.err
}

// Do captures the creation request and reads its body before returning fixed
// SDP and Location metadata. A read error returns before a usable response;
// the caller owns and closes the returned response body.
func (s *liveHTTPUpstreamStub) Do(
	request *http.Request,
	_ string,
	_ int64,
	_ int,
) (*http.Response, error) {
	s.request = request
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	s.body = body
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Location": {"/backend-api/codex/call_test"},
		},
		Body: io.NopCloser(strings.NewReader("v=0\r\n")),
	}, nil
}

// DoWithTLS retains the supplied TLS profile and delegates captured HTTP
// exchange to Do. The profile pointer remains shared for assertions after the
// native creation call, which owns returned response cleanup.
func (s *liveHTTPUpstreamStub) DoWithTLS(
	request *http.Request,
	proxyURL string,
	providerID int64,
	providerConcurrency int,
	profile *tlsfingerprint.Profile,
) (*http.Response, error) {
	s.tlsProfile = profile
	return s.Do(request, proxyURL, providerID, providerConcurrency)
}

// TestLiveCapabilityOnlyAllowsOpenAIOAuth compares ordinary OpenAI OAuth with
// API Key, Grok OAuth, PAT and Agent Identity fixtures. Live capability
// accepts the ordinary OAuth case and rejects the other tested identities.
func TestLiveCapabilityOnlyAllowsOpenAIOAuth(t *testing.T) {
	require.True(t, provideradapter.SupportsOpenAIEndpoint(gatewayprovider.ExecutionProtocolRecord(&gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}), providercore.OpenAIEndpointCapabilityLive))
	require.False(t, provideradapter.SupportsOpenAIEndpoint(gatewayprovider.ExecutionProtocolRecord(&gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}), providercore.OpenAIEndpointCapabilityLive))
	require.False(t, provideradapter.SupportsOpenAIEndpoint(gatewayprovider.ExecutionProtocolRecord(&gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}), providercore.OpenAIEndpointCapabilityLive))
	require.False(t, provideradapter.SupportsOpenAIEndpoint(gatewayprovider.ExecutionProtocolRecord(&gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				providercore.OpenAIAuthModeCredentialKey: providercore.OpenAIAuthModePersonalAccessToken,
			},
		},
	}), providercore.OpenAIEndpointCapabilityLive))
	require.False(t, provideradapter.SupportsOpenAIEndpoint(gatewayprovider.ExecutionProtocolRecord(&gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				providercore.OpenAIAuthModeCredentialKey: providercore.OpenAIAuthModeAgentIdentity,
			},
		},
	}), providercore.OpenAIEndpointCapabilityLive))
}

// TestValidateLiveCallRequestDoesNotRequireDelegation validates nonblank SDP
// and a session lacking delegation. The shared validator accepts those
// settings and leaves the optional field absent.
func TestValidateLiveCallRequestDoesNotRequireDelegation(t *testing.T) {
	request := &gatewaysession.LiveCallRequest{
		SDP:     "v=0\r\n",
		Session: json.RawMessage(`{"model":"gpt-live-test","instructions":"hello"}`),
	}
	require.NoError(t, openaicore.ValidateLiveCallRequest(request))
	require.NotContains(t, string(request.Session), "delegation")
}

// TestCreateUpstreamLiveCallPreservesSession creates a native call with
// custom JSON session fields and routed TLS configuration. SDP, delegation
// and custom data survive, and captured authentication and Live identity
// headers match the fixture rule.
func TestCreateUpstreamLiveCallPreservesSession(t *testing.T) {
	upstream := &liveHTTPUpstreamStub{}
	profileService, routerService := newLiveTLSRoutingServices()
	service := newLiveFixture(liveFixtureInputs{transport: upstream, profiles: profileService, routers: routerService})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 7,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 2,
			Credentials: map[string]any{
				"access_token":       "test-access-token",
				"chatgpt_account_id": "acct_test",
			},
			Extra: map[string]any{
				"enable_tls_fingerprint":    true,
				"tls_fingerprint_router_id": int64(9),
			},
		},
	}
	session := json.RawMessage(`{
		"model":"gpt-live-test",
		"delegation":{"type":"client"},
		"custom":{"keep":true}
	}`)

	tlsRouterMatch := service.matchLiveTLSFingerprintRouter(provider, "test-live-client")
	created, err := service.createUpstreamLiveCall(context.Background(), provider, &gatewaysession.LiveCallRequest{
		SDP:     "v=offer\r\n",
		Session: session,
	}, `{"v":1,"s":0,"t":"v1.test"}`, tlsRouterMatch)
	require.NoError(t, err)
	require.Equal(t, "call_test", created.CallID)
	require.Equal(t, []byte("v=0\r\n"), created.SDP)
	require.NotNil(t, upstream.tlsProfile)
	require.Equal(t, "live-routed", upstream.tlsProfile.Name)

	var forwarded struct {
		SDP     string          `json:"sdp"`     // forwarded client session description
		Session json.RawMessage `json:"session"` // forwarded JSON session object
	}
	require.NoError(t, json.Unmarshal(upstream.body, &forwarded))
	require.Equal(t, "v=offer\r\n", forwarded.SDP)
	require.JSONEq(t, string(session), string(forwarded.Session))
	require.Equal(t, "Bearer test-access-token", upstream.request.Header.Get("Authorization"))
	require.Equal(t, "acct_test", upstream.request.Header.Get("Chatgpt-Account-Id"))
	require.Equal(t, "quicksilver=v2", upstream.request.Header.Get("OpenAI-Alpha"))
	require.Equal(t, "codex_vscode/0.144.1 live-test", upstream.request.Header.Get("User-Agent"))
	require.Equal(t, "codex_vscode", upstream.request.Header.Get("Originator"))
	require.Equal(t, `{"v":1,"s":0,"t":"v1.test"}`, upstream.request.Header.Get(openai.LiveAttestationHeader))
	require.NotEmpty(t, upstream.request.Header.Get("Session-Id"))
	require.NotEmpty(t, upstream.request.Header.Get("Thread-Id"))
	require.Empty(t, upstream.request.Header.Get("OpenAI-Beta"))
	require.Equal(t, upstreamcore.HTTPUpstreamProfileOpenAI, upstreamcore.HTTPUpstreamProfileFromContext(upstream.request.Context()))
	require.True(t, upstreamcore.HTTPUpstreamRedirectsDisabled(upstream.request.Context()))
}

// TestLiveClientPolicyUsesTLSRouterMatch checks allowed and unmatched client
// User-Agent values under router-only policy. The selected rule qualifies the
// allowed client, while the other receives the tested restriction reason.
func TestLiveClientPolicyUsesTLSRouterMatch(t *testing.T) {
	_, routerService := newLiveTLSRoutingServices()
	service := newLiveFixture(liveFixtureInputs{routers: routerService})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"tls_fingerprint_router_id":  int64(9),
				"openai_oauth_client_policy": providercore.OpenAIOAuthClientPolicyTLSRouterMatchedOnly,
			},
		},
	}

	matched := service.matchLiveTLSFingerprintRouter(provider, "test-live-client")
	result := service.liveClientPolicyResult(
		context.Background(),
		provider,
		gatewaysession.LiveCallIdentity{UserAgent: "test-live-client"},
		matched,
	)
	require.True(t, result.Enabled)
	require.True(t, result.Matched)

	notMatched := service.matchLiveTLSFingerprintRouter(provider, "unknown-client")
	result = service.liveClientPolicyResult(
		context.Background(),
		provider,
		gatewaysession.LiveCallIdentity{UserAgent: "unknown-client"},
		notMatched,
	)
	require.True(t, result.Enabled)
	require.False(t, result.Matched)
	require.Equal(t, providercore.CodexClientRestrictionReasonNotMatchedTLSRouter, result.Reason)
}

// TestLiveAttestationCipherRoundTripAndRejectsOtherInstanceKey encrypts one
// issued header with a production Live cipher. Its own key restores the
// plaintext, while a different configured secret rejects the ciphertext.
func TestLiveAttestationCipherRoundTripAndRejectsOtherInstanceKey(t *testing.T) {
	first := openai.NewLiveAttestationCipher("first-live-secret")
	second := openai.NewLiveAttestationCipher("second-live-secret")
	require.NotNil(t, first)
	require.NotNil(t, second)

	ciphertext, err := first.Encrypt(`{"v":1,"s":0,"t":"v1.opaque"}`)
	require.NoError(t, err)
	require.NotContains(t, ciphertext, "opaque")

	plaintext, err := first.Decrypt(ciphertext)
	require.NoError(t, err)
	require.Equal(t, `{"v":1,"s":0,"t":"v1.opaque"}`, plaintext)

	_, err = second.Decrypt(ciphertext)
	require.Error(t, err)
}

// TestPrepareLiveAttestationEncryptsHeaderAndReturnsExplicitProviderError
// pairs a generated header with encrypted saved data, then injects generation
// failure. Encryption round-trips the issued header and failure retains its
// typed unavailable reason.
func TestPrepareLiveAttestationEncryptsHeaderAndReturnsExplicitProviderError(t *testing.T) {
	cipher := openai.NewLiveAttestationCipher("live-attestation-test-secret")
	service := newLiveFixture(liveFixtureInputs{attestation: liveAttestationStub{header: `{"v":1,"s":0,"t":"v1.test"}`}, cipher: cipher})
	header, ciphertext, err := service.prepareLiveAttestation(context.Background())
	require.NoError(t, err)
	require.Equal(t, `{"v":1,"s":0,"t":"v1.test"}`, header)
	require.NotContains(t, ciphertext, "v1.test")
	decrypted, err := cipher.Decrypt(ciphertext)
	require.NoError(t, err)
	require.Equal(t, header, decrypted)

	service.Attestation = liveAttestationStub{err: errors.New("macOS app missing")}
	_, _, err = service.prepareLiveAttestation(context.Background())
	var unavailable *gatewaysession.LiveAttestationUnavailableError
	require.ErrorAs(t, err, &unavailable)
	require.Contains(t, unavailable.Error(), "macOS app missing")
}

// TestLiveMaxSessionDurationDefaultsAndOverrides compares absent duration
// with a configured positive lifetime. The executor uses its default in the
// first case and the supplied ninety-second value in the second.
func TestLiveMaxSessionDurationDefaultsAndOverrides(t *testing.T) {
	require.Equal(t, defaultLiveMaxSessionDuration, newLiveFixture(liveFixtureInputs{}).liveMaxSessionDuration())
	require.Equal(
		t,
		90*time.Second,
		newLiveFixture(liveFixtureInputs{duration: time.Duration(90) * time.Second}).liveMaxSessionDuration(),
	)
}

// TestLiveSidebandNormalCloseEndsCall maps normal upstream closure to the
// terminal-call sentinel. An internal-error close remains the original error
// for separate controller recovery handling.
func TestLiveSidebandNormalCloseEndsCall(t *testing.T) {
	normalClose := coderws.CloseError{Code: coderws.StatusNormalClosure}
	require.ErrorIs(t, liveSidebandReadError(normalClose), gatewaysession.ErrLiveCallNotFound)

	abnormalClose := coderws.CloseError{Code: coderws.StatusInternalError}
	require.Equal(t, abnormalClose, liveSidebandReadError(abnormalClose))
}

// TestLiveCreateFailoverUsesExistingOpenAIPolicy compares invalid-request,
// access, gateway and unclassified transport creation failures. The tested
// invalid request stops switching, while the other tested failures permit
// another provider.
func TestLiveCreateFailoverUsesExistingOpenAIPolicy(t *testing.T) {
	service := newLiveFixture(liveFixtureInputs{})
	require.False(t, service.shouldFailoverLiveCreateError(&forwardcore.UpstreamFailoverError{
		StatusCode:   http.StatusBadRequest,
		ResponseBody: []byte(`{"error":{"message":"invalid session"}}`),
	}))
	require.True(t, service.shouldFailoverLiveCreateError(&forwardcore.UpstreamFailoverError{
		StatusCode: http.StatusForbidden,
	}))
	require.True(t, service.shouldFailoverLiveCreateError(&forwardcore.UpstreamFailoverError{
		StatusCode: http.StatusBadGateway,
	}))
	require.True(t, service.shouldFailoverLiveCreateError(errors.New("transport failed")))
}

// TestLiveCallIDFromLocation reads absolute and relative upstream creation
// Locations. Both parsed call identifiers exclude surrounding service path
// and query metadata used by the control URL.
func TestLiveCallIDFromLocation(t *testing.T) {
	callID, err := openai.LiveCallIDFromLocation("https://chatgpt.com/backend-api/codex/call_123?intent=quicksilver")
	require.NoError(t, err)
	require.Equal(t, "call_123", callID)

	callID, err = openai.LiveCallIDFromLocation("/backend-api/codex/call_456")
	require.NoError(t, err)
	require.Equal(t, "call_456", callID)
}

// TestRequestTypeLive checks the saved usage enum's validity, string
// representation and parsing. The live label round-trips to the same request
// type used by zero-cost session observations.
func TestRequestTypeLive(t *testing.T) {
	require.True(t, usage.RequestTypeLive.IsValid())
	require.Equal(t, "live", usage.RequestTypeLive.String())
	parsed, err := usage.ParseUsageRequestType("live")
	require.NoError(t, err)
	require.Equal(t, usage.RequestTypeLive, parsed)
}
