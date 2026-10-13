package provider

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// TestClassifyOpenAIAPIKeyHealthFailureExclusions compares a
// provider-attributed 502 with shared, request-scoped, authentication, and
// same-provider retry failures. Only the ordinary provider failure qualifies
// for the API Key health classification.
func TestClassifyOpenAIAPIKeyHealthFailureExclusions(t *testing.T) {
	tests := []struct {
		name     string // health classification case label
		err      error  // forwarding failure under evaluation
		eligible bool   // expected provider health eligibility
	}{
		{name: "provider attributed 502", err: &forwardcore.UpstreamFailoverError{StatusCode: http.StatusBadGateway}, eligible: true},
		{name: "request scoped capacity", err: &forwardcore.UpstreamFailoverError{StatusCode: 529, RequestScopedTransient: true}},
		{name: "provider scoped overload", err: &forwardcore.UpstreamFailoverError{StatusCode: 529, Scope: forwardcore.GatewayFailureScopeShared}},
		{name: "dedicated same provider retry", err: &forwardcore.UpstreamFailoverError{StatusCode: http.StatusTooManyRequests, RetryableOnSameProvider: true}},
		{name: "credential disable path", err: &forwardcore.UpstreamFailoverError{StatusCode: http.StatusUnauthorized, Stage: forwardcore.GatewayFailureStageProviderAuth, Scope: forwardcore.GatewayFailureScopeProvider}},
		{name: "client request", err: &forwardcore.UpstreamFailoverError{StatusCode: http.StatusBadRequest}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, eligible := ClassifyOpenAIAPIKeyHealthFailure(tt.err)
			require.Equal(t, tt.eligible, eligible)
		})
	}
}

// TestOpenAI429RetryDelayHonorsBoundedRetryAfter evaluates an absent
// Retry-After header and a value exceeding the retry cap. Delay uses the
// default for absence and the maximum for the oversized value.
func TestOpenAI429RetryDelayHonorsBoundedRetryAfter(t *testing.T) {
	deadline := time.Now().Add(providercore.RuntimeRetryWindow)
	require.Equal(t, openAIOAuth429RetryDelay, OpenAI429RetryDelay(nil, deadline))
	require.Equal(t, openAIOAuth429MaxRetryDelay, OpenAI429RetryDelay(http.Header{"Retry-After": []string{"90"}}, deadline))
}
