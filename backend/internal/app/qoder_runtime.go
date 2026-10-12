package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	openaiwire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// qoderRuntime holds shared dependencies bound during application assembly.
type qoderRuntime struct {
	Choices     *selection.Generic
	Routes      *gatewayprovider.RoutePlanner
	Qoder       *gatewayprovider.QoderRuntime
	Refresh     *provideradapter.QoderRequestRefresh
	Billing     *admission.FundingAdmission
	Keys        *apikey.APIKeyService
	Completions *completion.UsageRecordWorkerPool
	Recorder    *completion.Recorder
}

// provideQoderRequestRefresh binds the shared refresh coordinator, provider store, and execution session cache.
func provideQoderRequestRefresh(store *providerpostgres.ProviderStore, tokens *provideradapter.QoderTokenProvider, coordinator *providercore.OAuthRefreshAPI, transport provideradapter.QoderTransport, profiles *egressprovider.TLSProfiles) *provideradapter.QoderRequestRefresh {
	return &provideradapter.QoderRequestRefresh{Store: store, Tokens: tokens, Coordinator: coordinator, Transport: transport, Profiles: profiles}
}

// provideQoderRuntime binds shared tokens, transport, and provider storage; the Qoder runtime owns its executor and conversations.
func provideQoderRuntime(tokens *provideradapter.QoderTokenProvider, transport provideradapter.QoderTransport, profiles *egressprovider.TLSProfiles, store *providerpostgres.ProviderStore, attributes *routing.ModelAttributeService) *gatewayprovider.QoderRuntime {
	return gatewayprovider.NewQoderRuntime(gatewayprovider.QoderRuntimeOptions{Attributes: attributes, Tokens: tokens, Transport: transport, Profiles: profiles, Health: store})
}

func (b *qoderRuntime) Prepare(ctx context.Context, request gateway.Request) (gateway.Request, error) {
	key := apikey.CopyAPIKey(request.Funding.Key)
	request.Route = b.Routes.PlanKey(ctx, key, request.Model).WithClientProtocol(protocol.ProtocolOpenAIChatCompletions)
	mapping := request.Route.Mapping()
	request.AttemptBody = request.Body
	if mapping.Mapped {
		request.AttemptBody = openaiwire.ReplaceModelInBody(request.Body, mapping.MappedModel)
	}
	return request, nil
}

func (b *qoderRuntime) Check(ctx context.Context, request gateway.Request, afterWait bool) error {
	if b.Billing == nil {
		return nil
	}
	key := apikey.CopyAPIKey(request.Funding.Key)
	return b.Billing.CheckKey(ctx, key, request.Funding.Subscription, "", afterWait)
}

