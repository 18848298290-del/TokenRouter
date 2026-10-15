package gemini

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	geminiwire "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// response.go reads Gemini responses, collects streamed content, and writes
// native or Messages output.
// Gemini output may arrive as ordinary JSON or one JSON payload per SSE data
// line, with an optional Code Assist response wrapper. Native output retains
// the original REST payload, while Messages conversion delegates its event and
// content rules to the protocol bridge. Observers receive raw payloads before
// converted output is emitted.
//
// Buffered stream collection retains content-bearing payloads and combines
// adjacent text parts with matching thought flags. It copies modified map
// containers during merging but keeps nested values shared, and merging text
// retains the earlier part metadata. Stream adapters differ in their handling
// of decode errors, write failures, and first-token timing; those rules belong
// to their methods, while the executor owns response-body closing.

// ResponseOptions supplies payload observers, response readers, header
// handling, and protocol error mapping.
type ResponseOptions struct {
	// ObserveRaw and ObserveState are optional synchronous observers. Payload
	// bytes and usage pointers are passed directly; callbacks leave these inputs
	// unchanged during conversion and copy mutable data retained for later use.
	ObserveRaw func([]byte) // raw payload observation callback
	// usage and legacy timing observation
	ObserveState func(*upstream.TokenUsage, *int)
	// GoogleError, ReadBody, WriteHeaders, ObserveImages, ReverseTools,
	// ClaudeError, ChatError, and CompatError are supplied for the output paths
	// that invoke them; these adapters call applicable handlers directly.
	GoogleError func(int, string) error // native response error mapping
	// caller-bounded response body reader
	ReadBody     func(io.Reader) ([]byte, error)
	WriteHeaders func(http.Header, http.Header) // response header forwarding callback
	// DebugHeaders selects rate-header logging; HasHeaderFilter selects streaming
	// header forwarding.
	DebugHeaders, HasHeaderFilter bool
	ObserveImages                 func([]byte)        // inline image observation callback
	ReverseTools                  func([]byte) []byte // upstream tool-name reversal callback
	// ClaudeError maps Messages errors; ChatError maps Chat errors.
	ClaudeError, ChatError func(int, string, string) error
	// selected OpenAI protocol error mapping
	CompatError func(OpenAICompatProtocol, int, string, string) error
}
type (
	// ResponseAdapter reads Gemini payloads and delivers one selected client
	// protocol through its options.
	ResponseAdapter struct{ Options ResponseOptions } // Options supplies conversion and output callbacks.
	// StreamResult holds converted stream usage and its legacy first-event timing
	// observation.
	StreamResult struct {
		Usage        *upstream.TokenUsage // observed normalized token usage
		FirstTokenMs *int                 // milliseconds to adapter first event
	}
)

// NativeStreamResult holds native payload usage and the first nonempty data
// timing.
type NativeStreamResult struct {
	Usage        *upstream.TokenUsage // observed normalized token usage
	FirstTokenMs *int                 // milliseconds to first native payload
}

// HandleNonStreamingResponse reads at most eight MiB, unwraps Gemini JSON, and
// writes a Messages response with inline images omitted. It observes raw/image
// data before conversion; callers own the source body and inspect
// OutputContext for downstream write errors.
func (s *ResponseAdapter) HandleNonStreamingResponse(c *upstream.OutputContext, resp *http.Response, originalModel string) (*upstream.TokenUsage, error) {
	// Retain a bounded prefix for buffered Messages conversion; an oversized JSON
	// body can fail decoding when the prefix ends within a value.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, s.Options.ClaudeError(http.StatusBadGateway, "upstream_error", "Failed to read upstream response")
	}

	unwrappedBody, err := UnwrapGeminiResponse(body)
	if err != nil {
		return nil, s.Options.ClaudeError(http.StatusBadGateway, "upstream_error", "Failed to parse upstream response")
	}
	s.observeRaw(unwrappedBody)
	s.Options.ObserveImages(unwrappedBody)

	var geminiResp map[string]any
	if err := json.Unmarshal(unwrappedBody, &geminiResp); err != nil {
		return nil, s.Options.ClaudeError(http.StatusBadGateway, "upstream_error", "Failed to parse upstream response")
	}

	claudeResp, usage := ConvertGeminiToClaudeMessage(geminiResp, originalModel, unwrappedBody, false)
	responseBody, err := json.Marshal(claudeResp)
	if err != nil {
		return nil, err
	}
	c.NextEvent(bridge.CompatJSONHasContent(responseBody), true)
	c.Data(http.StatusOK, "application/json; charset=utf-8", responseBody)

	return usage, nil
}

