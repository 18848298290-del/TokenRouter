package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	openaiprotocol "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	claude "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// cn_test_adaptive.go tests enabled CN native protocols in sequence,
// optionally narrowed to one selected protocol. An empty selection checks
// enabled Chat, Messages, and Responses endpoints in that order, ending at the
// first failed endpoint.
//
// Each endpoint can emit content and status events, but completion is
// suppressed until every selected endpoint succeeds. Chat owns its start
// event; tests without Chat commit one start before the other endpoints run.
// Model preparation applies one mapping for Messages and Responses, while Chat
// delegates its original supplied model to the fixed executor. Native
// Responses remove synthetic Codex instructions and request stateless
// behavior, and native authentication failures can attempt provider error
// persistence.

// ExecuteAdaptive tests one selected native protocol or every enabled protocol
// for a CN record. Empty protocol sets and disabled explicit selections fail
// before an upstream request; a blank Messages or Responses model uses the
// OpenAI test default before mapping.
//
// Chat, Messages, and Responses execute in a fixed order and stop on their
// first failure. Completion suppression spans those attempts so intermediate
// parsers cannot declare the entire test successful. Chat supplies its own
// start event; otherwise this coordinator emits one before execution. The
// suppression flag is cleared on return and before the single final success
// event.
func (s *CNProviderTest) ExecuteAdaptive(c *TestRun, value *providercore.Record, modelID string, prompt string, selected protocol.ProtocolID) error {
	testModelID := strings.TrimSpace(modelID)
	if testModelID == "" {
		testModelID = openai.DefaultTestModel
	}
	testModelID = mappedTestModel(value, testModelID)
	if c.ProtocolRoute.ClientProtocol != "" {
		if _, err := resolveProbeType(c.Context, s.Attributes, value, c.GroupID, testModelID, string(selected), providercore.ProviderTestTypeText); err != nil {
			return (TestStreamOutput{}).Error(c, err.Error())
		}
	}

	authToken := strings.TrimSpace(value.GetOpenAIProtocolAPIKey())
	if authToken == "" {
		return (TestStreamOutput{}).Error(c, "No API key available")
	}

	// Each endpoint parser can emit success independently. Suppress those
	// intermediate completions so a later endpoint failure cannot follow an
	// already published whole-test success.
	c.SetSuppressCompletion(true)
	defer c.SetSuppressCompletion(false)
	enabled := value.UpstreamProtocols()
	if len(enabled) == 0 {
		return (TestStreamOutput{}).Error(c, "No upstream protocols enabled")
	}
	if selected != "" {
		if !slices.Contains(enabled, selected) {
			return (TestStreamOutput{}).Error(c, fmt.Sprintf("Upstream protocol %s is not enabled", selected))
		}
		enabled = []protocol.ProtocolID{selected}
	}
	c.Begin(false)
	// The fixed Chat executor commits its own start event. When Chat is absent,
	// commit one here before Messages or Responses content can reach the sink.
	if !slices.Contains(enabled, protocol.ProtocolOpenAIChatCompletions) {
		c.Begin(true)
		(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_start", Model: testModelID})
	}
	if slices.Contains(enabled, protocol.ProtocolOpenAIChatCompletions) {
		if err := s.executeChat(c, value, modelID, prompt); err != nil {
			return err
		}
	}

	if slices.Contains(enabled, protocol.ProtocolAnthropicMessages) {
		if err := s.executeAdaptiveAnthropic(c, value, testModelID, prompt, authToken); err != nil {
			return err
		}
	}

	if slices.Contains(enabled, protocol.ProtocolOpenAIResponses) {
		if err := s.executeAdaptiveResponses(c, value, testModelID, prompt, authToken); err != nil {
			return err
		}
	}

	c.SetSuppressCompletion(false)
	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_complete", Success: true})
	return nil
}

// executeAdaptiveAnthropic runs a mapped native Messages probe with the
// supplied key and prompt. It validates the protocol root, applies the selected
// API-key scheme and allowed overrides, closes the response, and records 401
// state best effort.
func (s *CNProviderTest) executeAdaptiveAnthropic(c *TestRun, value *providercore.Record, testModelID string, prompt string, authToken string) error {
	ctx := c.Context
	baseURL, err := s.ValidateURL((providercore.ProtocolTarget{Record: value}).GetCNProtocolBaseURL(providercore.APIProtocolAnthropic))
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Invalid adaptive Anthropic base URL: %s", err.Error()))
	}
	apiURL := strings.TrimRight(baseURL, "/") + "/v1/messages"

	payload, err := claude.TestPayloadWithPrompt(testModelID, prompt)
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create adaptive Anthropic test payload")
	}
	if err := applyProbeSampling(c, s.Attributes, value, testModelID, "anthropic_messages", payload); err != nil {
		return (TestStreamOutput{}).Error(c, err.Error())
	}
	// These fixed probe payloads contain JSON-compatible values supplied by
	// platform builders. Encoding retains their supported shapes after the local
	// protocol preparation.
	payloadBytes, _ := json.Marshal(payload)

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "status", Text: "Testing adaptive Anthropic endpoint through native /v1/messages"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create adaptive Anthropic request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("anthropic-version", "2023-06-01")
	for key, value := range claude.DefaultHeaders {
		req.Header.Set(key, value)
	}
	req.Header.Set("anthropic-beta", claude.APIKeyBetaHeader)
	claude.SetAPIKeyAuthHeader(req.Header, value.GetAnthropicAPIKeyAuthScheme() == providercore.AnthropicAPIKeyAuthSchemeAuthorizationBearer, authToken)
	applyGrokQuotaHeaders(value, req.Header)

	resp, err := s.doAdaptiveRequest(req, value)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Adaptive Anthropic endpoint request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// Use the available non-OK response prefix for endpoint diagnostics even if
		// reading fails, retaining the status that caused this adaptive attempt to
		// stop.
		body, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("Adaptive Anthropic endpoint returned %d: %s", resp.StatusCode, string(body))
		if resp.StatusCode == http.StatusUnauthorized && s.Store != nil {
			// The endpoint has already returned an authentication failure. Attempt its
			// provider-state write, then keep the upstream diagnostic when storage
			// itself reports an error.
			_ = s.Store.SetError(ctx, value.ID, errMsg)
		}
		return (TestStreamOutput{}).Error(c, errMsg)
	}

	if err := (TestStreamOutput{}).AdaptiveAnthropic(c, resp.Body); err != nil {
		return err
	}
	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "status", Text: "Verified through native /v1/messages"})
	return nil
}

