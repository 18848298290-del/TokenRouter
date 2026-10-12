package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Messages HTTP admission uses request-scoped Key copies when calling routing,
// reasoning, moderation and funding services. Context setters install parsed
// client, probe, thinking and route information for downstream selection,
// while an absent isolation callback permits admission. A configured error
// service is bound for this request; missing binding input retains existing
// request state.

// MessagesBindings provides routing, funding, isolation, and moderation dependencies.
type MessagesBindings struct {
	PlanRoute           func(context.Context, *apikey.APIKey, string) routing.RoutePlan // request route planning callback
	ClientVersions      func(context.Context) (string, string)                          // runtime client version bounds
	ClientGroupFallback ClientGroupFallbackResolver                                     // authorized client group resolver
	Funding             interface {
		// CheckKey validates the supplied Key and optional subscription for funding
		// admission. Its final bool selects skipped RPM counting; this backend
		// passes false with an empty model and returns the admission error.
		CheckKey(context.Context, *apikey.APIKey, *billing.UserSubscription, string, bool) error
	} // request funding admission service
	Moderation ModerationPort // content moderation service
	// Errors is optional; nil makes BindErrors retain existing request state.
	// Protocol defaults apply when no service has been bound by another caller.
	Errors *errorpolicy.ErrorPassthroughService
	// IsolateSession is optional; an absent callback permits session admission.
	// A configured callback receives a copied key and the gateway isolation source.
	IsolateSession func(context.Context, *apikey.APIKey, int64, string, string) error
	CachedSession  func(context.Context, *int64, string) (int64, error) // sticky provider lookup callback
	// ObserveCompatibility runs when Messages returns after authenticated request
	// logging is created, including later admission errors; that path requires a callback.
	ObserveCompatibility func(*zap.Logger)
}

// messagesHTTPBackend adapts bound admission services to request-local HTTP
// state.
type messagesHTTPBackend struct {
	bindings MessagesBindings // shared admission service bindings
}

// NewBoundMessagesHandler creates HTTP admission with the supplied binding set
// and executor. The backend retains those service pointers/callbacks, while
// the handler constructs per-request route, client and session state for
// execution.
func NewBoundMessagesHandler(options MessagesHTTPOptions, bindings MessagesBindings, prompt MessagesPrompt, concurrency *ConcurrencyHelper, executor execution.Executor) *MessagesHandler {
	return NewMessagesHandler(options, messagesHTTPBackend{bindings}, prompt, concurrency, executor)
}

// Access prefers the effective request Key already installed for routing.
// Otherwise it reads middleware authentication and returns a Key copy with its
// presence flag, leaving missing authentication for the handler to reject.
// A stored typed nil can return true; the caller requires middleware/effective
// key state that supplies a usable key before Messages reads its identity.
func (p messagesHTTPBackend) Access(c *gin.Context) (*apikey.APIKey, bool) {
	if key, ok := EffectiveAPIKey(c); ok {
		return key, true
	}
	// Middleware authentication remains the fallback when no effective route key
	// exists; copy that key before request-specific routing can replace its state.
	key, ok := keyhttp.GetAPIKeyFromContext(c)
	return apikey.CopyAPIKey(key), ok
}

// CompatibilityMetrics invokes the configured request-compatibility observer
// with the current logger. The bound callback is required on Messages return
// paths after authentication has established request logging, including
// admission failures.
func (p messagesHTTPBackend) CompatibilityMetrics(log *zap.Logger) {
	p.bindings.ObserveCompatibility(log)
}

// ObserveRequest stores the client model and stream intent in HTTP Ops state.
func (p messagesHTTPBackend) ObserveRequest(c *gin.Context, model string, stream bool) {
	SetOpsRequestContext(c, model, stream)
}

// ObserveEndpoint stores the normalized endpoint and legacy streaming request
// category for Ops.
func (p messagesHTTPBackend) ObserveEndpoint(c *gin.Context, stream bool) {
	SetOpsEndpointContext(c, "", int16(usage.RequestTypeFromLegacy(stream, false)))
}

