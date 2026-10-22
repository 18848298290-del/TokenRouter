package app

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// qoder_compatible.go adapts provider selection and Qoder execution for
// compatibility HTTP handlers. Qoder compatibility handlers reuse generic
// provider selection and the request-side session refresh adapter. Selection
// carries acquired-slot and waiting data into HTTP orchestration, while
// completion capture converts the selected provider, Key, usage, and pricing
// fields into the shared recorder input.

// qoderCompatibleExecution binds Qoder selection, routing, refresh, and
// completion dependencies.
type qoderCompatibleExecution struct {
	choices                       *selection.Generic                   // generic provider selection
	*gatewayprovider.RoutePlanner                                      // requested model routing
	runtime                       *gatewayprovider.QoderRuntime        // Qoder platform execution
	refresh                       *provideradapter.QoderRequestRefresh // request session recovery
	keys                          *apikey.APIKeyService                // completion Key effects
}

// qoderCompatibleSelection exposes one generic selection to the compatibility
// HTTP runtime.
type qoderCompatibleSelection struct {
	owner *qoderCompatibleExecution        // shared execution dependencies
	value *gatewayprovider.SelectionResult // original scheduling result
}

// qoderCompatibleTarget holds one provider attempt and its shared execution
// dependencies.
type qoderCompatibleTarget struct {
	owner *qoderCompatibleExecution          // shared execution dependencies
	value *gatewayprovider.ExecutionProvider // selected execution target
}

// Select delegates model-aware generic selection with excluded providers.
// Errors return before target creation; the returned selection
// retains acquired, release, and wait data for the compatibility runtime's
// resource handling.
func (p *qoderCompatibleExecution) Select(ctx context.Context, id *int64, hash, model string, excluded map[int64]struct{}, _ int64) (gatewayhttp.QoderCompatibleSelection, error) {
	value, err := p.choices.SelectProviderWithLoadAwareness(ctx, id, hash, model, excluded)
	if err != nil {
		return nil, err
	}
	return &qoderCompatibleSelection{owner: p, value: value}, nil
}

// Plan delegates Key and model routing to the shared route planner.
func (p *qoderCompatibleExecution) Plan(ctx context.Context, key *apikey.APIKey, model string) routing.RoutePlan {
	return p.PlanKey(ctx, key, model)
}

// Target wraps the selected provider with its compatibility execution owner.
func (s *qoderCompatibleSelection) Target() gatewayhttp.QoderCompatibleTarget {
	return &qoderCompatibleTarget{owner: s.owner, value: s.value.Provider}
}

// Acquired reports whether generic selection already obtained the provider
// slot.
func (s *qoderCompatibleSelection) Acquired() bool { return s.value.Acquired }

// ReleaseFunc returns the original selection cleanup callback for HTTP resource
// ownership. The compatibility runtime retains its invocation obligation after
// forwarding, so this accessor does not run or replace it.
func (s *qoderCompatibleSelection) ReleaseFunc() func() { return s.value.ReleaseFunc }

// WaitPlan returns the original generic provider-wait plan for HTTP slot
// admission. A nil plan remains nil; the compatibility runtime interprets that
// absence together with Acquired before forwarding.
func (s *qoderCompatibleSelection) WaitPlan() *scheduler.ProviderWaitPlan { return s.value.WaitPlan }

// Report delegates attempt success and usage observations to the generic
// selection feedback.
func (s *qoderCompatibleSelection) Report(id int64, ok bool, result *forward.MessagesResult) {
	s.owner.choices.ReportAdvancedProviderScheduleResult(s.value, id, ok, result)
}

// Switched records provider switching through the original generic selection
// result.
func (s *qoderCompatibleSelection) Switched() { s.owner.choices.RecordAdvancedProviderSwitch(s.value) }

// Snapshot returns the selected provider's candidate data for compatibility
// orchestration.
func (t *qoderCompatibleTarget) Snapshot() provider.ProviderSnapshot {
	return gatewayprovider.ExecutionSnapshot(t.value)
}

// Forward delegates one prepared Qoder exchange using the requested client
// protocol.
func (t *qoderCompatibleTarget) Forward(ctx context.Context, c *gin.Context, body []byte, wire protocol.ProtocolID, model string) (*forward.MessagesResult, error) {
	return gatewayhttp.ForwardQoderAttempt(ctx, c, t.owner.runtime, gatewayprovider.ExecutionRecord(t.value), body, wire, model)
}

