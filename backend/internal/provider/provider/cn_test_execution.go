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
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	claude "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/deepseek"
	"github.com/TokenFlux/TokenRouter/internal/upstream/kimi"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/zhipu"
)

// cn_test_execution.go constructs and executes fixed-protocol CN API-key
// probes with a mapped test model. CN API-key probes derive a platform default
// model when the supplied name is blank, then apply one provider mapping. The
// selected group route or record protocol selects Messages, Responses, or Chat request construction
// and matching stream parsing, with allowed header overrides applied after
// authentication.

// CNProviderTest prepares fixed and adaptive native-protocol probes for CN
// API-key records.
type CNProviderTest struct {
	// Attributes resolves the final model for routed group probes. A nil callback
	// skips profile lookup, leaving declared output and sampling permissions unknown.
	Attributes ModelAttributeLookup
	Transport  QoderTransport // provider-aware native probe sender
	// Profiles optionally resolves native request fingerprints. A missing service
	// sends no selected TLS profile through the shared transport.
	Profiles    *egressprovider.TLSProfiles
	ValidateURL func(string) (string, error) // native upstream root validator
	// Store optionally records authentication failures from native Messages
	// probes. A nil store skips these best-effort state writes.
	Store interface {
		// SetError records a native probe's provider authentication failure. The
		// executor ignores write errors while returning the upstream status and
		// available body as its test failure.
		SetError(context.Context, int64, string) error
	}
	// Responses supplies the shared OpenAI executor for prepared CN Responses
	// targets. Configure it before that route is selected.
	Responses *OpenAIProviderTest
}

// defaultCNProviderTestModel returns the platform's declared connection-test
// model for Kimi, Zhipu, or DeepSeek. Other platforms return an empty string,
// leaving Execute to report that no test model is available.
func defaultCNProviderTestModel(platform string) string {
	switch platform {
	case capability.PlatformKimi:
		return kimi.DefaultTestModel
	case capability.PlatformZhipu:
		return zhipu.DefaultTestModel
	case capability.PlatformDeepseek:
		return deepseek.DefaultTestModel
	default:
		return ""
	}
}

