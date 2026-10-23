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

// openai_test_execution.go executes OpenAI text, compaction, and image probes
// using the selected authentication and protocol. OpenAI provider tests choose
// their endpoint from probe mode, explicit test type, authentication, and text
// protocol. Native and legacy compact modes use fixed payloads, explicit image
// selection requires declared image output, and API-key text requests can
// select Chat while OAuth uses Codex Responses.
//
// Shadow requests resolve their credential owner for authentication while
// keeping the selected record's transport identity. Agent Identity task
// recovery can replay text and compact attempts once without emitting another
// start event. Quota headers can produce Extra observations on failed
// requests, and 429 reconciliation persists a reset before recovering an
// error-state record. Image previews accept platform-specific response forms,
// and a nonempty API-key image array still fails when it contains no
// displayable result. Test events carry these observations through the run's
// retained sink-error handling.

// OpenAIProviderTest prepares provider-authenticated text, compact, and image
// connection probes.
type OpenAIProviderTest struct {
	// Attributes supplies capabilities for the mapped probe target. A missing
	// callback leaves automatic and image probes without required output evidence.
	Attributes ModelAttributeLookup
	// Store optionally receives probe observations, but credential-shadow lookup
	// requires its GetByID method. State write errors are ignored by best-effort
	// paths.
	Store       OpenAIProviderTestStore
	Transport   QoderTransport               // provider-aware probe HTTP sender
	ValidateURL func(string) (string, error) // configured API-key root validator
	// ApplyRouting sets outbound identity before transport. Configure this
	// callback for probe execution because request paths invoke it directly.
	ApplyRouting func(*TestRun, *providercore.Record, *http.Request, bool)
	// ResolveTLS selects the attempt's request fingerprint. The callback is
	// required for transport preparation, though its returned profile may be nil.
	ResolveTLS func(*TestRun, *providercore.Record) *tlsfingerprint.Profile
	// EnsureTask prepares or recovers Agent Identity tasks. Configure it before
	// selecting assertion authentication or a task-replay path.
	EnsureTask func(context.Context, *providercore.Record, string) error

	// Prepare optionally validates the target's run before Execute obtains
	// credentials. Preparation errors pass through event output and prevent
	// executor invocation.
	Prepare func(*TestRun, *providercore.Record) error
}

// OpenAIProviderTestStore loads credential owners and stores probe quota and
// health observations.
type OpenAIProviderTestStore interface {
	// GetByID loads the provider needed for credential-owner resolution or returns
	// a lookup error. Shadow probe preparation propagates that error before
	// sending an authenticated upstream request.
	GetByID(context.Context, int64) (*providercore.Record, error)
	// UpdateExtra stores a quota observation patch under the selected provider ID.
	// Probe paths treat errors as best effort and can still merge observed values
	// into their local record.
	UpdateExtra(context.Context, int64, map[string]any) error
	// SetError records a provider authentication failure observed by the probe.
	// Its storage error does not replace the upstream diagnostic being returned
	// from the failed request.
	SetError(context.Context, int64, string) error
	// ClearError recovers an error-state provider after reset persistence.
	// Reconciliation updates the local status and message only after this call
	// succeeds, while an earlier cooldown write stays committed on error.
	ClearError(context.Context, int64) error
	// SetRateLimited stores the provider's observed reset deadline. Reconciliation
	// publishes local rate-limit timestamps only after this write succeeds and
	// then may attempt separate error-state recovery.
	SetRateLimited(context.Context, int64, time.Time) error
	providercore.OpenAIPlanWriter // observed subscription plan writer
}

