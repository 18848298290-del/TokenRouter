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

// bindings.go binds shared forwarding, scheduling and completion services for
// text attempts.
// Text attempts invoke shared platform callbacks for the provider already
// selected by the text loop. Runtime construction binds HTTP error output, slot
// helpers, moderation, and completion submission once; Open then creates the
// request-local state that uses those references during serial provider
// attempts.

// ForwardPorts binds synchronous selected-provider execution and request
// preparation callbacks.
type ForwardPorts struct {
	// EnforceOpenAIClientPolicyForRequest checks the selected client's
	// eligibility before forwarding. A failure can write HTTP output and
	// ends this attempted exchange.
	EnforceOpenAIClientPolicyForRequest func(ctx context.Context, c *gin.Context, provider *gatewaycapture.ExecutionProvider, body []byte, tlsRouterMatch egress.TLSFingerprintRouterMatchResult) error
	// Forward executes a selected Responses provider and returns
	// result/error observations for the text loop's completion and recovery
	// decisions.
	Forward func(ctx context.Context, c *gin.Context, provider *gatewaycapture.ExecutionProvider, body []byte) (*forwardcore.OpenAIResult, error)
	// ForwardAsAnthropic executes selected-provider Messages output using
	// the request's prompt-cache identity and already derived
	// provider-layer model.
	ForwardAsAnthropic func(ctx context.Context, c *gin.Context, provider *gatewaycapture.ExecutionProvider, body []byte, promptCacheKey, defaultMappedModel string, tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult) (*forwardcore.OpenAIResult, error)
	// ForwardAsChatCompletions executes selected-provider Chat output with
	// the supplied prompt-cache identity. Its result can contain usage
	// despite an execution error.
	ForwardAsChatCompletions func(
		ctx context.Context,
		c *gin.Context,
		provider *gatewaycapture.ExecutionProvider,
		body []byte,
		promptCacheKey string,
		defaultMappedModel string,
		tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult,
	) (*forwardcore.OpenAIResult, error)
	// MatchOpenAITLSFingerprintRouterForRequest resolves the chosen
	// provider's TLS route from current HTTP client metadata before request
	// construction.
	MatchOpenAITLSFingerprintRouterForRequest func(c *gin.Context, provider *gatewaycapture.ExecutionProvider) egress.TLSFingerprintRouterMatchResult
	// ReplaceModelInBody prepares model-rewritten request bytes. The
	// request- local mapped-body cache retains returned storage and its
	// consumers treat those bytes as immutable.
	ReplaceModelInBody func(body []byte, newModel string) []byte
}

// SelectionPorts binds compatible selection, scheduling feedback, and quota
// observations.
type SelectionPorts struct {
	// image capability selector callback
	SelectImages func(context.Context, *int64, string, string, map[int64]struct{}, providercore.OpenAIImagesCapability) (*gatewaycapture.SelectionResult, scheduler.PlatformDecision, error)
	// provider switch metric callback
	RecordSwitch func()
	// selection-specific schedule feedback callback
	ReportSelection func(*gatewaycapture.SelectionResult, int64, string, bool, *int)

	// ObserveOpenAIProviderHealthFailure records classified
	// selected-provider failure observations. Retry readiness invokes it
	// when output has already ended replay eligibility.
	ObserveOpenAIProviderHealthFailure func(ctx context.Context, provider *gatewaycapture.ExecutionProvider, observedErr error) bool
	// RecordOpenAIProviderSwitchForSelection records a provider switch
	// using the captured selection. Switched invokes it after abandoning
	// the prior provider's session registration.
	RecordOpenAIProviderSwitchForSelection func(selection *gatewaycapture.SelectionResult)
	// ReportOpenAIProviderScheduleResult reports model-specific success or
	// failure with optional first-token latency. Callers supply the final
	// scheduling-model identity for the current attempt.
	ReportOpenAIProviderScheduleResult func(providerOrID *gatewaycapture.ExecutionProvider, model string, success bool, firstTokenMs *int, observedErr ...error) bool
	// SelectProviderWithSchedulerForCapability resolves the requested model
	// under capability and transport constraints. Returned acquisition or
	// wait metadata is consumed by the slot adapter.
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
	// SelectProviderWithSchedulerForCapabilityAndRoutingModel separates
	// client and routed models for Messages selection. Its returned target
	// and resource metadata belong to the current serial attempt.
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
	// UpdateCodexUsageSnapshotFromHeaders observes successful nonshadow
	// OAuth response headers. It updates provider quota snapshots
	// independently of generation completion settlement.
	UpdateCodexUsageSnapshotFromHeaders func(ctx context.Context, providerID int64, headers http.Header)
}