// HandleStreamingResponse converts Gemini data lines into synchronous Messages
// SSE events and returns bridge usage and first-token timing. Callers close
// the source response body and supply a flusher-capable output context.
//
// The bridge opens the message before payload reads and owns block ordering
// and final events. Invalid decoded payloads are skipped; non-EOF read errors
// abort with an error. First-token timing follows the bridge event flag, while
// OutputContext records writer failures even though WriteSSE ignores
// individual writes. Each data line is processed separately, including a final
// line without a trailing newline.
func (s *ResponseAdapter) HandleStreamingResponse(c *upstream.OutputContext, resp *http.Response, startTime time.Time, originalModel string) (*StreamResult, error) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	state := bridge.NewNativeGeminiMessagesStream(bridge.NativeGeminiRuntime{RandomHex: upstream.RandomHex, MessageID: upstream.GenerateAnthropicMsgID})
	var firstTokenMs *int
	writeEvent := func(event bridge.NativeMessageEvent) {
		if event.FirstToken && firstTokenMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
			if s.Options.ObserveState != nil {
				s.Options.ObserveState(nil, firstTokenMs)
			}
		}
		if event.Name != "" {
			WriteSSE(c.Writer, event.Name, event.Data, func(payload []byte) {
				semantic, terminal := bridge.CompatOutputMeaning(append([]byte("data: "), payload...))
				c.NextEvent(semantic, terminal)
			})
		}
		if event.Flush {
			flusher.Flush()
		}
	}
	for event := range state.Begin(upstream.GenerateAnthropicMsgID(), originalModel) {
		writeEvent(event)
	}
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("stream read error: %w", err)
		}

		if !strings.HasPrefix(line, "data:") {
			if errors.Is(err, io.EOF) {
				break
			}
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			if errors.Is(err, io.EOF) {
				break
			}
			continue
		}

		unwrappedBytes, err := UnwrapGeminiResponse([]byte(payload))
		if err != nil {
			continue
		}
		s.observeRaw(unwrappedBytes)
		s.Options.ObserveImages(unwrappedBytes)

		// Discard a malformed decoded payload before bridge processing so a
		// partially decoded content map cannot alter the Messages event state for
		// this line.
		var geminiResp map[string]any
		if err := json.Unmarshal(unwrappedBytes, &geminiResp); err != nil {
			continue
		}

		for event := range state.Process(geminiResp, unwrappedBytes) {
			writeEvent(event)
		}

		// Process a final data line even when EOF arrives without a trailing newline.
		if errors.Is(err, io.EOF) {
			break
		}
	}

	for event := range state.Finish() {
		writeEvent(event)
	}
	return &StreamResult{Usage: LegacyNativeGeminiUsage(state.Usage()), FirstTokenMs: firstTokenMs}, nil
}

// WriteSSE serializes data into an event/data frame and invokes optional
// payload observers before the data write. JSON and writer errors are
// discarded; OutputContext writers can retain their own error state, and
// callers supply safe event labels.
func WriteSSE(w io.Writer, event string, data any, observers ...func([]byte)) {
	// Continue formatting the frame after write failure. OutputContext retains
	// disconnects for executor accounting; ordinary writers receive no returned
	// error from this helper.
	if event != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", event)
	}
	// Observers and the data writer receive the marshaled bytes even after an
	// encoding failure; a nil byte result therefore still produces the
	// surrounding SSE frame.
	b, _ := json.Marshal(data)
	for _, observe := range observers {
		if observe != nil {
			observe(b)
		}
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", string(b))
}

// UnwrapIfNeeded returns an extracted response only for OAuth payloads and
// otherwise returns raw. Paths returning raw share its byte slice; an
// extracted object or array uses the bytes produced by UnwrapGeminiResponse.
func UnwrapIfNeeded(isOAuth bool, raw []byte) []byte {
	if !isOAuth {
		return raw
	}
	inner, err := UnwrapGeminiResponse(raw)
	if err != nil {
		return raw
	}
	return inner
}

