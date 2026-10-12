package app

import (
	"context"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// countExecution connects provider selection to token-counting executors.
type countExecution struct {
	planner   *gatewayprovider.RoutePlanner
	choices   *selection.Generic
	messages  *gatewayhttp.MessagesExecutor
	auxiliary *gatewayhttp.OpenAIAuxiliary
	gemini    *gatewayhttp.GeminiExecutor
	cooldown  func(context.Context, int64, *forwardcore.UpstreamFailoverError)
}

// countTarget holds the provider selected for one token count; execution resolves its credentials.
type countTarget struct {
	choices   *selection.Generic
	gateway   *gatewayhttp.MessagesExecutor
	auxiliary *gatewayhttp.OpenAIAuxiliary
	gemini    *gatewayhttp.GeminiExecutor
	provider  *gatewayprovider.ExecutionProvider
}

func (p countExecution) SelectCountTarget(ctx context.Context, id *int64, hash, model string, excluded map[int64]struct{}) (gatewayhttp.CountTarget, error) {
	value, err := p.choices.SelectProviderForModelWithExclusions(ctx, id, hash, model, excluded)
	if err != nil {
		return nil, err
	}
	return countTarget{gateway: p.messages, auxiliary: p.auxiliary, gemini: p.gemini, provider: value, choices: p.choices}, nil
}

func (p countExecution) PlanCountRoute(ctx context.Context, key *apikey.APIKey, model string) routing.RoutePlan {
	return p.planner.PlanKey(ctx, key, model)
}

func (t countTarget) Snapshot() provider.ProviderSnapshot {
	return gatewayprovider.ExecutionSnapshot(t.provider)
}
func (t countTarget) RetryLimit() int { return t.provider.View().GetPoolModeRetryCount() }
func (t countTarget) ForwardCountTokens(ctx context.Context, c *gin.Context, parsed *requeststate.ParsedRequest) error {
	return gatewayhttp.ForwardSelectedCountTokens(ctx, c, t.provider, parsed, t.gateway, t.auxiliary, t.gemini)
}

func (t countTarget) ReleaseSession(ctx context.Context, hash string) {
	t.choices.ReleaseProviderSession(ctx, t.provider, hash)
}

// provideCountTokensHTTP binds token-counting HTTP dependencies.
func provideCountTokensHTTP(planner *gatewayprovider.RoutePlanner, messages *gatewayhttp.MessagesExecutor, shared *schedulerSharedState, funding *admission.FundingAdmission, rules *errorpolicy.ErrorPassthroughService, cfg *config.Config, activity *gatewayRequestActivity, prompts *promptpolicy.Service, availability *gatewayModelAvailability, choices *selection.Generic, cooldown *provider.RetryCooldown, auxiliary *gatewayhttp.OpenAIAuxiliary, gemini *gatewayhttp.GeminiExecutor, clients *messageHTTPBindings) *gatewayhttp.CountTokensHandler {
	limit := int64(0)
	switches := 10
	if cfg != nil {
		limit = cfg.Gateway.MaxBodySize
		if cfg.Gateway.MaxProviderSwitches > 0 {
			switches = cfg.Gateway.MaxProviderSwitches
		}
	}
	var matcher gatewayhttp.ErrorRuleMatcher
	if rules != nil {
		matcher = rules
	}
	ports := gatewayhttp.CountHTTPPorts{
		Executor: countExecution{planner: planner, choices: choices, messages: messages, auxiliary: auxiliary, gemini: gemini, cooldown: messageRetryCooldown(cooldown)}, Funding: funding, ReadAccess: keyhttp.GetAPIKeyFromContext,
		ObserveCompatibility: func(log *zap.Logger) {
			gatewayhttp.LogCompatibilityFallback(log, func() gatewayhttp.CompatibilityLogSnapshot {
				return gatewayCompatibilitySnapshot(shared)
			})
		},
		BusinessError: func(c *gin.Context, err error, started bool, write func(int, string, string, bool)) bool {
			return gatewayhttp.WriteGroupSelectionBusinessError(c, err, started, keyhttp.GetAPIKeyFromContext, gatewayprovider.ModelDisplayCatalogue{}, write)
		},
		Failure: func(c *gin.Context, failure *forwardcore.UpstreamFailoverError, platform string, started bool) {
			gatewayhttp.WriteAnthropicFailover(c, failure, platform, started, matcher, forwardcore.IsOpenAISilentRefusalErrorBody, forwardcore.OpenAISilentRefusalClientMessage())
		},
	}
	if clients != nil {
		ports.ClientGroupFallback = clients.bindings.ClientGroupFallback
	}
	if availability != nil {
		ports.Diagnoser = availability.Messages
	}
	result := gatewayhttp.NewCountTokensHandler(limit, switches, ports, prompts)
	if activity != nil {
		result.BindRequestActivity(activity.Enter)
	}
	return result
}

func (p countExecution) TempUnscheduleRetryableError(ctx context.Context, id int64, failure *forwardcore.UpstreamFailoverError) {
	p.cooldown(ctx, id, failure)
}
