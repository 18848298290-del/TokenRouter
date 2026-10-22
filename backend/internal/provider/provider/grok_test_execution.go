package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// grok_test_execution.go executes Grok text and image probes and publishes
// their content and quota observations. Grok provider probes select Responses
// for text and the image-generation endpoint for image tests. Group attributes
// validate image output and select automatic probe types. OAuth uses the
// manual-test token path, while API-key probes read the saved key.
// Saved model mapping precedes request construction, and resolved endpoint
// policy still applies to either credential kind before the shared transport
// is called.
//
// Text responses can persist quota headers and recover or install their
// observed windows before body-status handling. Further failure classification
// distinguishes free usage, recoverable billing and a payment-required
// temporary window. Image responses instead emit previews from base64 or URL
// entries and require at least one usable image value for success. Both
// request paths consume and close the returned body through this synchronous
// probe invocation.

// GrokProviderTest probes Grok with shared token and transport services and
// records quota health.
type GrokProviderTest struct {
	// Attributes resolves the mapped model for the selected group and endpoint.
	// Missing declarations reject automatic and image probes before credentials
	// are acquired.
	Attributes ModelAttributeLookup
	// Tokens is required for OAuth probes and uses manual-test acquisition so
	// scheduling exclusions do not prevent an administrator credential test.
	Tokens *providercore.GrokTokenSource
	// Transport sends prepared provider requests. A nil value emits a test error
	// before token acquisition, and each successful call returns a
	// caller-owned body.
	Transport interface {
		// Do sends a prepared request with proxy and provider transport values.
		// The probe consumes and closes a successful response body; request errors
		// are emitted through the test run before any body handling.
		Do(*http.Request, string, int64, int) (*http.Response, error)
	}
	// Store optionally persists text quota snapshots, observed limits and selected
	// failure cooldowns. Image response handling does not use these text state
	// writes.
	Store interface {
		providercore.GrokRateLimitWriter // provider-wide deadline write interface
		// UpdateExtra stores observed text quota data for the selected provider.
		// The probe treats its write error as best effort, then continues limit
		// handling and response-status processing using that same observation.
		UpdateExtra(context.Context, int64, map[string]any) error
		// SetTempUnschedulable stores a selected temporary failure window and
		// reason.
		// The probe supplies a detached state-write context for these cooldown
		// paths
		// and discards write errors while reporting the upstream test failure.
		SetTempUnschedulable(context.Context, int64, time.Time, string) error
	}
	// OperatorValidator governs custom and API-key endpoints. Nil selects the
	// platform validator through GrokBaseURLValidator before requests are prepared.
	OperatorValidator xai.BaseURLValidator
	// DefaultBaseURL optionally resolves dynamic text defaults at call time.
	// Image URL preparation uses the platform endpoint choice from its
	// provider record.
	DefaultBaseURL func(context.Context) string
	// SuppressStart skips the text test_start event when enabled. Image probes
	// still emit their own start event before sending the image request.
	SuppressStart bool
}

