package messageforward

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// ErrorForTest runs the runtime error handler with fresh attempt state for
// external tests. The supplied response remains caller-owned, including any
// body rebuilt during health or error inspection.
func ErrorForTest(runtime *Runtime, ctx context.Context, output HTTPBoundary, target *gatewayadapter.ExecutionProvider, response *http.Response, retry bool, models ...string) (*forward.Result, error) {
	return runtime.handleError(ctx, output, &AttemptState{}, target, response, retry, models...)
}

// TestGatewayFailoverSideEffects_BedrockUsesMappedModel observes a Bedrock
// maintenance error with a model-specific temporary rule. Health stops
// scheduling and records the mapped model scope, while provider-wide temporary
// writes stay absent.
func TestGatewayFailoverSideEffects_BedrockUsesMappedModel(t *testing.T) {
	repo := &gatewaytestkit.ErrorPolicyStore{}
	healthObserver := gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: repo, Options: providercore.HealthOptions{}})

	svc := NewRuntime(Dependencies{Health: healthObserver}, Options{})
	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 20503,
			Type:     capability.ProviderTypeBedrock,
			Platform: capability.PlatformAnthropic,
			Credentials: map[string]any{
				"pool_mode":                  true,
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{
					map[string]any{
						"error_code":       float64(http.StatusServiceUnavailable),
						"keywords":         []any{"maintenance"},
						"duration_minutes": float64(30),
					},
				},
			},
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"maintenance"}}`)),
	}

	decision := svc.failoverHealth(context.Background(), resp, provider, "anthropic.claude-mapped")

	require.Equal(t, providercore.ErrorPolicyTempUnscheduled, decision.Policy)
	require.True(t, decision.StopScheduling)
	require.False(t, decision.RetryableOnSameProvider(gatewayadapter.ExecutionErrorPolicy(provider), http.StatusServiceUnavailable))
	require.Len(t, repo.ModelRateLimitCalls, 1)
	require.Equal(t, "anthropic.claude-mapped", repo.ModelRateLimitCalls[0].Scope)
	require.Zero(t, repo.TempCalls)
}
