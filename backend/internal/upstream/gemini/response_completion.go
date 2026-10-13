package gemini

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	geminiwire "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Client streaming and upstream streaming are separate Gemini choices. A
// buffered client can consume upstream SSE by collecting parts before protocol
// conversion, and each completion entry chooses the matching native, Messages,
// Chat, or Responses writer.
//
// Collected usage may be newer than the content-bearing payload chosen for
// conversion. Messages selects collected usage when its input or output is
// positive, while OpenAI conversion also accepts a positive cache count.
// Native and OpenAI paths return zero counters when conversion supplies no
// usage. Response-body ownership remains with the executor, and downstream
// write errors remain observable through the OutputContext.

// CompleteMessages selects streamed conversion, collected OAuth SSE, or
// buffered conversion for the client response. Callers align upstream framing
// with stream/useUpstreamStream and own resp.Body closing.
//
// For a buffered upstream stream, content parts are collected before Messages
// conversion and the collected usage replaces converted usage when its input
// or output count is positive. Image observations use the serialized combined
// response. Serialization recovery delegates to OutputContext.JSON; callers
// inspect output errors separately from conversion errors. Legacy first-token
// timing is supplied by the streaming adapter and remains nil for buffered
// completion.
func (s *ResponseAdapter) CompleteMessages(c *upstream.OutputContext, resp *http.Response, startTime time.Time, originalModel string, stream, useUpstreamStream bool) (*upstream.TokenUsage, *int, error) {
	var err error
	var usage *upstream.TokenUsage
	var firstTokenMs *int
	if stream {
		streamRes, err := s.HandleStreamingResponse(c, resp, startTime, originalModel)
		if err != nil {
			return nil, nil, err
		}
		usage = streamRes.Usage
		firstTokenMs = streamRes.FirstTokenMs
	} else {
		if useUpstreamStream {
			collected, usageObj, err := CollectGeminiSSE(resp.Body, true, s.Options.ObserveRaw)
			if err != nil {
				return nil, nil, s.Options.ClaudeError(http.StatusBadGateway, "upstream_error", "Failed to read upstream stream")
			}
			// Collected maps contain decoded JSON values. Their encoding has no custom
			// marshaler or reference cycle; optional raw observers already received each
			// participating payload.
			collectedBytes, _ := json.Marshal(collected)
			s.Options.ObserveImages(collectedBytes)
			claudeResp, usageObj2 := ConvertGeminiToClaudeMessage(collected, originalModel, collectedBytes, false)
			if body, err := json.Marshal(claudeResp); err == nil {
				c.NextEvent(bridge.CompatJSONHasContent(body), true)
				c.Data(http.StatusOK, "application/json; charset=utf-8", body)
			} else {
				// Delegate failed direct serialization to OutputContext.JSON, which can
				// commit a header-only result when its own marshal fails; the executor
				// still inspects retained sink errors.
				c.JSON(http.StatusOK, claudeResp)
			}
			usage = usageObj2
			// Prefer positive collected input/output observations because a later
			// usage-only SSE payload may carry final counts absent from the selected
			// content-bearing response used for conversion.
			if usageObj != nil && (usageObj.InputTokens > 0 || usageObj.OutputTokens > 0) {
				usage = usageObj
			}
		} else {
			usage, err = s.HandleNonStreamingResponse(c, resp, originalModel)
			if err != nil {
				return nil, nil, err
			}
		}
	}

	return usage, firstTokenMs, nil
}

// CompleteNative selects native streaming, buffered SSE collection, or
// buffered payload forwarding from the two stream flags. Callers pass the
// OAuth wrapper choice and close resp.Body; a missing usage result becomes
// zero counters.
//
// Collected SSE is reencoded as one native JSON response and observed for
// images before output. Direct buffered forwarding retains upstream status,
// whereas collection emits a successful combined response. Stream adapters
// report their legacy first-payload timing, while buffered paths keep it nil.
// Read and conversion failures return before a completed result is supplied.
func (s *ResponseAdapter) CompleteNative(c *upstream.OutputContext, resp *http.Response, startTime time.Time, stream, useUpstreamStream, isOAuth bool) (*upstream.TokenUsage, *int, error) {
	var usage *upstream.TokenUsage
	var firstTokenMs *int

	if stream {
		streamRes, err := s.HandleNativeStreamingResponse(c, resp, startTime, isOAuth)
		if err != nil {
			return nil, nil, err
		}
		usage = streamRes.Usage
		firstTokenMs = streamRes.FirstTokenMs
	} else {
		if useUpstreamStream {
			collected, usageObj, err := CollectGeminiSSE(resp.Body, isOAuth, s.Options.ObserveRaw)
			if err != nil {
				return nil, nil, s.Options.GoogleError(http.StatusBadGateway, "Failed to read upstream stream")
			}
			// Collection supplies ordinary JSON-decoded maps, so serialization errors
			// are discarded on this combined native path; raw observers have already
			// recorded each participating payload.
			b, _ := json.Marshal(collected)
			s.Options.ObserveImages(b)
			c.NextEvent(geminiwire.ObservePayload(b).Semantic, true)
			c.Data(http.StatusOK, "application/json", b)
			usage = usageObj
		} else {
			usageResp, err := s.HandleNativeNonStreamingResponse(c, resp, isOAuth)
			if err != nil {
				return nil, nil, err
			}
			usage = usageResp
		}
	}

	if usage == nil {
		usage = &upstream.TokenUsage{}
	}

	return usage, firstTokenMs, nil
}