// Execute runs a Grok text or image probe with the selected group and credentials.
// It accepts prompt and test type as the first two optional arguments, maps the
// test name, and defaults missing text identity through the platform resolver.
// OAuth requires Tokens; API-key input requires a nonblank saved key.
// Empty explicit image input defaults before provider mapping; empty text input
// defaults afterward. Each model is mapped once and that final identity selects
// its group attributes and outgoing request. Automatic probes prefer declared
// image output, and explicit text keeps its selected route. Attribute errors
// stop before credential acquisition or transport.
//
// Text handling stamps quota plan evidence and performs best-effort snapshot/
// limit writes before inspecting a failed body. Content-policy rejection skips
// the additional body-based cooldown classification, while observed quota writes
// may already have occurred. Successful text bodies use the shared Responses
// event reader; every transport response on this path is closed on return.
func (s *GrokProviderTest) Execute(c *TestRun, value *providercore.Record, modelID string, testArgs ...string) error {
	ctx := c.Context
	prompt := ""
	var testTypes []string
	if len(testArgs) > 0 {
		prompt = testArgs[0]
	}
	if len(testArgs) > 1 {
		testTypes = testArgs[1:2]
	}

	if s.Transport == nil {
		return (TestStreamOutput{}).Error(c, "HTTP upstream not configured")
	}

	testModelID := strings.TrimSpace(modelID)
	requestedType, explicitType := providercore.ProviderTestTypeFromArgs(testTypes...)
	if testModelID == "" && explicitType && requestedType == providercore.ProviderTestTypeImage {
		testModelID = "grok-imagine-image"
	}
	// Image defaults participate in provider mapping, while a blank text request
	// selects its platform default afterward. Resolve each supplied alias once so
	// capability lookup and dispatch cannot follow different mapping hops.
	if mapped := strings.TrimSpace(mappedTestModel(value, testModelID)); mapped != "" {
		testModelID = mapped
	}
	testModelID = xai.ResolveDefaultTextModel(testModelID, xai.DefaultResponsesModel)
	testType, capabilityErr := resolveProbeType(ctx, s.Attributes, value, c.GroupID, testModelID, "openai_responses", testTypes...)
	if capabilityErr != nil {
		return (TestStreamOutput{}).Error(c, capabilityErr.Error())
	}

	var authToken string
	switch value.Type {
	case capability.ProviderTypeOAuth:
		if s.Tokens == nil {
			return (TestStreamOutput{}).Error(c, "Grok token provider not configured")
		}
		var err error
		// Administrator probes need credential results even for
		// scheduling-excluded records.
		// Use the manual-test source so rate limits, overload and temporary
		// exclusion
		// do not block this deliberate test before its upstream request.
		authToken, err = s.Tokens.GetAccessTokenForManualTest(ctx, value)
		if err != nil {
			return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to get Grok access token: %s", err.Error()))
		}
	case capability.ProviderTypeAPIKey:
		authToken = strings.TrimSpace(value.GetCredential("api_key"))
		if authToken == "" {
			return (TestStreamOutput{}).Error(c, "Grok API key is missing")
		}
	default:
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Unsupported Grok provider type: %s", value.Type))
	}
	if testType == providercore.ProviderTestTypeImage {
		imagePrompt := strings.TrimSpace(prompt)
		if imagePrompt == "" {
			imagePrompt = "Generate a cute orange cat astronaut sticker on a clean pastel background."
		}
		return s.executeImage(c, ctx, value, authToken, testModelID, imagePrompt)
	}

	apiURL, err := s.responsesURL(value)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Invalid Grok base URL: %s", err.Error()))
	}

	c.Begin(true)

	payloadBytes, err := xai.ProviderTestBody(testModelID, prompt)
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create Grok test payload")
	}

	if !s.SuppressStart {
		(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_start", Model: testModelID})
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create Grok request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+authToken)
	if value.IsGrokOAuth() && (xai.MediaCodec{}).IsGrokCLIProxyTarget(apiURL) {
		xai.ApplyCLIHeaders(req.Header)
	}
	// Provider overrides pass through the shared protected-header policy after CLI
	// defaults, so a probe uses the same enabled override values as provider
	// requests.
	applyGrokQuotaHeaders(value, req.Header)

	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}

	resp, err := s.Transport.Do(req, proxyURL, value.ID, value.Concurrency)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Grok Responses API request failed: %s", err.Error()))
	}
	// The Responses reader consumes this text stream after quota handling. Close
	// the same body on earlier status failures so both routes release
	// transport ownership.
	defer func() { _ = resp.Body.Close() }()

	now := time.Now()
	snapshot := xai.ParseQuotaObservation(resp.Header, resp.StatusCode, now)
	providercore.StampGrokQuotaPlan(value, snapshot, testModelID, xai.ResolveGrokTextResponsesModelID, xai.ApplyGrok45ResponsesPlanSignal)
	// Header observations are useful on failed statuses too. Save and apply their
	// timing before body classification, retaining the upstream test outcome
	// if storage fails.
	if snapshot != nil && s.Store != nil {
		resetAt, limited := providercore.GrokRateLimitResetAtForProvider(value, snapshot, now)
		if limited {
			providercore.NormalizeGrokExhaustedWindowResets(snapshot, resetAt, now)
		}
		_ = s.Store.UpdateExtra(ctx, value.ID, map[string]any{
			"grok_usage_snapshot": snapshot,
		})
		if limited {
			providercore.PersistGrokRateLimit(ctx, s.Store, value, resetAt, slog.Warn)
		} else if providercore.IsSuccessfulGrokRateLimitRecovery(value, snapshot) {
			providercore.ClearGrokRateLimitAfterRecovery(ctx, s.Store, value, slog.Warn)
		}
	} else if s.Store != nil && providercore.IsSuccessfulGrokRateLimitRecovery(value, &xai.QuotaSnapshot{StatusCode: resp.StatusCode}) {
		providercore.ClearGrokRateLimitAfterRecovery(ctx, s.Store, value, slog.Warn)
	}

	if resp.StatusCode != http.StatusOK {
		// A failing text response remains a status error even if body reading stops
		// early; retain partial diagnostic text for output and cooldown
		// classification.
		body, _ := io.ReadAll(resp.Body)
		if s.Store != nil && !xai.IsGrokContentPolicyRejection(resp.StatusCode, body) {
			decision := xai.ClassifyGrokUpstreamFailure(resp.StatusCode, body, testModelID)
			switch {
			case decision.Class == xai.GrokFailureFreeUsage:
				// Prefer an active observed reset to the short probe cooldown.
				// Missing
				// usable timing selects detached temporary state while the
				// upstream failure remains visible.
				if resetAt, limited := providercore.GrokRateLimitResetAtForProvider(value, snapshot, now); limited && resetAt.After(now) {
					providercore.PersistGrokRateLimit(ctx, s.Store, value, resetAt, slog.Warn)
				} else {
					stateCtx, cancel := grokTestStateContext(ctx)
					_ = s.Store.SetTempUnschedulable(stateCtx, value.ID, now.Add(xai.GrokFreeUsageProbeCooldown), "grok free usage exhausted")
					cancel()
				}
			case decision.Class == xai.GrokFailureBilling && (xai.IsSpendingLimitError(body) || strings.Contains(strings.ToLower(decision.Reason), "credit")):
				providercore.PersistGrokRateLimit(ctx, s.Store, value, providercore.GrokSpendingLimitResetAt(value, now), slog.Warn)
			case resp.StatusCode == http.StatusPaymentRequired:
				// No spending or credit reset was selected. Use the temporary
				// billing window
				// for payment-required response handling, detached from the
				// failed probe cancellation.
				stateCtx, cancel := grokTestStateContext(ctx)
				_ = s.Store.SetTempUnschedulable(stateCtx, value.ID, now.Add(30*time.Minute), "grok payment required")
				cancel()
			}
		}
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Grok Responses API returned %d: %s", resp.StatusCode, string(body)))
	}

	return (TestStreamOutput{}).Responses(c, resp.Body)
}

