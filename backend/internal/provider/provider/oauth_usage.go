package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// oauth_usage.go issues OpenAI and Anthropic usage requests with provider
// authentication and transport settings. OpenAI usage probes can obtain quota
// observations from headers even when the inference endpoint returns an error.
// The provider record selects bearer or Agent Identity authentication, and the
// final User-Agent determines the accompanying Codex identity headers before
// transport runs.
//
// A shared transport carries provider proxy, concurrency, and optional TLS
// fingerprint settings; callers without it use a pooled HTTP client.
// Fingerprint lookup failures keep the default identity rather than failing
// usage display. Agent task registration delegates to the shared coordinator
// with a fallback mutex for unpersisted records. Anthropic usage forwards the
// same proxy and fingerprint choices through its usage client, while provider
// services keep responsibility for caching, cancellation tracking, and
// identity-checked snapshot writes.

// OAuthUsageTransport prepares platform usage requests with provider
// credentials and transport dependencies.
type OAuthUsageTransport struct {
	// Transport optionally sends provider-aware OpenAI probes. Its absence uses a
	// pooled HTTP client built from the selected proxy.
	Transport QoderTransport
	// Profiles optionally resolves provider request fingerprints. A nil service
	// leaves usage requests without an explicitly selected TLS profile.
	Profiles *egressprovider.TLSProfiles
	// Fingerprints optionally supplies cached client identity. Read failures are
	// ignored, leaving default OpenAI headers or Anthropic options in effect.
	Fingerprints anthropic.FingerprintCache
	// EnsureTask takes precedence over the coordinator path when configured. Its
	// task preparation errors propagate to Agent Identity probe authentication.
	EnsureTask func(context.Context, *provider.Record, string) error
	// Tasks and TaskOptions supply registration when EnsureTask is absent.
	// ensureTask copies the options and installs fallback as the instance-local
	// lock for unpersisted records.
	Tasks       *provider.OpenAITaskCoordinator // shared task registration coordinator
	TaskOptions provider.OpenAITaskOptions      // task registration callback dependencies
	fallback    sync.Mutex                      // unpersisted task registration lock
}

// ClaudeUsageClient retrieves Anthropic usage from prepared credential and
// transport options.
type ClaudeUsageClient interface {
	// FetchUsageWithOptions queries Anthropic usage with supplied authentication,
	// proxy, and fingerprint settings. The caller keeps options stable through the
	// request and receives either parsed usage or a fetch error.
	FetchUsageWithOptions(context.Context, *anthropic.UsageFetchOptions) (*provider.ClaudeUsageResponse, error)
}

// ensureTask invokes the injected task callback when present, otherwise
// delegates to Tasks. The coordinator receives a copied TaskOptions value with
// this transport's fallback mutex for unpersisted provider task registration.
func (s *OAuthUsageTransport) ensureTask(ctx context.Context, value *provider.Record, expected string) error {
	if s.EnsureTask != nil {
		return s.EnsureTask(ctx, value, expected)
	}
	options := s.TaskOptions
	options.FallbackMutex = &s.fallback
	return s.Tasks.Ensure(ctx, options, value, expected)
}

// profile resolves the record's enabled request TLS fingerprint through
// Profiles. Nil transports or profile services return nil; callers provide a
// nonnil record when profile selection is configured.
func (s *OAuthUsageTransport) profile(value *provider.Record) *tlsfingerprint.Profile {
	if s == nil || s.Profiles == nil {
		return nil
	}
	return s.Profiles.ResolveRequestTLS(egress.TLSSelection{Enabled: value.IsTLSFingerprintEnabled(), DirectProfileID: value.GetTLSFingerprintProfileID()})
}

