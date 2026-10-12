package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// OpenAIProviderTest binds storage, transport, and request policy; TestRun holds per-test state.
type OpenAIProviderTest struct {
	Store        OpenAIProviderTestStore
	Transport    QoderTransport
	ValidateURL  func(string) (string, error)
	ApplyRouting func(*TestRun, *providercore.Record, *http.Request, bool)
	ResolveTLS   func(*TestRun, *providercore.Record) *tlsfingerprint.Profile
	EnsureTask   func(context.Context, *providercore.Record, string) error

	Prepare func(*TestRun, *providercore.Record) error
}

// OpenAIProviderTestStore supplies provider reads and health and usage writes for probes.
type OpenAIProviderTestStore interface {
	GetByID(context.Context, int64) (*providercore.Record, error)
	UpdateExtra(context.Context, int64, map[string]any) error
	SetError(context.Context, int64, string) error
	ClearError(context.Context, int64) error
	SetRateLimited(context.Context, int64, time.Time) error
	providercore.OpenAIPlanWriter
}

// Execute runs an OpenAI probe using the provider credentials, mode, and test type.
func (s *OpenAIProviderTest) Execute(c *TestRun, value *providercore.Record, modelID string, prompt string, mode string, testTypes ...string) error {
	ctx := c.Context
	mode, testType, explicitTestType := providercore.ResolveProviderTestModeAndType(mode, testTypes...)

	testModelID := modelID
	if testModelID == "" {
		testModelID = openai.DefaultTestModel
	}

	// Native V2 uses ordinary Responses model mapping. Legacy compact probes additionally
	// apply compact_model_mapping.
	testModelID = mappedTestModel(value, testModelID)
	if mode == providercore.ProviderTestModeCompact {
		return s.executeNativeCompaction(c, value, testModelID)
	}
	if mode == providercore.ProviderTestModeLegacyCompact {
		testModelID = providercore.ResolveCompactForwardModel(value, testModelID)
		return s.executeLegacyCompact(c, value, testModelID)
	}

	// An explicit test type takes precedence over image detection by model name.
	if (explicitTestType && testType == providercore.ProviderTestTypeImage) ||
		(!explicitTestType && strings.HasPrefix(strings.ToLower(testModelID), "gpt-image-")) {
		imagePrompt := strings.TrimSpace(prompt)
		if imagePrompt == "" {
			imagePrompt = "Generate a cute orange cat astronaut sticker on a clean pastel background."
		}
		if value.Type == "apikey" {
			return s.ExecuteImageAPIKey(c, ctx, value, testModelID, imagePrompt)
		}
		return s.ExecuteImageOAuth(c, ctx, value, testModelID, imagePrompt)
	}

	credentialProvider := value
	if value.IsCredentialShadow() {
		resolved, err := providercore.ResolveCredentialRecord(ctx, s.read, value)
		if err != nil {
			return (TestStreamOutput{}).Error(c, err.Error())
		}
		credentialProvider = resolved
	}

	var authToken string
	var apiURL string
	var isOAuth bool

	if credentialProvider.IsOAuth() {
		isOAuth = true
		// Agent Identity signs each request instead of storing an OAuth token.
		if !credentialProvider.IsOpenAIAgentIdentity() {
			authToken = credentialProvider.GetOpenAIAccessToken()
		}
		if authToken == "" && !credentialProvider.IsOpenAIAgentIdentity() {
			return (TestStreamOutput{}).Error(c, "No access token available")
		}

		apiURL = "https://chatgpt.com/backend-api/codex/responses"
	} else if credentialProvider.Type == "apikey" {
		// The protocol-family key accessor also supports Chinese OpenAI-compatible providers.
		authToken = credentialProvider.GetOpenAIProtocolAPIKey()
		if authToken == "" {
			return (TestStreamOutput{}).Error(c, "No API key available")
		}

		baseURL := credentialProvider.OpenAIBaseURL((providercore.ProtocolTarget{Record: credentialProvider}).IsAdaptiveAPIProtocol())
		if baseURL == "" {
			baseURL = "https://api.openai.com"
		}
		normalizedBaseURL, err := s.ValidateURL(baseURL)
		if err != nil {
			return (TestStreamOutput{}).Error(c, fmt.Sprintf("Invalid base URL: %s", err.Error()))
		}
		protocol := providercore.ResolveUpstreamTextProtocol(value.Extra, providercore.TextProtocolResponses)
		if requested := c.RequestedProtocol; requested != "" && value.IsOpenAI() {
			protocol = requested
		}
		if protocol == providercore.TextProtocolChatCompletions {
			return s.executeChat(c, value, testModelID, prompt, normalizedBaseURL, authToken)
		}
		apiURL = httpclient.BuildOpenAIEndpointURL(normalizedBaseURL, "/v1/responses")
	} else {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Unsupported provider type: %s", value.Type))
	}

	c.Begin(true)

	// OAuth probes trim model names for the ChatGPT Codex endpoint.
	upstreamTestModelID := testModelID
	if isOAuth {
		upstreamTestModelID = strings.TrimSpace(testModelID)
	}
	payload := openai.TestResponsesPayload(upstreamTestModelID, prompt, isOAuth)
	payloadBytes, _ := json.Marshal(payload)

	// Task recovery retries the probe, so emit the start event only on the first attempt.
	if !c.TaskRecoveryTried {
		(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_start", Model: testModelID})
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create request")
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))

	req.Header.Set("Content-Type", "application/json")
	if credentialProvider.IsOpenAIAgentIdentity() {
		authHeaders, authErr := s.agentHeaders(ctx, credentialProvider)
		if authErr != nil {
			return (TestStreamOutput{}).Error(c, "Failed to build Agent Identity authentication")
		}
		for key, values := range authHeaders {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	if isOAuth {
		req.Host = "chatgpt.com"
		req.Header.Set("accept", "text/event-stream")
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("Originator", openai.ResolveCodexOutboundIdentity("").Originator)
		if customUA := strings.TrimSpace(credentialProvider.GetOpenAIUserAgent()); customUA != "" {
			req.Header.Set("User-Agent", customUA)
		} else {
			req.Header.Set("User-Agent", openai.CodexCanonicalUserAgent())
		}
		setTestChatGPTHeaders(req.Header, credentialProvider)
	}
	s.ApplyRouting(c, value, req, isOAuth)
	if value.Type == capability.ProviderTypeOAuth {
		// Pair identity after the probe UA override to prevent mismatched headers from causing a 404.
		openai.EnforceCodexIdentityHeaders(req.Header)
	}

	// Apply provider header overrides after routing, as forwarding does.
	applyGrokQuotaHeaders(credentialProvider, req.Header)

	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}

	resp, err := s.Transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, s.ResolveTLS(c, value))
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	if isOAuth && s.Store != nil {
		if updates, err := ExtractOpenAIUsageUpdates(resp, time.Now()); err == nil && len(updates) > 0 {
			_ = s.Store.UpdateExtra(ctx, value.ID, updates)
			value.Extra = providercore.MergeUsageExtra(value.Extra, updates)
		}
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		body = RedactAgentIdentityBody(ctx, s.read, credentialProvider, body)
		if !c.TaskRecoveryTried && credentialProvider.IsOpenAIAgentIdentity() && openai.IsAgentTaskInvalidHTTPResponse(resp.StatusCode, body) {
			expectedTaskID := credentialProvider.GetCredential("task_id")
			if err := s.EnsureTask(ctx, credentialProvider, expectedTaskID); err != nil {
				return (TestStreamOutput{}).Error(c, fmt.Sprintf("Agent Identity task recovery failed: %s", err.Error()))
			}
			c.TaskRecoveryTried = true
			return s.Execute(c, value, modelID, prompt, mode, testType)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			s.reconcileOpenAI429State(ctx, value, resp.Header, body)
		}
		// Persist a 401 as a provider authentication error.
		if resp.StatusCode == http.StatusUnauthorized && s.Store != nil {
			errMsg := fmt.Sprintf("Authentication failed (401): %s", string(body))
			_ = s.Store.SetError(ctx, value.ID, errMsg)
		}
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body)))
	}

	return (TestStreamOutput{}).Responses(c, resp.Body)
}

