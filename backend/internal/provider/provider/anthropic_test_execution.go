package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	claude "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/bedrock"
	"github.com/TokenFlux/TokenRouter/internal/upstream/vertex"
)

// AnthropicProviderTest probes Anthropic transports using provider credential and health services.
type AnthropicProviderTest struct {
	Attributes  func() modelcatalog.View
	Tokens      *providercore.ClaudeTokenSource
	Transport   QoderTransport
	Profiles    *egressprovider.TLSProfiles
	ValidateURL func(string) (string, error)
	Store       interface {
		SetError(context.Context, int64, string) error
	}
	UserAgent string
}

// Execute selects the Anthropic, Vertex, or Bedrock probe for the provider type.
func (s *AnthropicProviderTest) Execute(c *TestRun, value *providercore.Record, modelID string, prompt string) error {
	ctx := c.Context

	testModelID := modelID
	if testModelID == "" {
		testModelID = claude.DefaultTestModel
	}

	// Apply wildcard mappings to API key probes as well as forwarded requests.
	if value.Type == "apikey" {
		testModelID = mappedTestModel(value, testModelID)
	}

	// Bedrock uses a signed, non-streaming invocation.
	if value.IsBedrock() {
		return s.ExecuteBedrock(c, ctx, value, testModelID, prompt)
	}
	if value.Type == capability.ProviderTypeServiceAccount {
		return s.executeVertex(c, ctx, value, testModelID, prompt)
	}

	var authToken string
	var apiURL string

	if value.IsOAuth() {
		apiURL = "https://api.anthropic.com/v1/messages?beta=true"
		authToken = value.GetCredential("access_token")
		if authToken == "" {
			return (TestStreamOutput{}).Error(c, "No access token available")
		}
	} else if value.Type == "apikey" {
		authToken = value.GetCredential("api_key")
		if authToken == "" {
			return (TestStreamOutput{}).Error(c, "No API key available")
		}

		baseURL := value.GetBaseURL()
		if baseURL == "" {
			baseURL = "https://api.anthropic.com"
		}
		normalizedBaseURL, err := s.ValidateURL(baseURL)
		if err != nil {
			return (TestStreamOutput{}).Error(c, fmt.Sprintf("Invalid base URL: %s", err.Error()))
		}
		apiURL = strings.TrimSuffix(normalizedBaseURL, "/") + "/v1/messages?beta=true"
	} else {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Unsupported provider type: %s", value.Type))
	}

	c.Begin(true)

	payload, err := claude.TestPayloadWithPrompt(testModelID, prompt)
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create test payload")
	}
	s.applyModelAttributes(value, testModelID, payload)
	payloadBytes, _ := json.Marshal(payload)

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_start", Model: testModelID})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create request")
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	for key, value := range claude.DefaultHeaders {
		req.Header.Set(key, value)
	}

	if value.IsOAuth() {
		req.Header.Set("anthropic-beta", claude.DefaultBetaHeader)
		req.Header.Set("Authorization", "Bearer "+authToken)
	} else {
		req.Header.Set("anthropic-beta", claude.APIKeyBetaHeader)
		claude.SetAPIKeyAuthHeader(req.Header, value.GetAnthropicAPIKeyAuthScheme() == providercore.AnthropicAPIKeyAuthSchemeAuthorizationBearer, authToken)
	}
	s.applyUserAgent(req)

	// Apply provider header overrides after defaults and authentication, as forwarding does.
	applyGrokQuotaHeaders(value, req.Header)

	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}

	resp, err := s.Transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, s.resolveTLSProfile(value))
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body))

		// A forbidden probe marks the provider as errored.
		if resp.StatusCode == http.StatusForbidden {
			_ = s.Store.SetError(ctx, value.ID, errMsg)
		}

		return (TestStreamOutput{}).Error(c, errMsg)
	}

	return (TestStreamOutput{}).Anthropic(c, resp.Body)
}

func (s *AnthropicProviderTest) executeVertex(c *TestRun, ctx context.Context, value *providercore.Record, testModelID string, prompt string) error {
	if mappedModel, matched := providercore.ResolveMappedModel(providercore.ResolveModelMapping(value, ModelDefaults()), testModelID); matched {
		testModelID = mappedModel
	} else {
		testModelID = vertex.NormalizeVertexAnthropicModelID(testModelID)
	}

	c.Begin(true)

	payload, err := claude.TestPayloadWithPrompt(testModelID, prompt)
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create test payload")
	}
	s.applyModelAttributes(value, testModelID, payload)
	payloadBytes, _ := json.Marshal(payload)
	vertexBody, err := vertex.BuildVertexAnthropicRequestBody(payloadBytes)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to create Vertex request body: %s", err.Error()))
	}

	if s.Tokens == nil {
		return (TestStreamOutput{}).Error(c, "Claude token provider not configured")
	}
	accessToken, err := s.Tokens.GetAccessToken(ctx, value)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to get service account access token: %s", err.Error()))
	}

	fullURL, err := vertex.BuildVertexAnthropicURL(value.VertexProjectID(vertex.ServiceAccountProjectID), value.VertexLocation(testModelID), testModelID, true)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to build Vertex URL: %s", err.Error()))
	}

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_start", Model: testModelID})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(vertexBody))
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	s.applyUserAgent(req)

	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}

	resp, err := s.Transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, s.resolveTLSProfile(value))
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body))
		if resp.StatusCode == http.StatusForbidden {
			_ = s.Store.SetError(ctx, value.ID, errMsg)
		}
		return (TestStreamOutput{}).Error(c, errMsg)
	}

	return (TestStreamOutput{}).Anthropic(c, resp.Body)
}

