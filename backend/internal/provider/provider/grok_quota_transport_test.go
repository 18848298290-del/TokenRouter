package provider

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// Grok billing retries apply to GET queries, while active Responses quota POSTs
// are handled separately. These cases exercise grok_quota_transport.go with
// official and custom endpoint policies, model-list identity headers, retryable
// transport/status failures and a bounded billing retry count.

// TestBuildGrokBillingURLUsesCLIForOfficialAPIHosts supplies public and regional
// official OAuth bases. It checks weekly billing resolution returns the CLI
// billing endpoint for both selected official text addresses.
func TestBuildGrokBillingURLUsesCLIForOfficialAPIHosts(t *testing.T) {
	for _, baseURL := range []string{xai.DefaultBaseURL, "https://us-west-2.api.x.ai/v1"} {
		value := &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"base_url": baseURL}}

		weekly, err := billingURLForTest(value, (egress.OperatorURLPolicy{}).Validate, true)
		require.NoError(t, err)
		require.Equal(t, xai.DefaultCLIBaseURL+xai.BillingWeeklyPath, weekly)
	}
}

// TestBuildGrokBillingURLKeepsCustomRelay supplies a custom OAuth prefix.
// It checks monthly billing appends its endpoint beneath that configured prefix
// after provider-specific URL validation succeeds.
func TestBuildGrokBillingURLKeepsCustomRelay(t *testing.T) {
	value := &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{
		"base_url": "https://relay.example.test/xai/v1",
	}}

	monthly, err := billingURLForTest(value, (egress.OperatorURLPolicy{}).Validate, false)
	require.NoError(t, err)
	require.Equal(t, "https://relay.example.test/xai/v1"+xai.BillingMonthlyPath, monthly)
}

// TestGrokBillingURLFollowsProviderBaseURL compares OAuth defaults, custom
// billing routing and a rejecting operator allowlist. It checks weekly/monthly
// paths and rejection before an address outside policy can carry credentials.
func TestGrokBillingURLFollowsProviderBaseURL(t *testing.T) {
	t.Run("oauth default stays on CLI gateway", func(t *testing.T) {
		value := &providercore.Record{
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeOAuth,
			Credentials: map[string]any{},
		}

		weeklyURL, err := billingURLForTest(value, nil, true)
		require.NoError(t, err)
		require.Equal(t, xai.DefaultCLIBaseURL+"/billing?format=credits", weeklyURL)

		monthlyURL, err := billingURLForTest(value, nil, false)
		require.NoError(t, err)
		require.Equal(t, xai.DefaultCLIBaseURL+"/billing", monthlyURL)
	})

	t.Run("oauth custom forwarding address carries billing probes", func(t *testing.T) {
		value := &providercore.Record{
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"base_url": "https://relay.example.test/v1",
			},
		}

		weeklyURL, err := billingURLForTest(value, nil, true)
		require.NoError(t, err)
		require.Equal(t, "https://relay.example.test/v1/billing?format=credits", weeklyURL)
	})

	t.Run("billing probe honors the operator allowlist like forwarding", func(t *testing.T) {
		// A selected custom relay still passes the operator host allowlist before
		// billing request construction, so rejection is observable without
		// transmitting the OAuth token.
		value := &providercore.Record{
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"base_url": "https://relay.example.test/v1",
			},
		}
		policy := egress.OperatorURLPolicy{Enabled: true, UpstreamHosts: []string{"cli-chat-proxy.grok.com"}}

		_, err := billingURLForTest(value, policy.Validate, true)
		require.EqualError(t, err, "invalid base url: base URL rejected by URL security policy")
	})
}

// TestSyncGrokObservedModelsRejectsOAuthCustomURLOutsideOperatorPolicy supplies
// an OAuth relay outside the allowed host set. It checks the policy error and
// that the transport never receives a model-list request.
func TestSyncGrokObservedModelsRejectsOAuthCustomURLOutsideOperatorPolicy(t *testing.T) {
	provider := &providercore.Record{
		ID:       901,
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "secret-token",
			"base_url":     "https://blocked.example.test/v1",
		},
	}
	repo := &grokQuotaProviderRepo{grokQuotaReadStore: &grokQuotaReadStore{
		providersByID: map[int64]*providercore.Record{provider.ID: provider},
	}}
	upstream := &grokQuotaHTTPRecorder{}
	policy := egress.OperatorURLPolicy{Enabled: true, UpstreamHosts: []string{"allowed.example.test"}}
	requests := &GrokQuotaTransport{Do: upstream.Do, OperatorValidator: policy.Validate}

	err := providercore.SyncGrokObservedModels(context.Background(), providercore.GrokModelsOptions{Fetch: requests.FetchModels, UpdateExtra: repo.UpdateExtra}, provider)
	require.ErrorContains(t, err, "base URL rejected by URL security policy")
	require.Nil(t, upstream.lastReq)
}

