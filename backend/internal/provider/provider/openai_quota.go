package provider

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// OpenAIQuotaFactory prepares quota clients and delegates task recovery and persistence to the shared
// coordinator.
type OpenAIQuotaFactory struct {
	Proxy       func(context.Context, int64) (*egress.Proxy, error)
	Transport   QoderTransport
	Profiles    *egressprovider.TLSProfiles
	Routers     OpenAITokenRouterReader
	Tasks       *provider.OpenAITaskCoordinator
	TaskOptions provider.OpenAITaskOptions
	fallback    sync.Mutex
}

func (f *OpenAIQuotaFactory) ensureTask(ctx context.Context, value *provider.Record, taskID string) error {
	options := f.TaskOptions
	options.FallbackMutex = &f.fallback
	return f.Tasks.Ensure(ctx, options, value, taskID)
}

func (f *OpenAIQuotaFactory) Client(ctx context.Context, value *provider.Record, token string) (provider.OpenAIQuotaClient, error) {
	proxyURL := ""
	if value.ProxyID != nil {
		proxy, err := f.Proxy(ctx, *value.ProxyID)
		if err != nil {
			return nil, err
		}
		if proxy != nil {
			proxyURL = proxy.URL()
		}
	}
	var router *egress.TLSFingerprintRouter
	if f.Routers != nil {
		router = f.Routers.GetRuntimeRouter(value.GetTLSFingerprintRouterID())
		if router != nil && !router.Enabled {
			router = nil
		}
	}
	userAgent := openai.CodexInviteDefaultUserAgent
	if router != nil && strings.TrimSpace(router.CodexInviteResetUserAgent) != "" {
		userAgent = strings.TrimSpace(router.CodexInviteResetUserAgent)
	}
	profile := resolveCodexInviteTLS(f.Profiles, value, router)
	options := openai.QuotaClientOptions{Available: value != nil, URL: openai.BuildCodexBackendURL, UserAgent: userAgent}
	options.Authenticate = func(ctx context.Context) (string, string, error) {
		if value.IsOpenAIAgentIdentity() {
			headers, taskID, err := AgentIdentityHeaders(ctx, value, f.ensureTask)
			return headers.Get("Authorization"), taskID, err
		}
		return "Bearer " + token, "", nil
	}
	options.ProviderHeaders = func(headers http.Header) {
		openai.SetChatGPTAccountHeaders(headers, value.GetChatGPTAccountID(), value.IsChatGPTAccountFedRAMP())
	}
	options.Do = func(request *http.Request) (*http.Response, error) {
		return f.Transport.DoWithTLS(request, proxyURL, value.ID, value.Concurrency, profile)
	}
	options.IsAgentIdentity = value.IsOpenAIAgentIdentity
	options.RecoverTask = func(ctx context.Context, taskID string) error {
		return f.ensureTask(ctx, value, taskID)
	}
	options.Redact = func(ctx context.Context, body []byte) []byte {
		return RedactAgentIdentityBody(ctx, f.TaskOptions.Read, value, body)
	}
	options.Failure = func(status int, message string) {
		slog.Warn("openai_quota_upstream_failed", "provider_id", value.ID, "status", status, "body", openai.Truncate(message, 240))
	}
	return &openai.QuotaClient{Options: options}, nil
}