// CollectGeminiSSE collects decoded map payloads from individual data
// lines into one response and returns the latest extracted usage. Callers own
// the reader; DONE or EOF finishes collection. A DONE line returns before
// pending read-error handling; otherwise non-EOF errors discard the returned
// snapshots.
//
// JSON decode errors are ignored, so a partially decoded nonnil map can
// participate in collection. Content-bearing payloads are preferred over later
// metadata-only payloads, and their parts accumulate in encounter order.
// Adjacent same-thought text is joined before containers are merged into the
// selected response. Optional raw observers run synchronously for each
// participating map and can retain accounting observations even when a later
// read fails.
func CollectGeminiSSE(body io.Reader, isOAuth bool, observers ...func([]byte)) (map[string]any, *upstream.TokenUsage, error) {
	reader := bufio.NewReader(body)

	// Keep content and metadata observations separately because a terminal
	// usage-only payload can follow text; conversion still needs the content
	// snapshot alongside its latest usage.
	var last map[string]any
	var lastWithParts map[string]any
	var collectedParts []any
	usage := &upstream.TokenUsage{}

	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			trimmed := strings.TrimRight(line, "\r\n")
			if after, ok := strings.CutPrefix(trimmed, "data:"); ok {
				payload := strings.TrimSpace(after)
				switch payload {
				case "", "[DONE]":
					if payload == "[DONE]" {
						return MergeCollectedGeminiParts(PickGeminiCollectResult(last, lastWithParts), collectedParts), usage, nil
					}
				default:
					var parsed map[string]any
					var rawBytes []byte
					if isOAuth {
						// Use the extracted OAuth response bytes before map decoding so
						// collection sees inner generation content; decode errors are ignored
						// and the nonnil map check decides participation.
						innerBytes, err := UnwrapGeminiResponse([]byte(payload))
						if err == nil {
							rawBytes = innerBytes
							_ = json.Unmarshal(innerBytes, &parsed)
						}
					} else {
						rawBytes = []byte(payload)
						_ = json.Unmarshal(rawBytes, &parsed)
					}
					// A nonnil decoded map participates even after a JSON error, matching
					// this collector partial-map behavior; observers see the corresponding
					// raw payload before merging.
					if parsed != nil {
						for _, observe := range observers {
							if observe != nil {
								observe(rawBytes)
							}
						}
						last = parsed
						if u := ExtractGeminiUsage(rawBytes); u != nil {
							usage = u
						}
						if parts := ExtractGeminiParts(parsed); len(parts) > 0 {
							lastWithParts = parsed
							collectedParts = AppendCollectedGeminiParts(collectedParts, parts)
						}
					}
				}
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
	}

	return MergeCollectedGeminiParts(PickGeminiCollectResult(last, lastWithParts), collectedParts), usage, nil
}

// PickGeminiCollectResult selects the nonnil content-bearing snapshot before
// the last snapshot through the bridge. Supplied maps remain shared; when
// neither exists, the bridge returns a fresh empty object for conversion.
func PickGeminiCollectResult(last map[string]any, lastWithParts map[string]any) map[string]any {
	return bridge.NativePickGeminiCollectResult(last, lastWithParts)
}

// AppendCollectedGeminiParts shallow-copies incoming parts and joins adjacent
// text when incoming text is nonempty and boolean thought flags match. The
// returned slice can reuse collected storage, so callers use the returned
// value and serialize mutation.
//
// Joining copies the preceding map, appends new text, and retains its
// metadata; other attributes on the incoming text part are not merged.
// Nontext, empty incoming text, and differently flagged parts remain separate
// shallow copies, while nested values stay shared. This represents ordered
// text deltas and does not deduplicate cumulative payloads.
func AppendCollectedGeminiParts(collected []any, parts []map[string]any) []any {
	for _, part := range parts {
		partCopy := make(map[string]any, len(part))
		maps.Copy(partCopy, part)

		text, hasText := partCopy["text"].(string)
		if hasText && text != "" && len(collected) > 0 {
			if previous, ok := collected[len(collected)-1].(map[string]any); ok {
				previousText, previousHasText := previous["text"].(string)
				previousThought, _ := previous["thought"].(bool)
				currentThought, _ := partCopy["thought"].(bool)
				// Join text only across adjacent matching thought channels so reasoning
				// and visible text remain distinct; keep earlier metadata when adding the
				// next delta text.
				if previousHasText && previousThought == currentThought {
					previousCopy := make(map[string]any, len(previous))
					maps.Copy(previousCopy, previous)
					previousCopy["text"] = previousText + text
					collected[len(collected)-1] = previousCopy
					continue
				}
			}
		}

		collected = append(collected, partCopy)
	}
	return collected
}