// executeChat probes OpenAI-compatible API key providers through /v1/chat/completions.
func (s *OpenAIProviderTest) executeChat(
	c *TestRun,
	value *providercore.Record,
	testModelID string,
	prompt string,
	normalizedBaseURL string,
	authToken string,
) error {
	ctx := c.Context
	apiURL := httpclient.BuildOpenAIEndpointURL(normalizedBaseURL, "/v1/chat/completions")

	c.Begin(true)

	payload := openai.TestChatCompletionsPayload(testModelID, prompt)
	payloadBytes, _ := json.Marshal(payload)

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_start", Model: testModelID})
	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "status", Text: "正在通过 /v1/chat/completions 测试连接"})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create Chat Completions request")
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+authToken)
	s.ApplyRouting(c, value, req, false)

	// Apply provider header overrides after routing, as forwarding does.
	applyGrokQuotaHeaders(value, req.Header)

	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}

	resp, err := s.Transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, s.ResolveTLS(c, value))
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Chat Completions API (/v1/chat/completions) request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			s.reconcileOpenAI429State(ctx, value, resp.Header, body)
		}
		if resp.StatusCode == http.StatusUnauthorized && s.Store != nil {
			errMsg := fmt.Sprintf("Chat Completions authentication failed (401): %s", string(body))
			_ = s.Store.SetError(ctx, value.ID, errMsg)
		}
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Chat Completions API (/v1/chat/completions) returned %d: %s", resp.StatusCode, string(body)))
	}

	return (TestStreamOutput{}).ChatCompletions(c, resp.Body)
}

