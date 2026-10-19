package mediaentry

import (
	"context"
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// media_http_adapter.go binds media HTTP admission and request factories to
// shared runtime services and effective key state.
// Media HTTP admission reads authenticated Key and subject data while the
// Runtime retains shared application services. Route planning produces a
// context carrying group mapping; image entry installs it immediately, while
// Grok entry receives the planned context alongside rewritten forwarding
// bytes. Per-request factories retain the source identity used for
// completion.
//
// A model-free composite video lookup can restore a task's saved Key view and
// provider. The adapter publishes that view to authentication context, Ops
// fallback metadata and request group state together after successful
// ownership resolution, so subsequent selection and logging refer to the same
// group. Funding and explicit-session checks then use the request's current
// Key view. The selected platform attempt keeps credential-bearing provider
// state inside its request adapter.

// mediaHTTPAdapter binds shared Runtime services to one media HTTP admission
// interface.
type mediaHTTPAdapter struct {
	h *Runtime // shared application media bindings
}

// MediaHTTPHandler constructs a media route handler using this Runtime's
// shared bindings.
func (h *Runtime) MediaHTTPHandler() *gatewayhttp.MediaHandler {
	return gatewayhttp.NewMediaHandler(mediaHTTPAdapter{h})
}

// mediaAccessView builds media admission facts from a Key, returning nil for
// nil input. GroupID is copied as a scalar pointer; image permission requires
// a hydrated group whose image feature is enabled.
func mediaAccessView(key *apikey.APIKey) *gatewayhttp.MediaAccess {
	if key == nil {
		return nil
	}
	var group *int64
	if key.GroupID != nil {
		v := *key.GroupID
		group = &v
	}
	return &gatewayhttp.MediaAccess{HasGroup: key.Group != nil, ID: key.ID, GroupID: group, Composite: key.IsComposite, ImagesAllowed: key.Group != nil && key.Group.AllowImageGeneration}
}

// Access reads the authenticated middleware Key and converts its media
// admission view. The middleware presence flag is returned directly; callers
// still require a usable view when continuing entry admission.
func (p mediaHTTPAdapter) Access(c *gin.Context) (*gatewayhttp.MediaAccess, bool) {
	key, ok := keyhttp.GetAPIKeyFromContext(c)
	return mediaAccessView(key), ok
}

// Subject returns authenticated user identity and concurrency with the
// middleware presence flag. False tells the media entry to present its
// missing-user-context error before resource admission and provider
// selection.
func (p mediaHTTPAdapter) Subject(c *gin.Context) (gatewayhttp.MediaSubject, bool) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	return gatewayhttp.MediaSubject{UserID: subject.UserID, Concurrency: subject.Concurrency}, ok
}

// Logger returns a request logger carrying the component and supplied fields.
func (p mediaHTTPAdapter) Logger(c *gin.Context, name string, fields ...zap.Field) *zap.Logger {
	return gatewayhttp.RequestLogger(c, name, fields...)
}

// Dependencies checks the retained entry services before HTTP body
// consumption begins. False means Ensure has presented its
// unavailable-service response; a nil logger is permitted by Voice and
// Realtime callers.
func (p mediaHTTPAdapter) Dependencies(c *gin.Context, log *zap.Logger) bool {
	return p.h.bindings.Dependencies.Ensure(c, log)
}

// Error presents a classified client error for the current request endpoint.
// SystemOne paths use their dedicated envelope; other paths use the shared
// OpenAI error writer with the supplied status and message.
func (p mediaHTTPAdapter) Error(c *gin.Context, status int, code, message string) {
	if gatewayhttp.IsSystemOneEndpoint(c.Request.URL.Path) {
		gatewayhttp.WriteSystemOneError(c, status, code, "", "", message)
		return
	}
	gatewayhttp.DefaultOpenAIErrorOutput().WriteError(c, status, code, message)
}

// StreamingError delegates error presentation using the supplied stream
// startup state. A started stream uses the shared stream error path, while
// unstarted output follows the ordinary OpenAI HTTP response rules.
func (p mediaHTTPAdapter) StreamingError(c *gin.Context, status int, code, message string, stream bool) {
	gatewayhttp.DefaultOpenAIErrorOutput().StreamError(c, status, code, message, stream)
}

