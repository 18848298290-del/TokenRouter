package grok

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// realtime_connection.go opens Grok Realtime sessions and ties connection
// cleanup to activity release.
// A Grok Realtime connection keeps its activity registration for the entire
// session, including the relay that follows a successful handshake. Dialing
// builds the Voice model query and authentication headers before entering that
// activity, so target parsing or registration failures stop before a network
// connection is attempted.
//
// A failed handshake may still return a connection, and that connection is
// closed before activity is released. Successful dialing transfers both
// resources to RealtimeSession. Concurrent callers can close the session through
// one sync.Once operation, which records the connection's close error and
// releases activity after that close. Ready checks the retained connection
// pointer, so it is an initialization check rather than evidence that an
// already-closed socket remains open.

// RealtimeDialOptions supplies the Voice target, authentication and
// connection-registration callbacks.
type RealtimeDialOptions struct {
	// BaseURL and Model define the WebSocket target for one dial. DialRealtime
	// changes the scheme to wss and sets model in the existing query, using its
	// default when Model is blank.
	BaseURL, Model string // BaseURL: selected Voice URL; Model: selected Voice model
	Token          string `json:"-"` // bearer credential value
	// CLIHeaders and ApplyHeaders populate one newly allocated header map.
	// CLIHeaders runs for the CLI proxy host, and ApplyHeaders then applies the
	// caller's permitted overrides. CLIHeaders: subscription identity callback;
	// ApplyHeaders: final header callback
	CLIHeaders, ApplyHeaders func(http.Header)
	// Dial performs the required handshake with the prepared context, URL and
	// headers; a successful result needs a nonnil connection for session cleanup.
	Dial func(context.Context, string, http.Header) (upstream.FrameConn, int, error)
	// Enter registers session activity before dialing and returns a callable
	// release, transferred to the session after success.
	Enter func() (func(), error) // optional activity registration
}

// RealtimeDialError retains the handshake status alongside its underlying dial
// failure.
type RealtimeDialError struct {
	StatusCode int // handshake HTTP status
	// Err is nonnil when DialRealtime creates this wrapper and supplies the error
	// chain exposed by Unwrap.
	Err error // underlying dial error
}

// RealtimeSession owns a dialed frame connection and its activity release.
// Keep its fields stable and use the returned pointer throughout the session so
// concurrent Close calls share the same sync.Once and cached result.
type RealtimeSession struct {
	// FrameConn, done, once and closeErr participate in one session cleanup: once
	// closes FrameConn, saves closeErr and invokes done before repeated Close calls
	// return.
	upstream.FrameConn           // owned frame connection
	done               func()    // registered activity release
	once               sync.Once // session cleanup coordination
	closeErr           error     // cached connection-close error
}

// Error returns the underlying dial failure's message for ordinary error
// presentation. DialRealtime constructs this wrapper with a nonnil error;
// callers constructing one directly supply Err before requesting its message.
func (e *RealtimeDialError) Error() string { return e.Err.Error() }

// Unwrap exposes the underlying dial error to errors.Is and errors.As.
func (e *RealtimeDialError) Unwrap() error { return e.Err }

// Ready reports whether the session has a retained frame connection. Nil
// sessions return false. Closing does not clear FrameConn, so callers use this
// as an initialization check and manage open-state decisions themselves.
func (s *RealtimeSession) Ready() bool { return s != nil && s.FrameConn != nil }

// Close closes the retained connection and releases activity once across
// concurrent callers. It caches the first close error for later calls and
// returns nil for an uninitialized session. The connection is closed before its
// activity release runs.
func (s *RealtimeSession) Close() error {
	if !s.Ready() {
		return nil
	}
	// Connection closure and activity release share one operation so concurrent
	// callers cannot release registration while another close is still pending.
	s.once.Do(func() {
		s.closeErr = s.FrameConn.Close()
		if s.done != nil {
			s.done()
		}
	})
	return s.closeErr
}

// DialRealtime opens the supplied base as wss with the selected Voice model
// query. A blank model uses grok-voice-latest. Callers validate the endpoint,
// provide Dial, and close the returned session after use. Failed dialing closes
// any returned connection and releases activity; header overrides follow
// host-selected CLI identity.
func DialRealtime(ctx context.Context, options RealtimeDialOptions) (*RealtimeSession, error) {
	u, err := url.Parse(options.BaseURL)
	if err != nil {
		return nil, err
	}
	u.Scheme = "wss"
	query := u.Query()
	query.Set("model", firstNonEmpty(options.Model, "grok-voice-latest"))
	u.RawQuery = query.Encode()
	headers := http.Header{"Authorization": []string{"Bearer " + options.Token}}
	if options.CLIHeaders != nil && (MediaCodec{}).IsGrokCLIProxyTarget(u.String()) {
		options.CLIHeaders(headers)
	}
	if options.ApplyHeaders != nil {
		options.ApplyHeaders(headers)
	}
	var done func()
	if options.Enter != nil {
		done, err = options.Enter()
		if err != nil {
			return nil, err
		}
	}
	conn, status, err := options.Dial(ctx, u.String(), headers)
	if err != nil {
		// Failed handshakes can return a connection as well as an error; dispose of
		// that connection before releasing registration.
		if conn != nil {
			_ = conn.Close()
		}
		// No session is returned after a failed dial, so this path owns the
		// registration release that successful sessions defer.
		if done != nil {
			done()
		}
		return nil, &RealtimeDialError{StatusCode: status, Err: err}
	}
	return &RealtimeSession{FrameConn: conn, done: done}, nil
}

// ProbeRealtime checks dialing and closes a successfully initialized session
// before returning. Dial failures return the underlying error held by
// RealtimeDialError, while successful probes return the session's close error.
func ProbeRealtime(ctx context.Context, options RealtimeDialOptions) error {
	conn, err := DialRealtime(ctx, options)
	if err != nil {
		// Provider probes classify the original network failure, so unwrap the
		// handshake wrapper before handing that error to probe policy.
		if failure, ok := errors.AsType[*RealtimeDialError](err); ok {
			return failure.Err
		}
		return err
	}
	return conn.Close()
}
