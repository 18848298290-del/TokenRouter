package bedrock

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Crc32IEEETable is the CRC32 / IEEE table used by AWS EventStream.
var Crc32IEEETable = crc32.MakeTable(crc32.IEEE)

// BedrockEventStreamDecoder decodes AWS EventStream binary frames.
// Frame layout:
//
//	[total_byte_length: 4 bytes]
//	[headers_byte_length: 4 bytes]
//	[prelude_crc: 4 bytes]
//	[headers: variable]
//	[payload: variable]
//	[message_crc: 4 bytes]
type BedrockEventStreamDecoder struct {
	reader *bufio.Reader
}

// StreamOptions supplies provider identity, idle timeout, and observation callbacks.
type StreamOptions struct {
	Observe    func(anthropic.Observation)
	ProviderID int64
	Interval   time.Duration
	OnTimeout  func(context.Context, string)
}
type StreamResult struct {
	Usage            *upstream.TokenUsage
	FirstTokenMs     *int
	ClientDisconnect bool
}

// StreamResponse converts Bedrock InvokeModelWithResponseStream output to Claude SSE.
// Each chunk event contains a JSON payload whose bytes field holds base64-encoded
// Claude event JSON. The caller remains responsible for closing resp.Body.
func StreamResponse(
	ctx context.Context,
	resp *http.Response,
	c *upstream.OutputContext,
	options StreamOptions,
	startTime time.Time,
	model string,
) (*StreamResult, error) {
	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	if v := resp.Header.Get("x-amzn-requestid"); v != "" {
		c.Header("x-request-id", v)
	}

	usage := &upstream.TokenUsage{}
	var firstTokenMs *int
	clientDisconnected := false

	// Decode binary EventStream frames before examining their JSON payloads.
	decoder := NewBedrockEventStreamDecoder(resp.Body)

	type decodeEvent struct {
		payload []byte
		err     error
	}
	events := make(chan decodeEvent, 16)
	done := make(chan struct{})
	sendEvent := func(ev decodeEvent) bool {
		select {
		case events <- ev:
			return true
		case <-done:
			return false
		}
	}
	var lastReadAt atomic.Int64
	lastReadAt.Store(time.Now().UnixNano())

	go func() {
		defer close(events)
		for {
			payload, err := decoder.Decode()
			if err != nil {
				if err == io.EOF {
					return
				}
				_ = sendEvent(decodeEvent{err: err})
				return
			}
			lastReadAt.Store(time.Now().UnixNano())
			if !sendEvent(decodeEvent{payload: payload}) {
				return
			}
		}
	}()
	defer close(done)

	streamInterval := options.Interval
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				if !clientDisconnected {
					flusher.Flush()
				}
				return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: clientDisconnected}, nil
			}
			if ev.err != nil {
				if clientDisconnected {
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: true}, nil
				}
				if errors.Is(ev.err, context.Canceled) || errors.Is(ev.err, context.DeadlineExceeded) {
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: true}, nil
				}
				return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs}, fmt.Errorf("bedrock stream read error: %w", ev.err)
			}

			// Decode the chunk payload's base64 bytes field into Claude event JSON.
			sseData := ExtractBedrockChunkData(ev.payload)
			if sseData == nil {
				continue
			}

			if firstTokenMs == nil {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}

			// Convert invocation metrics to usage and remove the Bedrock-specific field.
			sseData = TransformBedrockInvocationMetrics(sseData)

			observation := anthropic.ObserveEvent(string(sseData))
			if options.Observe != nil {
				options.Observe(observation)
			}
			c.NextEvent(observation.Semantic, observation.Terminal)
			anthropic.ParseSSEUsagePassthrough(string(sseData), usage)

			eventType := gjson.GetBytes(sseData, "type").String()

			if !clientDisconnected {
				var writeErr error
				if eventType != "" {
					_, writeErr = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, sseData)
				} else {
					_, writeErr = fmt.Fprintf(w, "data: %s\n\n", sseData)
				}
				if writeErr != nil {
					clientDisconnected = true
					logger.LegacyPrintf("service.gateway", "[Bedrock] Client disconnected during streaming, continue draining for usage: provider=%d", options.ProviderID)
				} else {
					flusher.Flush()
				}
			}

		case <-intervalCh:
			lastRead := time.Unix(0, lastReadAt.Load())
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if clientDisconnected {
				return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: true}, nil
			}
			logger.LegacyPrintf("service.gateway", "[Bedrock] Stream data interval timeout: provider=%d model=%s interval=%s", options.ProviderID, model, streamInterval)
			if options.OnTimeout != nil {
				options.OnTimeout(ctx, model)
			}
			return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs}, fmt.Errorf("stream data interval timeout")
		}
	}
}

// ExtractBedrockChunkData decodes Claude event JSON from a chunk payload.
// The payload has the form {"bytes":"<base64-encoded-json>"}; invalid input returns nil.
func ExtractBedrockChunkData(payload []byte) []byte {
	b64 := gjson.GetBytes(payload, "bytes").String()
	if b64 == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	return decoded
}