// executeNativeCompaction probes streaming /responses with compaction_trigger.
// It uses ordinary model mapping and leaves the administrator's capability setting unchanged.
func (s *OpenAIProviderTest) executeNativeCompaction(c *TestRun, value *providercore.Record, testModelID string) error {
	ctx := c.Context
	credentialProvider := value
	if value.IsShadow() {
		resolved, err := providercore.ResolveCredentialRecord(ctx, s.read, value)
		if err != nil {
			return (TestStreamOutput{}).Error(c, "Failed to resolve provider credentials")
		}
		credentialProvider = resolved
	}

	authToken := ""
	apiURL := ""
	isOAuth := false
	switch {
	case credentialProvider.IsOAuth():
		isOAuth = true
		if !credentialProvider.IsOpenAIAgentIdentity() {
			authToken = credentialProvider.GetOpenAIAccessToken()
		}
		if authToken == "" && !credentialProvider.IsOpenAIAgentIdentity() {
			return (TestStreamOutput{}).Error(c, "No access token available")
		}
		apiURL = "https://chatgpt.com/backend-api/codex/responses"
	case value.Type == capability.ProviderTypeAPIKey:
		authToken = value.GetOpenAIApiKey()
		if authToken == "" {
			return (TestStreamOutput{}).Error(c, "No API key available")
		}
		baseURL := value.OpenAIBaseURL((providercore.ProtocolTarget{Record: value}).IsAdaptiveAPIProtocol())
		if baseURL == "" {
			baseURL = "https://api.openai.com"
		}
		normalizedBaseURL, err := s.ValidateURL(baseURL)
		if err != nil {
			return (TestStreamOutput{}).Error(c, fmt.Sprintf("Invalid base URL: %s", err.Error()))
		}
		apiURL = httpclient.BuildOpenAIEndpointURL(normalizedBaseURL, "/v1/responses")
	default:
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Unsupported provider type: %s", value.Type))
	}

	c.Begin(true)

	if isOAuth {
		testModelID = strings.TrimSpace(testModelID)
	}
	payloadBytes, _ := json.Marshal(openai.CompactionTestPayload(testModelID, isOAuth))
	if !c.TaskRecoveryTried {
		(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_start", Model: testModelID})
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create request")
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	// Reapply this negotiation header after provider overrides below.
	openai.EnsureRemoteCompactionV2Header(req.Header)
	if credentialProvider.IsOpenAIAgentIdentity() {
		authHeaders, authErr := s.agentHeaders(ctx, credentialProvider)
		if authErr != nil {
			return (TestStreamOutput{}).Error(c, "Failed to build Agent Identity authentication")
		}
		for key, values := range authHeaders {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	canonical := openai.ResolveCodexOutboundIdentity("")
	req.Header.Set("Originator", canonical.Originator)
	req.Header.Set("User-Agent", canonical.UserAgent)
	req.Header.Set("Version", canonical.Version)
	testSessionID := openai.CompactionTestSessionID(value.ID)
	req.Header.Set("Session_ID", testSessionID)
	req.Header.Set("Conversation_ID", testSessionID)
	s.ApplyRouting(c, value, req, isOAuth)

	if isOAuth {
		req.Host = "chatgpt.com"
		setTestChatGPTHeaders(req.Header, credentialProvider)
		if fingerprintIDs := testCodexFingerprint(value, req.Header); fingerprintIDs != nil {
			openai.ApplyCodexFingerprintHeaders(req.Header, fingerprintIDs)
		}
		openai.EnforceCodexIdentityHeaders(req.Header)
	}

	// Restore V2 negotiation after provider header overrides, as forwarding does.
	applyGrokQuotaHeaders(value, req.Header)
	openai.EnsureRemoteCompactionV2Header(req.Header)

	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}

	resp, err := s.Transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, s.ResolveTLS(c, value))
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	body = RedactAgentIdentityBody(ctx, s.read, credentialProvider, body)
	if !c.TaskRecoveryTried && credentialProvider.IsOpenAIAgentIdentity() && openai.IsAgentTaskInvalidHTTPResponse(resp.StatusCode, body) {
		expectedTaskID := credentialProvider.GetCredential("task_id")
		if err := s.EnsureTask(ctx, credentialProvider, expectedTaskID); err != nil {
			return (TestStreamOutput{}).Error(c, fmt.Sprintf("Agent Identity task recovery failed: %s", err.Error()))
		}
		c.TaskRecoveryTried = true
		return s.executeNativeCompaction(c, value, testModelID)
	}

	compactionFound := openai.CompactionTestHasOutput(body)
	if s.Store != nil {
		// Save quota observations while leaving the administrator's capability setting unchanged.
		var updates map[string]any
		if codexUpdates, err := ExtractOpenAIUsageUpdates(resp, time.Now()); err == nil && len(codexUpdates) > 0 {
			updates = mergeTestExtraUpdates(updates, codexUpdates)
		}
		if len(updates) > 0 {
			_ = s.Store.UpdateExtra(ctx, value.ID, updates)
			value.Extra = providercore.MergeUsageExtra(value.Extra, updates)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			s.reconcileOpenAI429State(ctx, value, resp.Header, body)
		}
	}

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized && s.Store != nil {
			errMsg := fmt.Sprintf("Authentication failed (401): %s", string(body))
			_ = s.Store.SetError(ctx, value.ID, errMsg)
		}
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body)))
	}
	if !compactionFound {
		return (TestStreamOutput{}).Error(c, "Upstream returned 2xx without a compaction output item (native remote compaction v2 unsupported on this chain)")
	}

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "content", Text: "Native remote compaction v2 test succeeded"})
	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_complete", Success: true})
	return nil
}

