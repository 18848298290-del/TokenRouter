package app

import (
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// provideOpenAIResponseHealth shares provider health, runtime state, and deferred writes.
func provideOpenAIResponseHealth(observer *provideradapter.UpstreamHealth, blocks *provider.RuntimeBlockState, models *provider.ModelTransientState, deferred *provider.DeferredService) *provideradapter.OpenAIResponseHealth {
	return &provideradapter.OpenAIResponseHealth{Health: observer, Runtime: blocks, ModelTransient: models, Deferred: deferred}
}

// provideOpenAIResponseOutput binds static options and dependencies for response output.
func provideOpenAIResponseOutput(cfg *config.Config, health *provideradapter.OpenAIResponseHealth, grok *provideradapter.GrokHealth, observer *provideradapter.UpstreamHealth, headers *egress.CompiledHeaderFilter, turns *gatewayhttp.CodexTurnStateHeaders, circuit *egress.ProxyStreamCircuit, readers *gatewayadapter.RuntimeReaders, responses session.OpenAIWSStateStore, choices *selection.Compatible, history *session.ReasoningHistory, identity *gatewayadapter.ExecutionAgentIdentity) *gatewayhttp.OpenAIResponseOutput {
	output := &gatewayhttp.OpenAIResponseOutput{
		Reasoning: history, Redact: identity.Redact, Health: health, GrokHealth: grok, Observer: observer, Headers: headers, Turns: turns,
		Corrector: openai.NewCodexToolCorrector(), ProxyCircuit: circuit, Responses: responses,
		ResponseTTL: choices.OpenAIHTTPResponseStickyTTL,
		Options:     gatewayhttp.OpenAIResponseOptions{ReadLimit: config.DefaultUpstreamResponseReadMaxBytes},
	}
	if readers != nil {
		output.TTFT = readers.Gateway.GetOpenAITTFTMode
	}
	if cfg != nil {
		v := cfg.Gateway
		output.Options.Configured = true
		output.Options.MaxLineSize = v.MaxLineSize
		output.Options.StreamDataIntervalTimeout = v.StreamDataIntervalTimeout
		output.Options.StreamKeepaliveInterval = v.StreamKeepaliveInterval
		output.Options.ImageStreamDataIntervalTimeout = v.ImageStreamDataIntervalTimeout
		output.Options.ImageStreamKeepaliveInterval = v.ImageStreamKeepaliveInterval
		output.Options.OpenAIFirstOutputTimeoutSeconds = v.OpenAIFirstOutputTimeoutSeconds
		output.Options.OpenAIHighEffortFirstOutputTimeoutSeconds = v.OpenAIHighEffortFirstOutputTimeoutSeconds
		output.Options.LogUpstreamErrorBody = v.LogUpstreamErrorBody
		output.Options.LogUpstreamErrorBodyMaxBytes = v.LogUpstreamErrorBodyMaxBytes
		output.Options.ResponseHeadersEnabled = cfg.Security.ResponseHeaders.Enabled
		if v.UpstreamResponseReadMaxBytes > 0 {
			output.Options.ReadLimit = v.UpstreamResponseReadMaxBytes
		}
	}
	return output
}

// provideReasoningHistory obtains the optional reasoning content cache from the shared gateway cache.
func provideReasoningHistory(cache session.GatewayCache) *session.ReasoningHistory {
	store, _ := cache.(session.ReasoningContentCache)
	return &session.ReasoningHistory{Cache: store, Warn: gatewayadapter.WarnReasoningCacheFailure}
}

// provideOpenAIResponseState constructs the application's shared session store.
func provideOpenAIResponseState(cache session.GatewayCache) session.OpenAIWSStateStore {
	return session.NewOpenAIWSStateStore(cache, gatewayadapter.LogOpenAIWSModeInfo)
}

func provideResponseHeaderFilter(cfg *config.Config) *egress.CompiledHeaderFilter {
	if cfg == nil {
		return nil
	}
	return egress.CompileHeaderFilter(egress.ResponseHeaderOptions{Enabled: cfg.Security.ResponseHeaders.Enabled, AdditionalAllowed: cfg.Security.ResponseHeaders.AdditionalAllowed, ForceRemove: cfg.Security.ResponseHeaders.ForceRemove})
}
