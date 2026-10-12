package media

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// mediaFrameStub records connection close attempts in the shared fixture
// event sequence.
type mediaFrameStub struct {
	closed int // number of fixture close invocations
	// order points at the admission fixture's event slice. Close appends
	// directly through that pointer, so the tests use this frame connection
	// serially.
	order *[]string // shared fixture cleanup event sink
}

// realtimePortsStub injects credential, wait and handshake behavior for
// admission assertions.
type realtimePortsStub struct {
	// selected supplies candidate IDs; released records slot cleanup, opened
	// records handshake calls and failed records reporting. Serial tests
	// compare these separate counts across failed and successful admission
	// phases.
	selected, released, opened, failed int  // candidate sequence number, release callback invocations, upstream handshake calls and reported handshake failures
	denyWait                           bool // injected provider slot rejection
	credentialFailure                  bool // inject credential lookup error
	firstDialFailure                   bool // fail only the first handshake
	// conn is installed by successful OpenRealtime and returned through the
	// lease. The test retains the same pointer to inspect its close count
	// after lease cleanup.
	conn *mediaFrameStub // shared returned frame connection fixture
	// order is shared with mediaFrameStub through a pointer and appended by
	// close and release callbacks. Tests use it serially to verify
	// transport-before-slot cleanup order.
	order    []string      // shared cleanup event sequence
	deadline time.Duration // observed handshake deadline remaining duration
}

// ReadFrame returns a text-frame kind and an injected cancellation error.
// It ignores context and reads no data; admission tests use the fixture for
// cleanup rather than message exchange.
func (f *mediaFrameStub) ReadFrame(context.Context) (upstream.FrameKind, []byte, error) {
	return upstream.FrameText, nil, context.Canceled
}

// WriteFrame accepts every fixture write and returns nil without retaining
// data. The admission tests do not exercise message transport, so this
// method supplies the required connection shape.
func (f *mediaFrameStub) WriteFrame(context.Context, upstream.FrameKind, []byte) error { return nil }

// Close records a close count and shared order event, then succeeds. Each
// call records again; the owning RealtimeLease, rather than this frame
// fixture, supplies repeated-close coordination.
func (f *mediaFrameStub) Close() error { f.closed++; *f.order = append(*f.order, "close"); return nil }

// SelectRealtime returns increasing provider IDs while counting selection
// calls. It ignores ctx and exclusions, letting these tests isolate
// admission ownership rather than enforce actual routing eligibility or
// cancellation.
func (p *realtimePortsStub) SelectRealtime(_ context.Context, _ map[int64]struct{}) (provider.ProviderSnapshot, bool, error) {
	p.selected++
	return provider.ProviderSnapshot{ID: int64(p.selected)}, true, nil
}

// AcquireRealtime rejects when denyWait is set and otherwise returns a
// counting release callback. Release appends the order event used to
// compare failed-attempt cleanup with successful lease cleanup.
func (p *realtimePortsStub) AcquireRealtime(context.Context, provider.ProviderSnapshot) (func(), bool) {
	if p.denyWait {
		return nil, false
	}
	return func() { p.released++; p.order = append(p.order, "release") }, true
}

// RealtimeCredential returns an injected credential error or the fixture
// token. It ignores the provider and context; admission tests use that
// error to force slot release before any dial.
func (p *realtimePortsStub) RealtimeCredential(context.Context, provider.ProviderSnapshot) (string, error) {
	if p.credentialFailure {
		return "", errors.New("credential")
	}
	return "secret", nil
}

// OpenRealtime captures the remaining supplied deadline and optionally
// fails the first dial. On success it installs a frame fixture sharing the
// order slice, which remains available for lease-close assertions.
func (p *realtimePortsStub) OpenRealtime(ctx context.Context, _ provider.ProviderSnapshot, _, _ string) (upstream.FrameConn, error) {
	p.opened++
	deadline, _ := ctx.Deadline()
	p.deadline = time.Until(deadline)
	if p.firstDialFailure && p.opened == 1 {
		return nil, errors.New("dial")
	}
	p.conn = &mediaFrameStub{order: &p.order}
	return p.conn, nil
}

// RealtimeOpenFailed increments the reported handshake-failure count for
// admission assertions.
func (p *realtimePortsStub) RealtimeOpenFailed(context.Context, provider.ProviderSnapshot, error) {
	p.failed++
}

// TestRealtimeAdmissionKeepsUpstreamBeforeAcceptAndReleaseOrder fails one
// handshake before opening a usable lease. It checks the dial deadline
// range, repeated lease close, and the event sequence that closes transport
// before successful slot release.
func TestRealtimeAdmissionKeepsUpstreamBeforeAcceptAndReleaseOrder(t *testing.T) {
	ports := &realtimePortsStub{firstDialFailure: true}
	result := OpenRealtime(context.Background(), "voice", 12*time.Second, ports)
	require.NotNil(t, result.Lease)
	require.Equal(t, 2, ports.selected)
	require.Equal(t, 1, ports.released)
	require.Equal(t, 1, ports.failed)
	// Allow one second for handshake setup before measuring the supplied
	// twelve-second deadline.
	require.Greater(t, ports.deadline, 11*time.Second)
	require.LessOrEqual(t, ports.deadline, 12*time.Second)
	// Invoke lease cleanup twice before inspecting transport and slot counts,
	// exercising the lease's once guard rather than fixture-side idempotence.
	require.NoError(t, result.Lease.Close())
	require.NoError(t, result.Lease.Close())
	require.Equal(t, 1, ports.conn.closed)
	require.Equal(t, 2, ports.released)
	require.Equal(t, []string{"release", "close", "release"}, ports.order)
}

// TestRealtimeAdmissionCredentialFailureAndWaiting rejects credentials
// across four selections, then rejects one slot wait. It checks candidate
// and wait evidence, release counts and the absence of any upstream
// handshake on both paths.
func TestRealtimeAdmissionCredentialFailureAndWaiting(t *testing.T) {
	ports := &realtimePortsStub{credentialFailure: true}
	result := OpenRealtime(context.Background(), "voice", time.Second, ports)
	require.Nil(t, result.Lease)
	require.True(t, result.CandidateSeen)
	require.Equal(t, 4, ports.selected)
	require.Equal(t, 4, ports.released)
	require.Zero(t, ports.opened)
	waiting := &realtimePortsStub{denyWait: true}
	result = OpenRealtime(context.Background(), "voice", time.Second, waiting)
	require.True(t, result.WaitRejected)
	require.Zero(t, waiting.opened)
	require.Zero(t, waiting.released)
}
