package antigravity

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"time"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Buffered OpenAI clients receive a collected Claude answer converted through
// the shared Responses representation. Collection preserves the Antigravity
// timing/usage result, while serialized output restores provider tool names
// and Responses-specific client tool forms before delivery.

// HandleChatCompletionsNonStreamingFromAntigravity delegates stream collection
// and shared Chat conversion using originalModel. Callers supply
// collection/error/tool callbacks, keep output and upstream body valid through
// completion, and close the body after return. Buffered write failures remain
// in OutputContext.Err; the returned collection snapshot does not acquire a
// ClientDisconnect flag from that write.
func (s *ResponseAdapter) HandleChatCompletionsNonStreamingFromAntigravity(c *upstream.OutputContext, resp *http.Response, startTime time.Time, originalModel string) (*StreamResult, error) {
	claudeResponse, result, err := s.CollectClaudeStreamResponse(resp, startTime, originalModel)
	if err != nil {
		return nil, s.Options.MapCollectionError(err)
	}
	var anthropicResponse protocolanthropic.AnthropicResponse
	if json.Unmarshal(claudeResponse, &anthropicResponse) != nil {
		return nil, s.Options.CompatError(http.StatusBadGateway, "upstream_error", "Failed to parse upstream response")
	}
	responsesResponse := bridge.AnthropicToResponsesResponse(bridge.Runtime{Now: time.Now, ReadRandom: rand.Read}, &anthropicResponse)
	chatResponse := bridge.ResponsesToChatCompletions(bridge.Runtime{Now: time.Now, ReadRandom: rand.Read}, responsesResponse, originalModel)
	payload, err := json.Marshal(chatResponse)
	if err != nil {
		return nil, s.Options.CompatError(http.StatusBadGateway, "upstream_error", "Failed to serialize upstream response")
	}
	payload = s.Options.ReverseTools(payload)
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
	return result, nil
}

// HandleResponsesNonStreamingFromAntigravity delegates collection and
// Responses conversion using originalModel and clientToolMapping. Callers keep
// the mapping stable, supply restoration/error callbacks, and close the
// upstream body after handling; successful collection observations accompany
// buffered output. Buffered write failures remain in OutputContext.Err; the
// returned collection snapshot does not acquire a ClientDisconnect flag from
// that write.
func (s *ResponseAdapter) HandleResponsesNonStreamingFromAntigravity(
	c *upstream.OutputContext,
	resp *http.Response,
	startTime time.Time,
	originalModel string,
	clientToolMapping bridge.ResponsesClientToolMapping,
) (*StreamResult, error) {
	claudeResponse, result, err := s.CollectClaudeStreamResponse(resp, startTime, originalModel)
	if err != nil {
		return nil, s.Options.MapCollectionError(err)
	}
	var anthropicResponse protocolanthropic.AnthropicResponse
	if json.Unmarshal(claudeResponse, &anthropicResponse) != nil {
		return nil, s.Options.CompatError(http.StatusBadGateway, "upstream_error", "Failed to parse upstream response")
	}
	responsesResponse := bridge.AnthropicToResponsesResponse(bridge.Runtime{Now: time.Now, ReadRandom: rand.Read}, &anthropicResponse)
	responsesResponse.Model = originalModel
	payload, err := json.Marshal(responsesResponse)
	if err != nil {
		return nil, s.Options.CompatError(http.StatusBadGateway, "upstream_error", "Failed to serialize upstream response")
	}
	// Restore provider names before client tool forms so serialized Responses
	// output can recover client-facing identities using the mapping associated
	// with this attempt.
	payload = s.Options.ReverseTools(payload)
	payload, _, err = bridge.RestoreResponsesClientToolPayload(payload, clientToolMapping)
	if err != nil {
		return nil, s.Options.CompatError(http.StatusBadGateway, "upstream_error", "Failed to restore client tools")
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
	return result, nil
}
