package app

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/openaiattempt"
	"github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	wshttp "github.com/TokenFlux/TokenRouter/internal/gateway/ws/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// provideResponsesWSHTTP binds WS handlers to shared attempt support, lifecycle tracking, and runtime readers.
func provideResponsesWSHTTP(
	source *wshttp.OpenAIWebSocketExecutor, credentials *gatewayhttp.RequestCredentialExecutor,
	funding *admission.FundingAdmission,
	keys *apikey.APIKeyService,
	common openaiattempt.Bindings,
	prompt *promptpolicy.Service,
	blocks *session.CyberBlocks,
	cfg *config.Config,
	activity *gatewayRequestActivity,
	choices *selection.Compatible, planner *gatewayprovider.RoutePlanner,
	subscriptions *billing.SubscriptionService,
	prices *billing.PriceResolver,
) *wshttp.ResponsesWSHandler {
	options := responsesWSOptions(cfg)
	if source != nil {
		options.Runtime = source.Runtime
	}
	b := responsesWSBindings(source, credentials, funding, keys, common, prompt, blocks, choices, planner, subscriptions)
	b.Pricing = &admission.ModelPricing{Resolver: prices}
	result := wshttp.New(options, b)
	result.BindRequestActivity(activity.Enter)
	return result
}

// responsesWSOptions returns static connection and first-frame options with defaults.
func responsesWSOptions(cfg *config.Config) wshttp.ResponsesWSOptions {
	options := wshttp.ResponsesWSOptions{
		MaxProviderSwitches: 3,
		ReadLimit:           wshttp.ResolveOpenAIWSClientReadLimitBytes(openAIWSExecutionOptions(cfg)),
		FirstMessageTimeout: wshttp.ResolveOpenAIWSClientFirstMessageTimeout(openAIWSExecutionOptions(cfg)),
	}
	if cfg != nil {
		options.MaxIngressConnectionsPerAPIKey = cfg.Gateway.OpenAIWS.MaxIngressConnectionsPerAPIKey
		if cfg.Gateway.MaxProviderSwitches > 0 {
			options.MaxProviderSwitches = cfg.Gateway.MaxProviderSwitches
		}
	}
	return options
}

// responsesWSBindings binds shared state and per-turn execution functions.
func responsesWSBindings(source *wshttp.OpenAIWebSocketExecutor, credentials *gatewayhttp.RequestCredentialExecutor, funding *admission.FundingAdmission, keys *apikey.APIKeyService, common openaiattempt.Bindings, prompt *promptpolicy.Service, blocks *session.CyberBlocks, choices *selection.Compatible, planner *gatewayprovider.RoutePlanner, subscriptions admission.SubscriptionReader) wshttp.Bindings {
	b := wshttp.Bindings{
		Common:        common,
		Subscriptions: subscriptions,
		Prompt:        prompt,
		Blocks:        blocks,
		Dependencies: gatewayhttp.OpenAIDependencies{
			Handler:     true,
			Gateway:     source != nil,
			Funding:     funding != nil,
			Keys:        keys != nil,
			Concurrency: common.Support.Concurrency != nil && common.Support.Concurrency.Service() != nil,
		},
	}
	if keys != nil {
		b.Keys = keys
	}
	if funding != nil {
		b.CheckFunding = funding.CheckKey
	}
	if source != nil {
		b.PlanRoute = func(ctx context.Context, key *apikey.APIKey, model string) routing.RoutePlan {
			return planner.PlanKey(ctx, key, model)
		}
		b.Isolate = source.EnsureSessionIsolation
		b.BindSticky = func(ctx context.Context, groupID *int64, hash string, id int64) error {
			ttl := choices.SessionStickyTTL()
			if source.Runtime != nil {
				ttl = time.Duration(source.Runtime.Snapshot().StickySessionTTLSeconds) * time.Second
			}
			return choices.BindStickySessionWithTTL(ctx, groupID, hash, id, ttl)
		}
		b.ReportSelection = common.Selection.ReportSelection
		b.Stop429 = stopOpenAI429
		b.Credential = credentials.Resolve
		b.ResolveRouting = choices.ResolveOpenAIWSRoutingModelForProvider
		b.BeginPreemption = source.BeginOpenAIWSIngressSessionPreemption
		b.Relay = source.ProxyResponsesWebSocketFromClient
	}
	return b
}