// Refresh delegates session refresh and rebuilds the target from the returned
// record. Nil record remains nil, available records retain the refresh error,
// and NewExecutionProvider starts the rebuilt target's request route and
// capabilities unset.
func (t *qoderCompatibleTarget) Refresh(ctx context.Context) (gatewayhttp.QoderCompatibleTarget, error) {
	value, err := t.owner.refresh.RefreshProviderSession(ctx, gatewayprovider.ExecutionRecord(t.value))
	if value == nil {
		return nil, err
	}
	return &qoderCompatibleTarget{owner: t.owner, value: gatewayprovider.NewExecutionProvider(value)}, err
}

// Completion delegates worker-input capture using the target and supplied
// request data. capture.Key is required for payer extraction, and the returned
// input uses CaptureMessages' snapshot rules before shared completion
// recording.
func (t *qoderCompatibleTarget) Completion(ctx context.Context, capture gatewayhttp.QoderCompletionCapture) *completion.Input {
	return gatewayprovider.CaptureMessages(ctx, &gatewayprovider.MessagesCapture{
		Result: capture.Result, APIKey: capture.Key, User: capture.Key.User, Provider: gatewayprovider.ExecutionCompletionRecord(t.value),
		Subscription: capture.Subscription, InboundEndpoint: capture.InboundEndpoint, UpstreamEndpoint: capture.UpstreamEndpoint,
		UserAgent: capture.UserAgent, IPAddress: capture.ClientIP, RequestPayloadHash: capture.PayloadHash, RequestBody: capture.Body,
		APIKeyService: t.owner.keys, PricingUsageFields: capture.Pricing,
	})
}

// provideQoderCompatibleHTTP binds three-switch compatibility handling to
// shared admission and completion. Execution is exposed when source is
// supplied, runtime presence sets platform availability, and optional
// Qoder/application activity hooks track each accepted request and attempt.
func provideQoderCompatibleHTTP(source *gatewayprovider.RoutePlanner, runtime *gatewayprovider.QoderRuntime, refresh *provideradapter.QoderRequestRefresh, concurrency *scheduler.ConcurrencyService, funding *admission.FundingAdmission, keys *apikey.APIKeyService, rules *errorpolicy.ErrorPassthroughService, pool *completion.UsageRecordWorkerPool, recorders GatewayCompletionRecorders, activity *gatewayRequestActivity, qoderActivity *qoderRequestActivity, choices *selection.Generic) *gatewayhttp.QoderCompatibleHandler {
	slots := gatewayhttp.NewConcurrencyHelper(concurrency, gatewayhttp.SSEPingFormatComment, 0)
	var matcher gatewayhttp.ErrorRuleMatcher
	if rules != nil {
		matcher = rules
	}
	options := gatewayhttp.QoderCompatibleOptions{
		Recorder: recorders.Forward, Pool: pool, Slots: slots, Rules: rules,
		ReadAccess: keyhttp.GetAPIKeyFromContext, PlatformAvailable: runtime != nil,
		MayRefresh: qoder.MayRefreshAttempt, MaySwitch: qoder.MaySwitchCompatibleAttempt,
		Errors: gatewayhttp.QoderErrorPresenter{Rules: matcher, Describe: gatewayprovider.DescribeQoderError, ReadAccess: keyhttp.GetAPIKeyFromContext, Catalogue: gatewayprovider.ModelDisplayCatalogue{}},
	}
	if source != nil {
		options.Execution = &qoderCompatibleExecution{RoutePlanner: source, choices: choices, runtime: runtime, refresh: refresh, keys: keys}
	}
	if funding != nil {
		options.Funding = funding
	}
	if qoderActivity != nil {
		options.Enter = qoderActivity.Enter
	}
	result := gatewayhttp.NewQoderCompatibleHandler(gatewayhttp.NewQoderCompatibleRuntime(options), slots, 3)
	if activity != nil {
		result.BindRequestActivity(activity.Enter)
	}
	return result
}

// BindStickySession delegates successful-attempt affinity persistence to the
// generic selector. The compatibility caller supplies the final group and
// session identity, and receives the storage error for its own handling.
func (p *qoderCompatibleExecution) BindStickySession(ctx context.Context, group *int64, hash string, id int64) error {
	return p.choices.BindStickySession(ctx, group, hash, id)
}