// executeAdaptiveResponses runs a mapped native Responses probe with the
// supplied key and prompt. It omits Codex instructions, normalizes supported CN
// requests to stateless form, closes the response, and records 401 state best
// effort.
func (s *CNProviderTest) executeAdaptiveResponses(c *TestRun, value *providercore.Record, testModelID string, prompt string, authToken string) error {
	ctx := c.Context
	baseURL, err := s.ValidateURL((providercore.ProtocolTarget{Record: value}).GetCNProtocolBaseURL(providercore.APIProtocolResponses))
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Invalid adaptive Responses base URL: %s", err.Error()))
	}
	apiURL := cnTestResponsesURL(value.Platform, baseURL)

	payload := openai.TestResponsesPayload(testModelID, prompt, false)
	if err := applyProbeSampling(c, s.Attributes, value, testModelID, "openai_responses", payload); err != nil {
		return (TestStreamOutput{}).Error(c, err.Error())
	}
	// Codex's probe builder supplies synthetic instructions for its own endpoint.
	// Remove that field before native CN Responses execution so this payload
	// carries the selected prompt directly.
	delete(payload, "instructions")
	// These fixed probe payloads contain JSON-compatible values supplied by
	// platform builders. Encoding retains their supported shapes after the local
	// protocol preparation.
	payloadBytes, _ := json.Marshal(payload)
	if (providercore.ProtocolTarget{Record: value}).UsesNativeCNResponses() {
		// Native CN Responses use fresh stateless requests. Set store false and
		// remove any continuation identifier before sending this probe through the
		// platform endpoint.
		payloadBytes = openaiprotocol.StatelessResponsesRequest(payloadBytes)
	}

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "status", Text: "Testing adaptive Responses endpoint through native /responses"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create adaptive Responses request")
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+authToken)
	openai.ApplyOpenAICodexProbeHeaders(req.Header)
	applyGrokQuotaHeaders(value, req.Header)

	resp, err := s.doAdaptiveRequest(req, value)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Adaptive Responses endpoint request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// Use the available non-OK response prefix for endpoint diagnostics even if
		// reading fails, retaining the status that caused this adaptive attempt to
		// stop.
		body, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("Adaptive Responses endpoint returned %d: %s", resp.StatusCode, string(body))
		if resp.StatusCode == http.StatusUnauthorized && s.Store != nil {
			// The endpoint has already returned an authentication failure. Attempt its
			// provider-state write, then keep the upstream diagnostic when storage
			// itself reports an error.
			_ = s.Store.SetError(ctx, value.ID, errMsg)
		}
		return (TestStreamOutput{}).Error(c, errMsg)
	}

	if err := (TestStreamOutput{}).Responses(c, resp.Body); err != nil {
		return err
	}
	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "status", Text: "Verified through native /responses"})
	return nil
}

// doAdaptiveRequest sends a prepared native request with the record's loaded
// proxy and selected TLS profile. A missing loaded proxy relation leaves the
// proxy URL empty; the caller closes any returned response body.
func (s *CNProviderTest) doAdaptiveRequest(req *http.Request, value *providercore.Record) (*http.Response, error) {
	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}
	return s.Transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, s.resolveTLSProfile(value))
}

// cnAnthropicBaseURLMisconfigHint returns a diagnostic for OpenAI-shaped paths
// in an Anthropic root. Invalid or incomplete URLs and unrecognized paths
// return empty text; callers validate the root separately before using this
// hint.
func cnAnthropicBaseURLMisconfigHint(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	path := strings.ToLower(strings.TrimRight(parsed.Path, "/"))
	if path == "" {
		return ""
	}
	openAICompatShaped := strings.Contains(path, "/paas/") ||
		strings.HasSuffix(path, "/chat/completions") ||
		strings.HasSuffix(path, "/responses") ||
		httpclient.OpenAIBaseURLHasVersionSuffix(path)
	if !openAICompatShaped {
		return ""
	}
	return fmt.Sprintf(
		"API protocol is anthropic but base_url (%s) looks like an OpenAI-compatible endpoint; set base_url to the provider's Anthropic endpoint (for example https://open.bigmodel.cn/api/anthropic) or switch api_protocol.",
		baseURL,
	)
}
