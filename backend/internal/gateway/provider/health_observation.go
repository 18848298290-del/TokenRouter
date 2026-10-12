package provider

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// ApplyExecutionSchedulingThreshold applies health thresholds and copies scheduling state back to the target.
func ApplyExecutionSchedulingThreshold(ctx context.Context, observer *provideradapter.UpstreamHealth, value *ExecutionProvider) bool {
	if observer == nil {
		return false
	}
	record := ExecutionRecord(value)
	paused := observer.Core.ApplyProviderSchedulingThreshold(ctx, record)
	if value != nil && record != nil {
		value.Record.TempUnschedulableUntil = record.TempUnschedulableUntil
		value.Record.TempUnschedulableReason = record.TempUnschedulableReason
		value.Record.Extra = record.Extra
	}
	return paused
}

// ObserveExecutionSessionWindow skips empty observations before accessing the optional health dependency.
func ObserveExecutionSessionWindow(ctx context.Context, observer *provideradapter.UpstreamHealth, value *ExecutionProvider, headers http.Header) {
	observation := provideradapter.SessionWindowObservation(headers)
	if observation.Status == "" {
		return
	}
	observer.Core.UpdateSessionWindow(ctx, ExecutionRecord(value), observation)
}

// ApplyGrokExecutionHealth delegates the attempt and provider record to Grok health handling.
func ApplyGrokExecutionHealth(ctx context.Context, health *provideradapter.GrokHealth, target *ExecutionProvider, status int, headers http.Header, body []byte, teamModel string, models ...string) provider.UpstreamErrorDecision {
	if health == nil || target == nil {
		return provider.UpstreamErrorDecision{Policy: provider.ErrorPolicyNone}
	}
	return health.ObserveError(ctx, target.View(), provideradapter.GrokHealthInput{
		Observation:     HealthObservationFromContext(ctx, status, headers, body, models),
		Models:          models,
		QuotaModel:      teamModel,
		TeamModel:       teamModel,
		RequestScoped:   IsRequestScopedProviderFailure(target, status, body),
		ServerTransient: IsTransientProviderFailure(status, body),
	})
}

// HealthObservationFromContext collects model, thinking, and endpoint details for health evaluation.
func HealthObservationFromContext(ctx context.Context, status int, headers http.Header, body []byte, models []string) provideradapter.HealthObservation {
	input := provideradapter.HealthObservation{Status: status, Headers: headers, Body: body, EffectiveModel: requeststate.HealthModel(ctx, models), ModelProvided: len(models) > 0, Thinking: requeststate.HealthThinking(ctx), ImagesEndpoint: requeststate.OpenAIImagesEndpointFromContext(ctx)}
	if len(models) > 0 {
		input.Model = models[0]
	}
	return input
}

// ApplyExecutionHealth observes an independent record, then copies credentials and extra state back to the target.
func ApplyExecutionHealth(ctx context.Context, observer *provideradapter.UpstreamHealth, target *ExecutionProvider, input provideradapter.HealthObservation) provider.UpstreamErrorDecision {
	record := ExecutionRecord(target)
	result := observer.ApplyUpstreamError(ctx, record, input)
	if target != nil && record != nil {
		target.Record.Credentials, target.Record.Extra = record.Credentials, record.Extra
	}
	return result
}

// ExecutionErrorPolicy returns provider identity, platform, type, and credentials for policy evaluation.
func ExecutionErrorPolicy(value *ExecutionProvider) *provider.Record {
	if value == nil {
		return nil
	}
	return &provider.Record{ID: value.Record.ID, Platform: value.Record.Platform, Type: value.Record.Type, Credentials: value.Record.Credentials}
}

// ApplyOpenAIResponseHealth combines request, model, and response details for OpenAI health evaluation.
func ApplyOpenAIResponseHealth(ctx context.Context, health *provideradapter.OpenAIResponseHealth, target *ExecutionProvider, status int, headers http.Header, body []byte, suppressDefaultRateLimit bool, models ...string) provider.UpstreamErrorDecision {
	return health.Apply(ctx, target.View(), provideradapter.OpenAIResponseHealthInput{
		Observation:              HealthObservationFromContext(ctx, status, headers, body, models),
		Models:                   models,
		SuppressDefaultRateLimit: suppressDefaultRateLimit,
		ContentRejected:          IsContentPolicyRejection(body) || IsOpenAICyberWarningPayload(body, upstream.ExtractErrorMessage(body)),
		RequestScoped:            IsRequestScopedProviderFailure(target, status, body),
		SelfBuiltImage:           openai.IsOpenAIImagesSelfBuiltRequest(ctx),
		Transient:                IsTransientProviderFailure(status, body),
	})
}
