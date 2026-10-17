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
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// stream.go decodes Bedrock EventStream frames and emits Claude SSE with
// normalized usage.
// Bedrock streaming wraps Claude JSON events inside base64 chunk payloads
// carried by binary AWS EventStream frames. The decoder verifies the IEEE
// prelude and message checksums before classifying headers, then the output
// loop adapts invocation metrics to Messages usage and emits Claude SSE.
//
// A decoder worker feeds a bounded channel so the output loop can check idle
// timing independently of blocked reads. Complete decoded chunks update the
// read clock, while client write failure stops further output but permits
// usage draining. Returning closes the worker send signal; the caller closes
// the response body to release a blocked decoder, and this adapter does not
// join that worker. Frame lengths require internally consistent header bounds:
// CRC and minimum-total checks do not establish every slice bound or cap
// allocation size.

// Crc32IEEETable selects IEEE CRC32 for both EventStream checksums, matching
// frame construction and validation across the platform decoder and fixtures.
var Crc32IEEETable = crc32.MakeTable(crc32.IEEE)

// BedrockEventStreamDecoder reads one binary frame at a time and selects chunk
// payloads.
type BedrockEventStreamDecoder struct {
	reader *bufio.Reader // buffered binary source reader
}

// StreamOptions supplies provider diagnostics, decoded-event observation, and
// idle-timeout handling.
type StreamOptions struct {
	Observe    func(anthropic.Observation) // optional decoded event observer
	ProviderID int64                       // selected provider log identifier
	// Interval enables idle checks only when positive. The worker refreshes its
	// clock after Decode returns a chunk, so other frame types do not count as
	// observed output here.
	Interval time.Duration // complete-chunk idle timeout interval
	// OnTimeout optionally observes an expired idle interval while the client is
	// connected; it runs before the adapter returns its timeout error.
	OnTimeout func(context.Context, string)
}

// StreamResult retains normalized usage and
// downstream disconnect state.
type StreamResult struct {
	Usage *upstream.TokenUsage // collected normalized token counters

	ClientDisconnect bool // observed downstream write failure
}

// StreamResponse converts Bedrock binary chunks into synchronous Claude SSE
// and returns collected usage. Callers supply a flusher-capable
// output context and close resp.Body after this adapter returns, releasing any
// decoder blocked in a read.
//
// The worker reports complete chunks and decoding failures through a bounded
// channel. A client write failure keeps draining for usage;
// cancellation-shaped read errors return collected counters as disconnect.
// Other read errors propagate while output is connected; after a client write
// failure, any read error ends usage draining with a nil error. A positive idle
// interval checks the last complete chunk clock and can invoke OnTimeout.
// Returning stops channel sends without joining the worker. Decoded events
// carry their receive time through the queue so synchronous downstream writes
// cannot move upstream arrival observations to the time the event is consumed.
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

	clientDisconnected := false

	// Decode binary framing before inspecting the bytes envelope so Claude event
	// parsing sees only extracted chunk content after frame checksum checks.
	decoder := NewBedrockEventStreamDecoder(resp.Body)

	// decodeEvent carries a decoded chunk or failure to the output loop.
	type decodeEvent struct {
		payload   []byte    // decoded binary chunk payload
		err       error     // binary decoding failure result
		arrivedAt time.Time // upstream decoded frame arrival
	}
	// Separate blocked decoding from output/timer selection. The bounded queue
	// retains complete chunk results, and done lets a worker waiting to send
	// leave when output handling returns.
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

	// Keep the read clock with complete decoded chunks and publish failures
	// through the same queue, allowing idle checks while the source reader is
	// blocked.
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
			if !sendEvent(decodeEvent{payload: payload, arrivedAt: time.Now()}) {
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
				return &StreamResult{Usage: usage, ClientDisconnect: clientDisconnected}, nil
			}
			if ev.err != nil {
				// After downstream failure, a decoder error ends the usage drain. Return
				// collected counters and the disconnect flag without adding another
				// platform failure.
				if clientDisconnected {
					return &StreamResult{Usage: usage, ClientDisconnect: true}, nil
				}
				if errors.Is(ev.err, context.Canceled) || errors.Is(ev.err, context.DeadlineExceeded) {
					return &StreamResult{Usage: usage, ClientDisconnect: true}, nil
				}
				return &StreamResult{Usage: usage}, fmt.Errorf("bedrock stream read error: %w", ev.err)
			}

			// Extract base64 event bytes after frame decoding; an absent or invalid
			// bytes envelope is skipped so it cannot enter Claude observation or output
			// conversion.
			sseData := ExtractBedrockChunkData(ev.payload)
			if sseData == nil {
				continue
			}

			// Normalize invocation metrics before both observation and passthrough
			// usage parsing so the accounting paths read the same Claude-shaped event
			// sent to the client.
			sseData = TransformBedrockInvocationMetrics(sseData)

			if stages := timing.StagesFromContext(ctx); stages != nil {
				stages.ObserveEventAt(stages.Attempt(), string(sseData), "", ev.arrivedAt)
			}
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
				// Stop writing after the first downstream failure while keeping the
				// upstream read path active, because later chunks can still carry final
				// usage observations.
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
				return &StreamResult{Usage: usage, ClientDisconnect: true}, nil
			}
			logger.LegacyPrintf("service.gateway", "[Bedrock] Stream data interval timeout: provider=%d model=%s interval=%s", options.ProviderID, model, streamInterval)
			if options.OnTimeout != nil {
				options.OnTimeout(ctx, model)
			}
			return &StreamResult{Usage: usage}, fmt.Errorf("stream data interval timeout")
		}
	}
}