// Bindings supplies app-owned services and platform callbacks for text-attempt
// runtime construction.
type Bindings struct {
	// authorized group switch callbacks
	Fallback GroupFallbackPorts
	// request session tracking callbacks
	Sessions SessionPorts
	// selected platform forwarding callbacks
	Forward ForwardPorts
	// provider selection and feedback callbacks
	Selection SelectionPorts
	// shared HTTP attempt helpers
	Support *Support
	// shared usage completion recorder
	Recorder *completion.Recorder
	// compatibility model availability reader
	Diagnoser routing.ModelAvailabilityDiagnoser
	// routed model availability reader
	ResolvedDiagnoser routing.ModelAvailabilityDiagnoser
}

// openAIExecutionDependencies retains the callbacks and shared services
// consumed by attempt bridges.
type openAIExecutionDependencies struct {
	// authorized destination group callbacks
	fallback GroupFallbackPorts
	// request session lifecycle callbacks
	sessions SessionPorts
	// shared usage completion recorder
	recorder *completion.Recorder
	// effective Key quota updater
	apiKeyService gatewaycapture.QuotaUpdater
	// compatible model availability reader
	diagnoser routing.ModelAvailabilityDiagnoser
	// routed model availability reader
	resolvedDiagnoser routing.ModelAvailabilityDiagnoser
	// selected client eligibility callback
	enforceOpenAIClientPolicyForRequest func(ctx context.Context, c *gin.Context, provider *gatewaycapture.ExecutionProvider, body []byte, tlsRouterMatch egress.TLSFingerprintRouterMatchResult) error
	// selected Responses exchange callback
	forward func(ctx context.Context, c *gin.Context, provider *gatewaycapture.ExecutionProvider, body []byte) (*forwardcore.OpenAIResult, error)
	// selected Messages exchange callback
	forwardAsAnthropic func(ctx context.Context, c *gin.Context, provider *gatewaycapture.ExecutionProvider, body []byte, promptCacheKey, defaultMappedModel string, tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult) (*forwardcore.OpenAIResult, error)
	// selected Chat exchange callback
	forwardAsChatCompletions func(
		ctx context.Context,
		c *gin.Context,
		provider *gatewaycapture.ExecutionProvider,
		body []byte,
		promptCacheKey string,
		defaultMappedModel string,
		tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult,
	) (*forwardcore.OpenAIResult, error)
	// selected TLS routing matcher
	matchOpenAITLSFingerprintRouterForRequest func(c *gin.Context, provider *gatewaycapture.ExecutionProvider) egress.TLSFingerprintRouterMatchResult
	// post-output failure health observer
	observeOpenAIProviderHealthFailure func(ctx context.Context, provider *gatewaycapture.ExecutionProvider, observedErr error) bool
	// captured selection switch observer
	recordOpenAIProviderSwitchForSelection func(selection *gatewaycapture.SelectionResult)
	// mapped request byte replacer
	replaceModelInBody func(body []byte, newModel string) []byte
	// model-specific scheduling result observer
	reportOpenAIProviderScheduleResult func(providerOrID *gatewaycapture.ExecutionProvider, model string, success bool, firstTokenMs *int, observedErr ...error) bool
	// requested-model capability selection callback
	selectProviderWithSchedulerForCapability func(
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
	// routed-model capability selection callback
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
	// successful OAuth quota-header observer
	updateCodexUsageSnapshotFromHeaders func(ctx context.Context, providerID int64, headers http.Header)
	// acquireResponsesProviderSlot owns selected capacity admission and
	// returns its cleanup. Forward invokes the returned cleanup before
	// reporting outcomes to the text loop.
	acquireResponsesProviderSlot func(
		c *gin.Context,
		groupID *int64,
		sessionHash string,
		selection *gatewaycapture.SelectionResult,
		reqStream bool,
		streamStarted *bool,
		reqLog *zap.Logger,
	) (func(), bool)
	// Messages streaming-aware error writer
	anthropicStreamingAwareError func(c *gin.Context, status int, errType, message string, streamStarted bool)
	// deriveOpenAIForwardAttemptBody derives current-provider input from
	// canonical bytes and request-local passthrough history. Its caller
	// treats returned bytes as immutable.
	deriveOpenAIForwardAttemptBody func(
		reqLog *zap.Logger,
		canonicalBody []byte,
		provider *gatewaycapture.ExecutionProvider,
		state *openAIPassthroughFailoverState,
	) []byte
	// missing Messages error supplier
	ensureAnthropicErrorResponse func(c *gin.Context, streamStarted bool) bool
	// missing OpenAI error supplier
	ensureOpenAIForwardErrorResponse func(c *gin.Context, streamStarted bool, err error) bool
	// classified stream-read error supplier
	ensureOpenAIStreamReadErrorResponse func(c *gin.Context, err error, streamStarted bool) bool
	// Messages terminal recovery writer
	handleAnthropicFailoverExhausted func(c *gin.Context, failoverErr *forwardcore.UpstreamFailoverError, streamStarted bool)
	// OpenAI terminal recovery writer
	handleFailoverExhausted func(c *gin.Context, failoverErr *forwardcore.UpstreamFailoverError, streamStarted bool)
	// status-only terminal recovery writer
	handleFailoverExhaustedSimple func(c *gin.Context, statusCode int, streamStarted bool)
	// local selection business-error writer
	handleOpenAISelectionBusinessError func(c *gin.Context, err error, streamStarted bool) bool
	// OpenAI streaming-aware error writer
	handleStreamingAwareError func(c *gin.Context, status int, errType, message string, streamStarted bool)
	// recordCyberPolicyIfMarked captures current Cyber evidence and
	// completion inputs before policy recording. It returns whether that
	// request-level policy path handled the mark.
	recordCyberPolicyIfMarked func(c *gin.Context, apiKey *apikey.APIKey, provider *gatewaycapture.ExecutionProvider, subscription *billing.UserSubscription, model string, forwardErrored bool, cyberBlockArg []byte, pricingFields routing.PricingUsageFields, requestPayloadHash string, nativeCompaction ...bool) bool
	// upstream Cyber warning recorder
	recordOpenAICyberWarning func(c *gin.Context, reqLog *zap.Logger, apiKey *apikey.APIKey, provider *gatewaycapture.ExecutionProvider, model string, statusCode int, responseBody []byte, warningText string)
	// recordOpenAIForwardErrorCyberWarning reports warning presence in an
	// error chain. Its true result does not establish durable recording
	// success.
	recordOpenAIForwardErrorCyberWarning func(c *gin.Context, reqLog *zap.Logger, apiKey *apikey.APIKey, provider *gatewaycapture.ExecutionProvider, model string, statusCode int, err error) bool
	// submitOpenAIUsageRecordTask receives a pre-captured usage task and
	// current image count. The bound Submission service decides queueing
	// and fallback execution.
	submitOpenAIUsageRecordTask func(c *gin.Context, result *forwardcore.OpenAIResult, task completion.UsageRecordTask)
}

// New binds shared attempt operations and HTTP helpers into a reusable runtime.
// Callers supply a nonnil Support and the callbacks required by their execution
// paths. The copied binding values retain shared service references; Open
// creates fresh request state, and completion submission uses Support's
// image-count policy.
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
			// Completion has captured request values before
			// reaching this callback. Use observed image count to
			// select Submission's queue and fallback policy for
			// that task.
			support.Submission.SubmitImages(c, images, task)
		},
	}
	return &Runtime{dependencies: d}
}

// binding returns the shared execution dependencies installed on this request
// bridge.
func (b *responsesAttemptBridge) binding() *openAIExecutionDependencies { return b.fixed }