// TestSyncGrokObservedModelsUsesCLIIdentityAndProviderHeaders synchronizes models
// with stored OAuth user/email hints. It checks CLI identity headers, the models
// URL and persistence of the observed model metadata.
func TestSyncGrokObservedModelsUsesCLIIdentityAndProviderHeaders(t *testing.T) {
	provider := &providercore.Record{
		ID:       902,
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "secret-token",
			"sub":          "user-902",
			"email":        "user902@example.test",
		},
	}
	repo := &grokQuotaProviderRepo{grokQuotaReadStore: &grokQuotaReadStore{
		providersByID: map[int64]*providercore.Record{provider.ID: provider},
	}}
	upstream := &grokQuotaHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"grok-4.5"}]}`)),
	}}
	requests := &GrokQuotaTransport{Do: upstream.Do}

	require.NoError(t, providercore.SyncGrokObservedModels(context.Background(), providercore.GrokModelsOptions{Fetch: requests.FetchModels, UpdateExtra: repo.UpdateExtra}, provider))
	require.Equal(t, xai.DefaultCLIBaseURL+"/models", upstream.lastReq.URL.String())
	require.Equal(t, xai.CLIClientVersion, upstream.lastReq.Header.Get("x-grok-client-version"))
	require.Equal(t, xai.CLIClientIdentifier, upstream.lastReq.Header.Get("x-grok-client-identifier"))
	require.Equal(t, xai.CLIUserAgent(xai.CLIClientVersion), upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, "interactive", upstream.lastReq.Header.Get("X-Grok-Client-Mode"))
	require.Equal(t, "user-902", upstream.lastReq.Header.Get("X-UserID"))
	require.Equal(t, "user902@example.test", upstream.lastReq.Header.Get("X-Email"))
	require.Contains(t, repo.updates[provider.ID], providercore.GrokObservedModelsExtraKey)
}

// TestPreferBillingObservationStatus compares weekly and monthly failure statuses.
// It checks forbidden precedence across either window, then weekly status
// preference and monthly selection when the weekly status is absent.
func TestPreferBillingObservationStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string // subtest display label
		weeklyStatus  int    // observed weekly HTTP status
		monthlyStatus int    // observed monthly HTTP status
		want          int    // expected selected HTTP status
	}{
		{name: "weekly forbidden wins", weeklyStatus: http.StatusForbidden, monthlyStatus: http.StatusBadGateway, want: http.StatusForbidden},
		{name: "monthly forbidden wins", weeklyStatus: http.StatusBadGateway, monthlyStatus: http.StatusForbidden, want: http.StatusForbidden},
		{name: "weekly observation otherwise wins", weeklyStatus: http.StatusTooManyRequests, monthlyStatus: http.StatusBadGateway, want: http.StatusTooManyRequests},
		{name: "monthly observation is fallback", monthlyStatus: http.StatusBadGateway, want: http.StatusBadGateway},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, (&providercore.GrokQuotaService{Options: providercore.GrokQuotaOptions{MapStatus: forward.MapStatus, Warn: slog.Warn}}).PreferBillingObservationStatus(tt.weeklyStatus, tt.monthlyStatus))
		})
	}
}

// TestGrokQuotaServiceFetchBillingRetries502ThenSucceeds returns one 502 before
// a successful weekly billing payload. It checks parsed utilization and exactly
// two GET requests to the weekly billing URL.
func TestGrokQuotaServiceFetchBillingRetries502ThenSucceeds(t *testing.T) {
	provider := healthyGrokQuotaOAuthProvider(401)
	upstream := &grokQuotaSequenceUpstream{steps: []grokQuotaUpstreamStep{
		{status: http.StatusBadGateway, body: `The origin web server returned an invalid or incomplete response to Cloudflare.`},
		{status: http.StatusOK, body: `{"config":{"currentPeriod":{"type":"WEEKLY","start":"2026-07-09T03:25:00Z","end":"2026-07-16T03:25:00Z"},"creditUsagePercent":12}}`},
	}}
	svc := &GrokQuotaTransport{Do: upstream.Do, MapStatus: forward.MapStatus}

	summary, status, err := svc.FetchBilling(context.Background(), provider, "access-token", "", true)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.NotNil(t, summary)
	require.NotNil(t, summary.UsagePercent)
	require.Equal(t, 12.0, *summary.UsagePercent)
	requests := upstream.snapshotRequests()
	require.Len(t, requests, 2)
	require.Equal(t, http.MethodGet, requests[0].Method)
	require.Equal(t, "/v1/billing", requests[0].URL.Path)
	require.Equal(t, "format=credits", requests[0].URL.RawQuery)
}

// TestGrokQuotaServiceFetchBillingRetriesTransportErrorThenSucceeds injects an
// initial transport failure before billing success. It checks the final summary
// and status, with exactly two recorded query attempts.
func TestGrokQuotaServiceFetchBillingRetriesTransportErrorThenSucceeds(t *testing.T) {
	provider := healthyGrokQuotaOAuthProvider(402)
	upstream := &grokQuotaSequenceUpstream{steps: []grokQuotaUpstreamStep{
		{err: errors.New("temporary transport failure")},
		{status: http.StatusOK, body: `{"config":{"currentPeriod":{"type":"WEEKLY"},"creditUsagePercent":8}}`},
	}}
	svc := &GrokQuotaTransport{Do: upstream.Do, MapStatus: forward.MapStatus}

	summary, status, err := svc.FetchBilling(context.Background(), provider, "access-token", "", true)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.NotNil(t, summary)
	require.Len(t, upstream.snapshotRequests(), 2)
}

// TestGrokQuotaServiceFetchBillingStopsAfterSingleTransientRetry supplies two
// 502 responses followed by unused success. It checks the stable error reason,
// status diagnostic and two-request limit after transient retry exhaustion.
func TestGrokQuotaServiceFetchBillingStopsAfterSingleTransientRetry(t *testing.T) {
	provider := healthyGrokQuotaOAuthProvider(403)
	upstream := &grokQuotaSequenceUpstream{steps: []grokQuotaUpstreamStep{
		{status: http.StatusBadGateway, body: `cloudflare failure`},
		{status: http.StatusBadGateway, body: `cloudflare failure`},
		{status: http.StatusOK, body: `{"config":{"currentPeriod":{"type":"WEEKLY"}}}`},
	}}
	svc := &GrokQuotaTransport{Do: upstream.Do, MapStatus: forward.MapStatus}

	summary, status, err := svc.FetchBilling(context.Background(), provider, "access-token", "", true)

	require.Error(t, err)
	require.Nil(t, summary)
	require.Equal(t, http.StatusBadGateway, status)
	require.Equal(t, "GROK_QUOTA_PROBE_UPSTREAM_ERROR", apperror.Reason(err))
	require.Contains(t, apperror.Message(err), "billing returned 502: cloudflare failure")
	require.Len(t, upstream.snapshotRequests(), 2)
}

// TestGrokQuotaServiceFetchBillingDoesNotRetryNonTransientStatuses supplies 401,
// 403 and 429 billing outcomes. It checks one request for each, errors for
// authentication/access rejection, and a status-only 429 result.
func TestGrokQuotaServiceFetchBillingDoesNotRetryNonTransientStatuses(t *testing.T) {
	tests := []struct {
		name    string // subtest display label
		status  int    // returned upstream HTTP status
		wantErr bool   // expected billing error presence
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, wantErr: true},
		{name: "forbidden", status: http.StatusForbidden, wantErr: true},
		{name: "rate limited", status: http.StatusTooManyRequests, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := healthyGrokQuotaOAuthProvider(404)
			upstream := &grokQuotaSequenceUpstream{steps: []grokQuotaUpstreamStep{
				{status: tt.status, body: `{"error":{"message":"rejected"}}`},
				{status: http.StatusOK, body: `{"config":{"currentPeriod":{"type":"WEEKLY"}}}`},
			}}
			svc := &GrokQuotaTransport{Do: upstream.Do, MapStatus: forward.MapStatus}

			summary, status, err := svc.FetchBilling(context.Background(), provider, "access-token", "", true)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Nil(t, summary)
			require.Equal(t, tt.status, status)
			require.Len(t, upstream.snapshotRequests(), 1)
		})
	}
}

// billingURLForTest combines provider validation with weekly or monthly
// endpoint resolution.
// It returns validator errors before building the endpoint. The helper uses
// platform defaults rather than the dynamic text setting for its billing base.
func billingURLForTest(value *providercore.Record, operator xai.BaseURLValidator, weekly bool) (string, error) {
	validator, err := GrokBaseURLValidator(value, operator)
	if err != nil {
		return "", err
	}
	requests := &GrokQuotaTransport{OperatorValidator: operator}
	return xai.BuildBillingEndpointURL(requests.baseURL(context.Background(), value, false), weekly, validator)
}