// ExtractBedrockChunkData decodes the bytes field with standard base64 and
// returns its raw contents. Missing, empty, or invalid encoding returns nil;
// it does not validate the decoded bytes as complete Claude JSON.
func ExtractBedrockChunkData(payload []byte) []byte {
	b64 := gjson.GetBytes(payload, "bytes").String()
	if b64 == "" {
		return nil
	}
	// Discard partial decoding on an invalid base64 source so the output loop
	// cannot parse a truncated byte prefix as a complete Claude event.
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	return decoded
}

// TransformBedrockInvocationMetrics removes object-valued invocation metrics
// and inserts input/output usage when the usage field is absent. Existing
// usage presence, including null, takes precedence; byte modification errors
// are discarded and partial metric fields are handled independently.
//
// Capture the metrics before deleting their vendor field. An absent usage
// object receives only the input or output fields actually present in that
// capture; a supplied usage field keeps precedence even when null or
// incomplete. This leaves existing Claude accounting observations authoritative
// while permitting metrics-only events to supply their counters.
func TransformBedrockInvocationMetrics(data []byte) []byte {
	metrics := gjson.GetBytes(data, "amazon-bedrock-invocationMetrics")
	if !metrics.Exists() || !metrics.IsObject() {
		return data
	}

	// Remove the vendor metrics before selecting usage precedence so callers
	// receive the adapted event even when existing usage already supplies the
	// accounting observation.
	data, _ = sjson.DeleteBytes(data, "amazon-bedrock-invocationMetrics")

	// Treat usage presence as authoritative, including null, so invocation
	// metrics do not overwrite a field already supplied by the Claude-shaped
	// event.
	if gjson.GetBytes(data, "usage").Exists() {
		return data
	}

	// Insert present counters independently. Ignored patch errors can leave one
	// successfully inserted usage field even when the other modification fails.
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

// NewBedrockEventStreamDecoder wraps the borrowed source reader with a
// sixty-four-KiB buffer. The decoder owns neither source closing nor
// concurrency coordination; callers serialize Decode and release the source
// separately.
func NewBedrockEventStreamDecoder(r io.Reader) *BedrockEventStreamDecoder {
	return &BedrockEventStreamDecoder{
		reader: bufio.NewReaderSize(r, 64*1024),
	}
}

// Decode returns the next chunk payload, skipping nonerror frame types and
// reporting exception/error frames. Callers serialize decoding and provide a
// frame stream with internally consistent advertised lengths; returned chunk
// bytes belong to that decoded frame allocation.
//
// The prelude CRC is checked before using its lengths, and a sixteen-byte
// total minimum accounts for prelude and trailing checksum. The remaining
// advertised frame bytes are read before the message CRC is verified. Header
// length is then trusted for slicing without an additional bounds check, and
// total length controls an uncapped allocation. These checks establish
// checksum consistency rather than complete validation of arbitrary malformed
// frame lengths.
func (d *BedrockEventStreamDecoder) Decode() ([]byte, error) {
	for {
		// Read the fixed prelude before allocating advertised frame storage so
		// length fields are available together with the checksum protecting those
		// fields.
		prelude := make([]byte, 12)
		if _, err := io.ReadFull(d.reader, prelude); err != nil {
			return nil, err
		}

		// Check the prelude checksum before consuming its advertised lengths,
		// preventing a corrupted length field from directly selecting the following
		// allocation.
		preludeCRC := BedrockReadUint32(prelude[8:12])
		if crc32.Checksum(prelude[0:8], Crc32IEEETable) != preludeCRC {
			return nil, fmt.Errorf("eventstream prelude CRC mismatch")
		}

		totalLength := BedrockReadUint32(prelude[0:4])
		headersLength := BedrockReadUint32(prelude[4:8])

		if totalLength < 16 { // A frame needs its prelude and trailing checksum.
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

		// Include the complete prelude, including its CRC, when checking the message
		// checksum because AWS framing protects every byte before the trailing
		// checksum.
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

		// Skip nonerror framing events such as initial-response and continue until a
		// chunk or failure is found, so callers receive generation payloads rather
		// than protocol setup metadata.
	}
}

// ExtractEventStreamHeaderValue returns matching string or boolean header text
// from typed EventStream headers. Missing or malformed supported values and
// unknown types return empty text; known unreturned types are skipped by their
// wire widths.
//
// String values carry a big-endian sixteen-bit byte length, and boolean values
// carry their value in the type tag. Fixed-width numeric fields and
// length-prefixed byte fields advance scanning so a later requested header can
// be found. Some unsupported types return immediately when their name matches,
// while byte-array, timestamp, and UUID values are skipped even for a matching
// name. No semantic JSON or event
// validity is established by this header scan.
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

		// Use each wire type width to advance past unreturned values while retaining
		// later header lookup; an unknown type ends scanning because its next
		// position cannot be resolved.
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

// BedrockReadUint32 decodes four big-endian bytes into a thirty-two-bit value.
// Callers provide at least four bytes; a shorter slice panics during direct
// indexing before a value can be returned.
func BedrockReadUint32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// BedrockReadUint16 decodes two big-endian bytes into a sixteen-bit value.
// Callers provide at least two bytes; a shorter slice panics during direct
// indexing rather than returning an error.
func BedrockReadUint16(b []byte) uint16 {
	return uint16(b[0])<<8 | uint16(b[1])
}