// Reasoning applies request-specific Anthropic reasoning policy using a Key
// copy. It returns the possibly rewritten bytes, changed flag and any policy
// error, which admission handles before provider execution begins.
func (p messagesHTTPBackend) Reasoning(c *gin.Context, key *apikey.APIKey, body []byte) ([]byte, bool, error) {
	return ApplyAnthropicReasoningEffortPolicyForRequest(c, apikey.CopyAPIKey(key), body)
}

// PolicyDenied marks request-local policy refusal as an Ops business
// limitation.
func (p messagesHTTPBackend) PolicyDenied(c *gin.Context) {
	MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
}

// Plan resolves request routing through the configured planner using a copied
// Key. The returned plan retains the caller's original model and effective
// group mapping for downstream execution and completion.
func (p messagesHTTPBackend) Plan(ctx context.Context, key *apikey.APIKey, model string) routing.RoutePlan {
	old := apikey.CopyAPIKey(key)
	return p.bindings.PlanRoute(ctx, old, model)
}

// BindPlan replaces the HTTP request context with the supplied route plan
// installed. Existing context values remain available, and later execution
// reads the plan rather than repeating group/model planning in the handler.
func (p messagesHTTPBackend) BindPlan(c *gin.Context, plan routing.RoutePlan) {
	c.Request = c.Request.WithContext(requeststate.WithRoutePlan(c.Request.Context(), plan))
}

// BindProbe sets the one-token Haiku marker on the request context for client
// detection. Its true value lets the detection path interpret the already
// recognized probe independently of ordinary request validation.
func (p messagesHTTPBackend) BindProbe(c *gin.Context) {
	c.Request = c.Request.WithContext(requeststate.WithIsMaxTokensOneHaikuRequest(c.Request.Context(), true))
}

// Probe reads the request's one-token Haiku marker for client detection. A
// missing marker returns false, leaving the ordinary detection path active
// rather than treating an unmarked request as a probe.
func (p messagesHTTPBackend) Probe(c *gin.Context) bool {
	probe, _ := requeststate.IsMaxTokensOneHaikuRequestFromContext(c.Request.Context())
	return probe
}

// BindClient stores the detected Claude client flag in the HTTP request
// context. A nonempty version updates version state only for a detected Claude
// client; other detections retain any existing version value.
func (p messagesHTTPBackend) BindClient(c *gin.Context, d ClientDetection) {
	ctx := requeststate.SetClaudeCodeClient(c.Request.Context(), d.ClaudeCode)
	// Update version state only for a usable Claude detection so an unrelated
	// client or missing version does not erase an already installed version.
	if d.ClaudeCode && d.Version != "" {
		ctx = requeststate.SetClaudeCodeVersion(ctx, d.Version)
	}
	c.Request = c.Request.WithContext(ctx)
}

// BindThinking stores parsed thinking state for the subsequent platform
// execution. The request context is replaced with that flag installed,
// allowing Antigravity model identity and health handling to read the same
// parsed intent.
func (p messagesHTTPBackend) BindThinking(c *gin.Context, thinking bool) {
	c.Request = c.Request.WithContext(requeststate.WithThinkingEnabled(c.Request.Context(), thinking))
}

// ClientVersion returns the Claude client version stored on this request
// context. An absent value is empty; the HTTP handler applies its current
// bounds and selects any resulting rejection response.
func (p messagesHTTPBackend) ClientVersion(c *gin.Context) string {
	return requeststate.GetClaudeCodeVersion(c.Request.Context())
}

// ClientVersionBounds invokes the configured runtime version-settings reader
// with request context. It returns independent lower and upper strings, which
// Messages treats as disabled when their corresponding value is empty.
func (p messagesHTTPBackend) ClientVersionBounds(ctx context.Context) (string, string) {
	return p.bindings.ClientVersions(ctx)
}

