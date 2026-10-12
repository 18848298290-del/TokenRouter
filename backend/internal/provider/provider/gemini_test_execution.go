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
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
	"github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
	geminicli "github.com/TokenFlux/TokenRouter/internal/upstream/gemini/codeassist"
	"github.com/TokenFlux/TokenRouter/internal/upstream/vertex"
)

// GeminiProviderTest probes Gemini using provider credentials and a shared transport.
type GeminiProviderTest struct {
	Tokens      *providercore.GeminiTokenSource
	Transport   QoderTransport
	Profiles    *egressprovider.TLSProfiles
	ValidateURL func(string) (string, error)
	UserAgent   string
}

// Execute builds a credential-specific Gemini probe and emits its events synchronously.
func (s *GeminiProviderTest) Execute(c *TestRun, value *providercore.Record, modelID string, prompt string, testTypes ...string) error {
	ctx := c.Context

	testModelID := modelID
	if testModelID == "" {
		testModelID = geminicli.DefaultTestModel
	}

	// API key and service-account probes apply exact model mappings.
	if value.Type == capability.ProviderTypeAPIKey || value.Type == capability.ProviderTypeServiceAccount {
		mapping := providercore.ResolveModelMapping(value, ModelDefaults())
		if len(mapping) > 0 {
			if mappedModel, exists := mapping[testModelID]; exists {
				testModelID = mappedModel
			}
		}
	}

	c.Begin(true)

	payload := geminiTestPayload(testModelID, prompt, testTypes...)

	var req *http.Request
	var err error

	switch value.Type {
	case capability.ProviderTypeAPIKey:
		req, err = s.buildGeminiAPIKeyRequest(ctx, value, testModelID, payload)
	case capability.ProviderTypeOAuth:
		req, err = s.buildGeminiOAuthRequest(ctx, value, testModelID, payload)
	case capability.ProviderTypeServiceAccount:
		req, err = s.buildGeminiServiceAccountRequest(ctx, value, testModelID, payload)
	default:
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Unsupported provider type: %s", value.Type))
	}

	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to build request: %s", err.Error()))
	}

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_start", Model: testModelID})

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
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body)))
	}

	return (TestStreamOutput{}).Gemini(c, resp.Body)
}

// buildGeminiAPIKeyRequest builds an authenticated streaming AI Studio request.
func (s *GeminiProviderTest) buildGeminiAPIKeyRequest(ctx context.Context, value *providercore.Record, modelID string, payload []byte) (*http.Request, error) {
	apiKey := value.GetCredential("api_key")
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("no API key available")
	}

	baseURL := value.GetCredential("base_url")
	if baseURL == "" {
		baseURL = geminicli.AIStudioBaseURL
	}
	normalizedBaseURL, err := s.ValidateURL(baseURL)
	if err != nil {
		return nil, err
	}

	fullURL, err := gemini.BuildGeminiAIStudioModelActionURL(normalizedBaseURL, modelID, "streamGenerateContent", true)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", apiKey)
	s.applyUserAgent(req)

	return req, nil
}

// buildGeminiOAuthRequest selects Code Assist when project_id is present and AI Studio otherwise.
func (s *GeminiProviderTest) buildGeminiOAuthRequest(ctx context.Context, value *providercore.Record, modelID string, payload []byte) (*http.Request, error) {
	if s.Tokens == nil {
		return nil, fmt.Errorf("gemini token provider not configured")
	}

	accessToken, err := s.Tokens.GetAccessToken(ctx, value)
	if err != nil {
		return nil, fmt.Errorf("failed to get access token: %w", err)
	}

	projectID := strings.TrimSpace(value.GetCredential("project_id"))
	if projectID == "" {
		baseURL := value.GetCredential("base_url")
		if strings.TrimSpace(baseURL) == "" {
			baseURL = geminicli.AIStudioBaseURL
		}
		normalizedBaseURL, err := s.ValidateURL(baseURL)
		if err != nil {
			return nil, err
		}
		fullURL, err := gemini.BuildGeminiAIStudioModelActionURL(normalizedBaseURL, modelID, "streamGenerateContent", true)
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessToken)
		s.applyUserAgent(req)
		return req, nil
	}

	return s.buildCodeAssistRequest(ctx, accessToken, projectID, modelID, payload)
}

