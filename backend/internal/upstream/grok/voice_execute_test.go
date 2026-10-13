package grok

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Voice metering uses character-based TTS units or STT hours after response
// policy accepts a body. Local HTTP fixtures inspect token/header forwarding,
// output bytes and cleanup counts; a handled HTTP-error fixture stops before
// body reading, output or audio estimation.

// voiceOutput retains synchronous output observations for Voice and Responses
// assertions.
type voiceOutput struct {
	// head retains the supplied Header map from Begin; tests inspect that shared
	// map after the executor finishes output initialization.
	head upstream.OutputHead // recorded output head
	// events stores supplied event values without cloning their Data bytes; those
	// slices remain stable until capture assertions finish.
	events []upstream.OutputEvent // recorded output events
}

// voiceBody counts close invocations before delegating to its response body.
type voiceBody struct {
	// ReadCloser and closed participate in the same fixture: Close increments
	// closed before invoking ReadCloser.Close, so the counter records cleanup
	// attempts even when delegation fails.
	io.ReadCloser               // counted response body
	closed        *atomic.Int64 // body-close invocation counter
}

// Begin records the output head and accepts initialization for the fixture. The
// retained Header remains shared with the supplied head, so the test inspects it
// after execution rather than treating it as a cloned snapshot.
func (s *voiceOutput) Begin(head upstream.OutputHead) error { s.head = head; return nil }

// Emit appends an output event for later synchronous test assertions. Event Data
// retains its supplied byte storage; callers keep that data stable until the
// test finishes inspecting the captured event sequence.
func (s *voiceOutput) Emit(event upstream.OutputEvent) error {
	s.events = append(s.events, event)
	return nil
}

// Close increments the shared invocation counter and then delegates body
// closure. Every invocation counts, including repeated calls or an underlying
// close error; the fixture measures close attempts rather than successful
// resource release.
func (b *voiceBody) Close() error { b.closed.Add(1); return b.ReadCloser.Close() }

// TestVoiceExecuteLocalHTTP exchanges TTS, STT and custom-Voice fixtures through
// a local HTTP server. It asserts request authentication/headers/body, emitted
// response and request ID, single body cleanup, released activity, TTS character
// units, STT hours and absent custom-Voice usage.
func TestVoiceExecuteLocalHTTP(t *testing.T) {
	cases := []struct {
		endpoint string              // Voice operation fixture
		proto    protocol.ProtocolID // requested protocol identity
		// request, response and contentType define one local HTTP exchange. The server
		// compares request and returns response with contentType, which the sink must
		// observe after execution. request: supplied Voice JSON; response: upstream
		// response bytes; contentType: upstream response MIME
		request, response, contentType string
		// units is the expected TTS character-million quantity or STT hour quantity;
		// zero selects the custom-Voice assertions requiring no AudioUsage.
		units float64 // expected audio quantity
	}{
		{"tts", protocol.ProtocolTTS, `{"input":"你好世界"}`, "audio-bytes", "audio/mpeg", 4.0 / 1_000_000},
		{"stt", protocol.ProtocolSTT, `{}`, `{"duration":3600,"text":"hello"}`, "application/json", 1},
		{"custom-voices", protocol.ProtocolCustomVoices, `{"name":"voice"}`, `{"id":"v1"}`, "application/json", 0},
	}
	for _, tt := range cases {
		t.Run(tt.endpoint, func(t *testing.T) {
			var calls, closed, active atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, "Bearer fixture-token", r.Header.Get("Authorization"))
				assert.Equal(t, "fixture", r.Header.Get("X-Test"))
				data, _ := io.ReadAll(r.Body)
				assert.Equal(t, tt.request, string(data))
				w.Header().Set("Content-Type", tt.contentType)
				w.Header().Set("xai-request-id", "upstream-fixture")
				_, _ = io.WriteString(w, tt.response)
			}))
			defer server.Close()
			req, err := BuildVoiceRequest(context.Background(), http.MethodPost, server.URL, "fixture-token", "", []byte(tt.request), func(h http.Header) { h.Set("X-Test", "fixture") })
			require.NoError(t, err)
			target := &VoiceTarget{
				Endpoint:     tt.endpoint,
				BaseEndpoint: tt.endpoint,
				Request:      req,
				Enter:        func() (func(), error) { active.Add(1); return func() { active.Add(-1) }, nil },
				Do: func(r *http.Request) (*http.Response, error) {
					resp, err := server.Client().Do(r)
					if resp != nil {
						// Wrap each successful response with the invocation counter so executor
						// cleanup can be asserted alongside the local server's request count.
						resp.Body = &voiceBody{resp.Body, &closed}
					}
					return resp, err
				},
				ReadBody:    io.ReadAll,
				CopyHeaders: func(dst, src http.Header) { dst.Set("Content-Type", src.Get("Content-Type")) },
			}
			sink := &voiceOutput{}
			result, err := (VoiceExecutor{}).Execute(context.Background(), upstream.AttemptInput{Protocol: tt.proto, Body: []byte(tt.request), Target: target}, sink)
			require.NoError(t, err)
			require.EqualValues(t, 1, calls.Load())
			require.EqualValues(t, 1, closed.Load())
			require.Zero(t, active.Load())
			require.Equal(t, "upstream-fixture", result.RequestID)
			require.Equal(t, http.StatusOK, sink.head.Status)
			require.Equal(t, tt.contentType, sink.head.Header.Get("Content-Type"))
			require.Len(t, sink.events, 1)
			require.Equal(t, tt.response, string(sink.events[0].Data))
			require.True(t, result.Served)
			if tt.units == 0 {
				require.Nil(t, result.AudioUsage)
				require.False(t, result.HasUsage)
			} else {
				require.True(t, result.HasUsage)
				require.Equal(t, tt.endpoint, result.AudioUsage.Mode)
				require.InDelta(t, tt.units, result.AudioUsage.DurationOrUnits, 1e-12)
			}
		})
	}
}

// TestVoiceExecuteHTTPErrorDoesNotEmitOrMeter handles an injected HTTP 403
// before the normal body reader runs. Execution must return the policy error,
// emit no event, retain no audio usage and close the response once.
func TestVoiceExecuteHTTPErrorDoesNotEmitOrMeter(t *testing.T) {
	var closed atomic.Int64
	req, err := http.NewRequest(http.MethodPost, "https://fixture.invalid", strings.NewReader(`{}`))
	require.NoError(t, err)
	sentinel := io.ErrUnexpectedEOF
	target := &VoiceTarget{
		Request: req,
		Do: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusForbidden, Body: &voiceBody{io.NopCloser(strings.NewReader("denied")), &closed}}, nil
		},
		ReadBody:       func(io.Reader) ([]byte, error) { t.Fatal("错误响应由原策略处理"); return nil, nil },
		BeforeResponse: func(resp *http.Response) (bool, error) { require.Equal(t, 403, resp.StatusCode); return true, sentinel },
	}
	sink := &voiceOutput{}
	result, err := (VoiceExecutor{}).Execute(context.Background(), upstream.AttemptInput{Protocol: protocol.ProtocolTTS, Target: target}, sink)
	require.ErrorIs(t, err, sentinel)
	require.Nil(t, result.AudioUsage)
	require.Empty(t, sink.events)
	require.EqualValues(t, 1, closed.Load())
}
