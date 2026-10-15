package bedrock

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// executionSink retains copied successful output events and their emitted
// bytes.
type executionSink struct {
	body   bytes.Buffer           // captured emitted response bytes
	events []upstream.OutputEvent // copied successful sink events
}

// executionBody wraps a response body and counts fixture close invocations.
type executionBody struct {
	io.ReadCloser // wrapped upstream response body
	// closed is the nonnil shared atomic counter supplied by the test. It counts
	// each wrapper Close call before the wrapped response body is closed.
	closed *atomic.Int32 // shared close invocation counter
}

// Begin accepts response metadata while the fixture records payload output
// separately.
func (s *executionSink) Begin(upstream.OutputHead) error { return nil }

// Emit clones event bytes before retaining the event and appending its data to
// the fixture buffer. Callers read these accumulated values after synchronous
// execution completes so converter buffer reuse cannot alter assertions.
func (s *executionSink) Emit(event upstream.OutputEvent) error {
	event.Data = bytes.Clone(event.Data)
	s.events = append(s.events, event)
	_, _ = s.body.Write(event.Data)
	return nil
}

// Close increments the nonnil shared counter before closing the wrapped
// response body. Every invocation is counted, including repeated closes, and
// the wrapped close error returns to the fixture caller.
func (b *executionBody) Close() error { b.closed.Add(1); return b.ReadCloser.Close() }

// executionFrame encodes an EventStream event using string headers and IEEE
// CRCs around the supplied payload. Fixture names and lengths fit their wire
// widths; buffer and binary writes use fixed supported values.
func executionFrame(eventType string, payload []byte) []byte {
	// Supported fixed-width numbers and byte/string buffers make encoding errors
	// unavailable through these fixture writes; callers keep lengths within wire
	// widths.
	var headersBuf bytes.Buffer
	_ = headersBuf.WriteByte(byte(len(":event-type")))
	_, _ = headersBuf.WriteString(":event-type")
	_ = headersBuf.WriteByte(7) // string type
	_ = binary.Write(&headersBuf, binary.BigEndian, uint16(len(eventType)))
	_, _ = headersBuf.WriteString(eventType)
	_ = headersBuf.WriteByte(byte(len(":message-type")))
	_, _ = headersBuf.WriteString(":message-type")
	_ = headersBuf.WriteByte(7)
	_ = binary.Write(&headersBuf, binary.BigEndian, uint16(len("event")))
	_, _ = headersBuf.WriteString("event")

	headers := headersBuf.Bytes()
	headersLen := uint32(len(headers))
	// Include the prelude and trailing checksum in the wire total so the decoder
	// reads one complete fixture frame before verifying its message CRC.
	totalLen := uint32(12 + len(headers) + len(payload) + 4)

	// Encode the two big-endian prelude lengths before calculating their IEEE
	// checksum so the decoder can verify that prelude before reading the
	// remaining frame.
	var preludeBuf bytes.Buffer
	_ = binary.Write(&preludeBuf, binary.BigEndian, totalLen)
	_ = binary.Write(&preludeBuf, binary.BigEndian, headersLen)
	preludeBytes := preludeBuf.Bytes()
	preludeCRC := crc32.Checksum(preludeBytes, crc32.IEEETable)

	// Assemble the protected prelude and payload before computing the message
	// checksum because that checksum covers every preceding byte, including the
	// prelude CRC.
	var frame bytes.Buffer
	_, _ = frame.Write(preludeBytes)
	_ = binary.Write(&frame, binary.BigEndian, preludeCRC)
	_, _ = frame.Write(headers)
	_, _ = frame.Write(payload)

	messageCRC := crc32.Checksum(frame.Bytes(), crc32.IEEETable)
	_ = binary.Write(&frame, binary.BigEndian, messageCRC)
	return frame.Bytes()
}

// TestExecuteBedrockWireAndRelease returns buffered JSON and binary
// EventStream through a local bearer-authenticated endpoint. It checks
// model/usage observations, semantic output, request identity, metric removal,
// and one response close plus lifecycle release.
func TestExecuteBedrockWireAndRelease(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{true: "stream", false: "nonstream"}[stream], func(t *testing.T) {
			var closed, released atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer fixture-key", r.Header.Get("Authorization"))
				w.Header().Set("x-amzn-requestid", "bedrock-fixture")
				if stream {
					for _, event := range []string{`{"type":"message_start","message":{"model":"claude-runtime","usage":{"input_tokens":0}}}`, `{"type":"content_block_delta","delta":{"type":"text_delta","text":"visible"}}`, `{"type":"message_delta","amazon-bedrock-invocationMetrics":{"inputTokenCount":0,"outputTokenCount":2}}`, `{"type":"message_stop"}`} {
						payload := []byte(`{"bytes":"` + base64.StdEncoding.EncodeToString([]byte(event)) + `"}`)
						_, _ = w.Write(executionFrame("chunk", payload))
						_ = http.NewResponseController(w).Flush()
					}
				} else {
					_, _ = io.WriteString(w, `{"type":"message","model":"claude-runtime","content":[{"type":"text","text":"visible"}],"usage":{"input_tokens":0,"output_tokens":0},"future":true}`)
				}
			}))
			defer server.Close()
			target := &Target{ProviderID: 7, ReadBody: io.ReadAll, Enter: func() (func(), error) { return func() { released.Add(1) }, nil }, Request: RequestOptions{ModelID: "anthropic.fixture", Region: "us-east-1", APIKeyMode: true, APIKey: "fixture-key", Stream: stream, Do: func(r *http.Request) (*http.Response, error) {
				// Replace the target host with the local fixture while keeping the built
				// path, body, and authentication headers so this test observes actual
				// Bedrock request preparation.
				local, _ := http.NewRequestWithContext(r.Context(), r.Method, server.URL+r.URL.RequestURI(), r.Body)
				local.Header = r.Header.Clone()
				res, err := server.Client().Do(local)
				if res != nil {
					res.Body = &executionBody{ReadCloser: res.Body, closed: &closed}
				}
				return res, err
			}}, Retry: RetryPolicy{MaxAttempts: 1, MaxElapsed: time.Second}}
			sink := &executionSink{}
			ctx, stages := timing.WithStages(context.Background(), time.Now(), nil)
			result, err := (Executor{}).Execute(ctx, upstream.AttemptInput{Target: target, Protocol: protocol.ProtocolAnthropicMessages, Body: []byte(`{"messages":[]}`), ResponseModel: "fixture", Stream: stream}, sink)
			if stream {
				require.Contains(t, stages.Snapshot(), "upstream_first_event_ms")
				require.NotContains(t, stages.Snapshot(), "upstream_first_sse_data_ms")
			}
			require.NoError(t, err)
			require.True(t, result.HasUsage)
			require.True(t, result.Served)
			require.NotNil(t, result.FirstSemanticOutput)
			require.Equal(t, "bedrock-fixture", result.RequestID)
			require.Equal(t, "claude-runtime", result.UpstreamResponseModel)
			require.EqualValues(t, 1, closed.Load())
			require.EqualValues(t, 1, released.Load())
			require.Contains(t, sink.body.String(), "visible")
			require.NotContains(t, sink.body.String(), "amazon-bedrock-invocationMetrics")
			if stream {
				require.Equal(t, 2, result.Usage.OutputTokens)
				require.NotNil(t, result.FirstTokenMs)
			} else {
				require.Zero(t, result.Usage.OutputTokens)
				require.Nil(t, result.FirstTokenMs)
				require.Contains(t, sink.body.String(), "future")
			}
		})
	}
}
