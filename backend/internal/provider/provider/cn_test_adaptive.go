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

// ExecuteAdaptive probes the enabled native endpoints for a CN provider.
// An empty selected value probes every enabled protocol; a non-empty value
// restricts the probe to that protocol.
func (s *CNProviderTest) ExecuteAdaptive(c *TestRun, value *providercore.Record, modelID string, prompt string, selected protocol.ProtocolID) error {
	testModelID := strings.TrimSpace(modelID)
	if testModelID == "" {
		testModelID = openai.DefaultTestModel
	}
	testModelID = mappedTestModel(value, testModelID)

	authToken := strings.TrimSpace(value.GetOpenAIProtocolAPIKey())
	if authToken == "" {
		return (TestStreamOutput{}).Error(c, "No API key available")
	}

	// Suppress intermediate completion events until every selected endpoint succeeds.
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
	// Chat emits its own start event; emit one here when Chat is not selected.
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
	payloadBytes, _ := json.Marshal(payload)

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "status", Text: "正在通过原生 /v1/messages 测试自适应 Anthropic 端点"})
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
		body, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("Adaptive Anthropic endpoint returned %d: %s", resp.StatusCode, string(body))
		if resp.StatusCode == http.StatusUnauthorized && s.Store != nil {
			_ = s.Store.SetError(ctx, value.ID, errMsg)
		}
		return (TestStreamOutput{}).Error(c, errMsg)
	}

	if err := (TestStreamOutput{}).AdaptiveAnthropic(c, resp.Body); err != nil {
		return err
	}
	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "status", Text: "已通过原生 /v1/messages 验证"})
	return nil
}

func (s *CNProviderTest) executeAdaptiveResponses(c *TestRun, value *providercore.Record, testModelID string, prompt string, authToken string) error {
	ctx := c.Context
	baseURL, err := s.ValidateURL((providercore.ProtocolTarget{Record: value}).GetCNProtocolBaseURL(providercore.APIProtocolResponses))
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Invalid adaptive Responses base URL: %s", err.Error()))
	}
	apiURL := cnTestResponsesURL(value.Platform, baseURL)

	payload := openai.TestResponsesPayload(testModelID, prompt, false)
	// The native Responses probe omits the synthetic instructions used by the OpenAI probe.
	delete(payload, "instructions")
	payloadBytes, _ := json.Marshal(payload)
	if (providercore.ProtocolTarget{Record: value}).UsesNativeCNResponses() {
		payloadBytes = openaiprotocol.StatelessResponsesRequest(payloadBytes)
	}

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "status", Text: "正在通过原生 /responses 测试自适应 Responses 端点"})
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
		body, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("Adaptive Responses endpoint returned %d: %s", resp.StatusCode, string(body))
		if resp.StatusCode == http.StatusUnauthorized && s.Store != nil {
			_ = s.Store.SetError(ctx, value.ID, errMsg)
		}
		return (TestStreamOutput{}).Error(c, errMsg)
	}

	if err := (TestStreamOutput{}).Responses(c, resp.Body); err != nil {
		return err
	}
	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "status", Text: "已通过原生 /responses 验证"})
	return nil
}

func (s *CNProviderTest) doAdaptiveRequest(req *http.Request, value *providercore.Record) (*http.Response, error) {
	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}
	return s.Transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, s.resolveTLSProfile(value))
}

// cnAnthropicBaseURLMisconfigHint identifies OpenAI endpoint paths in an Anthropic base URL.
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
