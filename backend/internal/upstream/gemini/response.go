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

type ResponseOptions struct {
	ObserveRaw                    func([]byte)
	ObserveState                  func(*upstream.TokenUsage, *int)
	GoogleError                   func(int, string) error
	ReadBody                      func(io.Reader) ([]byte, error)
	WriteHeaders                  func(http.Header, http.Header)
	DebugHeaders, HasHeaderFilter bool
	ObserveImages                 func([]byte)
	ReverseTools                  func([]byte) []byte
	ClaudeError, ChatError        func(int, string, string) error
	CompatError                   func(OpenAICompatProtocol, int, string, string) error
}
type (
	ResponseAdapter struct{ Options ResponseOptions }
	StreamResult    struct {
		Usage        *upstream.TokenUsage
		FirstTokenMs *int
	}
)

type NativeStreamResult struct {
	Usage        *upstream.TokenUsage
	FirstTokenMs *int
}

func (s *ResponseAdapter) HandleNonStreamingResponse(c *upstream.OutputContext, resp *http.Response, originalModel string) (*upstream.TokenUsage, error) {
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

func WriteSSE(w io.Writer, event string, data any, observers ...func([]byte)) {
	if event != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", event)
	}
	b, _ := json.Marshal(data)
	for _, observe := range observers {
		if observe != nil {
			observe(b)
		}
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", string(b))
}

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

func CollectGeminiSSE(body io.Reader, isOAuth bool, observers ...func([]byte)) (map[string]any, *upstream.TokenUsage, error) {
	reader := bufio.NewReader(body)

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
						innerBytes, err := UnwrapGeminiResponse([]byte(payload))
						if err == nil {
							rawBytes = innerBytes
							_ = json.Unmarshal(innerBytes, &parsed)
						}
					} else {
						rawBytes = []byte(payload)
						_ = json.Unmarshal(rawBytes, &parsed)
					}
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

func PickGeminiCollectResult(last map[string]any, lastWithParts map[string]any) map[string]any {
	return bridge.NativePickGeminiCollectResult(last, lastWithParts)
}

// AppendCollectedGeminiParts joins adjacent text deltas with the same thought flag.
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

// MergeCollectedGeminiParts writes the collected SSE parts into the first candidate.
func MergeCollectedGeminiParts(response map[string]any, collectedParts []any) map[string]any {
	if len(collectedParts) == 0 {
		return response
	}

	// Copy each modified container so the caller retains its response maps and slices.
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
				// Pass empty payloads and completion markers through unchanged.
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
						// Terminate each rebuilt SSE event with a blank line.
						_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", rawToWrite)
					} else {
						// Pass AI Studio data lines through unchanged.
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

// UnwrapGeminiResponse extracts an object or array in response, otherwise returning raw.
// It uses the raw JSON field to avoid decoding and re-encoding the full payload.
func UnwrapGeminiResponse(raw []byte) ([]byte, error) {
	result := gjson.GetBytes(raw, "response")
	if result.Exists() && result.Type == gjson.JSON {
		return []byte(result.Raw), nil
	}
	return raw, nil
}

// ConvertGeminiToClaudeMessage converts a response with generated message IDs and upstream usage counters.
func ConvertGeminiToClaudeMessage(geminiResp map[string]any, originalModel string, rawData []byte, includeInlineData bool) (map[string]any, *upstream.TokenUsage) {
	result, usage := bridge.NativeConvertGeminiToClaudeMessage(bridge.NativeGeminiRuntime{MessageID: upstream.GenerateAnthropicMsgID, RandomHex: upstream.RandomHex}, geminiResp, originalModel, rawData, includeInlineData)
	return result, LegacyNativeGeminiUsage(usage)
}

// ExtractGeminiUsage parses Gemini usage through the protocol bridge into upstream counters.
func ExtractGeminiUsage(data []byte) *upstream.TokenUsage {
	return LegacyNativeGeminiUsage(bridge.NativeExtractGeminiUsage(data))
}

// ExtractGeminiParts returns response content parts through the Gemini protocol bridge.
func ExtractGeminiParts(geminiResp map[string]any) []map[string]any {
	return bridge.NativeExtractGeminiParts(geminiResp)
}

func LegacyNativeGeminiUsage(usage *bridge.NativeGeminiUsage) *upstream.TokenUsage {
	return bridge.NativeUsageProjection(usage)
}

func (s *ResponseAdapter) observeRaw(payload []byte) {
	if s.Options.ObserveRaw != nil {
		s.Options.ObserveRaw(payload)
	}
}
