package googleforward

import (
	"errors"
	"net/http"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// antigravity_response.go binds Antigravity response conversion to attempt
// output and tool restoration.
// Antigravity response modes share attempt tool restoration and HTTP error
// presentation. The response options install semantic commitment and typed
// failover callbacks, while stream limits are supplied when startup
// configuration is present and pure image/unwrap operations can use no output.

// antigravityResponseAdapter binds response presentation to the current attempt
// output and tool map. Configured options supply stream limits; a nil attempt
// supports pure extraction methods, while output callbacks require a usable
// attempt.
func (s *Antigravity) antigravityResponseAdapter(c *attempt) *antigravity.ResponseAdapter {
	opts := antigravity.ResponseOptions{
		ReverseTools: func(body []byte) []byte { return c.reverseTools(body) },
		ClaudeError:  func(status int, kind, message string) error { return c.ClaudeError(status, kind, message) },
		CompatError: func(status int, kind, message string) error {
			return c.AntigravityCompatError(status, kind, message)
		},
		MapCollectionError: func(err error) error { return c.MapAntigravityCollectionError(err) },
		Failover: func(body []byte) error {
			return &forwardcore.UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: body, RetryableOnSameProvider: true}
		},
		IsFailover:    func(err error) bool { var value *forwardcore.UpstreamFailoverError; return errors.As(err, &value) },
		MarkCommitted: func() { c.Commit() },
	}
	if s.Options.Configured {
		opts.MaxLineSize = s.Options.MaxLineSize
		opts.StreamDataIntervalTimeout = s.Options.StreamInterval
		opts.StreamKeepaliveInterval = s.Options.StreamKeepalive
	}
	return &antigravity.ResponseAdapter{Options: opts}
}

// extractImageInputSize delegates request image-size extraction through the
// platform response adapter.
func (s *Antigravity) extractImageInputSize(body []byte) string {
	return s.antigravityResponseAdapter(nil).ExtractImageInputSize(body)
}

// unwrapV1InternalResponse delegates single-envelope extraction to the platform
// response adapter.
func (s *Antigravity) unwrapV1InternalResponse(body []byte) ([]byte, error) {
	return s.antigravityResponseAdapter(nil).UnwrapV1InternalResponse(body)
}
