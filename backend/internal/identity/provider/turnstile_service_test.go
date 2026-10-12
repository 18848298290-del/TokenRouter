package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// TurnstileServiceSuite runs form and response checks through an in-process HTTP transport.
type TurnstileServiceSuite struct {
	suite.Suite                    // assertions and suite lifecycle
	ctx         context.Context    // verification request context value
	verifier    *turnstileVerifier // adapter under current test
	// received holds one captured form until the test reads it. In-process
	// handlers run synchronously, so buffering lets verification finish first.
	received chan url.Values
}

// roundTripFunc implements HTTP transport behavior with a fixture function.
type roundTripFunc func(*http.Request) (*http.Response, error)

// TestVerifyToken_SendsFormAndDecodesJSON captures the form sent through the
// in-process handler. Verification returns the decoded success response, and the
// secret, challenge, and remote address retain their supplied values.
func (s *TurnstileServiceSuite) TestVerifyToken_SendsFormAndDecodesJSON() {
	s.setupTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The buffered channel retains the captured form until assertions read it
		// after VerifyToken returns, so the synchronous handler can finish its response.
		// The adapter supplies an encoded form; this capture keeps available bytes
		// and parsed values while discarding read/query errors for the later field assertions.
		body, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(body))
		s.received <- values

		w.Header().Set("Content-Type", "application/json")
		// Discard fixture encoder errors and inspect the decoded adapter response
		// after the synchronous handler returns, as the observation of successful verification transport.
		_ = json.NewEncoder(w).Encode(identity.TurnstileVerifyResponse{Success: true})
	}))

	resp, err := s.verifier.VerifyToken(s.ctx, "sk", "token", "1.1.1.1")
	require.NoError(s.T(), err, "VerifyToken")
	require.NotNil(s.T(), resp)
	require.True(s.T(), resp.Success, "expected success response")

	// Read the buffered capture after request completion so the assertions examine
	// the form actually handled by the fixture, including all three submitted fields.
	select {
	case values := <-s.received:
		require.Equal(s.T(), "sk", values.Get("secret"))
		require.Equal(s.T(), "token", values.Get("response"))
		require.Equal(s.T(), "1.1.1.1", values.Get("remoteip"))
	default:
		require.Fail(s.T(), "expected server to receive request")
	}
}

// TestVerifyToken_ContentType handles a verification request with the in-process
// transport. The recorded Content-Type begins with the form-encoding media type
// needed to interpret the challenge parameters.
func (s *TurnstileServiceSuite) TestVerifyToken_ContentType() {
	var contentType string
	s.setupTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		// Discard fixture encoder errors and inspect the decoded adapter response
		// after the synchronous handler returns, as the observation of successful verification transport.
		_ = json.NewEncoder(w).Encode(identity.TurnstileVerifyResponse{Success: true})
	}))

	_, err := s.verifier.VerifyToken(s.ctx, "sk", "token", "1.1.1.1")
	require.NoError(s.T(), err)
	require.True(s.T(), strings.HasPrefix(contentType, "application/x-www-form-urlencoded"), "unexpected content-type: %s", contentType)
}

// TestVerifyToken_EmptyRemoteIP_NotSent supplies an empty remote address to the
// verification adapter. The captured form returns an empty remoteip lookup while
// the request and successful JSON response still complete.
func (s *TurnstileServiceSuite) TestVerifyToken_EmptyRemoteIP_NotSent() {
	s.setupTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Buffer the capture before this synchronous handler returns its response,
		// allowing the empty-IP assertion to inspect the submitted form after verification.
		// The adapter supplies an encoded form; this capture keeps available bytes
		// and parsed values while discarding read/query errors for the later field assertions.
		body, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(body))
		s.received <- values

		w.Header().Set("Content-Type", "application/json")
		// Discard fixture encoder errors and inspect the decoded adapter response
		// after the synchronous handler returns, as the observation of successful verification transport.
		_ = json.NewEncoder(w).Encode(identity.TurnstileVerifyResponse{Success: true})
	}))

	_, err := s.verifier.VerifyToken(s.ctx, "sk", "token", "")
	require.NoError(s.T(), err)

	select {
	case values := <-s.received:
		require.Equal(s.T(), "", values.Get("remoteip"), "remoteip should be empty or not sent")
	default:
		require.Fail(s.T(), "expected server to receive request")
	}
}