// Moderate submits rewritten Messages content with the authenticated subject
// and a copied Key. It delegates to the shared moderation runner, returning
// its optional decision so admission can interpret blocked output.
func (p messagesHTTPBackend) Moderate(c *gin.Context, log *zap.Logger, key *apikey.APIKey, subject authctx.AuthSubject, model string, body []byte) *moderation.Decision {
	return RunContentModeration(GatewayModerationEndpoints{}, c, log, p.bindings.Moderation, apikey.CopyAPIKey(key), subject, moderation.ContentModerationProtocolAnthropicMessages, model, body)
}

// BindErrors installs the shared final-response rule service on the current
// HTTP request. A nil configured service leaves current binding state
// unchanged; ordinary defaults apply when the request has no bound service.
func (p messagesHTTPBackend) BindErrors(c *gin.Context) {
	if p.bindings.Errors != nil {
		BindErrorPassthroughService(c, p.bindings.Errors)
	}
}

// Eligibility checks funding using a copied Key and optional request
// subscription. It passes an empty model and skip-RPM=false to the bound
// funding service, leaving final-model pricing to the execution path.
func (p messagesHTTPBackend) Eligibility(ctx context.Context, key *apikey.APIKey, sub *billing.UserSubscription) error {
	old := apikey.CopyAPIKey(key)
	return p.bindings.Funding.CheckKey(ctx, old, sub, "", false)
}

// ForcedPlatform returns any forced platform installed by API-key middleware.
// The presence flag distinguishes an unrestricted request; Messages uses a
// forced Gemini value to prepare its separate execution body and kind.
func (p messagesHTTPBackend) ForcedPlatform(c *gin.Context) (string, bool) {
	return keyhttp.GetForcePlatformFromContext(c)
}

// Isolate delegates gateway session ownership checking with a copied Key and
// subject ID. An absent isolation callback returns nil, while configured
// errors are passed to admission for conflict or availability presentation.
func (p messagesHTTPBackend) Isolate(ctx context.Context, key *apikey.APIKey, userID int64, hash string) error {
	if p.bindings.IsolateSession == nil {
		return nil
	}
	return p.bindings.IsolateSession(ctx, apikey.CopyAPIKey(key), userID, session.SessionIsolationSourceGateway, hash)
}

// CachedSession delegates group-scoped sticky provider lookup to the bound
// cache reader. It returns that provider identity and error unchanged,
// allowing the HTTP handler to apply its advisory lookup policy.
func (p messagesHTTPBackend) CachedSession(ctx context.Context, groupID *int64, hash string) (int64, error) {
	return p.bindings.CachedSession(ctx, groupID, hash)
}

// Prefetch stores the sticky provider/group pair on the HTTP request context.
// Later selection reads these hints for this attempt while existing request
// values and its previously installed route plan remain available.
func (p messagesHTTPBackend) Prefetch(c *gin.Context, providerID, groupID int64) {
	c.Request = c.Request.WithContext(requeststate.WithPrefetchedStickySession(c.Request.Context(), providerID, groupID))
}

// PrepareGemini delegates group-attempt preparation using retained
// parsed/body/model data and a copied Key. It returns the prepared request and
// error while discarding the separate mapping result already represented by
// admission's route plan.
func (p messagesHTTPBackend) PrepareGemini(ctx context.Context, call MessagesCall) (*requeststate.ParsedRequest, error) {
	// The route plan already captures group mapping for completion; retain only
	// the prepared request and error from this second representation-building step.
	parsed, _, err := PrepareGroupAttempt(ctx, call.Parsed, call.Body, apikey.CopyAPIKey(call.Key), call.Model, p.bindings.PlanRoute)
	return parsed, err
}

// MarkStream records the supplied protocol-stream failure in request-local Ops
// state.
func (p messagesHTTPBackend) MarkStream(c *gin.Context, kind, message string, status int) {
	MarkOpsStreamError(c, kind, message, status)
}

// FailoverObservation forwards supplied provider-switch diagnostic values to
// request telemetry.
func (p messagesHTTPBackend) FailoverObservation(ctx context.Context, event string, values map[string]any) {
	telemetry.Failover(ctx, event, values)
}