// Execute routes an OpenAI probe and emits events for its selected model. Blank
// models use the platform test default, ordinary mapping applies first, and
// legacy compact adds its separate mapping; compact modes use fixed payloads
// before image or text selection.
//
// Automatic probe type follows declared final-model output, while explicit
// image tests require the same capability and explicit text retains its route.
// OAuth and shadows resolve their credential owner for authentication, keeping
// the selected record's proxy, concurrency, and state-write ID. API-key text
// routes use the requested protocol when supplied for OpenAI, otherwise their
// resolved configuration.
//
// Text Agent Identity failures can recover the currently recorded task and
// replay once, retaining the run's start-event state. Responses are closed
// here; OAuth quota patches and 401/429 observations are best-effort state
// updates that can occur before a diagnostic probe failure is returned.
func (s *OpenAIProviderTest) Execute(c *TestRun, value *providercore.Record, modelID string, prompt string, mode string, testTypes ...string) error {
	ctx := c.Context
	mode, requestedType, explicitType := providercore.ResolveProviderTestModeAndType(mode, testTypes...)
	if explicitType {
		testTypes = []string{requestedType}
	}

	testModelID := modelID
	if testModelID == "" {
		testModelID = openai.DefaultTestModel
	}

	// Compact modes choose different model rules before fixed payload creation.
	// Native V2 uses the ordinary mapped target, while legacy compact applies its
	// configured compact mapping afterward.
	testModelID = mappedTestModel(value, testModelID)
	if mode == providercore.ProviderTestModeCompact {
		return s.executeNativeCompaction(c, value, testModelID)
	}
	if mode == providercore.ProviderTestModeLegacyCompact {
		testModelID = providercore.ResolveCompactForwardModel(value, testModelID)
		return s.executeLegacyCompact(c, value, testModelID)
	}

	// Explicit type selection determines whether the image endpoint is tested.
	// Automatic selection reads the group's declared output for each endpoint.
	// Text protocol selection also determines which protocol-specific profile rules match.
	textProtocol := providercore.TextProtocolResponses
	if value.Type == providercore.ProviderTypeAPIKey {
		textProtocol = providercore.ResolveUpstreamTextProtocol(value.Extra, providercore.TextProtocolResponses)
		if requested := c.RequestedProtocol; requested != "" && value.IsOpenAI() {
			textProtocol = requested
		}
	}
	testType, capabilityErr := resolveProbeType(ctx, s.Attributes, value, c.GroupID, testModelID, string(providercore.TestProtocolID(string(textProtocol))), testTypes...)
	if capabilityErr != nil {
		return (TestStreamOutput{}).Error(c, capabilityErr.Error())
	}
	if testType == providercore.ProviderTestTypeImage {
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
		// Agent Identity derives a signed Authorization assertion for each request.
		// Token absence therefore rejects bearer authentication while permitting the
		// task-signing path to continue.
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
		if textProtocol == providercore.TextProtocolChatCompletions {
			return s.executeChat(c, value, testModelID, prompt, normalizedBaseURL, authToken)
		}
		apiURL = httpclient.BuildOpenAIEndpointURL(normalizedBaseURL, "/v1/responses")
	} else {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Unsupported provider type: %s", value.Type))
	}

	c.Begin(true)

	// The OAuth Codex payload uses the trimmed model name, while the test-start
	// event retains the selected spelling so the administrator can identify the
	// tested configuration.
	upstreamTestModelID := testModelID
	if isOAuth {
		upstreamTestModelID = strings.TrimSpace(testModelID)
	}
	payload := openai.TestResponsesPayload(upstreamTestModelID, prompt, isOAuth)
	if err := applyProbeSampling(c, s.Attributes, value, upstreamTestModelID, "openai_responses", payload); err != nil {
		return (TestStreamOutput{}).Error(c, err.Error())
	}
	// Platform probe payloads contain JSON-compatible strings, numbers, and
	// collections. Their fixed shape allows direct encoding before the request
	// body is constructed.
	payloadBytes, _ := json.Marshal(payload)

	// Task recovery reuses this run for its replay. Emit the start event before
	// the first attempt only, so a recovered request does not introduce another
	// test start.
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
		// Routing can replace the initial User-Agent. Reconcile its Codex companion
		// headers before permitted provider overrides so this request presents one
		// paired identity.
		openai.EnforceCodexIdentityHeaders(req.Header)
	}

	// Routing has selected client identity before permitted provider overrides
	// run. Apply the shared header filter here so blocked authentication and
	// session headers cannot replace probe credentials.
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
		// Missing quota headers can produce an extraction status error. It supplies
		// no patch; the probe still handles that response's status and body below.
		if updates, err := ExtractOpenAIUsageUpdates(resp, time.Now()); err == nil && len(updates) > 0 {
			// Quota persistence is best effort after the response arrives. Retain
			// observed values in the request's local Extra even when storing the patch
			// fails.
			_ = s.Store.UpdateExtra(ctx, value.ID, updates)
			value.Extra = providercore.MergeUsageExtra(value.Extra, updates)
		}
	}

	if resp.StatusCode != http.StatusOK {
		// Keep the available non-OK body for diagnostic and health classification
		// even when reading fails. The upstream status remains the probe's reported
		// failure.
		body, _ := io.ReadAll(resp.Body)
		body = RedactAgentIdentityBody(ctx, s.read, credentialProvider, body)
		if !c.TaskRecoveryTried && credentialProvider.IsOpenAIAgentIdentity() && openai.IsAgentTaskInvalidHTTPResponse(resp.StatusCode, body) {
			expectedTaskID := credentialProvider.GetCredential("task_id")
			if err := s.EnsureTask(ctx, credentialProvider, expectedTaskID); err != nil {
				return (TestStreamOutput{}).Error(c, fmt.Sprintf("Agent Identity task recovery failed: %s", err.Error()))
			}
			// Set the replay guard before reentering Execute. A repeated task rejection
			// then returns its failure while the existing start event remains the
			// probe's sole start.
			c.TaskRecoveryTried = true
			return s.Execute(c, value, modelID, prompt, mode, testType)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			s.reconcileOpenAI429State(ctx, value, resp.Header, body)
		}
		// Persist a 401 as a provider authentication error.
		if resp.StatusCode == http.StatusUnauthorized && s.Store != nil {
			errMsg := fmt.Sprintf("Authentication failed (401): %s", string(body))
			// The upstream has already rejected authentication. Attempt provider error
			// persistence, but retain that rejection as the returned probe failure even
			// when its state write fails.
			_ = s.Store.SetError(ctx, value.ID, errMsg)
		}
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body)))
	}

	return (TestStreamOutput{}).Responses(c, resp.Body)
}