// ExecuteBedrock probes SigV4 or API key credentials with a non-streaming invocation.
func (s *AnthropicProviderTest) ExecuteBedrock(c *TestRun, ctx context.Context, value *providercore.Record, testModelID string, prompt string) error {
	route, err := bedrock.ResolveBedrockModelRoute(&bedrock.RouteInput{Region: value.GetCredential("aws_region"), ForceGlobal: value.GetCredential("aws_force_global") == "true", Model: mappedTestModel(value, testModelID)}, testModelID)
	if err != nil {
		return (TestStreamOutput{}).Error(c, bedrock.BedrockRoutingDiagnostic(err))
	}
	region := route.SourceRegion
	testModelID = route.ModelID

	// The test UI consumes SSE even though the upstream invocation is non-streaming.
	c.Begin(true)

	// The Bedrock probe omits stream and cache_control fields.
	testPrompt := strings.TrimSpace(prompt)
	if testPrompt == "" {
		testPrompt = "hi"
	}
	bedrockPayload := map[string]any{
		"anthropic_version": "bedrock-2023-05-31",
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{
						"type": "text",
						"text": testPrompt,
					},
				},
			},
		},
		"max_tokens":  256,
		"temperature": 1,
	}
	s.applyModelAttributes(value, testModelID, bedrockPayload)
	bedrockBody, _ := json.Marshal(bedrockPayload)

	apiURL := bedrock.BuildBedrockURL(region, testModelID, false)

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_start", Model: testModelID})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bedrockBody))
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create request")
	}
	req.Header.Set("Content-Type", "application/json")

	if value.IsBedrockAPIKey() {
		apiKey := value.GetCredential("api_key")
		if apiKey == "" {
			return (TestStreamOutput{}).Error(c, "No API key available")
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
	} else {
		signer, err := NewBedrockSignerFromProvider(value)
		if err != nil {
			return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to create Bedrock signer: %s", err.Error()))
		}
		if err := signer.SignRequest(ctx, req, bedrockBody); err != nil {
			return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to sign request: %s", err.Error()))
		}
	}
	s.applyUserAgent(req)

	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}

	resp, err := s.Transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, nil)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body)))
	}

	// Expose the first content block from the non-streaming Claude response.
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to parse response: %s", err.Error()))
	}

	text := ""
	if len(result.Content) > 0 {
		text = result.Content[0].Text
	}
	if text == "" {
		text = "(empty response)"
	}

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "content", Text: text})
	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_complete", Success: true})
	return nil
}

func (s *AnthropicProviderTest) applyUserAgent(req *http.Request) {
	if agent := strings.TrimSpace(s.UserAgent); agent != "" {
		req.Header.Set("User-Agent", agent)
	}
}

func (s *AnthropicProviderTest) resolveTLSProfile(value *providercore.Record) *tlsfingerprint.Profile {
	if s.Profiles == nil {
		return nil
	}
	return s.Profiles.ResolveRequestTLS(egress.TLSSelection{Enabled: value.IsTLSFingerprintEnabled(), DirectProfileID: value.GetTLSFingerprintProfileID()})
}

// mappedTestModel applies one provider mapping to a probe model.
func mappedTestModel(value *providercore.Record, model string) string {
	mapped, _ := providercore.ResolveMappedModel(providercore.ResolveModelMapping(value, ModelDefaults()), model)
	return mapped
}

func (s *AnthropicProviderTest) applyModelAttributes(value *providercore.Record, model string, payload map[string]any) {
	var view modelcatalog.View
	if s.Attributes != nil {
		view = s.Attributes()
	}
	resolved := view.Resolve(value.ModelAttributeTarget(model, "anthropic_messages"), modelcatalog.Attributes{}).ForRequest(modelcatalog.RequestState{})
	if resolved.Attributes.Temperature != nil && !*resolved.Attributes.Temperature {
		delete(payload, "temperature")
	}
	if resolved.Attributes.TopP != nil && !*resolved.Attributes.TopP {
		delete(payload, "top_p")
	}
}