// executeImage calls the selected image endpoint and emits returned preview values.
// The request asks for one image in b64_json format. Response reading and JSON
// failures become test errors; MIME defaults to image/png, and nonblank base64
// takes precedence over a nonblank URL. Revised prompts can emit text, but
// success requires at least one image value. The response body closes on return.
func (s *GrokProviderTest) executeImage(c *TestRun, ctx context.Context, value *providercore.Record, authToken, modelID, prompt string) error {
	apiURL, err := s.imageURL(value)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Invalid Grok image base URL: %s", err.Error()))
	}

	c.Begin(true)
	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_start", Model: modelID})

	payloadBytes, err := json.Marshal(map[string]any{
		"model":           modelID,
		"prompt":          prompt,
		"n":               1,
		"response_format": "b64_json",
	})
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create Grok image test payload")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to create Grok image request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)
	if value.IsGrokOAuth() && (xai.MediaCodec{}).IsGrokCLIProxyTarget(apiURL) {
		xai.ApplyCLIHeaders(req.Header)
	}
	applyGrokQuotaHeaders(value, req.Header)

	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}
	resp, err := s.Transport.Do(req, proxyURL, value.ID, value.Concurrency)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Grok image request failed: %s", err.Error()))
	}
	// Keep the image response through preview parsing, then close even if JSON
	// decoding or a missing image value returns an error before test completion.
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to read Grok image response: %s", err.Error()))
	}
	if resp.StatusCode != http.StatusOK {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Grok image API returned %d: %s", resp.StatusCode, string(body)))
	}

	var result struct {
		Data []struct { // returned image preview entries
			// B64JSON and URL supply alternate image values. A nonblank base64
			// value
			// wins; URL is considered when base64 is blank, and neither value means
			// this item cannot increment the successful image count.
			B64JSON string `json:"b64_json"` // encoded base64 image bytes
			URL     string `json:"url"`      // upstream image preview URL
			// RevisedPrompt emits a content event when nonempty, even if this item
			// supplies no image value; that event alone cannot make the image
			// probe succeed.
			RevisedPrompt string `json:"revised_prompt"`
			// MimeType is trimmed before image event construction. Blank input
			// selects
			// image/png for both base64 data URLs and plain URL preview events.
			MimeType string `json:"mime_type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to parse Grok image response: %s", err.Error()))
	}
	if len(result.Data) == 0 {
		return (TestStreamOutput{}).Error(c, "No images returned from Grok API")
	}
	images := 0
	for _, item := range result.Data {
		if item.RevisedPrompt != "" {
			(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "content", Text: item.RevisedPrompt})
		}
		mimeType := strings.TrimSpace(item.MimeType)
		if mimeType == "" {
			mimeType = "image/png"
		}
		// Base64 values become data URLs carrying the resolved MIME type, while URL
		// items retain their upstream address for the test event consumer to
		// display.
		switch {
		case strings.TrimSpace(item.B64JSON) != "":
			images++
			(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "image", ImageURL: "data:" + mimeType + ";base64," + item.B64JSON, MimeType: mimeType})
		case strings.TrimSpace(item.URL) != "":
			images++
			(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "image", ImageURL: item.URL, MimeType: mimeType})
		}
	}
	// Revised prompt text can be emitted by an item without image bytes or URL.
	// Require an image value so a text-only HTTP 200 cannot mark this image
	// test successful.
	// The failure diagnostic is bounded and redacted before the test event
	// receives it.
	if images == 0 {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Grok returned no image data: %s", logredact.TruncateLine(body, 512)))
	}
	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_complete", Success: true})
	return nil
}

// responsesURL builds a validated text endpoint using current dynamic defaults.
// It chooses the provider validator first, then reads DefaultBaseURL through
// Background during endpoint construction. The actual probe request retains its
// TestRun context for transport cancellation.
func (s *GrokProviderTest) responsesURL(value *providercore.Record) (string, error) {
	validator, err := GrokBaseURLValidator(value, s.OperatorValidator)
	if err != nil {
		return "", err
	}
	// Endpoint defaults are read during request construction, using Background for
	// settings access while the constructed request still follows its test-run
	// cancellation.
	transport := GrokQuotaTransport{DefaultBaseURL: s.DefaultBaseURL}
	return xai.BuildResponsesURLWithValidator(transport.baseURL(context.Background(), value, true), validator)
}

// imageURL builds the validated image-generation endpoint for the selected
// provider.
// It uses saved/platform endpoint choice, maps an official OAuth CLI target to
// the public media API, and retains custom endpoint routing. Dynamic text
// DefaultBaseURL is not read on this path.
func (s *GrokProviderTest) imageURL(value *providercore.Record) (string, error) {
	validator, err := GrokBaseURLValidator(value, s.OperatorValidator)
	if err != nil {
		return "", err
	}
	base := (&GrokQuotaTransport{}).baseURL(context.Background(), value, false)
	// The subscription CLI target serves text. Redirect its image route to the
	// public media API while retaining custom operator endpoint selection for
	// validation.
	if value.IsGrokOAuth() && (xai.MediaCodec{}).IsGrokCLIProxyTarget(base) {
		base = xai.DefaultBaseURL
	}
	return xai.BuildMediaEndpointURL(base, xai.GrokMediaEndpointImagesGenerations, "", validator)
}

// grokTestStateContext creates a detached five-second budget for probe health
// persistence.
// Nonnil input retains its values but loses request cancellation and deadline;
// nil input uses Background. Callers invoke the returned cancel after their
// temporary-state write.
func grokTestStateContext(ctx context.Context) (context.Context, context.CancelFunc) {
	// A failed probe may already have canceled its run context. Retain its
	// values and give the selected health write an independent deadline.
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, 5*time.Second)
}
