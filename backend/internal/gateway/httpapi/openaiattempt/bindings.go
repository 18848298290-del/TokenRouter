package openaiattempt

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// ForwardPorts executes one provider attempt; gateway/text owns provider switching.
type ForwardPorts struct {
	EnforceOpenAIClientPolicyForRequest func(ctx context.Context, c *gin.Context, provider *gatewaycapture.ExecutionProvider, body []byte, tlsRouterMatch egress.TLSFingerprintRouterMatchResult) error
	Forward                             func(ctx context.Context, c *gin.Context, provider *gatewaycapture.ExecutionProvider, body []byte) (*forwardcore.OpenAIResult, error)
	ForwardAsAnthropic                  func(ctx context.Context, c *gin.Context, provider *gatewaycapture.ExecutionProvider, body []byte, promptCacheKey, defaultMappedModel string, tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult) (*forwardcore.OpenAIResult, error)
	ForwardAsChatCompletions            func(
		ctx context.Context,
		c *gin.Context,
		provider *gatewaycapture.ExecutionProvider,
		body []byte,
		promptCacheKey string,
		defaultMappedModel string,
		tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult,
	) (*forwardcore.OpenAIResult, error)
	MatchOpenAITLSFingerprintRouterForRequest func(c *gin.Context, provider *gatewaycapture.ExecutionProvider) egress.TLSFingerprintRouterMatchResult
	ReplaceModelInBody                        func(body []byte, newModel string) []byte
}

// SelectionPorts shares scheduler and health services.
type SelectionPorts struct {
	SelectImages    func(context.Context, *int64, string, string, map[int64]struct{}, providercore.OpenAIImagesCapability) (*gatewaycapture.SelectionResult, scheduler.PlatformDecision, error)
	RecordSwitch    func()
	ReportSelection func(*gatewaycapture.SelectionResult, int64, string, bool, *int)

	ObserveOpenAIProviderHealthFailure       func(ctx context.Context, provider *gatewaycapture.ExecutionProvider, observedErr error) bool
	RecordOpenAIProviderSwitchForSelection   func(selection *gatewaycapture.SelectionResult)
	ReportOpenAIProviderScheduleResult       func(providerOrID *gatewaycapture.ExecutionProvider, model string, success bool, firstTokenMs *int, observedErr ...error) bool
	SelectProviderWithSchedulerForCapability func(
		ctx context.Context,
		groupID *int64,
		previousResponseID string,
		sessionHash string,
		requestedModel string,
		excludedIDs map[int64]struct{},
		requiredTransport egress.OpenAIUpstreamTransport,
		requiredCapability providercore.OpenAIEndpointCapability,
		requireCompact bool,
		previousResponseCanMove bool,
		platformOverride ...string,
	) (*gatewaycapture.SelectionResult, scheduler.PlatformDecision, error)
	SelectProviderWithSchedulerForCapabilityAndRoutingModel func(
		ctx context.Context,
		groupID *int64,
		previousResponseID string,
		sessionHash string,
		requestedModel string,
		routingModel string,
		excludedIDs map[int64]struct{},
		requiredTransport egress.OpenAIUpstreamTransport,
		requiredCapability providercore.OpenAIEndpointCapability,
		requireCompact bool,
		previousResponseCanMove bool,
		platformOverride ...string,
	) (*gatewaycapture.SelectionResult, scheduler.PlatformDecision, error)
	UpdateCodexUsageSnapshotFromHeaders func(ctx context.Context, providerID int64, headers http.Header)
}

