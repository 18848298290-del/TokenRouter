package provider

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// OpenAI stream disconnects can identify a shared proxy fault, while client
// cancellation and deadline errors provide different evidence. Proxy feedback
// records qualifying failures in the circuit and logs a newly activated
// quarantine; successful streams clear that proxy's retained failure state.

// openAIProxyStreamCircuitProxyID returns a positive proxy ID for an OpenAI
// execution target. Missing targets, other platforms, absent proxy IDs, and
// nonpositive IDs return false so callers skip circuit feedback.
func openAIProxyStreamCircuitProxyID(provider *ExecutionProvider) (int64, bool) {
	if provider == nil || provider.Record.Platform != capability.PlatformOpenAI || provider.Record.ProxyID == nil || *provider.Record.ProxyID <= 0 {
		return 0, false
	}
	return *provider.Record.ProxyID, true
}

// RecordProxyStreamDisconnect records a qualifying OpenAI proxy failure and
// logs newly activated quarantine. Nil errors and context cancellation or
// deadline errors are ignored; the circuit handles its own nil/disabled cases
// and shared state.
func RecordProxyStreamDisconnect(circuit *egress.ProxyStreamCircuit, provider *ExecutionProvider, streamErr error, upstreamRequestID string) {
	proxyID, ok := openAIProxyStreamCircuitProxyID(provider)
	if !ok || streamErr == nil || errors.Is(streamErr, context.Canceled) || errors.Is(streamErr, context.DeadlineExceeded) {
		return
	}

	tripped, until := circuit.RecordFailure(proxyID, time.Now())
	if !tripped {
		return
	}
	logging.L().With(zap.String("component", "service.openai_gateway")).Warn(
		"openai.proxy_quarantined_stream_disconnect",
		zap.Int64("proxy_id", proxyID),
		zap.Int64("provider_id", provider.Record.ID),
		zap.Time("until", until),
		zap.String("upstream_request_id", upstreamRequestID),
		zap.String("error", logredact.SanitizeUpstreamQueries(streamErr.Error())),
	)
}

// ClearProxyStreamDisconnect clears retained failures for an OpenAI target's
// positive proxy ID after success. Targets without that identity and nil
// circuits are ignored, letting output callers share one feedback path.
func ClearProxyStreamDisconnect(circuit *egress.ProxyStreamCircuit, provider *ExecutionProvider) {
	proxyID, ok := openAIProxyStreamCircuitProxyID(provider)
	if !ok {
		return
	}
	if circuit != nil {
		circuit.RecordSuccess(proxyID)
	}
}