// EnsureForwardError requests a generic terminal response when current output
// permits it. True reports attempted presentation for panic diagnostics,
// while accepted response bytes remain controlled by Gin's underlying writer.
func (p mediaHTTPAdapter) EnsureForwardError(c *gin.Context, stream bool) bool {
	return gatewayhttp.DefaultOpenAIErrorOutput().EnsureFallback(c, stream)
}

// ObserveRequest records model and streaming metadata for the current HTTP
// entry. When endpoint is true, it also records the legacy request type
// derived from the stream flag for Ops endpoint diagnostics.
func (p mediaHTTPAdapter) ObserveRequest(c *gin.Context, model string, stream, endpoint bool) {
	gatewayhttp.SetOpsRequestContext(c, model, stream)
	if endpoint {
		gatewayhttp.SetOpsEndpointContext(c, "", int16(usage.RequestTypeFromLegacy(stream, false)))
	}
}

// AuthLatency stores elapsed media admission time in whole milliseconds for
// Ops.
func (p mediaHTTPAdapter) AuthLatency(c *gin.Context, elapsed time.Duration) {
	// Ops latency fields store whole milliseconds, truncating any submillisecond remainder.
	gatewayhttp.SetOpsLatencyMs(c, gatewayhttp.OpsAuthLatencyMsKey, elapsed.Milliseconds())
}

// Plan resolves the current Key's group route and returns its context with
// mapping. A true bind installs the route context on c.Request; false returns
// it for the caller's later execution.
func (p mediaHTTPAdapter) Plan(c *gin.Context, model string, bind bool) (context.Context, routing.GroupMappingResult) {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	plan := p.h.bindings.PlanRoute(c.Request.Context(), key, model)
	ctx := requeststate.WithRoutePlan(c.Request.Context(), plan)
	if bind {
		c.Request = c.Request.WithContext(ctx)
	}
	return ctx, plan.Mapping()
}

// ImagePermissionMessage returns the shared client wording for denied media
// generation.
func (p mediaHTTPAdapter) ImagePermissionMessage() string {
	return media.ImageGenerationPermissionMessage
}

// ImagePolicyDenied records the image feature gate as an Ops business
// limitation.
func (p mediaHTTPAdapter) ImagePolicyDenied(c *gin.Context) {
	gatewayhttp.MarkOpsClientBusinessLimited(c, gatewayhttp.OpsClientBusinessLimitedReasonLocalFeatureGate)
}

// Moderate checks image content using the complete subject retained in
// authentication context. The supplied MediaSubject is unused; a blocked
// decision writes its classified OpenAI error and returns true to stop entry.
func (p mediaHTTPAdapter) Moderate(c *gin.Context, log *zap.Logger, subject gatewayhttp.MediaSubject, model string, body []byte) bool {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	oldSubject, _ := authctx.GetAuthSubjectFromContext(c)
	decision := p.h.checkContentModeration(c, log, key, oldSubject, moderation.ContentModerationProtocolOpenAIImages, model, body)
	if decision == nil || !decision.Blocked {
		return false
	}
	gatewayhttp.DefaultOpenAIErrorOutput().WriteError(c, gatewayhttp.ContentModerationStatus(decision), gatewayhttp.ContentModerationErrorCode(decision), decision.Message)
	return true
}

// CyberSnapshot retains image-protocol input for later upstream Cyber warning
// recording.
func (p mediaHTTPAdapter) CyberSnapshot(c *gin.Context, body []byte) {
	gatewayhttp.SetOpenAICyberWarningRequestSnapshot(c, moderation.ContentModerationProtocolOpenAIImages, body)
}

// AcquireImage requests local image capacity and delegates its rejection
// presentation. Disabled or absent image admission can return nil cleanup
// with success; callers release any returned cleanup at request exit.
func (p mediaHTTPAdapter) AcquireImage(c *gin.Context, stream bool) (func(), bool) {
	return p.h.bindings.Resources.AcquireImage(c, stream)
}

// AcquireUser obtains user capacity through the shared HTTP resource manager.
// It can update the shared startup flag during streaming waits; success
// returns cleanup that the caller retains through request exit.
func (p mediaHTTPAdapter) AcquireUser(c *gin.Context, s gatewayhttp.MediaSubject, stream bool, started *bool, log *zap.Logger) (func(), bool) {
	return p.h.bindings.Resources.AcquireUser(c, s.UserID, s.Concurrency, stream, started, log)
}