// Execute runs the record's fixed native-protocol probe and emits test events.
// It requires an API-key record and nonblank key, applies a platform default
// and one model mapping, and closes the response after protocol-specific
// parsing. A routed test uses its resolved protocol and checks that route
// against group attributes before preparing the upstream request.
func (s *CNProviderTest) Execute(
	c *TestRun,
	value *providercore.Record,
	modelID string,
	prompt string,
) error {
	if value == nil || value.Type != capability.ProviderTypeAPIKey {
		return (TestStreamOutput{}).Error(c, "CN provider tests require an API Key provider")
	}
	apiKey := strings.TrimSpace(value.GetCNAPIKey())
	if apiKey == "" {
		return (TestStreamOutput{}).Error(c, "No API key available")
	}
	testModelID := strings.TrimSpace(modelID)
	if testModelID == "" {
		testModelID = defaultCNProviderTestModel(value.Platform)
	}
	testModelID = mappedTestModel(value, testModelID)
	if testModelID == "" {
		return (TestStreamOutput{}).Error(c, "No test model available")
	}

	ctx := c.Context
	protocol := (providercore.ProtocolTarget{Record: value, Protocol: c.ProtocolRoute.UpstreamProtocol}).GetAPIProtocol()
	if c.ProtocolRoute.ClientProtocol != "" {
		if _, err := resolveProbeType(ctx, s.Attributes, value, c.GroupID, testModelID, string(c.ProtocolRoute.UpstreamProtocol), providercore.ProviderTestTypeText); err != nil {
			return (TestStreamOutput{}).Error(c, err.Error())
		}
	}
	var (
		apiURL  string
		payload any
	)
	switch protocol {
	case providercore.APIProtocolAnthropic:
		baseURL, err := s.ValidateURL((providercore.ProtocolTarget{Record: value}).GetAnthropicProtocolBaseURL())
		if err != nil {
			return (TestStreamOutput{}).Error(c, fmt.Sprintf("Invalid base URL: %s", err.Error()))
		}
		if hint := cnAnthropicBaseURLMisconfigHint(baseURL); hint != "" {
			return (TestStreamOutput{}).Error(c, hint)
		}
		apiURL = strings.TrimRight(baseURL, "/") + "/v1/messages"
		payload, err = claude.TestPayloadWithPrompt(testModelID, prompt)
		if err != nil {
			return (TestStreamOutput{}).Error(c, "Failed to create test payload")
		}
	case providercore.APIProtocolResponses:
		baseURL, err := s.ValidateURL((providercore.ProtocolTarget{Record: value, Protocol: c.ProtocolRoute.UpstreamProtocol}).GetOpenAIBaseURL())
		if err != nil {
			return (TestStreamOutput{}).Error(c, fmt.Sprintf("Invalid base URL: %s", err.Error()))
		}
		apiURL = cnTestResponsesURL(value.Platform, baseURL)
		responsesPayload := openai.TestResponsesPayload(testModelID, prompt, false)
		// CN Responses probes are stateless connection checks. Disable response
		// storage in the outgoing native payload before it reaches the platform
		// endpoint.
		responsesPayload["store"] = false
		payload = responsesPayload
	default:
		baseURL, err := s.ValidateURL((providercore.ProtocolTarget{Record: value, Protocol: c.ProtocolRoute.UpstreamProtocol}).GetOpenAIBaseURL())
		if err != nil {
			return (TestStreamOutput{}).Error(c, fmt.Sprintf("Invalid base URL: %s", err.Error()))
		}
		apiURL = httpclient.BuildOpenAIEndpointURL(baseURL, "/v1/chat/completions")
		payload = openai.TestChatCompletionsPayload(testModelID, prompt)
	}
	if object, ok := payload.(map[string]any); ok {
		if err := applyProbeSampling(c, s.Attributes, value, testModelID, string(c.ProtocolRoute.UpstreamProtocol), object); err != nil {
			return (TestStreamOutput{}).Error(c, err.Error())
		}
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create test payload")
	}

	c.Begin(true)
	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_start", Model: testModelID})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create request")
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if protocol == providercore.APIProtocolAnthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
		claude.SetAPIKeyAuthHeader(req.Header, value.GetAnthropicAPIKeyAuthScheme() == providercore.AnthropicAPIKeyAuthSchemeAuthorizationBearer, apiKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	applyGrokQuotaHeaders(value, req.Header)

	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}
	resp, err := s.Transport.DoWithTLS(
		req,
		proxyURL,
		value.ID,
		value.Concurrency,
		s.resolveTLSProfile(value),
	)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// Keep available non-OK response bytes for probe diagnostics even if reading
		// fails. The selected endpoint's status remains the reported failure.
		body, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body))
		if (protocol == providercore.APIProtocolAnthropic && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden)) && s.Store != nil {
			// Record native Messages authentication rejection when storage is
			// configured. A failed state write does not replace the upstream failure
			// already being returned by the probe.
			_ = s.Store.SetError(ctx, value.ID, errMsg)
		}
		return (TestStreamOutput{}).Error(c, errMsg)
	}
	switch protocol {
	case providercore.APIProtocolAnthropic:
		return (TestStreamOutput{}).Anthropic(c, resp.Body)
	case providercore.APIProtocolResponses:
		return (TestStreamOutput{}).Responses(c, resp.Body)
	default:
		return (TestStreamOutput{}).ChatCompletions(c, resp.Body)
	}
}

// executeChat delegates adaptive Chat coverage to the fixed Execute path. That
// path supplies model defaults, authentication, allowed header overrides,
// proxy, and TLS options; adaptive callers suppress its intermediate completion
// event.
func (s *CNProviderTest) executeChat(
	c *TestRun,
	value *providercore.Record,
	modelID string,
	prompt string,
) error {
	return s.Execute(c, value, modelID, prompt)
}

// resolveTLSProfile selects the record's enabled request fingerprint and direct
// profile through Profiles. A missing profile service returns nil; callers
// supply a nonnil record for configured selection.
func (s *CNProviderTest) resolveTLSProfile(value *providercore.Record) *tlsfingerprint.Profile {
	if s.Profiles == nil {
		return nil
	}
	return s.Profiles.ResolveRequestTLS(egress.TLSSelection{Enabled: value.IsTLSFingerprintEnabled(), DirectProfileID: value.GetTLSFingerprintProfileID()})
}

// cnTestResponsesURL returns the platform-specific Responses endpoint under the
// supplied root. DeepSeek uses the unversioned responses suffix, while other
// platforms use v1; endpoint construction retains supported version and path
// prefixes.
func cnTestResponsesURL(platform, base string) string {
	if platform == capability.PlatformDeepseek {
		return httpclient.BuildOpenAIEndpointURL(base, "/responses")
	}
	return httpclient.BuildOpenAIEndpointURL(base, "/v1/responses")
}
