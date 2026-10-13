package messageforward

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// privateHTTPBoundary provides recording HTTP output for internal runtime
// assertions.
type privateHTTPBoundary struct {
	// HTTPBoundary embeds unused fixture methods; invoking one of those
	// methods with the nil embedded interface exposes an unexpected test
	// call.
	HTTPBoundary // unused HTTP fixture methods
	// Request is optional; missing input yields fresh empty headers while
	// an installed request supplies borrowed headers to preparation.
	Request *http.Request // inbound HTTP fixture request
	// Writer is installed by newPrivateHTTPFixture and retained by output
	// methods; callers keep its recorder usable through synchronous
	// assertions.
	Writer *privateOutputSink // recording output sink pointer
}

// privateOutputSink records response bytes and its fixture write-state
// observations.
type privateOutputSink struct {
	recorder *httptest.ResponseRecorder // recorded HTTP response data
	written  bool                       // Begin or data-write marker
}

// requestBoundaryFixture supplies request headers to preparation tests through
// borrowed request data.
type requestBoundaryFixture struct {
	// HTTPBoundary embeds unused fixture methods; invoking one of those
	// methods with the nil embedded interface exposes an unexpected test
	// call.
	HTTPBoundary // unused HTTP fixture methods
	// Request is optional; a nil fixture or missing request selects empty
	// headers and skips request-dependent fingerprint preparation.
	Request *http.Request // inbound preparation test request
}

// betaSettingsFixture supplies mutable settings values for runtime policy
// tests.
type betaSettingsFixture struct {
	// RuntimeSettingsStore embeds unused repository methods; these fixtures
	// implement the reads exercised by gateway policy and leave unexpected
	// operations observable through nil dispatch.
	gateway.RuntimeSettingsStore // unused settings store methods
	// values retains the test map; policy fixtures change it and invalidate
	// the runtime settings reader before querying the changed values.
	values map[string]string // mutable setting fixture map
}

// Begin clones header value slices into the recorder and records the supplied
// status. The fixture marks written after Begin so runtime assertions can
// distinguish startup from an unstarted response.
func (s *privateOutputSink) Begin(head upstream.OutputHead) error {
	for name, values := range head.Header {
		s.recorder.Header()[name] = append([]string(nil), values...)
	}
	s.recorder.WriteHeader(head.Status)
	s.written = true
	return nil
}

// Emit writes nonempty event data and applies the requested flush to the
// recorder. Data marks fixture write state, while flush-only events leave that
// fixture marker unchanged.
func (s *privateOutputSink) Emit(event upstream.OutputEvent) error {
	if len(event.Data) > 0 {
		s.written = true
		if _, err := s.recorder.Write(event.Data); err != nil {
			return err
		}
	}
	if event.Flush {
		s.recorder.Flush()
	}
	return nil
}

// Written reports the fixture marker set by Begin or nonempty event data.
func (s *privateOutputSink) Written() bool { return s.written }

// newPrivateHTTPFixture creates a recording boundary backed by the supplied
// HTTP recorder. It returns no construction error, and callers install any
// request before using request-dependent preparation paths.
func newPrivateHTTPFixture(rec *httptest.ResponseRecorder) (*privateHTTPBoundary, error) {
	return &privateHTTPBoundary{Writer: &privateOutputSink{recorder: rec}}, nil
}

// Present reports HTTP output as available for this internal recording fixture.
func (b *privateHTTPBoundary) Present() bool { return true }

// RequestPresent reports whether the recording fixture retains an inbound HTTP
// request.
func (b *privateHTTPBoundary) RequestPresent() bool { return b.Request != nil }

// RequestHeaders returns borrowed headers from the installed fixture request.
// An absent request produces a new empty header map, so preparation tests can
// exercise missing-header defaults without installing headers.
func (b *privateHTTPBoundary) RequestHeaders() http.Header {
	if b.Request == nil {
		return http.Header{}
	}
	return b.Request.Header
}

// HasHeaderFilter reports filtering as disabled for this internal recording
// fixture.
func (b *privateHTTPBoundary) HasHeaderFilter() bool { return false }

// Sink returns the recording output writer retained by this boundary fixture.
func (b *privateHTTPBoundary) Sink() upstream.OutputSink { return b.Writer }

// Written returns the recording writer marker used by runtime output
// assertions.
func (b *privateHTTPBoundary) Written() bool { return b.Writer.Written() }

