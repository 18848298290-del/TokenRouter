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

// openai_quota.go constructs quota clients with bearer or Agent Identity
// authentication and provider transport settings. Quota operations share
// invitation-specific User-Agent and TLS choices. The factory reads the
// selected proxy once and retains the provider record for account headers,
// transport identity, and task recovery callbacks.
//
// Agent Identity authentication returns both the assertion and the task that
// signed it, allowing the upstream quota client to recover that same task
// through the shared coordinator. Unpersisted records use this factory's
// fallback mutex, and response redaction can resolve the credential owner
// through TaskOptions.Read. The provider quota service owns snapshot
// persistence and recovery decisions after the client reports its
// observations.

// OpenAIQuotaFactory binds provider transport and task coordination to upstream
// quota clients.
type OpenAIQuotaFactory struct {
	// Proxy resolves the selected ProxyID. Client requires this callback when that
	// ID exists and propagates lookup errors.
	Proxy func(context.Context, int64) (*egress.Proxy, error)
	// Transport sends provider-aware quota requests. Configure it before executing
	// the client, because its sender callback directly invokes DoWithTLS.
	Transport QoderTransport
	// Profiles optionally resolves invitation and request fingerprints. A nil
	// service leaves quota requests with no selected TLS profile.
	Profiles *egressprovider.TLSProfiles
	// Routers optionally supplies invitation-specific identity settings. Missing
	// and disabled routers leave the default User-Agent and provider-profile
	// selection in effect.
	Routers OpenAITokenRouterReader
	// Tasks and TaskOptions prepare and recover Agent Identity tasks. ensureTask
	// copies the options and installs fallback for unpersisted records before
	// invoking the coordinator.
	Tasks       *provider.OpenAITaskCoordinator // shared task registration coordinator
	TaskOptions provider.OpenAITaskOptions      // task and credential lookup dependencies
	fallback    sync.Mutex                      // unpersisted task registration lock
}

// ensureTask delegates task preparation or recovery to the shared coordinator
// with copied options. The factory's fallback mutex serializes unpersisted
// provider task registration, and taskID identifies the task recovery expects
// to replace.
func (f *OpenAIQuotaFactory) ensureTask(ctx context.Context, value *provider.Record, taskID string) error {
	options := f.TaskOptions
	options.FallbackMutex = &f.fallback
	return f.Tasks.Ensure(ctx, options, value, taskID)
}

// Client builds an OpenAI quota client for a nonnil record and supplied bearer
// token. Configured proxy lookup errors abort construction; enabled routers
// select invitation-specific User-Agent and TLS settings with provider-profile
// fallback.
//
// Authentication callbacks sign Agent Identity tasks when needed and return the
// signing task ID to the upstream client's recovery path. Header, sender,
// redaction, and recovery callbacks retain the provider record, so keep it
// stable during operations. Transport is required for requests, while the
// shared coordinator owns task claims and persistence.
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
	// Bound logged upstream details to a 240-byte prefix after the quota client's
	// redaction callback, retaining the status and provider identity for
	// diagnostics.
	options.Failure = func(status int, message string) {
		slog.Warn("openai_quota_upstream_failed", "provider_id", value.ID, "status", status, "body", openai.Truncate(message, 240))
	}
	return &openai.QuotaClient{Options: options}, nil
}
