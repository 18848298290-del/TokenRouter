package provider

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	protocolcore "github.com/TokenFlux/TokenRouter/internal/protocol"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// AllowsCompatibleCompact applies the Grok and OpenAI Compact eligibility rules.
func AllowsCompatibleCompact(provider *ExecutionProvider) bool {
	return provider != nil && (provider.View().IsGrok() || provider.View().AllowsOpenAICompact())
}

// CompatibleProviderEligible reports whether a provider can serve this request.
// It checks platform, availability, quota pause, Spark restrictions, models, and endpoints.
//
// Callers must also check parent credential health for Spark shadow providers.
// That lookup remains outside this function to avoid a database dependency.
func CompatibleProviderEligible(ctx context.Context, provider *ExecutionProvider, platform string, requestedModel string, requireCompact bool, requiredCapability providercore.OpenAIEndpointCapability) bool {
	return CompatibleEligibilityReason(ctx, provider, platform, requestedModel, requireCompact, requiredCapability) == ""
}

// CompatibleEligibilityReason returns eligibility and the first rejection reason.
// Load batching uses the reason to diagnose unavailable providers.
// @project-doc docs/architecture/provider_scheduling_and_cache.md#advanced_scheduler_selection
func CompatibleEligibilityReason(ctx context.Context, provider *ExecutionProvider, platform string, requestedModel string, requireCompact bool, requiredCapability providercore.OpenAIEndpointCapability) string {
	platform = strings.TrimSpace(platform)
	if provider == nil {
		return "provider_nil"
	}
	if platform != "" && provider.Record.Platform != platform {
		return "platform_mismatch"
	}
	if !ExecutionModelPolicy(provider).Schedulable(ctx, requestedModel) {
		if provider.View().IsSchedulable() {
			return "model_rate_limited"
		}
		return "not_schedulable"
	}
	if provider.View().IsOpenAI() {
		if paused, reason := OpenAIQuotaPause(ctx, provider); paused {
			slog.Debug("provider_auto_paused_by_quota",
				"provider_id", provider.Record.ID,
				"window", reason.Window,
				"threshold", reason.Threshold,
				"utilization", reason.Utilization,
			)
			if reason.Window != "" {
				return "quota_auto_pause_" + reason.Window
			}
			return "quota_auto_pause"
		}
	}
	if provider.View().IsGrok() {
		if paused, reason := GrokQuotaPause(provider); paused {
			slog.Debug("grok_provider_auto_paused_by_quota",
				"provider_id", provider.Record.ID,
				"window", reason.Window,
				"threshold", reason.Threshold,
				"utilization", reason.Utilization,
			)
			if reason.Window != "" {
				return "quota_auto_pause_" + reason.Window
			}
			return "quota_auto_pause"
		}
	}
	if !ExecutionModelPolicy(provider).SupportsCompatibleRouting(ctx, requestedModel) {
		return "model_not_supported"
	}
	if !provider.View().IsSchedulable() || provider.View().IsQuotaExceeded() {
		return "provider_quota_exhausted"
	}
	if !SupportsRequestCapability(ctx, provider, requiredCapability) {
		if provider.View().IsGrok() && requiredCapability == providercore.OpenAIEndpointCapabilityGrokMediaGeneration {
			_, reason := providercore.GrokMediaGenerationEligibility(ExecutionRecord(provider), provideradapter.GrokTierRules())
			slog.Debug("grok_media_provider_ineligible", "provider_id", provider.Record.ID, "reason", reason)
		}
		return "capability_mismatch"
	}
	if requireCompact && !AllowsCompatibleCompact(provider) {
		return "compact_unsupported"
	}
	return ""
}

// OpenAIQuotaPause evaluates quota state using the current time and request thresholds.
func OpenAIQuotaPause(ctx context.Context, provider *ExecutionProvider) (bool, providercore.QuotaAutoPauseDecision) {
	return evaluateOpenAIQuotaPause(ctx, provider, time.Now())
}

// evaluateOpenAIQuotaPause delegates quota pause evaluation to the provider module.
func evaluateOpenAIQuotaPause(ctx context.Context, v *ExecutionProvider, now time.Time) (bool, providercore.QuotaAutoPauseDecision) {
	if v == nil {
		return false, providercore.QuotaAutoPauseDecision{}
	}
	return providercore.EvaluateQuotaAutoPause(v.Record.Platform, v.Record.Extra, QuotaAutoPauseSettings(ctx), now)
}

// WithQuotaAutoPauseSettings stores global OpenAI quota thresholds in the request snapshot.
// Selection, display, and capacity calculations share these thresholds.
func WithQuotaAutoPauseSettings(ctx context.Context, settings providercore.QuotaAutoPauseSettings) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	hints := requeststate.ExecutionHintsFromContext(ctx)
	hints.QuotaAutoPauseThreshold5h = settings.DefaultThreshold5h
	hints.QuotaAutoPauseThreshold7d = settings.DefaultThreshold7d
	return requeststate.WithExecutionHints(ctx, hints)
}

// QuotaAutoPauseSettings reads the snapshot shared by selection and pinned-provider checks.
func QuotaAutoPauseSettings(ctx context.Context) providercore.QuotaAutoPauseSettings {
	hints := requeststate.ExecutionHintsFromContext(ctx)
	return providercore.QuotaAutoPauseSettings{DefaultThreshold5h: hints.QuotaAutoPauseThreshold5h, DefaultThreshold7d: hints.QuotaAutoPauseThreshold7d}
}

// GrokQuotaPause evaluates the provider module's Grok quota-window rules.
func GrokQuotaPause(value *ExecutionProvider) (bool, providercore.QuotaAutoPauseDecision) {
	return providercore.EvaluateGrokQuotaAutoPause(ExecutionRecord(value), time.Now)
}

// SupportsRequestCapability checks WS, Compact, then ordinary HTTP eligibility.
func SupportsRequestCapability(ctx context.Context, provider *ExecutionProvider, capability providercore.OpenAIEndpointCapability) bool {
	if provider == nil {
		return false
	}
	source, _ := requeststate.ClientProtocolFromContext(ctx)
	if !provider.View().IsOpenAICompatible() && (capability == "" || capability == providercore.OpenAIEndpointCapabilityTextGeneration || capability == providercore.OpenAIEndpointCapabilityResponses) {
		policy := ExecutionModelPolicy(provider)
		if source != "" {
			return policy.AllowsProtocol(ctx)
		}
		group, _ := requeststate.GroupFromContext(ctx)
		for _, entry := range []protocolcore.ProtocolID{protocolcore.ProtocolAnthropicMessages, protocolcore.ProtocolOpenAIResponses, protocolcore.ProtocolOpenAIChatCompletions} {
			if _, allowed := policy.ProtocolRoute(group, entry); allowed {
				return true
			}
		}
		return false
	}
	if source == protocolcore.ProtocolResponsesWebSocket || source == protocolcore.ProtocolResponsesCompact {
		if capability == providercore.OpenAIEndpointCapabilityTextGeneration || capability == providercore.OpenAIEndpointCapabilityResponses {
			return ExecutionModelPolicy(provider).AllowsProtocol(ctx)
		}
		if capability == providercore.OpenAIEndpointCapabilityRemoteCompactionV2 {
			return ExecutionModelPolicy(provider).AllowsProtocol(ctx) && provider.View().AllowsOpenAINativeCompactionV2()
		}
	}
	return provideradapter.SupportsOpenAIEndpoint(ExecutionProtocolRecord(provider), capability)
}
