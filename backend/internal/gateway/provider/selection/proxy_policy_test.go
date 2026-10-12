package selection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestOpenAIProxyStreamQuarantineBypassContext checks that a request-local bypass leaves circuit state intact.
func TestOpenAIProxyStreamQuarantineBypassContext(t *testing.T) {
	proxyID := int64(7)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, ProxyID: &proxyID}}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{Reads: Reads{}, Shared: Shared{}}, nil)

	svc.proxyCircuit = egress.NewProxyStreamCircuit(egress.ProxyStreamCircuitSettings{
		FailureThreshold: 1,
		FailureWindow:    time.Minute,
		QuarantineTTL:    10 * time.Minute,
		MaxEntries:       16,
	})
	svc.proxyCircuit.RecordFailure(proxyID, time.Now())

	ctx := context.Background()
	require.True(t, svc.isOpenAIProxyStreamQuarantined(ctx, provider))
	require.False(t, svc.isOpenAIProxyStreamQuarantined(withOpenAIProxyStreamQuarantineBypass(ctx), provider))
}