// ProbeOpenAI sends a Codex probe and returns quota Extra updates extracted
// from its headers. Nil or non-OAuth records return nil data, and bearer
// authentication requires an access token.
//
// The network request receives a fifteen-second context, while Agent Identity
// task preparation uses the original caller context. Cached fingerprints can
// replace the default User-Agent; identity headers are then reconciled with
// that final value. The response body is closed here, and header quota data
// takes precedence over status errors, allowing useful observations from a
// rate-limited probe.
func (s *OAuthUsageTransport) ProbeOpenAI(ctx context.Context, value *provider.Record) (map[string]any, error) {
	if value == nil || !value.IsOAuth() {
		return nil, nil
	}
	accessToken := ""
	if !value.IsOpenAIAgentIdentity() {
		accessToken = value.GetOpenAIAccessToken()
	}
	if accessToken == "" && !value.IsOpenAIAgentIdentity() {
		return nil, fmt.Errorf("no access token available")
	}
	payload := openai.TestResponsesPayload(openai.CodexUsageProbeModel, "", true)
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal openai probe payload: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("create openai probe request: %w", err)
	}
	req.Host = "chatgpt.com"
	req.Header.Set("Content-Type", "application/json")
	if value.IsOpenAIAgentIdentity() {
		headers, _, authErr := AgentIdentityHeaders(ctx, value, s.ensureTask)
		if authErr != nil {
			return nil, fmt.Errorf("build Agent Identity authentication: %w", authErr)
		}
		for key, values := range headers {
			for _, item := range values {
				req.Header.Add(key, item)
			}
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	identity := openai.ResolveCodexOutboundIdentity("")
	req.Header.Set("Originator", identity.Originator)
	req.Header.Set("Version", identity.Version)
	req.Header.Set("User-Agent", identity.UserAgent)
	// A cache read failure keeps the default Codex User-Agent. Usage observation
	// can continue without cached identity, then reconcile headers to the selected
	// value below.
	if s.Fingerprints != nil {
		if fingerprint, readErr := s.Fingerprints.GetFingerprint(reqCtx, value.ID); readErr == nil && fingerprint != nil && strings.TrimSpace(fingerprint.UserAgent) != "" {
			req.Header.Set("User-Agent", strings.TrimSpace(fingerprint.UserAgent))
		}
	}
	// Cached fingerprints can replace User-Agent after defaults were written.
	// Reconcile originator and version from that final value so one probe presents
	// a consistent Codex identity.
	openai.EnforceCodexIdentityHeaders(req.Header)
	if value.IsOpenAIOAuthLike() {
		openai.SetChatGPTAccountHeaders(req.Header, value.GetChatGPTAccountID(), value.IsChatGPTAccountFedRAMP())
	}
	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}
	response, err := s.doOpenAI(req, value, proxyURL)
	if err != nil {
		return nil, fmt.Errorf("openai codex probe request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	return ExtractOpenAIUsageUpdates(response, time.Now())
}

// doOpenAI sends a prepared request through the injected OpenAI-profile
// transport when configured. Otherwise it builds a pooled client using the
// proxy, fifteen-second timeout, and ten-second response-header timeout; the
// caller owns any returned body.
func (s *OAuthUsageTransport) doOpenAI(request *http.Request, value *provider.Record, proxy string) (*http.Response, error) {
	if s != nil && s.Transport != nil {
		request = request.WithContext(upstream.WithHTTPUpstreamProfile(request.Context(), upstream.HTTPUpstreamProfileOpenAI))
		return s.Transport.DoWithTLS(request, proxy, value.ID, value.Concurrency, s.profile(value))
	}
	// Callers without the provider transport still use the proxy through a pooled
	// client. Its request and header deadlines bound that compatibility path.
	client, err := httpclient.GetClient(httpclient.Options{ProxyURL: proxy, Timeout: 15 * time.Second, ResponseHeaderTimeout: 10 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("build openai probe client: %w", err)
	}
	return client.Do(request)
}

// ExtractOpenAIUsageUpdates returns a new quota patch when Codex headers parse,
// including on error responses. Nil responses return nil data without an error,
// and the caller supplies the fallback observation time used for relative
// resets.
//
// Headers are inspected before status because a 429 probe can still report both
// quota windows. A response lacking quota headers returns a status error
// outside the 2xx range, or nil data for a successful status; this function
// reads no body and leaves response cleanup to its caller.
func ExtractOpenAIUsageUpdates(response *http.Response, now time.Time) (map[string]any, error) {
	if response == nil {
		return nil, nil
	}
	if snapshot := openai.ParseCodexRateLimitHeaders(response.Header); snapshot != nil {
		return provider.BuildCodexUsageExtraUpdates(snapshot, now), nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("openai codex probe returned status %d", response.StatusCode)
	}
	return nil, nil
}

// FetchAnthropic retrieves usage with a nonnil record and configured client.
// Missing access tokens fail before the request; loaded proxy and optional
// fingerprint values populate fetch options, and fingerprint lookup errors
// leave those options unaugmented.
func (s *OAuthUsageTransport) FetchAnthropic(ctx context.Context, value *provider.Record, client ClaudeUsageClient) (*provider.ClaudeUsageResponse, error) {
	token := value.GetCredential("access_token")
	if token == "" {
		return nil, fmt.Errorf("no access token available")
	}
	proxy := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxy = value.Proxy.URL()
	}
	options := &anthropic.UsageFetchOptions{AccessToken: token, ProxyURL: proxy, ProviderID: value.ID, TLSProfile: s.profile(value)}
	// Anthropic usage accepts its prepared default options when the fingerprint
	// cache is unavailable or fails, so identity-cache errors do not replace a
	// usable quota response.
	if s.Fingerprints != nil {
		if fingerprint, err := s.Fingerprints.GetFingerprint(ctx, value.ID); err == nil && fingerprint != nil {
			options.Fingerprint = fingerprint
		}
	}
	return client.FetchUsageWithOptions(ctx, options)
}
