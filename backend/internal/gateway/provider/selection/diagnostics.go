package selection

import (
	"context"
	"strings"
	"time"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// Scheduling diagnostics explain provider scores using the parameters and
// feedback bound to production selectors. Before scoring, the adapter supplies
// group, cost-window, RPM, and quota settings needed by platform eligibility
// checks; candidate observation does not acquire a provider slot or register a
// session.
//
// Compatible providers use protocol model rules, quota pauses, credential and
// model blocks, proxy quarantine, shadow parent health, and upstream group
// restrictions. Other targets use the generic selector's quota and model checks
// when available. Each branch reports its first platform rejection reason,
// keeping provider-specific policy with the execution record while the
// scheduler builds credential-free diagnostic results.

// GetOverview delegates the provider's scoring overview to a fresh diagnostic
// core. Callers supply the constructed Diagnostics instance; the returned
// result and source errors describe the provider's groups and effective
// advanced scheduling settings.
func (s *Diagnostics) GetOverview(ctx context.Context, id int64) (*policy.AdvancedSchedulerScoreDiagnosticResponse, error) {
	core, _ := s.diagnosticCore()
	return core.GetOverview(ctx, id)
}

// GetDetail delegates a scoring simulation for the provider and supplied group
// request. The core uses a fresh target association scope with the
// production-bound settings and feedback, then returns its detail or source
// error.
func (s *Diagnostics) GetDetail(ctx context.Context, id int64, request policy.AdvancedSchedulerScoreDiagnosticRequest) (*policy.AdvancedSchedulerScoreDiagnosticResponse, error) {
	core, _ := s.diagnosticCore()
	return core.GetDetail(ctx, id, request)
}

// effectiveSettings reads global runtime settings and resolves the group's
// advanced overrides through Parameters. Missing diagnostics or parameter
// bindings use Parameters defaults, and basic or absent groups supply empty
// override values.
func (s *Diagnostics) effectiveSettings(ctx context.Context, group *routing.Group) (policy.EffectiveSettings, policy.RuntimeSettings) {
	var parameters *schedulercore.Parameters
	if s != nil {
		parameters = s.schedulerParameters
	}
	runtime := parameters.Runtime(ctx)
	return parameters.Effective(ctx, schedulerGroupOverrides(group)), runtime
}

// prepareEligibilityContext supplies group and batch cost/RPM observations
// through the bound generic selector, then compatible quota settings when a
// group exists. A nil Diagnostics value passes context through for callers with
// no adapter.
func (s *Diagnostics) prepareEligibilityContext(ctx context.Context, group *routing.Group, providers []gatewayprovider.ExecutionProvider) context.Context {
	if s == nil {
		return ctx
	}
	if s.gatewayService != nil {
		ctx = s.gatewayService.withGroupContext(ctx, group)
		ctx = s.gatewayService.withWindowCostPrefetch(ctx, providers)
		ctx = s.gatewayService.withRPMPrefetch(ctx, providers)
	}
	if s.openAIGateway != nil && group != nil {
		ctx = s.openAIGateway.withOpenAIQuotaAutoPauseContext(ctx)
	}
	return ctx
}

// diagnosticPlatformFilterReason returns the first platform-specific diagnostic
// rejection, or empty when those gates pass. Callers supply a usable target and
// group after the core's general hard filters; the requested model is trimmed
// before inspection.
//
// Compatible targets check model scheduling, quota pause, and model support
// before credential, proxy, shadow, and upstream-group rules. Generic targets
// use the bound selector's model, quota, cost-window, RPM, and upstream
// restrictions, including sticky allowance from the request. Without that
// selector, protocol-record model rules supply the remaining checks; now is
// unused here because those helpers read their own timing inputs.
func (s *Diagnostics) diagnosticPlatformFilterReason(
	ctx context.Context,
	provider *gatewayprovider.ExecutionProvider,
	group *routing.Group,
	request policy.AdvancedSchedulerScoreDiagnosticRequest,
	now time.Time,
) string {
	model := strings.TrimSpace(request.RequestedModel)
	if provider != nil && provider.View().IsOpenAICompatible() {
		if !gatewayprovider.ExecutionModelPolicy(provider).Schedulable(ctx, model) {
			return "model_runtime_blocked"
		}
		if provider.View().IsOpenAI() {
			if paused, _ := gatewayprovider.OpenAIQuotaPause(ctx, provider); paused {
				return "quota_auto_pause"
			}
		}
		if provider.View().IsGrok() {
			if paused, _ := gatewayprovider.GrokQuotaPause(provider); paused {
				return "quota_auto_pause"
			}
		}
		if !gatewayprovider.ExecutionModelPolicy(provider).SupportsCompatibleRouting(ctx, model) {
			return "model_unsupported"
		}
		if s != nil && s.openAIGateway != nil {
			if s.openAIGateway.isOpenAIProviderRequestRuntimeBlocked(provider, model) {
				return "runtime_blocked"
			}
			if s.openAIGateway.isOpenAIProxyStreamQuarantined(ctx, provider) {
				return "proxy_stream_quarantined"
			}
			scheduler := &compatiblePicker{service: s.openAIGateway}
			if !providercore.ParentHealthyForShadow(gatewayprovider.ExecutionRecord(provider), func(id int64) *providercore.Record {
				return gatewayprovider.ExecutionRecord(scheduler.lookupShadowParentProvider(ctx, id))
			}) {
				return "shadow_parent_unhealthy"
			}
			groupID := group.ID
			if s.openAIGateway.NeedsUpstreamGroupRestriction(ctx, &groupID) &&
				s.openAIGateway.UpstreamRoutingModelRestricted(ctx, groupID, provider, model, false) {
				return "group_upstream_restricted"
			}
		}
		return ""
	}

	if s != nil && s.gatewayService != nil {
		if model != "" && !s.gatewayService.isModelSupportedByProviderWithContext(ctx, provider, model) {
			return "model_unsupported"
		}
		if !s.gatewayService.isProviderSchedulableForModelSelection(ctx, provider, model) {
			return "model_runtime_blocked"
		}
		if !s.gatewayService.isProviderSchedulableForQuota(provider) {
			return "quota_exceeded"
		}
		isSticky := provider.Record.ID == request.StickyProviderID
		if !s.gatewayService.isProviderSchedulableForWindowCost(ctx, provider, isSticky) {
			return "window_cost_exceeded"
		}
		if !s.gatewayService.isProviderSchedulableForRPM(ctx, provider, isSticky) {
			return "rpm_exceeded"
		}
		groupID := group.ID
		if s.gatewayService.needsUpstreamGroupRestrictionCheck(ctx, &groupID) &&
			s.gatewayService.isUpstreamModelRestrictedByGroup(ctx, groupID, provider, model) {
			return "group_upstream_restricted"
		}
		return ""
	}

	if model != "" && !gatewayprovider.ExecutionProtocolRecord(provider).IsModelSupported(model, provideradapter.ModelDefaults(), provideradapter.ModelRules(gatewayprovider.ExecutionProtocolRecord(provider))) {
		return "model_unsupported"
	}
	if !gatewayprovider.ExecutionModelPolicy(provider).Schedulable(ctx, model) {
		return "model_runtime_blocked"
	}
	return ""
}