// Bindings holds shared resources and execution interfaces injected at construction.
type Bindings struct {
	Fallback          GroupFallbackPorts
	Sessions          SessionPorts
	Forward           ForwardPorts
	Selection         SelectionPorts
	Support           *Support
	Recorder          *completion.Recorder
	Diagnoser         routing.ModelAvailabilityDiagnoser
	ResolvedDiagnoser routing.ModelAvailabilityDiagnoser
}
type openAIExecutionDependencies struct {
	fallback                            GroupFallbackPorts
	sessions                            SessionPorts
	recorder                            *completion.Recorder
	apiKeyService                       gatewaycapture.QuotaUpdater
	diagnoser                           routing.ModelAvailabilityDiagnoser
	resolvedDiagnoser                   routing.ModelAvailabilityDiagnoser
	enforceOpenAIClientPolicyForRequest func(ctx context.Context, c *gin.Context, provider *gatewaycapture.ExecutionProvider, body []byte, tlsRouterMatch egress.TLSFingerprintRouterMatchResult) error
	forward                             func(ctx context.Context, c *gin.Context, provider *gatewaycapture.ExecutionProvider, body []byte) (*forwardcore.OpenAIResult, error)
	forwardAsAnthropic                  func(ctx context.Context, c *gin.Context, provider *gatewaycapture.ExecutionProvider, body []byte, promptCacheKey, defaultMappedModel string, tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult) (*forwardcore.OpenAIResult, error)
	forwardAsChatCompletions            func(
		ctx context.Context,
		c *gin.Context,
		provider *gatewaycapture.ExecutionProvider,
		body []byte,
		promptCacheKey string,
		defaultMappedModel string,
		tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult,
	) (*forwardcore.OpenAIResult, error)
	matchOpenAITLSFingerprintRouterForRequest func(c *gin.Context, provider *gatewaycapture.ExecutionProvider) egress.TLSFingerprintRouterMatchResult
	observeOpenAIProviderHealthFailure        func(ctx context.Context, provider *gatewaycapture.ExecutionProvider, observedErr error) bool
	recordOpenAIProviderSwitchForSelection    func(selection *gatewaycapture.SelectionResult)
	replaceModelInBody                        func(body []byte, newModel string) []byte
	reportOpenAIProviderScheduleResult        func(providerOrID *gatewaycapture.ExecutionProvider, model string, success bool, firstTokenMs *int, observedErr ...error) bool
	selectProviderWithSchedulerForCapability  func(
		ctx context.Context,
		groupID *int64,
		previousResponseID string,
		sessionHash string,
		requestedModel string,
		excludedIDs map[int64]struct{},
		requiredTransport egress.OpenAIUpstreamTransport,
		requiredCapability providercore.OpenAIEndpointCapability,
		requireCompact bool,
		previousResponseCanMove bool,
		platformOverride ...string,
	) (*gatewaycapture.SelectionResult, scheduler.PlatformDecision, error)
	selectProviderWithSchedulerForCapabilityAndRoutingModel func(
		ctx context.Context,
		groupID *int64,
		previousResponseID string,
		sessionHash string,
		requestedModel string,
		routingModel string,
		excludedIDs map[int64]struct{},
		requiredTransport egress.OpenAIUpstreamTransport,
		requiredCapability providercore.OpenAIEndpointCapability,
		requireCompact bool,
		previousResponseCanMove bool,
		platformOverride ...string,
	) (*gatewaycapture.SelectionResult, scheduler.PlatformDecision, error)
	updateCodexUsageSnapshotFromHeaders func(ctx context.Context, providerID int64, headers http.Header)
	acquireResponsesProviderSlot        func(
		c *gin.Context,
		groupID *int64,
		sessionHash string,
		selection *gatewaycapture.SelectionResult,
		reqStream bool,
		streamStarted *bool,
		reqLog *zap.Logger,
	) (func(), bool)
	anthropicStreamingAwareError   func(c *gin.Context, status int, errType, message string, streamStarted bool)
	deriveOpenAIForwardAttemptBody func(
		reqLog *zap.Logger,
		canonicalBody []byte,
		provider *gatewaycapture.ExecutionProvider,
		state *openAIPassthroughFailoverState,
	) []byte
	ensureAnthropicErrorResponse         func(c *gin.Context, streamStarted bool) bool
	ensureOpenAIForwardErrorResponse     func(c *gin.Context, streamStarted bool, err error) bool
	ensureOpenAIStreamReadErrorResponse  func(c *gin.Context, err error, streamStarted bool) bool
	handleAnthropicFailoverExhausted     func(c *gin.Context, failoverErr *forwardcore.UpstreamFailoverError, streamStarted bool)
	handleFailoverExhausted              func(c *gin.Context, failoverErr *forwardcore.UpstreamFailoverError, streamStarted bool)
	handleFailoverExhaustedSimple        func(c *gin.Context, statusCode int, streamStarted bool)
	handleOpenAISelectionBusinessError   func(c *gin.Context, err error, streamStarted bool) bool
	handleStreamingAwareError            func(c *gin.Context, status int, errType, message string, streamStarted bool)
	recordCyberPolicyIfMarked            func(c *gin.Context, apiKey *apikey.APIKey, provider *gatewaycapture.ExecutionProvider, subscription *billing.UserSubscription, model string, forwardErrored bool, cyberBlockArg []byte, pricingFields routing.PricingUsageFields, requestPayloadHash string, nativeCompaction ...bool) bool
	recordOpenAICyberWarning             func(c *gin.Context, reqLog *zap.Logger, apiKey *apikey.APIKey, provider *gatewaycapture.ExecutionProvider, model string, statusCode int, responseBody []byte, warningText string)
	recordOpenAIForwardErrorCyberWarning func(c *gin.Context, reqLog *zap.Logger, apiKey *apikey.APIKey, provider *gatewaycapture.ExecutionProvider, model string, statusCode int, err error) bool
	submitOpenAIUsageRecordTask          func(c *gin.Context, result *forwardcore.OpenAIResult, task completion.UsageRecordTask)
}