// executeChat probes an already mapped API-key model through Chat Completions
// with its validated root and token. It applies routing and allowed overrides,
// closes the response, and attempts 401/429 health updates before returning
// endpoint failures.
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
	if err := applyProbeSampling(c, s.Attributes, value, testModelID, "openai_chat_completions", payload); err != nil {
		return (TestStreamOutput{}).Error(c, err.Error())
	}
	// Platform probe payloads contain JSON-compatible strings, numbers, and
	// collections. Their fixed shape allows direct encoding before the request
	// body is constructed.
	payloadBytes, _ := json.Marshal(payload)

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_start", Model: testModelID})
	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "status", Text: "Testing connection through /v1/chat/completions"})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create Chat Completions request")
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+authToken)
	s.ApplyRouting(c, value, req, false)

	// Routing has selected client identity before permitted provider overrides
	// run. Apply the shared header filter here so blocked authentication and
	// session headers cannot replace probe credentials.
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
		// Keep the available non-OK body for diagnostic and health classification
		// even when reading fails. The upstream status remains the probe's reported
		// failure.
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			s.reconcileOpenAI429State(ctx, value, resp.Header, body)
		}
		if resp.StatusCode == http.StatusUnauthorized && s.Store != nil {
			errMsg := fmt.Sprintf("Chat Completions authentication failed (401): %s", string(body))
			// The upstream has already rejected authentication. Attempt provider error
			// persistence, but retain that rejection as the returned probe failure even
			// when its state write fails.
			_ = s.Store.SetError(ctx, value.ID, errMsg)
		}
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Chat Completions API (/v1/chat/completions) returned %d: %s", resp.StatusCode, string(body)))
	}

	return (TestStreamOutput{}).ChatCompletions(c, resp.Body)
}

