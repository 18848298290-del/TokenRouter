package app

import (
	"context"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/openaiattempt"
	"github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	openaiwire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// provideOpenAIHTTPResources creates HTTP resources shared by text, media, WS, and compatibility endpoints.
func provideOpenAIHTTPResources(concurrency *scheduler.ConcurrencyService, cfg *config.Config) *gatewayhttp.OpenAIHTTPResources {
	ping := time.Duration(0)
	imageOptions := openAIImageAdmissionOptions(cfg)
	if cfg != nil {
		ping = time.Duration(cfg.Concurrency.PingInterval) * time.Second
	}
	return &gatewayhttp.OpenAIHTTPResources{Concurrency: gatewayhttp.NewConcurrencyHelper(concurrency, gatewayhttp.SSEPingFormatComment, ping), Images: &scheduler.ImageConcurrencyLimiter{}, ImageOptions: imageOptions}
}

// openAIImageAdmissionOptions extracts image admission configuration.
func openAIImageAdmissionOptions(cfg *config.Config) *gatewayhttp.OpenAIImageAdmissionOptions {
	if cfg == nil {
		return nil
	}
	value := cfg.Gateway.ImageConcurrency
	return &gatewayhttp.OpenAIImageAdmissionOptions{Enabled: value.Enabled, Limit: value.MaxConcurrentRequests, Wait: strings.TrimSpace(value.OverflowMode) == config.ImageConcurrencyOverflowModeWait, Timeout: time.Duration(value.WaitTimeoutSeconds) * time.Second, MaxWaiting: value.MaxWaitingRequests}
}

// provideOpenAITextHTTP binds attempt execution to the OpenAI text HTTP handler.
func provideOpenAITextHTTP(
	source *gatewayhttp.OpenAIResponsesExecutor,
	funding *admission.FundingAdmission,
	keys *apikey.APIKeyService,
	resources *gatewayhttp.OpenAIHTTPResources,
	cyber *gatewayhttp.CyberHandler,
	rules *errorpolicy.ErrorPassthroughService,
	moderator *moderation.ContentModerationService,
	prompts *promptpolicy.Service,
	cfg *config.Config,
	runtime *openaiattempt.Runtime,
	activity *gatewayRequestActivity,
	planner *gatewayadapter.RoutePlanner, cache session.GatewayCache,
	messages *messageHTTPBindings,
	subscriptions *billing.SubscriptionService,
) *gatewayhttp.OpenAITextHandler {
	options := openAITextOptions(cfg)
	bindings := openAITextBindings(source, funding, keys, resources, cyber, rules, moderator, planner, cache, subscriptions)
	if messages != nil {
		bindings.ClientVersions = messages.bindings.ClientVersions
	}
	result := gatewayhttp.NewBoundOpenAITextHandler(options, bindings, prompts, textflow.NewResponsesExecutor(runtime, textflow.ResponseOptions{MaxSwitches: options.MaxSwitches}, textflow.ResponseOptions{MaxSwitches: options.MaxSwitches, FirstOutputBudget: true}))
	result.BindRequestActivity(activity.Enter)
	return result
}

// openAITextOptions returns static HTTP options and the provider switch budget.
func openAITextOptions(cfg *config.Config) gatewayhttp.OpenAITextOptions {
	options := gatewayhttp.OpenAITextOptions{MaxSwitches: 3}
	if cfg != nil {
		options.ForceCodexCLI = cfg.Gateway.ForceCodexCLI
		options.MaxBodyBytes = cfg.Gateway.MaxBodySize
		if cfg.Gateway.MaxProviderSwitches > 0 {
			options.MaxSwitches = cfg.Gateway.MaxProviderSwitches
		}
		if cfg.Gateway.StreamKeepaliveInterval > 0 {
			options.CompactKeepaliveInterval = time.Duration(cfg.Gateway.StreamKeepaliveInterval) * time.Second
		}
	}
	return options
}

// openAITextBindings binds shared dependencies used to create per-request execution state.
func openAITextBindings(source *gatewayhttp.OpenAIResponsesExecutor, funding *admission.FundingAdmission, keys *apikey.APIKeyService, resources *gatewayhttp.OpenAIHTTPResources, cyber *gatewayhttp.CyberHandler, rules *errorpolicy.ErrorPassthroughService, moderator *moderation.ContentModerationService, planner *gatewayadapter.RoutePlanner, cache session.GatewayCache, subscriptions *billing.SubscriptionService) gatewayhttp.OpenAITextBindings {
	var moderationPort gatewayhttp.ModerationPort
	if moderator != nil {
		moderationPort = moderator
	}
	bindings := gatewayhttp.OpenAITextBindings{
		Dependencies: gatewayhttp.OpenAIDependencies{
			Handler:     true,
			Gateway:     source != nil,
			Funding:     funding != nil,
			Keys:        keys != nil,
			Concurrency: resources != nil && resources.Concurrency != nil && resources.Concurrency.Service() != nil,
		},
		Resources:     resources,
		ResponseOwner: func() session.HTTPResponseOwnerReader { return source.Lineage.Store },
		Moderation:    moderationPort,
		PlanRoute: func(ctx context.Context, key *apikey.APIKey, model string) routing.RoutePlan {
			return planner.PlanKey(ctx, key, model)
		},
		ReplaceModel:        openaiwire.ReplaceModelInBody,
		ClientGroupFallback: provideClientGroupFallbackResolver(keys, funding, subscriptions, cache),
		Errors:              rules,
		Funding:             funding,
		Cyber:               cyber,
		IsolateSession:      messageSessionIsolation(cache),
	}
	return bindings
}