// TestVerifyToken_RequestError makes the fixture transport fail before returning
// an HTTP response. VerifyToken propagates an error to its caller for a challenge
// whose result could not be retrieved.
func (s *TurnstileServiceSuite) TestVerifyToken_RequestError() {
	s.verifier.verifyURL = "http://in-process/turnstile"
	s.verifier.httpClient = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed")
		}),
	}

	_, err := s.verifier.VerifyToken(s.ctx, "sk", "token", "1.1.1.1")
	require.Error(s.T(), err, "expected error when server is closed")
}

// TestVerifyToken_InvalidJSON returns a body outside the expected JSON format.
// The verification request returns an error, so malformed fixture data cannot
// pass this case as a successful verification result.
func (s *TurnstileServiceSuite) TestVerifyToken_InvalidJSON() {
	s.setupTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Supply malformed response bytes while discarding fixture write errors, so
		// the assertion observes VerifyToken's failure to decode the result it receives.
		_, _ = io.WriteString(w, "not-valid-json")
	}))

	_, err := s.verifier.VerifyToken(s.ctx, "sk", "token", "1.1.1.1")
	require.Error(s.T(), err, "expected error for invalid JSON response")
}

// TestVerifyToken_SuccessFalse returns a decoded provider rejection and its error
// code. The adapter returns those values with a nil transport error for the identity
// service to interpret as the challenge decision.
func (s *TurnstileServiceSuite) TestVerifyToken_SuccessFalse() {
	s.setupTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Encoder errors are discarded in this fixture; the caller checks that a
		// received rejection still returns its provider fields with a nil transport error.
		_ = json.NewEncoder(w).Encode(identity.TurnstileVerifyResponse{
			Success:    false,
			ErrorCodes: []string{"invalid-input-response"},
		})
	}))

	resp, err := s.verifier.VerifyToken(s.ctx, "sk", "token", "1.1.1.1")
	require.NoError(s.T(), err, "VerifyToken should not error on success=false")
	require.NotNil(s.T(), resp)
	require.False(s.T(), resp.Success)
	require.Contains(s.T(), resp.ErrorCodes, "invalid-input-response")
}

// TestTurnstileServiceSuite runs the form-encoding and result-decoding fixture
// methods with fresh suite setup. Each case obtains a verifier and buffered capture
// channel before selecting its local transport behavior.
func TestTurnstileServiceSuite(t *testing.T) {
	suite.Run(t, new(TurnstileServiceSuite))
}

// SetupTest creates the verifier and one-value form capture for each suite case.
// The production constructor is checked for the concrete adapter before tests
// replace its transport or verification URL.
func (s *TurnstileServiceSuite) SetupTest() {
	s.ctx = context.Background()
	s.received = make(chan url.Values, 1)
	verifier, ok := NewTurnstileVerifier().(*turnstileVerifier)
	require.True(s.T(), ok, "type assertion failed")
	s.verifier = verifier
}

// setupTransport installs a synchronous handler transport and a local fixture
// URL on the current verifier. Handlers can send one capture through received
// before returning because SetupTest creates a buffered channel.
func (s *TurnstileServiceSuite) setupTransport(handler http.HandlerFunc) {
	s.verifier.verifyURL = "http://in-process/turnstile"
	s.verifier.httpClient = &http.Client{
		Transport: newInProcessTransport(handler, nil),
	}
}

// RoundTrip returns the response and error from the configured fixture function.
func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// newInProcessTransport executes handler synchronously through an HTTP fixture
// recorder and optionally captures request bytes. It reads and closes the original
// body, installs a replacement before capture, and returns the recorded response.
func newInProcessTransport(handler http.HandlerFunc, capture func(r *http.Request, body []byte)) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body []byte
		if r.Body != nil {
			// Fixture body reads retain any available bytes even when reading fails.
			// Replace the closed stream before capture and handling so both see readable input.
			body, _ = io.ReadAll(r.Body)
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		if capture != nil {
			capture(r, body)
		}

		rec := httptest.NewRecorder()
		handler(rec, r)
		return rec.Result(), nil
	})
}