// BindErrors installs configured error passthrough rules when the Runtime
// supplies them. A nil rules service leaves request error presentation at its
// existing configuration, so optional binding does not stop admission.
func (p mediaHTTPAdapter) BindErrors(c *gin.Context) {
	if p.h.bindings.Common.Support.Rules != nil {
		gatewayhttp.BindErrorPassthroughService(c, p.h.bindings.Common.Support.Rules)
	}
}

// Billing checks funding and RPM policy using the current Key and
// subscription. It passes afterWait=false to checkFunding; nil permits
// continuation, while a classified failure lets the HTTP entry present its
// retry delay.
func (p mediaHTTPAdapter) Billing(c *gin.Context) *gatewayhttp.MediaHTTPFailure {
	return p.checkFunding(c, false)
}

// checkFunding returns the funding check's client classification and
// underlying cause, or nil on success. afterWait=true selects a funding-only
// recheck; false includes RPM policy through the bound CheckFunding callback,
// so callers choose according to their admission phase.
func (p mediaHTTPAdapter) checkFunding(c *gin.Context, afterWait bool) *gatewayhttp.MediaHTTPFailure {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	subscription, _ := gatewayhttp.SubscriptionFromContext(c)
	err := p.h.bindings.CheckFunding(c.Request.Context(), key, subscription, "", afterWait)
	if err == nil {
		return nil
	}
	status, code, message, retry := gatewayhttp.BillingErrorDetails(err)
	return &gatewayhttp.MediaHTTPFailure{Status: status, Code: code, Message: message, RetryAfter: retry, Err: err}
}

// ExplicitSession derives selection identity from explicit OpenAI request
// hints using the supplied body. Missing explicit identity returns an empty
// hash through GenerateExplicitOpenAISessionHash, letting image entry remain
// without sticky isolation.
func (p mediaHTTPAdapter) ExplicitSession(c *gin.Context, body []byte) string {
	return gatewayhttp.GenerateExplicitOpenAISessionHash(c, body)
}

// Isolate checks an explicit OpenAI hash under the current authenticated Key
// and user. Nil errors permit entry; other outcomes are presented through the
// shared stream-aware isolation handler and return true.
func (p mediaHTTPAdapter) Isolate(c *gin.Context, userID int64, hash string, stream bool) bool {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	err := p.h.bindings.Isolate(c.Request.Context(), key, userID, session.SessionIsolationSourceOpenAI, hash)
	return p.h.handleOpenAISessionIsolationError(c, err, stream)
}

// ImageContext marks the current request context with image generation intent
// and Images endpoint.
func (p mediaHTTPAdapter) ImageContext(c *gin.Context) context.Context {
	return requeststate.WithOpenAIImagesEndpoint(requeststate.WithOpenAIImageGenerationIntent(c.Request.Context()))
}

// NewGenerationPorts constructs one media attempt adapter with request-local
// authentication and supplied entry facts. It retains the parsed input,
// source body and shared stream flag; callers keep that storage stable across
// serial attempts.
func (p mediaHTTPAdapter) NewGenerationPorts(c *gin.Context, in gatewayhttp.GenerationHTTPInput, log *zap.Logger, stream *bool) media.GenerationPorts {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	subject, _ := authctx.GetAuthSubjectFromContext(c)
	subscription, _ := gatewayhttp.SubscriptionFromContext(c)
	return &generationRequestAdapter{grok: in.Grok, h: p.h, c: c, apiKey: key, subject: subject, subscription: subscription, reqLog: log, streamStarted: stream, parsed: in.Parsed, body: in.Body, requestModel: in.RequestModel, routingModel: in.RoutingModel, sessionHash: in.SessionHash, groupMapping: in.Mapping, endpoint: grok.GrokMediaEndpoint(in.Endpoint), requestID: in.RequestID, contentType: in.ContentType, boundProviderID: in.BoundProviderID, videoCreated: in.VideoCreated}
}

// MaxSwitches returns this Runtime's configured media provider transition
// allowance.
func (p mediaHTTPAdapter) MaxSwitches() int { return p.h.bindings.Options.MaxSwitches }

// ParseGrok returns the codec's parsed model, video resolution, and moderation
// bytes for entry validation. Generation callers require a usable model before execution;
// model-free video lookup can retain empty body-derived data for its saved
// owner.
func (p mediaHTTPAdapter) ParseGrok(contentType string, body []byte) gatewayhttp.GrokMediaInput {
	value := gatewayadapter.GrokMediaCodec().ParseGrokMediaRequest(contentType, body)
	return gatewayhttp.GrokMediaInput{Model: value.Model, VideoResolution: value.Resolution, ModerationBody: value.ModerationBody()}
}

