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

// Proxy stream quarantine can exclude several OpenAI providers sharing the same
// proxy. A retry may bypass that quarantine through a copied request execution
// hint, while the circuit retains its observed failure state for later
// requests.
//
// Bypass is read from the current context and applies only to that derived
// attempt. Providers without a positive OpenAI proxy ID do not enter this
// circuit check. Warning publication uses one atomic timestamp per selector, so
// concurrent fail-open requests share the log interval and one comparison
// winner emits the warning. This bounds repeated outage messages while keeping
// the request's model and blocked-proxy count visible.

// openAIProxyStreamFailOpenLogInterval bounds repeated outage warnings emitted by
// concurrent requests through the same compatible selector's atomic log timestamp.
const openAIProxyStreamFailOpenLogInterval = 5 * time.Second

// openAIProxyStreamCircuitProxyID returns the positive proxy ID used by an
// OpenAI provider. Missing providers, other platforms, and nil or nonpositive
// IDs return false so those targets skip the stream circuit.
func openAIProxyStreamCircuitProxyID(provider *gatewayprovider.ExecutionProvider) (int64, bool) {
	if provider == nil || provider.Record.Platform != capability.PlatformOpenAI || provider.Record.ProxyID == nil || *provider.Record.ProxyID <= 0 {
		return 0, false
	}
	return *provider.Record.ProxyID, true
}

// withOpenAIProxyStreamQuarantineBypass returns a context with quarantine
// bypass set in its copied execution hints. A nil parent panics; the inherited
// circuit state remains owned by the selector while the derived request holds
// its bypass value.
func withOpenAIProxyStreamQuarantineBypass(ctx context.Context) context.Context {
	if ctx == nil {
		panic("cannot create context from nil parent")
	}
	hints := requeststate.ExecutionHintsFromContext(ctx)
	hints.ProxyQuarantineBypass = true
	return requeststate.WithExecutionHints(ctx, hints)
}

// openAIProxyStreamQuarantineBypassed reports whether this context's execution
// hints enable stream-quarantine bypass. A nil context returns false, so
// callers lacking a request value continue through ordinary circuit eligibility
// checks.
func openAIProxyStreamQuarantineBypassed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	return requeststate.ExecutionHintsFromContext(ctx).ProxyQuarantineBypass
}

// isOpenAIProxyStreamQuarantined reports an active circuit block for the
// target's positive OpenAI proxy ID. Request-local bypass, missing proxy
// eligibility, or an absent circuit returns false; the check uses the current
// wall clock.
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

// logOpenAIProxyStreamQuarantineFailOpen emits a bounded warning after
// selection bypasses blocked proxies. Callers supply a constructed selector;
// its atomic timestamp stores nanoseconds and is shared by requests using that
// selector.
//
// The interval comparison rejects warnings issued too soon after the previous
// one. CompareAndSwap then chooses one caller when several requests pass that
// comparison together, preventing each request from logging the same outage.
// The winner records the blocked-proxy count and requested model at
// publication.
func (s *Compatible) logOpenAIProxyStreamQuarantineFailOpen(requestedModel string, blockedProxies int) {
	now := time.Now().UnixNano()
	last := s.proxyFailOpenLogAt.Load()
	// Requests sharing this selector can pass the interval check together. The
	// timestamp swap chooses one warning publisher while other requests continue selection.
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
