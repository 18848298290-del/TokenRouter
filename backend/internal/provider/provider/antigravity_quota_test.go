package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// agUsageIdentityTransport returns fixed responses to check cache invalidation when credentials change.
type agUsageIdentityTransport struct{ calls atomic.Int32 }

func TestFetchQuotaUsesConfiguredModelsListBodyLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":{"model-a":{}}}`))
	}))
	defer server.Close()

	oldBaseURLs := append([]string(nil), antigravity.BaseURLs...)
	oldAvailability := antigravity.DefaultURLAvailability
	t.Cleanup(func() {
		antigravity.BaseURLs = oldBaseURLs
		antigravity.DefaultURLAvailability = oldAvailability
	})
	antigravity.BaseURLs = []string{server.URL}
	antigravity.DefaultURLAvailability = antigravity.NewURLAvailability(time.Minute)

	fetcher := &providercore.AntigravityQuota{Options: AntigravityQuotaOptions(8, nil)}
	_, err := fetcher.FetchQuota(context.Background(), &providercore.Record{
		Platform: capability.PlatformAntigravity,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token",
			"project_id":   "project",
		},
	}, "")
	require.ErrorContains(t, err, "response exceeds 8 bytes")
}

func TestFetchQuota_ForbiddenReturnsIsForbidden(t *testing.T) {
	// Check the typed 403 error that quota queries use to classify forbidden responses.
	forbiddenErr := &antigravity.ForbiddenError{
		StatusCode: 403,
		Body:       "Access denied",
	}

	var target *antigravity.ForbiddenError
	require.True(t, errors.As(forbiddenErr, &target))
	require.Equal(t, 403, target.StatusCode)
	require.Equal(t, "Access denied", target.Body)
	require.Contains(t, forbiddenErr.Error(), "403")
}

func TestClassifyForbiddenType(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected string
	}{
		{
			name:     "VALIDATION_REQUIRED keyword",
			body:     `{"error":{"message":"VALIDATION_REQUIRED"}}`,
			expected: "validation",
		},
		{
			name:     "verify your account",
			body:     `Please verify your account to continue`,
			expected: "validation",
		},
		{
			name:     "contains validation_url field",
			body:     `{"error":{"details":[{"metadata":{"validation_url":"https://..."}}]}}`,
			expected: "validation",
		},
		{
			name:     "terms of service violation",
			body:     `Your account has been suspended for Terms of Service violation`,
			expected: "violation",
		},
		{
			name:     "violation keyword",
			body:     `Provider suspended due to policy violation`,
			expected: "violation",
		},
		{
			name:     "generic 403",
			body:     `Access denied`,
			expected: "forbidden",
		},
		{
			name:     "empty body",
			body:     "",
			expected: "forbidden",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := antigravity.ClassifyForbiddenType(tt.body)
			require.Equal(t, tt.expected, got)
		})
	}
}

func TestExtractValidationURL(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected string
	}{
		{
			name:     "structured validation_url",
			body:     `{"error":{"details":[{"metadata":{"validation_url":"https://accounts.google.com/verify?token=abc"}}]}}`,
			expected: "https://accounts.google.com/verify?token=abc",
		},
		{
			name:     "structured appeal_url",
			body:     `{"error":{"details":[{"metadata":{"appeal_url":"https://support.google.com/appeal/123"}}]}}`,
			expected: "https://support.google.com/appeal/123",
		},
		{
			name:     "validation_url takes priority over appeal_url",
			body:     `{"error":{"details":[{"metadata":{"validation_url":"https://v.com","appeal_url":"https://a.com"}}]}}`,
			expected: "https://v.com",
		},
		{
			name:     "fallback regex with verify keyword",
			body:     `Please verify your account at https://accounts.google.com/verify`,
			expected: "https://accounts.google.com/verify",
		},
		{
			name:     "no URL in generic forbidden",
			body:     `Access denied`,
			expected: "",
		},
		{
			name:     "empty body",
			body:     "",
			expected: "",
		},
		{
			name:     "URL present but no validation keywords",
			body:     `Error at https://example.com/something`,
			expected: "",
		},
		{
			name:     "unicode escaped ampersand",
			body:     `validation required: https://accounts.google.com/verify?a=1\u0026b=2`,
			expected: "https://accounts.google.com/verify?a=1&b=2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := antigravity.ExtractValidationURL(tt.body)
			require.Equal(t, tt.expected, got)
		})
	}
}

func TestAntigravityUsageCacheDoesNotCrossCredentialIdentity(t *testing.T) {
	transport := &agUsageIdentityTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = old })
	quota := &providercore.AntigravityQuota{Options: AntigravityQuotaOptions(8*1024*1024, nil)}
	svc := providercore.NewOAuthUsageService(nil, providercore.NewOAuthUsageCache(), nil, providercore.OAuthUsageOptions{Antigravity: providercore.AntigravityUsageOptions{
		CanFetch: quota.CanFetch,
		Fetch: func(ctx context.Context, value *providercore.Record) (*providercore.UsageInfo, error) {
			result, err := quota.FetchQuota(ctx, value, quota.GetProxyURL(ctx, value))
			if result == nil {
				return nil, err
			}
			return result.UsageInfo, err
		},
		Degrade: AntigravityDegradedUsage, Enrich: EnrichUsageWithProviderError,
	}})
	a := &providercore.Record{ID: 885, Platform: capability.PlatformAntigravity, Type: capability.ProviderTypeOAuth, Status: providercore.StatusActive, Credentials: map[string]any{"access_token": "first", "project_id": "fixture"}}
	_, err := svc.GetAntigravityUsage(context.Background(), a)
	require.NoError(t, err)
	require.Equal(t, int32(2), transport.calls.Load())
	a.Credentials = map[string]any{"access_token": "second", "project_id": "fixture"}
	_, err = svc.GetAntigravityUsage(context.Background(), a)
	require.NoError(t, err)
	require.Equal(t, int32(4), transport.calls.Load())
}

func (t *agUsageIdentityTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	body := `{"models":{}}`
	if strings.Contains(r.URL.Path, "loadCodeAssist") {
		body = `{"currentTier":{"id":"FREE"}}`
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}