// Size returns recorder body length after fixture output has been marked
// written. An unstarted fixture returns minus one, matching the writer sentinel
// used by attempt output decisions.
func (b *privateHTTPBoundary) Size() int {
	if !b.Written() {
		return -1
	}
	return b.Writer.recorder.Body.Len()
}

// ServiceTier reports an empty tier for the internal recording fixture.
func (b *privateHTTPBoundary) ServiceTier() string { return "" }

// MarkPassthrough accepts the runtime observation without recording passthrough
// fixture state.
func (b *privateHTTPBoundary) MarkPassthrough() {}

// Observe accepts diagnostic notices that these internal output fixtures do not
// record.
func (b *privateHTTPBoundary) Observe(forwardcore.Notice) {}

// SetError accepts diagnostic details that these internal output fixtures do
// not record.
func (b *privateHTTPBoundary) SetError(int, string, string) {}

// MatchRule returns nil for every internal fixture rule lookup. Runtime error
// handling therefore exercises its default presentation choices unless an
// external HTTP fixture binds an actual rule service.
func (b *privateHTTPBoundary) MatchRule(string, int, []byte) *errorpolicy.ErrorPassthroughRule {
	return nil
}

// Commit marks fixture output as written for semantic commitment assertions.
func (b *privateHTTPBoundary) Commit() { b.Writer.written = true }

// MessageError records the supplied error status and marks fixture output
// written.
func (b *privateHTTPBoundary) MessageError(status int, _, _ string) {
	b.Writer.recorder.WriteHeader(status)
	b.Writer.written = true
}

// RawError records supplied status and body bytes for internal error-output
// assertions.
func (b *privateHTTPBoundary) RawError(status int, body []byte) {
	b.Writer.recorder.WriteHeader(status)
	_, _ = b.Writer.recorder.Write(body)
	b.Writer.written = true
}

// WriteHeaders clones upstream header slices into the fixture destination map.
// These tests bypass mode-specific filtering, and callers supply a destination
// map ready for writes during response handling.
func (b *privateHTTPBoundary) WriteHeaders(dst, src http.Header, _ bool) {
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
}

// ReadResponseBody delegates bounded reading to the HTTP client helper for
// internal fixtures. BodyKind is accepted but does not select presentation
// here; the caller owns closing the supplied reader.
func (b *privateHTTPBoundary) ReadResponseBody(reader io.Reader, limit int64, _ BodyKind) ([]byte, error) {
	return httpclient.ReadResponseBodyLimited(reader, limit)
}

// Present reports whether the preparation fixture pointer exists. A nil
// receiver returns false so OAuth preparation tests can intentionally run the
// path with no HTTP observation context.
func (b *requestBoundaryFixture) Present() bool { return b != nil }

// RequestPresent reports whether the preparation fixture and its request both
// exist. A nil fixture or missing request returns false, allowing callers to
// avoid request-dependent metadata lookup.
func (b *requestBoundaryFixture) RequestPresent() bool { return b != nil && b.Request != nil }

// RequestHeaders returns borrowed inbound headers for an installed preparation
// request. Nil fixture or absent request returns a fresh empty map, so builders
// receive their normal missing-header input.
func (b *requestBoundaryFixture) RequestHeaders() http.Header {
	if !b.RequestPresent() {
		return http.Header{}
	}
	return b.Request.Header
}

// GetValue returns a configured fixture string or ErrSettingNotFound for a
// missing key. The values map is borrowed, letting tests alter settings and
// invalidate the runtime reader before another query.
func (s betaSettingsFixture) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := s.values[key]; ok {
		return value, nil
	}
	return "", settings.ErrSettingNotFound
}

// newBetaRuntime constructs gateway settings with the mutable fixture map and
// Anthropic beta defaults. Tests retain the map and invalidate the appropriate
// reader after modifying values between policy assertions.
func newBetaRuntime(values map[string]string) *gateway.RuntimeSettings {
	return gateway.NewRuntimeSettings(betaSettingsFixture{values: values}, settings.ErrSettingNotFound, func() *gateway.BetaPolicySettings {
		return gatewayprovider.GatewayBetaPolicy(anthropic.DefaultBetaPolicySettings())
	})
}

// GetMultiple returns a fresh map containing configured values for requested
// keys. Missing keys are omitted, and string values remain valid after tests
// change the fixture map and invalidate readers.
func (s betaSettingsFixture) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string)
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}
