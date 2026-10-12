package httpapi

import (
	"context"
	"errors"
	"strings"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/openaiattempt"
	gatewaymedia "github.com/TokenFlux/TokenRouter/internal/gateway/media"
	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Bindings 绑定 WS 入站和每轮执行接口。
type Bindings struct {
	BindSticky   func(context.Context, *int64, string, int64) error
	Pricing      *admission.ModelPricing
	Common       openaiattempt.Bindings
	Dependencies gatewayhttp.OpenAIDependencies
	Prompt       gatewayhttp.MessagesPrompt
	Keys         interface {
		GetByKey(context.Context, string) (*apikey.APIKey, error)
		Reauthenticate(context.Context, *apikey.APIKey, apikey.AuthenticationInput) (*apikey.APIKey, error)
		AcquireRequest(context.Context, *apikey.APIKey) (context.Context, func(), time.Duration, error)
	}
	Subscriptions   admission.SubscriptionReader
	CheckFunding    func(context.Context, *apikey.APIKey, *billing.UserSubscription, string, bool) error
	Blocks          *session.CyberBlocks
	PlanRoute       func(context.Context, *apikey.APIKey, string) routing.RoutePlan
	Isolate         func(context.Context, *apikey.APIKey, int64, string, string) error
	ReportSelection func(*gatewayprovider.SelectionResult, int64, string, bool, *int)
	Stop429         func(*gatewayprovider.ExecutionProvider, int, int, *failover.OAuth429State) bool
	Credential      func(context.Context, *gin.Context, *gatewayprovider.ExecutionProvider) (string, string, error)
	ResolveRouting  func(context.Context, *int64, *gatewayprovider.ExecutionProvider, string, provider.OpenAIEndpointCapability) (string, error)
	BeginPreemption func(context.Context, *gin.Context, *gatewayprovider.ExecutionProvider, []byte) (context.Context, func(), bool)
	Relay           func(context.Context, *gin.Context, *coderws.Conn, *gatewayprovider.ExecutionProvider, string, []byte, *gatewayws.OpenAIIngressHooks) error
}

type openAIWSHTTPBackend struct{ bindings Bindings }

// openAIWSEntryAdapter 的每个方法仅接入一个已有能力，主循环与 turn 回调在 gateway/ws。
type openAIWSEntryAdapter struct {
	bindings     Bindings
	c            *gin.Context
	call         ResponsesWSCall
	key          *apikey.APIKey
	requestKey   *apikey.APIKey
	subject      authctx.AuthSubject
	subscription *billing.UserSubscription
	log          *zap.Logger
}

// openAIWSEntryTarget 向 WS 流程提供所选提供商的逐步执行接口。
type openAIWSEntryTarget struct {
	root      *openAIWSEntryAdapter
	provider  *gatewayprovider.ExecutionProvider
	selection *gatewayprovider.SelectionResult
	token     string
}

// wsEntryLogger 只持有日志句柄，后台完成回调不保留 HTTP 请求对象。
type wsEntryLogger struct{ log *zap.Logger }

// New 将 HTTP 升级接口与 WS 用例绑定。
func New(options ResponsesWSOptions, b Bindings) *ResponsesWSHandler {
	return NewResponsesWSHandler(options, openAIWSHTTPBackend{bindings: b}, b.Common.Support.Concurrency)
}

func (p openAIWSHTTPBackend) Access(c *gin.Context) (*gatewayws.EntryKey, bool) {
	key, ok := keyhttp.GetAPIKeyFromContext(c)
	if !ok {
		return nil, false
	}
	out := &gatewayws.EntryKey{
		ID:                key.ID,
		UserID:            key.UserID,
		GroupID:           key.GroupID,
		ModelMapping:      apikey.CloneModelMapping(key.ModelMapping),
		FastModePolicy:    key.FastModePolicy,
		RefreshFastPolicy: p.bindings.Keys != nil && strings.TrimSpace(key.Key) != "",
	}
	if key.Group != nil {
		out.Group = &gatewayws.EntryGroup{
			MaxReasoningEffort:          key.Group.MaxReasoningEffort,
			MaxReasoningEffortOverLimit: key.Group.MaxReasoningEffortOverLimit,
			ReasoningEffortMappings:     append([]routing.ReasoningEffortMapping(nil), key.Group.ReasoningEffortMappings...),
			ImagesAllowed:               routing.GroupAllowsResponsesImages(key.Group),
		}
	}
	return out, true
}

func (p openAIWSHTTPBackend) Transport(c *gin.Context) {
	gatewayhttp.SetOpenAIClientTransport(c, gatewayhttp.OpenAIClientTransportWS)
}

func (p openAIWSHTTPBackend) Error(c *gin.Context, status int, kind, message string) {
	gatewayhttp.DefaultOpenAIErrorOutput().WriteError(c, status, kind, message)
}

func (p openAIWSHTTPBackend) Dependencies(c *gin.Context, log *zap.Logger) bool {
	return p.bindings.Dependencies.Ensure(c, log)
}

func (p openAIWSHTTPBackend) SummarizeRead(err error) (string, string) {
	if closed, ok := errors.AsType[*gatewayws.ClientCloseError](err); ok {
		err = gatewayhttp.NewOpenAIWSClientCloseError(coderws.StatusCode(closed.Status), closed.Reason, closed.Cause)
	}
	return gatewayhttp.SummarizeWSCloseErrorForLog(err)
}

func (p openAIWSHTTPBackend) Entry(c *gin.Context, call ResponsesWSCall) gatewayws.EntryPorts {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	subject, _ := authctx.GetAuthSubjectFromContext(c)
	return &openAIWSEntryAdapter{bindings: p.bindings, c: c, call: call, key: key, subject: subject, log: call.Logger}
}

func (p *openAIWSEntryAdapter) Logger() gatewayws.EntryLogger { return wsEntryLogger{p.log} }
func (p *openAIWSEntryAdapter) SetLogger(log gatewayws.EntryLogger) {
	// 固定适配器的 With 返回同类记录器；违反此内部约定仍作为编程错误失败。
	typed, ok := log.(wsEntryLogger)
	if !ok {
		panic("unexpected websocket entry logger type")
	}
	p.log = typed.log
}

func (p *openAIWSEntryAdapter) ClassifyPrevious(id string) string {
	return openai.ClassifyOpenAIPreviousResponseIDKind(id)
}

func (p *openAIWSEntryAdapter) Platform() string {
	return gatewayhttp.OpenAICompatibleRequestPlatform(p.key)
}

func (p *openAIWSEntryAdapter) Prompt(ctx context.Context, body []byte) []byte {
	return p.bindings.Prompt.ApplyUserPromptReplacementToBody(ctx, body, "openai_responses")
}

func (p *openAIWSEntryAdapter) Redirect(ctx context.Context, model string) (context.Context, string) {
	return gatewayhttp.APIKeyModelRedirectContext(ctx, p.key, model)
}

func (p *openAIWSEntryAdapter) BindContext(ctx context.Context) {
	p.c.Request = p.c.Request.WithContext(ctx)
}

func (p *openAIWSEntryAdapter) ObserveFirst(model string) {
	gatewayhttp.SetOpsRequestContext(p.c, model, true)
	gatewayhttp.SetOpsEndpointContext(p.c, "", int16(usage.RequestTypeWSV2))
}

func (p *openAIWSEntryAdapter) CaptureCyber(body []byte) gatewayws.EntryCyberSnapshot {
	gatewayhttp.SetOpenAICyberWarningRequestSnapshot(p.c, moderation.ContentModerationProtocolOpenAIResponses, body)
	return gatewayws.EntryCyberSnapshot{Excerpt: gatewayhttp.CurrentOpenAICyberWarningPromptExcerpt(p.c), Input: gatewayhttp.CurrentOpenAICyberWarningSnapshot(p.c)}
}

func (p *openAIWSEntryAdapter) Moderate(_ context.Context, model string, body []byte) *moderation.Decision {
	return gatewayhttp.RunContentModeration(gatewayhttp.GatewayModerationEndpoints{}, p.c, p.log, p.bindings.Common.Support.Moderation, p.key, p.subject, moderation.ContentModerationProtocolOpenAIResponses, model, body)
}

func (p *openAIWSEntryAdapter) ModerationError(ctx context.Context, d *moderation.Decision) {
	gatewayhttp.WriteResponsesWSModeration(ctx, p.call.Conn, d)
}

func (p *openAIWSEntryAdapter) BlockedSession(ctx context.Context, body []byte) string {
	return gatewayhttp.FindBlockedCyberSession(ctx, p.bindings.Blocks, p.key.ID, p.c, body)
}

func (p *openAIWSEntryAdapter) BlockedError(ctx context.Context) {
	gatewayhttp.WriteResponsesWSCyberBlocked(ctx, p.call.Conn, moderationflow.SessionBlockedClientMessage)
}

func (p *openAIWSEntryAdapter) BlockedMessage() string {
	return moderationflow.SessionBlockedClientMessage
}

func (p *openAIWSEntryAdapter) BlockedOps(model, key string) {
	p.bindings.Common.Support.Cyber.EnqueueBlocked(p.c, p.key, model, key)
}

func (p *openAIWSEntryAdapter) Plan(ctx context.Context, model string) (context.Context, routing.GroupMappingResult) {
	plan := p.bindings.PlanRoute(ctx, p.key, model)
	return requeststate.WithRoutePlan(ctx, plan), plan.Mapping()
}

func (p *openAIWSEntryAdapter) ImageIntent(model string, body []byte, mapping routing.GroupMappingResult) ([]byte, string, bool) {
	return gatewayhttp.GroupMappedImageIntent("/v1/responses", model, body, mapping, p.Platform(), p.bindings.Common.Forward.ReplaceModelInBody)
}

func (p *openAIWSEntryAdapter) ExplicitImage(model string, body []byte) bool {
	return gatewayprovider.ImageIntent().IsExplicitImageGenerationIntent("/v1/responses", model, body)
}

func (p *openAIWSEntryAdapter) ImageContext(ctx context.Context) context.Context {
	return requeststate.WithOpenAIImageGenerationIntent(ctx)
}

func (p *openAIWSEntryAdapter) ImagesAllowed() bool {
	return routing.GroupAllowsResponsesImages(p.key.Group)
}

func (p *openAIWSEntryAdapter) ImageDeniedMessage() string {
	return gatewaymedia.ImageGenerationPermissionMessage
}

func (p *openAIWSEntryAdapter) PolicyDenied() {
	gatewayhttp.MarkOpsClientBusinessLimited(p.c, gatewayhttp.OpsClientBusinessLimitedReasonLocalPolicyDenied)
}

func (p *openAIWSEntryAdapter) FeatureDenied() {
	gatewayhttp.MarkOpsClientBusinessLimited(p.c, gatewayhttp.OpsClientBusinessLimitedReasonLocalFeatureGate)
}

// AcquireKey 为当前轮次预占 Key 请求额度，返回租约取消信号。
func (p *openAIWSEntryAdapter) AcquireKey(ctx context.Context) (context.Context, func(), error) {
	if p.bindings.Keys == nil {
		return ctx, nil, p.CloseError(1011, "API key request limiter unavailable", apikey.ErrKeyLimiterUnavailable)
	}
	key := p.requestKey
	if key == nil {
		key = p.key
	}
	admitted, release, _, err := p.bindings.Keys.AcquireRequest(ctx, key)
	if err != nil {
		status := 1011
		if errors.Is(err, apikey.ErrKeyConcurrencyExceeded) || errors.Is(err, apikey.ErrKeyRPMExceeded) {
			status = 1013
		}
		return ctx, nil, p.CloseError(status, err.Error(), err)
	}
	return admitted, release, nil
}

func (p *openAIWSEntryAdapter) AcquireUser(ctx context.Context) (func(), bool, error) {
	return p.bindings.Common.Support.Concurrency.TryAcquireUserSlotForAPIKey(ctx, p.subject.UserID, p.subject.Concurrency, p.key.ID)
}

func (p *openAIWSEntryAdapter) AcquireProvider(ctx context.Context, id int64, limit int) (func(), bool, error) {
	return p.bindings.Common.Support.Concurrency.TryAcquireProviderSlot(ctx, id, limit)
}

func (p *openAIWSEntryAdapter) WrapRelease(ctx context.Context, release func()) func() {
	return scheduler.WrapRelease(ctx, scheduler.ReleaseOnCancel, release)
}

func (p *openAIWSEntryAdapter) LoadSubscription() {
	p.subscription, _ = gatewayhttp.SubscriptionFromContext(p.c)
}

func (p *openAIWSEntryAdapter) Eligibility(ctx context.Context) error {
	return p.bindings.CheckFunding(ctx, p.key, p.subscription, "", false)
}

// AuthorizeTurn 使用当前身份和订阅检查下一轮，已放行轮次使用各自的完成快照。
func (p *openAIWSEntryAdapter) AuthorizeTurn(ctx context.Context) error {
	if p.bindings.Keys == nil || p.bindings.CheckFunding == nil {
		return apikey.ErrAuthenticationStopped
	}
	key, err := p.bindings.Keys.Reauthenticate(ctx, p.key, apikey.AuthenticationInput{
		ClientIP: p.call.ClientIP, CheckMemberLimits: true,
	})
	if err != nil {
		return err
	}
	if key == nil || key.Group == nil || !key.Group.AllowsClientProtocol(protocol.ProtocolResponsesWebSocket) {
		return errors.New("responses websocket protocol is not allowed by group")
	}
	funding, err := admission.ResolveFundingFromKey(ctx, key, p.bindings.Subscriptions, true)
	if err != nil {
		return err
	}
	if err := p.bindings.CheckFunding(ctx, key, funding.Subscription, "", false); err != nil {
		return err
	}
	p.requestKey = key
	return nil
}

func (p *openAIWSEntryAdapter) SessionHash(body []byte, seed string) string {
	return gatewayhttp.GenerateOpenAISessionHashWithFallback(p.c, body, seed)
}

func (p *openAIWSEntryAdapter) ExplicitHash(body []byte) string {
	return gatewayhttp.GenerateExplicitOpenAISessionHash(p.c, body)
}

func (p *openAIWSEntryAdapter) Isolate(ctx context.Context, source, hash string) error {
	return p.bindings.Isolate(ctx, p.key, p.subject.UserID, source, hash)
}

func (p *openAIWSEntryAdapter) IsolationError(ctx context.Context, err error) {
	gatewayhttp.WriteResponsesWSIsolation(ctx, p.call.Conn, err)
}

func (p *openAIWSEntryAdapter) IsolationReason(err error) string {
	return gatewayhttp.ResponsesWSIsolationCloseReason(err)
}

func (p *openAIWSEntryAdapter) Guardian(ctx context.Context, body []byte, model string) context.Context {
	return gatewayhttp.WithOpenAIGuardianParentAffinity(ctx, p.c, body, model)
}

func (p *openAIWSEntryAdapter) Select(ctx context.Context, previous, hash, model string, excluded map[int64]struct{}, responses, move bool, platform string) (*gatewayws.EntrySelection, gatewayws.EntryDecision, error) {
	capability := provider.OpenAIEndpointCapabilityTextGeneration
	if responses {
		capability = provider.OpenAIEndpointCapabilityResponses
	}
	selection, decision, err := p.bindings.Common.Selection.SelectProviderWithSchedulerForCapability(ctx, p.key.GroupID, previous, hash, model, excluded, egress.OpenAIUpstreamTransportResponsesWebsocketV2Ingress, capability, false, move, platform)
	d := gatewayws.EntryDecision{Layer: decision.Layer, CandidateCount: decision.CandidateCount, StickyPreviousHit: decision.StickyPreviousHit}
	if selection == nil {
		return nil, d, err
	}
	out := &gatewayws.EntrySelection{Acquired: selection.Acquired, ReleaseFunc: selection.ReleaseFunc, WaitPlan: selection.WaitPlan}
	if a := selection.Provider; a != nil {
		out.Provider = &gatewayws.EntryProvider{
			ProviderSnapshot: provider.ProviderSnapshot{ID: a.Record.ID, Platform: a.Record.Platform, Type: a.Record.Type, Concurrency: a.Record.Concurrency},
			Name:             a.Record.Name,
			Shadow:           a.View().IsShadow(),
		}
		out.Target = &openAIWSEntryTarget{root: p, provider: a, selection: selection}
	}
	return out, d, err
}

func (p *openAIWSEntryAdapter) SelectionLogError(err error, platform string) error {
	return gatewayhttp.OpenAICompatibleSelectionErrorForLog(err, platform)
}

func (p *openAIWSEntryAdapter) BindSticky(ctx context.Context, hash string, id int64) error {
	if p.bindings.BindSticky != nil {
		return p.bindings.BindSticky(ctx, p.key.GroupID, hash, id)
	}
	return p.bindings.Common.Support.Sticky.BindStickySession(ctx, p.key.GroupID, hash, id)
}

func (p *openAIWSEntryAdapter) RefreshFast(ctx context.Context) (string, bool) {
	key, err := p.bindings.Keys.GetByKey(ctx, p.key.Key)
	if err != nil || key == nil {
		return "", false
	}
	return apikey.NormalizeAPIKeyFastModePolicy(key.FastModePolicy)
}

func (p *openAIWSEntryAdapter) Failover(err error) (*gatewayws.EntryFailure, bool) {
	var failure *forwardcore.UpstreamFailoverError
	if !errors.As(err, &failure) || failure == nil {
		return nil, false
	}
	return &gatewayws.EntryFailure{Err: failure, StatusCode: failure.StatusCode, ReportScheduleFailure: failure.ShouldReportProviderScheduleFailure(), RetryNext: failure.ShouldRetryNextProvider()}, true
}

func (p *openAIWSEntryAdapter) CloseFailover(failure *gatewayws.EntryFailure) {
	var old *forwardcore.UpstreamFailoverError
	if failure != nil {
		_ = errors.As(failure.Err, &old)
	}
	gatewayhttp.CloseResponsesWSFailure(p.c, p.call.Conn, FailoverPresentation(old), gatewayhttp.MarkOpsStreamFailure)
}

func (p *openAIWSEntryAdapter) Close(status int, reason string) {
	gatewayhttp.CloseResponsesWS(p.call.Conn, coderws.StatusCode(status), reason)
}

func (p *openAIWSEntryAdapter) CloseError(status int, reason string, err error) error {
	return gatewayhttp.NewOpenAIWSClientCloseError(coderws.StatusCode(status), reason, err)
}

func (p *openAIWSEntryAdapter) CloseInfo(err error) gatewayws.EntryClose {
	return gatewayhttp.ResponsesWSCloseInfo(err)
}

func (p *openAIWSEntryAdapter) SessionPreempted(err error) bool {
	return gatewayws.IsSessionPreemptedError(err)
}

func (p *openAIWSEntryAdapter) RemovePrevious(body []byte) []byte {
	return openai.RemovePreviousResponseIDFromBody(body)
}

func (p *openAIWSEntryAdapter) LocalPolicyError(err error) bool {
	var policy *routing.ReasoningEffortOverLimitError
	return errors.As(err, &policy)
}

func (p *openAIWSEntryAdapter) ReportFailure(err error) bool {
	return gatewayws.EntryShouldReportFailure(err)
}

func (p *openAIWSEntryAdapter) EndedByClient(err error) bool {
	return gatewayhttp.ResponsesWSEndedByClient(err, p.CloseInfo(err))
}

func (p *openAIWSEntryAdapter) ClearCyber() {
	gatewayhttp.ClearCyberTurnState(p.c, gatewayhttp.ClearOpsCyberPolicy)
}

func (p *openAIWSEntryAdapter) CompletionRecorder() gatewayws.EntryCompletion {
	return p.bindings.Common.Recorder
}

func (p *openAIWSEntryAdapter) CompletionObserver() func(int64, string, error) {
	log := p.log
	return func(id int64, requestID string, err error) {
		log.Error("openai.websocket_record_usage_failed", zap.Int64("provider_id", id), zap.String("request_id", requestID), zap.Error(err))
	}
}

func (p *openAIWSEntryAdapter) SubmitCompletion(result *gatewayws.ForwardResult, task func(context.Context)) {
	images := 0
	if result != nil {
		images = result.ImageCount
	}
	p.bindings.Common.Support.Submission.SubmitImages(p.c, images, task)
}

func (t *openAIWSEntryTarget) MappedModel(model string) string {
	return gatewayprovider.ExecutionModelPolicy(t.provider).Mapped(model)
}

func (t *openAIWSEntryTarget) Report(model string, ok bool, first *int) {
	t.root.bindings.ReportSelection(t.selection, t.provider.Record.ID, model, ok, first)
}

func (t *openAIWSEntryTarget) Switched() {
	t.root.bindings.Common.Selection.RecordOpenAIProviderSwitchForSelection(t.selection)
}

func (t *openAIWSEntryTarget) Stop429(status, count int, state *failover.OAuth429State) bool {
	return t.root.bindings.Stop429(t.provider, status, count, state)
}

func (t *openAIWSEntryTarget) Credential(ctx context.Context) error {
	token, _, err := t.root.bindings.Credential(ctx, t.root.c, t.provider)
	if err == nil {
		t.token = token
	}
	return err
}

func (t *openAIWSEntryTarget) EnforceClient(ctx context.Context, first []byte) error {
	router := t.root.bindings.Common.Forward.MatchOpenAITLSFingerprintRouterForRequest(t.root.c, t.provider)
	return t.root.bindings.Common.Forward.EnforceOpenAIClientPolicyForRequest(ctx, t.root.c, t.provider, first, router)
}

func (t *openAIWSEntryTarget) ResolveRouting(ctx context.Context, model string, responses bool) (string, error) {
	capability := provider.OpenAIEndpointCapabilityTextGeneration
	if responses {
		capability = provider.OpenAIEndpointCapabilityResponses
	}
	mapped, err := t.root.bindings.ResolveRouting(ctx, t.root.key.GroupID, t.provider, model, capability)
	if err != nil {
		return "", err
	}
	err = gatewayhttp.CheckTextModelPricing(ctx, t.root.bindings.Pricing, t.root.key, t.provider, model, mapped, nil, protocol.ProtocolResponsesWebSocket)
	if err != nil {
		gatewayhttp.MarkOpsClientBusinessLimited(t.root.c, gatewayhttp.OpsClientBusinessLimitedReasonLocalPolicyDenied)
		return "", err
	}
	return mapped, nil
}

func (t *openAIWSEntryTarget) Warning(_ context.Context, model string, status int, body []byte, message string, snapshot gatewayws.EntryCyberSnapshot) {
	p := t.root
	p.bindings.Common.Support.RecordOpenAICyberWarningWithSnapshot(p.c, p.log, p.key, t.provider, model, status, body, message, snapshot.Excerpt, snapshot.Input)
}

func (t *openAIWSEntryTarget) RecordMarked(_ context.Context, model string, failed bool, body []byte, mapping routing.PricingUsageFields, hash string) bool {
	p := t.root
	return p.bindings.Common.Support.RecordCyberPolicyIfMarked(p.c, p.key, t.provider, p.subscription, model, failed, body, mapping, hash)
}

func (t *openAIWSEntryTarget) UpdateUsage(ctx context.Context, headers map[string][]string) {
	t.root.bindings.Common.Selection.UpdateCodexUsageSnapshotFromHeaders(ctx, t.provider.Record.ID, headers)
}

func (t *openAIWSEntryTarget) PrepareCompletion(ctx context.Context, result *gatewayws.ForwardResult, capture gatewayws.TurnCapture, model string, mapping routing.GroupMappingResult, body []byte, cyber bool) *completion.Input {
	p := t.root
	legacy := gatewayprovider.ForwardResultFromWS(result)
	// 完成快照携带本轮请求 ID 和模型映射，后台任务通过本轮记录器补充诊断信息。
	captureContext := gatewayhttp.PropagateAPIKeyModelRedirectTrace(gatewayhttp.CompletionContext(p.c), ctx)
	if id := telemetry.RequestIDValue(ctx); id != "" {
		captureContext = context.WithValue(captureContext, telemetry.RequestID, id)
	}
	captureContext = telemetry.CopyRequestCapture(captureContext, ctx)
	return gatewayprovider.CaptureOpenAI(captureContext, &gatewayprovider.OpenAICapture{
		Result: legacy, APIKey: p.key, User: p.key.User, Provider: gatewayprovider.ExecutionCompletionRecord(t.provider), Subscription: p.subscription,
		InboundEndpoint: gatewayhttp.GetInboundEndpoint(p.c), UpstreamEndpoint: openaiattempt.ResolveOpenAIUpstreamEndpoint(p.c, t.provider, legacy), UserAgent: p.call.UserAgent, IPAddress: p.call.ClientIP,
		RequestPayloadHash: billing.HashUsageRequestPayload(body), RequestBody: append([]byte(nil), body...), PricingAt: capture.StartedAt, APIKeyService: p.bindings.Common.Support.Quota,
		ClientSessionID: gatewayhttp.ExtractClientSessionID(p.c), PricingUsageFields: mapping.ToUsageFields(model, result.UpstreamModel), CyberBlocked: cyber,
	})
}

func (t *openAIWSEntryTarget) BeginPreemption(ctx context.Context, first []byte) (context.Context, func(), bool) {
	return t.root.bindings.BeginPreemption(ctx, t.root.c, t.provider, first)
}

func (t *openAIWSEntryTarget) Run(ctx context.Context, client gatewayws.ClientSocket, first []byte, hooks *gatewayws.EntryHooks) error {
	frames, ok := client.(gatewayhttp.WSClientFrames)
	if !ok {
		return errors.New("unsupported websocket HTTP frame adapter")
	}
	old := &gatewayws.OpenAIIngressHooks{
		ContextForTurn:              hooks.ContextForTurn,
		ClientLifecycleContext:      hooks.ClientLifecycleContext,
		InitialRequestModel:         hooks.InitialRequestModel,
		InitialTurnStartedAt:        hooks.InitialTurnStartedAt,
		MaxReasoningEffort:          hooks.MaxReasoningEffort,
		MaxReasoningEffortOverLimit: hooks.MaxReasoningEffortOverLimit,
		ReasoningEffortMappings:     hooks.ReasoningEffortMappings,
		ResolveFastModePolicy:       hooks.ResolveFastModePolicy,
		ResolveRoutingModel:         hooks.ResolveRoutingModel,
		TurnStarted:                 hooks.TurnStarted,
		BeforeTurn:                  hooks.BeforeTurn,
		BeforeRequest:               hooks.BeforeRequest,
		OnUpstreamError:             hooks.OnUpstreamError,
	}
	if hooks.AfterTurn != nil {
		old.AfterTurn = func(c gatewayws.OpenAITurnCapture) {
			hooks.AfterTurn(gatewayws.TurnCapture{Turn: c.Turn, StartedAt: c.StartedAt, RequestBody: c.RequestBody, OriginalModel: c.OriginalModel, PreviousResponseID: c.PreviousResponseID, Result: gatewayprovider.ProjectWSResult(c.Result), Err: c.Err, PayloadSource: c.PayloadSource})
		}
	}
	return t.root.bindings.Relay(ctx, t.root.c, frames.Conn, t.provider, t.token, first, old)
}

func (t *openAIWSEntryTarget) LogFailure(err error) {
	status, reason := gatewayhttp.SummarizeWSCloseErrorForLog(err)
	fields := []zap.Field{zap.Int64("provider_id", t.provider.Record.ID), zap.Error(err), zap.String("close_status", status), zap.String("close_reason", reason)}
	fields = openaiattempt.AppendOpenAIProviderProxyLogFields(fields, t.provider)
	t.root.log.Warn("openai.websocket_proxy_failed", fields...)
}

func wsEntryLogFields(fields []gatewayws.EntryField) []zap.Field {
	out := make([]zap.Field, 0, len(fields))
	for _, f := range fields {
		switch f.Kind {
		case 1:
			out = append(out, zap.String(f.Key, f.Text))
		case 2:
			out = append(out, zap.Int64(f.Key, f.Integer))
		case 3:
			out = append(out, zap.Bool(f.Key, f.Flag))
		case 4:
			out = append(out, zap.Error(f.Err))
		}
	}
	return out
}

func (l wsEntryLogger) With(fields ...gatewayws.EntryField) gatewayws.EntryLogger {
	return wsEntryLogger{l.log.With(wsEntryLogFields(fields)...)}
}

func (l wsEntryLogger) Info(message string, fields ...gatewayws.EntryField) {
	l.log.Info(message, wsEntryLogFields(fields)...)
}

func (l wsEntryLogger) Warn(message string, fields ...gatewayws.EntryField) {
	l.log.Warn(message, wsEntryLogFields(fields)...)
}

func (l wsEntryLogger) Error(message string, fields ...gatewayws.EntryField) {
	l.log.Error(message, wsEntryLogFields(fields)...)
}

func (l wsEntryLogger) Debug(message string, fields ...gatewayws.EntryField) {
	l.log.Debug(message, wsEntryLogFields(fields)...)
}

// FailoverPresentation 只转换错误展示字段，HTTP 映射由原生 Adapter 唯一持有。
func FailoverPresentation(err *forwardcore.UpstreamFailoverError) *gatewayhttp.ResponsesWSFailure {
	if err == nil {
		return nil
	}
	return &gatewayhttp.ResponsesWSFailure{
		Reason:            string(err.Reason),
		StatusCode:        err.StatusCode,
		ProviderAuth:      err.Stage == forwardcore.GatewayFailureStageProviderAuth,
		CredentialMessage: forwardcore.GrokCredentialUnavailableClientMessage,
	}
}