// MergeCollectedGeminiParts places collected parts in the first candidate of a
// shallowly copied response. Empty parts returns the supplied response
// directly; otherwise callers receive copied modified containers and a new
// parts slice.
//
// The response map, candidates slice, first candidate map, and its content map
// are copied before replacement, retaining unrelated entries and nested
// references. Missing or unsuitable containers are created, with model role
// used for new content. Part values themselves stay shared, so the returned
// structure supports container replacement rather than arbitrary independent
// mutation of nested data.
func MergeCollectedGeminiParts(response map[string]any, collectedParts []any) map[string]any {
	if len(collectedParts) == 0 {
		return response
	}

	// Copy each container changed by first-candidate replacement, while leaving
	// unrelated nested values shared, so assigning collected parts does not
	// overwrite the source response containers.
	result := make(map[string]any)
	maps.Copy(result, response)

	candidates, ok := result["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		candidates = []any{map[string]any{}}
	} else {
		candidates = append([]any(nil), candidates...)
	}

	candidate, ok := candidates[0].(map[string]any)
	if !ok {
		candidate = make(map[string]any)
		candidates[0] = candidate
	} else {
		candidateCopy := make(map[string]any, len(candidate))
		maps.Copy(candidateCopy, candidate)
		candidate = candidateCopy
		candidates[0] = candidate
	}

	content, ok := candidate["content"].(map[string]any)
	if !ok {
		content = map[string]any{"role": "model"}
		candidate["content"] = content
	} else {
		contentCopy := make(map[string]any, len(content))
		maps.Copy(contentCopy, content)
		content = contentCopy
		candidate["content"] = content
	}

	content["parts"] = append([]any(nil), collectedParts...)
	result["candidates"] = candidates

	return result
}

// HandleNativeNonStreamingResponse reads through the caller body limiter,
// optionally unwraps OAuth data, and forwards status, content type, and
// filtered headers. It observes raw/image data before output; absent usage
// returns zero counters, and callers inspect output errors separately.
func (s *ResponseAdapter) HandleNativeNonStreamingResponse(c *upstream.OutputContext, resp *http.Response, isOAuth bool) (*upstream.TokenUsage, error) {
	if s.Options.DebugHeaders {
		logger.LegacyPrintf("service.gemini_messages_compat", "[GeminiAPI] ========== Response Headers ==========")
		for key, values := range resp.Header {
			if strings.HasPrefix(strings.ToLower(key), "x-ratelimit") {
				logger.LegacyPrintf("service.gemini_messages_compat", "[GeminiAPI] %s: %v", key, values)
			}
		}
		logger.LegacyPrintf("service.gemini_messages_compat", "[GeminiAPI] ========================================")
	}

	respBody, err := s.Options.ReadBody(resp.Body)
	if err != nil {
		return nil, err
	}

	if isOAuth {
		unwrappedBody, uwErr := UnwrapGeminiResponse(respBody)
		if uwErr == nil {
			respBody = unwrappedBody
		}
	}
	s.observeRaw(respBody)
	s.Options.ObserveImages(respBody)

	s.Options.WriteHeaders(c.Writer.Header(), resp.Header)

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.NextEvent(geminiwire.ObservePayload(respBody).Semantic, true)
	c.Data(resp.StatusCode, contentType, respBody)

	if u := ExtractGeminiUsage(respBody); u != nil {
		return u, nil
	}
	return &upstream.TokenUsage{}, nil
}

