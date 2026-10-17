package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// TestSetOpsUpstreamModelStoresOnlyTrimmedModelSlug supplies a padded model
// then a blank update. It requires trimmed attribution to survive the blank
// setter and an explicit clear to replace it with empty.
func TestSetOpsUpstreamModelStoresOnlyTrimmedModelSlug(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	SetOpsUpstreamModel(c, "  gpt-5.6-sol  ")
	value, ok := c.Get(OpsUpstreamModelKey)
	require.True(t, ok)
	require.Equal(t, "gpt-5.6-sol", value)
	SetOpsUpstreamModel(c, "  ")
	value, ok = c.Get(OpsUpstreamModelKey)
	require.True(t, ok)
	require.Equal(t, "gpt-5.6-sol", value)
	ClearOpsUpstreamModel(c)
	value, ok = c.Get(OpsUpstreamModelKey)
	require.True(t, ok)
	require.Equal(t, "", value)
}

// TestCredentialFailureAfterDispatchRemainsVisible records authentication failure
// after an earlier exchange. Same-provider and replacement-provider failures keep
// unnumbered evidence without borrowing or overwriting the previous HTTP attempt.
func TestCredentialFailureAfterDispatchRemainsVisible(t *testing.T) {
	for _, nextProvider := range []int64{7, 8} {
		var saved telemetry.RequestRecord
		ctx := telemetry.WithRequestCapture(t.Context(), telemetry.RequestRecord{RequestID: "local"}, func(r telemetry.RequestRecord) { saved = r })
		ctx, _ = timing.WithStages(ctx, time.Now(), nil)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
		SetOpsSelectedProvider(c, provider.ProviderSnapshot{ID: 7, Platform: "openai"})
		first := timing.BeginHTTPTrace(c.Request.Context())
		timing.ObserveResponse(first, &http.Response{StatusCode: 503, Header: http.Header{"X-Request-Id": {"first"}}})
		AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{ProviderID: 7, UpstreamStatusCode: 503, Kind: "failover"})
		SetOpsSelectedProvider(c, provider.ProviderSnapshot{ID: nextProvider, Platform: "openai"})
		AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{ProviderID: nextProvider, Stage: "provider_auth", Reason: "refresh_failed", Kind: "credential_failover"})
		require.Len(t, saved.Attempts, 2)
		require.Equal(t, "first", saved.Attempts[0].RequestID)
		require.Equal(t, 503, saved.Attempts[0].Status)
		require.Equal(t, nextProvider, saved.Attempts[1].ProviderID)
		require.Zero(t, saved.Attempts[1].Number)
		require.Equal(t, "provider_auth", saved.Attempts[1].Stage)
		require.Empty(t, saved.Attempts[1].RequestID)
		SetOpsSelectedProvider(c, provider.ProviderSnapshot{ID: 9, Platform: "openai"})
		next := timing.BeginHTTPTrace(c.Request.Context())
		timing.ObserveResponse(next, &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {"next"}}})
		require.Equal(t, 2, saved.Attempts[2].Number)
		require.Equal(t, "next", saved.Attempts[2].RequestID)
	}
}

// TestCredentialHTTPRejectionKeepsDispatchOrdinal distinguishes an observed 401
// from credentials failing before dispatch. The existing HTTP attempt receives
// the authentication failure and keeps its selected request identifier.
func TestCredentialHTTPRejectionKeepsDispatchOrdinal(t *testing.T) {
	var saved telemetry.RequestRecord
	ctx := telemetry.WithRequestCapture(t.Context(), telemetry.RequestRecord{RequestID: "local"}, func(r telemetry.RequestRecord) { saved = r })
	ctx, _ = timing.WithStages(ctx, time.Now(), nil)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	SetOpsSelectedProvider(c, provider.ProviderSnapshot{ID: 7, Platform: "antigravity"})
	exchange := timing.BeginHTTPTrace(c.Request.Context())
	timing.ObserveResponse(exchange, &http.Response{StatusCode: 401, Header: http.Header{"X-Request-Id": {"rejected"}}})
	AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{ProviderID: 7, UpstreamStatusCode: 401, Stage: "provider_auth", Kind: "failover"})
	require.Len(t, saved.Attempts, 1)
	require.Equal(t, 1, saved.Attempts[0].Number)
	require.Equal(t, "failed", saved.Attempts[0].Outcome)
	require.Equal(t, "rejected", saved.Attempts[0].RequestID)
}

// TestMediaFailureKeepsDispatchAfterRetry records repeated and delayed failures
// for one or two providers. Fixed response references retain each failure's reason
// and selected ID while a newer successful exchange remains untouched.
func TestMediaFailureKeepsDispatchAfterRetry(t *testing.T) {
	for _, nextProvider := range []int64{7, 8} {
		for _, timed := range []bool{false, true} {
			var saved telemetry.RequestRecord
			ctx := telemetry.WithRequestCapture(t.Context(), telemetry.RequestRecord{RequestID: "local"}, func(r telemetry.RequestRecord) { saved = r })
			if timed {
				ctx, _ = timing.WithStages(ctx, time.Now(), nil)
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil).WithContext(ctx)
			SetOpsSelectedProvider(c, provider.ProviderSnapshot{ID: 7, RequestIDHeader: "X-Custom"})
			first := timing.BeginHTTPTrace(c.Request.Context())
			firstRef := telemetry.RequestAttemptFromContext(first)
			timing.ObserveResponse(first, &http.Response{StatusCode: 503, Header: http.Header{"X-Custom": {"reused"}}})
			AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{ProviderID: 7, UpstreamStatusCode: 503, Reason: "first", Kind: "failover"}, firstRef)
			SetOpsSelectedProvider(c, provider.ProviderSnapshot{ID: nextProvider, RequestIDHeader: "X-Custom"})
			second := timing.BeginHTTPTrace(c.Request.Context())
			secondRef := telemetry.RequestAttemptFromContext(second)
			timing.ObserveResponse(second, &http.Response{StatusCode: 503, Header: http.Header{"X-Custom": {"reused"}}})
			AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{ProviderID: nextProvider, UpstreamStatusCode: 503, Reason: "second", Kind: "failover"}, secondRef)
			third := timing.BeginHTTPTrace(c.Request.Context())
			timing.ObserveResponse(third, &http.Response{StatusCode: 200, Header: http.Header{"X-Custom": {"reused"}}})
			// A delayed failure must resolve its saved reference after replacement
			// dispatch, even when both providers and selected identifier values match.
			AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{ProviderID: 7, UpstreamStatusCode: 502, Reason: "first-late", Kind: "failover"}, firstRef)
			require.Equal(t, "first-late", saved.Attempts[0].Reason)
			require.Equal(t, "second", saved.Attempts[1].Reason)
			require.Equal(t, "failed", saved.Attempts[0].Outcome)
			require.Equal(t, "failed", saved.Attempts[1].Outcome)
			require.Equal(t, "running", saved.Attempts[2].Outcome)
			require.Equal(t, 200, saved.Attempts[2].Status)
			for _, attempt := range saved.Attempts {
				require.Equal(t, "reused", attempt.RequestID)
			}
			wrong := secondRef
			wrong.ProviderID = 999
			AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{ProviderID: nextProvider, UpstreamStatusCode: 500, Reason: "wrong-provider"}, wrong)
			require.Equal(t, "second", saved.Attempts[1].Reason)
		}
	}
}
