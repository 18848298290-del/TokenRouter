package live

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// cancelClaimStore cancels the request after ownership is claimed to test
// subsequent cleanup.
type cancelClaimStore struct {
	session.LiveCallStore                    // unused inherited call-store fixture operations
	cancel                context.CancelFunc // request cancellation invoked by claim
	released              atomic.Int32       // successful controller-release callback count
}

// controllerPorts counts target reads while returning the cancellation-test
// call store.
type controllerPorts struct {
	Ports                         // unused inherited Live runtime operations
	store   session.LiveCallStore // retained call-store fixture for lookup
	queries atomic.Int32          // attempted control target read count
}

// idleFrames supplies a control transport that waits for request cancellation.
type idleFrames struct{}

// finalizeStore allows one close claim, guarding zero-cost recording and lease
// release.
type finalizeStore struct {
	session.LiveCallStore             // unused inherited call-store fixture operations
	closed                atomic.Bool // successful closure claim was consumed
}

// finalLeases counts Live lease release attempts for finalization assertions.
type finalLeases struct {
	scheduler.LiveConcurrencyCache              // unused inherited Live lease operations
	released                       atomic.Int32 // attempted Live lease-release callback count
}

// finalizePorts supplies claim and lease fixtures and counts zero-usage
// callbacks.
type finalizePorts struct {
	Ports                 // unused inherited Live runtime operations
	store  *finalizeStore // retained call-store fixture for lookup
	leases *finalLeases   // retained lease release counter fixture
	writes atomic.Int32   // attempted zero-usage recording callback count
}

// ClaimLiveController cancels the request before returning a successful
// controller claim.
func (s *cancelClaimStore) ClaimLiveController(context.Context, string, string, string) (bool, error) {
	s.cancel()
	return true, nil
}

// ReleaseLiveController counts cleanup only when its supplied context remains
// active. Canceled cleanup returns the context error; the test verifies proxy
// release receives a separate cleanup budget.
func (s *cancelClaimStore) ReleaseLiveController(ctx context.Context, _ string, _ string) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	s.released.Add(1)
	return true, nil
}

// Store returns the call-store fixture used for controller-claim cancellation
// assertions.
func (p *controllerPorts) Store() (session.LiveCallStore, error) { return p.store, nil }

// Target counts unexpected target access and returns its diagnostic error.
func (p *controllerPorts) Target(context.Context, *session.LiveCallRecord) (Target, error) {
	p.queries.Add(1)
	return nil, errors.New("unexpected target read")
}

// BeginObserver refuses background observer admission for this controller
// cancellation fixture.
func (p *controllerPorts) BeginObserver(string) (context.Context, func(), bool) {
	return nil, nil, false
}

// ReadFrame waits for its supplied context to be canceled and returns that
// error. The fixture emits no control frame, keeping the cancellation path
// separate from payload forwarding.
func (idleFrames) ReadFrame(ctx context.Context) (int, []byte, error) {
	// Keep the fixture read pending until cancellation so no payload result can
	// win this test's termination decision before the context is canceled.
	<-ctx.Done()
	return 0, nil, ctx.Err()
}

// WriteFrame accepts control writes for the idle cancellation transport
// fixture.
func (idleFrames) WriteFrame(context.Context, int, []byte) error { return nil }

// SetReadLimit accepts the configured byte limit for the idle transport
// fixture.
func (idleFrames) SetReadLimit(int64) {}

// Close accepts cleanup of the idle control transport fixture.
func (idleFrames) Close() error { return nil }

// TestSidebandCancelledAfterClaimReleasesWithoutTarget cancels the context
// inside a successful controller claim. The proxy must release that claim
// using an active cleanup context and skip provider target reads.
func TestSidebandCancelledAfterClaimReleasesWithoutTarget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &cancelClaimStore{cancel: cancel}
	ports := &controllerPorts{store: store}
	// Configure one MiB for the service's inbound client frame limit.
	core := New(ports, time.Second, 1<<20)
	err := core.ProxyLiveSideband(ctx, &session.LiveCallRecord{CallHash: "call", ExpiresAt: time.Now().Add(time.Hour)}, idleFrames{})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int32(0), ports.queries.Load())
	require.Equal(t, int32(1), store.released.Load())
}

// MarkLiveCallClosed allows the first atomic close claim and refuses later
// claims. Concurrent claim calls share its atomic flag; storage retention
// arguments are accepted without implementing expiry in this fixture.
func (s *finalizeStore) MarkLiveCallClosed(context.Context, string, time.Duration) (bool, error) {
	return s.closed.CompareAndSwap(false, true), nil
}

// ReleaseLiveLease counts lease cleanup requested by a winning finalization
// claim.
func (s *finalLeases) ReleaseLiveLease(context.Context, int64, int64, int64, string) error {
	s.released.Add(1)
	return nil
}

// Store returns the close-claim fixture used for serial finalization
// assertions.
func (p *finalizePorts) Store() (session.LiveCallStore, error) { return p.store, nil }

// Leases returns the shared fixture counting finalization lease release
// attempts.
func (p *finalizePorts) Leases() (scheduler.LiveConcurrencyCache, error) { return p.leases, nil }

// RecordZeroUsage counts the recording callback permitted by a winning close
// claim.
func (p *finalizePorts) RecordZeroUsage(context.Context, *session.LiveCallRecord, int) {
	p.writes.Add(1)
}

// TestFinalizeClaimsOnceBeforeLeaseAndZeroUsage invokes Finalize twice against
// one call and closure flag. The single accepted claim must yield one
// zero-usage callback and one lease release.
func TestFinalizeClaimsOnceBeforeLeaseAndZeroUsage(t *testing.T) {
	ports := &finalizePorts{store: &finalizeStore{}, leases: &finalLeases{}}
	// Configure one MiB for the service's inbound client frame limit.
	core := New(ports, time.Second, 1<<20)
	record := &session.LiveCallRecord{CallHash: "call", ProviderID: 1, UserID: 2, APIKeyID: 3, LeaseID: "lease", CreatedAt: time.Now()}
	core.Finalize(record)
	core.Finalize(record)
	require.Equal(t, int32(1), ports.writes.Load())
	require.Equal(t, int32(1), ports.leases.released.Load())
}
