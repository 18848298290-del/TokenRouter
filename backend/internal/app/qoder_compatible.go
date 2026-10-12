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

// qoderCompatibleExecution binds selection, refresh, and completion capture for Qoder compatibility handlers.
type qoderCompatibleExecution struct {
	choices *selection.Generic
	*gatewayprovider.RoutePlanner
	runtime *gatewayprovider.QoderRuntime
	refresh *provideradapter.QoderRequestRefresh
	keys    *apikey.APIKeyService
}

type qoderCompatibleSelection struct {
	owner *qoderCompatibleExecution
	value *gatewayprovider.SelectionResult
}

// qoderCompatibleTarget holds one attempt provider and exposes its snapshot to HTTP.
type qoderCompatibleTarget struct {
	owner *qoderCompatibleExecution
	value *gatewayprovider.ExecutionProvider
}

func (p *qoderCompatibleExecution) Select(ctx context.Context, id *int64, hash, model string, excluded map[int64]struct{}, userID int64) (gatewayhttp.QoderCompatibleSelection, error) {
	value, err := p.choices.SelectProviderWithLoadAwareness(ctx, id, hash, model, excluded, "", userID)
	if err != nil {
		return nil, err
	}
	return &qoderCompatibleSelection{owner: p, value: value}, nil
}

func (p *qoderCompatibleExecution) Plan(ctx context.Context, key *apikey.APIKey, model string) routing.RoutePlan {
	return p.PlanKey(ctx, key, model)
}

func (s *qoderCompatibleSelection) Target() gatewayhttp.QoderCompatibleTarget {
	return &qoderCompatibleTarget{owner: s.owner, value: s.value.Provider}
}

func (s *qoderCompatibleSelection) Acquired() bool { return s.value.Acquired }

func (s *qoderCompatibleSelection) ReleaseFunc() func() { return s.value.ReleaseFunc }

func (s *qoderCompatibleSelection) WaitPlan() *scheduler.ProviderWaitPlan { return s.value.WaitPlan }

func (s *qoderCompatibleSelection) Report(id int64, ok bool, result *forward.MessagesResult) {
	s.owner.choices.ReportAdvancedProviderScheduleResult(s.value, id, ok, result)
}

func (s *qoderCompatibleSelection) Switched() { s.owner.choices.RecordAdvancedProviderSwitch(s.value) }

func (t *qoderCompatibleTarget) Snapshot() provider.ProviderSnapshot {
	return gatewayprovider.ExecutionSnapshot(t.value)
}

func (t *qoderCompatibleTarget) Forward(ctx context.Context, c *gin.Context, body []byte, wire protocol.ProtocolID, model string) (*forward.MessagesResult, error) {
	return gatewayhttp.ForwardQoderAttempt(ctx, c, t.owner.runtime, gatewayprovider.ExecutionRecord(t.value), body, wire, model)
}

func (t *qoderCompatibleTarget) Refresh(ctx context.Context) (gatewayhttp.QoderCompatibleTarget, error) {
	value, err := t.owner.refresh.RefreshProviderSession(ctx, gatewayprovider.ExecutionRecord(t.value))
	if value == nil {
		return nil, err
	}
	return &qoderCompatibleTarget{owner: t.owner, value: gatewayprovider.NewExecutionProvider(value)}, err
}

func (t *qoderCompatibleTarget) Completion(ctx context.Context, capture gatewayhttp.QoderCompletionCapture) *completion.Input {
	return gatewayprovider.CaptureMessages(ctx, &gatewayprovider.MessagesCapture{
		Result: capture.Result, APIKey: capture.Key, User: capture.Key.User, Provider: gatewayprovider.ExecutionCompletionRecord(t.value),
		Subscription: capture.Subscription, InboundEndpoint: capture.InboundEndpoint, UpstreamEndpoint: capture.UpstreamEndpoint,
		UserAgent: capture.UserAgent, IPAddress: capture.ClientIP, RequestPayloadHash: capture.PayloadHash, RequestBody: capture.Body,
		APIKeyService: t.owner.keys, PricingUsageFields: capture.Pricing,
	})
}

// provideQoderCompatibleHTTP shares execution, billing, refresh, and request shutdown tracking.
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

// BindStickySession delegates session binding to the selector after a successful HTTP attempt.
func (p *qoderCompatibleExecution) BindStickySession(ctx context.Context, group *int64, hash string, id int64) error {
	return p.choices.BindStickySession(ctx, group, hash, id)
}
