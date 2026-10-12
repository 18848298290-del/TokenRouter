package media

import (
	"context"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Realtime admission opens the upstream connection before the HTTP adapter
// upgrades the client connection. A successful lease therefore retains both
// upstream transport and provider admission, while a separate result
// distinguishes candidate discovery from a wait-layer rejection.
//
// Credential and dial failures release the selected slot and exclude its
// provider before another candidate is considered. The handshake timeout
// derives from the caller's value and is canceled after dialing; it does
// not time the established session. Lease close coordinates competing
// cleanup calls with sync.Once, closing upstream before returning its slot
// and retaining the first close error.

// DefaultRealtimeDialTimeout is the caller-selected twelve-second limit for
// upstream handshake work before downstream upgrade. Established sessions
// have separate lifetime control.
const DefaultRealtimeDialTimeout = 12 * time.Second

// RealtimePorts supplies upstream realtime admission, credentials, dialing
// and failure reporting.
type RealtimePorts interface {
	// SelectRealtime chooses a realtime provider under ctx and excluded
	// provider IDs. Error or absence ends admission with accumulated candidate
	// evidence; the returned result carries no selection error.
	SelectRealtime(context.Context, map[int64]struct{}) (provider.ProviderSnapshot, bool, error)
	// AcquireRealtime waits for the selected provider slot and returns
	// admission. True requires a callable release for subsequent
	// credential/dial failure; false sets WaitRejected and ends admission
	// before any handshake.
	AcquireRealtime(context.Context, provider.ProviderSnapshot) (func(), bool)
	// RealtimeCredential supplies authorization after a provider slot has been
	// acquired. Failure releases that slot and excludes the provider, allowing
	// another candidate within the fixed four-selection allowance.
	RealtimeCredential(context.Context, provider.ProviderSnapshot) (string, error)
	// OpenRealtime performs the upstream handshake using a temporary timeout
	// context, selected provider, credential and model. Nil error requires a
	// nonnil connection; the caller cancels that context immediately after the
	// method returns.
	OpenRealtime(context.Context, provider.ProviderSnapshot, string, string) (upstream.FrameConn, error)
	// RealtimeOpenFailed reports a failed upstream handshake before admission
	// releases the provider slot. The callback receives the parent context and
	// owns scheduling or observation effects of that reported failure.
	RealtimeOpenFailed(context.Context, provider.ProviderSnapshot, error)
}

// RealtimeLease owns one admitted upstream connection until coordinated
// cleanup completes.
type RealtimeLease struct {
	Provider provider.ProviderSnapshot // selected realtime provider snapshot
	// Conn belongs to the lease until Close finishes. The HTTP adapter
	// coordinates session I/O with closure and closes the downstream
	// connection under its separate ownership.
	Conn upstream.FrameConn // owned upstream realtime frame connection
	// release returns the retained provider slot after Conn.Close is
	// attempted. The lease installs it only for successful admission, and
	// sync.Once guards it with transport cleanup.
	release func() // provider slot release callback
	// once and err coordinate cleanup and its returned result. Close sets err
	// inside the once callback, and each caller reads it only after that
	// callback has finished.
	once sync.Once // shared lease cleanup coordination
	err  error     // retained upstream close error
}

// RealtimeAdmission records candidate and wait evidence with an optional
// owned connection lease.
type RealtimeAdmission struct {
	// Lease is nonnil after usable connection and release results have been
	// obtained. Its caller owns Close; a nil lease requires interpreting
	// CandidateSeen and WaitRejected for final handling.
	Lease         *RealtimeLease // optional admitted upstream connection lease
	CandidateSeen bool           // at least one candidate selected
	WaitRejected  bool           // selected provider admission was rejected
}

// Close releases a nonnil realtime lease after its upstream connection
// closes. sync.Once coordinates repeated or concurrent cleanup, retaining
// the first transport close error for every caller.
//
// The connection is closed before the provider slot is released, including
// when transport close returns an error. The HTTP adapter owns downstream
// cleanup separately and coordinates active readers or writers with session
// shutdown. A zero-value lease has no connection or slot to release; a nil
// receiver is not accepted.
func (l *RealtimeLease) Close() error {
	// Serialize connection close and slot release as one lease cleanup
	// sequence, so competing cleanup calls observe the same retained error and
	// cannot release twice.
	l.once.Do(func() {
		if l.Conn != nil {
			l.err = l.Conn.Close()
		}
		if l.release != nil {
			l.release()
		}
	})
	return l.err
}

// OpenRealtime attempts at most four realtime candidates before downstream
// upgrade, returning an owned lease when upstream dialing succeeds. The
// caller supplies a nonnil ctx, a handshake timeout and valid port
// callbacks; dialTimeout is used directly, with no local default.
//
// Each acquired slot requires a usable release callback for credential or
// dial errors. Those errors release and exclude the provider, while wait
// rejection returns immediately. Nil-error dialing requires nonnil
// connection and release results; malformed success returns a nil lease
// without cleaning those results. A successful lease keeps upstream
// transport and slot ownership until Close.
func OpenRealtime(ctx context.Context, model string, dialTimeout time.Duration, ports RealtimePorts) RealtimeAdmission {
	excluded := make(map[int64]struct{})
	result := RealtimeAdmission{}
	// Four candidate selections bound upstream realtime admission retries.
	for range 4 {
		candidate, present, err := ports.SelectRealtime(ctx, excluded)
		if err != nil || !present {
			break
		}
		result.CandidateSeen = true
		release, acquired := ports.AcquireRealtime(ctx, candidate)
		if !acquired {
			result.WaitRejected = true
			return result
		}
		token, err := ports.RealtimeCredential(ctx, candidate)
		// Credential failure leaves no usable upstream session, so release its
		// admitted provider slot and exclude that provider before selecting
		// another candidate.
		if err != nil {
			release()
			excluded[candidate.ID] = struct{}{}
			continue
		}
		// Bound only upstream dialing with the supplied timeout, then cancel its
		// temporary context before retaining a successful session under separate
		// lease ownership.
		probeCtx, cancel := context.WithTimeout(ctx, dialTimeout)
		conn, err := ports.OpenRealtime(probeCtx, candidate, token, model)
		cancel()
		// Report the handshake failure before returning its provider slot, then
		// exclude that provider so another selection can attempt independent
		// credentials and transport.
		if err != nil {
			ports.RealtimeOpenFailed(ctx, candidate, err)
			release()
			excluded[candidate.ID] = struct{}{}
			continue
		}
		if release == nil || conn == nil {
			return result
		}
		result.Lease = &RealtimeLease{Provider: candidate, Conn: conn, release: release}
		return result
	}
	return result
}
