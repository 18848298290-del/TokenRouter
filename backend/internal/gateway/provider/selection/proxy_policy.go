package selection

import (
	"context"
	"time"

	"go.uber.org/zap"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

const

// Limit repeated fail-open warnings during a proxy outage.
openAIProxyStreamFailOpenLogInterval = 5 * time.Second

func openAIProxyStreamCircuitProxyID(provider *gatewayprovider.ExecutionProvider) (int64, bool) {
	if provider == nil || provider.Record.Platform != capability.PlatformOpenAI || provider.Record.ProxyID == nil || *provider.Record.ProxyID <= 0 {
		return 0, false
	}
	return *provider.Record.ProxyID, true
}

func withOpenAIProxyStreamQuarantineBypass(ctx context.Context) context.Context {
	// A nil parent panics; a derived attempt receives its own execution-hints value.
	if ctx == nil {
		panic("cannot create context from nil parent")
	}
	hints := requeststate.ExecutionHintsFromContext(ctx)
	hints.ProxyQuarantineBypass = true
	return requeststate.WithExecutionHints(ctx, hints)
}

func openAIProxyStreamQuarantineBypassed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	return requeststate.ExecutionHintsFromContext(ctx).ProxyQuarantineBypass
}

func (s *Compatible) isOpenAIProxyStreamQuarantined(ctx context.Context, provider *gatewayprovider.ExecutionProvider) bool {
	proxyID, ok := openAIProxyStreamCircuitProxyID(provider)
	if !ok {
		return false
	}
	if openAIProxyStreamQuarantineBypassed(ctx) {
		return false
	}
	circuit := s.getOpenAIProxyStreamCircuit()
	return circuit != nil && circuit.IsBlocked(proxyID, time.Now())
}

// logOpenAIProxyStreamQuarantineFailOpen rate-limits warnings per selector when quarantine is bypassed.
func (s *Compatible) logOpenAIProxyStreamQuarantineFailOpen(requestedModel string, blockedProxies int) {
	now := time.Now().UnixNano()
	last := s.proxyFailOpenLogAt.Load()
	if now-last < int64(openAIProxyStreamFailOpenLogInterval) ||
		!s.proxyFailOpenLogAt.CompareAndSwap(last, now) {
		return
	}
	logging.L().With(zap.String("component", "service.openai_gateway")).Warn(
		"openai.proxy_stream_quarantine_fail_open",
		zap.Int("blocked_proxies", blockedProxies),
		zap.String("model", requestedModel),
	)
}