// ResolveCompositeVideo restores a saved video's Key view and provider for
// its authenticated user. It returns ownership errors or unusable identity
// before changing context. A usable Key and positive provider are required
// for the publication path.
//
// The restored Key is installed in authentication context and Ops fallback
// state, and its group is installed on the request context. These updates
// keep subsequent HTTP admission, provider selection and diagnostics attached
// to the same saved video owner. The returned MediaAccess uses a scalar
// GroupID copy while the retained request Key supplies complete execution
// identity.
func (p mediaHTTPAdapter) ResolveCompositeVideo(c *gin.Context, requestID string, userID int64) (*gatewayhttp.MediaAccess, int64, error) {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	key, provider, err := p.h.resolveCompositeGrokVideoAPIKey(c.Request.Context(), key, requestID, userID)
	if err != nil || key == nil || provider <= 0 {
		return mediaAccessView(key), provider, err
	}
	// Publish all request views after ownership resolution succeeds, so routing
	// and failure diagnostics observe the same saved group and authenticated Key.
	c.Set(string(keyhttp.ContextKeyAPIKey), key)
	keyhttp.SetOpsFallbackAPIKey(c, key)
	c.Request = c.Request.WithContext(requeststate.WithGroup(c.Request.Context(), key.Group))
	return mediaAccessView(key), provider, nil
}

// ResolveVideoProvider reads saved task ownership for the supplied user, Key
// and group. It returns the retained provider ID or storage error; the HTTP
// caller requires a positive ID before forwarding lookup.
func (p mediaHTTPAdapter) ResolveVideoProvider(ctx context.Context, groupID *int64, id string, userID, keyID int64) (int64, error) {
	return p.h.bindings.VideoTasks().ResolveGrokMediaVideoRequestProvider(ctx, groupID, id, userID, keyID)
}

// RewriteGrok delegates media model replacement and returns forwarding bytes
// with their content type. Callers handle the codec error before execution
// and keep both returned values paired when constructing the platform
// attempt.
func (p mediaHTTPAdapter) RewriteGrok(body []byte, contentType, model string) ([]byte, string, error) {
	return gatewayadapter.GrokMediaCodec().RewriteGrokMediaRequestModel(body, contentType, model)
}

// AuxiliaryHTTPHandler constructs auxiliary media routes using this Runtime's
// shared bindings.
func (h *Runtime) AuxiliaryHTTPHandler() *gatewayhttp.AuxiliaryHandler {
	return gatewayhttp.NewAuxiliaryHandler(mediaHTTPAdapter{h})
}

// HTTPTransport marks AlphaSearch diagnostics with the ordinary inbound HTTP
// transport.
func (p mediaHTTPAdapter) HTTPTransport(c *gin.Context) {
	gatewayhttp.SetOpenAIClientTransport(c, gatewayhttp.OpenAIClientTransportHTTP)
}

// ParseFailure logs rejected request bytes through shared body parse
// diagnostics.
func (p mediaHTTPAdapter) ParseFailure(log *zap.Logger, body []byte) {
	gatewayhttp.LogRequestBodyParseFailure(log, body, nil)
}

// RewriteModel returns the shared forward helper's top-level model
// replacement bytes.
func (p mediaHTTPAdapter) RewriteModel(body []byte, model string) []byte {
	return p.h.bindings.Common.Forward.ReplaceModelInBody(body, model)
}

// FallbackSession derives OpenAI selection identity with the supplied ID as
// fallback. The request context provides any explicit session hints, and no
// request body is supplied to this hash helper.
func (p mediaHTTPAdapter) FallbackSession(c *gin.Context, id string) string {
	return gatewayhttp.GenerateOpenAISessionHashWithFallback(c, nil, id)
}

// NewEmbeddings constructs request-local execution using current Key and
// subscription with the supplied user, model and group mapping. It retains
// the shared startup flag for admission errors across the media attempt loop.
func (p mediaHTTPAdapter) NewEmbeddings(c *gin.Context, in gatewayhttp.AuxiliaryHTTPInput, log *zap.Logger, stream *bool) gatewayhttp.EmbeddingHTTPExecution {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	subscription, _ := gatewayhttp.SubscriptionFromContext(c)
	return &embeddingRequestAdapter{h: p.h, c: c, apiKey: key, userID: in.Subject.UserID, subscription: subscription, reqModel: in.Model, groupMapping: in.Mapping, reqLog: log, streamStarted: stream}
}