func (s *GeminiProviderTest) buildGeminiServiceAccountRequest(ctx context.Context, value *providercore.Record, modelID string, payload []byte) (*http.Request, error) {
	if s.Tokens == nil {
		return nil, fmt.Errorf("gemini token provider not configured")
	}
	accessToken, err := s.Tokens.GetAccessToken(ctx, value)
	if err != nil {
		return nil, fmt.Errorf("failed to get service account access token: %w", err)
	}
	fullURL, err := vertex.BuildVertexGeminiURL(value.VertexProjectID(vertex.ServiceAccountProjectID), value.VertexLocation(modelID), modelID, "streamGenerateContent", true)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	s.applyUserAgent(req)
	return req, nil
}

// buildCodeAssistRequest wraps the payload with the model and project and adds Code Assist headers.
func (s *GeminiProviderTest) buildCodeAssistRequest(ctx context.Context, accessToken, projectID, modelID string, payload []byte) (*http.Request, error) {
	var inner map[string]any
	if err := json.Unmarshal(payload, &inner); err != nil {
		return nil, err
	}

	wrapped := map[string]any{
		"model":   modelID,
		"project": projectID,
		"request": inner,
	}
	wrappedBytes, _ := json.Marshal(wrapped)

	normalizedBaseURL, err := s.ValidateURL(geminicli.GeminiCliBaseURL)
	if err != nil {
		return nil, err
	}
	fullURL := fmt.Sprintf("%s/v1internal:streamGenerateContent?alt=sse", normalizedBaseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(wrappedBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", geminicli.GeminiCLIUserAgent)
	s.applyUserAgent(req)

	return req, nil
}

// geminiTestPayload builds a text or image probe.
// An explicit test type takes precedence over model-name detection.
func geminiTestPayload(modelID string, prompt string, testTypes ...string) []byte {
	testType, explicitTestType := providercore.ProviderTestTypeFromArgs(testTypes...)
	useImageTest := (explicitTestType && testType == providercore.ProviderTestTypeImage) ||
		(!explicitTestType && antigravity.IsImageGenerationModel(modelID))
	if useImageTest {
		imagePrompt := strings.TrimSpace(prompt)
		if imagePrompt == "" {
			imagePrompt = "Generate a cute orange cat astronaut sticker on a clean pastel background."
		}

		payload := map[string]any{
			"contents": []map[string]any{
				{
					"role": "user",
					"parts": []map[string]any{
						{"text": imagePrompt},
					},
				},
			},
			"generationConfig": map[string]any{
				"responseModalities": []string{"TEXT", "IMAGE"},
				"imageConfig": map[string]any{
					"aspectRatio": "1:1",
				},
			},
		}
		bytes, _ := json.Marshal(payload)
		return bytes
	}

	textPrompt := strings.TrimSpace(prompt)
	if textPrompt == "" {
		textPrompt = "hi"
	}

	payload := map[string]any{
		"contents": []map[string]any{
			{
				"role": "user",
				"parts": []map[string]any{
					{"text": textPrompt},
				},
			},
		},
		"systemInstruction": map[string]any{
			"parts": []map[string]any{
				{"text": "You are a helpful AI assistant."},
			},
		},
	}
	bytes, _ := json.Marshal(payload)
	return bytes
}

func (s *GeminiProviderTest) applyUserAgent(req *http.Request) {
	if userAgent := strings.TrimSpace(s.UserAgent); userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
}

func (s *GeminiProviderTest) resolveTLSProfile(value *providercore.Record) *tlsfingerprint.Profile {
	if s.Profiles == nil {
		return nil
	}
	return s.Profiles.ResolveRequestTLS(egress.TLSSelection{Enabled: value.IsTLSFingerprintEnabled(), DirectProfileID: value.GetTLSFingerprintProfileID()})
}
