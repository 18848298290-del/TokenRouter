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

// GrokQuotaTransport performs upstream queries; the provider service owns claims, snapshots, health
// writes, and background tasks.
type GrokQuotaTransport struct {
	Do                func(*http.Request, string, int64, int) (*http.Response, error)
	Proxy             func(context.Context, int64) (*egress.Proxy, error)
	DefaultBaseURL    func(context.Context) string
	OperatorValidator grok.BaseURLValidator
	MapStatus         func(int) int
}

// GrokBaseURLValidator trusts official OAuth endpoints and applies operator policy to custom endpoints.
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

func (s *GrokQuotaTransport) baseURL(ctx context.Context, value *provider.Record, dynamic bool) string {
	fallback := ""
	if dynamic && s.DefaultBaseURL != nil {
		fallback = s.DefaultBaseURL(ctx)
	}
	return grok.ResolveProviderBaseURL(value.IsGrokOAuth(), value.GetCredential("base_url"), fallback)
}

func applyGrokQuotaHeaders(value *provider.Record, headers http.Header) {
	ApplyProviderHeaderOverrides(value, headers)
}

// ResolveProxy uses a loaded proxy first, then reads and attaches it to the record. Lookup failures
// return an empty URL.
func (s *GrokQuotaTransport) ResolveProxy(ctx context.Context, value *provider.Record) string {
	if value == nil || value.ProxyID == nil {
		return ""
	}
	if value.Proxy != nil {
		return value.Proxy.URL()
	}
	if s.Proxy != nil {
		if proxy, err := s.Proxy(ctx, *value.ProxyID); err == nil && proxy != nil {
			value.Proxy = proxy
			return proxy.URL()
		}
	}
	return ""
}

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

// GrokQuotaProbeBody builds a streaming probe, using the default Responses model when model is blank.
func GrokQuotaProbeBody(model string) ([]byte, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = grok.DefaultResponsesModel
	}
	return json.Marshal(map[string]any{"model": model, "input": "hi", "stream": true})
}

func (s *GrokQuotaTransport) ActiveQuota(ctx context.Context, value *provider.Record, token, proxy, model string, observe func(*grok.QuotaSnapshot, int)) error {
	body, err := GrokQuotaProbeBody(model)
	if err != nil {
		return apperror.Newf(http.StatusBadRequest, "GROK_QUOTA_PROBE_BODY_ERROR", "failed to build probe body: %v", err)
	}
	validator, err := GrokBaseURLValidator(value, s.OperatorValidator)
	var target string
	if err == nil {
		// Read dynamic endpoint settings at request construction, independently of request cancellation.
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
			if s.Proxy != nil && value.ProxyID != nil {
				if proxy, readErr := s.Proxy(ctx, *value.ProxyID); readErr == nil && proxy != nil {
					proxyURL = proxy.URL()
				}
			}
			if s.Do == nil {
				return nil, nil
			}
			return s.Do(request, proxyURL, value.ID, value.Concurrency)
		},
	})
}
