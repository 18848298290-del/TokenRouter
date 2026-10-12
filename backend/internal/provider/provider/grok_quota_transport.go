package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Grok quota queries validate provider endpoints before using billing, Responses
// or model-list paths. Official OAuth addresses use the trusted validator, while
// custom addresses and API-key providers follow operator validation. The adapter
// then applies provider headers and shared transport parameters to platform
// clients that own response reading and closing.

// GrokQuotaTransport performs upstream queries; the provider service owns
// claims, snapshots, health
// writes, and background tasks.
type GrokQuotaTransport struct {
	// Do is required for billing and active probes. Model fetches return an empty
	// observation when it is nil, allowing optional catalogue work to be skipped.
	Do func(*http.Request, string, int64, int) (*http.Response, error)
	// Proxy optionally loads provider proxy records. ResolveProxy attaches a
	// successful
	// result to the record, while FetchModels resolves independently for its
	// request.
	Proxy func(context.Context, int64) (*egress.Proxy, error)
	// DefaultBaseURL supplies dynamic text defaults when a caller enables that
	// selection. Billing uses platform defaults; active/model probes request
	// the dynamic value.
	DefaultBaseURL func(context.Context) string
	// OperatorValidator validates custom and API-key endpoints. Nil selects the
	// platform base validator, and returned validation errors pass through
	// redacted diagnostics.
	OperatorValidator grok.BaseURLValidator
	// MapStatus translates rejected billing and active statuses for platform
	// errors.
	// Configure it for those error branches after endpoint validation has
	// succeeded.
	MapStatus func(int) int
}

// GrokBaseURLValidator selects the endpoint policy for an OAuth or API-key
// provider.
// Nil, non-Grok and unsupported provider kinds return an error. OAuth trusts
// recognized official endpoints before applying operator policy to custom input;
// API keys always use operator policy. Nil operator selects the platform
// validator, and the returned wrapper redacts validation errors.
func GrokBaseURLValidator(value *provider.Record, operator grok.BaseURLValidator) (grok.BaseURLValidator, error) {
	if value == nil || !value.IsGrok() {
		return nil, fmt.Errorf("grok provider is required")
	}
	if operator == nil {
		operator = grok.ValidateBaseURL
	}
	switch value.Type {
	case capability.ProviderTypeOAuth:
		return grok.RedactedBaseURLValidator(func(raw string) (string, error) {
			if grok.IsOfficialBaseURL(raw) {
				return grok.ValidateTrustedBaseURL(raw)
			}
			return operator(raw)
		}), nil
	case capability.ProviderTypeAPIKey:
		return grok.RedactedBaseURLValidator(operator), nil
	default:
		return nil, fmt.Errorf("unsupported grok provider type: %s", value.Type)
	}
}

// baseURL resolves saved provider input with an optional dynamic text default.
// DefaultBaseURL is read only when dynamic is true and the callback exists.
// The platform resolver supplies kind defaults and normalizes OAuth input; a
// nonnil provider is required.
func (s *GrokQuotaTransport) baseURL(ctx context.Context, value *provider.Record, dynamic bool) string {
	fallback := ""
	if dynamic && s.DefaultBaseURL != nil {
		fallback = s.DefaultBaseURL(ctx)
	}
	return grok.ResolveProviderBaseURL(value.IsGrokOAuth(), value.GetCredential("base_url"), fallback)
}

// applyGrokQuotaHeaders applies enabled provider overrides to the supplied
// request headers.
// It delegates protected-header filtering and wire casing to
// ApplyProviderHeaderOverrides.
// Nil headers are ignored by that adapter, so no additional header map is
// allocated.
func applyGrokQuotaHeaders(value *provider.Record, headers http.Header) {
	ApplyProviderHeaderOverrides(value, headers)
}

// ResolveProxy returns a loaded proxy URL or attaches a successful storage lookup.
// Nil record, missing ID, absent lookup callback, lookup failure and nil lookup
// result return empty. A successful lookup mutates the borrowed record, so callers
// serialize proxy hydration before sharing it.
func (s *GrokQuotaTransport) ResolveProxy(ctx context.Context, value *provider.Record) string {
	if value == nil || value.ProxyID == nil {
		return ""
	}
	if value.Proxy != nil {
		return value.Proxy.URL()
	}
	// Failed proxy hydration returns the empty route selection used by callers.
	// Attach only a successful lookup so a failed read cannot replace a loaded
	// identity.
	if s.Proxy != nil {
		if proxy, err := s.Proxy(ctx, *value.ProxyID); err == nil && proxy != nil {
			value.Proxy = proxy
			return proxy.URL()
		}
	}
	return ""
}

