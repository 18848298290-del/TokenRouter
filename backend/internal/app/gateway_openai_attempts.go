package app

import (
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/openaiattempt"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	openaiwire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// provideOpenAIAttemptBindings shares platform calls, slots, and completion recording between HTTP and WS.
func provideOpenAIAttemptBindings(
	source *gatewayhttp.OpenAIResponsesExecutor,
	keys *apikey.APIKeyService,
	resources *gatewayhttp.OpenAIHTTPResources,
	cyber *gatewayhttp.CyberHandler,
	rules *errorpolicy.ErrorPassthroughService,
	moderator *moderation.ContentModerationService,
	records GatewayCompletionRecorders,
	worker *completion.UsageRecordWorkerPool, availability *gatewayModelAvailability, choices *selection.Compatible,
	unified *gatewayhttp.UnifiedTextExecutor,
	generic *selection.Generic,
	funding *admission.FundingAdmission, subscriptions *billing.SubscriptionService, planner *gatewaycapture.RoutePlanner, cache session.GatewayCache,
) openaiattempt.Bindings {
	support := &openaiattempt.Support{Rules: rules, Cyber: cyber, Submission: gatewayhttp.NewCompletionSubmission(worker, true)}
	if resources != nil {
		support.Concurrency = resources.Concurrency
	}
	if keys != nil {
		support.Quota = keys
	}
	if moderator != nil {
		support.Moderation = moderator
	}
	b := openaiattempt.Bindings{Support: support, Recorder: records.OpenAI}
	if keys != nil && funding != nil && planner != nil {
		b.Fallback = openaiattempt.GroupFallbackPorts{
			Plan:    planner.PlanKey,
			Resolve: provideRuntimeGroupFallbackResolver(keys, funding, subscriptions, cache),
		}
	}

	if s := source; s != nil {
		b.Forward.EnforceOpenAIClientPolicyForRequest = s.Requests.EnforceClient
		b.Forward.Forward = s.Forward
		b.Forward.ForwardAsAnthropic = s.Text.Messages
		b.Forward.ForwardAsChatCompletions = s.Text.Chat
		b.Forward.MatchOpenAITLSFingerprintRouterForRequest = s.Requests.MatchTLS
		b.Forward.ReplaceModelInBody = openaiwire.ReplaceModelInBody
		b.Selection.UpdateCodexUsageSnapshotFromHeaders = s.Text.CodexUsage.Headers
	}
	if generic != nil {
		b.Sessions = openaiattempt.SessionPorts{New: generic.NewSessionAttempts, Track: generic.TrackSessionAttempt, IncrementRPM: generic.IncrementProviderRPM}
	}
	if unified != nil {
		b.Forward.Forward = unified.Responses
		b.Forward.ForwardAsAnthropic = unified.Messages
		b.Forward.ForwardAsChatCompletions = unified.Chat
		b.Forward.EnforceOpenAIClientPolicyForRequest = unified.EnforceClient
	}
	if choices != nil {
		b.Sessions.StickyProviderID = choices.StickyProviderID
		support.Sticky = choices
		b.Selection.ObserveOpenAIProviderHealthFailure = choices.ObserveOpenAIProviderHealthFailure
		b.Selection.RecordOpenAIProviderSwitchForSelection = choices.RecordOpenAIProviderSwitchForSelection
		b.Selection.ReportOpenAIProviderScheduleResult = func(a *gatewaycapture.ExecutionProvider, model string, success bool, first *int, errs ...error) bool {
			return choices.ReportOpenAIProviderScheduleResult(a, model, success, first, errs...)
		}
		b.Selection.SelectProviderWithSchedulerForCapability = choices.SelectProviderWithSchedulerForCapability
		b.Selection.SelectProviderWithSchedulerForCapabilityAndRoutingModel = choices.SelectProviderWithSchedulerForCapabilityAndRoutingModel
		b.Selection.SelectImages = choices.SelectProviderWithSchedulerForImages
		b.Selection.RecordSwitch = choices.RecordOpenAIProviderSwitch
		b.Selection.ReportSelection = choices.ReportOpenAIProviderScheduleResultForSelection
	}

	if availability != nil {
		b.Diagnoser = availability.Compatible
		b.ResolvedDiagnoser = availability.Resolved
	}
	return b
}

// provideOpenAITextAttemptRuntime binds execution support and platform calls for text requests.
func provideOpenAITextAttemptRuntime(b openaiattempt.Bindings) *openaiattempt.Runtime {
	return openaiattempt.New(b)
}
