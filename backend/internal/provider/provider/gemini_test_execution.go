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
	"github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
	geminicli "github.com/TokenFlux/TokenRouter/internal/upstream/gemini/codeassist"
	"github.com/TokenFlux/TokenRouter/internal/upstream/vertex"
)

// gemini_test_execution.go builds and executes streaming Gemini probes across
// API-key, OAuth, and service-account routes. Gemini provider tests share a
// streaming event reader but prepare different credential routes. API keys use
// the AI Studio model action, OAuth uses Code Assist when a project is stored
// and AI Studio otherwise, and service accounts use a Vertex project/location
// URL with their access token.
//
// Automatic test types come from declared output modalities, while explicit
// image probes require positive capability and explicit text retains its route. API-key and
// service-account mappings choose the model before payload construction. Routed
// group probes use the shared one-hop mapping for all credential types. Code
// Assist wraps the same payload with its project and model identifiers, while
// the other routes send the payload directly to their model-action endpoint.

// GeminiProviderTest probes Gemini using provider credentials and a shared
// transport.
type GeminiProviderTest struct {
	// Attributes supplies capabilities for the mapped probe target. A missing
	// callback leaves automatic and image probes without required output evidence.
	Attributes ModelAttributeLookup
	// Tokens is required for OAuth and service-account probes. API-key probes
	// read their stored key directly and do not consult this source.
	Tokens *providercore.GeminiTokenSource
	// Transport sends the prepared streaming request with provider proxy and TLS
	// selection. Configure it before executing any successfully prepared
	// credential route.
	Transport QoderTransport
	// Profiles optionally resolves provider fingerprint settings. A nil value
	// passes no profile to Transport, leaving transport defaults to that
	// dependency.
	Profiles *egressprovider.TLSProfiles
	// ValidateURL normalizes and validates API-key and OAuth base URLs before
	// request construction. Service-account requests build their dedicated
	// Vertex URL directly.
	ValidateURL func(string) (string, error)
	// UserAgent overrides route defaults when nonblank after trimming. Automatic
	// target executions set it on a local executor copy for that probe.
	UserAgent string
}

// Execute prepares and streams a Gemini text or image probe for one provider.
// A blank model uses the platform test default. API-key and service-account
// models apply exact saved mappings; standalone OAuth keeps the supplied name.
// Routed group probes apply the shared provider mapping for every credential type. Transport
// responses are closed on return, and non-200 bodies become test errors before
// the shared Gemini event reader is invoked.
func (s *GeminiProviderTest) Execute(c *TestRun, value *providercore.Record, modelID string, prompt string, testTypes ...string) error {
	ctx := c.Context

	testModelID := modelID
	if testModelID == "" {
		testModelID = geminicli.DefaultTestModel
	}

	// Routed probes apply the same one-hop provider rules used during selection,
	// including wildcard aliases and OAuth mappings. Standalone tests retain their
	// credential-specific model preparation below.
	if c.ProtocolRoute.ClientProtocol != "" {
		testModelID = mappedTestModel(value, testModelID)
	} else if value.Type == capability.ProviderTypeAPIKey || value.Type == capability.ProviderTypeServiceAccount {
		mapping := providercore.ResolveModelMapping(value, ModelDefaults())
		if len(mapping) > 0 {
			if mappedModel, exists := mapping[testModelID]; exists {
				testModelID = mappedModel
			}
		}
	}

	c.Begin(true)

	testType, capabilityErr := resolveProbeType(ctx, s.Attributes, value, c.GroupID, testModelID, "gemini_generate_content", testTypes...)
	if capabilityErr != nil {
		return (TestStreamOutput{}).Error(c, capabilityErr.Error())
	}
	payload := geminiTestPayload(testModelID, prompt, testType)

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
	// The shared event reader consumes the successful body; retain closing here so
	// non-200 responses and reader failures release the same transport
	// response as success.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Return any diagnostic bytes read before failure; the probe still
		// reports the
		// HTTP status instead of replacing it with a secondary response-read error.
		body, _ := io.ReadAll(resp.Body)
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body)))
	}

	return (TestStreamOutput{}).Gemini(c, resp.Body)
}

// buildGeminiAPIKeyRequest prepares a streaming model request with the stored
// API key.
// Blank keys return an error. Missing base_url uses AI Studio, the selected base
// passes ValidateURL, and a nonblank executor UserAgent overrides request defaults.
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

// buildGeminiOAuthRequest prepares OAuth streaming through Code Assist or AI
// Studio.
// Tokens is required and its acquisition errors are returned. A nonblank stored
// project selects the Code Assist envelope; otherwise the configured or default
// AI Studio base is validated and receives the unwrapped payload.
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

// buildGeminiServiceAccountRequest prepares an authenticated streaming Vertex
// model action.
// Tokens is required; acquisition and URL-building errors are returned. The
// record supplies project and model-dependent location selection, and the returned
// request carries the token as Bearer authorization.
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

// buildCodeAssistRequest wraps valid JSON payload fields with project and
// model identity.
// The payload must decode as an object or null before construction. The fixed
// Code Assist base passes ValidateURL, and an executor UserAgent can override
// the platform CLI header on the returned streaming request.
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
	// Decoding supplied only JSON-compatible values, and the wrapper adds strings.
	// Re-encoding that shape cannot introduce unsupported values or cycles
	// from caller objects.
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

// geminiTestPayload builds JSON for a selected text or image probe.
// The caller supplies the resolved test type. Blank prompts use
// route-specific defaults; image payloads request text and image modalities with
// a square aspect ratio, while text payloads include the helper system instruction.
func geminiTestPayload(modelID string, prompt string, testTypes ...string) []byte {
	// Both payload variants contain fixed JSON-compatible containers and
	// scalar values.
	// Their ignored marshal errors are limited to these constructed test shapes.
	testType, _ := providercore.ProviderTestTypeFromArgs(testTypes...)
	useImageTest := testType == providercore.ProviderTestTypeImage
	// The executor has already resolved and validated image capability. Payload
	// construction follows that supplied operation independently of model spelling.
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

// applyUserAgent sets a trimmed executor override when one is supplied.
// A blank override retains the route's existing header. Callers pass a nonnil
// request after constructing its route defaults and authentication headers.
func (s *GeminiProviderTest) applyUserAgent(req *http.Request) {
	if userAgent := strings.TrimSpace(s.UserAgent); userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
}

// resolveTLSProfile resolves the provider's fingerprint selection through Profiles.
// A nil Profiles service returns nil, allowing the transport's own default
// behavior. With a service configured, the caller supplies a nonnil provider
// record.
func (s *GeminiProviderTest) resolveTLSProfile(value *providercore.Record) *tlsfingerprint.Profile {
	if s.Profiles == nil {
		return nil
	}
	return s.Profiles.ResolveRequestTLS(egress.TLSSelection{Enabled: value.IsTLSFingerprintEnabled(), DirectProfileID: value.GetTLSFingerprintProfileID()})
}