// FetchBilling queries the selected weekly or monthly billing endpoint.
// The URL uses saved provider input with platform defaults and endpoint validation.
// It passes a transport concurrency value of at least two, with two attempts
// and a 100ms
// retry delay for platform retryable failures. A 429 returns its status without
// a billing error, and the platform closes each response body.
func (s *GrokQuotaTransport) FetchBilling(ctx context.Context, value *provider.Record, token, proxy string, weekly bool) (*grok.BillingSummary, int, error) {
	validator, err := GrokBaseURLValidator(value, s.OperatorValidator)
	var target string
	if err == nil {
		target, err = grok.BuildBillingEndpointURL(s.baseURL(ctx, value, false), weekly, validator)
	}
	if err != nil {
		return nil, 0, apperror.Newf(http.StatusBadRequest, "GROK_QUOTA_BASE_URL_INVALID", "invalid Grok base_url: %v", err)
	}
	return grok.FetchBilling(ctx, grok.BillingFetchOptions{
		URL: target, Token: token, ProviderID: value.ID, Weekly: weekly, MaxAttempts: 2, RetryDelay: 100 * time.Millisecond,
		Do: func(request *http.Request) (*http.Response, error) {
			return s.Do(request, proxy, value.ID, max(value.Concurrency, 2))
		},
		ApplyHeaders: func(headers http.Header) { applyGrokQuotaHeaders(value, headers) }, Truncate: openai.Truncate, MapStatus: s.MapStatus, Warn: slog.Warn,
	})
}

// GrokQuotaProbeBody encodes a streaming Responses request with a minimal prompt.
// It trims the supplied model and uses DefaultResponsesModel when blank.
// The returned JSON carries the selected model, input and stream flag for the
// active quota header query.
func GrokQuotaProbeBody(model string) ([]byte, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = grok.DefaultResponsesModel
	}
	return json.Marshal(map[string]any{"model": model, "input": "hi", "stream": true})
}

// ActiveQuota probes Responses headers under the platform's twenty-second
// request budget.
// Dynamic endpoint defaults are read during construction using Background.
// The observe callback is required and receives parsed snapshot/status before
// status handling; 429 is an observed quota result rather than an error.
// OAuth CLI headers precede provider overrides, and the platform closes the
// response.
func (s *GrokQuotaTransport) ActiveQuota(ctx context.Context, value *provider.Record, token, proxy, model string, observe func(*grok.QuotaSnapshot, int)) error {
	body, err := GrokQuotaProbeBody(model)
	if err != nil {
		return apperror.Newf(http.StatusBadRequest, "GROK_QUOTA_PROBE_BODY_ERROR", "failed to build probe body: %v", err)
	}
	validator, err := GrokBaseURLValidator(value, s.OperatorValidator)
	var target string
	if err == nil {
		// Dynamic endpoint selection is a construction-time settings read. Use
		// Background
		// for that lookup while the actual header probe retains the caller
		// cancellation budget.
		target, err = grok.BuildResponsesURLWithValidator(s.baseURL(context.Background(), value, true), validator)
	}
	if err != nil {
		return apperror.Newf(http.StatusBadRequest, "GROK_QUOTA_BASE_URL_INVALID", "invalid Grok base_url: %v", err)
	}
	return grok.FetchActiveQuota(ctx, grok.ActiveQuotaOptions{
		URL: target, Body: body, Token: token, ProviderID: value.ID, Model: model, Timeout: 20 * time.Second,
		ApplyHeaders: func(headers http.Header) {
			if value.IsGrokOAuth() {
				grok.ApplyCLIHeaders(headers)
			}
			applyGrokQuotaHeaders(value, headers)
		},
		Do: func(request *http.Request) (*http.Response, error) {
			return s.Do(request, proxy, value.ID, max(value.Concurrency, 1))
		},
		Observe: observe, MapStatus: s.MapStatus, Warn: slog.Warn,
	})
}

// FetchModels reads observed model IDs from the validated dynamic text endpoint.
// It resolves proxy storage independently rather than using a loaded record proxy.
// Lookup failures use an empty proxy URL, and a nil Do produces an empty
// observation. Platform status failures also return no model IDs; its response
// reader caps the body and closes it.
func (s *GrokQuotaTransport) FetchModels(ctx context.Context, value *provider.Record, token string) ([]string, error) {
	base := strings.TrimSpace(s.baseURL(ctx, value, true))
	if base == "" {
		base = grok.DefaultCLIBaseURL
	}
	validator, err := GrokBaseURLValidator(value, s.OperatorValidator)
	if err != nil {
		return nil, err
	}
	base, err = validator(base)
	if err != nil {
		return nil, err
	}
	return grok.FetchObservedModels(ctx, grok.ModelsRequest{
		URL: httpclient.BuildOpenAIEndpointURL(base, "/v1/models"), Token: token, UserID: value.GetCredential("sub"), Email: value.GetCredential("email"), OAuth: value.IsGrokOAuth(),
		ApplyOverrides: func(headers http.Header) { applyGrokQuotaHeaders(value, headers) },
		Do: func(request *http.Request) (*http.Response, error) {
			proxyURL := ""
			// Model observation is best effort. A failed proxy lookup leaves
			// this request
			// on the empty proxy selection rather than converting that lookup
			// into query failure.
			if s.Proxy != nil && value.ProxyID != nil {
				if proxy, readErr := s.Proxy(ctx, *value.ProxyID); readErr == nil && proxy != nil {
					proxyURL = proxy.URL()
				}
			}
			// Optional catalogue transport can be absent during local option
			// construction.
			// Return no response so the platform model reader yields an empty
			// observation.
			if s.Do == nil {
				return nil, nil
			}
			return s.Do(request, proxyURL, value.ID, value.Concurrency)
		},
	})
}
