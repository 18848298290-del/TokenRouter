package grok

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

type RealtimeDialOptions struct {
	BaseURL, Model           string
	Token                    string `json:"-"`
	CLIHeaders, ApplyHeaders func(http.Header)
	Dial                     func(context.Context, string, http.Header) (upstream.FrameConn, int, error)
	Enter                    func() (func(), error)
}
type RealtimeDialError struct {
	StatusCode int
	Err        error
}

type RealtimeSession struct {
	upstream.FrameConn
	done     func()
	once     sync.Once
	closeErr error
}

func (e *RealtimeDialError) Error() string { return e.Err.Error() }
func (e *RealtimeDialError) Unwrap() error { return e.Err }

func (s *RealtimeSession) Ready() bool { return s != nil && s.FrameConn != nil }
func (s *RealtimeSession) Close() error {
	if !s.Ready() {
		return nil
	}
	s.once.Do(func() {
		s.closeErr = s.FrameConn.Close()
		if s.done != nil {
			s.done()
		}
	})
	return s.closeErr
}

// DialRealtime opens a WebSocket using the supplied target and headers. Callers check provider
// access and pricing, then close the returned session to release its activity registration.
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
		if conn != nil {
			_ = conn.Close()
		}
		if done != nil {
			done()
		}
		return nil, &RealtimeDialError{StatusCode: status, Err: err}
	}
	return &RealtimeSession{FrameConn: conn, done: done}, nil
}

// ProbeRealtime closes a successful connection and unwraps dial failures.
func ProbeRealtime(ctx context.Context, options RealtimeDialOptions) error {
	conn, err := DialRealtime(ctx, options)
	if err != nil {
		if failure, ok := errors.AsType[*RealtimeDialError](err); ok {
			return failure.Err
		}
		return err
	}
	return conn.Close()
}