// New stores dependencies; Open creates per-request attempt state.
func New(b Bindings) *Runtime {
	support := b.Support
	output := gatewayhttp.DefaultOpenAIErrorOutput()
	d := &openAIExecutionDependencies{
		fallback: b.Fallback,
		sessions: b.Sessions,
		recorder: b.Recorder, apiKeyService: support.Quota, diagnoser: b.Diagnoser, resolvedDiagnoser: b.ResolvedDiagnoser,
		enforceOpenAIClientPolicyForRequest:                     b.Forward.EnforceOpenAIClientPolicyForRequest,
		forward:                                                 b.Forward.Forward,
		forwardAsAnthropic:                                      b.Forward.ForwardAsAnthropic,
		forwardAsChatCompletions:                                b.Forward.ForwardAsChatCompletions,
		matchOpenAITLSFingerprintRouterForRequest:               b.Forward.MatchOpenAITLSFingerprintRouterForRequest,
		replaceModelInBody:                                      b.Forward.ReplaceModelInBody,
		observeOpenAIProviderHealthFailure:                      b.Selection.ObserveOpenAIProviderHealthFailure,
		recordOpenAIProviderSwitchForSelection:                  b.Selection.RecordOpenAIProviderSwitchForSelection,
		reportOpenAIProviderScheduleResult:                      b.Selection.ReportOpenAIProviderScheduleResult,
		selectProviderWithSchedulerForCapability:                b.Selection.SelectProviderWithSchedulerForCapability,
		selectProviderWithSchedulerForCapabilityAndRoutingModel: b.Selection.SelectProviderWithSchedulerForCapabilityAndRoutingModel,
		updateCodexUsageSnapshotFromHeaders:                     b.Selection.UpdateCodexUsageSnapshotFromHeaders,
		acquireResponsesProviderSlot:                            support.AcquireResponsesProviderSlot,
		ensureAnthropicErrorResponse:                            support.EnsureAnthropicErrorResponse,
		ensureOpenAIStreamReadErrorResponse:                     support.EnsureOpenAIStreamReadErrorResponse,
		handleAnthropicFailoverExhausted:                        support.HandleAnthropicFailoverExhausted,
		handleFailoverExhausted:                                 support.HandleFailoverExhausted,
		handleFailoverExhaustedSimple:                           support.HandleFailoverExhaustedSimple,
		handleOpenAISelectionBusinessError:                      support.HandleOpenAISelectionBusinessError,
		recordOpenAICyberWarning:                                support.RecordOpenAICyberWarning,
		recordOpenAIForwardErrorCyberWarning:                    support.RecordOpenAIForwardErrorCyberWarning,
		anthropicStreamingAwareError:                            output.WriteAnthropicStreamingError,
		ensureOpenAIForwardErrorResponse:                        output.EnsureResponse,
		handleStreamingAwareError:                               output.StreamError,
		deriveOpenAIForwardAttemptBody:                          deriveOpenAIForwardAttemptBody,
		recordCyberPolicyIfMarked: func(c *gin.Context, key *apikey.APIKey, provider *gatewaycapture.ExecutionProvider, sub *billing.UserSubscription, model string, failed bool, body []byte, fields routing.PricingUsageFields, hash string, compact ...bool) bool {
			return support.RecordCyberPolicyIfMarked(c, key, provider, sub, model, failed, body, fields, hash, compact...)
		},
		submitOpenAIUsageRecordTask: func(c *gin.Context, result *forwardcore.OpenAIResult, task completion.UsageRecordTask) {
			images := 0
			if result != nil {
				images = result.ImageCount
			}
			support.Submission.SubmitImages(c, images, task)
		},
	}
	return &Runtime{dependencies: d}
}
func (b *responsesAttemptBridge) binding() *openAIExecutionDependencies { return b.fixed }
