package app

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// openAITokenExecution connects provider selection to token counting.
type openAITokenExecution struct {
	*gatewayhttp.OpenAIAuxiliary
	planner *gatewayprovider.RoutePlanner
	choices *selection.Compatible
}

// openAITokenTarget holds credentials for one count and returns a separate selection snapshot to HTTP.
type openAITokenTarget struct {
	source *gatewayhttp.OpenAIAuxiliary
	value  *gatewayprovider.ExecutionProvider
}

func (p openAITokenExecution) PlanTokenRoute(ctx context.Context, key *apikey.APIKey, model string) routing.RoutePlan {
	return p.planner.PlanKey(ctx, key, model)
}

func (p openAITokenExecution) TokenSessionHash(c *gin.Context, body []byte) string {
	return gatewayhttp.GenerateOpenAISessionHash(c, body)
}

func (p openAITokenExecution) SelectCount(ctx context.Context, group *int64, hash, model, platform string) (gatewayhttp.OpenAICountTarget, error) {
	value, err := p.choices.SelectProviderForTokenCount(ctx, group, hash, model, provider.OpenAIEndpointCapabilityTextGeneration, platform)
	if value == nil {
		return nil, err
	}
	return openAITokenTarget{source: p.OpenAIAuxiliary, value: value}, err
}

func (p openAITokenExecution) SelectInputTokens(ctx context.Context, group *int64, hash, model, routingModel string, excluded map[int64]struct{}, platform string) (gatewayhttp.InputTokensSelection, error) {
	selected, _, err := p.choices.SelectProviderWithSchedulerForCapabilityAndRoutingModel(scheduler.WithSelectOnly(ctx), group, "", hash, model, routingModel, excluded, egress.OpenAIUpstreamTransportAny, provider.OpenAIEndpointCapabilityTextGeneration, false, false, platform)
	if err != nil || selected == nil || selected.Provider == nil {
		return gatewayhttp.InputTokensSelection{}, err
	}
	result := gatewayhttp.InputTokensSelection{Target: openAITokenTarget{source: p.OpenAIAuxiliary, value: selected.Provider}}
	if selected.Acquired {
		result.Release = selected.ReleaseFunc
	}
	return result, nil
}

func (t openAITokenTarget) Snapshot() provider.ProviderSnapshot {
	return gatewayprovider.ExecutionSnapshot(t.value)
}

func (t openAITokenTarget) RetryLimit() int { return t.value.View().GetPoolModeRetryCount() }

func (t openAITokenTarget) ForwardCount(ctx context.Context, c *gin.Context, body []byte, model string) error {
	return t.source.ForwardCountTokensAsAnthropic(ctx, c, t.value, body, model)
}

func (t openAITokenTarget) ForwardInputTokens(ctx context.Context, c *gin.Context, body []byte) error {
	return t.source.ForwardResponsesInputTokens(ctx, c, t.value, body)
}

// provideOpenAITokensHTTP binds provider selection and counting to the OpenAI token endpoint.
func provideOpenAITokensHTTP(source *gatewayhttp.OpenAIAuxiliary, funding *admission.FundingAdmission, keys *apikey.APIKeyService, concurrency *scheduler.ConcurrencyService, rules *errorpolicy.ErrorPassthroughService, cfg *config.Config, activity *gatewayRequestActivity, prompts *promptpolicy.Service, availability *gatewayModelAvailability, choices *selection.Compatible, planner *gatewayprovider.RoutePlanner) *gatewayhttp.OpenAITokensHandler {
	options := gatewayhttp.OpenAITokenOptions{MaxSwitches: 3}
	if cfg != nil {
		options.MaxBodyBytes = cfg.Gateway.MaxBodySize
		if cfg.Gateway.MaxProviderSwitches > 0 {
			options.MaxSwitches = cfg.Gateway.MaxProviderSwitches
		}
	}
	ports := gatewayhttp.OpenAITokenPorts{Execution: openAITokenExecution{OpenAIAuxiliary: source, choices: choices, planner: planner}, Funding: funding}
	if availability != nil {
		ports.Diagnoser = availability.Compatible
		ports.ResolvedDiagnoser = availability.Resolved
	}
	if rules != nil {
		ports.Rules = rules
	}
	if source == nil {
		ports.MissingDependencies = append(ports.MissingDependencies, "gatewayService")
	}
	if funding == nil {
		ports.MissingDependencies = append(ports.MissingDependencies, "billingCacheService")
	}
	if keys == nil {
		ports.MissingDependencies = append(ports.MissingDependencies, "apiKeyService")
	}
	if concurrency == nil {
		ports.MissingDependencies = append(ports.MissingDependencies, "concurrencyHelper")
	}
	result := gatewayhttp.NewOpenAITokensHandler(options, ports, prompts)
	if activity != nil {
		result.BindRequestActivity(activity.Enter)
	}
	return result
}