// HandleNativeStreamingResponse forwards native data lines and returns the
// latest usage with first-payload timing. Callers close resp.Body and supply a
// flusher-capable OutputContext; header forwarding follows HasHeaderFilter.
//
// OAuth data lines are unwrapped and rewritten as complete data frames, while
// REST lines retain their incoming spelling. Empty data and DONE pass through,
// and nondata lines are also forwarded. First-token timing starts at the first
// nonempty payload, independently of semantic content. Writer failures remain
// in OutputContext while reading continues; non-EOF read errors return an
// error and discard the returned stream snapshot.
func (s *ResponseAdapter) HandleNativeStreamingResponse(c *upstream.OutputContext, resp *http.Response, startTime time.Time, isOAuth bool) (*NativeStreamResult, error) {
	if s.Options.DebugHeaders {
		logger.LegacyPrintf("service.gemini_messages_compat", "[GeminiAPI] ========== Streaming Response Headers ==========")
		for key, values := range resp.Header {
			if strings.HasPrefix(strings.ToLower(key), "x-ratelimit") {
				logger.LegacyPrintf("service.gemini_messages_compat", "[GeminiAPI] %s: %v", key, values)
			}
		}
		logger.LegacyPrintf("service.gemini_messages_compat", "[GeminiAPI] ====================================================")
	}

	if s.Options.HasHeaderFilter {
		s.Options.WriteHeaders(c.Writer.Header(), resp.Header)
	}

	c.Status(resp.StatusCode)
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/event-stream; charset=utf-8"
	}
	c.Header("Content-Type", contentType)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	reader := bufio.NewReader(resp.Body)
	usage := &upstream.TokenUsage{}
	var firstTokenMs *int

	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			trimmed := strings.TrimRight(line, "\r\n")
			if after, ok0 := strings.CutPrefix(trimmed, "data:"); ok0 {
				payload := strings.TrimSpace(after)
				// Keep empty data and DONE lines as received because they describe stream
				// framing and completion independently of JSON payload conversion and
				// usage extraction.
				if payload == "[DONE]" {
					c.NextEvent(false, true)
				}
				if payload == "" || payload == "[DONE]" {
					_, _ = io.WriteString(c.Writer, line)
					flusher.Flush()
				} else {
					var rawToWrite string
					rawToWrite = payload

					var rawBytes []byte
					if isOAuth {
						innerBytes, err := UnwrapGeminiResponse([]byte(payload))
						if err == nil {
							rawToWrite = string(innerBytes)
							rawBytes = innerBytes
						}
					} else {
						rawBytes = []byte(payload)
					}

					if u := ExtractGeminiUsage(rawBytes); u != nil {
						usage = u
					}
					s.observeRaw(rawBytes)
					s.Options.ObserveImages(rawBytes)

					if firstTokenMs == nil {
						ms := int(time.Since(startTime).Milliseconds())
						firstTokenMs = &ms
						if s.Options.ObserveState != nil {
							s.Options.ObserveState(nil, firstTokenMs)
						}
					}

					observed := geminiwire.ObservePayload(rawBytes)
					c.NextEvent(observed.Semantic, observed.Terminal)
					if isOAuth {
						// The unwrapped OAuth payload is a rebuilt event rather than the source
						// line; terminate it with a blank line so native clients receive a
						// complete frame.
						_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", rawToWrite)
					} else {
						// REST output keeps the original data line because it already carries
						// the native payload spelling and its newline framing from the selected
						// upstream.
						_, _ = io.WriteString(c.Writer, line)
					}
					flusher.Flush()
				}
			} else {
				_, _ = io.WriteString(c.Writer, line)
				flusher.Flush()
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}

	return &NativeStreamResult{Usage: usage, FirstTokenMs: firstTokenMs}, nil
}

// UnwrapGeminiResponse extracts the raw object or array in response as new
// bytes, otherwise returning the supplied slice with nil error. Field
// extraction preserves nested JSON spelling and does not validate the complete
// outer document.
func UnwrapGeminiResponse(raw []byte) ([]byte, error) {
	result := gjson.GetBytes(raw, "response")
	if result.Exists() && result.Type == gjson.JSON {
		return []byte(result.Raw), nil
	}
	return raw, nil
}

// ConvertGeminiToClaudeMessage delegates buffered response conversion with
// generated message and tool identifiers. rawData supplies usage independently
// of the decoded map, and includeInlineData selects the bridge image-text
// representation in the returned Messages body.
func ConvertGeminiToClaudeMessage(geminiResp map[string]any, originalModel string, rawData []byte, includeInlineData bool) (map[string]any, *upstream.TokenUsage) {
	result, usage := bridge.NativeConvertGeminiToClaudeMessage(bridge.NativeGeminiRuntime{MessageID: upstream.GenerateAnthropicMsgID, RandomHex: upstream.RandomHex}, geminiResp, originalModel, rawData, includeInlineData)
	return result, LegacyNativeGeminiUsage(usage)
}

// ExtractGeminiUsage copies bridge token buckets into upstream usage,
// returning nil for absent metadata. Explicit null or zero metadata yields
// zero counters; cached input is separated from prompt input and thinking
// joins candidate output.
func ExtractGeminiUsage(data []byte) *upstream.TokenUsage {
	return LegacyNativeGeminiUsage(bridge.NativeExtractGeminiUsage(data))
}

// ExtractGeminiParts returns the bridge-selected first-candidate part maps
// from a decoded response. The maps retain shared nested values, and
// unsuitable candidate/content/part shapes yield an empty result.
func ExtractGeminiParts(geminiResp map[string]any) []map[string]any {
	return bridge.NativeExtractGeminiParts(geminiResp)
}

// LegacyNativeGeminiUsage copies the bridge token counters into a fresh
// upstream usage value. Nil input returns nil, and the four integer buckets
// retain their supplied accounting values.
func LegacyNativeGeminiUsage(usage *bridge.NativeGeminiUsage) *upstream.TokenUsage {
	return bridge.NativeUsageProjection(usage)
}

// observeRaw synchronously invokes the optional raw observer with the supplied
// payload bytes.
func (s *ResponseAdapter) observeRaw(payload []byte) {
	if s.Options.ObserveRaw != nil {
		s.Options.ObserveRaw(payload)
	}
}