// executeLegacyCompact probes /responses/compact without writing capability settings.
// Authentication and rate-limit failures still update provider health.
func (s *OpenAIProviderTest) executeLegacyCompact(c *TestRun, value *providercore.Record, testModelID string) error {
	ctx := c.Context
	credentialProvider := value
	if value.IsShadow() {
		resolved, err := providercore.ResolveCredentialRecord(ctx, s.read, value)
		if err != nil {
			return (TestStreamOutput{}).Error(c, "Failed to resolve provider credentials")
		}
		credentialProvider = resolved
	}

	authToken := ""
	apiURL := ""
	isOAuth := false

	switch {
	case credentialProvider.IsOAuth():
		isOAuth = true
		if !credentialProvider.IsOpenAIAgentIdentity() {
			authToken = credentialProvider.GetOpenAIAccessToken()
		}
		if authToken == "" && !credentialProvider.IsOpenAIAgentIdentity() {
			return (TestStreamOutput{}).Error(c, "No access token available")
		}
		apiURL = "https://chatgpt.com/backend-api/codex/responses" + "/compact"
	case value.Type == capability.ProviderTypeAPIKey:
		authToken = value.GetOpenAIApiKey()
		if authToken == "" {
			return (TestStreamOutput{}).Error(c, "No API key available")
		}
		baseURL := value.OpenAIBaseURL((providercore.ProtocolTarget{Record: value}).IsAdaptiveAPIProtocol())
		if baseURL == "" {
			baseURL = "https://api.openai.com"
		}
		normalizedBaseURL, err := s.ValidateURL(baseURL)
		if err != nil {
			return (TestStreamOutput{}).Error(c, fmt.Sprintf("Invalid base URL: %s", err.Error()))
		}
		apiURL = openai.AppendResponsesPathSuffix(httpclient.BuildOpenAIEndpointURL(normalizedBaseURL, "/v1/responses"), "/compact")
	default:
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Unsupported provider type: %s", value.Type))
	}

	c.Begin(true)

	payloadBytes, _ := json.Marshal(openai.LegacyCompactionTestPayload(testModelID))
	if !c.TaskRecoveryTried {
		(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_start", Model: testModelID})
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create request")
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if credentialProvider.IsOpenAIAgentIdentity() {
		authHeaders, authErr := s.agentHeaders(ctx, credentialProvider)
		if authErr != nil {
			return (TestStreamOutput{}).Error(c, "Failed to build Agent Identity authentication")
		}
		for key, values := range authHeaders {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	canonical := openai.ResolveCodexOutboundIdentity("")
	req.Header.Set("Originator", canonical.Originator)
	req.Header.Set("User-Agent", canonical.UserAgent)
	req.Header.Set("Version", canonical.Version)
	testSessionID := openai.LegacyCompactionTestSessionID(value.ID)
	req.Header.Set("Session_ID", testSessionID)
	req.Header.Set("Conversation_ID", testSessionID)
	s.ApplyRouting(c, value, req, isOAuth)

	if isOAuth {
		req.Host = "chatgpt.com"
		setTestChatGPTHeaders(req.Header, credentialProvider)
		// Re-pair Codex identity headers after applying the probe UA override.
		openai.EnforceCodexIdentityHeaders(req.Header)
	}

	// Apply provider header overrides after routing, as forwarding does.
	applyGrokQuotaHeaders(value, req.Header)

	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}

	resp, err := s.Transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, s.ResolveTLS(c, value))
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	body = RedactAgentIdentityBody(ctx, s.read, credentialProvider, body)
	if !c.TaskRecoveryTried && credentialProvider.IsOpenAIAgentIdentity() && openai.IsAgentTaskInvalidHTTPResponse(resp.StatusCode, body) {
		expectedTaskID := credentialProvider.GetCredential("task_id")
		if err := s.EnsureTask(ctx, credentialProvider, expectedTaskID); err != nil {
			return (TestStreamOutput{}).Error(c, fmt.Sprintf("Agent Identity task recovery failed: %s", err.Error()))
		}
		c.TaskRecoveryTried = true
		return s.executeLegacyCompact(c, value, testModelID)
	}

	if s.Store != nil {
		// Save quota observations while leaving the administrator's capability setting unchanged.
		var updates map[string]any
		if codexUpdates, err := ExtractOpenAIUsageUpdates(resp, time.Now()); err == nil && len(codexUpdates) > 0 {
			updates = mergeTestExtraUpdates(updates, codexUpdates)
		}
		if len(updates) > 0 {
			_ = s.Store.UpdateExtra(ctx, value.ID, updates)
			value.Extra = providercore.MergeUsageExtra(value.Extra, updates)
		}
		// Persist the observed 429 reset so scheduling can pause this provider.
		if resp.StatusCode == http.StatusTooManyRequests {
			s.reconcileOpenAI429State(ctx, value, resp.Header, body)
		}
	}

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized && s.Store != nil {
			errMsg := fmt.Sprintf("Authentication failed (401): %s", string(body))
			_ = s.Store.SetError(ctx, value.ID, errMsg)
		}
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body)))
	}

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "content", Text: "Compact test succeeded"})
	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_complete", Success: true})
	return nil
}

