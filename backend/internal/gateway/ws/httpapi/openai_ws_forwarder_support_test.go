package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestOpenAIWSStandaloneFailedStructured403AppliesProviderSideEffectsOnce
// supplies a nested credential rejection in response.failed. The helper
// reports applied effects, writes the provider error once, and marks that
// provider blocked.
func TestOpenAIWSStandaloneFailedStructured403AppliesProviderSideEffectsOnce(t *testing.T) {
	repo := &openAIStream403ProviderRepo{}
	svc := newWSFixture(wsFixtureInputs{health: newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 923, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	failed := []byte(`{"type":"response.failed","response":{"error":{"type":"permission_error","code":"invalid_api_key","status_code":403,"message":"credential rejected"}}}`)

	require.True(t, svc.handleOpenAIWSFailureProviderSideEffects(context.Background(), provider, "gpt-5", nil, failed))
	require.Equal(t, 1, repo.setErrorCalls)
	require.True(t, wsFixtureProviderBlocked(svc, provider))
}

// TestOpenAIWSPairedStructured403SideEffectsCanBeDeduplicated processes a
// workspace-suspended error followed by its matching failed event. The
// caller's applied flag skips the second helper call, leaving one recorded
// provider error.
func TestOpenAIWSPairedStructured403SideEffectsCanBeDeduplicated(t *testing.T) {
	repo := &openAIStream403ProviderRepo{}
	svc := newWSFixture(wsFixtureInputs{health: newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 924, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	errorEvent := []byte(`{"type":"error","error":{"code":"workspace_suspended","status_code":403,"message":"workspace is suspended"}}`)
	failedEvent := []byte(`{"type":"response.failed","response":{"error":{"code":"workspace_suspended","status_code":403,"message":"workspace is suspended"}}}`)

	applied := svc.handleOpenAIWSFailureProviderSideEffects(context.Background(), provider, "gpt-5", nil, errorEvent)
	if !applied {
		applied = svc.handleOpenAIWSFailureProviderSideEffects(context.Background(), provider, "gpt-5", nil, failedEvent)
	}

	require.True(t, applied)
	require.Equal(t, 1, repo.setErrorCalls)
}

// TestMarkOpenAIWSClientVisibleFailure_ResponseFailedNestedError marks a
// nested context-length rejection selected for client delivery. Ops retains
// the supplied 400 status, type, code and message and marks the failure as
// counting toward SLA.
func TestMarkOpenAIWSClientVisibleFailure_ResponseFailedNestedError(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	markOpenAIWSClientVisibleFailure(c, "response.failed", []byte(`{"type":"response.failed","response":{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"too long","status_code":400}}}`))

	got, ok := gatewayhttp.GetOpsStreamError(c)
	require.True(t, ok)
	require.True(t, got.CountTowardsSLA)
	require.Equal(t, http.StatusBadRequest, got.IntendedStatus)
	require.Equal(t, "invalid_request_error", got.ErrType)
	require.Equal(t, "context_length_exceeded", got.Code)
	require.Equal(t, "too long", got.Message)
}

// TestMarkOpenAIWSClientVisibleFailure_ErrorAndSuccessBoundary compares a
// standalone rate-limit error with a completed event. The error receives a
// derived 429 Ops status; completion leaves the Ops stream-error observation
// absent.
func TestMarkOpenAIWSClientVisibleFailure_ErrorAndSuccessBoundary(t *testing.T) {
	t.Run("error", func(t *testing.T) {
		c, _ := gin.CreateTestContext(nil)
		markOpenAIWSClientVisibleFailure(c, "error", []byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"slow down"}}`))
		got, ok := gatewayhttp.GetOpsStreamError(c)
		require.True(t, ok)
		require.Equal(t, http.StatusTooManyRequests, got.IntendedStatus)
	})

	t.Run("completed does not mark", func(t *testing.T) {
		c, _ := gin.CreateTestContext(nil)
		markOpenAIWSClientVisibleFailure(c, "response.completed", []byte(`{"type":"response.completed"}`))
		_, ok := gatewayhttp.GetOpsStreamError(c)
		require.False(t, ok)
	})
}