// executeNativeCompaction probes Responses with the supplied ordinarily mapped
// model and fixed compaction payload. OAuth shadows use their credential owner,
// while transport and observations retain the selected record ID.
//
// Remote-compaction negotiation is restored after permitted header overrides.
// Agent Identity task rejection can replay once and retains the start-event
// flag. The response is read through a two-MiB cap and redacted before
// inspection; success requires HTTP 200 and a detected compaction output item.
// Quota and authentication observations can update provider state, while this
// probe leaves administrator capability settings unchanged.
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
	// The native compaction builder returns fixed JSON-compatible values. Encode
	// that payload directly so task replay sends the same model and compaction
	// request shape.
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

	// Permitted overrides can alter negotiation headers after initial
	// construction. Restore remote-compaction V2 selection last so the probe still
	// tests the intended endpoint capability.
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

	// Read at most two MiB and retain available bytes even if reading fails.
	// Compaction or error inspection operates on that prefix before returning the
	// probe result.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	body = RedactAgentIdentityBody(ctx, s.read, credentialProvider, body)
	if !c.TaskRecoveryTried && credentialProvider.IsOpenAIAgentIdentity() && openai.IsAgentTaskInvalidHTTPResponse(resp.StatusCode, body) {
		expectedTaskID := credentialProvider.GetCredential("task_id")
		if err := s.EnsureTask(ctx, credentialProvider, expectedTaskID); err != nil {
			return (TestStreamOutput{}).Error(c, fmt.Sprintf("Agent Identity task recovery failed: %s", err.Error()))
		}
		// Set the run's replay guard before retrying compact execution, preventing a
		// second task rejection from starting another recursive attempt.
		c.TaskRecoveryTried = true
		return s.executeNativeCompaction(c, value, testModelID)
	}

	compactionFound := openai.CompactionTestHasOutput(body)
	if s.Store != nil {
		// Save quota observations while leaving the administrator's capability setting unchanged.
		var updates map[string]any
		// A response without usable quota headers contributes no patch, including
		// extraction status errors. The compact failure path still uses the upstream
		// status and captured body below.
		if codexUpdates, err := ExtractOpenAIUsageUpdates(resp, time.Now()); err == nil && len(codexUpdates) > 0 {
			updates = mergeTestExtraUpdates(updates, codexUpdates)
		}
		if len(updates) > 0 {
			// Quota persistence is best effort after the response arrives. Retain
			// observed values in the request's local Extra even when storing the patch
			// fails.
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
			// The upstream has already rejected authentication. Attempt provider error
			// persistence, but retain that rejection as the returned probe failure even
			// when its state write fails.
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

// executeLegacyCompact probes the compact endpoint with an already
// legacy-mapped model and fixed payload. OAuth shadows resolve authentication
// through their credential owner, while API-key roots are validated and receive
// the compact path suffix.
//
// Agent Identity task rejection can recover and replay once with the same run's
// start-event state. The capped response is redacted, and HTTP 200 produces
// probe success without a separate compaction-item validation. Quota, 401, and
// 429 observations remain eligible for state updates, but no capability setting
// is written by this method.
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

	// The legacy compact builder returns fixed JSON-compatible values. Its direct
	// encoding retains the prepared model and fixed input on any task-recovery
	// replay.
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
		// Automatic routing can replace the initially selected User-Agent. Rebuild
		// its Codex companion headers before this compact request reaches transport.
		openai.EnforceCodexIdentityHeaders(req.Header)
	}

	// Routing has selected client identity before permitted provider overrides
	// run. Apply the shared header filter here so blocked authentication and
	// session headers cannot replace probe credentials.
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

	// Read at most two MiB and retain available bytes even if reading fails.
	// Compaction or error inspection operates on that prefix before returning the
	// probe result.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	body = RedactAgentIdentityBody(ctx, s.read, credentialProvider, body)
	if !c.TaskRecoveryTried && credentialProvider.IsOpenAIAgentIdentity() && openai.IsAgentTaskInvalidHTTPResponse(resp.StatusCode, body) {
		expectedTaskID := credentialProvider.GetCredential("task_id")
		if err := s.EnsureTask(ctx, credentialProvider, expectedTaskID); err != nil {
			return (TestStreamOutput{}).Error(c, fmt.Sprintf("Agent Identity task recovery failed: %s", err.Error()))
		}
		// Set the run's replay guard before retrying compact execution, preventing a
		// second task rejection from starting another recursive attempt.
		c.TaskRecoveryTried = true
		return s.executeLegacyCompact(c, value, testModelID)
	}

	if s.Store != nil {
		// Save quota observations while leaving the administrator's capability setting unchanged.
		var updates map[string]any
		// A response without usable quota headers contributes no patch, including
		// extraction status errors. The compact failure path still uses the upstream
		// status and captured body below.
		if codexUpdates, err := ExtractOpenAIUsageUpdates(resp, time.Now()); err == nil && len(codexUpdates) > 0 {
			updates = mergeTestExtraUpdates(updates, codexUpdates)
		}
		if len(updates) > 0 {
			// Quota persistence is best effort after the response arrives. Retain
			// observed values in the request's local Extra even when storing the patch
			// fails.
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
			// The upstream has already rejected authentication. Attempt provider error
			// persistence, but retain that rejection as the returned probe failure even
			// when its state write fails.
			_ = s.Store.SetError(ctx, value.ID, errMsg)
		}
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body)))
	}

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "content", Text: "Compact test succeeded"})
	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_complete", Success: true})
	return nil
}