func (s *OpenAIProviderTest) reconcileOpenAI429State(ctx context.Context, value *providercore.Record, headers http.Header, body []byte) {
	if s == nil || s.Store == nil || value == nil {
		return
	}

	providercore.PersistOpenAIObservedPlan(ctx, s.Store, value, openai.ParseUsageLimitPlanType(body), slog.Info, slog.Warn)

	var resetAt *time.Time
	if calculated := providercore.OpenAI429ResetTime(openai.ParseCodexRateLimitHeaders(headers), time.Now, slog.Info); calculated != nil {
		resetAt = calculated
	} else if unixTs := openai.ParseUsageLimitResetTime(body, time.Now); unixTs != nil {
		t := time.Unix(*unixTs, 0)
		resetAt = &t
	}
	if resetAt == nil {
		return
	}

	if err := s.Store.SetRateLimited(ctx, value.ID, *resetAt); err != nil {
		return
	}

	now := time.Now()
	value.RateLimitedAt = &now
	value.RateLimitResetAt = resetAt

	if value.Status == providercore.StatusError {
		if err := s.Store.ClearError(ctx, value.ID); err != nil {
			return
		}
		value.Status = providercore.StatusActive
		value.ErrorMessage = ""
	}
}

// ExecuteImageAPIKey calls the image endpoint with an API key and emits preview events.
func (s *OpenAIProviderTest) ExecuteImageAPIKey(c *TestRun, ctx context.Context, value *providercore.Record, modelID, prompt string) error {
	authToken := value.GetOpenAIApiKey()
	if authToken == "" {
		return (TestStreamOutput{}).Error(c, "No API key available")
	}

	baseURL := value.OpenAIBaseURL((providercore.ProtocolTarget{Record: value}).IsAdaptiveAPIProtocol())
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	normalizedBaseURL, err := s.ValidateURL(baseURL)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Invalid base URL: %s", err.Error()))
	}
	apiURL := httpclient.BuildOpenAIEndpointURL(normalizedBaseURL, upstream.OpenAIImagesGenerationsEndpoint)

	c.Begin(true)

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_start", Model: modelID})

	payload := map[string]any{
		"model":           modelID,
		"prompt":          prompt,
		"n":               1,
		"response_format": "b64_json",
	}
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create request")
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)
	s.ApplyRouting(c, value, req, false)

	// Apply provider header overrides after routing, as forwarding does.
	applyGrokQuotaHeaders(value, req.Header)

	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}

	resp, err := s.Transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, s.ResolveTLS(c, value))
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to read response: %s", err.Error()))
	}

	if resp.StatusCode != http.StatusOK {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body)))
	}

	// Accept b64_json and URL results because relays may ignore response_format and return links.
	var result struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			URL           string `json:"url"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to parse response: %s", err.Error()))
	}

	if len(result.Data) == 0 {
		return (TestStreamOutput{}).Error(c, "No images returned from API")
	}

	images := 0
	for _, item := range result.Data {
		if item.RevisedPrompt != "" {
			(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "content", Text: item.RevisedPrompt})
		}
		switch {
		case item.B64JSON != "":
			images++
			(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{
				Type:     "image",
				ImageURL: "data:image/png;base64," + item.B64JSON,
				MimeType: "image/png",
			})
		case strings.HasPrefix(item.URL, "https://") || strings.HasPrefix(item.URL, "data:image/"):
			images++
			(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "image", ImageURL: item.URL})
		}
	}
	// A nonempty data array can still contain no displayable images.
	if images == 0 {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Upstream returned no image data: %s", logredact.TruncateLine(body, 512)))
	}

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_complete", Success: true})
	return nil
}

// ExecuteImageOAuth probes the Codex Responses image tool with OAuth credentials.
func (s *OpenAIProviderTest) ExecuteImageOAuth(c *TestRun, ctx context.Context, value *providercore.Record, modelID, prompt string) error {
	credentialProvider := value
	if value.IsShadow() {
		resolved, err := providercore.ResolveCredentialRecord(ctx, s.read, value)
		if err != nil {
			return (TestStreamOutput{}).Error(c, "Failed to resolve provider credentials")
		}
		credentialProvider = resolved
	}
	authToken := ""
	if !credentialProvider.IsOpenAIAgentIdentity() {
		authToken = credentialProvider.GetOpenAIAccessToken()
	}
	if authToken == "" && !credentialProvider.IsOpenAIAgentIdentity() {
		return (TestStreamOutput{}).Error(c, "No access token available")
	}

	c.Begin(true)

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_start", Model: modelID})
	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "content", Text: "Calling Codex /responses image tool...\n"})

	parsed := &upstream.ImageRequest{
		Endpoint: upstream.OpenAIImagesGenerationsEndpoint,
		Model:    strings.TrimSpace(modelID),
		Prompt:   prompt,
	}
	upstream.ApplyOpenAIImagesDefaults(parsed)

	responsesBody, err := openai.BuildOpenAIImagesResponsesRequest(parsed, parsed.Model)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to build image request: %s", err.Error()))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(responsesBody))
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create request")
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))
	req.Host = "chatgpt.com"
	if credentialProvider.IsOpenAIAgentIdentity() {
		authHeaders, authErr := s.agentHeaders(ctx, credentialProvider)
		if authErr != nil {
			return (TestStreamOutput{}).Error(c, "Failed to build Agent Identity authentication")
		}
		for key, values := range authHeaders {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", openai.ResolveCodexOutboundIdentity("").Originator)
	if customUA := strings.TrimSpace(credentialProvider.GetOpenAIUserAgent()); customUA != "" {
		req.Header.Set("User-Agent", customUA)
	} else {
		req.Header.Set("User-Agent", openai.CodexCanonicalUserAgent())
	}
	s.ApplyRouting(c, value, req, true)
	setTestChatGPTHeaders(req.Header, credentialProvider)
	// Match originator to the final User-Agent prefix to prevent a 404 (issue #3901).
	openai.EnforceCodexIdentityHeaders(req.Header)

	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}
	resp, err := s.Transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, s.ResolveTLS(c, value))
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Responses API request failed: %s", err.Error()))
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		body = RedactAgentIdentityBody(ctx, s.read, credentialProvider, body)
		message := strings.TrimSpace(upstream.ExtractErrorMessage(body))
		if message == "" {
			message = fmt.Sprintf("Responses API returned %d", resp.StatusCode)
		}
		return (TestStreamOutput{}).Error(c, message)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to read image response: %s", err.Error()))
	}
	body = RedactAgentIdentityBody(ctx, s.read, credentialProvider, body)

	results, _, _, _, _, err := openai.CollectOpenAIImagesFromResponsesBody(body, time.Now)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to parse image response: %s", err.Error()))
	}
	if len(results) == 0 {
		return (TestStreamOutput{}).Error(c, "No images returned from responses API")
	}

	for _, item := range results {
		if item.RevisedPrompt != "" {
			(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "content", Text: item.RevisedPrompt})
		}
		mimeType := openai.OpenAIImageOutputMIMEType(item.OutputFormat)
		(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{
			Type:     "image",
			ImageURL: "data:" + mimeType + ";base64," + item.Result,
			MimeType: mimeType,
		})
	}

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_complete", Success: true})
	return nil
}

func (s *OpenAIProviderTest) read(ctx context.Context, id int64) (*providercore.Record, error) {
	return s.Store.GetByID(ctx, id)
}

func (s *OpenAIProviderTest) agentHeaders(ctx context.Context, value *providercore.Record) (http.Header, error) {
	headers, _, err := AgentIdentityHeaders(ctx, value, s.EnsureTask)
	return headers, err
}

func setTestChatGPTHeaders(headers http.Header, value *providercore.Record) {
	if headers == nil || value == nil || !value.IsOpenAIOAuthLike() {
		return
	}
	openai.SetChatGPTAccountHeaders(headers, value.GetChatGPTAccountID(), value.IsChatGPTAccountFedRAMP())
}

func mergeTestExtraUpdates(base, more map[string]any) map[string]any {
	if len(base) == 0 && len(more) == 0 {
		return nil
	}
	result := make(map[string]any, len(base)+len(more))
	maps.Copy(result, base)
	maps.Copy(result, more)
	return result
}

func testCodexFingerprint(value *providercore.Record, headers http.Header) *openai.FingerprintIDs {
	if value == nil || !value.IsOpenAIOAuthLike() {
		return nil
	}
	mode := providercore.CodexFingerprintModeFromExtra(value.Extra)
	if mode == providercore.CodexFingerprintOff {
		return nil
	}
	clientSession := ""
	if headers != nil {
		clientSession = openai.ExtractClientSessionID(headers)
	}
	return CodexFingerprintIDs(value, clientSession, mode)
}