// EndEmbeddingFailure delegates final Embeddings stage presentation after the
// media loop returns. A nil failure leaves successful output intact; nonnil
// values use request-local selection and output evidence in renderFailure.
func (p *embeddingRequestAdapter) EndEmbeddingFailure(f *media.EmbeddingFailure) { p.renderFailure(f) }

// NewAlphaSearch constructs request-local search execution retaining source
// bytes, current funding and supplied selection identity. Mapped forwarding
// bytes stay with the caller, while completion uses the original body stored
// in this adapter.
func (p mediaHTTPAdapter) NewAlphaSearch(c *gin.Context, in gatewayhttp.AuxiliaryHTTPInput, log *zap.Logger, stream *bool) gatewayhttp.AlphaHTTPExecution {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	subscription, _ := gatewayhttp.SubscriptionFromContext(c)
	return &alphaRequestAdapter{h: p.h, c: c, apiKey: key, subscription: subscription, groupMapping: in.Mapping, requestedModel: in.Model, originalBody: in.Body, userID: in.Subject.UserID, sessionHash: in.SessionHash, reqLog: log, streamStarted: stream}
}

// EndAlphaFailure delegates final AlphaSearch stage presentation after the
// media loop returns. A nil value leaves successful output intact; nonnil
// failures consult current request and selection state before attempting
// error output.
func (p *alphaRequestAdapter) EndAlphaFailure(f *media.AlphaFailure) { p.renderFailure(f) }

// ModerateVoice checks supplied TTS audit content using authenticated request
// identity and OpenAI Chat protocol. It uses grok-4.5 as the moderation model
// label; a blocked decision is presented before true ends Voice entry.
func (p mediaHTTPAdapter) ModerateVoice(c *gin.Context, log *zap.Logger, _ gatewayhttp.MediaSubject, body []byte) bool {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	subject, _ := authctx.GetAuthSubjectFromContext(c)
	decision := p.h.checkContentModeration(c, log, key, subject, moderation.ContentModerationProtocolOpenAIChat, "grok-4.5", body)
	if decision == nil || !decision.Blocked {
		return false
	}
	gatewayhttp.DefaultOpenAIErrorOutput().WriteError(c, gatewayhttp.ContentModerationStatus(decision), gatewayhttp.ContentModerationErrorCode(decision), decision.Message)
	return true
}

// NewVoice creates Grok Voice operations with current authentication,
// subscription and logger. The auxiliary input is unused; the Voice loop
// supplies its endpoint, body and content type when executing.
func (p mediaHTTPAdapter) NewVoice(c *gin.Context, _ gatewayhttp.AuxiliaryHTTPInput, log *zap.Logger) media.VoicePorts {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	subscription, _ := gatewayhttp.SubscriptionFromContext(c)
	return &grokVoiceAdapter{h: p.h, c: c, apiKey: key, subscription: subscription, reqLog: log}
}

// EndVoice presents classified Voice failover exhaustion or a no-provider
// failure. Nil leaves successful output intact; another terminal failure
// causes no additional HTTP write in this adapter, so its caller retains
// existing output.
func (p mediaHTTPAdapter) EndVoice(c *gin.Context, f *media.VoiceFailure) {
	if f == nil {
		return
	}
	if last, ok := errors.AsType[*forwardcore.UpstreamFailoverError](f.Last); ok {
		p.h.bindings.Common.Support.HandleFailoverExhausted(c, last, false)
	} else if f.NoProviders {
		gatewayhttp.DefaultOpenAIErrorOutput().WriteError(c, 503, "api_error", "No available Grok providers")
	}
}

// NewRealtime creates a request-local Grok admission and relay adapter with
// the authenticated Key.
func (p mediaHTTPAdapter) NewRealtime(c *gin.Context, log *zap.Logger) gatewayhttp.RealtimeHTTPExecution {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	return &grokRealtimeAdapter{h: p.h, c: c, apiKey: key, reqLog: log}
}

// RealtimeDialTimeout returns the shared default used to bound upstream
// Realtime dialing.
func (p mediaHTTPAdapter) RealtimeDialTimeout() time.Duration {
	return media.DefaultRealtimeDialTimeout
}