// reconcileOpenAI429State observes the reported plan and persists a reset
// selected from Codex headers before the response body. Missing dependencies or
// reset signals skip further handling, and a failed rate-limit write leaves the
// record's in-memory times unchanged.
//
// After reset persistence succeeds, the selected record receives its rate-limit
// timestamps. An error-state record then attempts ClearError; failure leaves
// the already persisted cooldown in place, while success updates Status and
// ErrorMessage locally. These are separate writes, so callers cannot treat a
// later recovery error as rollback of the observed reset.
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

	// Persist the observed reset before publishing local timestamps or clearing an
	// error. A failed write leaves those later recovery steps untouched.
	if err := s.Store.SetRateLimited(ctx, value.ID, *resetAt); err != nil {
		return
	}

	now := time.Now()
	value.RateLimitedAt = &now
	value.RateLimitResetAt = resetAt

	// The cooldown has already been committed. Recover the error state separately,
	// retaining that earlier limit when ClearError fails before local status
	// publication.
	if value.Status == providercore.StatusError {
		if err := s.Store.ClearError(ctx, value.ID); err != nil {
			return
		}
		value.Status = providercore.StatusActive
		value.ErrorMessage = ""
	}
}

// ExecuteImageAPIKey requests one image using the supplied model and prompt
// with API-key authentication. It validates the configured root, applies route
// and header policy, closes the response, and requires HTTP 200 with decodable
// image entries. The configured generation capability selects the outgoing
// mode; native streams aggregate through the shared Images reader with partial
// previews disabled. Conversion failures end this probe without another POST.
//
// A base64 value wins over an entry's URL. Otherwise HTTPS or data:image/
// values are displayable, and revised prompts can be emitted before their
// images. The method counts emitted images separately from array entries, so a
// nonempty array of unusable values returns a diagnostic failure instead of
// successful completion.
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
	// Platform probe payloads contain JSON-compatible strings, numbers, and
	// collections. Their fixed shape allows direct encoding before the request
	// body is constructed.
	payloadBytes, _ := json.Marshal(payload)
	mode, err := value.ImageStreamMode(capability.ProtocolImagesGenerations)
	if err != nil {
		return (TestStreamOutput{}).Error(c, err.Error())
	}
	payloadBytes, _, err = upstream.RewriteImageRequest(payloadBytes, "application/json", modelID, mode, false)
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to prepare image request")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create request")
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)
	s.ApplyRouting(c, value, req, false)

	// Routing has selected client identity before permitted provider overrides
	// run. Apply the shared header filter here so blocked authentication and
	// session headers cannot replace probe credentials.
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

	download := openai.ImageBackfillOptions{
		ValidateURL: s.ValidateURL,
		Do: func(req *http.Request) (*http.Response, error) {
			return s.Transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, s.ResolveTLS(c, value))
		},
	}
	options := openai.ImageResponseOptions{
		ExpectedImages: 1,
		ReadLimit:      func() int64 { return 64 << 20 },
		ReadBody:       func(reader io.Reader) ([]byte, error) { return httpclient.ReadResponseBodyLimited(reader, 64<<20) },
		FetchImage:     func(rawURL string) (string, error) { return download.FetchBase64(ctx, rawURL) },
	}
	var body []byte
	if resp.StatusCode >= 400 {
		body, err = options.ReadBody(resp.Body)
	} else {
		body, _, err = openai.ReadImagesBufferedResponse(ctx, resp, options, "b64_json")
	}
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to read response: %s", err.Error()))
	}

	if resp.StatusCode != http.StatusOK {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body)))
	}

	// Accept b64_json and URL results because relays may ignore response_format and return links.
	var result struct {
		// Data contains upstream image candidates. Completion requires at least one
		// emitted image, so an array alone does not establish a usable preview.
		Data []struct {
			// B64JSON and URL are alternative preview sources. A nonempty B64JSON wins;
			// otherwise URL must begin with HTTPS or data:image/ before emission.
			B64JSON       string `json:"b64_json"`       // base64 image preview payload
			URL           string `json:"url"`            // alternate image preview URL
			RevisedPrompt string `json:"revised_prompt"` // upstream revised prompt text
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
	// Count emitted previews separately from response entries. Rejected URL
	// schemes and empty payloads can leave a nonempty array with no image the test
	// UI can display.
	if images == 0 {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Upstream returned no image data: %s", logredact.TruncateLine(body, 512)))
	}

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_complete", Success: true})
	return nil
}

// ExecuteImageOAuth builds a Codex Responses image-tool request with provider
// or shadow-owner authentication. It uses the selected record's proxy,
// concurrency, and TLS choices while account headers follow the credential
// owner.
//
// The image builder supplies platform defaults and the outbound agent is paired
// with Codex identity before sending. Failed statuses return a redacted
// diagnostic from a capped body; successful replies are collected into image
// results before previews are emitted. No result is a probe failure, and this
// path closes the response without task-recovery replay.
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
	// Routing can replace the initial User-Agent. Pair its Codex companion
	// headers with that final value before preparing transport for this image
	// probe.
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
		// Limit failed image-response diagnostics to two MiB and retain available
		// bytes on read failure, then redact Agent Identity values before extracting
		// the message.
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

// read loads a provider record through the configured probe store.
func (s *OpenAIProviderTest) read(ctx context.Context, id int64) (*providercore.Record, error) {
	return s.Store.GetByID(ctx, id)
}

// agentHeaders obtains signed authentication through EnsureTask and returns its
// headers or preparation error. The signing task ID is discarded here; replay
// paths read the credential owner's current task field after a rejected
// response.
func (s *OpenAIProviderTest) agentHeaders(ctx context.Context, value *providercore.Record) (http.Header, error) {
	headers, _, err := AgentIdentityHeaders(ctx, value, s.EnsureTask)
	return headers, err
}

// setTestChatGPTHeaders applies account and FedRAMP headers for OpenAI
// OAuth-like credentials. Nil maps, nil records, and unsupported authentication
// leave headers untouched; callers pass the resolved credential owner for
// shadow probes.
func setTestChatGPTHeaders(headers http.Header, value *providercore.Record) {
	if headers == nil || value == nil || !value.IsOpenAIOAuthLike() {
		return
	}
	openai.SetChatGPTAccountHeaders(headers, value.GetChatGPTAccountID(), value.IsChatGPTAccountFedRAMP())
}

// mergeTestExtraUpdates returns a new shallow map combining both patches, with
// more taking precedence. Two empty maps return nil, and nested mutable values
// remain shared with their source patches.
func mergeTestExtraUpdates(base, more map[string]any) map[string]any {
	if len(base) == 0 && len(more) == 0 {
		return nil
	}
	result := make(map[string]any, len(base)+len(more))
	maps.Copy(result, base)
	maps.Copy(result, more)
	return result
}

// testCodexFingerprint derives attempt IDs for OpenAI OAuth-like records using
// configured fingerprint mode and inbound session headers. Unsupported records,
// disabled modes, and missing valid seeds return nil; callers reuse the result
// for that attempt.
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