func (b *qoderRuntime) Select(ctx context.Context, request gateway.Request, excluded map[int64]struct{}) (*gateway.Selection, error) {
	ctx = requeststate.WithRoutePlan(ctx, request.Route)
	key := apikey.CopyAPIKey(request.Funding.Key)
	plan := request.Route
	mapping := plan.Mapping()
	body := request.AttemptBody
	var project func(*gatewayprovider.SelectionResult, *gatewayprovider.ExecutionProvider, bool) *gateway.Selection
	project = func(selection *gatewayprovider.SelectionResult, provider *gatewayprovider.ExecutionProvider, refresh bool) *gateway.Selection {
		executor, input := b.Qoder.PrepareQoderTarget(qoder.RequestMetadata{APIKeyID: key.ID, ClaudeCode: request.Metadata.ClaudeCode, Headers: http.Header(request.Metadata.Headers).Clone()}, gatewayprovider.ExecutionRecord(provider), body, protocol.ProtocolOpenAIChatCompletions, request.Model)
		snapshot := gatewayprovider.ExecutionSnapshot(provider)
		candidate, _ := plan.ResolveCandidate(snapshot)
		selected := &gateway.Selection{Snapshot: snapshot, Plan: candidate, Acquired: selection.Acquired, Release: selection.ReleaseFunc, WaitPlan: selection.WaitPlan, Executor: executor, Input: input}
		if refresh {
			selected.Acquired = false
			selected.Release = nil
			if selected.WaitPlan == nil {
				selected.WaitWithoutCounter = true
				selected.WaitPlan = &scheduler.ProviderWaitPlan{ProviderID: provider.Record.ID, MaxConcurrency: provider.Record.Concurrency, Timeout: 30 * time.Second, MaxWaiting: 0}
			}
		}
		selected.Observe = func(result upstream.AttemptResult, err error) {
			b.Qoder.ObserveQoderFailure(ctx, gatewayprovider.ExecutionRecord(provider), err)
			var legacy *forwardcore.MessagesResult
			if err == nil || result.Served && result.HasUsage {
				legacy = forwardcore.MessagesFromAttempt(result)
			}
			b.Choices.ReportAdvancedProviderScheduleResult(selection, provider.Record.ID, err == nil, legacy)
		}
		selected.Switched = func() { b.Choices.RecordAdvancedProviderSwitch(selection) }
		selected.Refresh = func(ctx context.Context) (*gateway.Selection, error) {
			updated, err := b.Refresh.RefreshProviderSession(ctx, gatewayprovider.ExecutionRecord(provider))
			if err != nil || updated == nil {
				return nil, err
			}
			return project(selection, gatewayprovider.NewExecutionProvider(updated), true), nil
		}
		selected.Bind = func(ctx context.Context, _ upstream.AttemptResult) {
			bindCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = b.Choices.BindStickySession(bindCtx, key.GroupID, request.SessionHash, provider.Record.ID)
		}

		selected.Complete = func(callCtx context.Context, result upstream.AttemptResult) {
			snapshot := gatewayprovider.CaptureMessages(callCtx, &gatewayprovider.MessagesCapture{
				Result: forwardcore.MessagesFromAttempt(result),
				APIKey: key, User: key.User, Provider: gatewayprovider.ExecutionCompletionRecord(provider), Subscription: request.Funding.Subscription,
				InboundEndpoint: request.Metadata.InboundEndpoint, UpstreamEndpoint: request.Metadata.UpstreamEndpoint,
				UserAgent: request.Metadata.UserAgent, IPAddress: request.Metadata.ClientIP,
				RequestPayloadHash: billing.HashUsageRequestPayload(request.Body), RequestBody: request.Body,
				APIKeyService: b.Keys, PricingUsageFields: mapping.ToUsageFields(request.Model, result.UpstreamModel),
			})
			task := completion.WrapTaskContext(callCtx, func(workerCtx context.Context) {
				if err := b.Recorder.Record(workerCtx, snapshot, false); err != nil {
					logging.LegacyPrintf("handler.qoder_gateway", "record usage failed provider=%d: %v", snapshot.Provider.ID, err)
				}
			})
			if b.Completions != nil {
				b.Completions.Submit(task)
				return
			}
			completionCtx, cancel := context.WithTimeout(context.WithoutCancel(callCtx), 10*time.Second)
			defer cancel()
			task(completionCtx)
		}
		return selected
	}
	selection, err := b.Choices.SelectProviderWithLoadAwareness(ctx, key.GroupID, request.SessionHash, request.Model, excluded, "", request.UserID)
	if err != nil {
		return nil, err
	}
	return project(selection, selection.Provider, false), nil
}

func (b *qoderRuntime) CanRefresh(err error) bool { return qoder.MayRefreshAttempt(err) }

func (b *qoderRuntime) CanFailover(err error) bool { return qoder.MaySwitchAttempt(err) }

func (b *qoderRuntime) RefreshPending(err error) bool {
	return errors.Is(err, providercore.ErrQoderRefreshInProgress)
}

func (b *qoderRuntime) QueueFailure(kind string, err error) {
	logging.LegacyPrintf("handler.qoder_gateway", "%s wait counter failed: %v", kind, err)
}