// TransformBedrockInvocationMetrics removes amazon-bedrock-invocationMetrics and
// converts its token counts to Anthropic usage when usage is absent.
//
// A message_delta event can contain:
//
//	{"type":"message_delta","delta":{...},"amazon-bedrock-invocationMetrics":{"inputTokenCount":150,"outputTokenCount":42}}
//
// It becomes:
//
//	{"type":"message_delta","delta":{...},"usage":{"input_tokens":150,"output_tokens":42}}
func TransformBedrockInvocationMetrics(data []byte) []byte {
	metrics := gjson.GetBytes(data, "amazon-bedrock-invocationMetrics")
	if !metrics.Exists() || !metrics.IsObject() {
		return data
	}

	data, _ = sjson.DeleteBytes(data, "amazon-bedrock-invocationMetrics")

	// Existing usage takes precedence over invocation metrics.
	if gjson.GetBytes(data, "usage").Exists() {
		return data
	}

	inputTokens := metrics.Get("inputTokenCount")
	outputTokens := metrics.Get("outputTokenCount")
	if inputTokens.Exists() {
		data, _ = sjson.SetBytes(data, "usage.input_tokens", inputTokens.Int())
	}
	if outputTokens.Exists() {
		data, _ = sjson.SetBytes(data, "usage.output_tokens", outputTokens.Int())
	}

	return data
}

func NewBedrockEventStreamDecoder(r io.Reader) *BedrockEventStreamDecoder {
	return &BedrockEventStreamDecoder{
		reader: bufio.NewReaderSize(r, 64*1024),
	}
}

// Decode returns the next chunk payload, skipping other non-error events.
func (d *BedrockEventStreamDecoder) Decode() ([]byte, error) {
	for {
		// The 12-byte prelude contains two lengths and its own CRC.
		prelude := make([]byte, 12)
		if _, err := io.ReadFull(d.reader, prelude); err != nil {
			return nil, err
		}

		// Validate the prelude before using its lengths.
		preludeCRC := BedrockReadUint32(prelude[8:12])
		if crc32.Checksum(prelude[0:8], Crc32IEEETable) != preludeCRC {
			return nil, fmt.Errorf("eventstream prelude CRC mismatch")
		}

		totalLength := BedrockReadUint32(prelude[0:4])
		headersLength := BedrockReadUint32(prelude[4:8])

		if totalLength < 16 { // minimum: 12 prelude + 4 message_crc
			return nil, fmt.Errorf("invalid eventstream frame: total_length=%d", totalLength)
		}

		remaining := int(totalLength) - 12
		if remaining <= 0 {
			continue
		}
		data := make([]byte, remaining)
		if _, err := io.ReadFull(d.reader, data); err != nil {
			return nil, err
		}

		// The message CRC covers the prelude, headers, and payload.
		messageCRC := BedrockReadUint32(data[len(data)-4:])
		h := crc32.New(Crc32IEEETable)
		_, _ = h.Write(prelude)
		_, _ = h.Write(data[:len(data)-4])
		if h.Sum32() != messageCRC {
			return nil, fmt.Errorf("eventstream message CRC mismatch")
		}

		headers := data[:headersLength]
		payload := data[headersLength : len(data)-4] // exclude message CRC

		eventType := ExtractEventStreamHeaderValue(headers, ":event-type")

		if eventType == "chunk" {
			// Chunk payloads contain the base64 bytes field.
			return payload, nil
		}

		exceptionType := ExtractEventStreamHeaderValue(headers, ":exception-type")
		if exceptionType != "" {
			return nil, fmt.Errorf("bedrock exception: %s: %s", exceptionType, string(payload))
		}

		messageType := ExtractEventStreamHeaderValue(headers, ":message-type")
		if messageType == "exception" || messageType == "error" {
			return nil, fmt.Errorf("bedrock error: %s", string(payload))
		}

		// Ignore other event types, such as initial-response.
	}
}

// ExtractEventStreamHeaderValue returns a string or boolean header as text.
// Missing, malformed, and unsupported values return an empty string. Header layout:
//
//	[name_length: 1 byte][name: variable][value_type: 1 byte][value: variable]
//
// Type 7 is a string with a two-byte big-endian value length.
func ExtractEventStreamHeaderValue(headers []byte, targetName string) string {
	pos := 0
	for pos < len(headers) {
		if pos >= len(headers) {
			break
		}
		nameLen := int(headers[pos])
		pos++
		if pos+nameLen > len(headers) {
			break
		}
		name := string(headers[pos : pos+nameLen])
		pos += nameLen

		if pos >= len(headers) {
			break
		}
		valueType := headers[pos]
		pos++

		switch valueType {
		case 7: // string
			if pos+2 > len(headers) {
				return ""
			}
			valueLen := int(BedrockReadUint16(headers[pos : pos+2]))
			pos += 2
			if pos+valueLen > len(headers) {
				return ""
			}
			value := string(headers[pos : pos+valueLen])
			pos += valueLen
			if name == targetName {
				return value
			}
		case 0: // bool true
			if name == targetName {
				return "true"
			}
		case 1: // bool false
			if name == targetName {
				return "false"
			}
		case 2: // byte
			pos++
			if name == targetName {
				return ""
			}
		case 3: // short
			pos += 2
			if name == targetName {
				return ""
			}
		case 4: // int
			pos += 4
			if name == targetName {
				return ""
			}
		case 5: // long
			pos += 8
			if name == targetName {
				return ""
			}
		case 6: // bytes
			if pos+2 > len(headers) {
				return ""
			}
			valueLen := int(BedrockReadUint16(headers[pos : pos+2]))
			pos += 2 + valueLen
		case 8: // timestamp
			pos += 8
		case 9: // uuid
			pos += 16
		default:
			return "" // an unknown type has no known length to skip
		}
	}
	return ""
}

func BedrockReadUint32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func BedrockReadUint16(b []byte) uint16 {
	return uint16(b[0])<<8 | uint16(b[1])
}