// CompleteOpenAI selects Chat or Responses output and streamed, collected, or
// buffered Gemini input handling. Callers supply callbacks for the selected
// protocol and retain response-body ownership through completion.
//
// Buffered upstream SSE is collected before conversion and its usage is
// offered as the complete override observation; the bridge decides whether
// that override qualifies. Responses output restores client tool
// representations, while Chat output can delegate serialization recovery to
// OutputContext.JSON. Missing returned usage becomes zero counters, legacy
// timing comes from the streaming adapter, and callers inspect OutputContext
// for client-write errors.
func (s *ResponseAdapter) CompleteOpenAI(c *upstream.OutputContext, resp *http.Response, startTime time.Time, originalModel string, clientStream, useUpstreamStream, isOAuth, includeUsage bool, protocol OpenAICompatProtocol, clientToolMapping bridge.ResponsesClientToolMapping) (*upstream.TokenUsage, *int, error) {
	var err error
	var usage *upstream.TokenUsage
	var firstTokenMs *int
	if clientStream {
		streamRes, err := s.HandleOpenAICompatStreamingResponseFromGemini(c, resp, startTime, originalModel, isOAuth, includeUsage, protocol, clientToolMapping)
		if err != nil {
			return nil, nil, err
		}
		usage = streamRes.Usage
		firstTokenMs = streamRes.FirstTokenMs
	} else if useUpstreamStream {
		collected, usageObj, err := CollectGeminiSSE(resp.Body, isOAuth, s.Options.ObserveRaw)
		if err != nil {
			return nil, nil, s.Options.CompatError(protocol, http.StatusBadGateway, "upstream_error", "Failed to read upstream stream")
		}
		// Collected maps contain decoded JSON values. Encoding them has no custom
		// marshaler or reference cycle; optional raw observers already received the
		// participating payloads.
		collectedBytes, _ := json.Marshal(collected)
		if protocol == OpenAICompatResponses {
			responsesResp, usageObj2, convertErr := GeminiResponseToResponses(collected, originalModel, collectedBytes, usageObj)
			if convertErr != nil {
				return nil, nil, s.Options.CompatError(protocol, http.StatusBadGateway, "upstream_error", "Failed to parse upstream response")
			}
			if err = s.WriteGeminiResponsesResponse(c, resp, responsesResp, clientToolMapping); err != nil {
				return nil, nil, err
			}
			usage = usageObj2
		} else {
			chatResp, usageObj2, convertErr := GeminiResponseToChatCompletions(collected, originalModel, collectedBytes, usageObj)
			if convertErr != nil {
				return nil, nil, s.Options.CompatError(protocol, http.StatusBadGateway, "upstream_error", "Failed to parse upstream response")
			}
			if responseBytes, marshalErr := json.Marshal(chatResp); marshalErr == nil {
				c.NextEvent(bridge.CompatJSONHasContent(responseBytes), true)
				c.Data(http.StatusOK, "application/json; charset=utf-8", responseBytes)
			} else {
				// Use OutputContext.JSON for the failed Chat serialization path so its
				// header-only marshal-failure behavior and retained sink-error handling
				// apply to this completion.
				c.JSON(http.StatusOK, chatResp)
			}
			usage = usageObj2
		}
	} else {
		var usageResp *upstream.TokenUsage
		if protocol == OpenAICompatResponses {
			usageResp, err = s.HandleResponsesNonStreamingResponseFromGemini(c, resp, originalModel, isOAuth, clientToolMapping)
		} else {
			usageResp, err = s.HandleChatCompletionsNonStreamingResponseFromGemini(c, resp, originalModel, isOAuth)
		}
		if err != nil {
			return nil, nil, err
		}
		usage = usageResp
	}

	if usage == nil {
		usage = &upstream.TokenUsage{}
	}

	return usage, firstTokenMs, nil
}
