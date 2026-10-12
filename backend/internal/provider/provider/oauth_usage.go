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

// OAuthUsageTransport performs usage requests; the provider service owns query caching and identity
// rechecks.
type OAuthUsageTransport struct {
	Transport    QoderTransport
	Profiles     *egressprovider.TLSProfiles
	Fingerprints anthropic.FingerprintCache
	EnsureTask   func(context.Context, *provider.Record, string) error
	Tasks        *provider.OpenAITaskCoordinator
	TaskOptions  provider.OpenAITaskOptions
	fallback     sync.Mutex
}

// ClaudeUsageClient fetches upstream usage with provider request options.
type ClaudeUsageClient interface {
	FetchUsageWithOptions(context.Context, *anthropic.UsageFetchOptions) (*provider.ClaudeUsageResponse, error)
}

// ensureTask uses the injected callback or supplies an instance-local fallback mutex to the task
// coordinator.
func (s *OAuthUsageTransport) ensureTask(ctx context.Context, value *provider.Record, expected string) error {
	if s.EnsureTask != nil {
		return s.EnsureTask(ctx, value, expected)
	}
	options := s.TaskOptions
	options.FallbackMutex = &s.fallback
	return s.Tasks.Ensure(ctx, options, value, expected)
}

func (s *OAuthUsageTransport) profile(value *provider.Record) *tlsfingerprint.Profile {
	if s == nil || s.Profiles == nil {
		return nil
	}
	return s.Profiles.ResolveRequestTLS(egress.TLSSelection{Enabled: value.IsTLSFingerprintEnabled(), DirectProfileID: value.GetTLSFingerprintProfileID()})
}

// ProbeOpenAI sends a probe with a fifteen-second request timeout and platform identity headers.
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
	if s.Fingerprints != nil {
		if fingerprint, readErr := s.Fingerprints.GetFingerprint(reqCtx, value.ID); readErr == nil && fingerprint != nil && strings.TrimSpace(fingerprint.UserAgent) != "" {
			req.Header.Set("User-Agent", strings.TrimSpace(fingerprint.UserAgent))
		}
	}
	// Keep identity headers consistent with the final User-Agent after applying the cached fingerprint.
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

func (s *OAuthUsageTransport) doOpenAI(request *http.Request, value *provider.Record, proxy string) (*http.Response, error) {
	if s != nil && s.Transport != nil {
		request = request.WithContext(upstream.WithHTTPUpstreamProfile(request.Context(), upstream.HTTPUpstreamProfileOpenAI))
		return s.Transport.DoWithTLS(request, proxy, value.ID, value.Concurrency, s.profile(value))
	}
	client, err := httpclient.GetClient(httpclient.Options{ProxyURL: proxy, Timeout: 15 * time.Second, ResponseHeaderTimeout: 10 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("build openai probe client: %w", err)
	}
	return client.Do(request)
}

// ExtractOpenAIUsageUpdates accepts quota headers even on error responses before checking status.
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
	if s.Fingerprints != nil {
		if fingerprint, err := s.Fingerprints.GetFingerprint(ctx, value.ID); err == nil && fingerprint != nil {
			options.Fingerprint = fingerprint
		}
	}
	return client.FetchUsageWithOptions(ctx, options)
}
